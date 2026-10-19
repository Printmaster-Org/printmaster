package updateb2

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"
)

func retainedInfo() fileInfo {
	now := time.Now().Truncate(time.Second)
	info := fileInfo{ID: "version-1", BucketID: "bucket-id", Name: "records/abc.json",
		Action: "upload", Length: 2, Uploaded: now.UnixMilli()}
	info.Retention.Readable = true
	info.Retention.Value.Mode = "governance"
	info.Retention.Value.Until = now.Add(365 * 24 * time.Hour).UnixMilli()
	return info
}

func fixtureStore(t *testing.T, transport roundTripFunc) *Store {
	t.Helper()
	var auth authorization
	a := authFixture()
	storage(a)["downloadUrl"] = "https://f004.backblazeb2.com"
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	return &Store{checker: checker{client: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, auth: auth, bucketID: "bucket-id", recovery: true}
}

func jsonResponse(t *testing.T, value interface{}) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}
}

func TestB2VersionRead(t *testing.T) {
	for _, scenario := range []string{"valid", "absent", "hidden", "wrong bucket", "retention filtered",
		"compliance", "expired", "short retention", "short download", "missing listing", "foreign result"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			info := retainedInfo()
			switch scenario {
			case "hidden":
				info.Action = "hide"
			case "wrong bucket":
				info.BucketID = "other"
			case "retention filtered":
				info.Retention.Readable = false
			case "compliance":
				info.Retention.Value.Mode = "compliance"
			case "expired":
				info.Uploaded = time.Now().Add(-400 * 24 * time.Hour).UnixMilli()
				info.Retention.Value.Until = time.Now().Add(-time.Hour).UnixMilli()
			case "short retention":
				info.Retention.Value.Until--
			case "foreign result":
				info.Name += "-other"
			}
			s := fixtureStore(t, func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "secret-token" {
					t.Fatal("missing account token")
				}
				if strings.HasSuffix(r.URL.Path, "/b2_list_file_versions") {
					if r.Method != http.MethodPost {
						t.Fatal("not a POST")
					}
					var input map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil ||
						input["bucketId"] != "bucket-id" || input["prefix"] != "records/abc.json" ||
						input["startFileName"] != "records/abc.json" || input["maxFileCount"] != float64(1) {
						t.Fatal("incorrect version listing request")
					}
					if scenario == "missing listing" {
						return jsonResponse(t, map[string]interface{}{}), nil
					}
					files := []fileInfo{info}
					if scenario == "absent" {
						files = []fileInfo{}
					}
					return jsonResponse(t, map[string]interface{}{"files": files}), nil
				}
				if r.Method != http.MethodGet || r.URL.Host != "f004.backblazeb2.com" ||
					r.URL.Query().Get("fileId") != "version-1" {
					t.Fatal("not an exact version download")
				}
				body := "{}"
				if scenario == "short download" {
					body = "{"
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			data, err := s.Get(context.Background(), "records/abc.json")
			switch scenario {
			case "valid":
				if err != nil || string(data) != "{}" {
					t.Fatalf("got %s, %v", data, err)
				}
			case "absent", "foreign result":
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("got %v", err)
				}
			default:
				if err == nil || errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("unsafe missing/success result: %v", err)
				}
			}
		})
	}
}

