// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// A RELYING PARTY THAT CANNOT BE BUILT IS A 503 WITH A REMEDY, NOT A 500.
//
// Before this, all four ceremony legs wrapped the verifier's construction failure
// in a plain error that statusFor had no arm for, so it fell to default: and the
// operator was told "internal error". Nothing internal was broken and there was
// something concrete to change — which is the definition of the wrong answer.
//
// The harness serves over httptest with Host "example.com", so the relying party
// is derived per request. Overriding the Host is how a test reaches an address a
// relying party cannot be built from, with no configuration involved at all: this
// is the PRE-EXISTING path, and it is closed here for every deployment that
// declares nothing.
func TestAnUnusableRelyingPartyIsAnHonest503(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	// A single-label host. The verifier compiled into this build refuses it —
	// that is a property of the library, not of the specification, and the
	// message says so rather than blaming the standard.
	hdr := map[string]string{"Host": "olivares", "X-Forwarded-Host": "olivares"}
	for _, path := range []string{
		"/v1/auth/webauthn/register/options",
		"/v1/auth/webauthn/authenticate/options",
	} {
		r := h.do("POST", path, token, nil, hdr)
		if r.code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d %s, want 503", path, r.code, r.raw)
		}
		e, _ := r.body["error"].(map[string]any)
		if e["code"] != "webauthn_relying_party_unusable" {
			t.Fatalf("%s code = %v, want webauthn_relying_party_unusable: %s", path, e["code"], r.raw)
		}
		msg, _ := e["message"].(string)
		if msg == "internal error" || msg == "" {
			t.Fatalf("%s message = %q, which is what this whole arm exists to remove", path, msg)
		}
		// The remedy has to be actionable and must not disclose what this
		// deployment resolved: no relying-party id, no source, and no promise of
		// a log line that the 5xx band does not write.
		for _, want := range []string{"--public-url", "domain name"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s message lacks %q: %s", path, want, msg)
			}
		}
		for _, leak := range []string{"olivares\"", "rp_id", "The server log"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%s message discloses %q: %s", path, leak, msg)
			}
		}
		if cc := r.hdr.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control = %q; a 'not configured' answer must never be replayed to someone who has just configured it", path, cc)
		}
	}
}

// THE NON-FIRING DIRECTION, and it is the one that matters for an attacker: a
// REFUSED CEREMONY is not a configuration problem. Answering 503 to a bad
// signature would tell a caller which half of their attempt was wrong.
func TestARefusedCeremonyIsNotAnUnusableRelyingParty(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	registerOK(t, h, token, soft)

	opts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if opts.code != http.StatusOK {
		t.Fatalf("auth options = %d %s", opts.code, opts.raw)
	}
	r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, opts, flagUP|flagUV, testOrigin, true)}, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("a tampered signature = %d %s, want 403", r.code, r.raw)
	}
	e, _ := r.body["error"].(map[string]any)
	if e["code"] != "webauthn_verification_failed" {
		t.Fatalf("a tampered signature was classified %v", e["code"])
	}
	// And the ordinary path is untouched: the same deployment, at a usable
	// address, still completes a ceremony.
	opts = h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	ok := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, opts, flagUP|flagUV, testOrigin, false)}, nil)
	if ok.code != http.StatusOK || ok.body["aal"] != float64(3) {
		t.Fatalf("the happy path broke: %d %s", ok.code, ok.raw)
	}
}

// A DECLARED ADDRESS THAT CANNOT BE A RELYING PARTY IS AN EXPLICIT UNAVAILABLE,
// AND NOT A FALL-BACK TO THE REQUEST'S HOST.
//
// This is the outcome the whole change turns on. An operator who declares
// "https://10.0.0.7:8443" has named an address; deriving an authentication
// authority from whatever Host header arrives instead would pick one they never
// chose, and it would do it silently. All four legs refuse, including the two
// that would otherwise have succeeded against a spoofable header.
func TestADeclaredUnusableAddressRefusesEveryLegRatherThanDerivingOne(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.WebAuthnUnusable = true })
	token := h.adminLogin()
	for _, c := range []struct {
		path string
		body any
	}{
		{"/v1/auth/webauthn/register/options", nil},
		{"/v1/auth/webauthn/authenticate/options", nil},
		{"/v1/auth/webauthn/register", map[string]any{"credential": map[string]any{"id": "x"}}},
		{"/v1/auth/webauthn/authenticate", map[string]any{"credential": map[string]any{"id": "x"}}},
	} {
		// The Host a browser would send is a perfectly good relying party, which
		// is exactly why it must not be used: the operator declared otherwise.
		r := h.do("POST", c.path, token, c.body, map[string]string{"Host": "example.com"})
		if r.code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d %s, want 503 — the request Host was used as an authority nobody declared", c.path, r.code, r.raw)
			continue
		}
		e, _ := r.body["error"].(map[string]any)
		if e["code"] != "webauthn_relying_party_unusable" {
			t.Errorf("%s code = %v", c.path, e["code"])
		}
	}
}

// The two states are contradictory, and an embedder who set both would silently
// get one of them. api.New refuses instead.
func TestAPinAndAnUnusableAddressCannotBothBeSet(t *testing.T) {
	o, _, _, _, _, _ := newHarnessOptions(t)
	o.WebAuthn = auth.WebAuthnRP{ID: "panel.example.com", DisplayName: "x", Origins: []string{"https://panel.example.com"}}
	o.WebAuthnUnusable = true
	if _, err := api.New(o); err == nil {
		t.Fatal("api.New accepted a pinned relying party together with WebAuthnUnusable")
	}
	// Non-firing: either one alone is fine.
	o.WebAuthnUnusable = false
	if _, err := api.New(o); err != nil {
		t.Fatalf("api.New refused a plain pinned relying party: %v", err)
	}
}
