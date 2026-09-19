// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ldap

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/olivaresai/olivares/sdk"
)

const (
	ldapWireRole    = "OLIVARES_LDAP_WIRE_ROLE"
	ldapWireCaseEnv = "OLIVARES_LDAP_WIRE_CASE"
	ldapWireDirEnv  = "OLIVARES_LDAP_WIRE_DIR"
	ldapWireBindDN  = "cn=wire-reader,dc=example"
)

type ldapWireCase struct {
	name, scheme, certificate string
}

var ldapWireCases = []ldapWireCase{
	{"starttls_valid", "ldap", "valid"},
	{"starttls_wrong_host", "ldap", "wrong_host"},
	{"starttls_untrusted", "ldap", "untrusted"},
	{"ldaps_valid", "ldaps", "valid"},
	{"ldaps_wrong_host", "ldaps", "wrong_host"},
	{"ldaps_untrusted", "ldaps", "untrusted"},
}

// Linux's x509 loader reads SSL_CERT_FILE/SSL_CERT_DIR once per process. Run the
// already compiled test executable with isolated roots; do not inject RootCAs
// into Source or change the parent process's global trust/cache.
func TestLDAPWireCertificateTransport(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range ldapWireCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ldapWireCertificates(t, dir, tc.certificate)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestLDAPWireChild$", "-test.v", "-test.timeout=20s")
			cmd.WaitDelay = 2 * time.Second
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "SSL_CERT_FILE" && key != "SSL_CERT_DIR" && !strings.HasPrefix(key, "OLIVARES_LDAP_WIRE_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, ldapWireRole+"=certificate-peer", ldapWireCaseEnv+"="+tc.name,
				ldapWireDirEnv+"="+dir, "SSL_CERT_FILE="+filepath.Join(dir, "trusted-ca.pem"),
				"SSL_CERT_DIR="+filepath.Join(dir, "empty-roots"))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("wire child failed (context=%v): %v\n%s", ctx.Err(), err, output)
			}
			if !bytes.Contains(output, []byte("LDAP_WIRE_QUALIFIED "+tc.name)) {
				t.Fatalf("wire child did not execute its qualification oracle:\n%s", output)
			}
		})
	}
}

// Only the parent test supplies this role. A normal package run skips this
// helper entry; that skip is not a wire qualification result.
func TestLDAPWireChild(t *testing.T) {
	if os.Getenv(ldapWireRole) == "" {
		t.Skip("invoked only by TestLDAPWireCertificateTransport in an isolated process")
	}
	if os.Getenv(ldapWireRole) != "certificate-peer" {
		t.Fatal("unknown LDAP wire child role")
	}
	var tc ldapWireCase
	for _, candidate := range ldapWireCases {
		if candidate.name == os.Getenv(ldapWireCaseEnv) {
			tc = candidate
		}
	}
	if tc.name == "" {
		t.Fatal("unknown LDAP wire child case")
	}
	dir := os.Getenv(ldapWireDirEnv)
	certificate := ldapWireCheckCertificates(t, dir, tc.certificate)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stopListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
	done := make(chan ldapWireObservation, 1)
	go func() { done <- ldapWireServe(ctx, listener, certificate, tc.scheme) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		stopListener()
		_ = listener.Close()
		if !joined {
			// Cancellation closes both accept and an accepted socket, so the
			// protocol goroutine cannot remain blocked in a network operation.
			<-done
		}
	})

	source := New()
	if err := source.Open(ctx, sdk.Config{Settings: map[string]string{
		"url": tc.scheme + "://" + listener.Addr().String(), "base_dn": "dc=example",
		"bind_dn": ldapWireBindDN, "bind_password": testBindPassword,
		// LDAPS deliberately also sets this to detect a nested TLS upgrade.
		"start_tls": "true",
	}}); err != nil {
		t.Fatalf("valid fixture configuration was refused: %v", err)
	}
	connection, connectErr := source.connect() // ordinary default dialer, no hooks
	if connection != nil {
		if err := connection.Close(); err != nil {
			t.Errorf("close successful connection: %v", err)
		}
	}
	observed := <-done
	joined = true
	if observed.err != nil {
		t.Fatalf("wire protocol did not reach the required outcome: %v (events=%v, client=%v)", observed.err, observed.events, connectErr)
	}
	prefix := []string{"accepted"}
	if tc.scheme == "ldap" {
		prefix = append(prefix, "starttls")
	}
	if tc.certificate == "valid" {
		if connectErr != nil || connection == nil {
			t.Fatalf("valid trusted certificate must connect: %v", connectErr)
		}
		want := append(prefix, "tls", "bind", "closed")
		if !reflect.DeepEqual(observed.events, want) || observed.binds != 1 ||
			observed.bindDN != ldapWireBindDN || !observed.passwordMatched || observed.tlsVersion < tls.VersionTLS12 {
			t.Fatalf("TLS must precede exactly one correct bind: events=%v binds=%d identity=%q tls=%d", observed.events, observed.binds, observed.bindDN, observed.tlsVersion)
		}
	} else {
		if connectErr == nil || connection != nil {
			t.Fatal("invalid certificate was accepted")
		}
		want := append(prefix, "tls_rejected")
		if !reflect.DeepEqual(observed.events, want) || observed.binds != 0 {
			t.Fatalf("certificate rejection must precede every Bind: events=%v binds=%d", observed.events, observed.binds)
		}
		ldapWireCheckRejection(t, tc, connectErr)
	}
	t.Logf("LDAP_WIRE_QUALIFIED %s", tc.name)
}

