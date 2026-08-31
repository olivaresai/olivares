// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/olivaresai/olivares/core/auth"
)

// keypairPEM generates a self-signed certificate and its private key (RSA or EC),
// PEM-encoded as tls.X509KeyPair (and thus the SP key loaders) expect.
func keypairPEM(t *testing.T, ec bool) (certPEM, keyPEM string) {
	t.Helper()
	var signer crypto.Signer
	if ec {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		signer = k
	} else {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		signer = k
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "sp.olivares.test"},
		NotBefore:    samlFixedTime.Add(-time.Hour),
		NotAfter:     samlFixedTime.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

func TestLoadSigningKeypairMethods(t *testing.T) {
	cEC, kEC := keypairPEM(t, true)
	if _, _, method, err := loadSigningKeypair(cEC, kEC); err != nil {
		t.Fatalf("EC signing keypair rejected: %v", err)
	} else if method != dsig.ECDSASHA256SignatureMethod {
		t.Errorf("EC signature method = %q, want ECDSA-SHA256", method)
	}
	cR, kR := keypairPEM(t, false)
	if _, _, method, err := loadSigningKeypair(cR, kR); err != nil {
		t.Fatalf("RSA signing keypair rejected: %v", err)
	} else if method != dsig.RSASHA256SignatureMethod {
		t.Errorf("RSA signature method = %q, want RSA-SHA256", method)
	}
}

// TestLoadEncryptionKeypairRejectsEC proves the honesty guard: an EC key cannot
// serve the encryption role (xmlenc has no ECDH-ES), so it is rejected with an
// explicit RSA reason rather than advertising a capability the SP cannot honor.
func TestLoadEncryptionKeypairRejectsEC(t *testing.T) {
	cEC, kEC := keypairPEM(t, true)
	_, _, err := loadEncryptionKeypair(cEC, kEC)
	if err == nil || !strings.Contains(err.Error(), "RSA") {
		t.Fatalf("an EC encryption key must be rejected with an RSA reason; got %v", err)
	}
	// RSA is accepted.
	cR, kR := keypairPEM(t, false)
	if _, _, err := loadEncryptionKeypair(cR, kR); err != nil {
		t.Fatalf("RSA encryption keypair rejected: %v", err)
	}
}

// TestSAMLSignedAuthnRequest proves the SP signs the AuthnRequest with the configured
// keypair AND that the HTTP-Redirect signature CRYPTOGRAPHICALLY verifies against the
// public key over the signed octets — for BOTH an EC and an RSA signing key (the
// regulated-bar capability an IdP actually relies on). Presence of a Signature param
// is not enough; a wrong-key or garbage signature must not pass.
func TestSAMLSignedAuthnRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ec      bool
		wantAlg string
	}{
		{"EC P-256", true, dsig.ECDSASHA256SignatureMethod},
		{"RSA 2048", false, dsig.RSASHA256SignatureMethod},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cPEM, kPEM := keypairPEM(t, tc.ec)
			signer, cert, method, err := loadSigningKeypair(cPEM, kPEM)
			if err != nil {
				t.Fatalf("loadSigningKeypair: %v", err)
			}
			sp := &samlProvider{
				beginSP: saml.ServiceProvider{
					EntityID: samlSPEntityID, AcsURL: mustURL(samlACSURL),
					Key: signer, Certificate: cert, SignatureMethod: method,
				},
				idpSSOURL: samlIDPSSOURL, replay: newReplayStore(),
			}

			redirect, err := sp.beginAuth(context.Background(), auth.AuthParams{RequestID: "_req1", State: "csrf-state"})
			if err != nil {
				t.Fatalf("beginAuth: %v", err)
			}
			u, err := url.Parse(redirect)
			if err != nil {
				t.Fatalf("parse redirect: %v", err)
			}
			if u.Query().Get("SAMLRequest") == "" {
				t.Error("redirect must carry a SAMLRequest")
			}
			if got := u.Query().Get("SigAlg"); got != tc.wantAlg {
				t.Errorf("SigAlg = %q, want %q", got, tc.wantAlg)
			}
			if u.Query().Get("RelayState") != "csrf-state" {
				t.Errorf("RelayState = %q, want the CSRF state", u.Query().Get("RelayState"))
			}

			// Per the SAML HTTP-Redirect binding the signed octets are the RAW query
			// string up to (not including) "&Signature="; the Signature param is the
			// base64 of the signature over SHA-256(octets).
			raw := u.RawQuery
			i := strings.Index(raw, "&Signature=")
			if i < 0 {
				t.Fatal("a signed AuthnRequest must carry a Signature query parameter")
			}
			signedOctets := raw[:i]
			sig, err := base64.StdEncoding.DecodeString(u.Query().Get("Signature"))
			if err != nil {
				t.Fatalf("decode signature: %v", err)
			}
			digest := sha256.Sum256([]byte(signedOctets))

			switch tc.ec {
			case true:
				pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
				if !ok {
					t.Fatalf("EC cert public key is %T", cert.PublicKey)
				}
				if !ecdsa.VerifyASN1(pub, digest[:], sig) {
					t.Error("the EC redirect signature must verify against the SP public key")
				}
			default:
				pub, ok := cert.PublicKey.(*rsa.PublicKey)
				if !ok {
					t.Fatalf("RSA cert public key is %T", cert.PublicKey)
				}
				if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
					t.Errorf("the RSA redirect signature must verify against the SP public key: %v", err)
				}
			}
		})
	}
}

