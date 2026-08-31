// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modelsign

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"
)

// --- test bundle builders ----------------------------------------------------

// omsStatement builds an OMS v1.0 in-toto statement over a single-file manifest.
func omsStatement(t *testing.T, fileName, fileData string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(fileData))
	digest := hex.EncodeToString(sum[:])
	st := statement{
		Type:          statementTypeInToto,
		Subject:       []subject{{Name: "my-model", Digest: map[string]string{"sha256": digest}}},
		PredicateType: PredicateTypeOMSv1,
	}
	pred := omsPredicate{
		Resources: []resourceDescriptor{{Name: fileName, Digest: digest, Algorithm: "sha256"}},
		Serialization: serialization{
			Method: "files", HashType: "sha256", IgnorePaths: []string{"model.sig"},
		},
	}
	pb, err := json.Marshal(pred)
	if err != nil {
		t.Fatal(err)
	}
	st.Predicate = pb
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// signPAE signs the DSSE pre-image of payload under priv (ed25519 or ecdsa).
func signPAE(t *testing.T, priv crypto.Signer, payload []byte) []byte {
	t.Helper()
	msg := pae(dssePayloadTypeInToto, payload)
	switch k := priv.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(k, msg)
	case *ecdsa.PrivateKey:
		sum := sha256.Sum256(msg)
		sig, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	default:
		t.Fatalf("unsupported signer %T", priv)
		return nil
	}
}

// bareKeyBundle assembles a bare-key OMS bundle and returns the bundle JSON + the
// PEM public key for the trust list.
func bareKeyBundle(t *testing.T, payload, sig []byte, pub crypto.PublicKey) ([]byte, string) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	b := bundle{
		MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: verificationMaterial{
			PublicKey: &publicKeyHint{Hint: "test-key"},
		},
		DSSEEnvelope: &dsseEnvelope{
			Payload:     base64.StdEncoding.EncodeToString(payload),
			PayloadType: dssePayloadTypeInToto,
			Signatures:  []dsseSignature{{Sig: base64.StdEncoding.EncodeToString(sig)}},
		},
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bj, pubPEM
}

// fulcioLikeCert issues a CA + a short-lived leaf carrying a SAN URI identity and
// the Fulcio v2 issuer extension, returning the leaf signer, the leaf DER and the
// CA PEM (the trusted root).
func fulcioLikeCert(t *testing.T, identity, issuer string, withTLog bool) (*ecdsa.PrivateKey, []byte, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-fulcio-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuerDER, err := asn1.Marshal(issuer)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(identity)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "sigstore-intermediate-leaf"},
		// Intentionally short-lived (Fulcio-style): already expired vs "now". The
		// verifier must anchor it at NotBefore, not "now".
		NotBefore: time.Now().Add(-30 * time.Minute), NotAfter: time.Now().Add(-20 * time.Minute),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs:            []*url.URL{u},
		ExtraExtensions: []pkix.Extension{{Id: oidFulcioIssuerV2, Value: issuerDER}},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return leafKey, leafDER, caPEM
}

func certBundle(t *testing.T, payload, sig, leafDER []byte, withTLog bool) []byte {
	t.Helper()
	vm := verificationMaterial{
		Certificate: &certBytes{RawBytes: base64.StdEncoding.EncodeToString(leafDER)},
	}
	if withTLog {
		vm.TLogEntries = []json.RawMessage{json.RawMessage(`{"logIndex":"1"}`)}
	}
	b := bundle{
		MediaType:            "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: vm,
		DSSEEnvelope: &dsseEnvelope{
			Payload:     base64.StdEncoding.EncodeToString(payload),
			PayloadType: dssePayloadTypeInToto,
			Signatures:  []dsseSignature{{Sig: base64.StdEncoding.EncodeToString(sig)}},
		},
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bj
}

// --- bare-key tests ----------------------------------------------------------

func TestVerifyBareKeyEd25519(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := omsStatement(t, "weights.bin", "the model weights")
	sig := signPAE(t, priv, payload)
	bj, pubPEM := bareKeyBundle(t, payload, sig, pub)

	// Valid signature, key on the trust list → verified bare-key.
	v, err := Verify(bj, TrustPolicy{Keys: []string{pubPEM}}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.Verified {
		t.Fatalf("want verified, got reason %q", v.Reason)
	}
	if v.Method != MethodBareKey {
		t.Errorf("method = %q, want %q", v.Method, MethodBareKey)
	}
	if v.PredicateType != PredicateTypeOMSv1 || v.ResourceCount != 1 {
		t.Errorf("predicate=%q resources=%d", v.PredicateType, v.ResourceCount)
	}
	if v.ArtifactVerified {
		t.Errorf("artifact must be unverified with no resolved digests")
	}
}

func TestVerifyBareKeyDenyClosed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := omsStatement(t, "weights.bin", "the model weights")
	sig := signPAE(t, priv, payload)
	bj, pubPEM := bareKeyBundle(t, payload, sig, pub)

	// Empty trust policy → deny-closed, not verified.
	if v, _ := Verify(bj, TrustPolicy{}, nil); v.Verified {
		t.Error("empty policy must not verify")
	}

	// A DIFFERENT trusted key → signature does not verify under it.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	oder, _ := x509.MarshalPKIXPublicKey(otherPub)
	otherPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: oder}))
	if v, _ := Verify(bj, TrustPolicy{Keys: []string{otherPEM}}, nil); v.Verified {
		t.Error("wrong trusted key must not verify")
	}

	// Tampered payload (same sig) → signature mismatch, not verified.
	tampered := omsStatement(t, "weights.bin", "MALICIOUS weights")
	bjT, _ := bareKeyBundle(t, tampered, sig, pub)
	if v, _ := Verify(bjT, TrustPolicy{Keys: []string{pubPEM}}, nil); v.Verified {
		t.Error("tampered payload must not verify")
	}
}

