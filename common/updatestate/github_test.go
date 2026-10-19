package updatestate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const goodProtection = `{"enforce_admins":{"enabled":true},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"required_linear_history":{"enabled":true},"restrictions":{"users":[{"login":"owner"}],"teams":[],"apps":[]}}`

func fakeGitHub(t *testing.T, handler func(*http.Request) (int, string)) *GitHub {
	t.Helper()
	g := &GitHub{repository: "owner/state", token: "secret", client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	g.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || !strings.HasPrefix(r.URL.Path, "/repos/owner/state/") ||
			r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("wrong API boundary/header: %s", r.URL.Redacted())
		}
		status, body := handler(r)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)),
			Header: http.Header{"Location": []string{"https://example.invalid/steal"}}, Request: r}, nil
	})
	return g
}

func TestGitHubConditionalContentsBoundary(t *testing.T) {
	t.Parallel()
	data := []byte(`{"schema":1,"generation":0,"parent_blob":"","latest":null,"pending":null}`)
	revision := blob(data)
	writes := 0
	g := fakeGitHub(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/protection") {
			return 200, goodProtection
		}
		if r.Method == http.MethodGet {
			if r.URL.RawQuery != "ref=main" {
				t.Fatal("authority branch not pinned")
			}
			return 200, fmt.Sprintf(`{"type":"file","encoding":"base64","sha":%q,"content":%q,"size":%d}`,
				revision, base64.StdEncoding.EncodeToString(data), len(data))
		}
		writes++
		var request struct{ SHA, Content, Branch string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.SHA != revision || request.Branch != "main" || request.Content != base64.StdEncoding.EncodeToString(data) {
			t.Fatal("write lacked exact conditional revision/content")
		}
		return 200, fmt.Sprintf(`{"content":{"sha":%q}}`, revision)
	})
	actual, rev, err := g.Read(context.Background())
	if err != nil || string(actual) != string(data) || rev != revision {
		t.Fatalf("read failed: %v", err)
	}
	if err := g.Replace(context.Background(), rev, data); err != nil || writes != 1 {
		t.Fatalf("write failed: %v", err)
	}
}

func TestGitHubFailsClosedWithoutLeakingCredentials(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing", "conflict", "rate limit", "server error", "redirect", "oversized",
		"bad JSON", "no protection", "force push", "deletion", "no restrictions", "bad blob", "symlink", "bad write response"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			calls := 0
			g := fakeGitHub(t, func(r *http.Request) (int, string) {
				calls++
				if strings.HasSuffix(r.URL.Path, "/protection") {
					switch scenario {
					case "no protection":
						return 200, "{}"
					case "force push":
						return 200, strings.Replace(goodProtection, `"allow_force_pushes":{"enabled":false}`, `"allow_force_pushes":{"enabled":true}`, 1)
					case "deletion":
						return 200, strings.Replace(goodProtection, `"allow_deletions":{"enabled":false}`, `"allow_deletions":{"enabled":true}`, 1)
					case "no restrictions":
						return 200, strings.Replace(goodProtection, `[{"login":"owner"}]`, `[]`, 1)
					default:
						return 200, goodProtection
					}
				}
				switch scenario {
				case "missing":
					return 404, "secret"
				case "conflict":
					return 409, "secret"
				case "rate limit":
					return 429, "secret"
				case "server error":
					return 503, "secret"
				case "redirect":
					return 302, "secret"
				case "oversized":
					return 200, strings.Repeat("x", (64<<10)+1)
				case "bad JSON":
					return 200, "{"
				case "bad blob":
					return 200, fmt.Sprintf(`{"type":"file","encoding":"base64","sha":%q,"content":"eA==","size":1}`, strings.Repeat("0", 40))
				case "symlink":
					return 200, `{"type":"symlink"}`
				default:
					return 200, "{}"
				}
			})
			var err error
			if scenario == "bad write response" || scenario == "conflict" {
				err = g.Replace(context.Background(), strings.Repeat("a", 40), []byte("{}"))
			} else {
				_, _, err = g.Read(context.Background())
			}
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure hidden or leaked credential: %v", err)
			}
			if calls > 2 {
				t.Fatal("unexpected retry/redirect")
			}
		})
	}
	for _, repository := range []string{"", "owner/repo/extra", "owner/repo?ref=x", "https://example.com/repo", "owner/repo.git"} {
		if _, err := NewGitHub(context.Background(), repository, "secret"); err == nil {
			t.Fatalf("invalid repository accepted: %s", repository)
		}
	}
}

func TestGitHubResponseByteLimit(t *testing.T) {
	t.Parallel()
	for _, length := range []int{64 << 10, (64 << 10) + 1} {
		g := fakeGitHub(t, func(*http.Request) (int, string) {
			return 200, "{}" + strings.Repeat(" ", length-2)
		})
		var result map[string]interface{}
		err := g.request(context.Background(), http.MethodGet, "/contents/state.json?ref=main", nil, &result)
		if (length == 64<<10 && err != nil) || (length > 64<<10 && err == nil) {
			t.Fatalf("wrong response limit at %d: %v", length, err)
		}
	}
}
