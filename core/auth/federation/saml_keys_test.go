// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"math/big"
	"strings"
	"testing"
	"time"

	dsig "github.com/russellhaering/goxmldsig"
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
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
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
