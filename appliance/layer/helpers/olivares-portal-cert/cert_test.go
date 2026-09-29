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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

const testOperationID = "0123456789abcdef0123456789abcdef"

// facts is a host whose portal origin and addresses the case chooses.
type facts struct {
	origin    string
	addresses []netip.Addr
	err       error
}

func (f *facts) Origin() (string, error)          { return f.origin, f.err }
func (f *facts) Addresses() ([]netip.Addr, error) { return f.addresses, f.err }

// testNow is the moment every case generates at.
var testNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// newEnv returns the helper's environment rooted in fresh directories: the portal's TLS
// directory, the portal's certificate spool and the helper's own state directory.
func newEnv(t *testing.T, f *facts) env {
	t.Helper()
	root := t.TempDir()
	e := env{
		TLSDir:   filepath.Join(root, "etc-olivares-portal"),
		SpoolDir: filepath.Join(root, "spool-cert"),
		StateDir: filepath.Join(root, "state"),
		Facts:    f,
		Now:      func() time.Time { return testNow },
		Random:   rand.Reader,
	}
	for _, dir := range []string{e.TLSDir, e.SpoolDir, e.StateDir, filepath.Join(e.StateDir, keyStore)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(e.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

// selfSigned returns a certificate and its key, as PEM, naming name.
func selfSigned(t *testing.T, name string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: testNow.Add(-time.Hour), NotAfter: testNow.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
}

// storeKey places keyPEM in the helper's key store under name, as the protected store holds it.
func storeKey(t *testing.T, e env, name string, keyPEM []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.StateDir, keyStore, name+".key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

// issued runs begin and returns the nonce it answered.
func issued(t *testing.T, e env) string {
	t.Helper()
	response := e.begin()
	if response.Result != helperschema.ResultPerformed || len(response.Nonce) != 64 {
		t.Fatalf("begin answered %+v, want a nonce", response)
	}
	return response.Nonce
}

// installed reads the pair in the portal's TLS directory.
func installed(t *testing.T, e env) (certPEM, keyPEM []byte) {
	t.Helper()
	certPEM, err := os.ReadFile(filepath.Join(e.TLSDir, "tls.crt"))
	if err != nil {
		t.Fatalf("no certificate was installed: %v", err)
	}
	keyPEM, err = os.ReadFile(filepath.Join(e.TLSDir, "tls.key"))
	if err != nil {
		t.Fatalf("no key was installed: %v", err)
	}
	return certPEM, keyPEM
}

func TestCertHelper_FilenameIsDerivedFromItsNonce(t *testing.T) {
	e := newEnv(t, &facts{})
	certPEM, keyPEM := selfSigned(t, "console.example.test")
	storeKey(t, e, "portal", keyPEM)

	t.Run("begin issues a nonce and install reads the one file named by it", func(t *testing.T) {
		nonce := issued(t, e)
		spool := filepath.Join(e.SpoolDir, nonce+".pem")
		if err := os.WriteFile(spool, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		response := e.install(nonce, "store:portal")
		if response.Result != helperschema.ResultPerformed {
			t.Fatalf("install of the nonce's file answered %+v", response)
		}
		gotCert, gotKey := installed(t, e)
		if !bytes.Equal(gotCert, certPEM) || !bytes.Equal(gotKey, keyPEM) {
			t.Fatal("the installed pair is not the spooled certificate and the referenced key")
		}
		// The spooled file is consumed: the same nonce names nothing a second time.
		if _, err := os.Lstat(spool); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the spooled file outlived its install: %v", err)
		}
		if again := e.install(nonce, "store:portal"); again.Result != helperschema.ResultRefused {
			t.Fatalf("a replayed nonce answered %+v", again)
		}
	})

	t.Run("a nonce this helper did not issue names no file, even when that file exists", func(t *testing.T) {
		var other helperschema.SpoolKey
		copy(other[:], "another helper's spool key, 32 b")
		foreign, err := helperschema.IssueNonce(other, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		own := issued(t, e)
		for name, nonce := range map[string]string{
			"issued under another key":  string(foreign),
			"the issued nonce, flipped": own[:63] + map[bool]string{true: "1", false: "0"}[own[63] == '0'],
		} {
			if err := os.WriteFile(filepath.Join(e.SpoolDir, nonce+".pem"), certPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			if response := e.install(nonce, "store:portal"); response.Result != helperschema.ResultRefused ||
				response.Code != helperschema.CodeInputRefused {
				t.Errorf("%s: install answered %+v, want input_refused", name, response)
			}
		}
	})

	t.Run("the file behind an issued nonce is a regular file, never a link", func(t *testing.T) {
		nonce := issued(t, e)
		target := filepath.Join(t.TempDir(), "elsewhere.pem")
		if err := os.WriteFile(target, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(e.SpoolDir, nonce+".pem")); err != nil {
			t.Fatal(err)
		}
		if response := e.install(nonce, "store:portal"); response.Result != helperschema.ResultRefused {
			t.Fatalf("a link in the spool answered %+v", response)
		}
	})

	t.Run("the document names no file: a caller supplies a nonce, never a name", func(t *testing.T) {
		nonce := issued(t, e)
		for _, document := range []string{
			`{"op": "install", "nonce": "` + nonce + `", "key_ref": "store:portal", "operation_id": "` + testOperationID + `", "file": "/etc/DO-NOT-PRINT"}`,
			`{"op": "install", "nonce": "` + nonce + `.pem", "key_ref": "store:portal", "operation_id": "` + testOperationID + `"}`,
			`{"op": "install", "nonce": "../../etc/DO-NOT-PRINT", "key_ref": "store:portal", "operation_id": "` + testOperationID + `"}`,
			`{"op": "install", "nonce": "` + strings.ToUpper(nonce) + `", "key_ref": "store:portal", "operation_id": "` + testOperationID + `"}`,
			`{"op": "begin", "nonce": "` + nonce + `", "operation_id": "` + testOperationID + `"}`,
		} {
			err := helperschema.Decode(strings.NewReader(document), &helperschema.CertRequest{})
			var input *helperschema.InputError
			if !errors.As(err, &input) || strings.Contains(err.Error(), "DO-NOT-PRINT") {
				t.Errorf("%s: %v, want an input refusal that repeats nothing", document, err)
			}
		}
		name, err := helperschema.SpoolKey{}.SpoolName(strings.Repeat("0", 64), helperschema.SpoolCertificate)
		if err == nil {
			t.Fatalf("an unissued nonce derived %q", name)
		}
	})
}

func TestCertHelper_KeyIsAReferenceNeverALiteral(t *testing.T) {
	e := newEnv(t, &facts{})
	certPEM, keyPEM := selfSigned(t, "console.example.test")
	keyHeader := strings.SplitN(string(keyPEM), "\n", 2)[0]
	storeKey(t, e, "portal", keyPEM)
	nonce := issued(t, e)
	if err := os.WriteFile(filepath.Join(e.SpoolDir, nonce+".pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a key literal, a path or anything but store:<name> is refused and never repeated", func(t *testing.T) {
		literal := strings.ReplaceAll(string(keyPEM), "\n", `\n`)
		for _, ref := range []string{
			literal,
			keyHeader,
			"MHcCAQEEIDO-NOT-PRINT",
			"/etc/olivares-portal/tls.key",
			"file:/etc/DO-NOT-PRINT",
			"store:../DO-NOT-PRINT",
			"store:/DO-NOT-PRINT",
			"store:",
			"store:-lead",
			"STORE:portal",
			"store:Portal",
			"store:" + strings.Repeat("a", 64),
			"",
		} {
			document := `{"op": "install", "nonce": "` + nonce + `", "key_ref": "` + ref + `", "operation_id": "` + testOperationID + `"}`
			err := helperschema.Decode(strings.NewReader(document), &helperschema.CertRequest{})
			var input *helperschema.InputError
			if !errors.As(err, &input) || input.Field != "$.key_ref" && input.Field != "$" {
				t.Errorf("key_ref %.40q: %v, want an input refusal at $.key_ref", ref, err)
				continue
			}
			for _, secret := range []string{"DO-NOT-PRINT", "BEGIN", "MHcC"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the refusal repeats the value: %v", err)
				}
			}
		}
		if !helperschema.KeyRefShape("store:portal") || !helperschema.KeyRefShape("store:portal-2026") {
			t.Fatal("control: a key reference was refused")
		}
	})

	t.Run("the document has no field that could carry key material", func(t *testing.T) {
		for _, field := range []string{"key", "private_key", "key_pem", "pem", "certificate"} {
			document := `{"op": "install", "nonce": "` + nonce + `", "key_ref": "store:portal", "operation_id": "` +
				testOperationID + `", "` + field + `": "` + keyHeader + `"}`
			if err := helperschema.Decode(strings.NewReader(document), &helperschema.CertRequest{}); err == nil {
				t.Errorf("a document with %q was accepted", field)
			}
		}
	})

	t.Run("a reference resolves in the helper's key store alone, to a protected regular file", func(t *testing.T) {
		if response := e.install(nonce, "store:absent"); response.Result != helperschema.ResultRefused {
			t.Errorf("an unknown reference answered %+v", response)
		}
		loose := filepath.Join(e.StateDir, keyStore, "loose.key")
		if err := os.WriteFile(loose, keyPEM, 0o644); err != nil {
			t.Fatal(err)
		}
		if response := e.install(nonce, "store:loose"); response.Result != helperschema.ResultRefused {
			t.Errorf("a key readable by others answered %+v", response)
		}
		if err := os.Symlink(loose, filepath.Join(e.StateDir, keyStore, "linked.key")); err != nil {
			t.Fatal(err)
		}
		if response := e.install(nonce, "store:linked"); response.Result != helperschema.ResultRefused {
			t.Errorf("a linked key answered %+v", response)
		}
		for _, name := range []string{"tls.crt", "tls.key"} {
			if _, err := os.Lstat(filepath.Join(e.TLSDir, name)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused install left %s: %v", name, err)
			}
		}
	})

	t.Run("an answer never carries the key", func(t *testing.T) {
		response := e.install(nonce, "store:portal")
		if response.Result != helperschema.ResultPerformed {
			t.Fatalf("control: install answered %+v", response)
		}
		body := response.Detail + response.Nonce + string(response.Bundle)
		block, _ := pem.Decode(keyPEM)
		if strings.Contains(body, "PRIVATE") || strings.Contains(body, hex.EncodeToString(block.Bytes[:8])) {
			t.Fatalf("the answer carries key material: %+v", response)
		}
	})

	t.Run("a spooled file that holds a key is refused: the key is never a literal in the spool either", func(t *testing.T) {
		nonce := issued(t, e)
		if err := os.WriteFile(filepath.Join(e.SpoolDir, nonce+".pem"), append(append([]byte{}, certPEM...), keyPEM...), 0o600); err != nil {
			t.Fatal(err)
		}
		if response := e.install(nonce, "store:portal"); response.Result != helperschema.ResultRefused {
			t.Fatalf("a spooled key answered %+v", response)
		}
	})
}

func TestCertHelper_GenerateNamesTheCurrentOriginAndAddresses(t *testing.T) {
	host := &facts{origin: "console.example.test",
		addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::10")}}
	e := newEnv(t, host)

	generated := func(t *testing.T) (*x509.Certificate, string) {
		t.Helper()
		response := helper(e).Perform(context.Background(), helperschema.Peer{}, &helperschema.CertRequest{Op: helperschema.CertGenerate, OperationID: testOperationID})
		if response.Result != helperschema.ResultPerformed {
			t.Fatalf("generate answered %+v", response)
		}
		certPEM, keyPEM := installed(t, e)
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("the generated certificate and key do not form a pair: %v", err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
			t.Fatal("the generated server leaf grants CA signing authority")
		}
		if !bytes.Equal(leaf.RawIssuer, leaf.RawSubject) {
			t.Fatal("the generated certificate is not self-issued")
		}
		if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
			t.Fatalf("the generated certificate is not self-signed: %v", err)
		}
		tampered := bytes.Clone(leaf.Signature)
		tampered[len(tampered)-1] ^= 1
		if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, tampered); err == nil {
			t.Fatal("a modified leaf signature verified")
		}
		if leaf.NotBefore.After(testNow) || leaf.NotAfter.Before(testNow) {
			t.Fatalf("the certificate is not valid now: %v to %v", leaf.NotBefore, leaf.NotAfter)
		}
		for name, mode := range map[string]os.FileMode{"tls.key": 0o600, "tls.crt": 0o644} {
			info, err := os.Lstat(filepath.Join(e.TLSDir, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
				t.Fatalf("%s is %v (%v), want a regular file mode %o", name, info.Mode(), err, mode)
			}
		}
		sum := sha256.Sum256(leaf.Raw)
		return leaf, hex.EncodeToString(sum[:])
	}

	leaf, first := generated(t)
	if leaf.Subject.CommonName != "console.example.test" || !slices.Equal(leaf.DNSNames, []string{"console.example.test"}) {
		t.Errorf("the certificate names %q and %v, want the current origin alone", leaf.Subject.CommonName, leaf.DNSNames)
	}
	if got := ipStrings(leaf); !slices.Equal(got, []string{"192.0.2.10", "2001:db8::10"}) {
		t.Errorf("the certificate names the addresses %v, want the current ones", got)
	}

	t.Run("the names are read when generate runs, never remembered", func(t *testing.T) {
		host.origin, host.addresses = "new.example.test", []netip.Addr{netip.MustParseAddr("198.51.100.7")}
		leaf, second := generated(t)
		if second == first {
			t.Fatal("a second generate installed the same certificate")
		}
		if !slices.Equal(leaf.DNSNames, []string{"new.example.test"}) || !slices.Equal(ipStrings(leaf), []string{"198.51.100.7"}) {
			t.Fatalf("the certificate names %v and %v, want only the current origin and address", leaf.DNSNames, ipStrings(leaf))
		}
	})

	t.Run("no origin or address the host cannot state is invented, and nothing is replaced", func(t *testing.T) {
		before, _ := installed(t, e)
		for name, broken := range map[string]*facts{
			"unreadable facts":          {err: errors.New("the host's facts cannot be read")},
			"no origin":                 {addresses: host.addresses},
			"an origin that is no name": {origin: "bad name!", addresses: host.addresses},
			"no address":                {origin: "console.example.test"},
		} {
			f := newEnv(t, broken)
			f.TLSDir = e.TLSDir
			response := f.generate()
			if response.Result == helperschema.ResultPerformed {
				t.Errorf("%s: generate answered %+v", name, response)
			}
			if after, _ := installed(t, e); !bytes.Equal(after, before) {
				t.Errorf("%s: the installed certificate changed", name)
			}
		}
	})

	t.Run("a caller cannot add a name or an address", func(t *testing.T) {
		for _, field := range []string{`"dns_names": ["evil.example"]`, `"addresses": ["203.0.113.1"]`, `"origin": "evil.example"`} {
			document := `{"op": "generate", "operation_id": "` + testOperationID + `", ` + field + `}`
			if err := helperschema.Decode(strings.NewReader(document), &helperschema.CertRequest{}); err == nil {
				t.Errorf("generate accepted %s", field)
			}
		}
	})

	t.Run("generate is the repair console's alone", func(t *testing.T) {
		for _, rule := range helperschema.CertRules() {
			want := []helperschema.Invoker(nil)
			if rule.Subcommand == helperschema.CertGenerate {
				want = []helperschema.Invoker{helperschema.RepairConsole}
			}
			if !rule.Mutating || !slices.Equal(rule.Invokers, want) {
				t.Errorf("cert %s is %+v, want a mutating subcommand for %v", rule.Subcommand, rule, want)
			}
		}
	})
}

// ipStrings returns the certificate's addresses as text, in order.
func ipStrings(leaf *x509.Certificate) []string {
	var out []string
	for _, ip := range leaf.IPAddresses {
		addr, _ := netip.AddrFromSlice(ip)
		out = append(out, addr.Unmap().String())
	}
	return out
}
