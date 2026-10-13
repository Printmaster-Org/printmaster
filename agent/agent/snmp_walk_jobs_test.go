package agent

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gosnmp/gosnmp"
)

type fakeWalkClient struct {
	mu      sync.Mutex
	trees   map[string][]gosnmp.SnmpPDU
	walked  []string
	block   chan struct{}
	bulk    bool
	walkErr error
}

func (f *fakeWalkClient) Connect() error { return nil }
func (f *fakeWalkClient) Get([]string) (*gosnmp.SnmpPacket, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeWalkClient) Close() error { return nil }

func (f *fakeWalkClient) Walk(root string, fn gosnmp.WalkFunc) error {
	f.mu.Lock()
	f.walked = append(f.walked, root)
	f.mu.Unlock()
	if f.walkErr != nil {
		return f.walkErr
	}
	for _, pdu := range f.trees[root] {
		if f.block != nil {
			<-f.block
		}
		if err := fn(pdu); err != nil {
			return err
		}
	}
	return nil
}

type fakeBulkWalkClient struct{ *fakeWalkClient }

func (f fakeBulkWalkClient) BulkWalk(root string, fn gosnmp.WalkFunc) error {
	f.mu.Lock()
	f.bulk = true
	f.mu.Unlock()
	return f.Walk(root, fn)
}

func walkPDUs(prefix string, n int) []gosnmp.SnmpPDU {
	out := make([]gosnmp.SnmpPDU, n)
	for i := range out {
		out[i] = gosnmp.SnmpPDU{Name: "." + prefix + "." + strconv.Itoa(i+1), Type: gosnmp.Integer, Value: i}
	}
	return out
}

func readWalkFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open walk file: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	var doc map[string]interface{}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("walk file is not valid JSON: %v", err)
	}
	return doc
}

