// Package webhook provides the outbound HTTP delivery primitives shared by
// every server feature that posts to operator-supplied URLs (alert channels,
// scheduled report delivery, ...).
//
// All callers must go through ValidateURL (directly or via Poster) so the
// SSRF guard is enforced in exactly one place.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// allowPrivateTargets disables the loopback/private-network guard. It exists
// only so tests can post to httptest servers on 127.0.0.1.
var allowPrivateTargets atomic.Bool

// SetAllowPrivateTargets toggles the private-network guard. Production code
// must never call this; it is intended for test initialization only.
func SetAllowPrivateTargets(allow bool) {
	allowPrivateTargets.Store(allow)
}

// privateNetworks lists RFC 1918, RFC 4193 and link-local ranges that
// operator-supplied URLs may not target.
var privateNetworks = mustParseCIDRs(
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
	"fe80::/10",
	"169.254.0.0/16",
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("webhook: invalid built-in CIDR %q: %v", cidr, err))
		}
		nets = append(nets, n)
	}
	return nets
}

// IsPrivateIP reports whether ip is loopback, link-local, or in a private range.
func IsPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, n := range privateNetworks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateURL checks that rawURL is an http(s) URL that does not target
// localhost or a private network, and returns a normalized copy rebuilt from
// its parsed components (which also breaks static-analysis taint chains).
//
// Hostnames that fail DNS resolution are accepted: the request will fail on
// its own, and rejecting them would make validation depend on transient DNS.
func ValidateURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("URL scheme must be http or https, got %q", parsed.Scheme)
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("URL must have a hostname")
	}

	if !allowPrivateTargets.Load() {
		if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
			return "", fmt.Errorf("localhost URLs are not allowed for webhooks")
		}
		if ips, err := net.LookupIP(hostname); err == nil {
			for _, ip := range ips {
				if IsPrivateIP(ip) {
					return "", fmt.Errorf("webhook URLs cannot target private/internal networks")
				}
			}
		}
	}

	safeURL := &url.URL{
		Scheme:   parsed.Scheme,
		Host:     parsed.Host,
		Path:     parsed.Path,
		RawQuery: parsed.RawQuery,
		Fragment: parsed.Fragment,
	}
	return safeURL.String(), nil
}

// Poster sends POST requests to validated URLs with a bounded retry loop.
// The zero value is usable and applies the defaults documented on each field.
type Poster struct {
	// Client performs the requests. Defaults to DefaultClient().
	Client *http.Client
	// MaxRetries is the total number of attempts. Defaults to 3.
	MaxRetries int
	// RetryDelay is the pause between attempts. Defaults to 5s.
	RetryDelay time.Duration
}

// maxRedirects bounds how many redirects the default client will follow.
const maxRedirects = 5

// DefaultClient returns an HTTP client with a 30s timeout that re-applies
// ValidateURL to every redirect target, so a public URL cannot bounce the
// request to localhost or a private network.
func DefaultClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if _, err := ValidateURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

func (p Poster) withDefaults() Poster {
	if p.Client == nil {
		p.Client = DefaultClient()
	}
	if p.MaxRetries <= 0 {
		p.MaxRetries = 3
	}
	if p.RetryDelay <= 0 {
		p.RetryDelay = 5 * time.Second
	}
	return p
}

// PostJSON marshals payload and posts it as application/json.
func (p Poster) PostJSON(ctx context.Context, rawURL string, headers map[string]string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}
	return p.Post(ctx, rawURL, "application/json", headers, data)
}

// Post validates rawURL and posts body with the given content type. Any 2xx
// response is success; transport errors and other statuses are retried until
// MaxRetries attempts have been made or ctx is cancelled.
func (p Poster) Post(ctx context.Context, rawURL, contentType string, headers map[string]string, body []byte) error {
	validatedURL, err := ValidateURL(rawURL)
	if err != nil {
		return fmt.Errorf("webhook URL validation failed: %w", err)
	}
	p = p.withDefaults()

	var lastErr error
	for attempt := 1; attempt <= p.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, validatedURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := p.Client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			err = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
		lastErr = err

		if attempt < p.MaxRetries {
			select {
			case <-ctx.Done():
				return fmt.Errorf("request cancelled after %d attempts: %w", attempt, lastErr)
			case <-time.After(p.RetryDelay):
			}
		}
	}
	return fmt.Errorf("request failed after %d attempts: %w", p.MaxRetries, lastErr)
}
