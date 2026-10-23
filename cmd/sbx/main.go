package main

import (
	"fmt"
	"os"

	"github.com/TorstenDittmann/sbx/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		if code, ok := cli.ExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintf(os.Stderr, "sbx: %v\n", err)
		os.Exit(1)
	}
}
