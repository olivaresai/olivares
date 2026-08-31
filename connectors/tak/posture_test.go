// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// posture_test.go exercises the posture pass: the pure CoreConfig->findings mapping,
// the minimal-data guarantee (no secret ever reaches a finding), the honest
// degradation of an unreadable config / unreachable server into findings rather than
// errors, and the mTLS client's refusal to skip verification.

// --- shared helpers ---------------------------------------------------------

// doerFunc adapts a function to the httpDoer seam, so a posture test never starts a
// real server.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func hasFindingKind(fs []model.FindingReport, kind string) bool {
	for _, f := range fs {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

// writeCoreConfig writes xml to a temp CoreConfig.xml and returns its path.
func writeCoreConfig(t *testing.T, xml string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "CoreConfig.xml")
	if err := os.WriteFile(p, []byte(xml), 0o600); err != nil {
		t.Fatalf("write CoreConfig: %v", err)
	}
	return p
}

// genKeyPair mints a throwaway self-signed ed25519 leaf and returns its PEM cert and
// PKCS#8 PEM key, suitable for tls.X509KeyPair.
func genKeyPair(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 keygen: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tak-test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// --- postureFindings: every kind + a clean config --------------------------

func TestPostureFindingsByKind(t *testing.T) {
	crlPresent := &CRL{Name: "Marti CA", CRLFile: "certs/ca.crl"}

	cases := []struct {
		name string
		cfg  Configuration
		kind string
		sev  model.Severity
	}{
		{
			"input_unencrypted_udp",
			Configuration{Network: &Network{Inputs: []Input{{Name: "stdudp", Protocol: "udp", Port: "8087", Auth: "x509"}}}},
			findingInputUnencrypted, model.SeverityHigh,
		},
		{
			"input_unencrypted_stcp",
			Configuration{Network: &Network{Inputs: []Input{{Name: "streamtcp", Protocol: "stcp", Port: "8088", Auth: "x509"}}}},
			findingInputUnencrypted, model.SeverityHigh,
		},
		{
			"input_anonymous",
			Configuration{Network: &Network{Inputs: []Input{{Name: "anon", Protocol: "tls", Port: "8089"}}}},
			findingInputAnonymous, model.SeverityMedium,
		},
		{
			"sa_announce_enabled",
			Configuration{Network: &Network{Announce: &Announce{Enable: "true"}}},
			findingSAAnnounceEnabled, model.SeverityInfo,
		},
		{
			"keystore_default_password",
			Configuration{Security: &Security{TLS: &TLSConfig{KeystorePass: takDefaultPassword, CRL: crlPresent}}},
			findingDefaultKeystorePass, model.SeverityCritical,
		},
		{
			"truststore_default_password",
			Configuration{Security: &Security{TLS: &TLSConfig{TruststorePass: takDefaultPassword, CRL: crlPresent}}},
			findingDefaultKeystorePass, model.SeverityCritical,
		},
		{
			"tls_legacy_version",
			Configuration{Security: &Security{TLS: &TLSConfig{Context: "TLSv1", CRL: crlPresent}}},
			findingTLSLegacyVersion, model.SeverityHigh,
		},
		{
			"no_crl",
			Configuration{Security: &Security{TLS: &TLSConfig{Context: "TLSv1.2"}}}, // CRL nil
			findingNoCRL, model.SeverityLow,
		},
		{
			"takserver_ca_default_password",
			Configuration{CertificateSigning: &CertificateSigning{TAKServerCAConfig: &TAKServerCAConfig{KeystorePass: takDefaultPassword}}},
			findingDefaultKeystorePass, model.SeverityCritical,
		},
		{
			"msca_truststore_default_password",
			Configuration{CertificateSigning: &CertificateSigning{MicrosoftCAConfig: &MicrosoftCAConfig{TruststorePass: takDefaultPassword}}},
			findingDefaultKeystorePass, model.SeverityCritical,
		},
		{
			"msca_inline_password",
			Configuration{CertificateSigning: &CertificateSigning{MicrosoftCAConfig: &MicrosoftCAConfig{Password: "hunter2"}}},
			findingMSCAInlinePassword, model.SeverityHigh,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := postureFindings(tc.cfg, "ref", fixedAt)
			var found *model.FindingReport
			for i := range got {
				if got[i].Kind == tc.kind {
					found = &got[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("no finding of kind %q; got %+v", tc.kind, got)
			}
			if found.Severity != tc.sev {
				t.Errorf("severity = %q, want %q", found.Severity, tc.sev)
			}
			if found.SubjectKind != subjectKindServer {
				t.Errorf("SubjectKind = %q, want %q", found.SubjectKind, subjectKindServer)
			}
			if found.SubjectRef != "ref" {
				t.Errorf("SubjectRef = %q, want ref", found.SubjectRef)
			}
			if found.Title == "" || found.DetailHash == "" {
				t.Errorf("finding missing Title/DetailHash: %+v", found)
			}
		})
	}
}

func TestPostureCleanConfigYieldsNoBadFindings(t *testing.T) {
	clean := Configuration{
		Network: &Network{
			Inputs:   []Input{{Name: "stdssl", Protocol: "tls", Port: "8089", Auth: "x509"}},
			Announce: &Announce{Enable: "false"},
		},
		Security: &Security{TLS: &TLSConfig{
			Context:        "TLSv1.2",
			KeystorePass:   "a-strong-unique-passphrase",
			TruststorePass: "another-strong-passphrase",
			CRL:            &CRL{Name: "Marti CA", CRLFile: "certs/ca.crl"},
		}},
		CertificateSigning: &CertificateSigning{TAKServerCAConfig: &TAKServerCAConfig{KeystorePass: "strong-ca-passphrase"}},
	}
	if got := postureFindings(clean, "ref", fixedAt); len(got) != 0 {
		t.Fatalf("clean config yielded %d findings, want 0: %+v", len(got), got)
	}
}

// TestPostureNeverLeaksSecrets is the minimal-data guarantee: a keystore default
// password and an inline MS-CA password must NEVER survive into any finding field.
func TestPostureNeverLeaksSecrets(t *testing.T) {
	cfg := Configuration{
		Security:           &Security{TLS: &TLSConfig{KeystorePass: "atakatak"}}, // CRL nil -> also no_crl, fine
		CertificateSigning: &CertificateSigning{MicrosoftCAConfig: &MicrosoftCAConfig{Password: "hunter2"}},
	}
	got := postureFindings(cfg, "https://takserver.example.mil:8443", fixedAt)
	if !hasFindingKind(got, findingDefaultKeystorePass) {
		t.Fatal("fixture should have produced the default-keystore-password finding")
	}
	if !hasFindingKind(got, findingMSCAInlinePassword) {
		t.Fatal("fixture should have produced the inline-MS-CA-password finding")
	}
	for _, f := range got {
		blob, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("marshal finding: %v", err)
		}
		if bytes.Contains(blob, []byte("atakatak")) {
			t.Errorf("finding leaked keystore password: %s", blob)
		}
		if bytes.Contains(blob, []byte("hunter2")) {
			t.Errorf("finding leaked MS-CA password: %s", blob)
		}
	}
}

// --- gatherPosture: honest degradation --------------------------------------

func TestGatherPostureUnreadableCoreConfig(t *testing.T) {
	cfg := config{
		coreConfigPath: filepath.Join(t.TempDir(), "does-not-exist", "CoreConfig.xml"),
		posture:        true,
	}
	got, err := gatherPosture(context.Background(), cfg, nil, fixedAt)
	if err != nil {
		t.Fatalf("gatherPosture returned error, want none: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings = %d, want exactly 1", len(got))
	}
	if got[0].Kind != findingCoreConfigUnreadable {
		t.Errorf("Kind = %q, want %q", got[0].Kind, findingCoreConfigUnreadable)
	}
}

func TestGatherPostureProbeNon2xx(t *testing.T) {
	path := writeCoreConfig(t, `<Configuration><security><tls keystorePass="atakatak"/></security></Configuration>`)
	cfg := config{
		coreConfigPath: path,
		serverURL:      "https://takserver.example.mil:8443",
		versionPath:    "/Marti/api/version",
		posture:        true,
		requestTimeout: 5 * time.Second,
	}
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("service unavailable")),
			Header:     make(http.Header),
		}, nil
	})

	got, err := gatherPosture(context.Background(), cfg, doer, fixedAt)
	if err != nil {
		t.Fatalf("gatherPosture returned error, want none: %v", err)
	}
	if !hasFindingKind(got, findingAPIUnreachable) {
		t.Errorf("want a %q finding for a non-2xx probe; got %+v", findingAPIUnreachable, got)
	}
	// The offline CoreConfig findings must still be present alongside the probe fault.
	if !hasFindingKind(got, findingDefaultKeystorePass) {
		t.Errorf("CoreConfig findings must survive a failed probe; got %+v", got)
	}
}

