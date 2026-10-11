// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	dsig "github.com/russellhaering/goxmldsig"

	"github.com/olivaresai/olivares/core/auth"
)

// The SAML login journey. It drives the real Provider the way the control plane does:
// build it from configuration (fetching the IdP metadata over HTTP), begin a login, let a
// hand-built IdP answer, and validate the POSTed response; and it reads the SP metadata an
// IdP is onboarded with. The IdP side shares no code with the SAML library under test, so
// this file is the regression net for that library: it must stay green across a swap.

type jnyConfig struct {
	metadata                string // IdP metadata; "" = one signing cert, no wrapper
	metadataStatus          int    // HTTP status of the metadata fetch; 0 = 200
	encCertPEM, encKeyPEM   string
	signCertPEM, signKeyPEM string
	emailAttr, groupsAttr   string
}

type jny struct {
	t    *testing.T
	idp  *jnyKey
	prov *Provider
	n    int
}

// newJourneyErr wires a provider against a fresh IdP and reports the construction error.
func newJourneyErr(t *testing.T, mods ...func(*jnyConfig)) (*jny, error) {
	t.Helper()
	cfg := jnyConfig{}
	for _, m := range mods {
		m(&cfg)
	}
	idp := newJnyKey(t, "idp.olivares.test")
	metadata := cfg.metadata
	if metadata == "" {
		metadata = jnyMetadata(jnyIdPEntityID, jnyIdPSSOURL, false, jnyKeyDescriptor{use: "signing", cert: idp})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if cfg.metadataStatus != 0 {
			http.Error(w, "unavailable", cfg.metadataStatus)
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write([]byte(metadata))
	}))
	t.Cleanup(srv.Close)

	env := map[string]string{
		envProtocol:        auth.ProtocolSAML,
		envSAMLMetadataURL: srv.URL + "/metadata",
		envSAMLEntityID:    jnySPEntityID,
		envSAMLACSURL:      jnyACSURL,
		envSAMLIDPSSOURL:   jnyIdPSSOURL,
		envSAMLCertPEM:     cfg.encCertPEM,
		envSAMLKeyPEM:      cfg.encKeyPEM,
		envSAMLSignCertPEM: cfg.signCertPEM,
		envSAMLSignKeyPEM:  cfg.signKeyPEM,
		envSAMLEmailAttr:   cfg.emailAttr,
		envSAMLGroupsAttr:  cfg.groupsAttr,
	}
	prov, err := FromEnv(envFrom(env))
	return &jny{t: t, idp: idp, prov: prov}, err
}

func newJourney(t *testing.T, mods ...func(*jnyConfig)) *jny {
	t.Helper()
	j, err := newJourneyErr(t, mods...)
	if err != nil {
		t.Fatalf("build SAML provider: %v", err)
	}
	return j
}

// begin starts a login with a fresh core-owned request id and returns what the IdP receives.
func (j *jny) begin() (string, jnyAuthnRequest, string) {
	j.t.Helper()
	j.n++
	id := fmt.Sprintf("_flow-%d-%s", j.n, jnyRandID())
	redirect, err := j.prov.BeginAuth(context.Background(), auth.AuthParams{
		State: "csrf-state", Nonce: "nonce", RequestID: id, RedirectURI: jnyACSURL,
	})
	if err != nil {
		j.t.Fatalf("BeginAuth: %v", err)
	}
	req, _ := jnyDecodeRedirect(j.t, redirect)
	return id, req, redirect
}

func (j *jny) login(raw, requestID string) (auth.FederatedIdentity, error) {
	return j.prov.ValidateAssertion(context.Background(), auth.Assertion{Protocol: auth.ProtocolSAML, Raw: raw, RequestID: requestID})
}

// answer begins a login and returns a default response for it for the test to adjust.
func (j *jny) answer() (jnyResp, string) {
	id, _, _ := j.begin()
	return newJnyResp(j.idp, id), id
}

func (j *jny) mustLogin(r jnyResp, id string) auth.FederatedIdentity {
	j.t.Helper()
	identity, err := j.login(r.mint(j.t), id)
	if err != nil {
		j.t.Fatalf("a valid response was rejected: %v", err)
	}
	return identity
}

