// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmssign_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/audit/kmssign"
)

// fakeKMS is an in-memory cloud KMS: a local P-256 key that answers Sign and
// GetPublicKey in each provider's wire format, so the backends are exercised end to
// end without any network. It also records that the request carried the expected
// auth header.
type fakeKMS struct {
	t        *testing.T
	priv     *ecdsa.PrivateKey
	provider string // "aws" | "gcp" | "azure"
	sawAuth  bool
}

func (f *fakeKMS) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	switch f.provider {
	case "aws":
		if strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			f.sawAuth = true
		}
		target := req.Header.Get("X-Amz-Target")
		switch target {
		case "TrentService.Sign":
			var in struct{ Message string }
			_ = json.Unmarshal(body, &in)
			digest, _ := base64.StdEncoding.DecodeString(in.Message)
			der, _ := ecdsa.SignASN1(rand.Reader, f.priv, digest)
			return jsonResp(map[string]string{"Signature": base64.StdEncoding.EncodeToString(der)}), nil
		case "TrentService.GetPublicKey":
			der, _ := x509.MarshalPKIXPublicKey(&f.priv.PublicKey)
			return jsonResp(map[string]string{"PublicKey": base64.StdEncoding.EncodeToString(der)}), nil
		}
	case "gcp":
		if strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
			f.sawAuth = true
		}
		if strings.HasSuffix(req.URL.Path, "/publicKey") {
			der, _ := x509.MarshalPKIXPublicKey(&f.priv.PublicKey)
			pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
			return jsonResp(map[string]string{"pem": string(pemBytes)}), nil
		}
		var in struct {
			Digest map[string]string `json:"digest"`
		}
		_ = json.Unmarshal(body, &in)
		digest, _ := base64.StdEncoding.DecodeString(in.Digest["sha256"])
		der, _ := ecdsa.SignASN1(rand.Reader, f.priv, digest)
		return jsonResp(map[string]string{"signature": base64.StdEncoding.EncodeToString(der)}), nil
	case "azure":
		if strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
			f.sawAuth = true
		}
		if req.Method == http.MethodGet {
			// Extract the JWK x/y coordinates via ECDH (the raw 0x04||x||y point),
			// avoiding the deprecated ecdsa.PublicKey.X/.Y fields.
			ep, err := f.priv.PublicKey.ECDH()
			if err != nil {
				f.t.Fatal(err)
			}
			pt := ep.Bytes() // 65 bytes: 0x04 || x(32) || y(32)
			return jsonResp(map[string]any{"key": map[string]string{
				"kty": "EC", "crv": "P-256",
				"x": base64.RawURLEncoding.EncodeToString(pt[1:33]),
				"y": base64.RawURLEncoding.EncodeToString(pt[33:65]),
			}}), nil
		}
		var in struct{ Value string }
		_ = json.Unmarshal(body, &in)
		digest, _ := base64.RawURLEncoding.DecodeString(in.Value)
		r, s, _ := ecdsa.Sign(rand.Reader, f.priv, digest)
		raw := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		return jsonResp(map[string]string{"value": base64.RawURLEncoding.EncodeToString(raw)}), nil
	}
	f.t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
	return nil, nil
}

func jsonResp(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}
}

// verifyOffBox reproduces the off-box check the auditor does: parse the pinned DER
// public key and ecdsa.VerifyASN1 the signature over SHA-256(preimage).
func verifyOffBox(t *testing.T, der, sig, preimage []byte) {
	t.Helper()
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("parse pub: %v", err)
	}
	ek, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("not ecdsa: %T", pub)
	}
	d := sha256.Sum256(preimage)
	if !ecdsa.VerifyASN1(ek, d[:], sig) {
		t.Fatal("off-box verification failed")
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAWSBackend(t *testing.T) {
	ctx := context.Background()
	fk := &fakeKMS{t: t, priv: newKey(t), provider: "aws"}
	a, err := kmssign.NewAWS(kmssign.AWSConfig{
		Region: "eu-west-1", KeyID: "arn:aws:kms:eu-west-1:0:key/ledger",
		Creds: kmssign.AWSCreds{AccessKeyID: "AKIA", SecretAccessKey: "secret"},
		Doer:  fk, Endpoint: "https://kms.eu-west-1.amazonaws.com/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Algorithm() != audit.AlgECDSAP256SHA256 {
		t.Fatalf("alg = %q", a.Algorithm())
	}
	preimage := []byte("olivares.audit.checkpoint.v1|tenant|7|hash")
	sig, err := a.SignCheckpoint(ctx, preimage)
	if err != nil {
		t.Fatal(err)
	}
	der, err := a.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	verifyOffBox(t, der, sig, preimage)
	if !fk.sawAuth {
		t.Error("AWS request was not SigV4-signed")
	}
}

func TestGCPBackend(t *testing.T) {
	ctx := context.Background()
	fk := &fakeKMS{t: t, priv: newKey(t), provider: "gcp"}
	g, err := kmssign.NewGCP(kmssign.GCPConfig{
		KeyVersionName: "projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1",
		Token:          kmssign.StaticToken("tok"), Doer: fk,
	})
	if err != nil {
		t.Fatal(err)
	}
	preimage := []byte("gcp-preimage")
	sig, err := g.SignCheckpoint(ctx, preimage)
	if err != nil {
		t.Fatal(err)
	}
	der, err := g.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	verifyOffBox(t, der, sig, preimage)
	if !fk.sawAuth {
		t.Error("GCP request had no bearer token")
	}
}

// TestAzureBackend exercises the r||s -> DER signature conversion and JWK -> DER
// public-key conversion that make Azure uniform with AWS/GCP for the verifier.
func TestAzureBackend(t *testing.T) {
	ctx := context.Background()
	fk := &fakeKMS{t: t, priv: newKey(t), provider: "azure"}
	a, err := kmssign.NewAzure(kmssign.AzureConfig{
		VaultURL: "https://v.vault.azure.net", KeyName: "ledger", KeyVersion: "abc",
		Token: kmssign.StaticToken("tok"), Doer: fk,
	})
	if err != nil {
		t.Fatal(err)
	}
	preimage := []byte("azure-preimage")
	sig, err := a.SignCheckpoint(ctx, preimage)
	if err != nil {
		t.Fatal(err)
	}
	// The returned signature must be ASN.1-DER (not raw r||s), so the uniform
	// off-box verifier accepts it.
	var ecdsaSig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(sig, &ecdsaSig); err != nil {
		t.Fatalf("Azure signature is not ASN.1-DER: %v", err)
	}
	der, err := a.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	verifyOffBox(t, der, sig, preimage)
	if !fk.sawAuth {
		t.Error("Azure request had no bearer token")
	}
}

func TestRejectsBadConfig(t *testing.T) {
	if _, err := kmssign.NewAWS(kmssign.AWSConfig{KeyID: "k"}); err == nil {
		t.Error("expected error for missing region")
	}
	if _, err := kmssign.NewGCP(kmssign.GCPConfig{KeyVersionName: "x"}); err == nil {
		t.Error("expected error for missing token source")
	}
	if _, err := kmssign.NewAWS(kmssign.AWSConfig{Region: "r", KeyID: "k", SigningAlgorithm: "RSASSA_PSS_SHA_256"}); err == nil {
		t.Error("expected error for unsupported RSA algorithm (documented seam)")
	}
}