func TestGatherPostureFindingsAreSorted(t *testing.T) {
	path := writeCoreConfig(t, `<Configuration>
  <network>
    <input _name="zzz" protocol="tcp" port="8088"/>
    <input _name="aaa" protocol="udp" port="8087" auth="x509"/>
    <announce enable="true"/>
  </network>
  <security><tls context="TLSv1"/></security>
</Configuration>`)
	cfg := config{coreConfigPath: path, posture: true}

	run1, err := gatherPosture(context.Background(), cfg, nil, fixedAt)
	if err != nil {
		t.Fatalf("gatherPosture #1: %v", err)
	}
	run2, err := gatherPosture(context.Background(), cfg, nil, fixedAt)
	if err != nil {
		t.Fatalf("gatherPosture #2: %v", err)
	}

	if len(run1) < 3 {
		t.Fatalf("expected several findings from the multi-issue config, got %d", len(run1))
	}
	if len(run1) != len(run2) {
		t.Fatalf("nondeterministic count: %d vs %d", len(run1), len(run2))
	}
	for i := range run1 {
		if run1[i].Kind != run2[i].Kind || run1[i].Title != run2[i].Title {
			t.Fatalf("nondeterministic order at %d: %q/%q vs %q/%q",
				i, run1[i].Kind, run1[i].Title, run2[i].Kind, run2[i].Title)
		}
	}
	// And the order is the documented total order (Kind, SubjectRef, Title, DetailHash).
	for i := 1; i < len(run1); i++ {
		if lessFinding(run1[i], run1[i-1]) {
			t.Errorf("findings not sorted at index %d: %q after %q", i, run1[i].Kind, run1[i-1].Kind)
		}
	}
}

