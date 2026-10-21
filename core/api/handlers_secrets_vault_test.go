// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	vaultScopePath  = "/v1/console/secrets?scope=tenant"
	vaultRevealPath = "/v1/console/secrets/reveal?scope=tenant"
	// vaultText is the secret inside the item; a leak check looks for it. vaultValue is
	// the item document the engine seals and reveals; bobValue is another person's.
	vaultText  = "vault-fixture-value-0123"
	vaultValue = `{"type":"note","note":"` + vaultText + `"}`
	bobValue   = `{"type":"note","note":"bob-` + vaultText + `"}`
)

// vaultFixture is one tenant with two tenant admins (alice, bob) and the platform
// superadmin, each holding a session.
type vaultFixture struct {
	h       *harness
	root    string
	tenant  model.TenantID
	alice   string
	bob     string
	aliceID model.ID
	bobID   model.ID
}

func newVaultFixture(t *testing.T) vaultFixture {
	t.Helper()
	h := newSecretsHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "vault-co")
	alice := h.mkMember(root, "alice@vault.io", "fixturepass1", auth.RoleAdmin, tenant)
	bob := h.mkMember(root, "bob@vault.io", "fixturepass1", auth.RoleAdmin, tenant)
	return vaultFixture{
		h: h, root: root, tenant: tenant, alice: alice, bob: bob,
		aliceID: h.principalOf(alice).UserID, bobID: h.principalOf(bob).UserID,
	}
}

func (f vaultFixture) item(owner model.ID, label string) string {
	return "vault/" + owner.String() + "/" + label
}

func (f vaultFixture) put(token, name, value string) resp {
	f.h.t.Helper()
	return f.h.do("PUT", vaultScopePath, token, map[string]any{"name": name, "value": value}, tenantHdr(f.tenant))
}

func (f vaultFixture) reveal(token, name string) resp {
	f.h.t.Helper()
	return f.h.do("POST", vaultRevealPath, token, map[string]any{"name": name}, tenantHdr(f.tenant))
}

// entryID is the stored secret entry behind a tenant-scope name.
func (f vaultFixture) entryID(name string) model.ID {
	f.h.t.Helper()
	var id model.ID
	err := f.h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		rows, _, err := as.Secrets().List(context.Background(), model.Query{Filters: []model.Filter{
			{Column: "scope", Op: model.OpEq, Value: f.tenant.String()},
			{Column: "name", Op: model.OpEq, Value: name},
		}, Limit: 1})
		if err == nil && len(rows) == 1 {
			id = rows[0].ID
		}
		return err
	})
	if err != nil || id.IsZero() {
		f.h.t.Fatalf("no secret entry for %s: %v", name, err)
	}
	return id
}

// An owner reveals her own item; nobody else gets it, whatever their authority.
func TestVaultRevealIsOwnerOnly(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	if got := f.put(f.alice, name, vaultValue); got.code != http.StatusOK {
		t.Fatalf("owner put = %d %s", got.code, got.raw)
	}

	got := f.reveal(f.alice, name)
	if got.code != http.StatusOK || got.body["value"] != vaultValue || got.body["name"] != name {
		t.Fatalf("owner reveal = %d %s", got.code, got.raw)
	}
	if cc := got.hdr.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("reveal Cache-Control = %q, want no-store", cc)
	}

	issued := f.h.do("POST", "/v1/tokens", f.alice, map[string]any{"name": "ci", "tenant": f.tenant.String(), "role": auth.RoleAdmin}, nil)
	if issued.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", issued.code, issued.raw)
	}
	apiToken := issued.body["token"].(string)

	// A token is stopped by the step-up, the others by the owner rule: pin both causes.
	for who, c := range map[string]struct{ token, code string }{
		"another tenant admin":  {f.bob, "forbidden"},
		"the superadmin":        {f.root, "forbidden"},
		"the owner's API token": {apiToken, "step_up_required"},
	} {
		denied := f.reveal(c.token, name)
		if denied.code != http.StatusForbidden || errCode(denied.body) != c.code || strings.Contains(denied.raw, vaultText) {
			t.Fatalf("%s reveal = %d %s, want 403 %s without the value", who, denied.code, denied.raw, c.code)
		}
	}
	// Nobody writes into, lists or deletes in somebody else's namespace either.
	if got := f.put(f.bob, f.item(f.aliceID, "planted"), bobValue); got.code != http.StatusForbidden {
		t.Fatalf("put into another namespace = %d %s, want 403", got.code, got.raw)
	}
	if got := f.put(f.root, f.item(f.aliceID, "planted"), vaultValue); got.code != http.StatusForbidden {
		t.Fatalf("superadmin put into another namespace = %d %s, want 403", got.code, got.raw)
	}
	if got := f.h.do("DELETE", vaultScopePath, f.bob, map[string]any{"name": name}, tenantHdr(f.tenant)); got.code != http.StatusForbidden {
		t.Fatalf("delete in another namespace = %d %s, want 403", got.code, got.raw)
	}
	for who, token := range map[string]string{"bob": f.bob, "root": f.root, "the owner's API token": apiToken} {
		listed := f.h.do("GET", vaultScopePath, token, nil, tenantHdr(f.tenant))
		if listed.code != http.StatusOK || strings.Contains(listed.raw, name) {
			t.Fatalf("%s list = %d %s, want no vault item of alice", who, listed.code, listed.raw)
		}
	}
	own := f.h.do("GET", vaultScopePath, f.alice, nil, tenantHdr(f.tenant))
	if own.code != http.StatusOK || !strings.Contains(own.raw, name) || strings.Contains(own.raw, vaultText) {
		t.Fatalf("owner list = %d %s", own.code, own.raw)
	}
	if again := f.reveal(f.alice, name); again.code != http.StatusOK || again.body["value"] != vaultValue {
		t.Fatalf("owner reveal after the refusals = %d %s", again.code, again.raw)
	}
}

