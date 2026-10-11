// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CertificateLoader serves one certificate/key pair and reloads it when the
// certificate file's modification time changes. The last successfully loaded
// certificate is immutable, so GetCertificate and NotAfter are safe to call
// concurrently from HTTP, gRPC, and metrics scrapes.
type CertificateLoader struct {
	certFile   string
	keyFile    string
	publicFile string

	mu      sync.RWMutex
	modTime time.Time
	cert    *tls.Certificate
}

// NewCertificateLoader builds and eagerly validates a reloadable certificate
// loader. Eager loading keeps listener startup fail-closed; later calls to Load
// perform only a certificate-file stat while the mtime is unchanged.
func NewCertificateLoader(certFile, keyFile string) (*CertificateLoader, error) {
	loader := &CertificateLoader{certFile: certFile, keyFile: keyFile}
	if err := loader.Load(); err != nil {
		return nil, err
	}
	return loader, nil
}

// NewCertificateLoaderWithPublicCertificate also publishes the served certificate
// chain for consumers that must not have access to the engine's private material.
func NewCertificateLoaderWithPublicCertificate(certFile, keyFile, publicFile string) (*CertificateLoader, error) {
	if publicFile == "" || filepath.Clean(publicFile) == filepath.Clean(certFile) || filepath.Clean(publicFile) == filepath.Clean(keyFile) {
		return nil, fmt.Errorf("secure: a distinct public certificate path is required")
	}
	loader := &CertificateLoader{certFile: certFile, keyFile: keyFile, publicFile: publicFile}
	if err := loader.Load(); err != nil {
		return nil, err
	}
	return loader, nil
}

// Load reloads the key pair when the certificate file's mtime changes. The
// cached mtime advances only after both files load and the leaf parses, so an
// in-progress or invalid rotation is retried on the next handshake or scrape.
func (l *CertificateLoader) Load() error {
	info, err := os.Stat(l.certFile)
	if err != nil {
		return fmt.Errorf("secure: stat server certificate: %w", err)
	}

	l.mu.RLock()
	unchanged := l.cert != nil && info.ModTime().Equal(l.modTime)
	l.mu.RUnlock()
	if unchanged {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Another caller may have completed the reload while this caller waited.
	info, err = os.Stat(l.certFile)
	if err != nil {
		return fmt.Errorf("secure: stat server certificate: %w", err)
	}
	if l.cert != nil && info.ModTime().Equal(l.modTime) {
		return nil
	}

	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return fmt.Errorf("secure: load server keypair: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return fmt.Errorf("secure: server certificate chain is empty")
	}
	// Go's LoadX509KeyPair populates Leaf; retain a compatibility fallback for
	// environments that explicitly restore the pre-Go-1.23 GODEBUG behavior.
	if cert.Leaf == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("secure: parse server leaf certificate: %w", err)
		}
		cert.Leaf = leaf
	}
	if l.publicFile != "" {
		if err := publishTLSCertificate(l.publicFile, cert.Certificate); err != nil {
			return fmt.Errorf("secure: publish server certificate: %w", err)
		}
	}
	l.cert = &cert
	l.modTime = info.ModTime()
	return nil
}

// GetCertificate implements tls.Config.GetCertificate. Every handshake performs
// a cheap mtime check and observes a successfully rotated pair without restart.
// A reload FAILURE serves the retained last-good pair instead of aborting the
// handshake: a routine non-atomic rotation (cert file written before the key
// file catches up) would otherwise fail every handshake on every listener for
// the whole swap window (adversarial review M1). Hard-fail only when no pair
// has ever loaded — the constructor guarantees one, so that is the fail-closed
// floor, not an expected path.
func (l *CertificateLoader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	err := l.Load()
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.cert == nil {
		if err == nil {
			err = fmt.Errorf("secure: no server certificate loaded")
		}
		return nil, err
	}
	return l.cert, nil
}

// NotAfter returns the expiry of the last successfully loaded leaf.
func (l *CertificateLoader) NotAfter() (time.Time, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.cert == nil || l.cert.Leaf == nil {
		return time.Time{}, false
	}
	return l.cert.Leaf.NotAfter, true
}

// ServerTLSConfig builds the TLS config the engine's gRPC ingest server uses.
// It always serves over TLS ≥1.2 with the given server cert/key. When
// clientCAFile is non-empty it additionally turns on MUTUAL TLS: the server
// requires and verifies a client certificate chaining to a CA in that bundle —
// so only collectors holding a certificate signed by an operator-trusted CA can
// connect (docs/SECURITY-HARDENING.md collector→core, §3). Empty clientCAFile keeps server-only
// TLS (the single-node default, where clients of the gRPC port authenticate with
// a bearer token); there is never a plaintext fallback here.
func ServerTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	loader, err := NewCertificateLoader(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return ServerTLSConfigWithLoader(loader, clientCAFile)
}

// ServerTLSConfigWithLoader builds the server config around a shared reloadable
// certificate loader. A composition root can give the same loader to HTTP and
// gRPC so every listener rotates certificates together.
func ServerTLSConfigWithLoader(loader *CertificateLoader, clientCAFile string) (*tls.Config, error) {
	if loader == nil {
		return nil, fmt.Errorf("secure: server certificate loader is required")
	}
	if err := loader.Load(); err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		GetCertificate: loader.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	if clientCAFile == "" {
		return cfg, nil
	}
	pemBytes, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("secure: read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("secure: client CA %q contains no usable certificate", clientCAFile)
	}
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.RequireAndVerifyClientCert // true mTLS: peer cert mandatory
	return cfg, nil
}

