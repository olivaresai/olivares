// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// searchDemoModule contributes two kinds to the federated search: a read-tier
// kind any viewer passes and an admin-tier kind only admins pass — the pair
// that proves per-kind deny-closed gating.
type searchDemoModule struct {
	failThing bool
	thingHits int
}

func (searchDemoModule) APINamespace() string { return "searchdemo" }
func (searchDemoModule) Permissions() []auth.Permission {
	return []auth.Permission{"searchdemo:thing:read", "searchdemo:vault:admin"}
}
func (searchDemoModule) APIRoutes(reg api.RouteRegistrar) {}

func (m searchDemoModule) SearchKinds() []api.SearchKind {
	return []api.SearchKind{
		{
			Kind:       "searchdemo.thing",
			Permission: "searchdemo:thing:read",
			Search: func(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
				if m.failThing {
					return nil, errors.New("thing provider down")
				}
				n := m.thingHits
				if n == 0 {
					n = 1
				}
				out := make([]api.SearchResult, 0, n)
				for i := 0; i < n && (limit <= 0 || i < limit); i++ {
					out = append(out, api.SearchResult{
						Kind: "searchdemo.thing", ID: "t" + string(rune('1'+i)), Name: "Payment Thing",
					})
				}
				return out, nil
			},
		},
		{
			Kind:       "searchdemo.vault",
			Permission: "searchdemo:vault:admin",
			Search: func(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
				return []api.SearchResult{{Kind: "searchdemo.vault", ID: "v1", Name: "Payment Vault"}}, nil
			},
		},
	}
}

// resultKinds collects the kind of every result in a search response.
func resultKinds(t *testing.T, r resp) map[string]int {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("search = %d %s", r.code, r.raw)
	}
	kinds := map[string]int{}
	for _, it := range r.body["results"].([]any) {
		kinds[it.(map[string]any)["kind"].(string)]++
	}
	return kinds
}

func TestSearchFederatedRBAC(t *testing.T) {
	h := newHarness(t, searchDemoModule{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	mkUser := func(email, pass, role string) string {
		r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
		}
		uid := r.body["id"].(string)
		if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); g.code != http.StatusCreated {
			t.Fatalf("grant %s = %d %s", email, g.code, g.raw)
		}
		lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
		if lr.code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
		}
		return lr.body["token"].(string)
	}
	viewer := mkUser("viewer@acme.com", "viewerpass1234", string(auth.RoleViewer))
	boss := mkUser("boss-payton@acme.com", "bosspass1234", string(auth.RoleAdmin))

	// A workspace whose name matches the query (workspace create is AAL3).
	h.elevate(admin)
	if r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Payments Platform", "slug": "payments"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}

	// Superadmin: passes every kind — module read + admin kinds and the workspace.
	kinds := resultKinds(t, h.do("GET", "/v1/search?q=payment", admin, nil, tenantHdr(tenant)))
	for _, want := range []string{"searchdemo.thing", "searchdemo.vault", "workspace"} {
		if kinds[want] == 0 {
			t.Fatalf("superadmin search missing kind %s: %v", want, kinds)
		}
	}

	// Viewer: read-tier kinds only — the admin-tier vault kind is silently
	// absent (deny-closed), and user results need user:read (admin-tier).
	kinds = resultKinds(t, h.do("GET", "/v1/search?q=payment", viewer, nil, tenantHdr(tenant)))
	if kinds["searchdemo.thing"] == 0 || kinds["workspace"] == 0 {
		t.Fatalf("viewer search missing read-tier kinds: %v", kinds)
	}
	if kinds["searchdemo.vault"] != 0 {
		t.Fatalf("viewer search leaked admin-tier kind: %v", kinds)
	}
	if k := resultKinds(t, h.do("GET", "/v1/search?q=payton", viewer, nil, tenantHdr(tenant))); k["user"] != 0 {
		t.Fatalf("viewer search leaked user kind without user:read: %v", k)
	}

	// Tenant admin: sees the member roster kind but NOT the deployment-wide
	// connector kind (system:admin at the system tenant).
	kinds = resultKinds(t, h.do("GET", "/v1/search?q=payton", boss, nil, tenantHdr(tenant)))
	if kinds["user"] == 0 {
		t.Fatalf("admin search missing user kind: %v", kinds)
	}
	if kinds["connector"] != 0 {
		t.Fatalf("tenant admin search leaked the system connector kind: %v", kinds)
	}

	// Query validation and authentication.
	if r := h.do("GET", "/v1/search?q=payment", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated search = %d, want 401", r.code)
	}
	if r := h.do("GET", "/v1/search", boss, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("empty query = %d, want 400", r.code)
	}
	long := make([]byte, 101)
	for i := range long {
		long[i] = 'a'
	}
	if r := h.do("GET", "/v1/search?q="+string(long), boss, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("oversized query = %d, want 400", r.code)
	}
}

