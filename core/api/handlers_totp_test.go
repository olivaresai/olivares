// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// apiTOTPSealer mirrors the composition root's AES-256-GCM sealer (cmd/olivares
// /totpwire.go) closely enough to prove the wire contract end to end.
type apiTOTPSealer struct{ aead cipher.AEAD }

func (s *apiTOTPSealer) Seal(_ context.Context, scope model.TenantID, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nil, nonce, plaintext, []byte("totp.api.test|"+scope.String()))
	return "v1:" + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (s *apiTOTPSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		trimmedV1(sealed))
	if err != nil {
		return nil, err
	}
	ns := s.aead.NonceSize()
	return s.aead.Open(nil, raw[:ns], raw[ns:], []byte("totp.api.test|"+scope.String()))
}

func trimmedV1(s string) string {
	if len(s) > 3 && s[:3] == "v1:" {
		return s[3:]
	}
	return s
}

// apiTOTPCode computes the current 6-digit SHA1 code for a base32 secret —
// the authenticator app's side of the ceremony, from the RFC directly.
func apiTOTPCode(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, seed)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := int(sum[len(sum)-1] & 0x0f)
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000)
}

// wireTOTPSealer gives the harness's authenticator a working sealer and
// returns nothing; every TOTP handler test starts here.
func wireTOTPSealer(t *testing.T, h *harness) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	h.authr.WithTOTPSeedSealer(&apiTOTPSealer{aead: aead})
}

// The console journey over the wire: enrol from a session (secret + otpauth URI
// + QR PNG), activate with the app's code, then every password login answers
// with the pending challenge that a code (or a recovery code) completes.
func TestTOTPHTTPJourney(t *testing.T) {
	h := newHarness(t)
	wireTOTPSealer(t, h)
	admin := h.adminLogin()

	// Enrol: the provisioning material is shown exactly once, QR included.
	r := h.do("POST", "/v1/auth/totp/enrol", admin, map[string]any{}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("enrol = %d %s", r.code, r.raw)
	}
	secret := r.body["secret"].(string)
	uri := r.body["uri"].(string)
	if secret == "" || uri == "" || !base64IsPNG(t, r.body["qr_png_base64"].(string)) {
		t.Fatalf("enrolment material = %s", r.raw)
	}
	r = h.do("POST", "/v1/auth/totp/activate", admin, map[string]any{"code": apiTOTPCode(t, secret, time.Now())}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("activate = %d %s", r.code, r.raw)
	}
	codes := r.body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(codes))
	}

	// The admin's next login answers with the challenge, not a session.
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": harnessAdminEmail, "password": harnessAdminPass}, nil)
	if r.code != http.StatusOK || r.body["mfa_required"] != true {
		t.Fatalf("gated login = %d %s", r.code, r.raw)
	}
	mfa := r.body["mfa_token"].(string)
	if mfa == "" || r.body["enrolment_required"] != false {
		t.Fatalf("gated login = %s", r.raw)
	}

	// A wrong code is a 401 that does NOT consume the challenge: the correct
	// code then completes the SAME one (a typo must not restart the login).
	r = h.do("POST", "/v1/auth/totp/challenge", "", map[string]any{"mfa_token": mfa, "code": "000000"}, nil)
	if r.code != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/totp/challenge", "", map[string]any{"mfa_token": mfa, "code": apiTOTPCode(t, secret, time.Now())}, nil)
	if r.code != http.StatusOK || r.body["token"] == nil {
		t.Fatalf("challenge after typo = %d %s", r.code, r.raw)
	}
	completed := r.body["token"].(string)
	// Single-use on success: the completed challenge cannot mint again.
	r = h.do("POST", "/v1/auth/totp/challenge", "", map[string]any{"mfa_token": mfa, "code": apiTOTPCode(t, secret, time.Now())}, nil)
	if r.code != http.StatusUnauthorized {
		t.Fatalf("completed challenge reused = %d, want 401", r.code)
	}
	r = h.do("GET", "/v1/auth/totp/status", completed, nil, nil)
	if r.code != http.StatusOK || r.body["enrolled"] != true {
		t.Fatalf("status = %d %s", r.code, r.raw)
	}

	// A recovery code completes a login exactly once.
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": harnessAdminEmail, "password": harnessAdminPass}, nil)
	mfa = r.body["mfa_token"].(string)
	r = h.do("POST", "/v1/auth/totp/challenge", "", map[string]any{"mfa_token": mfa, "recovery_code": codes[0].(string)}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("recovery completion = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": harnessAdminEmail, "password": harnessAdminPass}, nil)
	mfa = r.body["mfa_token"].(string)
	r = h.do("POST", "/v1/auth/totp/challenge", "", map[string]any{"mfa_token": mfa, "recovery_code": codes[0].(string)}, nil)
	if r.code != http.StatusUnauthorized {
		t.Fatalf("spent recovery code = %d, want 401", r.code)
	}
}

