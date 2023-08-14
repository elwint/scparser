# Source code parser

The parser takes a function name and package path as an argument and returns the source code of the function and its underlying functions (up to a depth of 5). The parser relies on the Abstract Syntax Tree (AST) and type information of the Go code to find and process the functions. It handles packages listed in the `go.mod` file, and processes only the functions declared in those packages.
