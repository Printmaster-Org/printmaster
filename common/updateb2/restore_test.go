package updateb2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPinnedRecoveryFindsRetainedVersionBehindPoisonAndHide(t *testing.T) {
	t.Parallel()
	want := []byte("{}")
	pin := fmt.Sprintf("%x", sha256.Sum256(want))
	calls := 0
	s := fixtureStore(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/b2_list_file_versions") {
			var input map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["maxFileCount"] != float64(10) || input["prefix"] != "records/abc.json" {
				t.Fatal("unbounded/unscoped recovery listing")
			}
			info := retainedInfo()
			if calls == 1 {
				if _, exists := input["startFileId"]; exists {
					t.Fatal("unexpected initial cursor")
				}
				hide := info
				hide.ID, hide.Action = "hidden", "hide"
				info.ID = "poison"
				return jsonResponse(t, map[string]interface{}{
					"files": []fileInfo{hide, info}, "nextFileName": info.Name, "nextFileId": "old",
				}), nil
			}
			if input["startFileId"] != "old" {
				t.Fatal("incorrect version cursor")
			}
			info.ID = "old"
			return jsonResponse(t, map[string]interface{}{"files": []fileInfo{info}}), nil
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "secret-token" {
			t.Fatal("unexpected recovery request")
		}
		body := "{}"
		if r.URL.Query().Get("fileId") == "poison" {
			body = "[]"
		} else if r.URL.Query().Get("fileId") != "old" {
			t.Fatal("unexpected version")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	data, err := s.GetPinned(context.Background(), "records/abc.json", pin)
	if err != nil || string(data) != string(want) || calls != 4 {
		t.Fatalf("data=%s err=%v calls=%d", data, err, calls)
	}
}

func TestPinnedRecoveryBoundsAndFailures(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong hash", "expired", "filtered retention", "compliance",
		"repeat cursor", "missing cursor", "repeat version", "wrong bucket", "wrong name", "missing files",
		"version limit", "download limit", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			listings, downloads := 0, 0
			pin := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
			if scenario == "wrong hash" || scenario == "version limit" || scenario == "download limit" {
				pin = strings.Repeat("a", 64)
			}
			ctx := context.Background()
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			s := fixtureStore(t, func(r *http.Request) (*http.Response, error) {
				if scenario == "canceled" {
					return nil, r.Context().Err()
				}
				if strings.HasSuffix(r.URL.Path, "/b2_list_file_versions") {
					listings++
					if scenario == "missing files" {
						return jsonResponse(t, map[string]interface{}{}), nil
					}
					info := retainedInfo()
					if scenario == "repeat cursor" {
						info.ID = fmt.Sprintf("version-%d", listings)
					}
					switch scenario {
					case "missing":
						return jsonResponse(t, map[string]interface{}{"files": []fileInfo{}}), nil
					case "expired":
						info.Uploaded = time.Now().Add(-400 * 24 * time.Hour).UnixMilli()
						info.Retention.Value.Until = time.Now().Add(-time.Hour).UnixMilli()
					case "filtered retention":
						info.Retention.Readable = false
					case "compliance":
						info.Retention.Value.Mode = "compliance"
					case "wrong bucket":
						info.BucketID = "other"
					case "wrong name":
						info.Name = "unrelated"
					}
					result := map[string]interface{}{"files": []fileInfo{info}}
					if strings.Contains(scenario, "cursor") || scenario == "repeat version" {
						result["nextFileName"], result["nextFileId"] = info.Name, "next"
						if scenario == "missing cursor" {
							result["nextFileId"] = ""
						}
					}
					if scenario == "version limit" || scenario == "download limit" {
						infos := make([]fileInfo, 10)
						for i := range infos {
							infos[i] = info
							infos[i].ID = fmt.Sprintf("version-%d-%d", listings, i)
							if scenario == "download limit" {
								infos[i].Length = objectLimit
							}
						}
						result["files"], result["nextFileName"], result["nextFileId"] = infos, info.Name, fmt.Sprintf("next-%d", listings)
					}
					return jsonResponse(t, result), nil
				}
				downloads++
				body := "{}"
				if scenario == "download limit" {
					body = strings.Repeat("x", objectLimit)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if strings.Contains(scenario, "cursor") || scenario == "repeat version" {
				pin = strings.Repeat("a", 64)
			}
			_, err := s.GetPinned(ctx, "records/abc.json", pin)
			if err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if scenario == "version limit" && (listings != 10 || downloads != 100 || !strings.Contains(err.Error(), "100-version")) {
				t.Fatalf("wrong version bound: listings=%d downloads=%d err=%v", listings, downloads, err)
			}
			if scenario == "download limit" && (downloads != 4 || !strings.Contains(err.Error(), "64 MiB")) {
				t.Fatalf("wrong download bound: downloads=%d err=%v", downloads, err)
			}
		})
	}
}

func TestRecoveryReaderPolicyAndWriteBlock(t *testing.T) {
	for _, scenario := range []string{"valid", "inspection only", "writer", "bypass"} {
		t.Run(scenario, func(t *testing.T) {
			a, b := authFixture(), bucketFixture()
			storage(a)["downloadUrl"] = "https://f004.backblazeb2.com"
			caps := []string{"listBuckets", "readBucketRetentions", "listFiles", "readFiles", "readFileRetentions"}
			switch scenario {
			case "inspection only":
				caps = caps[:2]
			case "writer":
				caps = append(caps, "writeFiles")
			case "bypass":
				caps = append(caps, "bypassGovernance")
			}
			allowed(a)["capabilities"] = caps
			calls := 0
			c := checker{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() == authorizeURL {
					return jsonResponse(t, a), nil
				}
				return jsonResponse(t, map[string]interface{}{"buckets": []interface{}{b}}), nil
			})}}
			reader, err := c.openStore(context.Background(), StoreOptions{
				Bucket: "recovery", S3Endpoint: "https://s3.us-west-004.backblazeb2.com", KeyID: "id", ApplicationKey: "secret", Recovery: true,
			}, true)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("over/underprivileged reader accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before := calls
			if err := reader.Put(context.Background(), "record.json", []byte("{}")); err == nil || calls != before {
				t.Fatal("read-only store attempted upload")
			}
		})
	}
}
