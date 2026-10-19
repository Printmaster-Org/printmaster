package updatetrust

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func publicMirrorFixture(t *testing.T) (RestoreOptions, *PublicReader, *memoryPublicationStore) {
	t.Helper()
	_, opts, recovery, public := deliveryFixture(t)
	opts.Bootstrap, opts.ExpectedPreviousSHA256 = true, ""
	if err := DeliverPublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	id, err := SealedIdentity(opts.Directory, opts.Root)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := public.files[r.URL.Path[1:]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	reader, err := NewPublicReader(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	reader.client = server.Client()
	reader.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_ = recovery
	return RestoreOptions{Root: opts.Root, ExpectedTimestampSHA256: id.TimestampSHA256,
		ExpectedRecordSHA256: id.RecordSHA256, ExpectedTimestampVersion: id.Version}, reader, public
}

func TestPublicMirrorAuthenticatedTimestampLastAndFailures(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "tampered dependency", "missing inventory", "missing timestamp", "rollback", "same-version conflict", "expiry", "lock"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			opts, reader, public := publicMirrorFixture(t)
			directory := t.TempDir()
			id := PublicationIdentity{RootSHA256: digest(opts.Root), TimestampSHA256: opts.ExpectedTimestampSHA256,
				RecordSHA256: opts.ExpectedRecordSHA256, Version: opts.ExpectedTimestampVersion}
			switch scenario {
			case "tampered dependency":
				public.files["1.root.json"] = []byte("changed")
			case "missing inventory":
				delete(public.files, "records/"+id.TimestampSHA256+".json")
			case "missing timestamp":
				delete(public.files, "timestamp.json")
			case "rollback", "same-version conflict":
				ts := metadata.Timestamp(time.Now().Add(time.Hour))
				ts.Signed.Version = id.Version
				if scenario == "rollback" {
					ts.Signed.Version++
				}
				data, err := ts.ToBytes(false)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "timestamp.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				id.Version++
			case "lock":
				if err := os.WriteFile(filepath.Join(directory, ".publisher.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := MirrorPublication(context.Background(), directory, opts.Root, id, reader)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if err := CheckMirrorFreshness(directory, opts.Root, 48*time.Hour); err != nil {
					t.Fatal(err)
				}
				if err := CheckMirrorFreshness(directory, opts.Root, 7*24*time.Hour); err == nil {
					t.Fatal("exact timestamp lifetime margin should fail")
				}
				if err := MirrorPublication(context.Background(), directory, opts.Root, id, reader); err != nil {
					t.Fatal("idempotent mirror failed", err)
				}
			} else if err == nil {
				t.Fatal("invalid mirror succeeded")
			}
			if scenario != "success" && scenario != "rollback" && scenario != "same-version conflict" {
				if _, err := os.Stat(filepath.Join(directory, "timestamp.json")); !os.IsNotExist(err) {
					t.Fatal("failed mirror changed timestamp")
				}
			}
		})
	}
}

func TestPublicReaderConfinesPathsAndRejectsRedirects(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/?x=1", "https://example.com/../x"} {
		if _, err := NewPublicReader(base); err == nil {
			t.Fatal("unsafe source URL accepted")
		}
	}
	opts, reader, _ := publicMirrorFixture(t)
	for _, name := range []string{"../root.pem", "publications/invalid/timestamp.json", "root.pem", "records/../timestamp.json"} {
		if _, err := reader.GetPinned(context.Background(), name, opts.ExpectedTimestampSHA256); err == nil {
			t.Fatal("unsafe source name accepted")
		}
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.invalid", http.StatusFound) }))
	defer redirect.Close()
	r, err := NewPublicReader(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.client = redirect.Client()
	r.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if _, err := r.GetPinned(context.Background(), "records/"+opts.ExpectedTimestampSHA256+".json", opts.ExpectedRecordSHA256); err == nil {
		t.Fatal("redirect accepted")
	}
}