// --- keyless (Fulcio-like) tests --------------------------------------------

func TestVerifyKeylessCert(t *testing.T) {
	const identity = "https://github.com/acme/models/.github/workflows/sign.yml@refs/heads/main"
	const issuer = "https://token.actions.githubusercontent.com"
	leafKey, leafDER, caPEM := fulcioLikeCert(t, identity, issuer, false)
	payload := omsStatement(t, "weights.bin", "the model weights")
	sig := signPAE(t, leafKey, payload)
	bj := certBundle(t, payload, sig, leafDER, true)

	policy := TrustPolicy{
		Roots:             []string{caPEM},
		AllowedIdentities: []string{`^https://github\.com/acme/models/`},
		AllowedIssuers:    []string{issuer},
	}
	v, err := Verify(bj, policy, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.Verified {
		t.Fatalf("want verified, got reason %q", v.Reason)
	}
	if v.Method != MethodSigstoreKeyless {
		t.Errorf("method = %q, want %q", v.Method, MethodSigstoreKeyless)
	}
	if v.SignerIdentity != identity {
		t.Errorf("identity = %q, want %q", v.SignerIdentity, identity)
	}
	if v.SignerIssuer != issuer {
		t.Errorf("issuer = %q, want %q", v.SignerIssuer, issuer)
	}
	// Option B: the verdict records the anchoring root, and it round-trips through
	// AnchorStillTrusted — what Verify pins is exactly what the re-check accepts.
	if len(v.SignerRoots) != 1 {
		t.Fatalf("SignerRoots = %v, want exactly the one anchoring root", v.SignerRoots)
	}
	rec := RecordedAnchor{Identity: v.SignerIdentity, Issuer: v.SignerIssuer, Roots: v.SignerRoots, Method: v.Method}
	if !AnchorStillTrusted(policy, rec) {
		t.Errorf("the recorded keyless anchor must still be trusted under the admitting policy")
	}
	replaced := policy
	replaced.Roots = []string{func() string { p, _ := caCertPEM(t); return p }()} // a DIFFERENT root
	if AnchorStillTrusted(replaced, rec) {
		t.Errorf("the recorded keyless anchor must be denied once its root is replaced (Option B)")
	}
	// The honest tlog seam: present but never natively verified.
	if !v.TransparencyLogPresent || v.TransparencyLogVerified {
		t.Errorf("tlog present=%v verified=%v; want present=true verified=false", v.TransparencyLogPresent, v.TransparencyLogVerified)
	}
}

func TestVerifyKeylessDenyClosed(t *testing.T) {
	const identity = "https://github.com/acme/models/sign.yml@main"
	const issuer = "https://token.actions.githubusercontent.com"
	leafKey, leafDER, caPEM := fulcioLikeCert(t, identity, issuer, false)
	payload := omsStatement(t, "weights.bin", "the model weights")
	sig := signPAE(t, leafKey, payload)
	bj := certBundle(t, payload, sig, leafDER, false)

	cases := []struct {
		name   string
		policy TrustPolicy
	}{
		{"no roots", TrustPolicy{AllowedIdentities: []string{".*"}, AllowedIssuers: []string{issuer}}},
		{"untrusted root", TrustPolicy{Roots: []string{otherCAPEM(t)}, AllowedIdentities: []string{".*"}, AllowedIssuers: []string{issuer}}},
		{"wrong identity", TrustPolicy{Roots: []string{caPEM}, AllowedIdentities: []string{`^https://evil\.example/`}, AllowedIssuers: []string{issuer}}},
		{"no identities", TrustPolicy{Roots: []string{caPEM}, AllowedIssuers: []string{issuer}}},
		{"wrong issuer", TrustPolicy{Roots: []string{caPEM}, AllowedIdentities: []string{".*"}, AllowedIssuers: []string{"https://accounts.google.com"}}},
		{"no issuers", TrustPolicy{Roots: []string{caPEM}, AllowedIdentities: []string{".*"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Verify(bj, tc.policy, nil)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if v.Verified {
				t.Fatalf("%s: must be deny-closed (not verified)", tc.name)
			}
			if v.Reason == "" {
				t.Errorf("%s: a denial must carry a reason", tc.name)
			}
		})
	}
}

func otherCAPEM(t *testing.T) string {
	t.Helper()
	_, der, ca := fulcioLikeCert(t, "https://x/y", "https://z", false)
	_ = der
	return ca
}

// --- artifact re-hash coverage ----------------------------------------------

func TestArtifactReHash(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	const fileData = "the model weights"
	payload := omsStatement(t, "weights.bin", fileData)
	sig := signPAE(t, priv, payload)
	bj, pubPEM := bareKeyBundle(t, payload, sig, pub)
	policy := TrustPolicy{Keys: []string{pubPEM}}
	sum := sha256.Sum256([]byte(fileData))
	good := hex.EncodeToString(sum[:])

	// Matching resolved digest → artifact verified.
	v, _ := Verify(bj, policy, map[string]string{"weights.bin": good})
	if !v.Verified || !v.ArtifactVerified {
		t.Fatalf("verified=%v artifact=%v reason=%q coverage=%q", v.Verified, v.ArtifactVerified, v.Reason, v.ArtifactCoverage)
	}

	// Mismatched digest → signature still verified, artifact NOT verified.
	v, _ = Verify(bj, policy, map[string]string{"weights.bin": "deadbeef"})
	if !v.Verified || v.ArtifactVerified {
		t.Errorf("mismatch: verified=%v artifact=%v (want true/false)", v.Verified, v.ArtifactVerified)
	}

	// Missing file digest → artifact NOT verified (covered-set gap).
	v, _ = Verify(bj, policy, map[string]string{"other.bin": good})
	if v.ArtifactVerified {
		t.Errorf("a manifest file with no resolved digest must not pass artifact verification")
	}
}

// --- malformed / wrong-predicate --------------------------------------------

func TestWrongPredicateType(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// A well-formed in-toto statement but NOT an OMS predicate.
	st := statement{Type: statementTypeInToto, PredicateType: "https://slsa.dev/provenance/v1", Predicate: json.RawMessage(`{}`)}
	payload, _ := json.Marshal(st)
	sig := signPAE(t, priv, payload)
	bj, pubPEM := bareKeyBundle(t, payload, sig, pub)
	v, err := Verify(bj, TrustPolicy{Keys: []string{pubPEM}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verified {
		t.Error("a non-OMS predicate must not verify as a model signature")
	}
}

func TestMalformedBundle(t *testing.T) {
	if _, err := Verify([]byte(`not json`), TrustPolicy{Keys: []string{"x"}}, nil); !errors.Is(err, ErrMalformedBundle) {
		t.Errorf("want ErrMalformedBundle, got %v", err)
	}
	if _, err := Verify([]byte(`{"mediaType":"x"}`), TrustPolicy{Keys: []string{"x"}}, nil); !errors.Is(err, ErrMalformedBundle) {
		t.Errorf("missing envelope: want ErrMalformedBundle, got %v", err)
	}
}

// pkiLeafNoSAN issues a CA + a leaf with NO SAN identity and NO Fulcio issuer
// extension (a plain PKI signing cert), returning the leaf signer, its DER and the
// trusted CA PEM. Used to prove the keyless→PKI downgrade is closed.
func pkiLeafNoSAN(t *testing.T) (*ecdsa.PrivateKey, []byte, string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "pki-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "attacker"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		// Deliberately NO URIs and NO issuer extension.
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return leafKey, leafDER, caPEM
}

// TestVerifyPKIAndNoDowngrade proves (a) an explicit PKI posture (no identity/issuer
// pins) verifies a chain-anchored cert as certificate-pki, and (b) a SAN-less cert
// CANNOT silently downgrade to PKI to bypass configured keyless identity/issuer pins.
func TestVerifyPKIAndNoDowngrade(t *testing.T) {
	leafKey, leafDER, caPEM := pkiLeafNoSAN(t)
	payload := omsStatement(t, "weights.bin", "w")
	sig := signPAE(t, leafKey, payload)
	bj := certBundle(t, payload, sig, leafDER, false)

	// (a) No pins → explicit PKI-only posture → verified as certificate-pki.
	v, err := Verify(bj, TrustPolicy{Roots: []string{caPEM}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Verified || v.Method != MethodCertificatePKI {
		t.Fatalf("PKI posture: verified=%v method=%q reason=%q", v.Verified, v.Method, v.Reason)
	}

	// (b) With identity/issuer pins configured, the SAN-less cert must be DENIED — it
	// must not downgrade to unchecked PKI and escape the pin (the HIGH bypass, closed).
	v, err = Verify(bj, TrustPolicy{Roots: []string{caPEM}, AllowedIdentities: []string{".*"}, AllowedIssuers: []string{"https://issuer"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verified {
		t.Fatal("a SAN-less cert with pinned identity/issuer must be deny-closed (no keyless→PKI downgrade)")
	}
	if v.Reason == "" {
		t.Error("the downgrade denial must carry a reason")
	}
}

func TestECDSABareKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := omsStatement(t, "weights.bin", "ecdsa model")
	sig := signPAE(t, priv, payload)
	bj, pubPEM := bareKeyBundle(t, payload, sig, &priv.PublicKey)
	v, err := Verify(bj, TrustPolicy{Keys: []string{pubPEM}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Verified {
		t.Fatalf("ecdsa bare key: want verified, got %q", v.Reason)
	}
}
