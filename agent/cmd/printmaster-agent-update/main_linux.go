package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"printmaster/agent/autoupdate"
)

func main() {
	// Do not inherit cancellation from the service being upgraded. The command
	// has its own budget; durable intent survives process/transaction failures.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := autoupdate.RunRepositoryPackageHelper(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "PrintMaster package helper:", err)
		os.Exit(1)
	}
}
