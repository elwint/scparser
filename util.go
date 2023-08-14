package scparser

import "os"

func panicOnErr(err error) {
	if err != nil {
		panic(err)
	}
}

func changeDir(dir string) func() {
	curDir, err := os.Getwd()
	panicOnErr(err)

	err = os.Chdir(dir)
	panicOnErr(err)

	return func() {
		err := os.Chdir(curDir)
		panicOnErr(err)
	}
}
