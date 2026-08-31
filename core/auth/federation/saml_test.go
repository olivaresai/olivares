// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/olivaresai/olivares/core/auth"
)

const (
	samlSPEntityID = "https://sp.olivares.test/saml/metadata"
	samlACSURL     = "https://sp.olivares.test/saml/acs"
	samlIDPSSOURL  = "https://idp.olivares.test/saml/sso"
	samlIDPMetaURL = "https://idp.olivares.test/saml/metadata"
)

// samlFixedTime is the clock both minting and validation use, so the assertion's
// time window is deterministic.
var samlFixedTime = time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

// samlHarness is an in-process IdP that mints REAL signed SAML responses, plus the
// SP-side samlProvider under test (constructed directly, bypassing FromEnv's network
// metadata fetch).
type samlHarness struct {
	idp *saml.IdentityProvider
	sp  *samlProvider
}

type mockSPP struct{ meta *saml.EntityDescriptor }

func (m *mockSPP) GetServiceProvider(_ *http.Request, id string) (*saml.EntityDescriptor, error) {
	if id == samlSPEntityID {
		return m.meta, nil
	}
	return nil, fmt.Errorf("unknown service provider %q", id)
}

func newSAMLHarness(t *testing.T) *samlHarness { return buildSAMLHarness(t, false) }

// buildSAMLHarness builds the in-process IdP + the SP under test. When encrypt is
// true the SP publishes an RSA encryption certificate, so the IdP encrypts the
// assertion to it (RSA-OAEP) and the SP decrypts it with the matching key — the
// Encrypted-assertion path, end to end.
func buildSAMLHarness(t *testing.T, encrypt bool) *samlHarness {
	t.Helper()
	// Pin crewjam's clock (SAML conditions) and dsig clock (signature validity) to a
	// fixed instant; restore on cleanup so tests don't leak global state.
	prevNow, prevClock := saml.TimeNow, saml.Clock
	saml.TimeNow = func() time.Time { return samlFixedTime }
	saml.Clock = dsig.NewFakeClockAt(samlFixedTime)
	t.Cleanup(func() { saml.TimeNow, saml.Clock = prevNow, prevClock })

	key, cert := genKeyCert(t)
	idpMeta := mustURL(samlIDPMetaURL)
	idpSSO := mustURL(samlIDPSSOURL)
	acs := mustURL(samlACSURL)

	idp := &saml.IdentityProvider{
		Key: key, Certificate: cert, MetadataURL: idpMeta, SSOURL: idpSSO,
	}
	// The SP the IdP issues for; its metadata carries no IdP trust (it is the SP's
	// own descriptor), so it can be built before wiring the IdP trust below.
	spMetaOnly := &saml.ServiceProvider{EntityID: samlSPEntityID, MetadataURL: mustURL(samlSPEntityID), AcsURL: acs}

	// The SP under test trusts THIS IdP's signing cert (idp.Metadata()).
	sp := saml.ServiceProvider{
		EntityID: samlSPEntityID, MetadataURL: mustURL(samlSPEntityID), AcsURL: acs,
		IDPMetadata: idp.Metadata(), AllowIDPInitiated: false,
	}
	prov := &samlProvider{
		beginSP: sp, validateSP: sp, metaSP: sp, idpSSOURL: samlIDPSSOURL, replay: newReplayStore(),
	}

	if encrypt {
		// The SP advertises an RSA encryption cert (so the IdP encrypts to it), and
		// validateSP holds the matching key to decrypt the EncryptedAssertion.
		encKey, encCert := genKeyCert(t)
		spMetaOnly.Certificate = encCert
		prov.validateSP.Key = encKey
		prov.validateSP.Certificate = encCert
		prov.encCert = encCert
	}
	// Resolve the SP metadata AFTER wiring any encryption cert (the IdP looks for the
	// use="encryption" KeyDescriptor in this document).
	idp.ServiceProviderProvider = &mockSPP{meta: spMetaOnly.Metadata()}

	return &samlHarness{idp: idp, sp: prov}
}