func ldapWireCheckRejection(t *testing.T, tc ldapWireCase, err error) {
	t.Helper()
	if tc.scheme == "ldaps" {
		var hostname x509.HostnameError
		var authority x509.UnknownAuthorityError
		if (tc.certificate == "wrong_host" && !errors.As(err, &hostname)) ||
			(tc.certificate == "untrusted" && !errors.As(err, &authority)) {
			t.Fatalf("LDAPS failed without the expected certificate cause: %v", err)
		}
		return
	}
	// go-ldap v3.4.14 StartTLS formats the TLS handshake error with %v, losing
	// the x509 type. Corroborate this diagnostic with the server's actual remote
	// TLS alert and zero-Bind event oracle, never an arbitrary connection error.
	expected := "x509: certificate signed by unknown authority"
	if tc.certificate == "wrong_host" {
		expected = "x509: certificate is valid for 127.0.0.2, not 127.0.0.1"
	}
	if !strings.Contains(err.Error(), expected) {
		t.Fatalf("StartTLS failed without the expected certificate cause: %v", err)
	}
}

type ldapWireObservation struct {
	events          []string
	binds           int
	bindDN          string
	passwordMatched bool
	tlsVersion      uint16
	err             error
}

func ldapWireServe(ctx context.Context, listener *net.TCPListener, certificate tls.Certificate, scheme string) (out ldapWireObservation) {
	conn, err := listener.Accept()
	if err != nil {
		out.err = fmt.Errorf("fixture accept: %w", err)
		return
	}
	defer func() { _ = conn.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		out.err = err
		return
	}
	out.events = append(out.events, "accepted")
	if scheme == "ldap" {
		id, request, err := ldapWireRequest(conn)
		if err != nil {
			out.err = fmt.Errorf("read StartTLS: %w", err)
			return
		}
		if request.Tag == goldap.ApplicationBindRequest {
			out.binds++
		}
		if request.Tag != goldap.ApplicationExtendedRequest || len(request.Children) != 1 ||
			request.Children[0].Class != asn1.ClassContextSpecific || request.Children[0].Tag != 0 ||
			request.Children[0].IsCompound || string(request.Children[0].Bytes) != "1.3.6.1.4.1.1466.20037" {
			out.err = errors.New("expected StartTLS extended request before credentials")
			return
		}
		out.events = append(out.events, "starttls")
		if err := ldapWireReply(conn, id, goldap.ApplicationExtendedResponse); err != nil {
			out.err = err
			return
		}
	}
	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		var remote *net.OpError
		if errors.As(err, &remote) && remote.Op == "remote error" {
			out.events = append(out.events, "tls_rejected")
			return
		}
		out.err = fmt.Errorf("TLS did not complete or receive a client alert: %w", err)
		return
	}
	out.events = append(out.events, "tls")
	out.tlsVersion = tlsConn.ConnectionState().Version
	id, request, err := ldapWireRequest(tlsConn)
	if err != nil {
		out.err = fmt.Errorf("read protected Bind: %w", err)
		return
	}
	if request.Tag == goldap.ApplicationBindRequest {
		out.binds++
	}
	if request.Tag != goldap.ApplicationBindRequest || len(request.Children) != 3 ||
		request.Children[0].Class != asn1.ClassUniversal || request.Children[0].Tag != asn1.TagInteger ||
		request.Children[1].Class != asn1.ClassUniversal || request.Children[1].Tag != asn1.TagOctetString ||
		request.Children[1].IsCompound || request.Children[2].Class != asn1.ClassContextSpecific ||
		request.Children[2].Tag != 0 || request.Children[2].IsCompound {
		out.err = errors.New("expected version3 simple Bind after TLS")
		return
	}
	var version int64
	if rest, err := asn1.Unmarshal(request.Children[0].FullBytes, &version); err != nil || len(rest) != 0 || version != 3 {
		out.err = errors.New("Bind must use LDAP version3")
		return
	}
	out.events = append(out.events, "bind")
	out.bindDN = string(request.Children[1].Bytes)
	out.passwordMatched = string(request.Children[2].Bytes) == testBindPassword
	if err := ldapWireReply(tlsConn, id, goldap.ApplicationBindResponse); err != nil {
		out.err = err
		return
	}
	_, extra, err := ldapWireRequest(tlsConn)
	if !errors.Is(err, io.EOF) {
		if err == nil && extra.Tag == goldap.ApplicationBindRequest {
			out.binds++
		}
		out.err = fmt.Errorf("expected client close after Bind, extra operation=%v error=%v", extra != nil, err)
		return
	}
	out.events = append(out.events, "closed")
	return
}

