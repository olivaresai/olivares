// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package teams

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

const (
	testAppID      = "11111111-2222-3333-4444-555555555555"
	testServiceURL = "https://smba.trafficmanager.net/amer/"
	testKID        = "test-key-1"
)

// jwksDoer serves the OpenID metadata + JWKS from an in-memory key set, recording fetches.
type jwksDoer struct {
	metadataURL string
	jwksURL     string
	jwks        string
	fetches     int
}

func (d *jwksDoer) Do(req *http.Request) (*http.Response, error) {
	d.fetches++
	switch req.URL.String() {
	case d.metadataURL:
		return jresp(200, `{"issuer":"`+DefaultIssuer+`","jwks_uri":"`+d.jwksURL+`","id_token_signing_alg_values_supported":["RS256"]}`), nil
	case d.jwksURL:
		return jresp(200, d.jwks), nil
	default:
		return jresp(404, `{"error":"not found"}`), nil
	}
}

func jresp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// fullClaims carries the standard JWT claims plus the Bot Framework serviceUrl claim.
type fullClaims struct {
	jwt.Claims
	ServiceURL string `json:"serviceurl,omitempty"`
}

type harness struct {
	v      *Verifier
	priv   *rsa.PrivateKey
	now    time.Time
	signer jose.Signer
}

func newHarness(t *testing.T, cfg VerifierConfig) *harness {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: &priv.PublicKey, KeyID: testKID, Algorithm: "RS256", Use: "sig"}
	setBytes, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	if err != nil {
		t.Fatal(err)
	}
	doer := &jwksDoer{
		metadataURL: DefaultMetadataURL,
		jwksURL:     "https://login.botframework.com/v1/.well-known/keys",
		jwks:        string(setBytes),
	}
	if cfg.AppID == "" {
		cfg.AppID = testAppID
	}
	cfg.Doer = doer
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: priv, KeyID: testKID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{v: v, priv: priv, now: now, signer: signer}
}

func (h *harness) sign(t *testing.T, c fullClaims) string {
	t.Helper()
	tok, err := jwt.Signed(h.signer).Claims(c).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

// validClaims is a well-formed Connector→Bot token for the test bot.
func (h *harness) validClaims() fullClaims {
	return fullClaims{
		Claims: jwt.Claims{
			Issuer:    DefaultIssuer,
			Audience:  jwt.Audience{testAppID},
			Expiry:    jwt.NewNumericDate(h.now.Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(h.now.Add(-time.Minute)),
		},
		ServiceURL: testServiceURL,
	}
}

func bodyWith(serviceURL string) []byte {
	return []byte(`{"type":"invoke","name":"adaptiveCard/action","serviceUrl":"` + serviceURL + `"}`)
}

func TestVerifyValid(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	tok := h.sign(t, h.validClaims())
	cl, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if cl.Issuer != DefaultIssuer || cl.ServiceURL != testServiceURL {
		t.Errorf("claims wrong: %+v", cl)
	}
}

func TestVerifyWrongAudienceRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	c := h.validClaims()
	c.Audience = jwt.Audience{"some-other-bot"}
	tok := h.sign(t, c)
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("a token minted for another bot (wrong aud) must be rejected")
	}
}

func TestVerifyWrongIssuerRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	c := h.validClaims()
	c.Issuer = "https://evil.example.com"
	tok := h.sign(t, c)
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("an untrusted issuer must be rejected")
	}
}

func TestVerifyExpiredRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	c := h.validClaims()
	c.Expiry = jwt.NewNumericDate(h.now.Add(-time.Hour))
	tok := h.sign(t, c)
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("an expired token must be rejected")
	}
}

func TestVerifyMissingExpRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	c := h.validClaims()
	c.Expiry = nil // go-jose does not require exp; the verifier must
	tok := h.sign(t, c)
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("a token with no exp must be rejected")
	}
}

func TestVerifyServiceURLMismatchRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	tok := h.sign(t, h.validClaims())
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith("https://attacker.example.com/"), h.now); err == nil {
		t.Fatal("a serviceUrl-claim that does not match the activity must be rejected")
	}
}

