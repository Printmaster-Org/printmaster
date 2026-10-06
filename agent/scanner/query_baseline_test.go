package scanner

import (
	"context"
	"reflect"
	"testing"

	"printmaster/agent/scanner/capabilities"
	"printmaster/agent/scanner/vendor"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// A deterministic vendor exposes composition without freezing a vendor's
// evolving OID catalog. No factory/global mutation or SNMP transport involved.
type baselineVendor struct {
	vendor.GenericVendor
	metricCaps *capabilities.DeviceCapabilities
}

func (*baselineVendor) BaseOIDs() []string      { return []string{"base"} }
func (*baselineVendor) SupplyOIDs() []string    { return []string{"supply"} }
func (*baselineVendor) PaperTrayOIDs() []string { return []string{"tray"} }
func (v *baselineVendor) MetricOIDs(caps *capabilities.DeviceCapabilities) []string {
	v.metricCaps = caps
	return []string{"metric"}
}

func TestBaselineProfileOIDComposition(t *testing.T) {
	t.Parallel()
	for _, profile := range []QueryProfile{QueryMinimal, QueryEssential, QueryFull, QueryMetrics, QueryProfile(99)} {
		t.Run(profile.String(), func(t *testing.T) {
			t.Parallel()
			caps := &capabilities.DeviceCapabilities{IsColor: true}
			module := &baselineVendor{}
			var want []string
			switch profile {
			case QueryMinimal:
				want = appendUniqueOIDs([]string{oids.PrtGeneralSerialNumber}, VendorIDTargetOIDs()...)
			case QueryEssential:
				want = appendUniqueOIDs([]string{"base", "supply", "tray", oids.PrtMarkerLifeCount}, VendorIDTargetOIDs()...)
			case QueryMetrics:
				want = []string{"base", "metric", "supply", "tray"}
			}
			if got := buildQueryOIDsWithModule(profile, caps, module); !reflect.DeepEqual(got, want) {
				t.Fatalf("OIDs = %#v, want %#v", got, want)
			}
			if profile == QueryMetrics {
				if module.metricCaps != caps {
					t.Fatal("metrics did not forward capability pointer")
				}
			} else if module.metricCaps != nil {
				t.Fatal("non-metrics profile invoked metric selector")
			}
			// Essential currently does NOT call MetricOIDs, despite the vendor
			// interface comment saying metrics apply to both profiles. This is
			// baseline composition, not a decision to preserve that mismatch.
		})
	}
}

func TestBaselineProfileQueryOperations(t *testing.T) {
	t.Parallel()
	for _, profile := range []QueryProfile{QueryMinimal, QueryEssential, QueryFull, QueryMetrics} {
		t.Run(profile.String(), func(t *testing.T) {
			t.Parallel()
			client := &mockSNMPClient{
				getResult: &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{{Name: oids.PrtGeneralSerialNumber, Value: "serial"}}},
				walkPDUs:  []gosnmp.SnmpPDU{{Name: oids.SysDescr, Value: "printer"}},
			}
			factoryCalls := 0
			result, err := queryDeviceWithCapabilitiesAndClient(context.Background(), "192.0.2.20", profile, "", 7, nil, func(_ *SNMPConfig, target string, timeout int) (SNMPClient, error) {
				factoryCalls++
				if target != "192.0.2.20" || timeout != 7 {
					t.Errorf("factory args = %q, %d", target, timeout)
				}
				return client, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if factoryCalls != 1 || result.IP != "192.0.2.20" || result.Profile != profile || result.VendorHint != "" {
				t.Fatalf("result=%#v, factory calls=%d", result, factoryCalls)
			}
			preflight := []string{oids.SysObjectID, oids.SysDescr, oids.HrDeviceDescr}
			if !reflect.DeepEqual(client.getInputs[0], preflight) {
				t.Fatalf("preflight = %v, want %v", client.getInputs[0], preflight)
			}
			module := &vendor.GenericVendor{}
			var wantRoots, wantScalars []string
			if profile == QueryFull {
				wantRoots = []string{"1.3.6.1.2.1", "1.3.6.1.2.1.43"}
			} else {
				tables := map[string]bool{}
				if profile == QueryEssential || profile == QueryMetrics {
					for _, root := range append(module.SupplyOIDs(), module.PaperTrayOIDs()...) {
						tables[root] = true
					}
				}
				for _, oid := range buildQueryOIDsWithModule(profile, nil, module) {
					if tables[oid] {
						wantRoots = append(wantRoots, oid)
					} else {
						wantScalars = append(wantScalars, oid)
					}
				}
			}
			if !reflect.DeepEqual(client.walkRoots, wantRoots) {
				t.Fatalf("walks = %v, want %v", client.walkRoots, wantRoots)
			}
			wantGets := [][]string{preflight}
			wantGets = append(wantGets, clusterOIDs(wantScalars, defaultOIDBatchSize)...)
			if !reflect.DeepEqual(client.getInputs, wantGets) {
				t.Fatalf("GETs = %v, want %v", client.getInputs, wantGets)
			}
			wantCaps := profile == QueryFull || profile == QueryEssential
			if (result.Capabilities != nil) != wantCaps {
				t.Fatalf("capabilities present = %v, want %v", result.Capabilities != nil, wantCaps)
			}
		})
	}
}

func TestBaselineFullProfileEnterpriseWalk(t *testing.T) {
	t.Parallel()
	client := &mockSNMPClient{
		getResult: &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{{Name: "." + oids.SysObjectID, Value: ".1.3.6.1.4.1.11.2.3"}}},
		walkPDUs:  []gosnmp.SnmpPDU{{Name: oids.PrtGeneralSerialNumber, Value: "serial"}},
	}
	_, err := queryDeviceWithCapabilitiesAndClient(context.Background(), "192.0.2.20", QueryFull, "", 7, nil, func(*SNMPConfig, string, int) (SNMPClient, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1.3.6.1.2.1", "1.3.6.1.2.1.43", "1.3.6.1.4.1.11"}
	if !reflect.DeepEqual(client.walkRoots, want) || client.getCalls != 1 {
		t.Fatalf("walk roots=%v, GETs=%d", client.walkRoots, client.getCalls)
	}
}