// mint produces a base64 SAML Response for the given AuthnRequest id and session,
// signed by the IdP — the same shape an IdP posts to the ACS.
func (h *samlHarness) mint(t *testing.T, requestID string, session *saml.Session) string {
	t.Helper()
	authnReq := fmt.Sprintf(`<AuthnRequest xmlns="urn:oasis:names:tc:SAML:2.0:protocol" `+
		`AssertionConsumerServiceURL=%q Destination=%q ID=%q IssueInstant=%q Version="2.0">`+
		`<Issuer xmlns="urn:oasis:names:tc:SAML:2.0:assertion" `+
		`Format="urn:oasis:names:tc:SAML:2.0:nameid-format:entity">%s</Issuer></AuthnRequest>`,
		samlACSURL, samlIDPSSOURL, requestID, samlFixedTime.Format(time.RFC3339), samlSPEntityID)

	req := saml.IdpAuthnRequest{Now: samlFixedTime, IDP: h.idp, RequestBuffer: []byte(authnReq)}
	req.HTTPRequest, _ = http.NewRequest("POST", samlIDPSSOURL, nil)
	if err := req.Validate(); err != nil {
		t.Fatalf("mint: validate authn request: %v", err)
	}
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(&req, session); err != nil {
		t.Fatalf("mint: make assertion: %v", err)
	}
	if err := req.MakeAssertionEl(); err != nil {
		t.Fatalf("mint: sign assertion: %v", err)
	}
	if err := req.MakeResponse(); err != nil {
		t.Fatalf("mint: make response: %v", err)
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.ResponseEl)
	xmlStr, err := doc.WriteToString()
	if err != nil {
		t.Fatalf("mint: serialize response: %v", err)
	}
	return base64.StdEncoding.EncodeToString([]byte(xmlStr))
}

func (h *samlHarness) validate(raw, requestID string) (auth.FederatedIdentity, error) {
	return h.sp.validate(context.Background(), auth.Assertion{Protocol: auth.ProtocolSAML, Raw: raw, RequestID: requestID})
}

func session(nameID, email string) *saml.Session {
	return &saml.Session{
		ID: "sess-1", CreateTime: samlFixedTime, ExpireTime: samlFixedTime.Add(time.Hour),
		NameID: nameID, NameIDFormat: "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent",
		UserName: "alice", UserEmail: email,
	}
}

func TestSAML_ValidResponse(t *testing.T) {
	h := newSAMLHarness(t)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	id, err := h.validate(raw, "req-1")
	if err != nil {
		t.Fatalf("valid signed response rejected: %v", err)
	}
	if id.Email != "alice@corp.example" || id.Subject != "alice-nameid" {
		t.Errorf("identity = %+v, want alice@corp.example / alice-nameid", id)
	}
}

func TestSAML_ReplayRejected(t *testing.T) {
	h := newSAMLHarness(t)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	if _, err := h.validate(raw, "req-1"); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}
	// The SAME signed response, replayed, must be rejected by our replay store
	// (crewjam itself does not dedup bearer assertions).
	if _, err := h.validate(raw, "req-1"); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("a replayed assertion must be rejected; got err=%v", err)
	}
}

func TestSAML_TamperedSignatureRejected(t *testing.T) {
	h := newSAMLHarness(t)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	// Flip a signed attribute value: the XML stays well-formed but the signature
	// digest no longer matches, so ParseResponse must reject it.
	decoded, _ := base64.StdEncoding.DecodeString(raw)
	tampered := strings.Replace(string(decoded), "alice@corp.example", "attacker@evil.example", 1)
	if tampered == string(decoded) {
		t.Fatal("test setup: expected to tamper the response body")
	}
	bad := base64.StdEncoding.EncodeToString([]byte(tampered))

	if _, err := h.validate(bad, "req-1"); err == nil {
		t.Fatal("a response whose signed content was tampered must be rejected")
	}
}

