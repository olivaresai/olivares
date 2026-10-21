// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	vaultTOTPPath     = "/v1/console/secrets/totp?scope=tenant"
	vaultGeneratePath = "/v1/console/secrets/generate?scope=tenant"
	// vaultSeedB32 is the RFC 6238 SHA-1 test seed "12345678901234567890".
	vaultSeedB32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	vaultLogin   = `{"type":"login","username":"ann","password":"pw-` + vaultText + `","totp":"` + vaultSeedB32 + `"}`
)

// member adds a plain member (viewer): no tenant authority beyond reading.
func (f vaultFixture) member(email string) (token string, id model.ID) {
	f.h.t.Helper()
	token = f.h.mkMember(f.root, email, "fixturepass1", auth.RoleViewer, f.tenant)
	return token, f.h.principalOf(token).UserID
}

func (f vaultFixture) list(token string) resp {
	f.h.t.Helper()
	return f.h.do("GET", vaultScopePath, token, nil, tenantHdr(f.tenant))
}

func (f vaultFixture) code(token, name string) resp {
	f.h.t.Helper()
	return f.h.do("POST", vaultTOTPPath, token, map[string]any{"name": name}, tenantHdr(f.tenant))
}

// listed maps a list response to name -> hint.
func listed(t *testing.T, r resp) map[string]string {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	out := map[string]string{}
	rows, _ := r.body["secrets"].([]any)
	for _, row := range rows {
		m := row.(map[string]any)
		hint, _ := m["hint"].(string)
		out[m["name"].(string)] = hint
	}
	return out
}

// Every member keeps a vault: a viewer stores, lists, reveals and deletes her own
// items, and gains nothing else in the organization's secrets.
func TestVaultMemberKeepsAPersonalVault(t *testing.T) {
	f := newVaultFixture(t)
	carol, carolID := f.member("carol@vault.io")
	mine := f.item(carolID, "bank")

	if got := f.put(carol, mine, vaultLogin); got.code != http.StatusOK {
		t.Fatalf("member put = %d %s", got.code, got.raw)
	}
	if got := listed(t, f.list(carol)); len(got) != 1 || got[mine] != "login+totp" {
		t.Fatalf("member list = %v, want her item with its kind as the hint", got)
	}
	if got := f.reveal(carol, mine); got.code != http.StatusOK || got.body["value"] != vaultLogin {
		t.Fatalf("member reveal = %d %s", got.code, got.raw)
	}

	// Organization secrets stay with the organization's admins.
	f.put(f.alice, "mcp/github", "tenant-token")
	f.put(f.alice, "env/GITHUB_TOKEN", "tenant-env")
	f.put(f.alice, f.item(f.aliceID, "mail"), vaultValue)
	for _, name := range []string{"mcp/github", "env/OTHER", "mcp/new"} {
		if got := f.put(carol, name, "member-token"); got.code != http.StatusForbidden {
			t.Fatalf("member put %s = %d %s, want 403", name, got.code, got.raw)
		}
	}
	if got := f.h.do("DELETE", vaultScopePath, carol, map[string]any{"name": "mcp/github"}, tenantHdr(f.tenant)); got.code != http.StatusForbidden {
		t.Fatalf("member delete mcp/github = %d %s, want 403", got.code, got.raw)
	}
	if got := f.put(carol, f.item(f.aliceID, "planted"), vaultValue); got.code != http.StatusForbidden {
		t.Fatalf("member put into alice's namespace = %d %s, want 403", got.code, got.raw)
	}
	if got := f.reveal(carol, f.item(f.aliceID, "mail")); got.code != http.StatusForbidden || strings.Contains(got.raw, vaultText) {
		t.Fatalf("member reveal of alice's item = %d %s, want 403", got.code, got.raw)
	}
	// An admin lists the organization's secrets and her own vault, and nobody's else.
	adminView := listed(t, f.list(f.alice))
	if _, ok := adminView["mcp/github"]; !ok || adminView[f.item(f.aliceID, "mail")] != "note" {
		t.Fatalf("admin list = %v, want mcp/github and her own item", adminView)
	}
	if _, ok := adminView[mine]; ok {
		t.Fatalf("admin list shows carol's item: %v", adminView)
	}
	// A member lists none of the organization's secrets.
	for name := range listed(t, f.list(carol)) {
		if name != mine {
			t.Fatalf("member list shows %s", name)
		}
	}

	if got := f.h.do("DELETE", vaultScopePath, carol, map[string]any{"name": mine}, tenantHdr(f.tenant)); got.code != http.StatusNoContent {
		t.Fatalf("member delete = %d %s", got.code, got.raw)
	}
	if got := listed(t, f.list(carol)); len(got) != 0 {
		t.Fatalf("list after delete = %v", got)
	}
}

