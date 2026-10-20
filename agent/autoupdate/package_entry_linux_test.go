package autoupdate

import (
	"context"
	"testing"
)

func TestPrivilegedRepositoryPackageHelperConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{`{"schema":1,"metadata_url":"https://updates.example/metadata"}`, true},
		{"{\n\"schema\": 1, \"metadata_url\": \"https://updates.example/metadata/\"\n}", true},
		{`{"schema":2,"metadata_url":"https://updates.example"}`, false},
		{`{"schema":1,"metadata_url":"http://updates.example"}`, false},
		{`{"schema":1,"metadata_url":"https://user:pass@updates.example"}`, false},
		{`{"schema":1,"metadata_url":"https://updates.example?token=x"}`, false},
		{`{"schema":1,"metadata_url":"https://updates.example?"}`, false},
		{`{"schema":1,"metadata_url":"https://updates.example#fragment"}`, false},
		{`{"schema":1,"metadata_url":"https://updates.example","root":"untrusted"}`, false},
		{`{"schema":1,"schema":1,"metadata_url":"https://updates.example"}`, false},
		{`{"Schema":1,"metadata_url":"https://updates.example"}`, false},
		{`{"schema":1,"metadata_url":null}`, false},
		{`{"schema":1,"metadata_url":"https://updates.example"}{}`, false},
		{`null`, false},
	} {
		t.Run(test.body, func(t *testing.T) {
			_, err := decodePackageHelperConfiguration([]byte(test.body))
			if (err == nil) != test.valid {
				t.Fatalf("configuration result: %v", err)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageHelperRejectsArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--root", "untrusted"}, {"install"}, {"--help"}, {"--state=/tmp/arbitrary"}} {
		if err := RunRepositoryPackageHelper(context.Background(), args, nil, nil); err == nil {
			t.Fatalf("helper accepted request-controlled arguments: %v", args)
		}
	}
}
