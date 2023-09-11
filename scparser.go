package scparser

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Parse retrieves the source code of the specified function and its underlying functions
// within the Go module packages. It takes the package path and function name as input
// arguments and returns a formatted string containing the combined source code.
// The function will panic if the provided function is not found in the package path.
func Parse(funcPkgPath, funcName string, excludeRoot, codeOnly bool) string {
	// Change the working directory to the given package directory
	changeBack := changeDir(funcPkgPath)
	defer changeBack()

	funcSig, funcToFileAndPkg := initialize(funcName)

	p := newParser(funcToFileAndPkg)

	// Process the function and its underlying functions up to a depth of 5 (or 6 if root is excluded)
	if excludeRoot {
		p.processFunction(funcSig, 6)
	} else {
		p.processFunction(funcSig, 5)
	}

	return p.toString(excludeRoot, codeOnly)
}

type parser struct {
	// rootPkg is the root package of the Go module
	rootPkg *packages.Package

	// funcToFileAndPkg is a map that stores the file and package for each function signature
	funcToFileAndPkg map[*types.Signature]fileAndPkg

	// functions is a map of packages to their function source code
	functions map[*packages.Package]string

	// pkgOrder is an ordered list of processed packages to maintain the order of processing
	pkgOrder []*packages.Package

	// seen is a map to keep track of already processed functions
	seen map[*types.Signature]bool
}

// fileAndPkg is a struct that contains a pointer to an ast.File and a pointer to a packages.Package
type fileAndPkg struct {
	file *ast.File
	pkg  *packages.Package
}

func newParser(funcToFileAndPkg map[*types.Signature]fileAndPkg) *parser {
	return &parser{
		funcToFileAndPkg: funcToFileAndPkg,
		functions:        make(map[*packages.Package]string),
		seen:             make(map[*types.Signature]bool),
	}
}

// Convert functions into one string
func (p *parser) toString(excludeRoot, codeOnly bool) string {
	var result string
	for k, pkg := range p.pkgOrder {
		if k == 0 && excludeRoot {
			continue
		}
		if k > 1 || (k == 1 && !excludeRoot) {
			result += formatPkg(pkg.Name, codeOnly) + "\n"
		}
		result += formatFunctions(p.functions[pkg], codeOnly)
		if k < len(p.pkgOrder)-1 {
			result += "\n\n"
		}
	}

	return result
}

func formatPkg(pkgName string, codeOnly bool) string {
	if codeOnly {
		return `// ` + pkgName
	}

	return pkgName
}

func formatFunctions(functions string, codeOnly bool) string {
	if codeOnly {
		return functions
	}

	return "```" + functions + "```"
}

// processFunction processes a function with the provided package path and signature, and its underlying functions up to the specified depth
func (p *parser) processFunction(funcSig *types.Signature, depth int) {
	// Check if the function signature has already been processed
	// If so, return early to avoid processing it again
	if p.seen[funcSig] {
		return
	}

	// Try to get the file and package information associated with the function signature
	// Return if the function signature is not found in the map (i.e. not in a go mod package)
	f, ok := p.funcToFileAndPkg[funcSig]
	if !ok {
		return
	}

	// Inspect the AST (Abstract Syntax Tree) of the file
	ast.Inspect(f.file, func(n ast.Node) bool {
		// Check if the node is a function declaration
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			return true
		}

		// Check if the function signature matches the target function signature
		sig, ok := f.pkg.TypesInfo.ObjectOf(fn.Name).Type().(*types.Signature)
		if !ok || sig != funcSig {
			return true
		}

		// Extract the source code of the function
		funcSrc, err := extractSourceCode(f.pkg.Fset, f.file, fn)
		if err != nil {
			panic(err)
		}

		// If the package is not yet in the functions map, add it to the pkgOrder list
		if _, ok := p.functions[f.pkg]; !ok {
			p.pkgOrder = append(p.pkgOrder, f.pkg)
		}

		// Append the extracted function source code to the existing source code for the package, separated by a newline
		p.functions[f.pkg] += "\n" + funcSrc

		// Add the function to the map of processed functions
		p.seen[funcSig] = true

		// Process the underlying functions
		p.processUnderlyingFunctions(f.pkg, fn, depth-1)

		// Return false to stop AST traversal once the target function is found and processed
		return false
	})
}

