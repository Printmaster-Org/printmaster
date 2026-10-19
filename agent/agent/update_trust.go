package agent

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"printmaster/common/updatetrust"
)

var ErrPublisherRootUnprovisioned = errors.New("embedded publisher root has not been provisioned")

// NewPublisherVerifier uses only the build's root as initial authority. The
// authenticated Server transports metadata but cannot supply bootstrap trust.
func (c *ServerClient) NewPublisherVerifier(root []byte, cacheDir string) (*updatetrust.Client, error) {
	if len(bytes.TrimSpace(root)) == 0 || bytes.Equal(bytes.TrimSpace(root), []byte("null")) {
		return nil, ErrPublisherRootUnprovisioned
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && base.Scheme != "http") {
		return nil, fmt.Errorf("invalid Server origin for publisher metadata")
	}
	if c.HTTPClient == nil {
		return nil, fmt.Errorf("Server HTTP client required for publisher metadata")
	}
	client := *c.HTTPClient
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &publisherMetadataTransport{server: c, origin: base, base: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameMetadataOrigin(req.URL, base) {
			return fmt.Errorf("publisher metadata redirect changes authenticated Server origin")
		}
		if c.HTTPClient.CheckRedirect != nil {
			return c.HTTPClient.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("publisher metadata exceeded redirect limit")
		}
		return nil
	}
	return updatetrust.NewClient(updatetrust.Options{
		TrustedRoot: root, CacheDir: cacheDir, HTTPClient: &client,
		MetadataURL: strings.TrimRight(c.BaseURL, "/") + updatetrust.MetadataRoute,
	})
}

type publisherMetadataTransport struct {
	server *ServerClient
	origin *url.URL
	base   http.RoundTripper
}

func sameMetadataOrigin(candidate, origin *url.URL) bool {
	return candidate.Scheme == origin.Scheme && strings.EqualFold(candidate.Host, origin.Host) && candidate.User == nil
}

func (t *publisherMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !sameMetadataOrigin(req.URL, t.origin) {
		return nil, fmt.Errorf("publisher metadata must use authenticated Server origin")
	}
	prefix := strings.TrimRight(t.origin.Path, "/") + updatetrust.MetadataRoute
	if !strings.HasPrefix(req.URL.Path, prefix) || !updatetrust.ValidMetadataName(strings.TrimPrefix(req.URL.Path, prefix)) ||
		req.URL.RawQuery != "" || req.URL.Fragment != "" {
		return nil, fmt.Errorf("publisher metadata request must name a supported metadata file")
	}
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.server.GetToken())
	return t.base.RoundTrip(cloned)
}