func TestJourneySAML_Login(t *testing.T) {
	j := newJourney(t, func(c *jnyConfig) { c.groupsAttr = "groups" })
	id, req, redirect := j.begin()

	if !strings.HasPrefix(redirect, jnyIdPSSOURL+"?") {
		t.Errorf("redirect = %q, want the IdP SSO URL", redirect)
	}
	if _, q := jnyDecodeRedirect(t, redirect); q.Get("RelayState") != "csrf-state" || q.Get("Signature") != "" {
		t.Errorf("RelayState=%q Signature=%q, want the CSRF state and an unsigned request", q.Get("RelayState"), q.Get("Signature"))
	}
	for field, got := range map[string][2]string{
		"ID (the core's request id)":  {req.ID, id},
		"Issuer":                      {req.Issuer, jnySPEntityID},
		"AssertionConsumerServiceURL": {req.AssertionConsumerServiceURL, jnyACSURL},
		"Destination":                 {req.Destination, jnyIdPSSOURL},
		"ProtocolBinding":             {req.ProtocolBinding, "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"},
		"Version":                     {req.Version, "2.0"},
	} {
		if got[0] != got[1] {
			t.Errorf("AuthnRequest %s = %q, want %q", field, got[0], got[1])
		}
	}
	if _, err := time.Parse(time.RFC3339, req.IssueInstant); err != nil {
		t.Errorf("AuthnRequest IssueInstant %q: %v", req.IssueInstant, err)
	}

	r := newJnyResp(j.idp, id)
	r.attrs = append(r.attrs, jnyAttr{name: "groups", values: []string{"eng", " ", "admins"}})
	identity := j.mustLogin(r, id)

	want := auth.FederatedIdentity{
		Protocol: auth.ProtocolSAML, Subject: "alice-nameid", Issuer: jnyIdPEntityID,
		Email: "alice@corp.example", DisplayName: "Alice Example", Groups: []string{"eng", "admins"}, AAL: auth.AAL1,
	}
	if !reflect.DeepEqual(identity, want) {
		t.Errorf("identity = %+v\nwant     %+v", identity, want)
	}
}

func TestJourneySAML_RejectsAndAccepts(t *testing.T) {
	j := newJourney(t)
	attacker := newJnyKey(t, "attacker.test")
	now := time.Now().UTC().Truncate(time.Second)
	other := "_some-other-request"
	cases := []struct {
		name   string
		adjust func(r *jnyResp)
		reject bool
	}{
		{"InResponseTo on the Response is another request", func(r *jnyResp) { r.respInResponseTo = jnyStr(other) }, true},
		{"InResponseTo on the SubjectConfirmationData is another request", func(r *jnyResp) { r.scdInResponseTo = jnyStr(other) }, true},
		{"unsolicited response with an empty InResponseTo", func(r *jnyResp) { r.respInResponseTo, r.scdInResponseTo = jnyStr(""), jnyStr("") }, true},
		{"Recipient is not our ACS", func(r *jnyResp) { r.recipient = "https://evil.test/acs" }, true},
		{"Destination is not our ACS", func(r *jnyResp) { r.destination = "https://evil.test/acs" }, true},
		{"audience is another service provider", func(r *jnyResp) { r.audience = "https://other-sp.test/metadata" }, true},
		{"assertion expired", func(r *jnyResp) { r.notBefore, r.notOnOrAfter = now.Add(-20*time.Minute), now.Add(-10*time.Minute) }, true},
		{"assertion not yet valid", func(r *jnyResp) { r.notBefore, r.notOnOrAfter = now.Add(10*time.Minute), now.Add(20*time.Minute) }, true},
		{"subject confirmation expired", func(r *jnyResp) { r.scdNotOnOrAfter = now.Add(-10 * time.Minute) }, true},
		{"response issued too long ago", func(r *jnyResp) { r.respIssueInstant = now.Add(-10 * time.Minute) }, true},
		{"assertion issued too long ago", func(r *jnyResp) { r.assertionIssueInstant = now.Add(-10 * time.Minute) }, true},
		{"assertion issued by another IdP", func(r *jnyResp) { r.assertionIssuer = "https://evil-idp.test" }, true},
		{"response issued by another IdP", func(r *jnyResp) { r.respIssuer = "https://evil-idp.test" }, true},
		{"status is not Success", func(r *jnyResp) { r.status = "urn:oasis:names:tc:SAML:2.0:status:Responder" }, true},
		{"nothing is signed", func(r *jnyResp) { r.sign = "none" }, true},
		{"response signed by an unknown key", func(r *jnyResp) { r.signer = attacker }, true},
		{"assertion signed by an unknown key", func(r *jnyResp) { r.sign, r.signer = "assertion", attacker }, true},
		{"no email attribute and no email NameID", func(r *jnyResp) { r.attrs, r.nameID = nil, "opaque-subject" }, true},

		{"issue delay: response issued one minute ago", func(r *jnyResp) { r.respIssueInstant = now.Add(-time.Minute) }, false},
		{"issue delay: response issued two minutes ago", func(r *jnyResp) { r.respIssueInstant = now.Add(-2 * time.Minute) }, true},
		{"issue delay: assertion issued one minute ago", func(r *jnyResp) { r.assertionIssueInstant = now.Add(-time.Minute) }, false},
		{"issue delay: assertion issued two minutes ago", func(r *jnyResp) { r.assertionIssueInstant = now.Add(-2 * time.Minute) }, true},
		// The skew allowance is 180 s; probe 30 s either side so a slow runner cannot flip a case.
		{"skew: Conditions expired 150 seconds ago", func(r *jnyResp) {
			r.notBefore, r.notOnOrAfter = now.Add(-10*time.Minute), now.Add(-150*time.Second)
		}, false},
		{"skew: Conditions expired 210 seconds ago", func(r *jnyResp) {
			r.notBefore, r.notOnOrAfter = now.Add(-10*time.Minute), now.Add(-210*time.Second)
		}, true},
		{"skew: Conditions valid in 150 seconds", func(r *jnyResp) { r.notBefore, r.notOnOrAfter = now.Add(150*time.Second), now.Add(10*time.Minute) }, false},
		{"skew: Conditions valid in 210 seconds", func(r *jnyResp) { r.notBefore, r.notOnOrAfter = now.Add(210*time.Second), now.Add(10*time.Minute) }, true},
		// Inherited from crewjam and kept so no IdP is broken: an assertion with no audience
		// is accepted. The InResponseTo and Recipient checks still bind it to this flow.
		{"the assertion carries no AudienceRestriction", func(r *jnyResp) { r.noAudience = true }, false},
		{"Conditions carry no NotBefore", func(r *jnyResp) { r.noNotBefore = true }, false},
		{"clock skew: not yet valid by one minute", func(r *jnyResp) { r.notBefore, r.notOnOrAfter = now.Add(time.Minute), now.Add(10*time.Minute) }, false},
		{"clock skew: Conditions expired one minute ago", func(r *jnyResp) {
			r.notBefore, r.notOnOrAfter = now.Add(-10*time.Minute), now.Add(-time.Minute)
		}, false},
		{"only the assertion is signed", func(r *jnyResp) { r.sign = "assertion" }, false},
		{"response and assertion are signed", func(r *jnyResp) { r.sign = "both" }, false},
		{"email falls back to an email NameID", func(r *jnyResp) { r.attrs, r.nameID = nil, "carol@corp.example" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j.t = t
			// Positive control: the unadjusted response for this very flow is accepted, so
			// the rejection below is caused by the one field this case changes.
			control, controlID := j.answer()
			j.mustLogin(control, controlID)

			r, id := j.answer()
			tc.adjust(&r)
			_, err := j.login(r.mint(t), id)
			switch {
			case tc.reject && err == nil:
				t.Fatal("the response was accepted; it must be rejected")
			case !tc.reject && err != nil:
				t.Fatalf("the response was rejected: %v", err)
			}
		})
	}
}