// The pinned client emits definite-length BER for these LDAP operations, which
// encoding/asn1 decodes as DER. This is a bounded fixture, not a general server.
type ldapWireOperation struct {
	Tag      int
	Children []asn1.RawValue
}

func ldapWireRequest(reader io.Reader) (int64, *ldapWireOperation, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	if header[0] != 0x30 {
		return 0, nil, errors.New("expected LDAP sequence")
	}
	size := int(header[1])
	if size&0x80 != 0 {
		width := size & 0x7f
		if width == 0 || width > 2 {
			return 0, nil, errors.New("fixture requires bounded definite LDAP length")
		}
		length := make([]byte, width)
		if _, err := io.ReadFull(reader, length); err != nil {
			return 0, nil, err
		}
		header = append(header, length...)
		size = 0
		for _, b := range length {
			size = size<<8 | int(b)
		}
	}
	body := make([]byte, size) // at most 65535 bytes, bounded before allocation
	if _, err := io.ReadFull(reader, body); err != nil {
		return 0, nil, err
	}
	var envelope asn1.RawValue
	if rest, err := asn1.Unmarshal(append(header, body...), &envelope); err != nil || len(rest) != 0 {
		return 0, nil, fmt.Errorf("decode LDAP envelope: %v", err)
	}
	fields, err := ldapWireValues(envelope.Bytes)
	if err != nil || len(fields) != 2 || fields[1].Class != asn1.ClassApplication || !fields[1].IsCompound {
		return 0, nil, errors.New("invalid LDAP message envelope")
	}
	var id int64
	if rest, err := asn1.Unmarshal(fields[0].FullBytes, &id); err != nil || len(rest) != 0 || id <= 0 {
		return 0, nil, errors.New("invalid LDAP message ID")
	}
	children, err := ldapWireValues(fields[1].Bytes)
	if err != nil {
		return 0, nil, err
	}
	return id, &ldapWireOperation{Tag: fields[1].Tag, Children: children}, nil
}

