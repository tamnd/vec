// Command vec is the single-file vector database shell and tool.
package main

import (
	"os"

	"github.com/tamnd/vec/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
