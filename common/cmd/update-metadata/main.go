package main

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"printmaster/common/updatetrust"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Update metadata publishing failed:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("update-metadata", flag.ContinueOnError)
	directory := flags.String("repository", "", "exclusive local TUF metadata workspace")
	rootPath := flags.String("root", "", "owner-provisioned trusted root metadata")
	keysDir := flags.String("keys", "", "directory containing targets.pem, snapshot.pem and timestamp.pem (not root)")
	inventory := flags.String("inventory", "", "optional JSON array of PublishTarget entries; omit for freshness refresh")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *directory == "" || *rootPath == "" || *keysDir == "" || flags.NArg() != 0 {
		return fmt.Errorf("-repository, -root and -keys are required")
	}
	repositoryAbs, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	keysAbs, err := filepath.Abs(*keysDir)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.VolumeName(repositoryAbs), filepath.VolumeName(keysAbs)) {
		relative, err := filepath.Rel(repositoryAbs, keysAbs)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("signing keys must be outside the published metadata directory")
		}
	}
	if *inventory == "" {
		if _, err := os.Stat(filepath.Join(repositoryAbs, "timestamp.json")); err != nil {
			return fmt.Errorf("freshness refresh requires the existing publication workspace: %w", err)
		}
	}
	root, err := os.ReadFile(*rootPath)
	if err != nil {
		return err
	}
	keys := make(map[string]ed25519.PrivateKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		key, err := updatetrust.LoadSigningKey(filepath.Join(*keysDir, role+".pem"))
		if err != nil {
			return fmt.Errorf("load %s signing key: %w", role, err)
		}
		keys[role] = key
	}
	var targets []updatetrust.PublishTarget
	if *inventory != "" {
		file, err := os.Open(*inventory)
		if err != nil {
			return err
		}
		defer file.Close()
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&targets); err != nil {
			return fmt.Errorf("decode publishing inventory: %w", err)
		}
		if targets == nil {
			return fmt.Errorf("publishing inventory must be a JSON array, not null")
		}
		if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
			return fmt.Errorf("publishing inventory must contain exactly one JSON array")
		}
	}
	if err := updatetrust.Publish(updatetrust.PublishOptions{Directory: *directory, Root: root, Keys: keys, Targets: targets}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Update metadata published locally; upload versioned files first and timestamp.json last")
	return nil
}
