package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"printmaster/common/updateb2"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Update storage preflight failed:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("update-storage-check", flag.ContinueOnError)
	public := flags.String("metadata-bucket", "", "public metadata bucket name")
	recovery := flags.String("recovery-bucket", "", "private recovery bucket name")
	endpoint := flags.String("s3-endpoint", "", "expected HTTPS S3 endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	return updateb2.Check(context.Background(), *public, *recovery, *endpoint, getenv, output)
}
