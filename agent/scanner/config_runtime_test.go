package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	pmsettings "printmaster/common/settings"
)

func TestRuntimeSNMPSettingsOverrideEnvironment(t *testing.T) {
	runtimeSNMPSettings.RLock()
	previous := runtimeSNMPSettings.settings
	runtimeSNMPSettings.RUnlock()
	t.Cleanup(func() {
		runtimeSNMPSettings.Lock()
		runtimeSNMPSettings.settings = previous
		runtimeSNMPSettings.Unlock()
	})
	t.Setenv("SNMP_VERSION", "2c")
	t.Setenv("SNMP_COMMUNITY", "environment")
	settings := pmsettings.DefaultSettings().SNMP
	settings.Version = "3"
	settings.Username = "fleet-user"
	settings.SecurityLevel = "authPriv"
	settings.AuthProtocol = "SHA256"
	settings.PrivProtocol = "AES"
	settings.AuthPassword = "authentication"
	settings.PrivPassword = "privacy-password"
	settings.ContextName = "fleet-context"
	SetSNMPSettings(settings)
	cfg, err := GetSNMPConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 2*time.Second || cfg.Version != gosnmp.Version3 || cfg.Username != settings.Username || cfg.SecurityLevel != gosnmp.AuthPriv ||
		cfg.AuthProtocol != gosnmp.SHA256 || cfg.PrivProtocol != gosnmp.AES || cfg.AuthPassword != settings.AuthPassword ||
		cfg.PrivPassword != settings.PrivPassword || cfg.ContextName != settings.ContextName {
		t.Fatalf("runtime SNMPv3 configuration not applied: %+v", cfg)
	}
	settings.Version = "1"
	settings.Community = "fleet-community"
	SetSNMPSettings(settings)
	cfg, err = GetSNMPConfig()
	if err != nil || cfg.Version != gosnmp.Version1 || cfg.Community != settings.Community || cfg.Username != "" {
		t.Fatal("switching back to SNMPv1 retained stale v3/environment settings")
	}
	settings.TimeoutMS = 500
	SetSNMPSettings(settings)
	cfg, err = GetSNMPConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewSNMPClientWithContext(context.Background(), cfg, "127.0.0.1", 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	transport := client.(*gosnmpClient).conn
	if transport.Timeout != 500*time.Millisecond || transport.Retries != 0 {
		t.Fatalf("transport ignored fleet timeout/zero retries: %s, %d", transport.Timeout, transport.Retries)
	}
}
