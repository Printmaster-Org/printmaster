package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"printmaster/common/updatestate"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Publication authority read failed:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("update-state", flag.ContinueOnError)
	repository := flags.String("repository", "", "independently configured protected GitHub owner/repository")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" || flags.NArg() != 0 {
		return fmt.Errorf("independently configured authority repository required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	backend, err := updatestate.NewGitHub(ctx, *repository, getenv("UPDATE_STATE_READ_TOKEN"))
	if err != nil {
		return err
	}
	latest, err := (updatestate.Journal{Backend: backend}).Latest(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(latest)
}
