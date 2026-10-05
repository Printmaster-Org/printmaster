package scanner

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func TestContextSNMPClientHonorsCancelledRequest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSNMPClientWithContext(ctx, &SNMPConfig{Version: gosnmp.Version2c}, "192.0.2.1", 2, 1); err != context.Canceled {
		t.Fatalf("cancelled client: %v", err)
	}
}

func TestContextSNMPClientBoundsSilentPrinter(t *testing.T) {
	t.Parallel()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client, err := NewSNMPClientWithContext(ctx, &SNMPConfig{Version: gosnmp.Version2c, Community: "public"}, "127.0.0.1", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Reconnect to our intentionally silent fixture rather than relying on an
	// unroutable address or a real printer's SNMP availability.
	underlying := client.(*gosnmpClient)
	underlying.conn.Conn.Close()
	underlying.conn.Port = uint16(listener.LocalAddr().(*net.UDPAddr).Port)
	if err := underlying.Connect(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := client.Get([]string{"1.3.6.1.2.1.1.1.0"}); err == nil {
		t.Fatal("silent fixture returned data")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context ignored: elapsed %v", elapsed)
	}
}
