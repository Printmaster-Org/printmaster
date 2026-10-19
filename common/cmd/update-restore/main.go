package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"printmaster/common/updateb2"
	"printmaster/common/updatetrust"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Update state restoration failed:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("update-restore", flag.ContinueOnError)
	directory := flags.String("repository", "", "existing empty protected restore workspace")
	records := flags.String("records", "", "existing empty protected high-water record directory")
	rootPath := flags.String("root", "", "independently trusted current public root")
	timestamp := flags.String("timestamp-sha256", "", "independently recorded latest-safe timestamp fingerprint")
	record := flags.String("record-sha256", "", "independently recorded matching sealed record fingerprint")
	serialized := flags.Bool("externally-serialized", false, "assert exclusive owner control throughout recovery")
	bucket := flags.String("recovery-bucket", "", "private retained recovery bucket")
	endpoint := flags.String("s3-endpoint", "", "expected HTTPS B2 S3 endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *records == "" || *rootPath == "" ||
		*timestamp == "" || *record == "" || !*serialized || *bucket == "" || *endpoint == "" {
		return fmt.Errorf("empty workspace/records, trusted root, both independent fingerprints, recovery bucket/endpoint and external serialization required")
	}
	root, err := os.ReadFile(*rootPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	reader, err := updateb2.NewRecoveryReader(ctx, updateb2.StoreOptions{
		Bucket: *bucket, S3Endpoint: *endpoint,
		KeyID: getenv("B2_RECOVERY_RESTORE_KEY_ID"), ApplicationKey: getenv("B2_RECOVERY_RESTORE_APPLICATION_KEY"),
	})
	if err != nil {
		return err
	}
	if err := updatetrust.RestorePublication(ctx, updatetrust.RestoreOptions{
		Directory: *directory, RecordDirectory: *records, Root: root,
		ExpectedTimestampSHA256: *timestamp, ExpectedRecordSHA256: *record,
		ExternalSerialization: *serialized, Recovery: reader,
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Authenticated publisher state restored: timestamp SHA256 %s, record SHA256 %s. Refresh expired routine metadata before delivery. No remote writes or signing performed.\n", *timestamp, *record)
	return err
}
