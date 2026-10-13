package agent

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Admin SNMP walk jobs capture complete, uncapped-by-default device walks to a
// gzipped JSON file on the agent so they can be downloaded later (for example
// through the server proxy) to build vendor OID mappings.

const (
	SNMPWalkFormat          = "printmaster-snmp-walk/v1"
	DefaultSNMPWalkMaxOIDs  = 100000
	MaxSNMPWalkMaxOIDs      = 2000000
	defaultSNMPWalkKeep     = 50
	defaultSNMPWalkParallel = 2

	SNMPWalkStatusRunning   = "running"
	SNMPWalkStatusComplete  = "complete"
	SNMPWalkStatusFailed    = "failed"
	SNMPWalkStatusCancelled = "cancelled"
)

// DefaultSNMPWalkRoots covers all of MIB-2 (includes Printer-MIB and
// Host-Resources) plus every enterprise subtree.
var DefaultSNMPWalkRoots = []string{"1.3.6.1.2.1", "1.3.6.1.4.1"}

var (
	ErrSNMPWalkInvalidRequest = errors.New("invalid snmp walk request")
	ErrSNMPWalkBusy           = errors.New("snmp walk already running")
	ErrSNMPWalkNotFound       = errors.New("snmp walk not found")
	errSNMPWalkLimit          = errors.New("snmp walk limit reached")
)

// SNMPWalkRequest describes an admin walk.
type SNMPWalkRequest struct {
	IP      string   `json:"ip"`
	Roots   []string `json:"roots,omitempty"`
	MaxOIDs int      `json:"max_oids,omitempty"`
	Label   string   `json:"label,omitempty"`
}

