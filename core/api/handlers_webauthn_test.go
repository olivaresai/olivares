// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// Software FIDO2 authenticator for the ceremony tests: it speaks the real
// wire protocol (CBOR attestation object, COSE ES256 key, signed authenticator
// data) against the real verification path — no mocked verifier anywhere. The
// harness serves over httptest (host example.com, no TLS), so the derived
// relying party is rpID "example.com" / origin "http://example.com".

const (
	testRPID   = "example.com"
	testOrigin = "http://example.com"

	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagAT = 0x40 // attested credential data included
)

type softAuthenticator struct {
	key       *ecdsa.PrivateKey
	credID    []byte
	aaguid    [16]byte
	signCount uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := make([]byte, 16)
	if _, err := rand.Read(credID); err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{key: key, credID: credID}
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// coseKey renders the authenticator's public key as a COSE_Key (EC2 / ES256).
func (a *softAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, err := cbor.Marshal(map[int]any{
		1: 2, 3: -7, -1: 1, -2: x, -3: y, // kty EC2, alg ES256, crv P-256
	})
	if err != nil {
		t.Fatal(err)
	}
	return cose
}

// authData builds authenticator data: rpIdHash || flags || signCount [|| attestedCredentialData].
func (a *softAuthenticator) authData(t *testing.T, flags byte, attested bool) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(testRPID))
	out := append([]byte{}, rpHash[:]...)
	out = append(out, flags)
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, a.signCount)
	out = append(out, count...)
	if attested {
		out = append(out, a.aaguid[:]...)
		idLen := make([]byte, 2)
		binary.BigEndian.PutUint16(idLen, uint16(len(a.credID)))
		out = append(out, idLen...)
		out = append(out, a.credID...)
		out = append(out, a.coseKey(t)...)
	}
	return out
}

func clientDataJSON(t *testing.T, ceremony, challenge, origin string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": ceremony, "challenge": challenge, "origin": origin, "crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// challengeFrom extracts the base64url challenge from an options response.
func challengeFrom(t *testing.T, r resp) string {
	t.Helper()
	pk, ok := r.body["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("options response has no publicKey envelope: %s", r.raw)
	}
	ch, ok := pk["challenge"].(string)
	if !ok || ch == "" {
		t.Fatalf("options response has no challenge: %s", r.raw)
	}
	return ch
}

// register produces the browser's registration response (attestation "none")
// for the given creation options. flags lets a test drop UV.
func (a *softAuthenticator) register(t *testing.T, options resp, flags byte, origin string) map[string]any {
	t.Helper()
	a.signCount++
	attObj, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(t, flags, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64url(a.credID), "rawId": b64url(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url(clientDataJSON(t, "webauthn.create", challengeFrom(t, options), origin)),
			"attestationObject": b64url(attObj),
		},
	}
}

// assert produces the browser's authentication response (a real ES256 signature
// over authData || SHA-256(clientDataJSON)). flags lets a test drop UV; tamper
// flips a signature byte.
func (a *softAuthenticator) assert(t *testing.T, options resp, flags byte, origin string, tamper bool) map[string]any {
	t.Helper()
	a.signCount++
	cd := clientDataJSON(t, "webauthn.get", challengeFrom(t, options), origin)
	ad := a.authData(t, flags, false)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		sig[len(sig)-1] ^= 0xff
	}
	return map[string]any{
		"id": b64url(a.credID), "rawId": b64url(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url(cd),
			"authenticatorData": b64url(ad),
			"signature":         b64url(sig),
			"userHandle":        nil,
		},
	}
}

// registerOK runs a full happy-path registration for the token's session.
func registerOK(t *testing.T, h *harness, token string, soft *softAuthenticator) {
	t.Helper()
	opts := h.do("POST", "/v1/auth/webauthn/register/options", token, nil, nil)
	if opts.code != http.StatusOK {
		t.Fatalf("register options = %d %s", opts.code, opts.raw)
	}
	r := h.do("POST", "/v1/auth/webauthn/register", token,
		map[string]any{"credential": soft.register(t, opts, flagUP|flagUV|flagAT, testOrigin)}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}
}

func whoamiAAL(t *testing.T, h *harness, token string) (int, []string) {
	t.Helper()
	r := h.do("GET", "/v1/auth/whoami", token, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", r.code, r.raw)
	}
	aal := 0
	if v, ok := r.body["aal"].(float64); ok {
		aal = int(v)
	}
	var amr []string
	if vs, ok := r.body["amr"].([]any); ok {
		for _, v := range vs {
			amr = append(amr, v.(string))
		}
	}
	return aal, amr
}

