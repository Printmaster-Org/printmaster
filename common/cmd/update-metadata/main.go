package main

import (
	"crypto/ed25519"
	"crypto/sha256"
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
	previousRootPath := flags.String("previous-root", "", "explicit prior root for one-version key transition; requires independent latest timestamp pin")
	expectedTimestamp := flags.String("expected-timestamp-sha256", "", "independently recorded latest timestamp fingerprint for recovery or transition")
	backupDirectory := flags.String("backup", "", "managed local transaction: existing protected publication backup directory")
	recordDirectory := flags.String("records", "", "managed local transaction: existing independent publication record directory")
	mirrorDirectory := flags.String("mirror", "", "managed local transaction: existing public metadata output directory")
	resume := flags.Bool("resume", false, "resume a sealed managed publication without signing another version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *directory == "" || *rootPath == "" || *keysDir == "" || flags.NArg() != 0 {
		return fmt.Errorf("-repository, -root and -keys are required")
	}
	managed := *backupDirectory != "" || *recordDirectory != "" || *mirrorDirectory != "" || *resume
	if managed && (*backupDirectory == "" || *recordDirectory == "" || *mirrorDirectory == "") {
		return fmt.Errorf("managed publication requires -backup, -records and -mirror together")
	}
	if *resume && (*inventory != "" || *previousRootPath != "" || *expectedTimestamp != "") {
		return fmt.Errorf("resume uses the sealed pending publication; do not provide inventory or transition/recovery flags")
	}
	repositoryAbs, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	keysAbs, err := filepath.Abs(*keysDir)
	if err != nil {
		return err
	}
	outputs := []string{repositoryAbs}
	if managed {
		outputs = append(outputs, *backupDirectory, *recordDirectory, *mirrorDirectory)
	}
	for _, output := range outputs {
		absolute, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		if !strings.EqualFold(filepath.VolumeName(absolute), filepath.VolumeName(keysAbs)) {
			continue
		}
		relative, err := filepath.Rel(absolute, keysAbs)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("signing keys must be outside publication output directories")
		}
	}
	if *previousRootPath != "" && *expectedTimestamp == "" {
		return fmt.Errorf("-previous-root requires -expected-timestamp-sha256 from the independent latest publication record")
	}
	if !*resume && (*inventory == "" || *previousRootPath != "" || *expectedTimestamp != "") {
		if _, err := os.Stat(filepath.Join(repositoryAbs, "timestamp.json")); err != nil {
			return fmt.Errorf("freshness refresh requires the existing publication workspace: %w", err)
		}
	}
	root, err := os.ReadFile(*rootPath)
	if err != nil {
		return err
	}
	var previousRoot []byte
	if *previousRootPath != "" {
		previousRoot, err = os.ReadFile(*previousRootPath)
		if err != nil {
			return err
		}
		if len(previousRoot) == 0 {
			return fmt.Errorf("previous root must not be empty")
		}
	}
	keys := make(map[string]ed25519.PrivateKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		if *resume {
			break
		}
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
	options := updatetrust.PublishOptions{Directory: *directory, Root: root, Keys: keys, Targets: targets,
		PreviousRoot: previousRoot, ExpectedTimestampSHA256: *expectedTimestamp}
	if managed {
		if err := updatetrust.PublishTransaction(updatetrust.TransactionOptions{Publish: options,
			BackupDirectory: *backupDirectory, RecordDirectory: *recordDirectory, MirrorDirectory: *mirrorDirectory, Resume: *resume}); err != nil {
			return err
		}
	} else if err := updatetrust.Publish(options); err != nil {
		return err
	}
	timestamp, err := os.ReadFile(filepath.Join(repositoryAbs, "timestamp.json"))
	if err != nil {
		return fmt.Errorf("metadata committed but timestamp fingerprint could not be recorded: %w", err)
	}
	message := "Update metadata published locally; upload versioned files first and timestamp.json last"
	if managed {
		message = "Managed publication backed up, recorded and distributed locally; no remote hosting was changed"
	}
	_, err = fmt.Fprintf(os.Stdout, "%s\nLatest timestamp SHA256 (retain independently): %x\n", message, sha256.Sum256(timestamp))
	return err
}
