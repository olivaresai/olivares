// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Only the public per-session CA reaches the tool. Its signing key stays in
// engine memory; no host trust store is changed. TLS terminates here so both
// SNI and every decrypted HTTP authority can be checked before any dial.
func sessionTLS(ds []destination) ([]byte, *tls.Config, error) {
	var hosts []string
	for _, d := range ds {
		if d.scheme == "https" {
			hosts = append(hosts, d.host)
		}
	}
	if len(hosts) == 0 {
		return nil, nil, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Olivares session proxy"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	root, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: new(big.Int).Add(serial, big.NewInt(1)), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			leaf.IPAddresses = append(leaf.IPAddresses, ip)
		} else {
			leaf.DNSNames = append(leaf.DNSNames, host)
		}
	}
	// The CA key only signs; the handshake uses a key of its own. Both live as
	// long as the launch, which has no certificate renewal path.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, Certificates: []tls.Certificate{{Certificate: [][]byte{der, root}, PrivateKey: leafKey}}}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}), cfg, nil
}

// Use net/http's TLS handshake deadlines, parsing limits and connection cleanup.
// goproxy's MITM handshake failure path does not close the hijacked connection.
func (p *proxy) serveConnect(w http.ResponseWriter, r *http.Request, forward http.Handler) {
	if p.tlsConfig == nil {
		http.Error(w, "session TLS unavailable", http.StatusForbidden)
		return
	}
	raw, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer raw.Close()
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		return
	}
	cfg := p.tlsConfig.Clone()
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if !strings.EqualFold(hello.ServerName, host) && !(hello.ServerName == "" && net.ParseIP(host) != nil) {
			return nil, errors.New("session TLS destination denied")
		}
		return nil, nil
	}
	conn := tls.Server(&bufferedConn{Conn: raw, reader: buffered.Reader}, cfg)
	listener := &connectListener{Conn: conn, done: make(chan struct{})}
	defer listener.Close()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, ErrorLog: p.server.ErrorLog,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				_ = listener.Close()
			}
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, inner *http.Request) {
			if inner.Method == http.MethodConnect {
				http.Error(w, "session destination denied", http.StatusForbidden)
				return
			}
			if !inner.URL.IsAbs() {
				inner.URL.Scheme, inner.URL.Host = "https", r.Host
			}
			forward.ServeHTTP(w, inner)
		}),
	}
	defer server.Close()
	_ = server.Serve(listener)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

type connectListener struct {
	net.Conn
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *connectListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.Conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *connectListener) Close() error {
	l.once.Do(func() { _ = l.Conn.Close(); close(l.done) })
	return nil
}
func (l *connectListener) Addr() net.Addr { return l.Conn.LocalAddr() }