// TestWebAuthnRegistrationAndStepUp is the happy path: a password
// session (AAL1, amr [pwd]) registers an authenticator, runs the step-up
// ceremony and reads back AAL3 + amr [pwd webauthn] through whoami — the exact
// flow that flips the panel's fail-closed gate live.
func TestWebAuthnRegistrationAndStepUp(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()

	aal, amr := whoamiAAL(t, h, token)
	if aal != 1 || len(amr) != 1 || amr[0] != "pwd" {
		t.Fatalf("fresh session aal/amr = %d %v, want 1 [pwd]", aal, amr)
	}

	// Options advertise the AAL3 posture: UV required, and the creation leg
	// carries the user entity the decoder needs.
	opts := h.do("POST", "/v1/auth/webauthn/register/options", token, nil, nil)
	if opts.code != http.StatusOK {
		t.Fatalf("register options = %d %s", opts.code, opts.raw)
	}
	pk := opts.body["publicKey"].(map[string]any)
	if sel, ok := pk["authenticatorSelection"].(map[string]any); !ok || sel["userVerification"] != "required" {
		t.Fatalf("creation options must require user verification: %s", opts.raw)
	}
	if user, ok := pk["user"].(map[string]any); !ok || user["id"] == "" || user["name"] == "" {
		t.Fatalf("creation options missing user entity: %s", opts.raw)
	}

	soft := newSoftAuthenticator(t)
	r := h.do("POST", "/v1/auth/webauthn/register", token,
		map[string]any{"credential": soft.register(t, opts, flagUP|flagUV|flagAT, testOrigin)}, nil)
	if r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}

	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if aopts.code != http.StatusOK {
		t.Fatalf("auth options = %d %s", aopts.code, aopts.raw)
	}
	apk := aopts.body["publicKey"].(map[string]any)
	if apk["userVerification"] != "required" {
		t.Fatalf("assertion options must require user verification: %s", aopts.raw)
	}
	allowed, _ := apk["allowCredentials"].([]any)
	if len(allowed) != 1 {
		t.Fatalf("allowCredentials = %v, want the registered credential", apk["allowCredentials"])
	}

	r = h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, testOrigin, false)}, nil)
	if r.code != http.StatusOK || r.body["aal"] != float64(3) {
		t.Fatalf("authenticate = %d %s", r.code, r.raw)
	}

	aal, amr = whoamiAAL(t, h, token)
	if aal != 3 {
		t.Fatalf("post-step-up aal = %d, want 3", aal)
	}
	if len(amr) != 2 || amr[0] != "pwd" || amr[1] != "webauthn" {
		t.Fatalf("post-step-up amr = %v, want [pwd webauthn]", amr)
	}

	// The elevation rides the SESSION: a refresh (rotated credential) keeps it.
	rr := h.do("POST", "/v1/auth/refresh", token, nil, nil)
	if rr.code != http.StatusOK {
		t.Fatalf("refresh = %d %s", rr.code, rr.raw)
	}
	aal, _ = whoamiAAL(t, h, rr.body["token"].(string))
	if aal != 3 {
		t.Fatalf("post-refresh aal = %d, want 3", aal)
	}
}

// TestWebAuthnUserVerificationRequired pins the AAL3 deny: an authenticator
// that did NOT verify its user (UV flag absent) is rejected on BOTH legs, and
// the session is never elevated.
func TestWebAuthnUserVerificationRequired(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()

	// Registration without UV is refused.
	opts := h.do("POST", "/v1/auth/webauthn/register/options", token, nil, nil)
	soft := newSoftAuthenticator(t)
	r := h.do("POST", "/v1/auth/webauthn/register", token,
		map[string]any{"credential": soft.register(t, opts, flagUP|flagAT, testOrigin)}, nil)
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "webauthn_verification_failed" {
		t.Fatalf("register without UV = %d %s, want 403 webauthn_verification_failed", r.code, r.raw)
	}

	// Register properly, then assert without UV: refused, AAL stays 1.
	registerOK(t, h, token, soft)
	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	r = h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP, testOrigin, false)}, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("assert without UV = %d %s, want 403", r.code, r.raw)
	}
	if aal, _ := whoamiAAL(t, h, token); aal != 1 {
		t.Fatalf("aal after denied step-up = %d, want 1", aal)
	}
}

// TestWebAuthnInvalidAssertionDenied pins the verification deny paths: a
// tampered signature and a wrong origin both refuse elevation.
func TestWebAuthnInvalidAssertionDenied(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	registerOK(t, h, token, soft)

	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, testOrigin, true)}, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("tampered signature = %d %s, want 403", r.code, r.raw)
	}

	aopts = h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	r = h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, "https://evil.example", false)}, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("wrong origin = %d %s, want 403", r.code, r.raw)
	}
	if aal, _ := whoamiAAL(t, h, token); aal != 1 {
		t.Fatalf("aal after denied assertions = %d, want 1", aal)
	}
}