// TestSearchUsersWorkspaceConfinement is the regression guard: the /v1/search
// user kind must confine EXACTLY as its dedicated list endpoint (/v1/members) does.
// A workspace-CONFINED admin holds user:read but only within its workspace — it may
// find the members OF its workspace via ⌘K, yet must NOT enumerate another
// workspace's users (incl. emails), which is reconnaissance-sensitive cross-workspace
// PII. A tenant-wide admin still finds everyone (no regression). Because handleSearch
// ignores ?kind and matches only ?q, the assertions drive the leak through ?q against
// per-workspace unique email local-parts, counting only "user"-kind hits.
func TestSearchUsersWorkspaceConfinement(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Two workspaces. Minting a workspace is owner/superadmin-only and AAL3-gated.
	h.elevate(admin)
	mkWS := func(name, slug string) string {
		r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": name, "slug": slug}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create workspace %s = %d %s", slug, r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	wsA := mkWS("Alpha", "alpha")
	wsB := mkWS("Beta", "beta")

	// mkUser creates a user, grants a membership (confined to wsID when non-empty), and
	// returns the user's session token.
	mkUser := func(email, pass, role, wsID string) string {
		r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
		}
		uid := r.body["id"].(string)
		grant := map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}
		if wsID != "" {
			grant["workspace_id"] = wsID
		}
		if g := h.do("POST", "/v1/memberships", admin, grant, nil); g.code != http.StatusCreated {
			t.Fatalf("grant %s = %d %s", email, g.code, g.raw)
		}
		lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
		if lr.code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
		}
		return lr.body["token"].(string)
	}

	// One member per workspace, each with a UNIQUE email local-part to search by.
	mkUser("alpha-uniq-aaa@acme.com", "alphapass1234", auth.RoleEditor, wsA)
	mkUser("bravo-uniq-bbb@acme.com", "bravopass1234", auth.RoleEditor, wsB)
	// The searchers: a WS-A-confined admin (user:read, confined to wsA) and a
	// tenant-wide admin (unconfined).
	confinedA := mkUser("confined-a@acme.com", "confinedpass1234", auth.RoleAdmin, wsA)
	tenantWide := mkUser("wide-admin@acme.com", "widepass1234", auth.RoleAdmin, "")

	userHits := func(token, q string) int {
		return resultKinds(t, h.do("GET", "/v1/search?q="+q, token, nil, tenantHdr(tenant)))["user"]
	}

	// THE FIX: a WS-A-confined admin must NOT surface the WS-B user via search.
	if n := userHits(confinedA, "bravo-uniq-bbb"); n != 0 {
		t.Fatalf("WS-A-confined admin leaked a WS-B user via ⌘K search: %d user hits, want 0", n)
	}
	// NOT over-filtered: the confined admin still finds its OWN workspace's member,
	// exactly as /v1/members would return it — proving this is a workspace filter, not
	// a blanket denial that would mask the leak.
	if n := userHits(confinedA, "alpha-uniq-aaa"); n == 0 {
		t.Fatalf("WS-A-confined admin cannot find a WS-A member (over-filtered): want >=1")
	}
	// NO REGRESSION: an unconfined tenant-wide admin still finds the WS-B user.
	if n := userHits(tenantWide, "bravo-uniq-bbb"); n == 0 {
		t.Fatalf("tenant-wide admin cannot find the WS-B user (regression): want >=1")
	}
}

