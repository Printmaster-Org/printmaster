package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// allowPrivate enables posting to httptest servers for the duration of a test.
func allowPrivate(t *testing.T) {
	t.Helper()
	SetAllowPrivateTargets(true)
	t.Cleanup(func() { SetAllowPrivateTargets(false) })
}

func TestValidateURLRejectsUnsafeTargets(t *testing.T) {
	for _, raw := range []string{
		"ftp://example.com/hook",
		"file:///etc/passwd",
		"https:///no-host",
		"http://localhost:8080/hook",
		"http://127.0.0.1/hook",
		"http://[::1]/hook",
		"http://10.1.2.3/hook",
		"http://192.168.1.10/hook",
		"http://172.16.0.5/hook",
		"http://169.254.169.254/latest/meta-data",
		"::not a url",
	} {
		if _, err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) = nil error, want rejection", raw)
		}
	}
}

func TestValidateURLAcceptsPublicAndNormalizes(t *testing.T) {
	got, err := ValidateURL("https://203.0.113.10/hooks/report?token=abc")
	if err != nil {
		t.Fatalf("ValidateURL: %v", err)
	}
	if got != "https://203.0.113.10/hooks/report?token=abc" {
		t.Fatalf("normalized URL = %q", got)
	}
}

func TestPosterSucceedsOn2xxAndSendsHeaders(t *testing.T) {
	allowPrivate(t)
	var gotBody, gotType, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotType, gotAuth = string(b), r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	err := Poster{}.PostJSON(context.Background(), srv.URL, map[string]string{"Authorization": "Bearer x"}, map[string]int{"n": 1})
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if gotBody != `{"n":1}` || gotType != "application/json" || gotAuth != "Bearer x" {
		t.Fatalf("unexpected request: body=%q type=%q auth=%q", gotBody, gotType, gotAuth)
	}
}

func TestPosterRetriesThenFails(t *testing.T) {
	allowPrivate(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := Poster{MaxRetries: 3, RetryDelay: time.Millisecond}
	err := p.Post(context.Background(), srv.URL, "text/plain", nil, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected status error, got %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", calls.Load())
	}
}

func TestPosterRetryRecovers(t *testing.T) {
	allowPrivate(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := Poster{MaxRetries: 3, RetryDelay: time.Millisecond}
	if err := p.Post(context.Background(), srv.URL, "text/plain", nil, nil); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", calls.Load())
	}
}

func TestPosterStopsRetryingWhenCancelled(t *testing.T) {
	allowPrivate(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	p := Poster{MaxRetries: 5, RetryDelay: time.Hour}
	start := time.Now()
	err := p.Post(ctx, srv.URL, "text/plain", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if time.Since(start) > 5*time.Second || calls.Load() != 1 {
		t.Fatalf("retry loop did not stop promptly: attempts=%d elapsed=%v", calls.Load(), time.Since(start))
	}
}

func TestPosterRejectsInvalidURLWithoutRequest(t *testing.T) {
	err := Poster{}.Post(context.Background(), "http://127.0.0.1:1/x", "text/plain", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestDefaultClientBlocksRedirectToPrivateTarget(t *testing.T) {
	// The guard is enabled; the redirect check must reject a hop to loopback.
	// The initial request is issued directly via the client (bypassing
	// Post's own ValidateURL) to simulate a public URL that redirects inward.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect to private target was followed")
	}))
	defer internal.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := DefaultClient().Post(redirector.URL, "text/plain", nil)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected redirect to be blocked")
	}
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
}