func TestNormalizeSNMPWalkRequest(t *testing.T) {
	req, err := NormalizeSNMPWalkRequest(SNMPWalkRequest{IP: " 10.0.0.5 "})
	if err != nil {
		t.Fatal(err)
	}
	if req.IP != "10.0.0.5" || req.MaxOIDs != DefaultSNMPWalkMaxOIDs {
		t.Fatalf("defaults not applied: %+v", req)
	}
	if strings.Join(req.Roots, ",") != strings.Join(DefaultSNMPWalkRoots, ",") {
		t.Fatalf("default roots = %v", req.Roots)
	}

	req, err = NormalizeSNMPWalkRequest(SNMPWalkRequest{
		IP:      "10.0.0.5",
		Roots:   []string{".1.3.6.1.2.1.43", "1.3.6.1.2.1", "1.3.6.1.4.1.1248", "1.3.6.1.4.1.1248"},
		MaxOIDs: 250000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(req.Roots, ","); got != "1.3.6.1.2.1,1.3.6.1.4.1.1248" {
		t.Fatalf("nested/duplicate roots not collapsed: %s", got)
	}
	if req.MaxOIDs != 250000 {
		t.Fatalf("custom max_oids lost: %d", req.MaxOIDs)
	}

	for _, bad := range []SNMPWalkRequest{
		{IP: "printer.local"},
		{IP: "10.0.0.5", Roots: []string{"1.3.6.x"}},
		{IP: "10.0.0.5", MaxOIDs: -1},
		{IP: "10.0.0.5", MaxOIDs: MaxSNMPWalkMaxOIDs + 1},
	} {
		if _, err := NormalizeSNMPWalkRequest(bad); !errors.Is(err, ErrSNMPWalkInvalidRequest) {
			t.Errorf("expected invalid request for %+v, got %v", bad, err)
		}
	}
}

func TestSNMPWalkManagerWritesDownloadableFile(t *testing.T) {
	dir := t.TempDir()
	client := &fakeWalkClient{trees: map[string][]gosnmp.SnmpPDU{
		"1.3.6.1.2.1": append(walkPDUs("1.3.6.1.2.1.1", 3), gosnmp.SnmpPDU{
			Name: ".1.3.6.1.4.1.1248.1.2.2.1.1.1.4.1", Type: gosnmp.OctetString, Value: []byte("@BDC ST2\r\n\x01"),
		}),
		"1.3.6.1.4.1": walkPDUs("1.3.6.1.4.1.1248.1", 5),
	}}
	m := NewSNMPWalkManager(dir, func(ip string) (SNMPClient, error) {
		return fakeBulkWalkClient{client}, nil
	}, map[string]string{"agent_version": "test"})

	job, err := m.Start(SNMPWalkRequest{IP: "192.168.2.165", Label: "EPSON ST-C8090"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()

	got, ok := m.Get(job.ID)
	if !ok || got.Status != SNMPWalkStatusComplete {
		t.Fatalf("job = %+v", got)
	}
	if got.OIDCount != 9 || got.Truncated || got.RootCounts["1.3.6.1.4.1"] != 5 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if !client.bulk {
		t.Fatal("expected BulkWalk to be used when available")
	}
	if !strings.Contains(got.FileName, "192-168-2-165_epson-st-c8090_") {
		t.Fatalf("file name = %s", got.FileName)
	}

	path, _, err := m.FilePath(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc := readWalkFile(t, path)
	if doc["format"] != SNMPWalkFormat || doc["agent_version"] != "test" || doc["ip"] != "192.168.2.165" {
		t.Fatalf("header = %v", doc)
	}
	entries := doc["entries"].([]interface{})
	if len(entries) != 9 {
		t.Fatalf("entries = %d", len(entries))
	}
	st2 := entries[3].(map[string]interface{})
	if st2["oid"] != "1.3.6.1.4.1.1248.1.2.2.1.1.1.4.1" || !strings.HasPrefix(st2["hex_value"].(string), "4042444320535432") {
		t.Fatalf("binary PDU not preserved as hex: %v", st2)
	}
	summary := doc["summary"].(map[string]interface{})
	if summary["oid_count"].(float64) != 9 || summary["status"] != SNMPWalkStatusComplete {
		t.Fatalf("summary = %v", summary)
	}

	// Results survive a restart.
	reloaded := NewSNMPWalkManager(dir, nil, nil)
	if j, ok := reloaded.Get(job.ID); !ok || j.Status != SNMPWalkStatusComplete || j.FileSize == 0 {
		t.Fatalf("reloaded job = %+v ok=%v", j, ok)
	}

	if err := reloaded.Delete(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("walk file not removed: %v", err)
	}
}

func TestSNMPWalkManagerHonoursMaxOIDs(t *testing.T) {
	dir := t.TempDir()
	client := &fakeWalkClient{trees: map[string][]gosnmp.SnmpPDU{
		"1.3.6.1.2.1": walkPDUs("1.3.6.1.2.1.1", 10),
		"1.3.6.1.4.1": walkPDUs("1.3.6.1.4.1.1", 10),
	}}
	m := NewSNMPWalkManager(dir, func(string) (SNMPClient, error) { return client, nil }, nil)
	job, err := m.Start(SNMPWalkRequest{IP: "10.0.0.9", MaxOIDs: 15})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _ := m.Get(job.ID)
	if got.Status != SNMPWalkStatusComplete || !got.Truncated || got.OIDCount != 15 {
		t.Fatalf("job = %+v", got)
	}
	path, _, _ := m.FilePath(job.ID)
	doc := readWalkFile(t, path)
	if n := len(doc["entries"].([]interface{})); n != 15 {
		t.Fatalf("entries = %d", n)
	}
}

func TestSNMPWalkManagerCancelAndBusy(t *testing.T) {
	dir := t.TempDir()
	block := make(chan struct{})
	client := &fakeWalkClient{block: block, trees: map[string][]gosnmp.SnmpPDU{
		"1.3.6.1.2.1": walkPDUs("1.3.6.1.2.1.1", 5),
	}}
	m := NewSNMPWalkManager(dir, func(string) (SNMPClient, error) { return client, nil }, nil)
	job, err := m.Start(SNMPWalkRequest{IP: "10.0.0.7", Roots: []string{"1.3.6.1.2.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(SNMPWalkRequest{IP: "10.0.0.7"}); !errors.Is(err, ErrSNMPWalkBusy) {
		t.Fatalf("expected busy error, got %v", err)
	}
	block <- struct{}{}
	if err := m.Delete(job.ID); err != nil {
		t.Fatal(err)
	}
	close(block)
	m.Wait()
	got, _ := m.Get(job.ID)
	if got.Status != SNMPWalkStatusCancelled {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestSNMPWalkManagerConnectFailure(t *testing.T) {
	dir := t.TempDir()
	m := NewSNMPWalkManager(dir, func(string) (SNMPClient, error) { return nil, errors.New("timeout") }, nil)
	job, err := m.Start(SNMPWalkRequest{IP: "10.0.0.8"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _ := m.Get(job.ID)
	if got.Status != SNMPWalkStatusFailed || len(got.Errors) == 0 || got.FileName != "" {
		t.Fatalf("job = %+v", got)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json.gz"))
	if len(files) != 0 {
		t.Fatalf("unexpected files: %v", files)
	}
}
