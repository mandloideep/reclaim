// Command reclaim finds reclaimable disk space and removes only what you select.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	a, err := defaultApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reclaim:", err)
		return 1
	}
	cmd := newRootCmd(a)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "reclaim:", err)
		return 1
	}
	return 0
}
