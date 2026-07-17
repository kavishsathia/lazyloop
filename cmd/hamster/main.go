package main

import (
	"fmt"
	"os"

	"hamster/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Printf("%s %s\n", version.Name, version.Version)
		return
	}

	fmt.Fprintf(os.Stderr, "usage: %s version\n", version.Name)
	os.Exit(2)
}
