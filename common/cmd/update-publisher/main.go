package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	mode := flags.String("mode", "check", "publish, check, mirror or preflight (read-only credential/configuration checks)")
	work := flags.String("work", "", "existing empty protected runner work directory")
	mirror := flags.String("mirror", "", "existing protected public Server mirror directory")
	rootPath := flags.String("root", "", "independently provisioned public root file; otherwise UPDATE_ROOT_JSON")
	stateRepo := flags.String("state-repository", "", "protected independent owner/repository")
	publicURL := flags.String("public-url", "", "public B2 metadata HTTPS base URL")
	bootstrap := flags.Bool("bootstrap", false, "explicit first publication only")
	devOnly := flags.Bool("dev-only", false, "accept only the complete Dev Agent binary matrix and bootstrap empty authority once")
	devSourceSHA := flags.String("dev-source-sha", "", "full source SHA for the current Dev build")
	devVersion := flags.String("dev-version", "", "exact source-derived current Dev Agent version")
	serialized := flags.Bool("externally-serialized", false, "exclusive ownership of all publish/recovery writers")
	interrupt := flags.Bool("rehearsal-interrupt", false, "isolated rehearsal only: stop before public timestamp commit")
	inventory := flags.String("inventory", "", "optional verified local release inventory; omit for freshness-only publication")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*mode != "publish" && *mode != "check" && *mode != "mirror" && *mode != "preflight") || *stateRepo == "" ||
		*publicURL == "" || (*mode != "preflight" && *mirror == "") || !*serialized || (*mode == "publish" && *work == "") ||
		(*mode != "publish" && (*bootstrap || *devOnly || *interrupt || *inventory != "")) ||
		(*mode != "publish" && (*devSourceSHA != "" || *devVersion != "")) ||
		(*devOnly && (*mode != "publish" || *inventory == "" || *bootstrap || *interrupt || *devSourceSHA == "" || *devVersion == "")) ||
		(!*devOnly && (*devSourceSHA != "" || *devVersion != "")) {
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
	if *devOnly && fmt.Sprintf("%x", sha256.Sum256(root)) != devBootstrapRootSHA256 {
		return fmt.Errorf("Dev publisher root fingerprint changed; owner root-transition review required")
	}
	if *mode == "preflight" {
		if err := updatetrust.ValidateBootstrapRoot(root, time.Now()); err != nil {
			return err
		}
		if _, err := updatetrust.NewPublicReader(*publicURL); err != nil {
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
	if *mode == "preflight" {
		state, _, err := journal.Read(ctx)
		if err != nil {
			return err
		}
		rootPin := fmt.Sprintf("%x", sha256.Sum256(root))
		if (state.Latest != nil && state.Latest.RootSHA256 != rootPin) ||
			(state.Pending != nil && state.Pending.Publication.RootSHA256 != rootPin) {
			return fmt.Errorf("authority root fingerprint differs from supplied root; owner transition required")
		}
		for _, store := range []struct {
			bucket, prefix     string
			recovery, readOnly bool
		}{
			{getenv("B2_METADATA_BUCKET"), "B2_METADATA_DELIVERY", false, false},
			{getenv("B2_RECOVERY_BUCKET"), "B2_RECOVERY_DELIVERY", true, false},
			{getenv("B2_RECOVERY_BUCKET"), "B2_RECOVERY_RESTORE", true, true},
		} {
			opts := updateb2.StoreOptions{Bucket: store.bucket, S3Endpoint: getenv("B2_S3_ENDPOINT"),
				KeyID: getenv(store.prefix + "_KEY_ID"), ApplicationKey: getenv(store.prefix + "_APPLICATION_KEY"), Recovery: store.recovery}
			if store.readOnly {
				_, err = updateb2.NewRecoveryReader(ctx, opts)
			} else {
				_, err = updateb2.NewStore(ctx, opts)
			}
			if err != nil {
				return fmt.Errorf("%s preflight: %w", store.prefix, err)
			}
		}
		_, err = fmt.Fprintf(output, "Read-only preflight verified root %s, authority generation %d (latest=%t, pending=%t), and all three B2 credential/bucket policies. No signing, remote writes or publication; write permissions and mirror delivery are not proven.\n",
			rootPin, state.Generation, state.Latest != nil, state.Pending != nil)
		return err
	}
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
			if *devOnly {
				if err := validateDevInventory(targets, *devSourceSHA, *devVersion); err != nil {
					return err
				}
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
		keys := func() (map[string]ed25519.PrivateKey, error) {
			keys := make(map[string]ed25519.PrivateKey)
			for _, role := range []string{"targets", "snapshot", "timestamp"} {
				key, err := updatetrust.ParseSigningKey([]byte(strings.TrimSpace(getenv("UPDATE_"+strings.ToUpper(role)+"_PEM")) + "\n"))
				if err != nil {
					return nil, fmt.Errorf("load routine %s key: %w", role, err)
				}
				keys[role] = key
			}
			return keys, nil
		}
		options := updatepublisher.Options{Work: *work, Root: root, Journal: journal,
			Recovery: retainedStore{Store: recovery, reader: reader}, Public: publicStore,
			Bootstrap: *bootstrap, Targets: targets, ExternalSerialization: true, LoadKeys: keys}
		var result updatepublisher.Result
		if *devOnly {
			result, err = runDevPublication(ctx, options, journal, targets)
		} else {
			result, err = updatepublisher.Run(ctx, options)
		}
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

const devBootstrapRootSHA256 = "201bedf94312320adc7056e0cde18deb1633c3707dbd1f978a6cdff2f25e1b14"

//go:embed dev-bootstrap-baselines.json
var devBootstrapBaselineFile embed.FS

type devBaselineRegistry struct {
	Schema     int                  `json:"schema"`
	RootSHA256 string               `json:"root_sha256"`
	Releases   []devBaselineRelease `json:"releases"`
}

type devBaselineRelease struct {
	Version   string            `json:"version"`
	SourceSHA string            `json:"source_sha"`
	Digests   map[string]string `json:"digests"`
}

func loadDevBaselineRegistry() (devBaselineRegistry, error) {
	data, err := devBootstrapBaselineFile.ReadFile("dev-bootstrap-baselines.json")
	if err != nil {
		return devBaselineRegistry{}, err
	}
	var registry devBaselineRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return devBaselineRegistry{}, err
	}
	if registry.Schema != 1 || registry.RootSHA256 != devBootstrapRootSHA256 || len(registry.Releases) == 0 {
		return devBaselineRegistry{}, fmt.Errorf("Dev baseline registry schema/root is invalid; owner transition review required")
	}
	return registry, nil
}

func validateDevInventory(targets []updatetrust.PublishTarget, sourceSHA, version string) error {
	registry, err := loadDevBaselineRegistry()
	if err != nil {
		return err
	}
	expected := map[string]struct{}{"linux/amd64": {}, "linux/arm64": {}, "windows/amd64": {}, "darwin/amd64": {}, "darwin/arm64": {}}
	marker := "-dev."
	markerIndex := strings.LastIndex(version, marker)
	commitPrefix := ""
	if markerIndex >= 0 {
		commitPrefix = version[markerIndex+len(marker):]
	}
	baseVersion := ""
	if markerIndex >= 0 {
		baseVersion = version[:markerIndex]
	}
	versionParts := strings.Split(baseVersion, ".")
	validBaseVersion := len(versionParts) == 3
	for _, part := range versionParts {
		validBaseVersion = validBaseVersion && part != "" && strings.Trim(part, "0123456789") == ""
	}
	if !validBaseVersion || len(sourceSHA) != 40 || strings.Trim(sourceSHA, "0123456789abcdef") != "" ||
		len(commitPrefix) < 7 || len(commitPrefix) > len(sourceSHA) || !strings.HasPrefix(sourceSHA, commitPrefix) ||
		version == "" {
		return fmt.Errorf("Dev publication source/version identity is invalid")
	}
	baselineDigests := make(map[string]map[string]string, len(registry.Releases))
	versions := make(map[string]int, len(registry.Releases)+1)
	sources := make(map[string]struct{}, len(registry.Releases))
	for _, release := range registry.Releases {
		releaseMarker := baseVersion + marker
		suffix := strings.TrimPrefix(release.Version, releaseMarker)
		if suffix == release.Version || len(suffix) < 7 || len(suffix) > 40 || strings.Trim(suffix, "0123456789abcdef") != "" ||
			len(release.SourceSHA) != 40 || strings.Trim(release.SourceSHA, "0123456789abcdef") != "" || !strings.HasPrefix(release.SourceSHA, suffix) ||
			release.Version == version || baselineDigests[release.Version] != nil {
			return fmt.Errorf("invalid/duplicate pinned Dev baseline identity: %s", release.Version)
		}
		if _, ok := sources[release.SourceSHA]; ok {
			return fmt.Errorf("duplicate pinned Dev baseline source: %s", release.SourceSHA)
		}
		if len(release.Digests) != len(expected) {
			return fmt.Errorf("invalid pinned Dev baseline digest set: %s", release.Version)
		}
		for key := range expected {
			digest := release.Digests[key]
			if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
				return fmt.Errorf("invalid pinned Dev baseline digest set: %s", release.Version)
			}
		}
		baselineDigests[release.Version] = release.Digests
		versions[release.Version] = 0
		sources[release.SourceSHA] = struct{}{}
	}
	versions[version] = 0
	if len(targets) != len(expected)*len(versions) {
		return fmt.Errorf("Dev publication requires all pinned baselines and current build matrix (%d targets)", len(expected)*len(versions))
	}
	seen := make(map[string]struct{}, len(targets))
	for _, entry := range targets {
		target := entry.Target
		key := target.Platform + "/" + target.Arch
		if target.Component != "agent" || target.Channel != "dev" || target.Format != "binary" {
			return fmt.Errorf("Dev publisher accepts only agent/dev binary targets")
		}
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("unexpected Dev target platform/architecture %q", key)
		}
		digests, isBaseline := baselineDigests[target.Version]
		if target.Version != version && !isBaseline {
			return fmt.Errorf("unexpected Dev target version %q", target.Version)
		}
		if target.SizeBytes <= 0 || len(target.SHA256) != 64 || strings.Trim(target.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("invalid Dev target digest or size")
		}
		if isBaseline && target.SHA256 != digests[key] {
			return fmt.Errorf("pinned Dev baseline digest mismatch for %s", key)
		}
		identity, err := target.Path()
		if err != nil {
			return err
		}
		if _, ok := seen[identity]; ok {
			return fmt.Errorf("duplicate Dev target %q", identity)
		}
		seen[identity] = struct{}{}
		versions[target.Version]++
	}
	for releaseVersion, count := range versions {
		if count != len(expected) {
			return fmt.Errorf("Dev inventory version %q has %d targets, expected %d", releaseVersion, count, len(expected))
		}
	}
	return nil
}

func runDevPublication(ctx context.Context, options updatepublisher.Options, journal updatestate.Journal, targets []updatetrust.PublishTarget) (updatepublisher.Result, error) {
	state, _, err := journal.Read(ctx)
	if err != nil {
		return updatepublisher.Result{}, err
	}
	recoveredPending := false
	workIndex := 0
	newWork := func(label string) (string, error) {
		work := filepath.Join(options.Work, label)
		if err := os.Mkdir(work, 0o700); err != nil {
			return "", err
		}
		return work, nil
	}
	if state.Pending != nil {
		work, err := newWork("recover-pending")
		if err != nil {
			return updatepublisher.Result{}, err
		}
		recovery := options
		recovery.Work, recovery.Targets, recovery.Bootstrap = work, nil, false
		result, err := updatepublisher.Run(ctx, recovery)
		if err != nil {
			return updatepublisher.Result{}, fmt.Errorf("recover pending Dev publication before signing new inventory: %w", err)
		}
		recoveredPending = result.RecoveredPending
		state, _, err = journal.Read(ctx)
		if err != nil {
			return updatepublisher.Result{}, err
		}
	}
	if state.Latest == nil && (state.Generation != 0 || state.Pending != nil) {
		return updatepublisher.Result{}, fmt.Errorf("Dev publisher authority is not an empty bootstrap or completed publication")
	}
	options.Bootstrap = state.Generation == 0 && state.Latest == nil
	options.Targets = targets
	workIndex++
	work, err := newWork(fmt.Sprintf("publish-dev-%d", workIndex))
	if err != nil {
		return updatepublisher.Result{}, err
	}
	options.Work = work
	result, err := updatepublisher.Run(ctx, options)
	if err != nil {
		return updatepublisher.Result{}, err
	}
	result.RecoveredPending = result.RecoveredPending || recoveredPending
	return result, nil
}