func TestJourneySAML_RejectsWrongRequestID(t *testing.T) {
	j := newJourney(t)
	control, controlID := j.answer()
	j.mustLogin(control, controlID)

	r, _ := j.answer()
	// The engine holds a different request id than the one the IdP answered.
	if _, err := j.login(r.mint(t), "_the-flow-the-engine-holds"); err == nil {
		t.Fatal("a response to another AuthnRequest must be rejected")
	}
}

func TestJourneySAML_ReplayRejected(t *testing.T) {
	j := newJourney(t)
	r, id := j.answer()
	raw := r.mint(t)
	if _, err := j.login(raw, id); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}
	if _, err := j.login(raw, id); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("the same response posted twice must be rejected as a replay; got %v", err)
	}
}

func TestJourneySAML_RejectsTamperingAndWrapping(t *testing.T) {
	j := newJourney(t)
	mutations := []struct {
		name string
		raw  func(t *testing.T, r jnyResp) string
	}{
		{"a signed value is changed", func(t *testing.T, r jnyResp) string {
			decoded, _ := base64.StdEncoding.DecodeString(r.mint(t))
			tampered := strings.Replace(string(decoded), "alice@corp.example", "attacker@evil.example", 1)
			if tampered == string(decoded) {
				t.Fatal("the response carries no value to tamper with")
			}
			return base64.StdEncoding.EncodeToString([]byte(tampered))
		}},
		{"an unsigned assertion is injected into a signed response", func(t *testing.T, r jnyResp) string {
			signed := r.mintXML(t)
			evil := r
			evil.sign, evil.nameID = "none", "evil-subject"
			evil.attrs = []jnyAttr{{name: "email", values: []string{"attacker@evil.example"}}}
			signed.InsertChildAt(1, jnyParse(t, evil.assertionXML()))
			return base64.StdEncoding.EncodeToString(jnySerialize(t, signed))
		}},
		{"a signed response is wrapped inside a forged one", func(t *testing.T, r jnyResp) string {
			signed := r.mintXML(t)
			forged := r
			forged.sign, forged.nameID = "none", "evil-subject"
			forged.attrs = []jnyAttr{{name: "email", values: []string{"attacker@evil.example"}}}
			wrapper := forged.mintXML(t)
			extensions := jnyParse(t, `<samlp:Extensions xmlns:samlp="`+jnyNSProtocol+`"/>`)
			extensions.AddChild(signed)
			wrapper.AddChild(extensions)
			return base64.StdEncoding.EncodeToString(jnySerialize(t, wrapper))
		}},
		{"a valid signature is copied onto a forged response", func(t *testing.T, r jnyResp) string {
			signed := r.mintXML(t)
			forged := r
			forged.sign, forged.nameID = "none", "evil-subject"
			forged.attrs = []jnyAttr{{name: "email", values: []string{"attacker@evil.example"}}}
			wrapper := forged.mintXML(t)
			wrapper.InsertChildAt(1, signed.FindElement("./Signature").Copy())
			return base64.StdEncoding.EncodeToString(jnySerialize(t, wrapper))
		}},
		{"the payload is not base64", func(*testing.T, jnyResp) string { return "%%% not base64 %%%" }},
		{"the payload is not XML", func(*testing.T, jnyResp) string { return base64.StdEncoding.EncodeToString([]byte("hello")) }},
		{"the payload is empty", func(*testing.T, jnyResp) string { return "" }},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			j.t = t
			control, controlID := j.answer()
			j.mustLogin(control, controlID)

			r, id := j.answer()
			if identity, err := j.login(m.raw(t, r), id); err == nil {
				t.Fatalf("accepted as %+v; it must be rejected", identity)
			}
		})
	}
}

