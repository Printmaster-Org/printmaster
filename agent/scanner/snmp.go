package scanner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMPConfig holds SNMP connection parameters.
type SNMPConfig struct {
	// Community is the community string for SNMPv1/v2c
	Community string
	// Version is the SNMP protocol version (gosnmp.Version1, Version2c, Version3)
	Version gosnmp.SnmpVersion

	// SNMPv3 security parameters
	// SecurityLevel: noAuthNoPriv, authNoPriv, or authPriv
	SecurityLevel gosnmp.SnmpV3MsgFlags
	// Username is the SNMPv3 security name (USM user)
	Username string
	// AuthProtocol: MD5, SHA, SHA224, SHA256, SHA384, SHA512
	AuthProtocol gosnmp.SnmpV3AuthProtocol
	// AuthPassword is the authentication passphrase
	AuthPassword string
	// PrivProtocol: DES, AES, AES192, AES256
	PrivProtocol gosnmp.SnmpV3PrivProtocol
	// PrivPassword is the privacy passphrase
	PrivPassword string
	// ContextName is the SNMPv3 context name (optional)
	ContextName string
}

// SNMPClient defines the interface for SNMP operations.
type SNMPClient interface {
	Connect() error
	Get(oids []string) (*gosnmp.SnmpPacket, error)
	Walk(rootOid string, walkFn gosnmp.WalkFunc) error
	Close() error
}

// gosnmpClient wraps gosnmp.GoSNMP to implement SNMPClient.
type gosnmpClient struct {
	conn *gosnmp.GoSNMP
}

// Connect establishes the SNMP connection.
func (c *gosnmpClient) Connect() error {
	return c.conn.Connect()
}

// Get performs an SNMP GET request.
func (c *gosnmpClient) Get(oids []string) (*gosnmp.SnmpPacket, error) {
	return c.conn.Get(oids)
}

// Walk performs an SNMP WALK request.
func (c *gosnmpClient) Walk(rootOid string, walkFn gosnmp.WalkFunc) error {
	return c.conn.Walk(rootOid, walkFn)
}

// Close closes the SNMP connection.
func (c *gosnmpClient) Close() error {
	return c.conn.Conn.Close()
}

// GetSNMPConfig loads SNMP configuration from environment variables.
// Defaults to community="public" and version=v2c if not specified.
func GetSNMPConfig() (*SNMPConfig, error) {
	community := os.Getenv("SNMP_COMMUNITY")
	if community == "" {
		community = "public"
	}

	versionStr := strings.ToLower(os.Getenv("SNMP_VERSION"))
	version := gosnmp.Version2c // default to v2c for better compatibility

	switch versionStr {
	case "", "2c", "v2c", "2":
		version = gosnmp.Version2c
	case "1", "v1":
		version = gosnmp.Version1
	case "3", "v3":
		version = gosnmp.Version3
	default:
		return nil, fmt.Errorf("unsupported SNMP version: %s (use 1, 2c, or 3)", versionStr)
	}

	cfg := &SNMPConfig{
		Community: community,
		Version:   version,
	}

	// SNMPv3 configuration from environment
	if version == gosnmp.Version3 {
		cfg.Username = os.Getenv("SNMP_USERNAME")
		cfg.ContextName = os.Getenv("SNMP_CONTEXT_NAME")

		// Security level
		secLevel := strings.ToLower(os.Getenv("SNMP_SECURITY_LEVEL"))
		switch secLevel {
		case "authpriv":
			cfg.SecurityLevel = gosnmp.AuthPriv
		case "authnopriv":
			cfg.SecurityLevel = gosnmp.AuthNoPriv
		default:
			cfg.SecurityLevel = gosnmp.NoAuthNoPriv
		}

		// Auth protocol
		authProto := strings.ToUpper(os.Getenv("SNMP_AUTH_PROTOCOL"))
		switch authProto {
		case "MD5":
			cfg.AuthProtocol = gosnmp.MD5
		case "SHA", "SHA1":
			cfg.AuthProtocol = gosnmp.SHA
		case "SHA224":
			cfg.AuthProtocol = gosnmp.SHA224
		case "SHA256":
			cfg.AuthProtocol = gosnmp.SHA256
		case "SHA384":
			cfg.AuthProtocol = gosnmp.SHA384
		case "SHA512":
			cfg.AuthProtocol = gosnmp.SHA512
		default:
			cfg.AuthProtocol = gosnmp.NoAuth
		}
		cfg.AuthPassword = os.Getenv("SNMP_AUTH_PASSWORD")

		// Privacy protocol
		privProto := strings.ToUpper(os.Getenv("SNMP_PRIV_PROTOCOL"))
		switch privProto {
		case "DES":
			cfg.PrivProtocol = gosnmp.DES
		case "AES", "AES128":
			cfg.PrivProtocol = gosnmp.AES
		case "AES192":
			cfg.PrivProtocol = gosnmp.AES192
		case "AES256":
			cfg.PrivProtocol = gosnmp.AES256
		case "AES192C":
			cfg.PrivProtocol = gosnmp.AES192C
		case "AES256C":
			cfg.PrivProtocol = gosnmp.AES256C
		default:
			cfg.PrivProtocol = gosnmp.NoPriv
		}
		cfg.PrivPassword = os.Getenv("SNMP_PRIV_PASSWORD")
	}

	return cfg, nil
}

// newSNMPClientImpl is the actual implementation of NewSNMPClient.
func newSNMPClientImpl(cfg *SNMPConfig, target string, timeoutSeconds int) (SNMPClient, error) {
	return NewSNMPClientWithContext(context.Background(), cfg, target, timeoutSeconds, 3)
}

// NewSNMPClientWithContext bounds compact monitoring requests and their retries.
func NewSNMPClientWithContext(ctx context.Context, cfg *SNMPConfig, target string, timeoutSeconds, retries int) (SNMPClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("SNMP config required")
	}
	if target == "" {
		return nil, fmt.Errorf("target IP required")
	}

	timeout := timeoutSeconds
	if timeout == 0 {
		timeout = 30
	}

	conn := &gosnmp.GoSNMP{
		Context: ctx,
		Target:  target,
		Port:    161,
		Version: cfg.Version,
		Timeout: time.Duration(timeout) * time.Second,
		Retries: retries,
	}

	// Configure based on SNMP version
	if cfg.Version == gosnmp.Version3 {
		// SNMPv3 configuration
		conn.SecurityModel = gosnmp.UserSecurityModel
		conn.MsgFlags = cfg.SecurityLevel
		conn.ContextName = cfg.ContextName

		conn.SecurityParameters = &gosnmp.UsmSecurityParameters{
			UserName:                 cfg.Username,
			AuthenticationProtocol:   cfg.AuthProtocol,
			AuthenticationPassphrase: cfg.AuthPassword,
			PrivacyProtocol:          cfg.PrivProtocol,
			PrivacyPassphrase:        cfg.PrivPassword,
		}
	} else {
		// SNMPv1/v2c configuration
		conn.Community = cfg.Community
	}

	client := &gosnmpClient{conn: conn}
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", target, err)
	}

	return client, nil
}

// NewSNMPClientFunc is the function used to create SNMP clients.
// It can be replaced with a mock for testing.
var NewSNMPClientFunc = newSNMPClientImpl

// NewSNMPClient creates a new SNMP client for the specified target.
// If timeoutSeconds is 0, defaults to 30 seconds.
func NewSNMPClient(cfg *SNMPConfig, target string, timeoutSeconds int) (SNMPClient, error) {
	return NewSNMPClientFunc(cfg, target, timeoutSeconds)
}
