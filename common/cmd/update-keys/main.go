package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"printmaster/common/updatetrust"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Publisher key operation failed:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("command required: init, rotate-root, rotate-routine, verify-keys or check-root")
	}
	command := args[0]
	if command != "init" && command != "rotate-root" && command != "rotate-routine" && command != "verify-keys" && command != "check-root" {
		return fmt.Errorf("unknown key operation %q", command)
	}
	flags := flag.NewFlagSet("update-keys "+command, flag.ContinueOnError)
	rootPath := flags.String("root", "", "public root metadata to check or rotate")
	var offline, routine, public, expiry, oldKey string
	if command == "init" || command == "rotate-root" || command == "verify-keys" {
		flags.StringVar(&offline, "offline", "", "offline root-key directory")
	}
	if command == "init" || command == "rotate-routine" || command == "verify-keys" {
		flags.StringVar(&routine, "routine", "", "routine signing-key directory (never upload root.pem)")
	}
	if command == "init" || command == "rotate-root" || command == "rotate-routine" {
		flags.StringVar(&public, "public", "", "new directory for versioned public root metadata")
		flags.StringVar(&expiry, "expires", "", "explicit future root expiry in RFC3339 UTC")
	}
	if command == "rotate-root" || command == "rotate-routine" {
		flags.StringVar(&oldKey, "old-key", "", "previous offline root.pem")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if command == "init" {
		if *rootPath != "" {
			return fmt.Errorf("init creates a new root; do not supply -root")
		}
	} else if *rootPath == "" {
		return fmt.Errorf("-root required")
	}
	if (command == "rotate-root" || command == "rotate-routine") && oldKey == "" {
		return fmt.Errorf("-old-key required")
	}
	if command == "init" || command == "rotate-root" || command == "rotate-routine" {
		expires, err := time.Parse(time.RFC3339, expiry)
		if err != nil || !expires.After(time.Now()) {
			return fmt.Errorf("-expires must be a future RFC3339 timestamp")
		}
		dirs := []string{offline, public}
		if command == "init" {
			dirs = append(dirs, routine)
		} else if command == "rotate-routine" {
			dirs = []string{routine, public}
		}
		if err := validateNewDirectories(dirs); err != nil {
			return err
		}
		var root []byte
		var rootKey ed25519.PrivateKey
		if command == "init" {
			keys, pub, err := generateRoleKeys("root", "targets", "snapshot", "timestamp")
			if err != nil {
				return err
			}
			rootKey = keys["root"]
			root, err = updatetrust.InitializeRoot(rootKey, pub, expires)
			if err != nil {
				return err
			}
			if err := createOutputs(dirs, func() error {
				for _, role := range []string{"targets", "snapshot", "timestamp"} {
					if err := writeKey(filepath.Join(routine, role+".pem"), keys[role]); err != nil {
						return err
					}
				}
				return writeProvisioning(offline, public, rootKey, root)
			}); err != nil {
				return err
			}
		} else {
			previous, err := readRoot(*rootPath)
			if err != nil {
				return err
			}
			old, err := updatetrust.LoadSigningKey(oldKey)
			if err != nil {
				return fmt.Errorf("load old offline key: %w", err)
			}
			if command == "rotate-routine" {
				keys, pub, err := generateRoleKeys("targets", "snapshot", "timestamp")
				if err != nil {
					return err
				}
				root, err = updatetrust.RotateRoutineKeys(previous, old, pub, expires)
				if err != nil {
					return err
				}
				if err := createOutputs(dirs, func() error {
					for _, role := range []string{"targets", "snapshot", "timestamp"} {
						if err := writeKey(filepath.Join(routine, role+".pem"), keys[role]); err != nil {
							return err
						}
					}
					return writePublicRoot(public, root)
				}); err != nil {
					return err
				}
			} else {
				_, rootKey, err = ed25519.GenerateKey(nil)
				if err != nil {
					return err
				}
				root, err = updatetrust.RotateRoot(previous, old, rootKey, expires)
				if err != nil {
					return err
				}
				if err := createOutputs(dirs, func() error {
					return writeProvisioning(offline, public, rootKey, root)
				}); err != nil {
					return err
				}
			}
		}
		return reportRoot(output, root)
	}
	root, err := readRoot(*rootPath)
	if err != nil {
		return err
	}
	if command == "verify-keys" {
		if offline == "" || routine == "" {
			return fmt.Errorf("-offline and -routine are required")
		}
		keys := make(map[string]ed25519.PrivateKey)
		for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
			dir := routine
			if role == "root" {
				dir = offline
			}
			key, err := updatetrust.LoadSigningKey(filepath.Join(dir, role+".pem"))
			if err != nil {
				return fmt.Errorf("verify %s backup key: %w", role, err)
			}
			keys[role] = key
		}
		if err := updatetrust.VerifySigningKeys(root, keys); err != nil {
			return err
		}
	} else if err := updatetrust.ValidateBootstrapRoot(root, time.Now()); err != nil {
		return err
	}
	return reportRoot(output, root)
}

func readRoot(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, updatetrust.MaxMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > updatetrust.MaxMetadataBytes {
		return nil, fmt.Errorf("publisher root exceeds metadata size limit")
	}
	return data, nil
}

func generateRoleKeys(roles ...string) (map[string]ed25519.PrivateKey, map[string]crypto.PublicKey, error) {
	keys := make(map[string]ed25519.PrivateKey)
	public := make(map[string]crypto.PublicKey)
	for _, role := range roles {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			return nil, nil, err
		}
		keys[role], public[role] = key, pub
	}
	return keys, public, nil
}

func reportRoot(output io.Writer, data []byte) error {
	root, err := metadata.Root().FromBytes(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Public root version %d; expires %s; SHA256 %x\n",
		root.Signed.Version, root.Signed.Expires.Format(time.RFC3339), sha256.Sum256(data))
	return err
}

func validateNewDirectories(dirs []string) error {
	absolute := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			return fmt.Errorf("new output directories required (-public and -offline and/or -routine for this operation)")
		}
		path, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("output directory must not already exist: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect output directory: %w", err)
		}
		// No recursive creation or linked ancestors: the owner must select an
		// existing trusted parent outside a source checkout or published tree.
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			if err := checkDirectory(parent); err != nil {
				return err
			}
			if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
				return fmt.Errorf("private-key outputs must be outside source checkouts")
			} else if !os.IsNotExist(err) {
				return err
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
		for _, other := range absolute {
			if strings.EqualFold(path, other) {
				return fmt.Errorf("output directories must be distinct")
			}
		}
		absolute = append(absolute, path)
	}
	return nil
}

func createOutputs(dirs []string, write func() error) error {
	for _, dir := range dirs {
		if err := makePrivateDirectory(dir); err != nil {
			return fmt.Errorf("create protected output %s: %w; partial outputs must be preserved and reviewed, never reused", dir, err)
		}
	}
	if err := write(); err != nil {
		return fmt.Errorf("write provisioning outputs: %w; partial outputs must be preserved and reviewed, never reused", err)
	}
	return nil
}

func writeKey(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writeExclusive(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func writeProvisioning(offline, public string, key ed25519.PrivateKey, data []byte) error {
	if err := writeKey(filepath.Join(offline, "root.pem"), key); err != nil {
		return err
	}
	return writePublicRoot(public, data)
}

func writePublicRoot(public string, data []byte) error {
	root, err := metadata.Root().FromBytes(data)
	if err != nil {
		return err
	}
	return writeExclusive(filepath.Join(public, fmt.Sprintf("%d.root.json", root.Signed.Version)), data)
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