// An empty request id must never match: it would let an unsolicited (IdP-initiated) response
// log in, which is the login CSRF the InResponseTo binding exists to stop.
func TestJourneySAML_RejectsEmptyRequestID(t *testing.T) {
	j := newJourney(t)
	control, controlID := j.answer()
	j.mustLogin(control, controlID)

	r, _ := j.answer()
	unsolicited := newJnyResp(j.idp, "")
	for name, raw := range map[string]string{
		"a response to an AuthnRequest with an id, validated with none": r.mint(t),
		"an unsolicited response with an empty InResponseTo":            unsolicited.mint(t),
	} {
		if _, err := j.login(raw, ""); err == nil {
			t.Errorf("%s was accepted with an empty request id; it must be rejected", name)
		}
	}
}

// IdPs write timestamps with fractional seconds (Entra uses seven digits) or a zone offset.
func TestJourneySAML_AcceptsIdPTimestampStyles(t *testing.T) {
	j := newJourney(t)
	styles := map[string]func(time.Time) string{
		"seven fractional digits": func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.0000000Z") },
		"milliseconds":            func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") },
		"a zone offset":           func(t time.Time) string { return t.In(time.FixedZone("", 2*3600)).Format("2006-01-02T15:04:05-07:00") },
	}
	for name, stamp := range styles {
		t.Run(name, func(t *testing.T) {
			j.t = t
			r, id := j.answer()
			r.stamp = stamp
			j.mustLogin(r, id)
		})
	}
}

// A signed assertion with a required element removed is refused, never half read.
func TestJourneySAML_RejectsAssertionMissingElements(t *testing.T) {
	j := newJourney(t)
	for _, drop := range []string{"Issuer", "Conditions", "Subject", "Subject/SubjectConfirmation", "Subject/SubjectConfirmation/SubjectConfirmationData"} {
		t.Run(drop, func(t *testing.T) {
			j.t = t
			control, controlID := j.answer()
			j.mustLogin(control, controlID)

			r, id := j.answer()
			r.drop = []string{drop}
			if _, err := j.login(r.mint(t), id); err == nil {
				t.Fatalf("an assertion without %s was accepted", drop)
			}
		})
	}
}

// An unsigned assertion next to a signed one must never decide the identity: it is either
// refused with the response, or ignored.
func TestJourneySAML_UnsignedSecondAssertionNeverWins(t *testing.T) {
	j := newJourney(t)
	for _, position := range []string{"first", "last"} {
		t.Run(position, func(t *testing.T) {
			j.t = t
			r, id := j.answer()
			r.sign = "assertion"
			signed := r.mintXML(t)
			evil := r
			evil.sign, evil.nameID = "none", "evil-subject"
			evil.attrs = []jnyAttr{{name: "email", values: []string{"attacker@evil.example"}}}
			at := len(signed.Child)
			if position == "first" {
				at = 2 // after the Issuer and Status
			}
			signed.InsertChildAt(at, jnyParse(t, evil.assertionXML()))
			identity, err := j.login(base64.StdEncoding.EncodeToString(jnySerialize(t, signed)), id)
			if err == nil && identity.Email != "alice@corp.example" {
				t.Fatalf("the unsigned assertion decided the identity: %+v", identity)
			}
		})
	}
}

// The replay store is keyed by the assertion, not by the response around it.
func TestJourneySAML_ReplayAcrossEnvelopes(t *testing.T) {
	j := newJourney(t)
	const assertionID = "same-assertion-in-two-envelopes"
	first, firstID := j.answer()
	first.assertionID = assertionID
	j.mustLogin(first, firstID)

	second, secondID := j.answer()
	second.assertionID = assertionID
	if _, err := j.login(second.mint(t), secondID); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("a new response carrying a consumed assertion must be rejected as a replay; got %v", err)
	}
}

// A refused response must not consume its assertion id.
func TestJourneySAML_RefusedResponseDoesNotConsumeTheAssertion(t *testing.T) {
	j := newJourney(t)
	const assertionID = "refused-then-valid"
	bad, badID := j.answer()
	bad.assertionID = assertionID
	bad.recipient = "https://evil.test/acs"
	if _, err := j.login(bad.mint(t), badID); err == nil {
		t.Fatal("a response with the wrong Recipient was accepted")
	}
	good, goodID := j.answer()
	good.assertionID = assertionID
	j.mustLogin(good, goodID)
}

