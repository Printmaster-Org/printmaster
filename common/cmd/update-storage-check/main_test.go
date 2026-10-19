package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestInspectionCLIRejectsMissingConfiguration(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"-metadata-bucket", "metadata", "-recovery-bucket", "recovery",
		"-s3-endpoint", "https://s3.us-west-004.backblazeb2.com"}, func(string) string { return "" }, &output)
	if err == nil || !strings.Contains(err.Error(), "B2_METADATA_CHECK_KEY_ID") || output.Len() != 0 {
		t.Fatalf("err=%v output=%s", err, &output)
	}
	if err := run([]string{"unexpected"}, func(string) string { return "" }, &output); err == nil {
		t.Fatal("accepted positional argument")
	}
}