// A vault item is a document: anything else is refused before it is sealed, with a
// message that does not repeat what was sent.
func TestVaultPutRefusesWhatIsNotAnItem(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	for what, value := range map[string]string{
		"plain text":    "password-" + vaultText,
		"unknown type":  `{"type":"card","note":"` + vaultText + `"}`,
		"unknown field": `{"type":"note","note":"x","cvv":"` + vaultText + `"}`,
		"empty login":   `{"type":"login"}`,
		"bad seed":      `{"type":"totp","totp":"` + vaultText + `"}`,
	} {
		got := f.put(f.alice, name, value)
		if got.code != http.StatusBadRequest || strings.Contains(got.raw, vaultText) {
			t.Fatalf("%s: put = %d %s, want 400 that repeats nothing", what, got.code, got.raw)
		}
	}
	if got := listed(t, f.list(f.alice)); len(got) != 0 {
		t.Fatalf("a refused item was stored: %v", got)
	}
	// An edit that sends no value keeps the sealed item and its kind.
	f.put(f.alice, name, vaultLogin)
	if got := f.h.do("PUT", vaultScopePath, f.alice, map[string]any{"name": name, "description": "Main account"}, tenantHdr(f.tenant)); got.code != http.StatusOK {
		t.Fatalf("description-only edit = %d %s", got.code, got.raw)
	}
	if got := f.reveal(f.alice, name); got.body["value"] != vaultLogin {
		t.Fatalf("after the description edit the item is %s", got.raw)
	}
	if got := listed(t, f.list(f.alice)); got[name] != "login+totp" {
		t.Fatalf("after the description edit the hint is %q", got[name])
	}

	// The operator's deployment-wide store keeps taking any value under a vault/ name.
	if got := f.h.do("PUT", "/v1/console/secrets", f.root, map[string]any{"name": "vault/token", "value": "operator-token"}, nil); got.code != http.StatusOK {
		t.Fatalf("global vault/token put = %d %s, want 200", got.code, got.raw)
	}
}

// totpAt is the authenticator app's side of RFC 6238 (SHA-1, six digits, 30 s),
// written from the RFC so the test never trusts the engine's arithmetic.
func totpAt(t *testing.T, unix int64) string {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(vaultSeedB32)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha1.New, seed)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(unix/30))
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1_000_000)
}

