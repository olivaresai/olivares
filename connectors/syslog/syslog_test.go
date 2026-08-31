// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package syslog

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// mustEncode renders n with o's resolved format, failing the test on the
// deny-closed corrupted-state error path that encode now has.
func mustEncode(t *testing.T, o *Output, n sdk.Notification) string {
	t.Helper()
	record, err := o.encode(n)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return record
}

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "Privilege escalation detected",
		Body:     "agent requested write to a read-only secret",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1", "mode": "write", "resource": "vault.path/db"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

// --- framing & config unit tests ---------------------------------------------

func TestFrameOctetCounting(t *testing.T) {
	o := New()
	o.transport = transportTCP
	o.framing = framingOctet
	record := "<134>1 ...example..."
	frame := string(o.frame(record))
	want := strconv.Itoa(len(record)) + " " + record
	if frame != want {
		t.Fatalf("octet frame = %q, want %q", frame, want)
	}
	// The length prefix must equal the octet count of the record exactly so a
	// receiver reads the right number of bytes.
	sp := strings.IndexByte(frame, ' ')
	gotLen, _ := strconv.Atoi(frame[:sp])
	if gotLen != len(record) {
		t.Fatalf("declared length %d != record length %d", gotLen, len(record))
	}
}

func TestFrameNonTransparent(t *testing.T) {
	o := New()
	o.transport = transportTCP
	o.framing = framingTransparent
	frame := string(o.frame("abc"))
	if frame != "abc\n" {
		t.Fatalf("non-transparent frame = %q, want %q", frame, "abc\n")
	}
}

func TestFrameUDPNoFraming(t *testing.T) {
	o := New()
	o.transport = transportUDP
	frame := string(o.frame("abc"))
	if frame != "abc" {
		t.Fatalf("udp frame = %q, want unframed %q", frame, "abc")
	}
}

func TestWithDefaultPort(t *testing.T) {
	cases := []struct {
		addr string
		t    transport
		want string
	}{
		{"collector.corp", transportTLS, "collector.corp:6514"},
		{"collector.corp", transportTCP, "collector.corp:514"},
		{"collector.corp", transportUDP, "collector.corp:514"},
		{"collector.corp:7000", transportTLS, "collector.corp:7000"},
		{"127.0.0.1:5000", transportUDP, "127.0.0.1:5000"},
	}
	for _, c := range cases {
		if got := withDefaultPort(c.addr, c.t); got != c.want {
			t.Errorf("withDefaultPort(%q,%q) = %q, want %q", c.addr, c.t, got, c.want)
		}
	}
}

func TestOpenDefaultsAreSecure(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"address": "collector.corp",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if o.transport != transportTLS {
		t.Errorf("default transport = %q, want tls (secure by default)", o.transport)
	}
	if o.address != "collector.corp:6514" {
		t.Errorf("default tls address = %q, want :6514", o.address)
	}
	if o.tlsConfig == nil || o.tlsConfig.InsecureSkipVerify {
		t.Errorf("default TLS must verify the server certificate (fail-closed)")
	}
	if o.tlsConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("TLS min version too low: %x", o.tlsConfig.MinVersion)
	}
}

