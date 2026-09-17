// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// newProbeWorkspaceID is a canonical id that names no row: the "known absence" arm.
func newProbeWorkspaceID() string { return model.NewID().String() }

// collectionScopeProbeModule is a three-route module that exists to measure the
// collection-scope capability at the two boundaries nothing else can reach: what the
// engine does with the DECLARED selector before it authorizes, and what it does to a
// plain sibling route mounted through the very same registrar.
type collectionScopeProbeModule struct{}

func (collectionScopeProbeModule) APINamespace() string { return "scopeprobe" }

func (collectionScopeProbeModule) Permissions() []auth.Permission {
	return []auth.Permission{"scopeprobe:probe:read", "scopeprobe:probe:admin"}
}

func (m collectionScopeProbeModule) APIRoutes(reg api.RouteRegistrar) {
	scoped, ok := reg.(api.CollectionScopeRouteRegistrar)
	if !ok {
		// The registrar the engine hands a module MUST carry the capability; a silent
		// fallback here would make the whole measurement vacuous.
		panic("api: the mounting registrar does not implement CollectionScopeRouteRegistrar")
	}
	declared := scoped.WithCollectionScope(api.CollectionScopeRef{WorkspaceQueryParam: "workspace_id"})
	declared.Handle("GET", "/scoped", "scopeprobe:probe:read", m.echo)
	// A route the caller's role does not grant: its denial is the public shape every
	// non-admissible scope must be indistinguishable from.
	declared.Handle("GET", "/denied", "scopeprobe:probe:admin", m.echo)
	// The control: same module, same seam, no declaration.
	reg.Handle("GET", "/plain", "scopeprobe:probe:read", m.echo)
}

// echo reports the workspace the route was AUTHORIZED against, which is the only way to
// observe from outside whether the decision carried one.
func (collectionScopeProbeModule) echo(w http.ResponseWriter, _ *http.Request, mc api.ModuleContext) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"workspace":"` + mc.Resource.WorkspaceID.String() + `"}`))
}

// TestCollectionScopeCapabilitySeedsTheDecisionAndOnlyItsOwnRoutes proves the four
// claims the capability makes, and each one is a claim an implementation could plausibly
// break in the other direction:
//
//  1. A DECLARED route is authorized against the CORROBORATED workspace. Without this
//     the whole increment is inert: modules/governance grants.go short-circuits scope
//     resolution on a resource with no id and no workspace, so no workspace-scoped grant
//     can match a collection whose decision carries neither.
//  2. Its selector is judged for FORM before anything is looked up, and a malformed one
//     is a client error rather than a refusal — it is decided from the request alone.
//  3. A KNOWN-ABSENT workspace and a DENIED caller are ONE public answer, byte for byte.
//     This is the ratified amendment: moving the resolver in front of the decision makes
//     its answers visible to a caller who has not been authorized, so an absent workspace
//     that answered differently from a denial would be an existence oracle.
//  4. A SIBLING route mounted through the same seam is untouched — no selector required,
//     no workspace in its decision. That is what keeps this a route's own declaration
//     rather than a namespace-wide rule, and it is the term an implementation that stored
//     the declaration on the shared registrar would break.
func TestCollectionScopeCapabilitySeedsTheDecisionAndOnlyItsOwnRoutes(t *testing.T) {
	h := newHarness(t, collectionScopeProbeModule{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scopeprobe")
	token := h.tenantToken(admin, tenant, "probe@scopeprobe.io")

	listed := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	if listed.code != http.StatusOK {
		t.Fatalf("list workspaces = %d %s", listed.code, listed.raw)
	}
	items, _ := listed.body["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("the tenant has no workspace to scope to: %s", listed.raw)
	}
	workspace, _ := items[0].(map[string]any)["id"].(string)
	if workspace == "" {
		t.Fatalf("workspace id absent from %s", listed.raw)
	}

	// (1) The declared route is authorized against the corroborated workspace, and the
	// handler can see it.
	ok := h.do("GET", "/v1/m/scopeprobe/scoped?workspace_id="+workspace, token, nil, tenantHdr(tenant))
	if ok.code != http.StatusOK {
		t.Fatalf("scoped route = %d %s", ok.code, ok.raw)
	}
	if got, _ := ok.body["workspace"].(string); got != workspace {
		t.Fatalf("scoped route was authorized against workspace %q, want the corroborated %q",
			got, workspace)
	}

	// (4) The sibling carries no selector requirement and no workspace in its decision.
	plain := h.do("GET", "/v1/m/scopeprobe/plain", token, nil, tenantHdr(tenant))
	if plain.code != http.StatusOK {
		t.Fatalf("plain sibling = %d %s", plain.code, plain.raw)
	}
	if got, _ := plain.body["workspace"].(string); got != "" {
		t.Fatalf("plain sibling was authorized against workspace %q; the declaration has "+
			"leaked onto a route that never asked for it", got)
	}

	// (2) Form before lookup: absent, repeated and non-canonical are all client errors.
	for name, query := range map[string]string{
		"absent":        "",
		"empty":         "?workspace_id=",
		"non canonical": "?workspace_id=not-a-uuid",
		"repeated":      "?workspace_id=" + workspace + "&workspace_id=" + workspace,
	} {
		bad := h.do("GET", "/v1/m/scopeprobe/scoped"+query, token, nil, tenantHdr(tenant))
		if bad.code != http.StatusBadRequest {
			t.Fatalf("%s selector = %d %s, want 400", name, bad.code, bad.raw)
		}
	}

	// (3) A known-absent workspace and a denied caller are the SAME public answer.
	absent := h.do("GET", "/v1/m/scopeprobe/scoped?workspace_id="+newProbeWorkspaceID(), token, nil, tenantHdr(tenant))
	denied := h.do("GET", "/v1/m/scopeprobe/denied?workspace_id="+workspace, token, nil, tenantHdr(tenant))
	if absent.code != http.StatusForbidden || denied.code != http.StatusForbidden {
		t.Fatalf("absent = %d %s / denied = %d %s, want both 403",
			absent.code, absent.raw, denied.code, denied.raw)
	}
	if absent.raw != denied.raw {
		t.Fatalf("an absent workspace answers %s while a denial answers %s: before admission "+
			"they must be indistinguishable, or the refusal reports which workspaces exist",
			absent.raw, denied.raw)
	}
	t.Logf("G1A_SCOPE_REFUSAL|absent=%d %s|denied=%d %s",
		absent.code, absent.raw, denied.code, denied.raw)
}
