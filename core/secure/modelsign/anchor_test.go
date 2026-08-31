// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modelsign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// anchorTestPEM returns a fresh PEM-encoded PKIX PUBLIC KEY (a bare-key trust-list entry).
func anchorTestPEM(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// caCertPEM returns a fresh self-signed CA CERTIFICATE PEM and its "root:<fp>" marker (the
// exact value anchorCertificate records in Verdict.SignerRoots for a leaf anchored to it).
func caCertPEM(t *testing.T) (pemStr, marker string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "modelsign-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), rootMarker(cert)
}

const (
	testKeylessID  = "https://ci.acme.io/build/42"
	testKeylessPat = `^https://ci\.acme\.io/.*$`
	testKeylessIss = "https://accounts.acme.io"
)

// TestAnchorStillTrusted pins the re-validation of a stored verdict's anchor against a
// CURRENT policy (used by the runtime deployment gate and the catalog admission gates after
// a policy rotation). With Option B the certificate branch pins the EXACT anchoring root(s).
func TestAnchorStillTrusted(t *testing.T) {
	k1, k2 := anchorTestPEM(t), anchorTestPEM(t)
	fp1 := "key:" + keyFingerprint(k1)

	// --- bare-key: pins the exact key fingerprint (unaffected by roots) ---
	if !AnchorStillTrusted(TrustPolicy{Keys: []string{k1, k2}}, RecordedAnchor{Identity: fp1, Method: MethodBareKey}) {
		t.Errorf("a key still in the trusted set must anchor")
	}
	if AnchorStillTrusted(TrustPolicy{Keys: []string{k2}}, RecordedAnchor{Identity: fp1, Method: MethodBareKey}) {
		t.Errorf("a key rotated OUT of the policy must not anchor")
	}
	if AnchorStillTrusted(TrustPolicy{}, RecordedAnchor{Identity: fp1, Method: MethodBareKey}) {
		t.Errorf("an empty policy must anchor nothing")
	}
	if AnchorStillTrusted(TrustPolicy{Keys: []string{k1}}, RecordedAnchor{Identity: "no-key-prefix", Method: MethodBareKey}) {
		t.Errorf("a bare-key identity without the key: prefix must not anchor (deny-closed)")
	}

	// --- keyless: exact anchoring root + identity + issuer must all still hold ---
	rootPEM, rootMk := caCertPEM(t)
	otherPEM, _ := caCertPEM(t)
	keyless := func(p TrustPolicy, roots []string) bool {
		return AnchorStillTrusted(p, RecordedAnchor{Identity: testKeylessID, Issuer: testKeylessIss, Roots: roots, Method: MethodSigstoreKeyless})
	}
	okPolicy := TrustPolicy{Roots: []string{rootPEM}, AllowedIdentities: []string{testKeylessPat}, AllowedIssuers: []string{testKeylessIss}}
	if !keyless(okPolicy, []string{rootMk}) {
		t.Errorf("a still-allowed keyless identity+issuer with its anchoring root present must anchor")
	}
	idDropped := TrustPolicy{Roots: []string{rootPEM}, AllowedIdentities: []string{`^https://other\.io/.*$`}, AllowedIssuers: []string{testKeylessIss}}
	if keyless(idDropped, []string{rootMk}) {
		t.Errorf("an identity dropped from the allow-list must not anchor")
	}
	issDropped := TrustPolicy{Roots: []string{rootPEM}, AllowedIdentities: []string{testKeylessPat}, AllowedIssuers: []string{"https://other.io"}}
	if keyless(issDropped, []string{rootMk}) {
		t.Errorf("an issuer dropped from the allow-list must not anchor")
	}
	if keyless(TrustPolicy{AllowedIdentities: []string{`.*`}, AllowedIssuers: []string{testKeylessIss}}, []string{rootMk}) {
		t.Errorf("a certificate signer cannot anchor without a trusted root")
	}
	// Option B: the anchoring root REPLACED (policy now trusts a different root) must NOT anchor,
	// even though identity+issuer still match and len(Roots) stays > 0. This is the residual B closes.
	if keyless(TrustPolicy{Roots: []string{otherPEM}, AllowedIdentities: []string{testKeylessPat}, AllowedIssuers: []string{testKeylessIss}}, []string{rootMk}) {
		t.Errorf("a keyless verdict whose anchoring root was replaced must not anchor (Option B)")
	}
	// A legacy keyless verdict that recorded NO root is deny-closed (never grandfathered).
	if keyless(okPolicy, nil) {
		t.Errorf("a keyless verdict with no recorded anchoring root must be deny-closed (re-admit to pin it)")
	}

	// --- multi-root recorded (cross-signed leaf): retained iff ANY recorded root remains ---
	rootA, mkA := caCertPEM(t)
	rootB, mkB := caCertPEM(t)
	multi := func(policyRoots []string, recorded []string) bool {
		return AnchorStillTrusted(TrustPolicy{Roots: policyRoots}, RecordedAnchor{Identity: "CN=x", Roots: recorded, Method: MethodCertificatePKI})
	}
	if !multi([]string{rootA}, []string{mkA, mkB}) {
		t.Errorf("a verdict anchored to A and B must survive while A is retained")
	}
	if !multi([]string{rootB}, []string{mkA, mkB}) {
		t.Errorf("a verdict anchored to A and B must survive while B is retained")
	}
	if multi([]string{otherPEM}, []string{mkA, mkB}) {
		t.Errorf("a verdict must not survive once ALL its recorded anchoring roots are gone")
	}

	// --- PEM BUNDLE: a single Roots[] entry may concatenate several certs; the anchoring root
	// may be the 2nd+. rootPresent must decode EVERY block, not just the first. ---
	bundle := otherPEM + "\n" + rootB // anchoring root (B) is the SECOND cert in the bundle entry
	if !AnchorStillTrusted(TrustPolicy{Roots: []string{bundle}}, RecordedAnchor{Identity: "CN=x", Roots: []string{mkB}, Method: MethodCertificatePKI}) {
		t.Errorf("the anchoring root as a non-first cert in a bundled Roots[] entry must be matched")
	}

	// --- certificate-PKI (no identity/issuer pins): trust rests on the exact root ---
	if !AnchorStillTrusted(TrustPolicy{Roots: []string{rootA}}, RecordedAnchor{Identity: "CN=build.acme.io", Roots: []string{mkA}, Method: MethodCertificatePKI}) {
		t.Errorf("a PKI signer must anchor while its exact trusted root is present")
	}
	if AnchorStillTrusted(TrustPolicy{}, RecordedAnchor{Identity: "CN=build.acme.io", Roots: []string{mkA}, Method: MethodCertificatePKI}) {
		t.Errorf("a PKI signer must NOT anchor once every trusted root is removed")
	}
	// Option B closes the former residual: REPLACING the sole root (drop A, add B in one edit) is
	// now caught — the recorded anchoring root A is gone, so the stale verdict no longer anchors.
	if AnchorStillTrusted(TrustPolicy{Roots: []string{rootB}}, RecordedAnchor{Identity: "CN=build.acme.io", Roots: []string{mkA}, Method: MethodCertificatePKI}) {
		t.Errorf("Option B: single-root replacement must no longer anchor a stale PKI verdict")
	}

	// --- unknown method is deny-closed ---
	if AnchorStillTrusted(TrustPolicy{Keys: []string{k1}}, RecordedAnchor{Identity: fp1, Method: "made-up-method"}) {
		t.Errorf("an unknown method must not anchor")
	}
}
