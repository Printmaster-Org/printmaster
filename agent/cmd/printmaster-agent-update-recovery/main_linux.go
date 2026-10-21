package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"printmaster/agent/autoupdate"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := autoupdate.RunRepositoryPackageRecovery(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "PrintMaster native package recovery:", err)
		os.Exit(1)
	}
}