// dropEmptyKeyInfo parses unverified XML, so it must not parse what gosaml2 would refuse by
// size: a payload over the token bound is left untouched for gosaml2 to reject.
func TestDropEmptyKeyInfoLeavesAnOversizedPayloadUntouched(t *testing.T) {
	small := `<r xmlns:ds="` + jnyNSDSig + `"><ds:Signature><ds:KeyInfo/></ds:Signature></r>`
	oversized := `<r xmlns:ds="` + jnyNSDSig + `">` + strings.Repeat("<a/>", 60000) /* well over gosaml2's 50,000-token bound */ + `<ds:Signature><ds:KeyInfo/></ds:Signature></r>`
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	// Control: a small payload has its empty KeyInfo dropped, so the helper is live.
	if got := dropEmptyKeyInfo(encode(small)); got == encode(small) {
		t.Fatal("control: an empty KeyInfo in a small payload must be dropped")
	}
	if got := dropEmptyKeyInfo(encode(oversized)); got != encode(oversized) {
		t.Error("an oversized payload must be returned untouched, not parsed")
	}
}

// TestJourneySAML_KeyInfoIsOnlyAHint: the signature's KeyInfo is the sender's claim about
// its key, not trust. A KeyInfo that names no certificate (a bare KeyValue, or none) is
// verified against the IdP certificate from the metadata; one that names a certificate the
// metadata does not list is refused.
func TestJourneySAML_KeyInfoIsOnlyAHint(t *testing.T) {
	j := newJourney(t)
	attacker := newJnyKey(t, "attacker.test")
	keyValue := `<ds:KeyInfo xmlns:ds="` + jnyNSDSig + `"><ds:KeyValue><ds:RSAKeyValue><ds:Modulus>AQAB</ds:Modulus><ds:Exponent>AQAB</ds:Exponent></ds:RSAKeyValue></ds:KeyValue></ds:KeyInfo>`
	untrusted := `<ds:KeyInfo xmlns:ds="` + jnyNSDSig + `"><ds:X509Data><ds:X509Certificate>` + attacker.b64() + `</ds:X509Certificate></ds:X509Data></ds:KeyInfo>`
	cases := []struct {
		name    string
		sign    string // which element carries the signature: "response" or "assertion"
		keyInfo string // "" removes the element
		reject  bool
	}{
		{"a KeyValue and no certificate", "response", keyValue, false},
		{"no KeyInfo at all", "response", "", false},
		{"a certificate the metadata does not list", "response", untrusted, true},
		{"a KeyValue on an assertion signature", "assertion", keyValue, false},
		{"no KeyInfo on an assertion signature", "assertion", "", false},
		{"an unlisted certificate on an assertion signature", "assertion", untrusted, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j.t = t
			control, controlID := j.answer()
			j.mustLogin(control, controlID)

			r, id := j.answer()
			r.sign = tc.sign
			signed := r.mintXML(t)
			sigPath := "./Signature"
			if tc.sign == "assertion" {
				sigPath = "./Assertion/Signature"
			}
			signature := signed.FindElement(sigPath)
			if signature == nil || signature.FindElement("./KeyInfo") == nil {
				t.Fatal("the signed element carries no KeyInfo to replace")
			}
			signature.RemoveChild(signature.FindElement("./KeyInfo"))
			if tc.keyInfo != "" {
				signature.AddChild(jnyParse(t, tc.keyInfo))
			}
			_, err := j.login(base64.StdEncoding.EncodeToString(jnySerialize(t, signed)), id)
			switch {
			case tc.reject && err == nil:
				t.Fatal("accepted; it must be rejected")
			case !tc.reject && err != nil:
				t.Fatalf("rejected: %v", err)
			}
		})
	}
}

func TestJourneySAML_EmailAndNameAttributes(t *testing.T) {
	cases := []struct {
		name                string
		emailAttr           string
		attrs               []jnyAttr
		wantEmail, wantName string
	}{
		{"email", "", []jnyAttr{{name: "email", values: []string{"a@x.test"}}}, "a@x.test", ""},
		{"emailAddress", "", []jnyAttr{{name: "emailAddress", values: []string{"b@x.test"}}}, "b@x.test", ""},
		{"oid", "", []jnyAttr{{name: "urn:oid:0.9.2342.19200300.100.1.3", values: []string{"c@x.test"}}}, "c@x.test", ""},
		{"claims uri", "", []jnyAttr{{name: "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", values: []string{"d@x.test"}}}, "d@x.test", ""},
		{"name matches case-insensitively", "", []jnyAttr{{name: "EMAIL", values: []string{"e@x.test"}}}, "e@x.test", ""},
		{"configured attribute by name", "mail", []jnyAttr{{name: "mail", values: []string{"f@x.test"}}}, "f@x.test", ""},
		{"configured attribute by friendly name", "mail", []jnyAttr{{name: "urn:x:1", friendly: "mail", values: []string{"g@x.test"}}}, "g@x.test", ""},
		{"display name", "", []jnyAttr{{name: "email", values: []string{"h@x.test"}}, {name: "name", values: []string{"Hana"}}}, "h@x.test", "Hana"},
		{"display name from the claims uri", "", []jnyAttr{{name: "email", values: []string{"i@x.test"}}, {name: "http://schemas.microsoft.com/identity/claims/displayname", values: []string{"Ines"}}}, "i@x.test", "Ines"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := newJourney(t, func(c *jnyConfig) { c.emailAttr = tc.emailAttr })
			r, id := j.answer()
			r.attrs = tc.attrs
			identity := j.mustLogin(r, id)
			if identity.Email != tc.wantEmail || identity.DisplayName != tc.wantName {
				t.Errorf("email=%q name=%q, want %q / %q", identity.Email, identity.DisplayName, tc.wantEmail, tc.wantName)
			}
		})
	}
}