// The owner gets the current code and the seconds it has left; the seed never leaves.
func TestVaultTOTPReturnsTheCodeNotTheSeed(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.put(f.alice, name, vaultLogin)

	before := time.Now().Unix()
	got := f.code(f.alice, name)
	after := time.Now().Unix()
	if got.code != http.StatusOK {
		t.Fatalf("code = %d %s", got.code, got.raw)
	}
	code, _ := got.body["code"].(string)
	valid := false
	for u := before; u <= after; u++ {
		valid = valid || code == totpAt(t, u)
	}
	if !valid {
		t.Fatalf("code %q is not the RFC 6238 code for the time of the request", code)
	}
	if left, _ := got.body["seconds_left"].(float64); left < 1 || left > 30 || got.body["period"] != float64(30) || got.body["digits"] != float64(6) {
		t.Fatalf("code body = %s, want 1..30 seconds left, period 30, 6 digits", got.raw)
	}
	if cc := got.hdr.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("code Cache-Control = %q, want no-store", cc)
	}
	// The seed is in no answer a list or a code gives.
	for _, raw := range []string{got.raw, f.list(f.alice).raw} {
		if strings.Contains(raw, vaultSeedB32) {
			t.Fatalf("the seed is in a response: %s", raw)
		}
	}

	issued := f.h.do("POST", "/v1/tokens", f.alice, map[string]any{"name": "ci", "tenant": f.tenant.String(), "role": auth.RoleAdmin}, nil)
	apiToken, _ := issued.body["token"].(string)
	for who, c := range map[string]struct{ token, code string }{
		"another tenant admin":  {f.bob, "forbidden"},
		"the superadmin":        {f.root, "forbidden"},
		"the owner's API token": {apiToken, "step_up_required"},
	} {
		denied := f.code(c.token, name)
		if denied.code != http.StatusForbidden || errCode(denied.body) != c.code || strings.Contains(denied.raw, code) {
			t.Fatalf("%s code = %d %s, want 403 %s without a code", who, denied.code, denied.raw, c.code)
		}
	}

	// An item without an authenticator secret, a missing item and another namespace.
	f.put(f.alice, f.item(f.aliceID, "note"), vaultValue)
	if got := f.code(f.alice, f.item(f.aliceID, "note")); got.code != http.StatusConflict || errCode(got.body) != "vault_no_totp" {
		t.Fatalf("code of a note = %d %s, want 409 vault_no_totp", got.code, got.raw)
	}
	if got := f.code(f.alice, f.item(f.aliceID, "absent")); got.code != http.StatusNotFound {
		t.Fatalf("code of a missing item = %d %s, want 404", got.code, got.raw)
	}
	for _, other := range []string{"mcp/github", f.item(f.bobID, "bank")} {
		if got := f.code(f.alice, other); got.code != http.StatusForbidden {
			t.Fatalf("code of %s = %d %s, want 403", other, got.code, got.raw)
		}
	}
	if got := f.h.do("POST", "/v1/console/secrets/totp", f.alice, map[string]any{"name": name}, nil); got.code != http.StatusForbidden {
		t.Fatalf("code in the global scope = %d %s, want 403", got.code, got.raw)
	}
}

