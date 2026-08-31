// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/tls"
	"net"
	"path/filepath"
	"testing"
)

// TestTLSNegotiatesHybridPQCKeyExchange pins the crypto-agility posture
// documents (docs/SEC-G3-CRYPTO-AGILITY-PQC.md): the engine's TLS configs leave
// CurvePreferences to the Go defaults, so TLS 1.3 negotiates the HYBRID
// post-quantum key exchange X25519MLKEM768 (ML-KEM-768 per FIPS 203 + X25519)
// when both peers support it — quantum-resistant key establishment is a SHIPPED
// transport property, not a roadmap item. A real loopback handshake proves it;
// if a refactor ever pinned CurvePreferences to classical curves, this fails.
func TestTLSNegotiatesHybridPQCKeyExchange(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	srvCfg, err := ServerTLSConfig(certPath, keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if srvCfg.CurvePreferences != nil {
		t.Fatal("ServerTLSConfig pinned CurvePreferences — that silently drops the default hybrid PQC key exchange")
	}
	cliCfg, err := ClientTLSConfig(certPath, "", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if cliCfg.CurvePreferences != nil {
		t.Fatal("ClientTLSConfig pinned CurvePreferences — that silently drops the default hybrid PQC key exchange")
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	done := make(chan error, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			done <- aerr
			return
		}
		defer func() { _ = c.Close() }()
		done <- c.(*tls.Conn).Handshake()
	}()

	conn, err := tls.Dial("tcp", ln.Addr().String(), cliCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := conn.ConnectionState()
	if st.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS %x, want 1.3", st.Version)
	}
	if st.CurveID != tls.X25519MLKEM768 {
		t.Fatalf("negotiated key exchange %v, want X25519MLKEM768 (hybrid ML-KEM-768) — the Go-default hybrid PQC KEM was not used", st.CurveID)
	}
	var _ net.Conn = conn // (typed use; the handshake above is the assertion)
}