func TestJourneySAML_Assurance(t *testing.T) {
	j := newJourney(t)
	now := time.Now().UTC().Truncate(time.Second)
	earlier := now.Add(-2 * time.Minute)
	cases := []struct {
		name    string
		authn   []jnyAuthn
		wantAAL int
		wantAt  time.Time
	}{
		{"mapped MFA context", []jnyAuthn{{context: jnyCtxMFA, instant: earlier}}, auth.AAL2, earlier},
		{"unmapped context does not elevate", []jnyAuthn{{context: jnyCtxPassword, instant: now}}, auth.AAL1, time.Time{}},
		{"MFA context with an expired upstream session", []jnyAuthn{{context: jnyCtxMFA, instant: earlier, sessionNotOnOrAfter: now.Add(-time.Minute)}}, auth.AAL1, time.Time{}},
		{"no authentication statement", nil, auth.AAL1, time.Time{}},
		// gosaml2 keeps one AuthnStatement per assertion, the last. An MFA statement that
		// comes first is therefore not seen and the login stays AAL1: it fails safe.
		{"an MFA statement after a password statement counts", []jnyAuthn{{context: jnyCtxPassword, instant: now}, {context: jnyCtxMFA, instant: earlier}}, auth.AAL2, earlier},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j.t = t
			r, id := j.answer()
			r.authn = tc.authn
			identity := j.mustLogin(r, id)
			if identity.AAL != tc.wantAAL || !identity.AuthenticatedAt.Equal(tc.wantAt) {
				t.Errorf("AAL=%d at %v, want %d at %v", identity.AAL, identity.AuthenticatedAt, tc.wantAAL, tc.wantAt)
			}
		})
	}
}

func TestJourneySAML_SignedAuthnRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ec      bool
		wantAlg string
	}{
		{"EC P-256", true, dsig.ECDSASHA256SignatureMethod},
		{"RSA 2048", false, dsig.RSASHA256SignatureMethod},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certPEM, keyPEM := keypairPEM(t, tc.ec)
			j := newJourney(t, func(c *jnyConfig) { c.signCertPEM, c.signKeyPEM = certPEM, keyPEM })
			id, _, redirect := j.begin()
			u, err := url.Parse(redirect)
			if err != nil {
				t.Fatal(err)
			}
			if got := u.Query().Get("SigAlg"); got != tc.wantAlg {
				t.Errorf("SigAlg = %q, want %q", got, tc.wantAlg)
			}
			// HTTP-Redirect binding (SAML bindings 3.4.4.1): the signed octets are the
			// still-encoded SAMLRequest, RelayState and SigAlg values in that fixed order,
			// whatever order the query carries them in. An IdP rebuilds them the same way.
			raw := map[string]string{}
			for _, kv := range strings.Split(u.RawQuery, "&") {
				k, v, _ := strings.Cut(kv, "=")
				raw[k] = v
			}
			if raw["Signature"] == "" {
				t.Fatal("a signed AuthnRequest must carry a Signature query parameter")
			}
			octets := "SAMLRequest=" + raw["SAMLRequest"] + "&RelayState=" + raw["RelayState"] + "&SigAlg=" + raw["SigAlg"]
			sig, err := base64.StdEncoding.DecodeString(u.Query().Get("Signature"))
			if err != nil {
				t.Fatalf("Signature is not base64: %v", err)
			}
			digest := sha256.Sum256([]byte(octets))
			block, _ := pem.Decode([]byte(certPEM))
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			switch pub := cert.PublicKey.(type) {
			case *ecdsa.PublicKey:
				if !ecdsa.VerifyASN1(pub, digest[:], sig) {
					t.Error("the EC signature must verify against the SP certificate")
				}
			case *rsa.PublicKey:
				if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
					t.Errorf("the RSA signature must verify against the SP certificate: %v", err)
				}
			}
			// A login still completes on the flow that started with the signed request.
			j.mustLogin(newJnyResp(j.idp, id), id)
		})
	}
}

