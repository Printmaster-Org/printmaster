package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestBaselineRangeExpansion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text      string
		ips, normalized []string
	}{
		{"cidr-host-bits", "192.0.2.3/30", []string{"192.0.2.0", "192.0.2.1", "192.0.2.2", "192.0.2.3"}, []string{"192.0.2.3/30"}},
		{"single-host-cidr", "192.0.2.7/32", []string{"192.0.2.7"}, []string{"192.0.2.7/32"}},
		{"full-range", "192.0.2.1 - 192.0.2.3", []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"}, []string{"192.0.2.1-192.0.2.3"}},
		{"one-octet", "192.0.2.1-3", []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"}, []string{"192.0.2.1-192.0.2.3"}},
		{"two-octets", "192.0.2.254-3.1", []string{"192.0.2.254", "192.0.2.255", "192.0.3.0", "192.0.3.1"}, []string{"192.0.2.254-192.0.3.1"}},
		{"three-octets", "192.0.2.254-0.3.1", []string{"192.0.2.254", "192.0.2.255", "192.0.3.0", "192.0.3.1"}, []string{"192.0.2.254-192.0.3.1"}},
		{"order-dedup-comments", "# comment\n\n192.0.2.2\n192.0.2.1-3\n192.0.2.2", []string{"192.0.2.2", "192.0.2.1", "192.0.2.3"}, []string{"192.0.2.2", "192.0.2.1-192.0.2.3", "192.0.2.2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRangeText(tc.text, 256)
			if err != nil || len(got.Errors) != 0 {
				t.Fatalf("parse errors = %v, %#v", err, got.Errors)
			}
			if got.Count != len(tc.ips) || !reflect.DeepEqual(got.IPs, tc.ips) || !reflect.DeepEqual(got.Normalized, tc.normalized) {
				t.Fatalf("result = %#v, want IPs %#v, normalized %#v", got, tc.ips, tc.normalized)
			}
		})
	}
	for _, wildcard := range []string{"192.0.2.x", "192.0.2.*"} {
		got, err := ParseRangeText(wildcard, 256)
		if err != nil || len(got.Errors) != 0 || got.Count != 256 || got.IPs[0] != "192.0.2.0" || got.IPs[255] != "192.0.2.255" {
			t.Fatalf("wildcard %q = %#v, %v", wildcard, got, err)
		}
	}
}

func TestBaselineRangeLineErrors(t *testing.T) {
	t.Parallel()
	text := "# skip\ninvalid\n2001:db8::/126\n192.0.2.3-1\n192.0.2.1-x\n192.0.2.1\n192.0.2.1/99\n192.0.2.1-999\n192.0.x"
	got, err := ParseRangeText(text, 256)
	if err != nil {
		t.Fatal(err)
	}
	want := []ParseError{
		{Line: 2, Msg: "unrecognized format or invalid IPv4"},
		{Line: 3, Msg: "IPv6 not supported"},
		{Line: 4, Msg: "end address is before start address"},
		{Line: 5, Msg: "wildcard not allowed in shorthand right side"},
		{Line: 7, Msg: "invalid CIDR"},
		{Line: 8, Msg: "invalid end IP after shorthand expansion"},
		{Line: 9, Msg: "wildcard allowed only on last octet like 192.168.1.x"},
	}
	if !reflect.DeepEqual(got.Errors, want) || !reflect.DeepEqual(got.IPs, []string{"192.0.2.1"}) || got.Count != 1 {
		t.Fatalf("result = %#v, want errors %#v plus valid line", got, want)
	}
}

func TestBaselineRangeLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		limit      int
		wantIPs    []string
		errorPart  string
	}{
		{"single-over", "192.0.2.1\n192.0.2.2", 1, []string{"192.0.2.1"}, "line 2: expansion would exceed max 1"},
		{"cidr-over", "192.0.2.9\n192.0.2.0/30", 4, []string{"192.0.2.9"}, "line 2: expansion would produce 5 addresses"},
		{"dash-over", "192.0.2.9\n192.0.2.1-4", 4, []string{"192.0.2.9"}, "line 2: expansion would produce 5 addresses"},
		{"wildcard-over", "192.0.2.x", 255, nil, "line 1: expansion would exceed max 255"},
		{"zero", "192.0.2.1", 0, nil, "line 1: expansion would exceed max 0"},
		{"negative", "192.0.2.1", -1, nil, "line 1: expansion would exceed max -1"},
		{"huge-cidr", "0.0.0.0/0", 10000, nil, "over max 10000"},
		// Known limit inconsistency: CIDR/range/wildcard check gross expansion
		// before dedup, so overlap can exceed limit despite fitting uniquely.
		// This is current behavior, not the intended future planner policy.
		{"overlap-cidr", "192.0.2.0/30\n192.0.2.0/30", 4, []string{"192.0.2.0", "192.0.2.1", "192.0.2.2", "192.0.2.3"}, "line 2: expansion would produce 8 addresses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRangeText(tc.text, tc.limit)
			if err == nil || !strings.Contains(err.Error(), tc.errorPart) {
				t.Fatalf("error = %v, want %q", err, tc.errorPart)
			}
			if !reflect.DeepEqual(got.IPs, tc.wantIPs) || got.Count != len(tc.wantIPs) || len(got.Errors) != 0 {
				t.Fatalf("partial result = %#v, want IPs %#v", got, tc.wantIPs)
			}
		})
	}
	got, err := ParseRangeText("192.0.2.1\n192.0.2.1", 1)
	if err != nil || got.Count != 1 || len(got.Normalized) != 2 {
		t.Fatalf("duplicate single at limit = %#v, %v", got, err)
	}
	// Do not execute ranges ending in 255.255.255.255: uint32 loop wraps.
	// Likewise avoid huge expansions; these are remaining safety gaps, not
	// behavior to bless with hanging or memory-exhausting tests.
}
