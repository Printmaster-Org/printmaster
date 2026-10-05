package main

import (
	"bytes"
	"io"
	"net/http/httptest"
	"testing"
)

// Smoke test the /static handler to ensure shared bundles are served and
// include expected markers. This verifies our embed wiring (MetricsJS/CardsJS)
// and that we set a conservative cache header for these assets.
func TestHandleStatic_SharedBundles(t *testing.T) {
	cases := []struct {
		path   string
		marker []byte
	}{
		{"/static/metrics.js", []byte("loadDeviceMetrics")},
		{"/static/cards.js", []byte("renderSavedCard")},
		{"/static/progressive-loader.js", []byte("createLoader")},
	}

	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		w := httptest.NewRecorder()

		// call the handler directly
		handleStatic(w, req)

		res := w.Result()
		body, _ := io.ReadAll(res.Body)

		if res.StatusCode != 200 {
			t.Fatalf("expected 200 for %s, got %d", c.path, res.StatusCode)
		}

			if res.Header.Get("Content-Type") != "application/javascript; charset=utf-8" {
				t.Fatalf("expected JavaScript MIME type for %s", c.path)
			}

		if !bytes.Contains(body, c.marker) {
			t.Fatalf("%s did not contain expected marker %q", c.path, string(c.marker))
		}

		cc := res.Header.Get("Cache-Control")
		if cc == "" {
			t.Fatalf("expected Cache-Control header for %s, got none", c.path)
		}
	}
}