func TestOpenForcesOctetFramingOnTLS(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"address": "c:6514", "transport": "tls", "framing": "non-transparent",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if o.framing != framingOctet {
		t.Errorf("TLS framing = %q, want octet-counting forced (RFC 5425)", o.framing)
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	cases := []map[string]string{
		{"address": "c", "transport": "carrier-pigeon"},
		{"address": "c", "format": "xml"},
		{"address": "c", "transport": "tcp", "framing": "frobnicate"},
		{}, // missing address
		{"address": "c", "transport": "tls", "cert_file": "/nope/cert.pem"}, // cert without key
	}
	for i, cfg := range cases {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

func TestNotifyBeforeOpen(t *testing.T) {
	o := New()
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("Notify before Open should error")
	}
}

// --- TCP transport round-trip ------------------------------------------------

func TestNotifyTCPOctetRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		got <- readOctetFrame(t, conn)
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tcp", "address": ln.Addr().String(), "hostname": "host1",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	n := sampleNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	record := waitFrame(t, got)
	assertParseableRFC5424(t, record)
	if !strings.Contains(record, "host1") {
		t.Errorf("record missing hostname: %q", record)
	}
	// Minimal-data: the wire bytes are exactly what siemfmt encodes — the connector
	// adds no enrichment, no raw payload of its own.
	if record != mustEncode(t, o, n) {
		t.Errorf("transport altered the record:\n got %q\nwant %q", record, mustEncode(t, o, n))
	}
}

func TestNotifyTCPCEFOverSyslog(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		got <- readOctetFrame(t, conn)
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tcp", "address": ln.Addr().String(), "format": "cef",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	record := waitFrame(t, got)
	// The CEF:0 record must be carried as the MSG of a real RFC 5424 frame.
	if !strings.HasPrefix(record, "<") {
		t.Errorf("CEF-over-syslog not wrapped in a 5424 frame: %q", record)
	}
	if !strings.Contains(record, "CEF:0|") {
		t.Errorf("record missing CEF payload: %q", record)
	}
}

// --- UDP transport round-trip ------------------------------------------------

func TestNotifyUDPDatagram(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 8192)
		_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		got <- string(buf[:n])
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "udp", "address": pc.LocalAddr().String(),
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	record := waitFrame(t, got)
	// RFC 5426: the datagram is the unframed record — no octet-count prefix.
	if _, err := strconv.Atoi(strings.Fields(record)[0]); err == nil {
		t.Errorf("udp datagram appears framed (leading number): %q", record)
	}
	assertParseableRFC5424(t, record)
}

// --- TLS 6514 transport (the regulated-SOC path) -----------------------------

func TestNotifyTLSRoundTrip(t *testing.T) {
	caCert, caKey, caPEM := genCA(t)
	serverCert := genCert(t, caCert, caKey, "127.0.0.1", false)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Force the handshake before reading the frame.
		if tc, ok := conn.(*tls.Conn); ok {
			_ = tc.Handshake()
		}
		got <- readOctetFrame(t, conn)
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tls", "address": ln.Addr().String(),
		"ca_file": caFile, "server_name": "127.0.0.1",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify over TLS: %v", err)
	}

	record := waitFrame(t, got)
	assertParseableRFC5424(t, record)
}

func TestNotifyTLSFailsClosedOnUntrustedCert(t *testing.T) {
	// Server presents a cert from a CA the client does NOT trust. The TLS dial must
	// fail — and must NOT fall back to cleartext.
	caCert, caKey, _ := genCA(t)
	serverCert := genCert(t, caCert, caKey, "127.0.0.1", false)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if tc, ok := c.(*tls.Conn); ok {
				_ = tc.Handshake() // will fail; that is the point
			}
			_ = c.Close()
		}
	}()

	o := New()
	// No ca_file => system roots, which do not trust our in-test CA.
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tls", "address": ln.Addr().String(), "server_name": "127.0.0.1",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.Notify(ctx, sampleNotification()); err == nil {
		t.Fatal("Notify over TLS with an untrusted server cert must fail closed")
	}
}

func TestNotifyMutualTLS(t *testing.T) {
	caCert, caKey, caPEM := genCA(t)
	serverCert := genCert(t, caCert, caKey, "127.0.0.1", false)
	clientCert, clientPEM, clientKeyPEM := genCertPEM(t, caCert, caKey, "olivares-client", true)

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "client.pem")
	keyFile := filepath.Join(dir, "client-key.pem")
	mustWrite(t, caFile, caPEM)
	mustWrite(t, certFile, clientPEM)
	mustWrite(t, keyFile, clientKeyPEM)
	_ = clientCert

	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(caCert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if tc, ok := conn.(*tls.Conn); ok {
			if herr := tc.Handshake(); herr != nil {
				return
			}
		}
		got <- readOctetFrame(t, conn)
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tls", "address": ln.Addr().String(),
		"ca_file": caFile, "cert_file": certFile, "key_file": keyFile, "server_name": "127.0.0.1",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("mutual-TLS Notify: %v", err)
	}
	assertParseableRFC5424(t, waitFrame(t, got))
}

// --- helpers -----------------------------------------------------------------

func waitFrame(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the delivered frame")
		return ""
	}
}

