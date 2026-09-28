// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/portal"
)

// keyStore is the directory, in the helper's own state directory, where a key reference resolves:
// store:<name> is <keyStore>/<name>.key and nothing else.
const keyStore = "keys"

// spoolKeyFile is the helper's own spool key, in its state directory: the secret that makes each
// nonce it issued verifiable without a list of them.
const spoolKeyFile = "spool.key"

// maxPEMBytes bounds a spooled certificate chain and a stored key.
const maxPEMBytes = 64 * 1024

// validity is how long a generated certificate is valid: the longest a browser accepts for a
// server certificate.
const validity = 397 * 24 * time.Hour

// hostFacts is what the host states about the portal when generate runs: its origin, the host
// name a browser reaches it by, and the addresses it listens on. It is read each time, never kept.
type hostFacts interface {
	Origin() (string, error)
	Addresses() ([]netip.Addr, error)
}

// env is the helper's world: the portal's TLS directory it installs into, the portal's spool it
// reads from, its own state directory, the host's facts, a clock and a source of randomness.
type env struct {
	TLSDir   string
	SpoolDir string
	StateDir string
	Facts    hostFacts
	Now      func() time.Time
	Random   io.Reader
}

// helper is the certificate helper over e.
func helper(e env) invocation.Helper {
	return invocation.Helper{
		Name:       helperschema.HelperCert,
		Rules:      helperschema.CertRules(),
		NewRequest: func() helperschema.Request { return &helperschema.CertRequest{} },
		Perform: func(_ context.Context, _ helperschema.Peer, request helperschema.Request) helperschema.Response {
			req, ok := request.(*helperschema.CertRequest)
			if !ok {
				return refused(helperschema.CodeInputRefused, "not a document of this helper")
			}
			switch req.Op {
			case helperschema.CertBegin:
				return e.begin()
			case helperschema.CertInstall:
				return e.install(req.Nonce, req.KeyRef)
			case helperschema.CertGenerate:
				return e.generate()
			}
			return refused(helperschema.CodeInputRefused, "not a subcommand of this helper")
		},
	}
}

func refused(code, detail string) helperschema.Response {
	return helperschema.Response{Result: helperschema.ResultRefused, Code: code, Detail: detail}
}

func failed(detail string) helperschema.Response {
	return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed, Detail: detail}
}

// begin issues a nonce under the helper's spool key, creating the key the first time.
func (e env) begin() helperschema.Response {
	key, err := e.spoolKey(true)
	if err != nil {
		return failed("the helper's spool key cannot be read or created; no nonce was issued")
	}
	nonce, err := helperschema.IssueNonce(key, e.Random)
	if err != nil {
		return failed("no random bytes for a nonce; no nonce was issued")
	}
	return helperschema.Response{Result: helperschema.ResultPerformed, Nonce: string(nonce),
		Detail: "a nonce was issued; the certificate is spooled under it and installed by install"}
}

// install installs the certificate spooled under nonce with the key keyRef names, and consumes the
// spooled file. The file's name is derived from a nonce this helper issued, never taken; the key is
// read from the helper's key store, never sent. Nothing is replaced unless the certificate and the
// key form a pair.
func (e env) install(nonce, keyRef string) helperschema.Response {
	key, err := e.spoolKey(false)
	if err != nil {
		return refused(helperschema.CodeInputRefused, "this helper has issued no nonce")
	}
	name, err := key.SpoolName(nonce, helperschema.SpoolCertificate)
	if err != nil {
		return refused(helperschema.CodeInputRefused, "not a nonce this helper issued")
	}
	certPEM, err := readUnder(e.SpoolDir, name, false)
	if err != nil {
		return refused(helperschema.CodeInputRefused, "no spooled certificate is readable under this nonce")
	}
	if !onlyCertificates(certPEM) {
		return refused(helperschema.CodeInputRefused, "the spooled file holds something other than certificates")
	}
	storeName, ok := helperschema.KeyRefName(keyRef)
	if !ok {
		return refused(helperschema.CodeInputRefused, "not a key reference")
	}
	keyPEM, err := readUnder(filepath.Join(e.StateDir, keyStore), storeName+".key", true)
	if err != nil {
		return refused(helperschema.CodeInputRefused, "the key reference names no protected key in the helper's store")
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return refused(helperschema.CodeInputRefused, "the spooled certificate and the referenced key do not form a pair")
	}
	if err := writePair(e.TLSDir, certPEM, keyPEM); err != nil {
		return failed("the pair could not be installed; the portal's previous pair is unchanged or refused by its custody check")
	}
	if root, err := os.OpenRoot(e.SpoolDir); err == nil {
		_ = root.Remove(name)
		_ = root.Close()
	}
	return helperschema.Response{Result: helperschema.ResultPerformed,
		Detail: "the spooled certificate was installed with the referenced key"}
}

