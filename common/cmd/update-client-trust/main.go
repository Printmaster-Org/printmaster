package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"printmaster/common/updatetrust"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Client trust operation failed:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "repair") {
		return fmt.Errorf("expected inspect or repair; no trust reset operation exists")
	}
	flags := flag.NewFlagSet("update-client-trust "+args[0], flag.ContinueOnError)
	cache := flags.String("cache", "", "protected client metadata directory, not publisher repository")
	var backup *string
	var serialized *bool
	if args[0] == "repair" {
		backup = flags.String("backup", "", "protected metadata copy matching the retained local checkpoint exactly")
		serialized = flags.Bool("externally-serialized", false, "assert all Agent/helper/verifier writers stopped and exclusive owner control")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *cache == "" || flags.NArg() != 0 {
		return fmt.Errorf("explicit client metadata directory required, with no positional arguments")
	}
	if args[0] == "inspect" {
		report, err := updatetrust.InspectClientTrust(*cache)
		return errors.Join(err, json.NewEncoder(output).Encode(report))
	}
	if *backup == "" || !*serialized {
		return fmt.Errorf("repair requires matching backup and explicit external serialization")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	evidence, err := updatetrust.RepairClientTrust(ctx, updatetrust.ClientTrustRepairOptions{
		CacheDir: *cache, BackupDir: *backup, ExternalSerialization: *serialized,
	})
	if err != nil {
		if evidence != "" {
			err = fmt.Errorf("%w; preserved repair evidence: %s", err, evidence)
		}
		return err
	}
	_, err = fmt.Fprintf(output, "Exact checkpoint-pinned metadata restored; checkpoint unchanged. Evidence: %s. Normal TUF refresh is still required; no installation or trust reset performed.\n", evidence)
	return err
}
