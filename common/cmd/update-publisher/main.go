package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"printmaster/common/updateb2"
	"printmaster/common/updatepublisher"
	"printmaster/common/updatestate"
	"printmaster/common/updatetrust"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Publisher operation failed:", err)
		os.Exit(1)
	}
}

type retainedStore struct {
	*updateb2.Store
	reader *updateb2.Store
}

func (s retainedStore) GetPinned(ctx context.Context, name, pin string) ([]byte, error) {
	return s.reader.GetPinned(ctx, name, pin)
}

type interruptedPublic struct{ updatetrust.PublicationStore }

func (s interruptedPublic) Put(ctx context.Context, name string, data []byte) error {
	if name == "timestamp.json" {
		return errors.New("intentional rehearsal interruption before public timestamp commit")
	}
	return s.PublicationStore.Put(ctx, name, data)
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("update-publisher", flag.ContinueOnError)
	mode := flags.String("mode", "check", "publish, check or mirror")
	work := flags.String("work", "", "existing empty protected runner work directory")
	mirror := flags.String("mirror", "", "existing protected public Server mirror directory")
	rootPath := flags.String("root", "", "independently provisioned public root file; otherwise UPDATE_ROOT_JSON")
	stateRepo := flags.String("state-repository", "", "protected independent owner/repository")
	publicURL := flags.String("public-url", "", "public B2 metadata HTTPS base URL")
	bootstrap := flags.Bool("bootstrap", false, "explicit first publication only")
	serialized := flags.Bool("externally-serialized", false, "exclusive ownership of all publish/recovery writers")
	interrupt := flags.Bool("rehearsal-interrupt", false, "isolated rehearsal only: stop before public timestamp commit")
	inventory := flags.String("inventory", "", "optional verified local release inventory; omit for freshness-only publication")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*mode != "publish" && *mode != "check" && *mode != "mirror") || *stateRepo == "" ||
		*publicURL == "" || *mirror == "" || !*serialized || (*mode == "publish" && *work == "") ||
		(*mode != "publish" && (*bootstrap || *interrupt || *inventory != "")) {
		return fmt.Errorf("explicit mode, state repository, public URL, mirror, serialization and publish work directory required")
	}
	if *interrupt && (*stateRepo != "Printmaster-Org/printmaster-update-rehearsal-state" ||
		getenv("B2_METADATA_BUCKET") != "printmaster-update-rehearsal-metadata" ||
		getenv("B2_RECOVERY_BUCKET") != "printmaster-update-rehearsal-recovery") {
		return fmt.Errorf("failure injection is restricted to the isolated rehearsal namespace")
	}
	root := []byte(strings.TrimSpace(getenv("UPDATE_ROOT_JSON")))
	if *rootPath != "" {
		var err error
		root, err = os.ReadFile(*rootPath)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	backend, err := updatestate.NewGitHub(ctx, *stateRepo, getenv("UPDATE_STATE_TOKEN"))
	if err != nil {
		return err
	}
	journal := updatestate.Journal{Backend: backend}
	if *mode == "publish" {
		var targets []updatetrust.PublishTarget
		if *inventory != "" {
			file, err := os.Open(*inventory)
			if err != nil {
				return err
			}
			data, readErr := io.ReadAll(io.LimitReader(file, updatetrust.MaxMetadataBytes+1))
			closeErr := file.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return err
			}
			if len(data) > int(updatetrust.MaxMetadataBytes) {
				return fmt.Errorf("release inventory exceeds metadata bound")
			}
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&targets); err != nil {
				return err
			}
			if targets == nil {
				return fmt.Errorf("release inventory must be a JSON array")
			}
			if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
				return fmt.Errorf("release inventory must contain exactly one array")
			}
		}
		open := func(bucket, prefix string, recovery bool) (*updateb2.Store, error) {
			return updateb2.NewStore(ctx, updateb2.StoreOptions{Bucket: bucket, S3Endpoint: getenv("B2_S3_ENDPOINT"),
				KeyID: getenv(prefix + "_KEY_ID"), ApplicationKey: getenv(prefix + "_APPLICATION_KEY"), Recovery: recovery})
		}
		public, err := open(getenv("B2_METADATA_BUCKET"), "B2_METADATA_DELIVERY", false)
		if err != nil {
			return err
		}
		recovery, err := open(getenv("B2_RECOVERY_BUCKET"), "B2_RECOVERY_DELIVERY", true)
		if err != nil {
			return err
		}
		reader, err := updateb2.NewRecoveryReader(ctx, updateb2.StoreOptions{
			Bucket: getenv("B2_RECOVERY_BUCKET"), S3Endpoint: getenv("B2_S3_ENDPOINT"),
			KeyID: getenv("B2_RECOVERY_RESTORE_KEY_ID"), ApplicationKey: getenv("B2_RECOVERY_RESTORE_APPLICATION_KEY")})
		if err != nil {
			return err
		}
		var publicStore updatetrust.PublicationStore = public
		if *interrupt {
			publicStore = interruptedPublic{public}
		}
		result, err := updatepublisher.Run(ctx, updatepublisher.Options{Work: *work, Root: root, Journal: journal,
			Recovery: retainedStore{Store: recovery, reader: reader}, Public: publicStore,
			Bootstrap: *bootstrap, Targets: targets, ExternalSerialization: true, LoadKeys: func() (map[string]ed25519.PrivateKey, error) {
				keys := make(map[string]ed25519.PrivateKey)
				for _, role := range []string{"targets", "snapshot", "timestamp"} {
					key, err := updatetrust.ParseSigningKey([]byte(strings.TrimSpace(getenv("UPDATE_"+strings.ToUpper(role)+"_PEM")) + "\n"))
					if err != nil {
						return nil, fmt.Errorf("load routine %s key: %w", role, err)
					}
					keys[role] = key
				}
				return keys, nil
			}})
		if err != nil {
			return err
		}
		if err := json.NewEncoder(output).Encode(result); err != nil {
			return err
		}
	}
	latest, err := journal.Latest(ctx)
	if err != nil {
		return err
	}
	source, err := updatetrust.NewPublicReader(*publicURL)
	if err != nil {
		return err
	}
	if err := updatetrust.MirrorPublication(ctx, *mirror, root, latest, source); err != nil {
		return err
	}
	if err := updatetrust.CheckMirrorFreshness(*mirror, root, 48*time.Hour); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Verified authority, public B2 metadata and timestamp-last mirror; timestamp version %d, at least 48h freshness. No root private key used.\n", latest.Version)
	return err
}