func TestB2UploadReceiptAndReadBack(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign host", "http", "userinfo", "wrong upload bucket",
		"wrong version", "tampered", "missing retention", "ambiguous", "query secret", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			info := retainedInfo()
			calls := 0
			s := fixtureStore(t, func(r *http.Request) (*http.Response, error) {
				calls++
				switch calls {
				case 1:
					if !strings.HasSuffix(r.URL.Path, "/b2_get_upload_url") {
						t.Fatal("unexpected upload URL request")
					}
					endpoint := "https://pod-000-1005-03.backblaze.com/b2api/v4/b2_upload_file?cvt=private-query&bucket=bucket-id"
					bucketID := "bucket-id"
					switch scenario {
					case "foreign host":
						endpoint = "https://attacker.example/b2api/v4/b2_upload_file"
					case "http":
						endpoint = strings.Replace(endpoint, "https:", "http:", 1)
					case "userinfo":
						endpoint = strings.Replace(endpoint, "https://", "https://secret:password@", 1)
					case "wrong upload bucket":
						bucketID = "other"
					}
					return jsonResponse(t, map[string]string{
						"uploadUrl": endpoint, "authorizationToken": "upload-secret", "bucketId": bucketID,
					}), nil
				case 2:
					if r.Method != http.MethodPost || r.Header.Get("Authorization") != "upload-secret" ||
						r.Header.Get("X-Bz-File-Name") != info.Name || r.ContentLength != 2 {
						t.Fatal("incorrect upload headers or length")
					}
					sum := sha1.Sum([]byte("{}"))
					if r.Header.Get("X-Bz-Content-Sha1") != hex.EncodeToString(sum[:]) {
						t.Fatal("incorrect B2 transport checksum")
					}
					if scenario == "ambiguous" || scenario == "query secret" {
						return nil, errors.New("private-query upload-secret")
					}
					if scenario == "redirect" {
						return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://attacker.example"}},
							Body: io.NopCloser(strings.NewReader("upload-secret"))}, nil
					}
					return jsonResponse(t, info), nil
				case 3:
					if !strings.HasSuffix(r.URL.Path, "/b2_get_file_info") {
						t.Fatal("upload receipt not checked independently")
					}
					actual := info
					if scenario == "wrong version" {
						actual.ID = "other"
					}
					if scenario == "missing retention" {
						actual.Retention.Readable = false
					}
					return jsonResponse(t, actual), nil
				case 4:
					body := "{}"
					if scenario == "tampered" {
						body = "[]"
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				default:
					t.Fatal("unexpected retry or extra request")
					return nil, errors.New("unexpected")
				}
			})
			err := s.Put(context.Background(), info.Name, []byte("{}"))
			if scenario == "valid" {
				if err != nil || calls != 4 {
					t.Fatalf("got %v, calls=%d", err, calls)
				}
			} else if err == nil {
				t.Fatal("unsafe upload accepted")
			}
			if err != nil && (strings.Contains(err.Error(), "private-query") || strings.Contains(err.Error(), "upload-secret")) {
				t.Fatal("secret exposed in error")
			}
			if (scenario == "ambiguous" || scenario == "redirect") && calls != 2 {
				t.Fatal("ambiguous upload retried automatically")
			}
		})
	}
}

func TestDeliveryCredentialPolicy(t *testing.T) {
	for _, scenario := range []string{"valid", "inspection only", "bypass", "retention writer", "delete", "key creation"} {
		t.Run(scenario, func(t *testing.T) {
			a, b := authFixture(), bucketFixture()
			caps := []string{"listBuckets", "readBucketRetentions", "listFiles", "readFiles", "writeFiles", "readFileRetentions"}
			switch scenario {
			case "inspection only":
				caps = caps[:2]
			case "bypass":
				caps = append(caps, "bypassGovernance")
			case "retention writer":
				caps = append(caps, "writeFileRetentions")
			case "delete":
				caps = append(caps, "deleteFiles")
			case "key creation":
				caps = append(caps, "writeKeys")
			}
			allowed(a)["capabilities"] = caps
			c := checker{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == authorizeURL {
					return jsonResponse(t, a), nil
				}
				return jsonResponse(t, map[string]interface{}{"buckets": []interface{}{b}}), nil
			})}}
			_, err := c.inspect(context.Background(), "recovery", "https://s3.us-west-004.backblazeb2.com", "id", "secret", true, true)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestB2InvalidObjectNames(t *testing.T) {
	for _, name := range []string{"", "../root.pem", "/timestamp.json", "a//b", "a\\b", "a?token=secret", "a/../b", "é.json"} {
		if validObjectName(name) {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"timestamp.json", "12.root.json", "publications/abc/record.json", "records/abc.json"} {
		if !validObjectName(name) {
			t.Errorf("rejected %q", name)
		}
	}
}
