package agent

import (
	"reflect"
	"testing"
)

func TestMergeStatusMessagesVendorFirstDeduped(t *testing.T) {
	got := mergeStatusMessages(
		[]string{"Paper low", "Error: Paper jam (Epson error 4)"},
		[]string{"Error: Paper jam (Epson error 4)", "Ready: Idle (Epson status 4)", ""},
	)
	want := []string{"Error: Paper jam (Epson error 4)", "Ready: Idle (Epson status 4)", "Paper low"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}