// Reveal is for personal vault items only: every other name, in either scope, stays
// write-only, and a malformed vault name is not a vault item.
func TestVaultRevealRefusesEveryOtherNamespace(t *testing.T) {
	f := newVaultFixture(t)
	for _, name := range []string{"mcp/github", "env/GITHUB_TOKEN"} {
		if got := f.put(f.alice, name, "tenant-"+vaultValue); got.code != http.StatusOK {
			t.Fatalf("seed %s = %d %s", name, got.code, got.raw)
		}
		denied := f.reveal(f.alice, name)
		if denied.code != http.StatusForbidden || strings.Contains(denied.raw, vaultText) {
			t.Fatalf("reveal %s = %d %s, want 403", name, denied.code, denied.raw)
		}
	}
	for _, name := range []string{"vault/" + f.aliceID.String(), "vault/" + f.aliceID.String() + "/", "vault/" + f.aliceID.String() + "x/a"} {
		denied := f.reveal(f.alice, name)
		if denied.code != http.StatusForbidden {
			t.Fatalf("reveal %q = %d %s, want 403", name, denied.code, denied.raw)
		}
	}

	// The deployment-wide store: a name that looks like a vault item is still global.
	svc := auth.NewSecretStore(f.h.st, fakeSealer{})
	actor := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true}
	globalName := f.item(f.aliceID, "lookalike")
	if _, err := svc.Put(context.Background(), actor, auth.GlobalSecretScope, globalName, "global-"+vaultValue, ""); err != nil {
		t.Fatal(err)
	}
	f.h.elevate(f.root)
	for where, path := range map[string]string{"global scope": "/v1/console/secrets/reveal", "tenant scope": vaultRevealPath} {
		denied := f.h.do("POST", path, f.root, map[string]any{"name": globalName}, tenantHdr(f.tenant))
		if denied.code != http.StatusForbidden || strings.Contains(denied.raw, vaultText) {
			t.Fatalf("superadmin reveal of the global lookalike in %s = %d %s, want 403", where, denied.code, denied.raw)
		}
	}
	// A tenant admin reaches neither the global scope nor, there, her own namespace.
	if got := f.h.do("POST", "/v1/console/secrets/reveal", f.alice, map[string]any{"name": f.item(f.aliceID, "bank")}, nil); got.code != http.StatusForbidden {
		t.Fatalf("tenant admin reveal in the global scope = %d %s, want 403", got.code, got.raw)
	}
	if got := f.reveal(f.alice, f.item(f.aliceID, "missing")); got.code != http.StatusNotFound {
		t.Fatalf("reveal of an item that does not exist = %d %s, want 404", got.code, got.raw)
	}
}

// Reveal asks for the deployment's step-up like every other secret write.
func TestVaultRevealRequiresStepUp(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.h.requirePasskeyStepUp()
	f.h.elevate(f.alice)
	if got := f.put(f.alice, name, vaultValue); got.code != http.StatusOK {
		t.Fatalf("put = %d %s", got.code, got.raw)
	}
	fresh := f.h.do("POST", "/v1/auth/login", "", map[string]any{"email": "alice@vault.io", "password": "fixturepass1"}, nil)
	aal1 := fresh.body["token"].(string)
	got := f.reveal(aal1, name)
	if got.code != http.StatusForbidden || got.body["error"].(map[string]any)["code"] != "step_up_required" ||
		strings.Contains(got.raw, vaultText) {
		t.Fatalf("reveal at AAL1 = %d %s, want 403 step_up_required", got.code, got.raw)
	}
	f.h.elevate(aal1)
	if got := f.reveal(aal1, name); got.code != http.StatusOK || got.body["value"] != vaultValue {
		t.Fatalf("reveal after the step-up = %d %s", got.code, got.raw)
	}
}

