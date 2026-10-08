package settings

import (
	"reflect"
	"strings"
	"testing"
)

func TestSchemaCoversEveryEditableSetting(t *testing.T) {
	fields := map[string]FieldMeta{}
	for _, field := range DefaultSchema().Fields {
		if _, exists := fields[field.Path]; exists {
			t.Fatalf("duplicate metadata: %s", field.Path)
		}
		fields[field.Path] = field
	}
	settings := reflect.ValueOf(DefaultSettings())
	for i := 0; i < settings.NumField(); i++ {
		section := settings.Type().Field(i).Tag.Get("json")
		value := settings.Field(i)
		for j := 0; j < value.NumField(); j++ {
			key := strings.Split(value.Type().Field(j).Tag.Get("json"), ",")[0]
			if key == "detected_subnet" {
				continue
			}
			path := section + "." + key
			meta, ok := fields[path]
			if !ok {
				t.Fatalf("missing setting metadata: %s", path)
			}
			if !reflect.DeepEqual(meta.Default, value.Field(j).Interface()) {
				t.Fatalf("metadata default differs for %s", path)
			}
		}
	}
}

func TestAgentLocalDiscoveryPreferences(t *testing.T) {
	local := DefaultSettings()
	local.Discovery.ShowDiscoverButtonAnyway = true
	local.Discovery.ShowDiscoveredDevicesAnyway = true
	local.Discovery.DetectedSubnet = "192.0.2.0/24"
	snapshot := local
	StripAgentLocalFields(&snapshot)
	if snapshot.Discovery.ShowDiscoverButtonAnyway || snapshot.Discovery.ShowDiscoveredDevicesAnyway || snapshot.Discovery.DetectedSubnet != "" {
		t.Fatal("snapshot must strip local display preferences and detected subnet")
	}
	CopyAgentLocalFields(local, &snapshot)
	if !reflect.DeepEqual(snapshot, local) {
		t.Fatal("local fields were not restored")
	}
}

func TestSpoolerBoundsAndMerge(t *testing.T) {
	for _, test := range []struct{ input, want int }{{0, 5}, {4, 5}, {45, 45}, {301, 300}} {
		cfg := DefaultSettings()
		cfg.Spooler.PollIntervalSeconds = test.input
		Sanitize(&cfg)
		if cfg.Spooler.PollIntervalSeconds != test.want {
			t.Fatalf("poll interval %d: got %d want %d", test.input, cfg.Spooler.PollIntervalSeconds, test.want)
		}

	}
	override := DefaultSettings()
	override.Spooler.Enabled = false
	override.Spooler.PollIntervalSeconds = 90
	if merged := Merge(DefaultSettings(), override); merged.Spooler != override.Spooler {
		t.Fatal("merge ignored spooler settings")
	}
}

func TestInvalidRuntimeSettingsAreRejected(t *testing.T) {
	for _, field := range []string{"version", "security_level", "auth_protocol", "priv_protocol", "asset_id_regex"} {
		t.Run(field, func(t *testing.T) {
			cfg := DefaultSettings()
			switch field {
			case "version":
				cfg.SNMP.Version = "4"
			case "security_level":
				cfg.SNMP.SecurityLevel = "invalid"
			case "auth_protocol":
				cfg.SNMP.AuthProtocol = "invalid"
			case "priv_protocol":
				cfg.SNMP.PrivProtocol = "invalid"
			case "asset_id_regex":
				cfg.Features.AssetIDRegex = "["
			}
			if len(Validate(cfg)) == 0 {
				t.Fatal("invalid runtime settings must not be accepted")
			}
		})
	}
}

func TestSNMPAliasesUseCanonicalUIValues(t *testing.T) {
	cfg := DefaultSettings()
	cfg.SNMP.Version = "v3"
	cfg.SNMP.SecurityLevel = "AUTHPRIV"
	cfg.SNMP.AuthProtocol = "sha1"
	cfg.SNMP.PrivProtocol = "aes128"
	cfg.Discovery.MetricsRescanIntervalSeconds = -1
	Sanitize(&cfg)
	if cfg.SNMP.Version != "3" || cfg.SNMP.SecurityLevel != "authPriv" || cfg.SNMP.AuthProtocol != "SHA" || cfg.SNMP.PrivProtocol != "AES" || cfg.Discovery.MetricsRescanIntervalSeconds != 0 {
		t.Fatalf("noncanonical settings survived normalization: %+v", cfg)
	}
}

func TestDefaultSettings(t *testing.T) {
	def := DefaultSettings()
	if !def.Discovery.SubnetScan {
		t.Fatal("expected subnet_scan default true")
	}
	if def.Logging.Level != "info" {
		t.Fatalf("unexpected log level %s", def.Logging.Level)
	}
	if !def.Features.CredentialsEnabled {
		t.Fatal("credentials enabled default expected true")
	}
}

func TestSanitize(t *testing.T) {
	s := DefaultSettings()
	s.Discovery.MetricsRescanIntervalMinutes = 999999
	s.Discovery.MetricsRescanIntervalSeconds = 5 // too low, should clamp to 15
	s.SNMP.TimeoutMS = -10
	s.SNMP.Retries = -1
	s.Discovery.Concurrency = 0
	Sanitize(&s)
	if s.Discovery.MetricsRescanIntervalMinutes != 1440 {
		t.Fatalf("interval not clamped, got %d", s.Discovery.MetricsRescanIntervalMinutes)
	}
	if s.Discovery.MetricsRescanIntervalSeconds != 15 {
		t.Fatalf("seconds interval not clamped to min 15, got %d", s.Discovery.MetricsRescanIntervalSeconds)
	}
	if s.SNMP.TimeoutMS != 500 {
		t.Fatalf("timeout not clamped, got %d", s.SNMP.TimeoutMS)
	}
	if s.SNMP.Retries != 0 {
		t.Fatalf("retries not clamped, got %d", s.SNMP.Retries)
	}
	if s.Discovery.Concurrency != 1 {
		t.Fatalf("concurrency not clamped, got %d", s.Discovery.Concurrency)
	}
}

func TestDefaultSchemaIncludesCriticalFields(t *testing.T) {
	schema := DefaultSchema()
	if schema.Version == "" {
		t.Fatal("schema version empty")
	}
	checks := map[string]bool{
		"discovery.subnet_scan":                 false,
		"discovery.autosave_discovered_devices": false,
		"logging.level":                         false,
	}
	for _, field := range schema.Fields {
		if _, ok := checks[field.Path]; ok {
			checks[field.Path] = true
		}
	}
	for path, seen := range checks {
		if !seen {
			t.Fatalf("expected metadata entry for %s", path)
		}
	}
}