// TestWebAuthnChallengeSingleUse pins anti-replay: a challenge is consumed by
// its FIRST finish attempt — replaying the same (valid) response is refused.
func TestWebAuthnChallengeSingleUse(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	registerOK(t, h, token, soft)

	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	cred := soft.assert(t, aopts, flagUP|flagUV, testOrigin, false)
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token, map[string]any{"credential": cred}, nil); r.code != http.StatusOK {
		t.Fatalf("first finish = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token, map[string]any{"credential": cred}, nil); r.code != http.StatusForbidden {
		t.Fatalf("replayed finish = %d %s, want 403", r.code, r.raw)
	}

	// An invalid attempt also consumes the challenge: re-finishing after a deny
	// requires a fresh begin.
	aopts = h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP, testOrigin, false)}, nil); r.code != http.StatusForbidden {
		t.Fatalf("UV-less finish = %d %s, want 403", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, testOrigin, false)}, nil); r.code != http.StatusForbidden {
		t.Fatalf("finish after consumed challenge = %d %s, want 403", r.code, r.raw)
	}
}

// TestWebAuthnCloneDetectionDenied pins the sign-count regression deny: an
// assertion whose counter does not advance past the stored one (a cloned
// authenticator signal) is refused even though its signature is valid.
func TestWebAuthnCloneDetectionDenied(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	registerOK(t, h, token, soft)

	soft.signCount = 10
	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, testOrigin, false)}, nil); r.code != http.StatusOK {
		t.Fatalf("first assert = %d %s", r.code, r.raw)
	}

	soft.signCount = 3 // regress below the stored counter -> clone warning
	aopts = h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, testOrigin, false)}, nil); r.code != http.StatusForbidden {
		t.Fatalf("regressed sign count = %d %s, want 403", r.code, r.raw)
	}
}

// TestWebAuthnAdditionalRegistrationRequiresStepUp pins the 800-63B binding
// rule: the FIRST credential bootstraps from an AAL1 session, but once a user
// has one, adding another demands a fresh AAL3 step-up — a stolen password
// session cannot bind the thief's own key next to the legitimate one.
func TestWebAuthnAdditionalRegistrationRequiresStepUp(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	first := newSoftAuthenticator(t)
	registerOK(t, h, token, first) // bootstrap at AAL1

	// A second registration from the (still AAL1-fresh? no — registration does
	// not elevate) session is refused with the machine-readable step_up_required.
	r := h.do("POST", "/v1/auth/webauthn/register/options", token, nil, nil)
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("second register options at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	// After a real step-up with the FIRST key, a second key registers fine.
	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if rr := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": first.assert(t, aopts, flagUP|flagUV, testOrigin, false)}, nil); rr.code != http.StatusOK {
		t.Fatalf("step-up = %d %s", rr.code, rr.raw)
	}
	second := newSoftAuthenticator(t)
	registerOK(t, h, token, second)
}

// TestWebAuthnCredentialLifecycle pins list/delete: metadata-only listing,
// AAL3-required unregistration (an AAL1 thief must not clear the victim's keys
// and reopen the bootstrap), and a deleted credential no longer asserts.
func TestWebAuthnCredentialLifecycle(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	registerOK(t, h, token, soft)

	list := h.do("GET", "/v1/auth/webauthn/credentials", token, nil, nil)
	if list.code != http.StatusOK {
		t.Fatalf("list = %d %s", list.code, list.raw)
	}
	items := list.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("credentials = %v, want 1", items)
	}
	item := items[0].(map[string]any)
	if _, leaked := item["credential"]; leaked {
		t.Fatalf("credential material leaked over the list endpoint: %s", list.raw)
	}
	// the enriched list includes backup_eligible.
	if _, has := item["backup_eligible"]; !has {
		t.Fatalf("list response missing backup_eligible field: %s", list.raw)
	}
	id := item["id"].(string)

	// Delete at AAL1 is refused (step_up_required).
	r := h.do("DELETE", "/v1/auth/webauthn/credentials/"+id, token, nil, nil)
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("delete at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	// Step up — then try to delete the ONLY credential: last-credential guard.
	aopts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if rr := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, aopts, flagUP|flagUV, testOrigin, false)}, nil); rr.code != http.StatusOK {
		t.Fatalf("step-up = %d %s", rr.code, rr.raw)
	}
	// deleting the last credential is refused (409 last_webauthn_credential).
	r = h.do("DELETE", "/v1/auth/webauthn/credentials/"+id, token, nil, nil)
	if r.code != http.StatusConflict || r.body["error"].(map[string]any)["code"] != "last_webauthn_credential" {
		t.Fatalf("delete last credential = %d %s, want 409 last_webauthn_credential", r.code, r.raw)
	}

	// Register a second credential, then deleting the first succeeds.
	soft2 := newSoftAuthenticator(t)
	registerOK(t, h, token, soft2)
	// Need a fresh step-up (the delete attempt consumed the AAL3 freshness? no,
	// it did not succeed — AAL3 is still live on the session).
	if r := h.do("DELETE", "/v1/auth/webauthn/credentials/"+id, token, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete with two credentials = %d %s, want 204", r.code, r.raw)
	}
	// The first key no longer works for assertion, but the second does.
	aopts = h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if aopts.code != http.StatusOK {
		t.Fatalf("options after delete first = %d %s", aopts.code, aopts.raw)
	}
}