// ClientTLSConfig builds the TLS config a collector uses to dial the core's gRPC
// ingest endpoint (CB-1 option C). caFile, when set, pins the CA that signs the
// core's server certificate (a self-signed core cert is verified by pinning its
// CA/cert here); empty uses the host's system roots. certFile/keyFile, when set,
// present a CLIENT certificate for mutual TLS — required whenever the core enforces
// it with --grpc-client-ca, the secure default for a remote collector (docs/SECURITY-HARDENING.md).
// serverName overrides the verification/SNI name, for dialing a core by IP whose
// certificate names a host. It never disables verification — there is no insecure
// fallback (docs/SECURITY-HARDENING.md).
func ClientTLSConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caFile != "" {
		pemBytes, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("secure: read core CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("secure: core CA %q contains no usable certificate", caFile)
		}
		cfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("secure: load collector client keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// EnsureTLSCert creates local TLS material naming localhost and this host's LAN
// addresses. Managed leaves are renewed when those names change, using the same
// local CA and leaf key. Existing operator certificates are left intact.
// The cert file includes the CA, so clients can use it as their trust bundle.
// Passing operatorProvided=true requires and preserves an explicitly supplied
// pair, including a copied historical generated certificate. An incomplete or
// invalid pair is refused before any write. Omitted preserves the original
// two-argument API and manages the engine's default local material.
func EnsureTLSCert(certPath, keyPath string, operatorProvided ...bool) (created bool, fingerprint string, err error) {
	if len(operatorProvided) > 0 && operatorProvided[0] {
		if _, err := readSecret(keyPath); err != nil {
			return false, "", fmt.Errorf("secure: load operator TLS key: %w", err)
		}
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
			return false, "", fmt.Errorf("secure: load operator TLS pair: %w", err)
		}
		fp, err := certFingerprint(certPath)
		return false, fp, err
	}
	dns := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" && host != "localhost" {
		dns = append(dns, host)
	}
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	for _, address := range LocalLANAddresses() {
		ips = append(ips, net.IP(address.AsSlice()))
	}
	return ensureTLSCert(certPath, keyPath, dns, ips)
}

// certFingerprint returns the SHA-256 fingerprint of the leaf certificate in a
// PEM file.
//
// NOTE, and it is the whole of second defect: this is a digest of the
// CERTIFICATE, in hex. It is what a browser shows and what an operator compares by
// eye. It is NOT the value `--pin-sha256` takes — see SPKIPin. Whoever prints one of
// these must be clear about which, because the engine once printed this one under a
// sentence telling the operator to pin it, and pinning it is impossible.
func certFingerprint(certPath string) (string, error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("secure: no PEM block in %s", certPath)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// SPKIPin returns the leaf certificate's SubjectPublicKeyInfo digest, base64
// encoded — the EXACT string the CLI's `--pin-sha256` accepts and compares.
//
// It exists so the engine can tell an operator to pin something and hand them the
// value that works. Before the first-boot line printed certFingerprint instead:
// a different digest of different bytes in a different encoding, which the flag
// rejected, and the several openssl incantations that produce this value were
// documented nowhere in the product.
//
// Pinning the SPKI rather than the certificate is deliberate and is why the two
// cannot simply be merged: the SPKI pin survives a certificate renewal that keeps
// the same key pair, which is what makes pinning operable at all.
//
// The encoding is base64 WITHOUT padding, and that is not cosmetic. Measured on the
// real binary while fixing: padded base64 ends in '=', slog's text handler
// quotes any value containing '=', and the first-boot line therefore rendered
// pin_sha256="B3PL…tVY=" — so an operator copying "that value, verbatim" copied the
// quotation marks too and the flag rejected it. Unpadded base64 is A-Za-z0-9+/ only,
// renders bare, and is byte-identical to the value the pin-mismatch error reports,
// so the value you are told to use and the value you are shown on failure are the
// same string. decodeSPKIPin accepts both paddings regardless.
func SPKIPin(certPath string) (string, error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf("secure: read certificate %s: %w", certPath, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("secure: no PEM block in %s", certPath)
	}
	pin, err := spkiPinOfDER(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("secure: parse certificate %s: %w", certPath, err)
	}
	return pin, nil
}

// spkiPinOfDER is the pin of one DER certificate: base64 (no padding) of the SHA-256
// of its SubjectPublicKeyInfo, the value --pin-sha256 takes.
func spkiPinOfDER(der []byte) (string, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// Pin is the --pin-sha256 value of the certificate this loader serves now, the same
// value SPKIPin gives for its file; it follows a reloaded certificate. It is public by
// nature: every TLS client receives the certificate.
func (l *CertificateLoader) Pin() (string, error) {
	cert, err := l.GetCertificate(nil)
	if err != nil {
		return "", err
	}
	if cert == nil || len(cert.Certificate) == 0 {
		return "", errors.New("secure: no certificate loaded")
	}
	return spkiPinOfDER(cert.Certificate[0])
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