// lessFinding mirrors sortFindings' comparator, so the test asserts the exact order
// the connector documents.
func lessFinding(a, b model.FindingReport) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.SubjectRef != b.SubjectRef {
		return a.SubjectRef < b.SubjectRef
	}
	if a.Title != b.Title {
		return a.Title < b.Title
	}
	return a.DetailHash < b.DetailHash
}

// --- newMTLSClient ----------------------------------------------------------

func TestNewMTLSClientNeverSkipsVerify(t *testing.T) {
	certPEM, keyPEM := genKeyPair(t)
	doer, err := newMTLSClient(config{
		clientCertPEM:  certPEM,
		clientKeyPEM:   keyPEM,
		requestTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("newMTLSClient: %v", err)
	}
	hc, ok := doer.(*http.Client)
	if !ok {
		t.Fatalf("doer is %T, want *http.Client", doer)
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", hc.Transport)
	}
	tc := tr.TLSClientConfig
	if tc == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if tc.InsecureSkipVerify {
		t.Error("InsecureSkipVerify = true; the mTLS client must NEVER skip verification")
	}
	if tc.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = 0x%x, want >= TLS 1.2 (0x%x)", tc.MinVersion, tls.VersionTLS12)
	}
	if len(tc.Certificates) != 1 {
		t.Errorf("client Certificates = %d, want 1 (the supplied keypair)", len(tc.Certificates))
	}
}

func TestNewMTLSClientRejectsBadPEM(t *testing.T) {
	t.Run("bad_keypair", func(t *testing.T) {
		if _, err := newMTLSClient(config{
			clientCertPEM: "-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----",
			clientKeyPEM:  "-----BEGIN PRIVATE KEY-----\nnot base64\n-----END PRIVATE KEY-----",
		}); err == nil {
			t.Fatal("newMTLSClient accepted a malformed client keypair, want error")
		}
	})

	t.Run("bad_ca", func(t *testing.T) {
		if _, err := newMTLSClient(config{caCertPEM: "this is not a PEM certificate"}); err == nil {
			t.Fatal("newMTLSClient accepted a malformed CA bundle, want error")
		}
	})
}

// TestMTLSClientRefusesRedirects is a credential-exfil regression guard. The
// audited TAK Server is the untrusted party in this connector's threat model. Under
// Go's default redirect policy, a malicious server can 302 the version probe to a
// host it controls (any host with a publicly-trusted cert, reachable when ca_cert is
// empty) and Go will re-present the operator's mTLS client certificate there. The
// client must refuse redirects so the probe reports an honest "unreachable" instead
// of chasing the Location to a third party and leaking the certificate.
func TestMTLSClientRefusesRedirects(t *testing.T) {
	certPEM, keyPEM := genKeyPair(t)
	doer, err := newMTLSClient(config{clientCertPEM: certPEM, clientKeyPEM: keyPEM, requestTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("newMTLSClient: %v", err)
	}
	hc := doer.(*http.Client)

	// The policy itself: any redirect must be refused with ErrUseLastResponse so the
	// caller sees the 3xx, not the redirected response.
	if hc.CheckRedirect == nil {
		t.Fatal("CheckRedirect is nil; the default policy follows cross-host redirects and leaks the client cert")
	}
	if err := hc.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect returned %v, want http.ErrUseLastResponse", err)
	}

	// End-to-end: a server that redirects must NOT cause the client to follow. The
	// client returns the 3xx response itself, and versionProbe classifies that as a
	// non-2xx -> tak_api_unreachable.
	var attackerHit bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attackerHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", attacker.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirector.Close()

	req, _ := http.NewRequest(http.MethodGet, redirector.URL, nil)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302 (the redirect was followed instead of refused)", resp.StatusCode)
	}
	if attackerHit {
		t.Fatal("the client followed the redirect to the attacker host — a real request reached it")
	}
}
