package updateb2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func authFixture() map[string]interface{} {
	return map[string]interface{}{
		"accountId": "account", "authorizationToken": "secret-token",
		"apiInfo": map[string]interface{}{"storageApi": map[string]interface{}{
			"apiUrl":   "https://api004.backblazeb2.com",
			"s3ApiUrl": "https://s3.us-west-004.backblazeb2.com",
			"allowed": map[string]interface{}{
				"buckets":      []map[string]string{{"id": "bucket-id", "name": "recovery"}},
				"capabilities": []string{"listBuckets", "readBucketRetentions"},
				"namePrefix":   nil,
			},
		}},
	}
}

func bucketFixture() map[string]interface{} {
	return map[string]interface{}{
		"bucketId": "bucket-id", "bucketName": "recovery", "bucketType": "allPrivate",
		"lifecycleRules": []interface{}{},
		"fileLockConfiguration": map[string]interface{}{
			"isClientAuthorizedToRead": true,
			"value": map[string]interface{}{
				"isFileLockEnabled": true,
				"defaultRetention": map[string]interface{}{
					"mode":   "governance",
					"period": map[string]interface{}{"duration": 365, "unit": "days"},
				},
			},
		},
	}
}

func storage(a map[string]interface{}) map[string]interface{} {
	return a["apiInfo"].(map[string]interface{})["storageApi"].(map[string]interface{})
}

func allowed(a map[string]interface{}) map[string]interface{} {
	return storage(a)["allowed"].(map[string]interface{})
}

func lock(b map[string]interface{}) map[string]interface{} {
	return b["fileLockConfiguration"].(map[string]interface{})
}

func lockValue(b map[string]interface{}) map[string]interface{} {
	return lock(b)["value"].(map[string]interface{})
}

func retention(b map[string]interface{}) map[string]interface{} {
	return lockValue(b)["defaultRetention"].(map[string]interface{})
}

func TestCheckBucket(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]interface{}, map[string]interface{})
		want   string
	}{
		{"valid", func(a, b map[string]interface{}) {}, ""},
		{"compliance", func(a, b map[string]interface{}) { retention(b)["mode"] = "compliance" }, "governance"},
		{"lock disabled", func(a, b map[string]interface{}) { lockValue(b)["isFileLockEnabled"] = false }, "Object Lock"},
		{"no period", func(a, b map[string]interface{}) { retention(b)["period"] = nil }, "365"},
		{"short retention", func(a, b map[string]interface{}) {
			retention(b)["period"] = map[string]interface{}{"duration": 30, "unit": "days"}
		}, "365"},
		{"wrong unit", func(a, b map[string]interface{}) {
			retention(b)["period"] = map[string]interface{}{"duration": 365, "unit": "years"}
		}, "365"},
		{"filtered lock", func(a, b map[string]interface{}) { lock(b)["isClientAuthorizedToRead"] = false }, "not readable"},
		{"missing value", func(a, b map[string]interface{}) { lock(b)["value"] = nil }, "not readable"},
		{"missing enabled", func(a, b map[string]interface{}) { delete(lockValue(b), "isFileLockEnabled") }, "not readable"},
		{"wrong access", func(a, b map[string]interface{}) { b["bucketType"] = "allPublic" }, "public/private"},
		{"wrong identity", func(a, b map[string]interface{}) { b["bucketId"] = "other" }, "identity"},
		{"lifecycle deletion", func(a, b map[string]interface{}) {
			b["lifecycleRules"] = []interface{}{map[string]interface{}{"daysFromHidingToDeleting": 1}}
		}, "lifecycle"},
		{"missing lifecycle", func(a, b map[string]interface{}) { delete(b, "lifecycleRules") }, "lifecycle"},
		{"all buckets key", func(a, b map[string]interface{}) { allowed(a)["buckets"] = nil }, "exactly"},
		{"multiple buckets", func(a, b map[string]interface{}) {
			allowed(a)["buckets"] = []map[string]string{{"id": "bucket-id", "name": "recovery"}, {"id": "other", "name": "other"}}
		}, "exactly"},
		{"wrong key bucket", func(a, b map[string]interface{}) {
			allowed(a)["buckets"] = []map[string]string{{"id": "other", "name": "other"}}
		}, "exactly"},
		{"prefix", func(a, b map[string]interface{}) { allowed(a)["namePrefix"] = "backup/" }, "prefix"},
		{"missing capability", func(a, b map[string]interface{}) {
			allowed(a)["capabilities"] = []string{"listBuckets"}
		}, "readBucketRetentions"},
		{"foreign api", func(a, b map[string]interface{}) { storage(a)["apiUrl"] = "https://attacker.example" }, "endpoint"},
		{"wrong region", func(a, b map[string]interface{}) { storage(a)["s3ApiUrl"] = "https://s3.us-east-005.backblazeb2.com" }, "endpoint"},
		{"missing token", func(a, b map[string]interface{}) { a["authorizationToken"] = "" }, "identity"},
	}
	for _, capability := range []string{"writeFiles", "deleteFiles", "writeBuckets", "writeKeys",
		"writeBucketRetentions", "writeFileRetentions", "bypassGovernance", "all", "unknownFuturePermission"} {
		tests = append(tests, struct {
			name   string
			mutate func(map[string]interface{}, map[string]interface{})
			want   string
		}{capability, func(a, b map[string]interface{}) {
			allowed(a)["capabilities"] = []string{"listBuckets", "readBucketRetentions", capability}
		}, "allowlist"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, b := authFixture(), bucketFixture()
			tt.mutate(a, b)
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var result interface{} = a
				switch calls {
				case 1:
					id, secret, ok := r.BasicAuth()
					if r.Method != http.MethodGet || r.URL.String() != authorizeURL || !ok || id != "key-id" || secret != "secret-key" {
						t.Fatal("unexpected authorization request")
					}
				case 2:
					if r.Method != http.MethodPost || r.URL.String() != "https://api004.backblazeb2.com/b2api/v4/b2_list_buckets" ||
						r.Header.Get("Authorization") != "secret-token" {
						t.Fatal("unexpected bucket request")
					}
					var request map[string]string
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
						len(request) != 2 || request["bucketId"] != "bucket-id" || request["accountId"] != "account" {
						t.Fatal("bucket listing is not restricted to authorized ID")
					}
					result = map[string]interface{}{"buckets": []interface{}{b}}
				default:
					t.Fatal("unexpected request: preflight must not mutate storage")
				}
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
			})}
			err := (checker{client: client}).check(context.Background(), "recovery",
				"https://s3.us-west-004.backblazeb2.com", "key-id", "secret-key", true)
			if tt.want == "" {
				if err != nil || calls != 2 {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v; want %q", err, tt.want)
			}
		})
	}
}