func ldapWireValues(encoded []byte) ([]asn1.RawValue, error) {
	var values []asn1.RawValue
	for len(encoded) > 0 {
		var value asn1.RawValue
		rest, err := asn1.Unmarshal(encoded, &value)
		if err != nil || len(rest) >= len(encoded) || len(values) >= 3 {
			return nil, errors.New("invalid or excessive LDAP fixture fields")
		}
		values = append(values, value)
		encoded = rest
	}
	return values, nil
}

func ldapWireReply(writer io.Writer, id int64, operation int) error {
	var body []byte
	for _, field := range []any{asn1.Enumerated(goldap.LDAPResultSuccess), []byte{}, []byte{}} {
		encoded, err := asn1.Marshal(field)
		if err != nil {
			return err
		}
		body = append(body, encoded...)
	}
	packet := struct {
		ID        int64
		Operation asn1.RawValue
	}{id, asn1.RawValue{Class: asn1.ClassApplication, Tag: operation, IsCompound: true, Bytes: body}}
	encoded, err := asn1.Marshal(packet)
	if err != nil {
		return err
	}
	n, err := writer.Write(encoded)
	if err == nil && n != len(encoded) {
		return io.ErrShortWrite
	}
	return err
}

func ldapWireCertificates(t *testing.T, dir, kind string) {
	t.Helper()
	trusted, trustedKey := ldapWireCA(t, "wire-trusted", 1)
	signer, signerKey := trusted, trustedKey
	if kind == "untrusted" {
		signer, signerKey = ldapWireCA(t, "wire-untrusted", 2)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host := "127.0.0.1"
	if kind == "wrong_host" {
		host = "127.0.0.2"
	}
	now := time.Now()
	server := &x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP(host)}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, server, signer, &serverKey.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"trusted-ca.pem": {Type: "CERTIFICATE", Bytes: trusted.Raw},
		"signing-ca.pem": {Type: "CERTIFICATE", Bytes: signer.Raw},
		"server.pem":     {Type: "CERTIFICATE", Bytes: der},
		"server-key.pem": {Type: "PRIVATE KEY", Bytes: key},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "empty-roots"), 0700); err != nil {
		t.Fatal(err)
	}
}

func ldapWireCA(t *testing.T, name string, serial int64) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key
}

func ldapWireCheckCertificates(t *testing.T, dir, kind string) tls.Certificate {
	t.Helper()
	if dir == "" || os.Getenv("SSL_CERT_FILE") != filepath.Join(dir, "trusted-ca.pem") ||
		os.Getenv("SSL_CERT_DIR") != filepath.Join(dir, "empty-roots") {
		t.Fatal("child trust environment is not isolated")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "empty-roots"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("child CA directory must exist and be empty: %v", err)
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem"))
	if err != nil {
		t.Fatalf("invalid server key fixture: %v", err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signing, err := os.ReadFile(filepath.Join(dir, "signing-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := os.ReadFile(filepath.Join(dir, "trusted-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(trusted)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("fixture trust file must contain exactly one CA certificate")
	}
	trustedCA, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("invalid fixture trusted CA: %v", err)
	}
	now := time.Now()
	if !trustedCA.IsCA || trustedCA.CheckSignatureFrom(trustedCA) != nil ||
		now.Before(trustedCA.NotBefore) || !now.Before(trustedCA.NotAfter) {
		t.Fatal("fixture trusted CA must be valid and self-signed")
	}
	if bytes.Equal(signing, trusted) != (kind != "untrusted") {
		t.Fatal("fixture signer/trust relationship is wrong")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(signing) {
		t.Fatal("invalid fixture signing CA")
	}
	host := "127.0.0.1"
	if kind == "wrong_host" {
		host = "127.0.0.2"
	}
	// This verifies fixture construction only. No explicit pool/config reaches
	// the production dialer, whose trust comes from the fresh process loader.
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
		t.Fatalf("server certificate fixture itself is broken: %v", err)
	}
	return certificate
}
