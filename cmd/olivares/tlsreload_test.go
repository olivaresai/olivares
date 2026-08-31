// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/secure"
)

func TestTLSCertificateExpiryGaugeOnlyWhenTLSEnabled(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if _, _, err := secure.EnsureTLSCert(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	loader, err := secure.NewCertificateLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	notAfter, ok := loader.NotAfter()
	if !ok {
		t.Fatal("loaded certificate has no NotAfter")
	}

	tlsRegistry := metrics.New("test", time.Now())
	registerTLSCertificateExpiry(tlsRegistry, loader, true)
	var tlsOut strings.Builder
	tlsRegistry.WritePrometheus(&tlsOut)
	if want := fmt.Sprintf("%s %d\n", tlsCertNotAfterMetric, notAfter.Unix()); !strings.Contains(tlsOut.String(), want) {
		t.Fatalf("TLS exposition missing %q:\n%s", want, tlsOut.String())
	}

	insecureRegistry := metrics.New("test", time.Now())
	registerTLSCertificateExpiry(insecureRegistry, loader, false)
	var insecureOut strings.Builder
	insecureRegistry.WritePrometheus(&insecureOut)
	if strings.Contains(insecureOut.String(), tlsCertNotAfterMetric) {
		t.Fatalf("--insecure exposition contains TLS expiry gauge:\n%s", insecureOut.String())
	}
}

func TestConfigureHTTPServerTLSPreservesPIVClientAuth(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if _, _, err := secure.EnsureTLSCert(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	loader, err := secure.NewCertificateLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	srv := &http.Server{TLSConfig: &tls.Config{
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  roots,
	}}
	if err := configureHTTPServerTLS(srv, loader); err != nil {
		t.Fatal(err)
	}
	if srv.TLSConfig.GetCertificate == nil || len(srv.TLSConfig.Certificates) != 0 {
		t.Fatal("HTTP server did not receive the reloadable GetCertificate config")
	}
	if srv.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS 1.2", srv.TLSConfig.MinVersion)
	}
	if srv.TLSConfig.ClientAuth != tls.VerifyClientCertIfGiven || srv.TLSConfig.ClientCAs != roots {
		t.Fatal("reloadable certificate config discarded the PIV/CAC client-auth policy")
	}
}
