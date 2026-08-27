// lazyloop is the same binary as hamster under the name people will type:
// bare, it opens the split screen; with arguments it is the whole CLI.
package main

import (
	"os"

	"hamster/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
