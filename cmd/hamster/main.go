package main

import (
	"os"

	"hamster/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
