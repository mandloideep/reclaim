// Command reclaim finds reclaimable disk space and removes only what you select.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "reclaim:", err)
		os.Exit(1)
	}
}

func run(_ []string) error {
	fmt.Println("reclaim: not implemented yet, see docs/DESIGN.md")
	return nil
}