func TestJourneySAML_ServiceProviderMetadata(t *testing.T) {
	encCertPEM, encKeyPEM := keypairPEM(t, false)  // RSA: decrypts
	signCertPEM, signKeyPEM := keypairPEM(t, true) // EC: signs
	j := newJourney(t, func(c *jnyConfig) {
		c.encCertPEM, c.encKeyPEM, c.signCertPEM, c.signKeyPEM = encCertPEM, encKeyPEM, signCertPEM, signKeyPEM
	})
	doc, err := j.prov.SAMLMetadata()
	if err != nil {
		t.Fatalf("SAMLMetadata: %v", err)
	}
	s := string(doc)
	for _, want := range []string{`AuthnRequestsSigned="true"`, jnySPEntityID, jnyACSURL, "HTTP-POST"} {
		if !strings.Contains(s, want) {
			t.Errorf("metadata lacks %q\n%s", want, s)
		}
	}
	if strings.Contains(s, "<NameIDFormat></NameIDFormat>") || strings.Contains(s, "<NameIDFormat/>") {
		t.Error("metadata must not carry an empty NameIDFormat")
	}
	kds := parseKeyDescriptors(t, doc)
	for use, certPEM := range map[string]string{"signing": signCertPEM, "encryption": encCertPEM} {
		block, _ := pem.Decode([]byte(certPEM))
		if got := normalizeB64(kds[use].cert); got != base64.StdEncoding.EncodeToString(block.Bytes) {
			t.Errorf("the %s KeyDescriptor carries the wrong certificate", use)
		}
	}
	if len(kds["signing"].encMethods) != 0 {
		t.Errorf("the signing descriptor must carry no EncryptionMethod, got %v", kds["signing"].encMethods)
	}
	if !strings.Contains(strings.Join(kds["encryption"].encMethods, " "), "rsa-oaep-mgf1p") {
		t.Errorf("the encryption descriptor must advertise rsa-oaep-mgf1p, got %v", kds["encryption"].encMethods)
	}
}

func TestJourneySAML_ServiceProviderMetadataIsHonest(t *testing.T) {
	signCertPEM, signKeyPEM := keypairPEM(t, true)
	signing := newJourney(t, func(c *jnyConfig) { c.signCertPEM, c.signKeyPEM = signCertPEM, signKeyPEM })
	doc, err := signing.prov.SAMLMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `use="signing"`) || strings.Contains(string(doc), `use="encryption"`) {
		t.Errorf("a signing-only SP must publish a signing descriptor and no encryption descriptor\n%s", doc)
	}

	bare, err := newJourney(t).prov.SAMLMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "KeyDescriptor") || !strings.Contains(string(bare), `AuthnRequestsSigned="false"`) {
		t.Errorf("an SP with no keys must publish no KeyDescriptor and AuthnRequestsSigned=false\n%s", bare)
	}
}

// TestJourneySAML_EncryptedAssertions logs in with an assertion encrypted the way the SP
// metadata asks: to the published encryption certificate, with every data algorithm the
// metadata advertises. A capability advertised but not honored fails here.
func TestJourneySAML_EncryptedAssertions(t *testing.T) {
	encCertPEM, encKeyPEM := keypairPEM(t, false)
	j := newJourney(t, func(c *jnyConfig) { c.encCertPEM, c.encKeyPEM = encCertPEM, encKeyPEM })
	doc, err := j.prov.SAMLMetadata()
	if err != nil {
		t.Fatal(err)
	}
	enc := parseKeyDescriptors(t, doc)["encryption"]
	block, err := base64.StdEncoding.DecodeString(normalizeB64(enc.cert))
	if err != nil {
		t.Fatal(err)
	}
	spCert, err := x509.ParseCertificate(block)
	if err != nil {
		t.Fatal(err)
	}

	var dataAlgs []string
	for _, m := range enc.encMethods {
		if strings.Contains(m, "-cbc") {
			dataAlgs = append(dataAlgs, m)
		}
	}
	if len(dataAlgs) == 0 {
		t.Fatalf("the SP advertises no CBC data algorithm: %v", enc.encMethods)
	}
	for _, alg := range dataAlgs {
		for _, sign := range []string{"response", "assertion"} {
			t.Run(alg[strings.LastIndex(alg, "#")+1:]+"/signed "+sign, func(t *testing.T) {
				j.t = t
				r, id := j.answer()
				r.encryptTo, r.encryptAlg, r.sign = spCert, alg, sign
				identity := j.mustLogin(r, id)
				if identity.Email != "alice@corp.example" || identity.Subject != "alice-nameid" {
					t.Errorf("identity = %+v", identity)
				}
			})
		}
	}

	t.Run("rejections", func(t *testing.T) {
		j.t = t
		wrongCert := newJnyKey(t, "someone-else").cert
		for _, tc := range []struct {
			name   string
			adjust func(r *jnyResp)
			mutate func(raw string) string
		}{
			{name: "encrypted to a certificate that is not ours", adjust: func(r *jnyResp) { r.encryptTo = wrongCert }},
			{name: "encrypted but nothing is signed", adjust: func(r *jnyResp) { r.sign = "none" }},
			{name: "ciphertext is tampered", mutate: func(raw string) string {
				decoded, _ := base64.StdEncoding.DecodeString(raw)
				s := string(decoded)
				end := strings.LastIndex(s, "CipherValue>")
				closing := strings.LastIndex(s[:end], "<")
				opening := strings.LastIndex(s[:closing], ">")
				pos := opening + (closing-opening)/2 // inside the ciphertext, away from its padding block
				b := []byte(s)
				if b[pos] == 'A' {
					b[pos] = 'B'
				} else {
					b[pos] = 'A'
				}
				return base64.StdEncoding.EncodeToString(b)
			}},
		} {
			control, controlID := j.answer()
			control.encryptTo = spCert
			j.mustLogin(control, controlID)

			r, id := j.answer()
			r.encryptTo = spCert
			if tc.adjust != nil {
				tc.adjust(&r)
			}
			raw := r.mint(t)
			if tc.mutate != nil {
				raw = tc.mutate(raw)
			}
			if _, err := j.login(raw, id); err == nil {
				t.Errorf("%s: accepted; it must be rejected", tc.name)
			}
		}
	})

	t.Run("an SP without an encryption key rejects an encrypted assertion", func(t *testing.T) {
		plain := newJourney(t)
		r, id := plain.answer()
		r.encryptTo = spCert
		if _, err := plain.login(r.mint(t), id); err == nil {
			t.Fatal("an encrypted assertion cannot be read without a decryption key and must be rejected")
		}
	})
}