// processUnderlyingFunctions processes the underlying functions called within the given function up to a specified depth
func (p *parser) processUnderlyingFunctions(pkg *packages.Package, fn *ast.FuncDecl, depth int) {
	if depth <= 0 {
		return
	}

	// Check if function decleration has body
	if fn.Body == nil {
		return
	}

	// Inspect the AST of the function body
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		// Check if the node is a call expression (function call)
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var funcNode *ast.Ident

		// Get the function node from the call expression
		switch fun := ce.Fun.(type) {
		case *ast.Ident:
			funcNode = fun
		case *ast.SelectorExpr:
			funcNode = fun.Sel
		default:
			return true
		}

		if funcNode == nil {
			return true
		}

		obj := pkg.TypesInfo.ObjectOf(funcNode)
		if obj == nil {
			return true
		}

		funcPkg := obj.Pkg()
		if funcPkg == nil {
			return true
		}

		// Get the function signature from the function node
		funcSig, ok := obj.Type().(*types.Signature)
		if !ok {
			return true
		}

		// Process the underlying functions recursively
		p.processFunction(funcSig, depth)

		return true
	})
}

// extractSourceCode extracts the source code of a function, including comments, from the provided file and function declaration
func extractSourceCode(fset *token.FileSet, file *ast.File, fn *ast.FuncDecl) (string, error) {
	var sb strings.Builder
	// Read the content of the file containing the function
	fileContent, err := os.ReadFile(fset.Position(fn.Pos()).Filename)
	if err != nil {
		return "", err
	}

	// Split the file content into lines
	lines := strings.Split(string(fileContent), "\n")
	start := fset.Position(fn.Pos()).Line - 1

	// Include comments above the function
	if fn.Doc != nil {
		for _, comment := range fn.Doc.List {
			if comment == nil {
				continue
			}
			commentStart := fset.Position(comment.Pos()).Line - 1
			commentEnd := fset.Position(comment.End()).Line - 1
			for i := commentStart; i <= commentEnd; i++ {
				sb.WriteString(lines[i])
				sb.WriteString("\n")
			}
		}
	}

	// Extract the function source code from the start to end line
	end := fset.Position(fn.End()).Line - 1
	for i := start; i <= end; i++ {
		sb.WriteString(lines[i])
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// parseGoModFile parses the go.mod file and returns a slice of package paths.
func parseGoModFile() []string {
	content, err := os.ReadFile("go.mod")
	if err != nil {
		panic(err)
	}

	var goModPaths []string
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			pkg := fields[0]
			if pkg == `module` || pkg == `require` {
				pkg = fields[1]
			}
			if pkg != `` && pkg != `)` && pkg != `(` && pkg != `require` && pkg != `go` {
				goModPaths = append(goModPaths, pkg)
			}
		}
	}

	return goModPaths
}

// loadPackages loads and returns the (sub)packages in the current working directory.
func loadPackages() []*packages.Package {
	err := exec.Command(`go`, `mod`, `vendor`).Run()
	if err != nil {
		fmt.Println("Warning: go mod vendor failed:", err)
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}, "...")
	if err != nil {
		panic(err)
	}
	if len(pkgs) == 0 {
		panic(`no packages found`)
	}
	return pkgs
}

// initialize searches for the target function with the provided name in the root package loads the go.mod packages
func initialize(funcName string) (*types.Signature, map[*types.Signature]fileAndPkg) {
	goModPaths := parseGoModFile()
	pkgs := loadPackages()

	// Return the signature of the intial target function
	var funcSig *types.Signature

	// Collect all function signatures and their respective files
	funcToFileAndPkg := make(map[*types.Signature]fileAndPkg)
	for _, pkg := range pkgs {
		// Skip packages not listed in go.mod
		if !isGoModPkg(goModPaths, pkg.PkgPath) {
			continue
		}

		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				// Check if the node is a function declaration
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Name == nil {
					return true
				}

				// Get the function signature from the TypesInfo of the package
				obj := pkg.TypesInfo.ObjectOf(fn.Name)
				if obj == nil {
					return true
				}

				sig, ok := obj.Type().(*types.Signature)
				if !ok || sig == nil {
					return true
				}

				funcToFileAndPkg[sig] = fileAndPkg{
					file: file,
					pkg:  pkg,
				}

				// If the function is the initial target function, store the signature
				if pkg.PkgPath == goModPaths[0] && fn.Name.Name == funcName {
					funcSig = sig
				}

				return true
			})
		}

		if pkg.PkgPath == goModPaths[0] && funcSig == nil {
			panic(fmt.Sprintf("Function %s not found in package path", funcName))
		}
	}

	return funcSig, funcToFileAndPkg
}

// isGoModPkg checks if the provided package path is listed in the go.mod file
func isGoModPkg(goModPaths []string, pkgPath string) bool {
	// Iterate through the goModPaths to check if the given package path matches or is a subpackage of any listed package
	for _, path := range goModPaths {
		if pkgPath == path || strings.HasPrefix(pkgPath, path+`/`) {
			return true
		}
	}

	return false
}