func TestRequestFailuresDoNotExposeSecrets(t *testing.T) {
	for _, scenario := range []string{"status", "transport", "json", "oversize", "redirect", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			client := &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					response := &http.Response{StatusCode: 200, Header: make(http.Header)}
					body := "secret-key secret-token"
					switch scenario {
					case "status":
						response.StatusCode = 403
					case "transport":
						return nil, errors.New(body)
					case "json":
						body = `{"secret-key":`
					case "oversize":
						body = strings.Repeat("x", responseLimit+1)
					case "redirect":
						response.StatusCode = 307
						response.Header.Set("Location", "https://attacker.example/secret-key")
					case "canceled":
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					response.Body = io.NopCloser(strings.NewReader(body))
					return response, nil
				}),
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if scenario != "canceled" {
				ctx = context.Background()
			}
			var result authorization
			err := (checker{client: client}).request(ctx, authorizeURL, "key-id", "secret-key", "", nil, &result)
			if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "secret-token") || calls > 1 {
				t.Fatalf("unsafe result: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPublicBucket(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		a, b := authFixture(), bucketFixture()
		b["bucketType"] = "allPublic"
		lockValue(b)["isFileLockEnabled"] = enabled
		lockValue(b)["defaultRetention"] = map[string]interface{}{"mode": nil, "period": nil}
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			var result interface{} = a
			if calls == 2 {
				result = map[string]interface{}{"buckets": []interface{}{b}}
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}
		err := (checker{client: client}).check(context.Background(), "recovery",
			"https://s3.us-west-004.backblazeb2.com", "id", "secret", false)
		if enabled && (err == nil || !strings.Contains(err.Error(), "disabled")) {
			t.Fatalf("accepted locked metadata bucket: %v", err)
		}
		if !enabled && (err != nil || calls != 2) {
			t.Fatalf("rejected metadata bucket: %v, calls=%d", err, calls)
		}
	}
}

func TestValidB2URL(t *testing.T) {
	for _, raw := range []string{
		"http://api.backblazeb2.com", "https://backblazeb2.com", "https://api.backblazeb2.com.attacker.example",
		"https://user:secret@api.backblazeb2.com", "https://api.backblazeb2.com:443",
		"https://api.backblazeb2.com/path", "https://api.backblazeb2.com?token=secret",
		"https://api.backblazeb2.com#secret", "https://api.backblazeb2.com?",
	} {
		if validB2URL(raw) {
			t.Errorf("accepted %s", raw)
		}
	}
	if !validB2URL("https://api004.backblazeb2.com") {
		t.Fatal("rejected ordinary B2 endpoint")
	}
}

func TestRunRejectsMissingConfigurationBeforeNetwork(t *testing.T) {
	var output bytes.Buffer
	err := Check(context.Background(), "metadata", "recovery", "https://s3.us-west-004.backblazeb2.com", func(string) string { return "" }, &output)
	if err == nil || !strings.Contains(err.Error(), "B2_METADATA_CHECK_KEY_ID") || output.Len() != 0 {
		t.Fatalf("got %v, output=%s", err, &output)
	}
	if err := Check(context.Background(), "", "", "", func(string) string { return "" }, &output); err == nil {
		t.Fatal("accepted missing flags")
	}
}
