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
		fmt.Fprintln(os.Stderr, "Update delivery failed:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("update-deliver", flag.ContinueOnError)
	directory := flags.String("publication", "", "protected sealed local publication backup directory")
	rootPath := flags.String("root", "", "independently trusted current public root")
	pin := flags.String("timestamp-sha256", "", "exact sealed publication fingerprint")
	previous := flags.String("previous-timestamp-sha256", "", "independently recorded last remote publication fingerprint")
	bootstrap := flags.Bool("bootstrap", false, "explicit first remote publication; do not use to repair missing state")
	serialized := flags.Bool("externally-serialized", false, "assert exclusive ownership of all remote writers throughout delivery")
	public := flags.String("metadata-bucket", "", "public metadata bucket")
	recovery := flags.String("recovery-bucket", "", "private retained recovery bucket")
	endpoint := flags.String("s3-endpoint", "", "expected HTTPS B2 S3 endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *rootPath == "" || *pin == "" || !*serialized ||
		*public == "" || *recovery == "" || *public == *recovery || *endpoint == "" ||
		(*bootstrap && *previous != "") || (!*bootstrap && *previous == "") {
		return fmt.Errorf("sealed publication/root/pin, distinct buckets/endpoint, external serialization and either bootstrap or previous remote pin required")
	}
	root, err := os.ReadFile(*rootPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	connect := func(name, prefix string, retained bool) (*updateb2.Store, error) {
		return updateb2.NewStore(ctx, updateb2.StoreOptions{
			Bucket: name, S3Endpoint: *endpoint, Recovery: retained,
			KeyID: getenv(prefix + "_KEY_ID"), ApplicationKey: getenv(prefix + "_APPLICATION_KEY"),
		})
	}
	publicStore, err := connect(*public, "B2_METADATA_DELIVERY", false)
	if err != nil {
		return fmt.Errorf("metadata delivery credentials/configuration: %w", err)
	}
	recoveryStore, err := connect(*recovery, "B2_RECOVERY_DELIVERY", true)
	if err != nil {
		return fmt.Errorf("recovery delivery credentials/configuration: %w", err)
	}
	if err := updatetrust.DeliverPublication(ctx, updatetrust.DeliveryOptions{
		Directory: *directory, Root: root, ExpectedTimestampSHA256: *pin, ExpectedPreviousSHA256: *previous,
		Bootstrap: *bootstrap, ExternalSerialization: *serialized, Public: publicStore, Recovery: recoveryStore,
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Verified remote delivery complete; retain independent latest remote timestamp SHA256: %s\n", *pin)
	return err
}
