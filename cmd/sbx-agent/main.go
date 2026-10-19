package main

import (
	"fmt"
	"os"

	"github.com/TorstenDittmann/sbx/internal/agent"
)

func main() {
	if err := agent.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "sbx-agent: %v\n", err)
		os.Exit(1)
	}
}
