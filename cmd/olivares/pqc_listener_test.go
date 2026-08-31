// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/tls"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secure"
)

// (release gate S-5, U4), closing finding F-08 of the adversarial
// contrast run against this work on 2026-08-06 (MEDIUM).
//
// docs/SEC-G3-CRYPTO-AGILITY-PQC.md says CurvePreferences is left to the Go
// defaults on purpose and that core/secure/pqc_test.go is the gate: "if a
// refactor pinned classical curves, the gate breaks". It is not that gate. That
// test builds its own server and client configs and handshakes those two
// (core/secure/pqc_test.go) — it never touches the objects the engine actually
// serves with. The REST listener's final config comes from
// configureHTTPServerTLS (tlsreload.go); gRPC's comes from
// secure.ServerTLSConfigWithLoader (cmd_serve.go newGRPCServer).
//
// The contrast proved the gap by mutation: pinning
// cfg.CurvePreferences = []tls.CurveID{tls.X25519} in tlsreload.go — the config
// the live HTTP server consumes — left BOTH the claimed PQC gate and the only
// test of that compositor green.
//
// My own measurement against the running binary was truthful but was a one-time
// manual observation, which is not a regression gate either. This is one: it
// handshakes the objects the production paths produce.
//
// HONEST BOUNDS, and there are TWO. Both were found by an adversarial panel after
// an earlier version of this comment claimed the REST half was end to end.
//   - gRPC: this calls the same factory production calls
//     (secure.ServerTLSConfigWithLoader) and catches a pin inside it, but
//     newGRPCServer hands that config to credentials.NewTLS and a pin at THAT
//     caller-level seam is not executed here. Closing it needs a constructed
//     *engine. A mutation at that exact line was confirmed to SURVIVE.
//   - REST: the subtest below starts from a server with a nil TLSConfig. Under
//     PIV/CAC, runEngine pre-sets srv.TLSConfig BEFORE configureHTTPServerTLS,
//     which CLONES a non-nil config and only touches MinVersion/Certificates/
//     GetCertificate — so CurvePreferences set at that seam survive into the live
//     listener. The PIV-shaped subtest below now covers exactly that.
func TestProductionListenerTLSNegotiatesHybridPQC(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if _, _, err := secure.EnsureTLSCert(certPath, keyPath); err != nil {
		t.Fatalf("ensure TLS cert: %v", err)
	}
	loader, err := secure.NewCertificateLoader(certPath, keyPath)
	if err != nil {
		t.Fatalf("certificate loader: %v", err)
	}

	t.Run("the REST listener composition", func(t *testing.T) {
		// Exactly what runEngine does for httpSrv and every auxiliary listener.
		srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
		if err := configureHTTPServerTLS(srv, loader); err != nil {
			t.Fatalf("configureHTTPServerTLS: %v", err)
		}
		assertNegotiatesHybrid(t, srv.TLSConfig)
	})

	t.Run("the REST listener composition when PIV pre-set a config", func(t *testing.T) {
		// Mirrors cmd_serve.go: with PIV/CAC armed, runEngine assigns a TLSConfig
		// BEFORE configureHTTPServerTLS runs, and that compositor clones rather than
		// resets. A curve pin placed there survives into the served listener, and
		// the nil-TLSConfig subtest above cannot see it.
		srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
		srv.TLSConfig = &tls.Config{ClientAuth: tls.VerifyClientCertIfGiven, MinVersion: tls.VersionTLS12}
		if err := configureHTTPServerTLS(srv, loader); err != nil {
			t.Fatalf("configureHTTPServerTLS: %v", err)
		}
		if srv.TLSConfig.ClientAuth != tls.VerifyClientCertIfGiven {
			t.Fatalf("the compositor dropped the PIV client-auth policy, so this subtest is not modeling the PIV path")
		}
		assertNegotiatesHybrid(t, srv.TLSConfig)
	})

	t.Run("the gRPC ingest composition", func(t *testing.T) {
		// Exactly what newGRPCServer hands to grpc.Creds.
		cfg, err := secure.ServerTLSConfigWithLoader(loader, "")
		if err != nil {
			t.Fatalf("ServerTLSConfigWithLoader: %v", err)
		}
		assertNegotiatesHybrid(t, cfg)
	})
}

// assertNegotiatesHybrid runs a real handshake against a server using cfg and
// requires the negotiated key exchange to be the hybrid ML-KEM group.
//
// The client is left at Go's defaults ON PURPOSE: this asserts what a stock
// client gets from this server, which is the property the document sells
// (harvest-now-decrypt-later). Pinning the client to the hybrid would still pass
// against a server that merely tolerates it while preferring classical.
func assertNegotiatesHybrid(t *testing.T, cfg *tls.Config) {
	t.Helper()
	if cfg == nil {
		t.Fatal("the production composition produced a nil TLS config")
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		_ = conn.(*tls.Conn).Handshake()
		_ = conn.Close()
	}()

	c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- self-signed by construction; the assertion is the key-exchange group
	if err != nil {
		t.Fatalf("handshake against the production TLS config: %v", err)
	}
	defer func() { _ = c.Close() }()
	<-done

	st := c.ConnectionState()
	if st.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS 0x%04x, want 1.3 — the hybrid ML-KEM groups exist only there", st.Version)
	}
	if st.CurveID != tls.X25519MLKEM768 {
		t.Fatalf("the listener a customer actually connects to negotiated key exchange %v, want X25519MLKEM768 (%d). The advertised property is absent either way, but the CAUSE is one of three and this test cannot tell them apart: something along this composition path pinned CurvePreferences, or the run set GODEBUG=tlsmlkem=0, or the toolchain default changed. Check those in that order before blaming the product",
			st.CurveID, tls.X25519MLKEM768)
	}
}
