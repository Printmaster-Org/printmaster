package agent

import (
	"fmt"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMPClient abstracts gosnmp for easier testing/mocking.
type SNMPClient interface {
	Connect() error
	Get(oids []string) (*gosnmp.SnmpPacket, error)
	Walk(root string, walkFn gosnmp.WalkFunc) error
	Close() error
}

// NewSNMPClient is a factory used by production code; tests can replace this
// variable to inject mock clients.
var NewSNMPClient = func(cfg *SNMPConfig, target string, timeoutSeconds int) (SNMPClient, error) {
	// Ensure a minimum SNMP timeout to be tolerant of slow devices/networks.
	tsec := timeoutSeconds
	if tsec < 30 {
		tsec = 30
	}
	snmp := &gosnmp.GoSNMP{
		Target:  target,
		Port:    161,
		Version: cfg.Version,
		Timeout: time.Duration(tsec) * time.Second,
		// increase retries to be more tolerant on lossy networks
		Retries: 3,
	}

	// Configure based on SNMP version
	if cfg.Version == gosnmp.Version3 {
		// SNMPv3 configuration
		snmp.SecurityModel = gosnmp.UserSecurityModel
		snmp.MsgFlags = cfg.SecurityLevel
		snmp.ContextName = cfg.ContextName

		snmp.SecurityParameters = &gosnmp.UsmSecurityParameters{
			UserName:                 cfg.Username,
			AuthenticationProtocol:   cfg.AuthProtocol,
			AuthenticationPassphrase: cfg.AuthPassword,
			PrivacyProtocol:          cfg.PrivProtocol,
			PrivacyPassphrase:        cfg.PrivPassword,
		}
	} else {
		// SNMPv1/v2c configuration
		snmp.Community = cfg.Community
	}

	if err := snmp.Connect(); err != nil {
		return nil, err
	}
	return &gosnmpWrapper{snmp: snmp}, nil
}

// gosnmpWrapper implements SNMPClient by delegating to gosnmp.GoSNMP.
type gosnmpWrapper struct {
	snmp *gosnmp.GoSNMP
}

func (w *gosnmpWrapper) Connect() error {
	if w.snmp == nil {
		return fmt.Errorf("nil gosnmp client")
	}
	return nil
}

func (w *gosnmpWrapper) Get(oids []string) (*gosnmp.SnmpPacket, error) {
	return w.snmp.Get(oids)
}

func (w *gosnmpWrapper) Walk(root string, walkFn gosnmp.WalkFunc) error {
	return w.snmp.Walk(root, walkFn)
}

// BulkWalk uses GETBULK on v2c/v3 and falls back to GETNEXT on v1.
func (w *gosnmpWrapper) BulkWalk(root string, walkFn gosnmp.WalkFunc) error {
	if w.snmp.Version == gosnmp.Version1 {
		return w.snmp.Walk(root, walkFn)
	}
	return w.snmp.BulkWalk(root, walkFn)
}

func (w *gosnmpWrapper) Close() error {
	if w.snmp != nil && w.snmp.Conn != nil {
		_ = w.snmp.Conn.Close()
	}
	return nil
}

// PingFunc allows tests to override ping behavior.
type PingFunc func(ip string, logFn func(string)) bool

// DoPing is the package-level ping function used by the scanner. Tests may
// replace this with a fake implementation that returns deterministic results.
var DoPing PingFunc = pingWithExec