// The require-for-administrators policy surface: read is system:admin; write is
// system:admin + AAL3; the gated admin login carries enrolment_required.
func TestTOTPPolicyHTTP(t *testing.T) {
	h := newHarness(t)
	wireTOTPSealer(t, h)
	h.requirePasskeyStepUp()
	admin := h.adminLogin()

	// Policy write demands a stepped-up session (the setup session is AAL1) under the
	// passkey step-up; the default policy asks for none.
	if r := h.do("PUT", "/v1/auth/totp/policy", admin, map[string]any{"require_for_admins": true}, nil); r.code != http.StatusForbidden {
		t.Fatalf("policy write at AAL1 = %d, want 403: %s", r.code, r.raw)
	}
	h.elevate(admin)
	r := h.do("PUT", "/v1/auth/totp/policy", admin, map[string]any{"require_for_admins": true}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("policy write = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/auth/totp/policy", admin, nil, nil)
	if r.code != http.StatusOK || r.body["require_for_admins"] != true {
		t.Fatalf("policy read = %d %s", r.code, r.raw)
	}

	// The admin's next login demands an enrolment (no factor yet).
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": harnessAdminEmail, "password": harnessAdminPass}, nil)
	if r.code != http.StatusOK || r.body["enrolment_required"] != true {
		t.Fatalf("policy-gated login = %d %s", r.code, r.raw)
	}
	mfa := r.body["mfa_token"].(string)

	// Enrol from the pending credential, activate, and the login completes.
	r = h.do("POST", "/v1/auth/totp/enrol", "", map[string]any{"mfa_token": mfa}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("enrol for login = %d %s", r.code, r.raw)
	}
	secret := r.body["secret"].(string)
	r = h.do("POST", "/v1/auth/totp/activate", "", map[string]any{"mfa_token": mfa, "code": apiTOTPCode(t, secret, time.Now())}, nil)
	if r.code != http.StatusOK || r.body["token"] == nil {
		t.Fatalf("activate for login = %d %s", r.code, r.raw)
	}
	if got := r.body["recovery_codes"].([]any); len(got) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(got))
	}
}

// The administrator reset: membership:write + AAL3 gates, tenant-scoped, and an
// editor is refused.
func TestTOTPAdminResetHTTP(t *testing.T) {
	h := newHarness(t)
	wireTOTPSealer(t, h)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "totp-reset")

	mkUser := func(email, pass, role string) string {
		r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass, "tenant": tenant.String(), "role": role}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", email, r.code, r.raw)
		}
		lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
		if lr.code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
		}
		return lr.body["token"].(string)
	}
	boss := mkUser("reset-boss@totp.test", "bosspass1234", auth.RoleAdmin)
	hand := mkUser("reset-hand@totp.test", "handpass1234", auth.RoleEditor)
	// The reset below is asserted under the passkey step-up; the people exist first
	// (adding a person asks for the same step-up, HU-28).
	h.requirePasskeyStepUp()

	// The victim enroles a factor.
	r := h.do("POST", "/v1/auth/totp/enrol", hand, map[string]any{}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("enrol = %d %s", r.code, r.raw)
	}
	secret := r.body["secret"].(string)
	if r = h.do("POST", "/v1/auth/totp/activate", hand, map[string]any{"code": apiTOTPCode(t, secret, time.Now())}, nil); r.code != http.StatusOK {
		t.Fatalf("activate = %d %s", r.code, r.raw)
	}
	victimID := userID(t, h, hand)

	// An editor cannot even read the factor; the admin can.
	if r = h.do("GET", "/v1/users/"+victimID+"/totp", hand, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor status = %d, want 403", r.code)
	}
	if r = h.do("GET", "/v1/users/"+victimID+"/totp", boss, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["enrolled"] != true {
		t.Fatalf("admin status = %d %s", r.code, r.raw)
	}

	// Reset demands AAL3 even for the admin...
	if r = h.do("POST", "/v1/users/"+victimID+"/totp/reset", boss, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("reset at AAL1 = %d, want 403: %s", r.code, r.raw)
	}
	h.elevate(boss)
	if r = h.do("POST", "/v1/users/"+victimID+"/totp/reset", boss, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("reset = %d %s", r.code, r.raw)
	}
	// ...and after it the account logs in directly again.
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "reset-hand@totp.test", "password": "handpass1234"}, nil)
	if lr.code != http.StatusOK || lr.body["mfa_required"] != nil {
		t.Fatalf("login after reset = %d %s", lr.code, lr.raw)
	}
}

// Removing your own factor is AAL3-gated, like removing a passkey.
func TestTOTPRemoveRequiresAAL3(t *testing.T) {
	h := newHarness(t)
	h.requirePasskeyStepUp()
	wireTOTPSealer(t, h)
	admin := h.adminLogin()

	r := h.do("POST", "/v1/auth/totp/enrol", admin, map[string]any{}, nil)
	secret := r.body["secret"].(string)
	if r = h.do("POST", "/v1/auth/totp/activate", admin, map[string]any{"code": apiTOTPCode(t, secret, time.Now())}, nil); r.code != http.StatusOK {
		t.Fatalf("activate = %d %s", r.code, r.raw)
	}
	if r = h.do("DELETE", "/v1/auth/totp", admin, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("remove at AAL1 = %d, want 403: %s", r.code, r.raw)
	}
	h.elevate(admin)
	if r = h.do("DELETE", "/v1/auth/totp", admin, nil, nil); r.code != http.StatusOK {
		t.Fatalf("remove at AAL3 = %d %s", r.code, r.raw)
	}
	if r = h.do("GET", "/v1/auth/totp/status", admin, nil, nil); r.code != http.StatusOK || r.body["enrolled"] != false {
		t.Fatalf("status after remove = %s", r.raw)
	}
}

func base64IsPNG(t *testing.T, b64 string) bool {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 8 {
		return false
	}
	return raw[0] == 0x89 && raw[1] == 'P' && raw[2] == 'N' && raw[3] == 'G'
}

// The harness bootstrap credentials (api_test.go adminLogin).
const (
	harnessAdminEmail = "root@x.io"
	harnessAdminPass  = "supersecret1"
)

// userID resolves the calling session's account id (whoami's user_id).
func userID(t *testing.T, h *harness, token string) string {
	t.Helper()
	r := h.do("GET", "/v1/auth/whoami", token, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", r.code, r.raw)
	}
	return r.body["user_id"].(string)
}