func TestVerifyServiceURLOptional(t *testing.T) {
	off := false
	h := newHarness(t, VerifierConfig{RequireServiceURL: &off})
	c := h.validClaims()
	c.ServiceURL = "" // no serviceUrl claim, and require=false → not checked
	tok := h.sign(t, c)
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, []byte(`{"type":"invoke"}`), h.now); err != nil {
		t.Fatalf("with RequireServiceURL=false the binding must not be enforced: %v", err)
	}
}

func TestVerifyHS256Rejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	// An attacker-forged HMAC (HS256) token must be rejected at parse (alg not allowlisted).
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("guessable-secret-padded-to-32+bytes!!")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(hs).Claims(h.validClaims()).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("an HS256 token must be rejected (algorithm-confusion defense)")
	}
}

func TestVerifyBadSignatureRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	// Sign with a DIFFERENT key but the trusted kid → the JWKS key cannot verify it.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: other, KeyID: testKID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	tok, _ := jwt.Signed(signer).Claims(h.validClaims()).Serialize()
	if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("a token signed by a key not in the JWKS must fail signature verification")
	}
}

func TestVerifyNoBearerRejected(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	if _, err := h.v.Verify(context.Background(), "", bodyWith(testServiceURL), h.now); err == nil {
		t.Fatal("a missing Authorization header must be rejected")
	}
}

func TestNewVerifierRequiresAppID(t *testing.T) {
	if _, err := NewVerifier(VerifierConfig{}); err == nil {
		t.Fatal("a verifier with no app_id (audience) must not be built")
	}
}

func TestNewVerifierRejectsCleartextMetadata(t *testing.T) {
	if _, err := NewVerifier(VerifierConfig{AppID: testAppID, MetadataURL: "http://login.botframework.com/v1/.well-known/openidconfiguration"}); err == nil {
		t.Fatal("a cleartext http metadata URL (root of trust) must be rejected")
	}
	// Loopback http is allowed for the Bot Framework emulator.
	if _, err := NewVerifier(VerifierConfig{AppID: testAppID, MetadataURL: "http://localhost:9000/v2.0/.well-known/openid-configuration"}); err != nil {
		t.Fatalf("loopback http metadata must be allowed (emulator): %v", err)
	}
}

// cleartextJWKSDoer serves a metadata document whose jwks_uri is cleartext http — a MITM
// pointing the KEY fetch at a forged set. The verifier must refuse it (fail closed).
type cleartextJWKSDoer struct{}

func (cleartextJWKSDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.String() == DefaultMetadataURL {
		return jresp(200, `{"issuer":"`+DefaultIssuer+`","jwks_uri":"http://attacker.example.com/keys"}`), nil
	}
	return jresp(404, `{}`), nil
}

func TestRefreshRejectsCleartextJWKSURI(t *testing.T) {
	v, err := NewVerifier(VerifierConfig{AppID: testAppID, Doer: cleartextJWKSDoer{}})
	if err != nil {
		t.Fatal(err)
	}
	// Sign a syntactically valid token (any RS key) so Verify reaches key resolution.
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: priv, KeyID: testKID}}, (&jose.SignerOptions{}).WithType("JWT"))
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	tok, _ := jwt.Signed(signer).Claims(fullClaims{Claims: jwt.Claims{
		Issuer: DefaultIssuer, Audience: jwt.Audience{testAppID}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
	}, ServiceURL: testServiceURL}).Serialize()
	if _, err := v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), now); err == nil {
		t.Fatal("a metadata document pointing jwks_uri at cleartext http must fail closed (forged-key defense)")
	}
}

func TestJWKSCachedAcrossVerifies(t *testing.T) {
	h := newHarness(t, VerifierConfig{})
	doer := h.v.doer.(*jwksDoer)
	for i := 0; i < 3; i++ {
		tok := h.sign(t, h.validClaims())
		if _, err := h.v.Verify(context.Background(), "Bearer "+tok, bodyWith(testServiceURL), h.now); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	// Metadata + JWKS fetched exactly once (2 GETs), then served from cache.
	if doer.fetches != 2 {
		t.Fatalf("expected 2 fetches (metadata+jwks) cached across 3 verifies, got %d", doer.fetches)
	}
}