// Each code is one ledger entry naming the actor and the item, holding neither the
// code nor the seed, and codes share the reveal budget.
func TestVaultTOTPIsAuditedAndRateLimited(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.put(f.alice, name, vaultLogin)
	for i := 0; i < 2; i++ {
		if got := f.code(f.alice, name); got.code != http.StatusOK {
			t.Fatalf("code %d = %d %s", i, got.code, got.raw)
		}
	}
	f.code(f.bob, name) // refused: no entry

	var codes []model.AuditEvent
	err := f.h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("the ledger has no canonical reader")
		}
		return walker.WalkCanonical(context.Background(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if strings.Contains(canonical, vaultSeedB32) || strings.Contains(canonical, vaultText) {
				t.Fatalf("the ledger holds a secret: %s", canonical)
			}
			if event.Action == "secret.totp" {
				codes = append(codes, event)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 {
		t.Fatalf("secret.totp entries = %d, want 2", len(codes))
	}
	for _, ev := range codes {
		if !strings.Contains(ev.Actor, f.aliceID.String()) || ev.TargetID != f.entryID(name) || ev.TargetKind != "core.secret_entry" {
			t.Fatalf("code entry = %+v, want alice as actor and her item as target", ev)
		}
	}

	limited := 0
	for i := 0; i < 40; i++ {
		if got := f.code(f.alice, name); got.code == http.StatusTooManyRequests {
			limited++
			if got.hdr.Get("Retry-After") == "" {
				t.Fatalf("429 without Retry-After: %s", got.raw)
			}
		}
	}
	if limited == 0 {
		t.Fatal("42 codes in a burst were all admitted")
	}
}

// Any signed-in member can generate a password; nothing is stored and nothing is cached.
func TestVaultGenerateReturnsAPasswordToAMember(t *testing.T) {
	f := newVaultFixture(t)
	carol, _ := f.member("carol@vault.io")
	gen := func(token string, body any) resp {
		return f.h.do("POST", vaultGeneratePath, token, body, tenantHdr(f.tenant))
	}

	got := gen(carol, map[string]any{})
	pw, _ := got.body["password"].(string)
	if got.code != http.StatusOK || len(pw) != 20 || got.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("default generate = %d %s (Cache-Control %q), want 20 characters, no-store", got.code, got.raw, got.hdr.Get("Cache-Control"))
	}
	got = gen(carol, map[string]any{"length": 40, "symbols": false, "upper": false})
	pw, _ = got.body["password"].(string)
	if got.code != http.StatusOK || len(pw) != 40 || strings.Trim(pw, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("generate without symbols or capitals = %d %s", got.code, got.raw)
	}
	for what, body := range map[string]any{
		"too short":  map[string]any{"length": 3},
		"too long":   map[string]any{"length": 500},
		"no classes": map[string]any{"lower": false, "upper": false, "digits": false, "symbols": false},
	} {
		if bad := gen(carol, body); bad.code != http.StatusBadRequest {
			t.Fatalf("%s: generate = %d %s, want 400", what, bad.code, bad.raw)
		}
	}
	if anon := gen("", map[string]any{}); anon.code != http.StatusUnauthorized {
		t.Fatalf("anonymous generate = %d %s, want 401", anon.code, anon.raw)
	}
	if kept := listed(t, f.list(carol)); len(kept) != 0 {
		t.Fatalf("generating stored something: %v", kept)
	}
}

// The description a list shows is bounded on every write that can set it, a description-only
// edit (no value) included: every member may write to the organization's scope.
func TestVaultDescriptionIsBoundedOnEveryWrite(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	if got := f.put(f.alice, name, vaultLogin); got.code != http.StatusOK {
		t.Fatalf("put = %d %s", got.code, got.raw)
	}
	long := strings.Repeat("d", 1025)
	for what, body := range map[string]map[string]any{
		"with a value":     {"name": name, "value": vaultLogin, "description": long},
		"description only": {"name": name, "value": "", "description": long},
	} {
		if got := f.h.do("PUT", vaultScopePath, f.alice, body, tenantHdr(f.tenant)); got.code != http.StatusBadRequest {
			t.Fatalf("%s: put = %d %s, want 400", what, got.code, got.raw)
		}
	}
	ok := map[string]any{"name": name, "value": "", "description": strings.Repeat("d", 1024)}
	if got := f.h.do("PUT", vaultScopePath, f.alice, ok, tenantHdr(f.tenant)); got.code != http.StatusOK {
		t.Fatalf("a 1024-byte description = %d %s", got.code, got.raw)
	}
}

// The authenticator code asks for the deployment's step-up like reveal does.
func TestVaultCodeRequiresStepUp(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.h.requirePasskeyStepUp()
	f.h.elevate(f.alice)
	if got := f.put(f.alice, name, vaultLogin); got.code != http.StatusOK {
		t.Fatalf("put = %d %s", got.code, got.raw)
	}
	fresh := f.h.do("POST", "/v1/auth/login", "", map[string]any{"email": "alice@vault.io", "password": "fixturepass1"}, nil)
	aal1 := fresh.body["token"].(string)
	got := f.code(aal1, name)
	if got.code != http.StatusForbidden || got.body["error"].(map[string]any)["code"] != "step_up_required" || got.body["code"] != nil {
		t.Fatalf("code at AAL1 = %d %s, want 403 step_up_required", got.code, got.raw)
	}
	f.h.elevate(aal1)
	if got := f.code(aal1, name); got.code != http.StatusOK || got.body["code"] == nil {
		t.Fatalf("code after the step-up = %d %s", got.code, got.raw)
	}
}