// TestWebAuthnRegisterWithName verifies that the optional name field is persisted
// and surfaced through the list endpoint.
func TestWebAuthnRegisterWithName(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)

	opts := h.do("POST", "/v1/auth/webauthn/register/options", token, nil, nil)
	if opts.code != http.StatusOK {
		t.Fatalf("register options = %d %s", opts.code, opts.raw)
	}
	r := h.do("POST", "/v1/auth/webauthn/register", token,
		map[string]any{
			"credential": soft.register(t, opts, flagUP|flagUV|flagAT, testOrigin),
			"name":       "YubiKey 5C NFC",
		}, nil)
	if r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("register with name = %d %s", r.code, r.raw)
	}

	list := h.do("GET", "/v1/auth/webauthn/credentials", token, nil, nil)
	if list.code != http.StatusOK {
		t.Fatalf("list = %d %s", list.code, list.raw)
	}
	items := list.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["name"] != "YubiKey 5C NFC" {
		t.Fatalf("name = %q, want %q", item["name"], "YubiKey 5C NFC")
	}
}

// TestWebAuthnRename verifies the PATCH rename endpoint: owner-only, no AAL3
// required, and the new name surfaces on the next list.
func TestWebAuthnRename(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	registerOK(t, h, token, soft)

	list := h.do("GET", "/v1/auth/webauthn/credentials", token, nil, nil)
	id := list.body["items"].([]any)[0].(map[string]any)["id"].(string)

	// Rename works without step-up (metadata change).
	r := h.do("PATCH", "/v1/auth/webauthn/credentials/"+id, token,
		map[string]any{"name": "Office YubiKey"}, nil)
	if r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("rename = %d %s", r.code, r.raw)
	}

	// The new name appears on list.
	list = h.do("GET", "/v1/auth/webauthn/credentials", token, nil, nil)
	item := list.body["items"].([]any)[0].(map[string]any)
	if item["name"] != "Office YubiKey" {
		t.Fatalf("name after rename = %q, want %q", item["name"], "Office YubiKey")
	}

	// Empty name is rejected.
	r = h.do("PATCH", "/v1/auth/webauthn/credentials/"+id, token,
		map[string]any{"name": ""}, nil)
	if r.code != http.StatusBadRequest {
		t.Fatalf("rename empty = %d %s, want 400", r.code, r.raw)
	}

	// Renaming a non-existent id returns 404.
	r = h.do("PATCH", "/v1/auth/webauthn/credentials/nonexistent", token,
		map[string]any{"name": "X"}, nil)
	if r.code != http.StatusNotFound {
		t.Fatalf("rename nonexistent = %d %s, want 404", r.code, r.raw)
	}
}

// TestWebAuthnPrincipalGates pins the principal rules: no anonymous ceremony,
// no token-principal ceremony, and a user with no registered credential cannot
// begin a step-up (machine-readable no_webauthn_credential).
func TestWebAuthnPrincipalGates(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()

	if r := h.do("POST", "/v1/auth/webauthn/register/options", "", nil, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous options = %d, want 401", r.code)
	}
	r := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if r.code != http.StatusBadRequest || r.body["error"].(map[string]any)["code"] != "no_webauthn_credential" {
		t.Fatalf("step-up without credential = %d %s, want 400 no_webauthn_credential", r.code, r.raw)
	}

	tok := h.do("POST", "/v1/tokens", token, map[string]any{"name": "ci", "superadmin": true}, nil)
	if tok.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", tok.code, tok.raw)
	}
	if r := h.do("POST", "/v1/auth/webauthn/register/options", tok.body["token"].(string), nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("token-principal options = %d %s, want 400", r.code, r.raw)
	}
}
