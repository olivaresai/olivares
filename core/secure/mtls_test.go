// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServerTLSConfigServerOnly: with no client CA, the server presents TLS but
// does NOT require a client certificate (the localhost single-node default).
func TestServerTLSConfigServerOnly(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(cert, key); err != nil {
		t.Fatalf("EnsureTLSCert: %v", err)
	}
	cfg, err := ServerTLSConfig(cert, key, "")
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("ClientAuth = %v, want NoClientCert (server-only TLS)", cfg.ClientAuth)
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want >= TLS1.2", cfg.MinVersion)
	}
	if cfg.GetCertificate == nil || len(cfg.Certificates) != 0 {
		t.Fatalf("server config must use the reloadable GetCertificate path")
	}
}

// TestServerTLSConfigMutual is the adversarial mTLS check: with a client CA
// configured, the server REQUIRES and VERIFIES a client certificate. A peer with
// NO client cert is rejected at the handshake; a peer with a cert signed by the
// trusted CA is accepted. This proves the collector→core mTLS guarantee (docs/SECURITY-HARDENING.md
// §1/§3) is real, not just claimed.
func TestServerTLSConfigMutual(t *testing.T) {
	dir := t.TempDir()

	// A CA that signs collector client certs.
	caCert, caKey := newCA(t)
	caFile := filepath.Join(dir, "client-ca.pem")
	writePEM(t, caFile, "CERTIFICATE", caCert.Raw)

	// The server's own (self-signed) cert/key on disk.
	srvCert, srvKey := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(srvCert, srvKey); err != nil {
		t.Fatalf("EnsureTLSCert: %v", err)
	}

	cfg, err := ServerTLSConfig(srvCert, srvKey, caFile)
	if err != nil {
		t.Fatalf("ServerTLSConfig(mTLS): %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go acceptLoop(ln)

	// 1) A client with NO certificate must be REJECTED. In TLS 1.3 the server's
	// bad-certificate alert surfaces on the client's first read, not at dial, so we
	// read: a rejected handshake yields a TLS error; an (incorrectly) accepted one
	// would let the server's sentinel byte through.
	t.Run("no client cert rejected", func(t *testing.T) {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test: server cert is not the subject here
		if err != nil {
			return // rejected at handshake (TLS 1.2 path) — correct
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1)
		if _, rerr := c.Read(buf); rerr == nil {
			t.Fatal("server accepted a client with no certificate (mTLS not enforced)")
		}
	})

	// 2) A client presenting a cert signed by the trusted CA must be ACCEPTED and
	// receive the server's sentinel byte.
	t.Run("valid client cert accepted", func(t *testing.T) {
		clientCert := newClientCert(t, caCert, caKey)
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			Certificates:       []tls.Certificate{clientCert},
			InsecureSkipVerify: true, //nolint:gosec // test: we assert client-auth, not server name
		})
		if err != nil {
			t.Fatalf("handshake with a valid client cert failed: %v", err)
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1)
		if _, rerr := c.Read(buf); rerr != nil {
			t.Fatalf("authenticated client could not read from server: %v", rerr)
		}
	})
}

// acceptLoop completes the server handshake and, on success, writes one sentinel
// byte so the client can distinguish "mTLS accepted me" from "handshake rejected".
func acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			tc, ok := c.(*tls.Conn)
			if !ok {
				return
			}
			if err := tc.Handshake(); err != nil {
				return // client-cert verification failed: the alert was sent
			}
			_, _ = tc.Write([]byte{1})
		}(c)
	}
}

func newCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "olivares-collector-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func newClientCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "collector-1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("sign client cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: mustParse(der)}
}

func mustParse(der []byte) *x509.Certificate { c, _ := x509.ParseCertificate(der); return c }

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