// TestSAMLSPMetadataBothKeypairs proves the published SP metadata advertises both
// the EC signing KeyDescriptor and the RSA encryption KeyDescriptor (with the
// RSA-OAEP method) plus AuthnRequestsSigned, so an IdP can be onboarded by URL.
func TestSAMLSPMetadataBothKeypairs(t *testing.T) {
	cEnc, kEnc := keypairPEM(t, false) // RSA encryption
	cSig, kSig := keypairPEM(t, true)  // EC signing
	_, encCert, err := loadEncryptionKeypair(cEnc, kEnc)
	if err != nil {
		t.Fatalf("enc keypair: %v", err)
	}
	_, signCert, _, err := loadSigningKeypair(cSig, kSig)
	if err != nil {
		t.Fatalf("sign keypair: %v", err)
	}
	base := saml.ServiceProvider{EntityID: samlSPEntityID, AcsURL: mustURL(samlACSURL)}
	sp := &samlProvider{
		beginSP: base, validateSP: base, metaSP: base,
		signCert: signCert, encCert: encCert,
		idpSSOURL: samlIDPSSOURL, replay: newReplayStore(),
	}

	doc, err := sp.SPMetadata()
	if err != nil {
		t.Fatalf("SPMetadata: %v", err)
	}
	s := string(doc)
	for _, want := range []string{
		`AuthnRequestsSigned="true"`,
		samlSPEntityID, // the SP entity id
		samlACSURL,     // the ACS endpoint an IdP posts to
	} {
		if !strings.Contains(s, want) {
			t.Errorf("published metadata missing %q\n---\n%s", want, s)
		}
	}

	// Bind each certificate to its SPECIFIC use (a signing/encryption SWAP must fail).
	kds := parseKeyDescriptors(t, doc)
	wantSign := base64.StdEncoding.EncodeToString(signCert.Raw)
	wantEnc := base64.StdEncoding.EncodeToString(encCert.Raw)
	if got := normalizeB64(kds["signing"].cert); got != wantSign {
		t.Errorf("signing KeyDescriptor carries the wrong certificate (a sign/enc swap would slip past a substring check)")
	}
	if got := normalizeB64(kds["encryption"].cert); got != wantEnc {
		t.Errorf("encryption KeyDescriptor carries the wrong certificate")
	}
	// The RSA-OAEP method must ride with the ENCRYPTION descriptor specifically.
	if !strings.Contains(strings.Join(kds["encryption"].encMethods, " "), "rsa-oaep-mgf1p") {
		t.Errorf("encryption KeyDescriptor must advertise the rsa-oaep-mgf1p method, got %v", kds["encryption"].encMethods)
	}
	if len(kds["signing"].encMethods) != 0 {
		t.Errorf("signing KeyDescriptor must carry no EncryptionMethod, got %v", kds["signing"].encMethods)
	}

	// An empty <NameIDFormat/> must not be published.
	if strings.Contains(s, "<NameIDFormat></NameIDFormat>") || strings.Contains(s, "<NameIDFormat/>") {
		t.Error("metadata must not carry an empty NameIDFormat element")
	}
}

type parsedKD struct {
	cert       string
	encMethods []string
}

// parseKeyDescriptors decodes the published metadata's SP KeyDescriptors into a
// use -> {cert, encryptionMethods} map (namespace-prefix agnostic via local names).
func parseKeyDescriptors(t *testing.T, doc []byte) map[string]parsedKD {
	t.Helper()
	var md struct {
		KDs []struct {
			Use  string `xml:"use,attr"`
			Cert string `xml:"KeyInfo>X509Data>X509Certificate"`
			EncM []struct {
				Algorithm string `xml:"Algorithm,attr"`
			} `xml:"EncryptionMethod"`
		} `xml:"SPSSODescriptor>KeyDescriptor"`
	}
	if err := xml.Unmarshal(doc, &md); err != nil {
		t.Fatalf("unmarshal metadata: %v\n%s", err, doc)
	}
	out := map[string]parsedKD{}
	for _, kd := range md.KDs {
		methods := make([]string, 0, len(kd.EncM))
		for _, m := range kd.EncM {
			methods = append(methods, m.Algorithm)
		}
		out[kd.Use] = parsedKD{cert: kd.Cert, encMethods: methods}
	}
	return out
}

// normalizeB64 strips XML whitespace a marshaller may have folded into the base64.
func normalizeB64(s string) string {
	return strings.NewReplacer(" ", "", "\n", "", "\t", "", "\r", "").Replace(strings.TrimSpace(s))
}

// TestSAMLSPMetadataSigningOnly proves a signing-only SP (no encryption key)
// publishes a signing descriptor and NO encryption descriptor — honest about a
// capability it does not have.
func TestSAMLSPMetadataSigningOnly(t *testing.T) {
	cSig, kSig := keypairPEM(t, true)
	_, signCert, _, err := loadSigningKeypair(cSig, kSig)
	if err != nil {
		t.Fatalf("sign keypair: %v", err)
	}
	base := saml.ServiceProvider{EntityID: samlSPEntityID, AcsURL: mustURL(samlACSURL)}
	sp := &samlProvider{beginSP: base, validateSP: base, metaSP: base, signCert: signCert, replay: newReplayStore()}

	doc, err := sp.SPMetadata()
	if err != nil {
		t.Fatalf("SPMetadata: %v", err)
	}
	s := string(doc)
	if !strings.Contains(s, `use="signing"`) {
		t.Error("signing descriptor must be published")
	}
	if strings.Contains(s, `use="encryption"`) {
		t.Error("no encryption descriptor must be published when no encryption key is configured")
	}
}
