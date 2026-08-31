// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/secure"
)

const (
	tlsCertNotAfterMetric   = "olivares_tls_cert_not_after_seconds"
	tlsCertExpiryWarnWindow = 30 * 24 * time.Hour
)

// configureHTTPServerTLS composes the shared reloadable certificate with any
// listener-specific TLS policy already present (notably optional PIV/CAC client
// authentication).
func configureHTTPServerTLS(srv *http.Server, loader *secure.CertificateLoader) error {
	if srv == nil {
		return fmt.Errorf("TLS HTTP server is required")
	}
	if loader == nil {
		return fmt.Errorf("TLS certificate loader is required")
	}
	cfg := srv.TLSConfig
	if cfg == nil {
		cfg = &tls.Config{}
	} else {
		cfg = cfg.Clone()
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	cfg.Certificates = nil
	cfg.GetCertificate = loader.GetCertificate
	srv.TLSConfig = cfg
	return nil
}

// registerTLSCertificateExpiry installs a scrape-time gauge only for a listener
// set that actually serves TLS. Each scrape refreshes the mtime cache first, so
// the gauge follows a hot certificate rotation even before the next handshake.
func registerTLSCertificateExpiry(reg *metrics.Registry, loader *secure.CertificateLoader, tlsEnabled bool) {
	if !tlsEnabled || reg == nil || loader == nil {
		return
	}
	reg.RegisterFunc(tlsCertNotAfterMetric, func(w io.Writer) {
		if err := loader.Load(); err != nil {
			return
		}
		notAfter, ok := loader.NotAfter()
		if !ok {
			return
		}
		fmt.Fprintf(w, "# HELP %s Unix timestamp at which the TLS certificate currently served by this engine expires.\n# TYPE %s gauge\n%s %d\n",
			tlsCertNotAfterMetric, tlsCertNotAfterMetric, tlsCertNotAfterMetric, notAfter.Unix())
	})
}

// warnTLSCertificateExpiry surfaces a near-term (or already elapsed) certificate
// deadline once at boot. Rotation remains hot; operators need no restart.
func warnTLSCertificateExpiry(log *slog.Logger, loader *secure.CertificateLoader, now time.Time) {
	if log == nil || loader == nil {
		return
	}
	notAfter, ok := loader.NotAfter()
	if !ok || notAfter.After(now.Add(tlsCertExpiryWarnWindow)) {
		return
	}
	log.Warn("TLS certificate expires within 30 days",
		"not_after", notAfter.UTC().Format(time.RFC3339),
		"expires_in", notAfter.Sub(now).Round(time.Second).String())
}
