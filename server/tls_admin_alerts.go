package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	adminnotify "printmaster/server/adminnotify"
	"printmaster/server/storage"
)

const tlsCertificateWarningWindow = 30 * 24 * time.Hour

// startTLSCertificateMonitor checks loaded certificates periodically and inspects
// certificates returned by dynamic providers (such as autocert) during handshakes.
func startTLSCertificateMonitor(ctx context.Context, cfg *tls.Config) {
	if cfg == nil {
		return
	}
	check := func(cert *tls.Certificate) {
		leaf := certificateLeaf(cert)
		if leaf == nil {
			return
		}
		reportTLSCertificateExpiry(context.Background(), leaf)
	}

	for i := range cfg.Certificates {
		check(&cfg.Certificates[i])
	}
	if cfg.GetCertificate != nil {
		getCertificate := cfg.GetCertificate
		cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := getCertificate(hello)
			if err == nil {
				check(cert)
			}
			return cert, err
		}
	}

	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for i := range cfg.Certificates {
					check(&cfg.Certificates[i])
				}
			}
		}
	}()
}

func certificateLeaf(cert *tls.Certificate) *x509.Certificate {
	if cert == nil {
		return nil
	}
	if cert.Leaf != nil {
		return cert.Leaf
	}
	if len(cert.Certificate) == 0 {
		return nil
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		logWarn("Failed to parse active TLS certificate", "error", err)
		return nil
	}
	cert.Leaf = leaf
	return leaf
}

func reportTLSCertificateExpiry(ctx context.Context, cert *x509.Certificate) {
	if cert == nil {
		return
	}
	now := time.Now()
	remaining := cert.NotAfter.Sub(now)
	name := cert.Subject.CommonName
	if name == "" && len(cert.DNSNames) > 0 {
		name = cert.DNSNames[0]
	}
	if name == "" {
		name = "configured TLS certificate"
	}

	if remaining <= 0 {
		clearAdminAlert(ctx, "tls.certificate.expiring", "")
		raiseAdminAlert(ctx, adminnotify.Event{
			Key:      "tls.certificate.expired",
			Severity: storage.AlertSeverityCritical,
			Title:    "Server TLS certificate has expired",
			Message:  fmt.Sprintf("The active TLS certificate for %q expired at %s. Renew or replace the certificate, then restart PrintMaster.", name, cert.NotAfter.UTC().Format(time.RFC3339)),
			Details:  fmt.Sprintf("certificate expires %s; serial %s", cert.NotAfter.UTC().Format(time.RFC3339), cert.SerialNumber),
		})
		return
	}

	clearAdminAlert(ctx, "tls.certificate.expired", "")
	if remaining <= tlsCertificateWarningWindow {
		raiseAdminAlert(ctx, adminnotify.Event{
			Key:      "tls.certificate.expiring",
			Severity: storage.AlertSeverityWarning,
			Title:    "Server TLS certificate is expiring soon",
			Message:  fmt.Sprintf("The active TLS certificate for %q expires in %s, at %s. Renew or replace it before it expires.", name, remaining.Round(time.Hour), cert.NotAfter.UTC().Format(time.RFC3339)),
			Details:  fmt.Sprintf("certificate expires %s; serial %s", cert.NotAfter.UTC().Format(time.RFC3339), cert.SerialNumber),
		})
		return
	}
	clearAdminAlert(ctx, "tls.certificate.expiring", "")
}