// Every reveal is one ledger entry naming the actor and the item, never the value.
func TestVaultRevealIsAuditedWithoutTheValue(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.put(f.alice, name, vaultValue)
	for i := 0; i < 2; i++ {
		if got := f.reveal(f.alice, name); got.code != http.StatusOK {
			t.Fatalf("reveal %d = %d %s", i, got.code, got.raw)
		}
	}
	f.reveal(f.bob, name) // refused: must leave no reveal entry

	var reveals []model.AuditEvent
	err := f.h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("the ledger has no canonical reader")
		}
		return walker.WalkCanonical(context.Background(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if strings.Contains(canonical, vaultText) {
				t.Fatalf("the ledger holds the value: %s", canonical)
			}
			if event.Action == "secret.reveal" {
				reveals = append(reveals, event)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reveals) != 2 {
		t.Fatalf("secret.reveal entries = %d, want 2 (one per successful reveal)", len(reveals))
	}
	entry := f.entryID(name)
	for _, ev := range reveals {
		if !strings.Contains(ev.Actor, f.aliceID.String()) || ev.TargetID != entry || ev.TargetKind != "core.secret_entry" || ev.OccurredAt.IsZero() {
			t.Fatalf("reveal entry = %+v, want alice as actor, entry %s as target and a time", ev, entry)
		}
	}
}

// A guessing or scraping session runs out of reveals.
func TestVaultRevealIsRateLimited(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.put(f.alice, name, vaultValue)
	limited := 0
	for i := 0; i < 40; i++ {
		got := f.reveal(f.alice, name)
		if got.code == http.StatusTooManyRequests {
			limited++
			if got.hdr.Get("Retry-After") == "" || strings.Contains(got.raw, vaultText) {
				t.Fatalf("429 = %s (Retry-After %q)", got.raw, got.hdr.Get("Retry-After"))
			}
			continue
		}
		if got.code != http.StatusOK {
			t.Fatalf("reveal %d = %d %s", i, got.code, got.raw)
		}
	}
	if limited == 0 {
		t.Fatal("40 reveals in a burst were all admitted")
	}
	// The budget is per person: bob's own reveals are untouched.
	bobName := f.item(f.bobID, "mail")
	f.put(f.bob, bobName, bobValue)
	if got := f.reveal(f.bob, bobName); got.code != http.StatusOK {
		t.Fatalf("bob reveal after alice's burst = %d %s", got.code, got.raw)
	}
}

// Spelling tricks do not reach another person's item, a deleted item is gone, and a
// tenant the caller does not belong to has no vault for her.
func TestVaultRevealNameVariantsAndLifecycle(t *testing.T) {
	f := newVaultFixture(t)
	bobName := f.item(f.bobID, "mail")
	f.put(f.bob, bobName, bobValue)
	aliceName := f.item(f.aliceID, "bank")
	f.put(f.alice, aliceName, vaultValue)

	for _, name := range []string{
		strings.ToUpper(bobName),
		"VAULT/" + f.aliceID.String() + "/bank",
		"vault/" + strings.ToUpper(f.aliceID.String()) + "/bank",
		"vault/" + f.aliceID.String() + "/../" + f.bobID.String() + "/mail",
		"vault/" + f.aliceID.String() + "//bank",
		bobName,
	} {
		got := f.reveal(f.alice, name)
		if got.code == http.StatusOK || strings.Contains(got.raw, "bob-"+vaultText) || strings.Contains(got.raw, vaultText) {
			t.Fatalf("alice revealed %q = %d %s", name, got.code, got.raw)
		}
	}

	other := f.h.createOrg(f.root, "other-co")
	if got := f.h.do("POST", vaultRevealPath, f.alice, map[string]any{"name": aliceName}, tenantHdr(other)); got.code != http.StatusForbidden {
		t.Fatalf("reveal through a tenant she is not in = %d %s, want 403", got.code, got.raw)
	}

	if got := f.h.do("DELETE", vaultScopePath, f.alice, map[string]any{"name": aliceName}, tenantHdr(f.tenant)); got.code != http.StatusNoContent {
		t.Fatalf("owner delete = %d %s", got.code, got.raw)
	}
	if got := f.reveal(f.alice, aliceName); got.code != http.StatusNotFound {
		t.Fatalf("reveal after delete = %d %s, want 404", got.code, got.raw)
	}
}