func TestSearchProviderFailureDegradesKindOnly(t *testing.T) {
	h := newHarness(t, searchDemoModule{failThing: true})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/search?q=payment", admin, nil, tenantHdr(tenant))
	kinds := resultKinds(t, r)
	if kinds["searchdemo.thing"] != 0 {
		t.Fatalf("failing provider still returned results: %v", kinds)
	}
	if kinds["searchdemo.vault"] == 0 {
		t.Fatalf("healthy kind lost when a sibling provider failed: %v", kinds)
	}

	// ...AND THE CALLER IS TOLD (2026-08-06). This test pinned "degrades the kind only" and
	// stopped there, so for its whole life the response could — and did — come back with
	// `truncated:false` and no other signal: an INCOMPLETE list published as an exhaustive
	// one, with the only party able to act on the gap being whoever was reading the server's
	// WARN log rather than whoever was holding the half-empty list.
	if r.body["degraded"] != true {
		t.Errorf("a failed provider left degraded=%v; the response claims a completeness it does not have: %s", r.body["degraded"], r.raw)
	}
	// `truncated` must NOT be borrowed to mean this. They are different answers: one says
	// "narrow your query", the other says "a source failed". A client that cannot tell them
	// apart cannot decide between retrying and escalating.
	if r.body["truncated"] == true {
		t.Errorf("a failed provider set truncated; that is the OTHER answer: %s", r.raw)
	}
	// And WHICH kind, because "something failed" is not actionable.
	names, _ := r.body["degraded_kinds"].([]any)
	if len(names) != 1 || names[0] != "searchdemo.thing" {
		t.Errorf("degraded_kinds = %v, want exactly [searchdemo.thing]: %s", names, r.raw)
	}
}

// The other direction, which is what stops `degraded` becoming a field that is always true:
// a search where every provider answered must say so, and must carry the empty list rather
// than omitting it — an absent field and an empty one would otherwise be indistinguishable
// to a client that checks for presence.
func TestSearchWithNoFailureIsNotDegraded(t *testing.T) {
	h := newHarness(t, searchDemoModule{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/search?q=payment", admin, nil, tenantHdr(tenant))
	if r.body["degraded"] != false {
		t.Errorf("degraded = %v on a healthy search; a flag that is always set is not a signal: %s", r.body["degraded"], r.raw)
	}
	names, ok := r.body["degraded_kinds"].([]any)
	if !ok {
		t.Errorf("degraded_kinds is absent (%T); an omitted list and an empty one must not be told apart by presence: %s", r.body["degraded_kinds"], r.raw)
	}
	if len(names) != 0 {
		t.Errorf("degraded_kinds = %v on a healthy search, want empty: %s", names, r.raw)
	}
}

func TestSearchPerKindCapSetsTruncated(t *testing.T) {
	h := newHarness(t, searchDemoModule{thingHits: 9})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/search?q=payment", admin, nil, tenantHdr(tenant))
	kinds := resultKinds(t, r)
	if kinds["searchdemo.thing"] != 5 {
		t.Fatalf("per-kind cap not applied: %v", kinds)
	}
	if r.body["truncated"] != true {
		t.Fatalf("truncated flag not set: %s", r.raw)
	}
}

// dupKindModule declares a kind that collides with a core kind — the server
// must refuse to build rather than let one kind shadow another.
type dupKindModule struct{}

func (dupKindModule) APINamespace() string             { return "dupdemo" }
func (dupKindModule) Permissions() []auth.Permission   { return []auth.Permission{"dupdemo:x:read"} }
func (dupKindModule) APIRoutes(reg api.RouteRegistrar) {}
func (dupKindModule) SearchKinds() []api.SearchKind {
	return []api.SearchKind{{
		Kind: "workspace", Permission: "dupdemo:x:read",
		Search: func(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
			return nil, nil
		},
	}}
}

// permlessKindModule declares a kind with no permission — deny-closed means it
// must be rejected at mount, never silently searchable.
type permlessKindModule struct{}

func (permlessKindModule) APINamespace() string { return "permless" }
func (permlessKindModule) Permissions() []auth.Permission {
	return []auth.Permission{"permless:x:read"}
}
func (permlessKindModule) APIRoutes(reg api.RouteRegistrar) {}
func (permlessKindModule) SearchKinds() []api.SearchKind {
	return []api.SearchKind{{
		Kind: "permless.thing",
		Search: func(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
			return nil, nil
		},
	}}
}

func TestSearchKindValidationAtMount(t *testing.T) {
	for name, mod := range map[string]api.Module{
		"duplicate kind":  dupKindModule{},
		"permission-less": permlessKindModule{},
	} {
		if err := buildServerErr(t, mod); err == nil {
			t.Fatalf("%s: api.New accepted an invalid search kind", name)
		}
	}
}

// buildServerErr builds a server exactly like newHarness but returns api.New's
// error instead of failing the test, so mount-time validation can be asserted.
func buildServerErr(t *testing.T, modules ...api.Module) error {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(context.Background()); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	if _, _, err := tok.Ensure(); err != nil {
		t.Fatal(err)
	}
	_, err = api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: modules,
	})
	return err
}