// generate creates a new self-signed pair naming the portal's current origin and addresses, as the
// host states them now, and installs it. A host that cannot state them gets no certificate.
func (e env) generate() helperschema.Response {
	if e.Facts == nil {
		return failed("the host's facts are not wired; nothing was generated")
	}
	origin, err := e.Facts.Origin()
	if err != nil || !dnsName(origin) {
		return refused(helperschema.CodeInputRefused, "the host states no portal origin that is a host name; nothing was generated")
	}
	addresses, err := e.Facts.Addresses()
	if err != nil || len(addresses) == 0 {
		return refused(helperschema.CodeInputRefused, "the host states no portal address; nothing was generated")
	}
	addresses = slices.Clone(addresses)
	slices.SortFunc(addresses, func(a, b netip.Addr) int { return a.Compare(b) })
	addresses = slices.Compact(addresses)
	certPEM, keyPEM, err := newSelfSignedPair(strings.ToLower(origin), addresses, e.Now(), e.Random)
	if err != nil {
		return failed("the pair could not be generated; nothing was installed")
	}
	if err := writePair(e.TLSDir, certPEM, keyPEM); err != nil {
		return failed("the pair could not be installed; the portal's previous pair is unchanged or refused by its custody check")
	}
	return helperschema.Response{Result: helperschema.ResultPerformed,
		Detail: "a new self-signed certificate naming the portal's current origin and addresses was installed"}
}

// newSelfSignedPair returns a new P-256 key and a self-signed server certificate for origin and
// addresses.
func newSelfSignedPair(origin string, addresses []netip.Addr, now time.Time, random io.Reader) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), random)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(random, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, a := range addresses {
		ips = append(ips, net.IP(a.Unmap().AsSlice()))
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: origin},
		DNSNames:              []string{origin},
		IPAddresses:           ips,
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(random, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), nil
}

// writePair replaces the portal's pair in dir: each file is written under a fixed temporary name,
// created exclusively, synced, given its mode and renamed over the old one, key first, and then the
// directory is synced. A crash between the two renames leaves a certificate and a key that do not
// pair, which the portal's custody check refuses: it serves nothing rather than a mismatched pair.
func writePair(dir string, certPEM, keyPEM []byte) error {
	if err := protectedDirectory(dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := writeNew(root, portal.KeyFile+".new", keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeNew(root, portal.CertificateFile+".new", certPEM, 0o644); err != nil {
		return err
	}
	if err := root.Rename(portal.KeyFile+".new", portal.KeyFile); err != nil {
		return err
	}
	if err := root.Rename(portal.CertificateFile+".new", portal.CertificateFile); err != nil {
		return err
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeNew creates name in root exclusively, with no link followed, writes data, sets mode and
// syncs. A file left under the fixed name by an interrupted run is removed first.
func writeNew(root *os.Root, name string, data []byte, mode os.FileMode) error {
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	chmodErr := f.Chmod(mode)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}

// protectedDirectory refuses a directory that is a link, not a directory, or writable by its group
// or others.
func protectedDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("the directory is not protected")
	}
	return nil
}

// readUnder reads name in dir: a regular file, never a link, of at most 64 KiB, the same file when
// opened as when measured. When private is set it must also be readable by its owner alone.
func readUnder(dir, name string, private bool) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	measured, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !measured.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(measured, info) || !info.Mode().IsRegular() || info.Size() > maxPEMBytes ||
		private && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("not a protected regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPEMBytes+1))
	if err != nil || len(data) > maxPEMBytes {
		return nil, errors.New("unreadable")
	}
	return data, nil
}

// onlyCertificates reports whether data is one or more PEM certificates and nothing else.
func onlyCertificates(data []byte) bool {
	rest := bytes.TrimSpace(data)
	count := 0
	for len(rest) > 0 {
		block, next := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false
		}
		count++
		rest = bytes.TrimSpace(next)
	}
	return count > 0
}

// spoolKey reads the helper's spool key, creating it exclusively when create is set and it is
// absent. The key is a regular file of exactly 32 bytes, readable by its owner alone.
func (e env) spoolKey(create bool) (helperschema.SpoolKey, error) {
	var key helperschema.SpoolKey
	data, err := readUnder(e.StateDir, spoolKeyFile, true)
	if err == nil {
		if len(data) != len(key) {
			return key, errors.New("the spool key is not 32 bytes")
		}
		copy(key[:], data)
		return key, nil
	}
	if !create || !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	if _, err := io.ReadFull(e.Random, key[:]); err != nil {
		return key, err
	}
	root, err := os.OpenRoot(e.StateDir)
	if err != nil {
		return key, err
	}
	defer root.Close()
	f, err := root.OpenFile(spoolKeyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return key, err
	}
	_, writeErr := f.Write(key[:])
	syncErr := f.Sync()
	closeErr := f.Close()
	return key, errors.Join(writeErr, syncErr, closeErr)
}

// dnsName reports whether s is a host name: 1 to 253 characters of dot-separated labels, each 1 to
// 63 letters, digits and inner hyphens.
func dnsName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