// SNMPWalkJob is the externally visible state of a walk job.
type SNMPWalkJob struct {
	ID          string         `json:"id"`
	IP          string         `json:"ip"`
	Label       string         `json:"label,omitempty"`
	Roots       []string       `json:"roots"`
	MaxOIDs     int            `json:"max_oids"`
	Status      string         `json:"status"`
	OIDCount    int            `json:"oid_count"`
	CurrentRoot string         `json:"current_root,omitempty"`
	RootCounts  map[string]int `json:"root_counts,omitempty"`
	Truncated   bool           `json:"truncated"`
	Errors      []string       `json:"errors,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
	DurationMS  int64          `json:"duration_ms"`
	FileName    string         `json:"file_name,omitempty"`
	FileSize    int64          `json:"file_size,omitempty"`
}

// SNMPWalkClientFactory opens an SNMP client for the target IP.
type SNMPWalkClientFactory func(ip string) (SNMPClient, error)

type snmpWalkEntry struct {
	job    SNMPWalkJob
	cancel context.CancelFunc
}

// SNMPWalkManager runs and tracks admin walk jobs.
type SNMPWalkManager struct {
	mu        sync.Mutex
	dir       string
	jobs      map[string]*snmpWalkEntry
	newClient SNMPWalkClientFactory
	meta      map[string]string
	keep      int
	parallel  int
	now       func() time.Time
	wg        sync.WaitGroup
}

// NewSNMPWalkManager creates a manager storing walks under dir. meta values
// (e.g. agent_version, agent_id) are written into every walk file. Previously
// completed walks found in dir are loaded so they survive agent restarts.
func NewSNMPWalkManager(dir string, newClient SNMPWalkClientFactory, meta map[string]string) *SNMPWalkManager {
	if newClient == nil {
		newClient = defaultSNMPWalkClient
	}
	m := &SNMPWalkManager{
		dir:       dir,
		jobs:      make(map[string]*snmpWalkEntry),
		newClient: newClient,
		meta:      meta,
		keep:      defaultSNMPWalkKeep,
		parallel:  defaultSNMPWalkParallel,
		now:       time.Now,
	}
	m.loadExisting()
	return m
}

func defaultSNMPWalkClient(ip string) (SNMPClient, error) {
	cfg, err := GetSNMPConfig()
	if err != nil {
		return nil, err
	}
	return NewSNMPClient(cfg, ip, 10)
}

// NormalizeSNMPWalkRequest validates the request and applies defaults.
func NormalizeSNMPWalkRequest(req SNMPWalkRequest) (SNMPWalkRequest, error) {
	ip := net.ParseIP(strings.TrimSpace(req.IP))
	if ip == nil {
		return req, fmt.Errorf("%w: ip must be a valid IP address", ErrSNMPWalkInvalidRequest)
	}
	req.IP = ip.String()

	if req.MaxOIDs == 0 {
		req.MaxOIDs = DefaultSNMPWalkMaxOIDs
	}
	if req.MaxOIDs < 0 || req.MaxOIDs > MaxSNMPWalkMaxOIDs {
		return req, fmt.Errorf("%w: max_oids must be between 1 and %d", ErrSNMPWalkInvalidRequest, MaxSNMPWalkMaxOIDs)
	}

	roots := req.Roots
	if len(roots) == 0 {
		roots = DefaultSNMPWalkRoots
	}
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.Trim(strings.TrimSpace(root), ".")
		if root == "" {
			continue
		}
		if !validOID(root) {
			return req, fmt.Errorf("%w: invalid root OID %q", ErrSNMPWalkInvalidRequest, root)
		}
		cleaned = append(cleaned, root)
	}
	if len(cleaned) == 0 {
		cleaned = append(cleaned, DefaultSNMPWalkRoots...)
	}
	req.Roots = collapseOIDRoots(cleaned)

	label := strings.TrimSpace(req.Label)
	if len(label) > 80 {
		label = label[:80]
	}
	req.Label = label
	return req, nil
}

func validOID(oid string) bool {
	parts := strings.Split(oid, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 10 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// collapseOIDRoots removes duplicates and roots nested under another root, so
// nothing is walked twice. Order of first appearance is preserved.
func collapseOIDRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		covered := false
		for _, other := range roots {
			if other != root && strings.HasPrefix(root, other+".") {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == root {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, root)
		}
	}
	return out
}

// Start validates the request and launches the walk in the background.
func (m *SNMPWalkManager) Start(req SNMPWalkRequest) (SNMPWalkJob, error) {
	req, err := NormalizeSNMPWalkRequest(req)
	if err != nil {
		return SNMPWalkJob{}, err
	}

	m.mu.Lock()
	running := 0
	for _, e := range m.jobs {
		if e.job.Status != SNMPWalkStatusRunning {
			continue
		}
		running++
		if e.job.IP == req.IP {
			m.mu.Unlock()
			return SNMPWalkJob{}, fmt.Errorf("%w for %s", ErrSNMPWalkBusy, req.IP)
		}
	}
	if running >= m.parallel {
		m.mu.Unlock()
		return SNMPWalkJob{}, fmt.Errorf("%w: %d walks in progress", ErrSNMPWalkBusy, running)
	}

	started := m.now().UTC()
	id := newSNMPWalkID()
	job := SNMPWalkJob{
		ID:         id,
		IP:         req.IP,
		Label:      req.Label,
		Roots:      req.Roots,
		MaxOIDs:    req.MaxOIDs,
		Status:     SNMPWalkStatusRunning,
		RootCounts: map[string]int{},
		StartedAt:  started,
		FileName:   snmpWalkFileName(req.IP, req.Label, started, id),
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.jobs[id] = &snmpWalkEntry{job: job, cancel: cancel}
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		m.run(ctx, id)
	}()
	return cloneSNMPWalkJob(job), nil
}

// Wait blocks until all running jobs finish (used by tests and shutdown).
func (m *SNMPWalkManager) Wait() { m.wg.Wait() }

// Get returns a job by ID.
func (m *SNMPWalkManager) Get(id string) (SNMPWalkJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return SNMPWalkJob{}, false
	}
	return cloneSNMPWalkJob(e.job), true
}

// List returns all jobs, newest first.
func (m *SNMPWalkManager) List() []SNMPWalkJob {
	m.mu.Lock()
	out := make([]SNMPWalkJob, 0, len(m.jobs))
	for _, e := range m.jobs {
		out = append(out, cloneSNMPWalkJob(e.job))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Delete cancels a running job or removes a finished job and its files.
func (m *SNMPWalkManager) Delete(id string) error {
	m.mu.Lock()
	e, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return ErrSNMPWalkNotFound
	}
	if e.job.Status == SNMPWalkStatusRunning {
		e.cancel()
		m.mu.Unlock()
		return nil
	}
	delete(m.jobs, id)
	fileName := e.job.FileName
	m.mu.Unlock()
	m.removeFiles(id, fileName)
	return nil
}

// FilePath returns the on-disk path of a finished walk.
func (m *SNMPWalkManager) FilePath(id string) (string, SNMPWalkJob, error) {
	job, ok := m.Get(id)
	if !ok {
		return "", job, ErrSNMPWalkNotFound
	}
	if job.Status == SNMPWalkStatusRunning || job.FileName == "" || job.FileSize == 0 {
		return "", job, fmt.Errorf("%w: walk has no downloadable file yet", ErrSNMPWalkNotFound)
	}
	return filepath.Join(m.dir, job.FileName), job, nil
}

func (m *SNMPWalkManager) update(id string, fn func(*SNMPWalkJob)) {
	m.mu.Lock()
	if e, ok := m.jobs[id]; ok {
		fn(&e.job)
	}
	m.mu.Unlock()
}

func (m *SNMPWalkManager) run(ctx context.Context, id string) {
	job, _ := m.Get(id)
	finish := func(status string, size int64, errs []string) {
		finished := m.now().UTC()
		m.update(id, func(j *SNMPWalkJob) {
			j.Status = status
			j.FileSize = size
			j.CurrentRoot = ""
			j.FinishedAt = &finished
			j.DurationMS = finished.Sub(j.StartedAt).Milliseconds()
			j.Errors = append(j.Errors, errs...)
			if size == 0 {
				j.FileName = ""
			}
		})
		m.persistMeta(id)
		m.prune()
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		finish(SNMPWalkStatusFailed, 0, []string{"create walk dir: " + err.Error()})
		return
	}
	client, err := m.newClient(job.IP)
	if err != nil {
		finish(SNMPWalkStatusFailed, 0, []string{"snmp connect: " + err.Error()})
		return
	}
	defer client.Close()

	path := filepath.Join(m.dir, job.FileName)
	size, status, errs := m.writeWalk(ctx, id, job, client, path)
	if status == SNMPWalkStatusFailed && size == 0 {
		_ = os.Remove(path)
	}
	finish(status, size, errs)
}

// writeWalk streams PDUs straight into a gzipped JSON document so large walks
// never have to be held in memory.
func (m *SNMPWalkManager) writeWalk(ctx context.Context, id string, job SNMPWalkJob, client SNMPClient, path string) (int64, string, []string) {
	f, err := os.Create(path)
	if err != nil {
		return 0, SNMPWalkStatusFailed, []string{"create file: " + err.Error()}
	}
	gz := gzip.NewWriter(f)
	bw := bufio.NewWriterSize(gz, 64*1024)

	header := map[string]interface{}{
		"format":     SNMPWalkFormat,
		"id":         job.ID,
		"ip":         job.IP,
		"label":      job.Label,
		"roots":      job.Roots,
		"max_oids":   job.MaxOIDs,
		"started_at": job.StartedAt,
	}
	for k, v := range m.meta {
		header[k] = v
	}
	headerJSON, _ := json.Marshal(header)
	// Re-open the header object so "entries" and "summary" follow it.
	bw.Write(headerJSON[:len(headerJSON)-1])
	bw.WriteString(`,"entries":[`)

	count := 0
	truncated := false
	cancelled := false
	var errs []string
	rootCounts := map[string]int{}
	walkFn := walkFuncFor(client)

	for _, root := range job.Roots {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		m.update(id, func(j *SNMPWalkJob) { j.CurrentRoot = root })
		rootStart := count
		err := walkFn(root, func(pdu gosnmp.SnmpPDU) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if count >= job.MaxOIDs {
				return errSNMPWalkLimit
			}
			b, _ := json.Marshal(PDUToRawPDU(pdu))
			if count > 0 {
				bw.WriteByte(',')
			}
			bw.WriteByte('\n')
			bw.Write(b)
			count++
			if count%200 == 0 {
				c := count
				m.update(id, func(j *SNMPWalkJob) { j.OIDCount = c })
			}
			return nil
		})
		rootCounts[root] = count - rootStart
		switch {
		case errors.Is(err, errSNMPWalkLimit):
			truncated = true
		case errors.Is(err, context.Canceled) || ctx.Err() != nil:
			cancelled = true
		case err != nil:
			errs = append(errs, fmt.Sprintf("%s: %v", root, err))
		}
		c := count
		rc := copyIntMap(rootCounts)
		m.update(id, func(j *SNMPWalkJob) {
			j.OIDCount = c
			j.RootCounts = rc
			j.Truncated = truncated
		})
		if truncated || cancelled {
			break
		}
	}

	status := SNMPWalkStatusComplete
	switch {
	case cancelled:
		status = SNMPWalkStatusCancelled
	case count == 0 && len(errs) > 0:
		status = SNMPWalkStatusFailed
	}

	finished := m.now().UTC()
	summary := map[string]interface{}{
		"status":      status,
		"oid_count":   count,
		"truncated":   truncated,
		"root_counts": rootCounts,
		"errors":      errs,
		"finished_at": finished,
		"duration_ms": finished.Sub(job.StartedAt).Milliseconds(),
	}
	summaryJSON, _ := json.Marshal(summary)
	bw.WriteString("\n],\"summary\":")
	bw.Write(summaryJSON)
	bw.WriteString("}\n")

	if err := bw.Flush(); err != nil {
		errs = append(errs, "write: "+err.Error())
		status = SNMPWalkStatusFailed
	}
	if err := gz.Close(); err != nil {
		errs = append(errs, "gzip: "+err.Error())
		status = SNMPWalkStatusFailed
	}
	if err := f.Close(); err != nil {
		errs = append(errs, "close: "+err.Error())
		status = SNMPWalkStatusFailed
	}
	if count == 0 {
		return 0, status, errs
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, SNMPWalkStatusFailed, append(errs, err.Error())
	}
	return info.Size(), status, errs
}

// bulkWalker is implemented by clients that support GETBULK walking.
type bulkWalker interface {
	BulkWalk(root string, walkFn gosnmp.WalkFunc) error
}

func walkFuncFor(client SNMPClient) func(string, gosnmp.WalkFunc) error {
	if bw, ok := client.(bulkWalker); ok {
		return bw.BulkWalk
	}
	return client.Walk
}

func (m *SNMPWalkManager) metaPath(id string) string {
	return filepath.Join(m.dir, id+".meta.json")
}

func (m *SNMPWalkManager) persistMeta(id string) {
	job, ok := m.Get(id)
	if !ok {
		return
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(m.metaPath(id), data, 0o644)
}

func (m *SNMPWalkManager) removeFiles(id, fileName string) {
	_ = os.Remove(m.metaPath(id))
	if fileName != "" {
		_ = os.Remove(filepath.Join(m.dir, filepath.Base(fileName)))
	}
}

func (m *SNMPWalkManager) loadExisting() {
	matches, _ := filepath.Glob(filepath.Join(m.dir, "*.meta.json"))
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var job SNMPWalkJob
		if json.Unmarshal(data, &job) != nil || job.ID == "" {
			continue
		}
		if job.Status == SNMPWalkStatusRunning {
			// Agent stopped mid-walk; the partial file is not valid JSON.
			job.Status = SNMPWalkStatusFailed
			job.Errors = append(job.Errors, "agent restarted during walk")
			job.FileSize = 0
		}
		job.FileName = filepath.Base(job.FileName)
		m.jobs[job.ID] = &snmpWalkEntry{job: job, cancel: func() {}}
	}
}

func (m *SNMPWalkManager) prune() {
	m.mu.Lock()
	var finished []SNMPWalkJob
	for _, e := range m.jobs {
		if e.job.Status != SNMPWalkStatusRunning {
			finished = append(finished, e.job)
		}
	}
	if len(finished) <= m.keep {
		m.mu.Unlock()
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].StartedAt.After(finished[j].StartedAt) })
	stale := finished[m.keep:]
	for _, j := range stale {
		delete(m.jobs, j.ID)
	}
	m.mu.Unlock()
	for _, j := range stale {
		m.removeFiles(j.ID, j.FileName)
	}
}

func cloneSNMPWalkJob(j SNMPWalkJob) SNMPWalkJob {
	j.Roots = append([]string(nil), j.Roots...)
	j.Errors = append([]string(nil), j.Errors...)
	j.RootCounts = copyIntMap(j.RootCounts)
	if j.FinishedAt != nil {
		t := *j.FinishedAt
		j.FinishedAt = &t
	}
	return j
}

func copyIntMap(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func newSNMPWalkID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func snmpWalkFileName(ip, label string, started time.Time, id string) string {
	parts := []string{"snmpwalk", strings.NewReplacer(".", "-", ":", "-").Replace(ip)}
	if slug := fileSlug(label); slug != "" {
		parts = append(parts, slug)
	}
	parts = append(parts, started.Format("20060102-150405"), id[:6])
	return strings.Join(parts, "_") + ".json.gz"
}

func fileSlug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, c := range strings.ToLower(s) {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if ok {
			b.WriteRune(c)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