func TestJourneySAML_IdPMetadataTrust(t *testing.T) {
	t.Run("an EntitiesDescriptor federation document is accepted", func(t *testing.T) {
		idp := newJnyKey(t, "idp")
		j := newJourney(t, func(c *jnyConfig) {
			c.metadata = jnyMetadata(jnyIdPEntityID, jnyIdPSSOURL, true, jnyKeyDescriptor{use: "signing", cert: idp})
		})
		j.idp = idp
		r, id := j.answer()
		j.mustLogin(r, id)
	})

	t.Run("a rolled-over IdP signing key listed second is trusted", func(t *testing.T) {
		old, next := newJnyKey(t, "old"), newJnyKey(t, "next")
		j := newJourney(t, func(c *jnyConfig) {
			c.metadata = jnyMetadata(jnyIdPEntityID, jnyIdPSSOURL, false, jnyKeyDescriptor{use: "signing", cert: old}, jnyKeyDescriptor{use: "signing", cert: next})
		})
		for _, k := range []*jnyKey{old, next} {
			j.idp = k
			r, id := j.answer()
			j.mustLogin(r, id)
		}
	})

	t.Run("a certificate with no use attribute may sign", func(t *testing.T) {
		idp := newJnyKey(t, "idp")
		j := newJourney(t, func(c *jnyConfig) {
			c.metadata = jnyMetadata(jnyIdPEntityID, jnyIdPSSOURL, false, jnyKeyDescriptor{cert: idp})
		})
		j.idp = idp
		r, id := j.answer()
		j.mustLogin(r, id)
	})

	t.Run("a key published only for encryption cannot sign", func(t *testing.T) {
		idp, encOnly := newJnyKey(t, "idp"), newJnyKey(t, "encryption-only")
		j := newJourney(t, func(c *jnyConfig) {
			c.metadata = jnyMetadata(jnyIdPEntityID, jnyIdPSSOURL, false,
				jnyKeyDescriptor{use: "signing", cert: idp}, jnyKeyDescriptor{use: "encryption", cert: encOnly})
		})
		j.idp = idp
		control, controlID := j.answer()
		j.mustLogin(control, controlID)

		j.idp = encOnly
		r, id := j.answer()
		if _, err := j.login(r.mint(t), id); err == nil {
			t.Fatal("a response signed by a key published only for encryption must be rejected")
		}
	})

	t.Run("metadata with an empty entityID leaves SSO unconfigured", func(t *testing.T) {
		idp := newJnyKey(t, "idp")
		_, err := newJourneyErr(t, func(c *jnyConfig) {
			c.metadata = jnyMetadata("", jnyIdPSSOURL, false, jnyKeyDescriptor{use: "signing", cert: idp})
		})
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured: an IdP with no entity id cannot be told apart from any other issuer", err)
		}
	})

	t.Run("metadata that lists no signing certificate leaves SSO unconfigured", func(t *testing.T) {
		encOnly := newJnyKey(t, "encryption-only")
		_, err := newJourneyErr(t, func(c *jnyConfig) {
			c.metadata = jnyMetadata(jnyIdPEntityID, jnyIdPSSOURL, false, jnyKeyDescriptor{use: "encryption", cert: encOnly})
		})
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured: no login could ever verify, so SSO reports itself unconfigured", err)
		}
	})

	t.Run("an IdP metadata fetch failure leaves SSO unconfigured", func(t *testing.T) {
		_, err := newJourneyErr(t, func(c *jnyConfig) { c.metadataStatus = http.StatusInternalServerError })
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured (the core maps it to 501, fail closed)", err)
		}
	})

	t.Run("metadata that is not XML leaves SSO unconfigured", func(t *testing.T) {
		_, err := newJourneyErr(t, func(c *jnyConfig) { c.metadata = "<html>not metadata" })
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured", err)
		}
	})
}