func readOctetFrame(t *testing.T, r io.Reader) string {
	t.Helper()
	br := bufio.NewReader(r)
	lenStr, err := br.ReadString(' ')
	if err != nil {
		t.Errorf("read octet length: %v", err)
		return ""
	}
	n, err := strconv.Atoi(strings.TrimSpace(lenStr))
	if err != nil {
		t.Errorf("bad octet length %q: %v", lenStr, err)
		return ""
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Errorf("read %d-octet message: %v", n, err)
		return ""
	}
	return string(buf)
}

// assertParseableRFC5424 does a minimal structural check that the record is a
// valid RFC 5424 frame: "<PRI>1 TIMESTAMP HOSTNAME APP PROCID MSGID ...".
func assertParseableRFC5424(t *testing.T, record string) {
	t.Helper()
	if !strings.HasPrefix(record, "<") {
		t.Fatalf("record does not start with <PRI>: %q", record)
	}
	gt := strings.IndexByte(record, '>')
	if gt < 2 {
		t.Fatalf("no PRI delimiter: %q", record)
	}
	if _, err := strconv.Atoi(record[1:gt]); err != nil {
		t.Fatalf("PRI not numeric: %q", record)
	}
	rest := record[gt+1:]
	if !strings.HasPrefix(rest, "1 ") {
		t.Fatalf("VERSION not 1: %q", record)
	}
	if len(strings.Fields(rest)) < 5 {
		t.Fatalf("too few header fields: %q", record)
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func genCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "olivares-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return cert, key, caPEM
}

func genCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, client bool) tls.Certificate {
	t.Helper()
	cert, _, _ := genCertPEM(t, ca, caKey, cn, client)
	return cert
}

func genCertPEM(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, client bool) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		if ip := net.ParseIP(cn); ip != nil {
			tmpl.IPAddresses = []net.IP{ip}
		} else {
			tmpl.DNSNames = []string{cn}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair, certPEM, keyPEM
}

// --- receiver payload budget (E3) ---------------------------------------

// oversizeNotification returns a notification whose encoded record is comfortably
// past any small budget, without repeating the body text the leak assertion looks
// for.
func oversizeNotification() sdk.Notification {
	n := sampleNotification()
	n.Fields = map[string]string{"evidence": strings.Repeat("x", 900)}
	return n
}

func TestNotifyFailsClosedOverConfiguredPayloadBudget(t *testing.T) {
	// A receiver that splits an oversize record (ArcSight's syslog daemon past
	// 1024 bytes, QRadar TCP past its 4096 default) turns one auditable event into
	// two unparseable halves. When the operator declares the receiver's budget, an
	// oversize record fails the delivery honestly — the engine retries and then
	// dead-letters it — instead of being silently split downstream. The record is
	// never truncated, and the error carries byte counts only, never the payload.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tcp", "address": ln.Addr().String(), "max_payload_bytes": "512",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	n := oversizeNotification()
	err = o.Notify(context.Background(), n)
	if err == nil {
		t.Fatal("oversize record must fail the delivery, not be sent for the receiver to split")
	}
	msg := err.Error()
	for _, want := range []string{"512", strconv.Itoa(len(mustEncode(t, o, n)))} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing byte count %q", msg, want)
		}
	}
	if strings.Contains(msg, "read-only secret") || strings.Contains(msg, strings.Repeat("x", 20)) {
		t.Errorf("error leaks record content: %q", msg)
	}
}

func TestNotifyPayloadBudgetIsOffByDefault(t *testing.T) {
	// No budget configured: the connector never invents a limit of its own — an
	// oversize record is delivered exactly as encoded.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		got <- readOctetFrame(t, conn)
	}()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tcp", "address": ln.Addr().String(),
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	n := oversizeNotification()
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if record := waitFrame(t, got); record != mustEncode(t, o, n) {
		t.Errorf("record altered:\n got %q\nwant %q", record, mustEncode(t, o, n))
	}
}

func TestOpenRejectsNegativePayloadBudget(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tcp", "address": "127.0.0.1:514", "max_payload_bytes": "-1",
	}})
	if err == nil {
		t.Fatal("a negative payload budget is a misconfiguration and must be rejected at Open")
	}
}