func TestSAML_WrongInResponseToRejected(t *testing.T) {
	h := newSAMLHarness(t)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	// The engine expected a DIFFERENT AuthnRequest id than the one bound into the
	// assertion → the SP-initiated InResponseTo binding fails (CSRF/mixup defense).
	if _, err := h.validate(raw, "some-other-request-id"); err == nil {
		t.Fatal("a response whose InResponseTo does not match the engine's request id must be rejected")
	}
}

func TestSAML_ExpiredConditionsRejected(t *testing.T) {
	h := newSAMLHarness(t)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	// Advance the SAML clock well past the assertion's NotOnOrAfter window (the dsig
	// signature clock stays valid, so the failure is the time window, not the sig).
	saml.TimeNow = func() time.Time { return samlFixedTime.Add(30 * time.Minute) }

	if _, err := h.validate(raw, "req-1"); err == nil {
		t.Fatal("an assertion past its NotOnOrAfter window must be rejected")
	}
}

func TestSAML_EmailFallsBackToNameID(t *testing.T) {
	h := newSAMLHarness(t)
	// No email attribute, but the NameID is an email — validate falls back to it.
	raw := h.mint(t, "req-1", session("carol@corp.example", ""))

	id, err := h.validate(raw, "req-1")
	if err != nil {
		t.Fatalf("response with email NameID rejected: %v", err)
	}
	if id.Email != "carol@corp.example" {
		t.Errorf("email = %q, want fallback to the email-shaped NameID", id.Email)
	}
}

func TestSAML_EncryptedAssertion(t *testing.T) {
	h := buildSAMLHarness(t, true)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	// Sanity: the IdP actually encrypted the assertion (the regulated requirement).
	decoded, _ := base64.StdEncoding.DecodeString(raw)
	if !strings.Contains(string(decoded), "EncryptedAssertion") {
		t.Fatal("test setup: expected the IdP to emit an EncryptedAssertion")
	}

	id, err := h.validate(raw, "req-1")
	if err != nil {
		t.Fatalf("encrypted assertion rejected: %v", err)
	}
	if id.Email != "alice@corp.example" || id.Subject != "alice-nameid" {
		t.Errorf("identity = %+v, want alice@corp.example / alice-nameid", id)
	}
}

func TestSAML_EncryptedAssertionTamperRejected(t *testing.T) {
	h := buildSAMLHarness(t, true)
	raw := h.mint(t, "req-1", session("alice-nameid", "alice@corp.example"))

	// Corrupt a byte INSIDE the assertion's ciphertext (the EncryptedData CipherValue
	// base64 body), keeping the XML well-formed — so the failure is the AES-CBC /
	// RSA-OAEP integrity (or the inner signature over the decrypted plaintext), NOT a
	// structural XML error. An encrypted assertion must not be a way to smuggle past
	// signature validation.
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	s := string(decoded)
	// The LAST CipherValue is the EncryptedData payload; flip a base64 char in its body
	// (a few bytes before its closing tag), to a DIFFERENT valid base64 char.
	closeEnd := strings.LastIndex(s, "CipherValue>")
	if closeEnd < 0 {
		t.Fatal("test setup: no CipherValue in the encrypted response")
	}
	closeStart := strings.LastIndex(s[:closeEnd], "<")
	pos := closeStart - 6 // safely inside the (large) base64 body
	b := []byte(s)
	if b[pos] == 'A' {
		b[pos] = 'B'
	} else {
		b[pos] = 'A'
	}
	bad := base64.StdEncoding.EncodeToString(b)
	if _, err := h.validate(bad, "req-1"); err == nil {
		t.Fatal("an EncryptedAssertion with tampered ciphertext must be rejected")
	}
}

// --- helpers ----------------------------------------------------------------

func mustURL(s string) url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return *u
}

func genKeyCert(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "idp.olivares.test"},
		NotBefore:    samlFixedTime.Add(-time.Hour),
		NotAfter:     samlFixedTime.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}
