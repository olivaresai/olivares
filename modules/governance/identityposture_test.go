// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// fakePosture is the read seam over the claude-api Admin connector's governance
// inventory. A nil slice with a nil error is the "wired but the org has none" case,
// which is DISTINCT from "not wired at all" (a nil provider) — the two must not
// render the same, which is what these tests pin.
type fakePosture struct {
	keys       []modelprovider.ExternalKeyRef
	workspaces []modelprovider.WorkspaceRef
	err        error
	keyCalls   int
	wsCalls    int
}

func (f *fakePosture) ExternalKeys(context.Context) ([]modelprovider.ExternalKeyRef, error) {
	f.keyCalls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]modelprovider.ExternalKeyRef(nil), f.keys...), nil
}

func (f *fakePosture) Workspaces(context.Context) ([]modelprovider.WorkspaceRef, error) {
	f.wsCalls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]modelprovider.WorkspaceRef(nil), f.workspaces...), nil
}

// postureHarness builds a harness whose identity console has the given posture
// provider wired (nil ⇒ deliberately unwired).
func postureHarness(t *testing.T, p governance.IdentityPostureProvider) (*harness, model.TenantID, string) {
	t.Helper()
	h, tenant, tok, _ := postureHarnessRoot(t, p)
	return h, tenant, tok
}

// postureHarnessRoot also returns the ROOT token, which is the only one that can mint
// the low-privilege users the permission tests need (a tenant admin cannot).
func postureHarnessRoot(t *testing.T, p governance.IdentityPostureProvider) (*harness, model.TenantID, string, string) {
	t.Helper()
	var opts []governance.IdentityConsoleOption
	if p != nil {
		opts = append(opts, governance.WithIdentityPostureProvider(p))
	}
	h := newHarnessWith(t, harnessOpts{identityOpts: opts})
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	return h, tenant, tok, root
}

func (h *harness) getPosture(path, token string, tenant model.TenantID) resp {
	h.t.Helper()
	return h.do("GET", path, token, nil, tenantHdr(tenant))
}

// THE ROUTE MUST EXIST AT ALL. This is the finding in one assertion: the typed
// console client calls these two paths and the engine registered neither, so an
// operator got a 404 while every mocked unit test stayed green.
func TestIdentityPostureRoutesAreRegistered(t *testing.T) {
	h, tenant, admin := postureHarness(t, &fakePosture{})
	for _, path := range []string{"/v1/m/identity/external-keys", "/v1/m/identity/residency"} {
		if r := h.getPosture(path, admin, tenant); r.code == http.StatusNotFound {
			t.Errorf("GET %s = 404: the console calls it and the engine does not register it", path)
		}
	}
}

// UNWIRED IS NOT EMPTY. With no Admin credential the answer is an explicit
// available=false plus a reason — never an empty inventory, which would read as
// "this org has no customer-managed keys" when the truth is "we could not look".
func TestExternalKeysUnwiredAnswersAvailableFalseWithAReason(t *testing.T) {
	h, tenant, admin := postureHarness(t, nil)
	r := h.getPosture("/v1/m/identity/external-keys", admin, tenant)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a declared-but-unwired posture is not an error): %s", r.code, r.raw)
	}
	if avail, _ := r.body["available"].(bool); avail {
		t.Error("available = true with no provider wired")
	}
	if reason, _ := r.body["reason"].(string); strings.TrimSpace(reason) == "" {
		t.Error("available=false with no reason: the console cannot tell the operator why")
	}
	items, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("items is not a JSON array (an empty page must serialize as [], never null): %s", r.raw)
	}
	if len(items) != 0 {
		t.Errorf("items = %d, want 0", len(items))
	}
}

func TestResidencyUnwiredAnswersAvailableFalseWithAReason(t *testing.T) {
	h, tenant, admin := postureHarness(t, nil)
	r := h.getPosture("/v1/m/identity/residency", admin, tenant)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", r.code, r.raw)
	}
	if avail, _ := r.body["available"].(bool); avail {
		t.Error("available = true with no provider wired")
	}
	if reason, _ := r.body["reason"].(string); strings.TrimSpace(reason) == "" {
		t.Error("available=false with no reason")
	}
}

// WIRED-AND-EMPTY IS A REAL ANSWER, distinct from unwired: available=true, zero items.
func TestExternalKeysWiredAndEmptyIsAvailableWithNoItems(t *testing.T) {
	h, tenant, admin := postureHarness(t, &fakePosture{})
	r := h.getPosture("/v1/m/identity/external-keys", admin, tenant)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d: %s", r.code, r.raw)
	}
	if avail, _ := r.body["available"].(bool); !avail {
		t.Errorf("available = false though the provider answered cleanly: %s", r.raw)
	}
	if reason, _ := r.body["reason"].(string); reason != "" {
		t.Errorf("reason = %q on an available inventory; a reason means we could NOT look", reason)
	}
}

func TestExternalKeysServesTheCollectedInventory(t *testing.T) {
	validated := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	p := &fakePosture{keys: []modelprovider.ExternalKeyRef{{
		ID: "ekey_01ABC", Provider: "aws_kms", Name: "prod-cmek", State: "active",
		LastValidatedAt: validated, InUse: true,
	}}}
	h, tenant, admin := postureHarness(t, p)
	r := h.getPosture("/v1/m/identity/external-keys", admin, tenant)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d: %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %s", len(items), r.raw)
	}
	got, _ := items[0].(map[string]any)
	// The JSON keys are the console's contract (web/src/features/identity/types.ts:351).
	for k, want := range map[string]any{
		"id": "ekey_01ABC", "provider": "aws_kms", "name": "prod-cmek",
		"state": "active", "in_use": true,
	} {
		if got[k] != want {
			t.Errorf("items[0].%s = %#v, want %#v", k, got[k], want)
		}
	}
	if got["last_validated_at"] != validated.Format(time.RFC3339) {
		t.Errorf("last_validated_at = %#v, want RFC3339 %s", got["last_validated_at"], validated.Format(time.RFC3339))
	}
	// A zero timestamp must be OMITTED, never emitted as year 1 — the console renders
	// "never" for an absent value and would print a fake date for a zero one.
	if _, present := got["created_at"]; present {
		t.Errorf("created_at present for a zero time: %#v", got["created_at"])
	}
}

func TestResidencyServesTheWorkspaceGovernanceObject(t *testing.T) {
	p := &fakePosture{workspaces: []modelprovider.WorkspaceRef{{
		ID: "wrkspc_1", Name: "prod", Geo: "us", ExternalKeyID: "ekey_01ABC",
		CompartmentID: "comp-9", Tags: map[string]string{"env": "prod"},
		Residency: &modelprovider.DataResidency{
			AllowedInferenceGeos: []string{"us", "eu"}, DefaultInferenceGeo: "us",
		},
	}}}
	h, tenant, admin := postureHarness(t, p)
	r := h.getPosture("/v1/m/identity/residency", admin, tenant)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d: %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %s", len(items), r.raw)
	}
	got, _ := items[0].(map[string]any)
	for k, want := range map[string]any{
		"id": "wrkspc_1", "name": "prod", "geo": "us",
		"external_key_id": "ekey_01ABC", "compartment_id": "comp-9",
	} {
		if got[k] != want {
			t.Errorf("items[0].%s = %#v, want %#v", k, got[k], want)
		}
	}
	dr, ok := got["data_residency"].(map[string]any)
	if !ok {
		t.Fatalf("data_residency missing or not an object: %s", r.raw)
	}
	geos, _ := dr["allowed_inference_geos"].([]any)
	if len(geos) != 2 || geos[0] != "us" || geos[1] != "eu" {
		t.Errorf("allowed_inference_geos = %#v, want [us eu]", geos)
	}
	if dr["default_inference_geo"] != "us" {
		t.Errorf("default_inference_geo = %#v, want us", dr["default_inference_geo"])
	}
}

// AN ARCHIVED WORKSPACE IS NOT A RESIDENCY SUBJECT. Listing it would show a CMEK gap
// for a workspace nobody can send inference to — a false posture finding.
func TestResidencyOmitsArchivedWorkspaces(t *testing.T) {
	p := &fakePosture{workspaces: []modelprovider.WorkspaceRef{
		{ID: "wrkspc_live", Name: "live"},
		{ID: "wrkspc_old", Name: "old", Archived: true},
	}}
	h, tenant, admin := postureHarness(t, p)
	r := h.getPosture("/v1/m/identity/residency", admin, tenant)
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (the archived workspace must be omitted): %s", len(items), r.raw)
	}
	if got, _ := items[0].(map[string]any); got["id"] != "wrkspc_live" {
		t.Errorf("items[0].id = %#v, want wrkspc_live", got["id"])
	}
}

// A FETCH FAULT DEGRADES, AND NEVER ECHOES THE ERROR. The connector error can embed
// the Admin endpoint and credential; it goes to the log, never to the browser.
func TestPostureFetchFailureDegradesWithoutLeakingTheError(t *testing.T) {
	secret := "https://api.anthropic.com?admin_key=sk-ant-admin-LEAK"
	for _, path := range []string{"/v1/m/identity/external-keys", "/v1/m/identity/residency"} {
		p := &fakePosture{err: errors.New("get " + secret + ": 401")}
		h, tenant, admin := postureHarness(t, p)
		r := h.getPosture(path, admin, tenant)
		if r.code != http.StatusOK {
			t.Errorf("%s status = %d, want 200 (a transient fault degrades, never 500s): %s", path, r.code, r.raw)
		}
		if avail, _ := r.body["available"].(bool); avail {
			t.Errorf("%s available = true after a fetch error", path)
		}
		if strings.Contains(r.raw, "sk-ant-admin") || strings.Contains(r.raw, "api.anthropic.com") {
			t.Errorf("%s response echoed the connector error verbatim: %s", path, r.raw)
		}
		if reason, _ := r.body["reason"].(string); strings.TrimSpace(reason) == "" {
			t.Errorf("%s degraded with no reason", path)
		}
	}
}

// THE PERMISSION MUST ACTUALLY GATE, AND IT MUST BE THE DECLARED ONE. lesson:
// a route whose guard is not the permission the module registers stops guarding without
// saying so. A built-in role cannot show this — governance:identity:read is read-tier, so
// even a viewer holds it — but an authored deny on exactly that resource+verb can: if the
// routes were mounted behind any OTHER permission, this deny would not match and they
// would still answer 200.
func TestIdentityPostureRoutesSitBehindTheDeclaredPermission(t *testing.T) {
	h, tenant, tok, root := postureHarnessRoot(t, &fakePosture{})
	// Control first: without the deny the routes answer.
	for _, path := range []string{"/v1/m/identity/external-keys", "/v1/m/identity/residency"} {
		if r := h.getPosture(path, tok, tenant); r.code != http.StatusOK {
			t.Fatalf("control GET %s = %d, want 200 before any deny: %s", path, r.code, r.raw)
		}
	}
	h.authorPolicy(root, tenant, "no-posture-reads", map[string]any{"rules": []any{
		map[string]any{"deny": true, "resource": "idposture", "verb": "read"},
	}})
	for _, path := range []string{"/v1/m/identity/external-keys", "/v1/m/identity/residency"} {
		if r := h.getPosture(path, tok, tenant); r.code != http.StatusForbidden {
			t.Errorf("GET %s under a deny on governance:idposture:read = %d, want 403 — the route is not behind the permission it declares: %s", path, r.code, r.raw)
		}
	}
}

// THE PROVIDER IS NOT CONSULTED WHEN THE CALLER IS DENIED — the deny happens before any
// Admin-API traffic, so a forbidden operator cannot make the engine call upstream.
func TestIdentityPostureDeniedCallerNeverReachesTheProvider(t *testing.T) {
	p := &fakePosture{}
	h, tenant, tok, root := postureHarnessRoot(t, p)
	h.authorPolicy(root, tenant, "no-posture-reads", map[string]any{"rules": []any{
		map[string]any{"deny": true, "resource": "idposture", "verb": "read"},
	}})
	before := p.keyCalls + p.wsCalls
	h.getPosture("/v1/m/identity/external-keys", tok, tenant)
	h.getPosture("/v1/m/identity/residency", tok, tenant)
	if got := p.keyCalls + p.wsCalls - before; got != 0 {
		t.Errorf("provider consulted %d time(s) for a denied caller", got)
	}
}

// THE READ IS AUDITED, ON EVERY ANSWER. Viewing which workspaces hold no
// customer-managed key names where data is provider-encrypted — recon-relevant, so it
// is audited deny-closed like the WIF graph beside it. The UNAVAILABLE answers are
// audited too: an operator asking and being told "not wired" is still an access attempt.
func TestIdentityPostureReadsAreAudited(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider governance.IdentityPostureProvider
	}{
		{"available", &fakePosture{}},
		{"unwired", nil},
		{"fetch failure", &fakePosture{err: errors.New("boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, tenant, admin := postureHarness(t, tc.provider)
			h.getPosture("/v1/m/identity/external-keys", admin, tenant)
			h.getPosture("/v1/m/identity/residency", admin, tenant)
			actions := h.auditActions(tenant)
			for _, want := range []string{
				"governance.identity.external_keys.read",
				"governance.identity.residency.read",
			} {
				if !contains(actions, want) {
					t.Errorf("no %s row in the ledger after the read (actions: %v)", want, actions)
				}
			}
		})
	}
}

// THE IDENTITY POSTURE IS A PRIVILEGED READ — editor and up, NEVER the lowest viewer
// role. Which workspaces hold no customer-managed key is NEGATIVE security posture: a map
// of where data is provider-encrypted, i.e. where the weak link is. That is the same
// recon concern that put the access graph and the authorization-query surface in
// privilegedReadPerms, and this route class belongs there for the same reason.
//
// THIS TEST IS THE MUTANT DETECTOR for that decision: drop governance:idposture:read back
// to the generic read tier (remove it from core/auth's privilegedReadPerms) and the
// viewer cases below go red by their own case, naming the role that regained the map.
func TestIdentityPostureIsAPrivilegedReadNotViewerTier(t *testing.T) {
	paths := []string{
		"/v1/m/identity/external-keys",
		"/v1/m/identity/residency",
		"/v1/m/identity/sso",
		"/v1/m/identity/wif",
	}
	h, tenant, _, root := postureHarnessRoot(t, &fakePosture{})
	_, viewer := h.roleUser(root, tenant, "viewer@x.io", auth.RoleViewer)
	_, editor := h.roleUser(root, tenant, "editor@x.io", auth.RoleEditor)

	for _, p := range paths {
		if r := h.getPosture(p, viewer, tenant); r.code != http.StatusForbidden {
			t.Errorf("VIEWER GET %s = %d, want 403: the lowest role must not read the "+
				"negative-CMEK/federation map (privilegedReadPerms)", p, r.code)
		}
		if r := h.getPosture(p, editor, tenant); r.code != http.StatusOK {
			t.Errorf("EDITOR GET %s = %d, want 200: the promotion must gate the viewer "+
				"only, not lock out the roles that need it", p, r.code)
		}
	}
}

// THE ROSTER STAYS VIEWER-READABLE; ONLY THE ROUTE THAT CARRIES A POSTURE ATTRIBUTE
// MOVES. Measured field by field rather than promoted by analogy:
//
//	/identities            roster.go:413-421  id, ref, name, kind, source,
//	                                          principal_type, disabled — WHO EXISTS
//	/groups                roster.go:470-475  ref, kind, display_name, source
//	/groups/{ref}/members  roster.go:514-518  member_ref, member_kind, via
//	/bindings              identity.go:281-288 …plus SHARED and AGENT_COUNT
//
// Only the last carries a posture attribute: an identity bound to N agents is lost
// attribution, and the product itself treats it as one (TestBindingSharedIdentityFinding,
// roster_identity_test.go:218, emits a FINDING for exactly that). `disabled` is NOT
// posture — it is lifecycle, and its polarity runs the other way: disabled=true is the
// SAFER account, so it names no missing control.
//
// The field is not gated separately because that would be theater: handleListBindings
// returns EVERY binding and computes the counts from that same set (identity.go:301-316),
// so a caller holding the list can derive `shared` by grouping. The route is the unit.
func TestRosterStaysViewerReadableExceptTheBindingTopology(t *testing.T) {
	h, tenant, _, root := postureHarnessRoot(t, &fakePosture{})
	_, viewer := h.roleUser(root, tenant, "rosterviewer@x.io", auth.RoleViewer)
	_, editor := h.roleUser(root, tenant, "rostereditor@x.io", auth.RoleEditor)

	// Seeing who exists is a legitimate governance-viewer function. Removing it would
	// be cutting scope, not hardening.
	for _, p := range []string{
		"/v1/m/governance/identities",
		"/v1/m/governance/groups",
	} {
		if r := h.getPosture(p, viewer, tenant); r.code != http.StatusOK {
			t.Errorf("VIEWER GET %s = %d, want 200: the subject INVENTORY is not posture", p, r.code)
		}
	}
	// The binding topology carries `shared`/`agent_count` — a missing control.
	if r := h.getPosture("/v1/m/governance/bindings", viewer, tenant); r.code != http.StatusForbidden {
		t.Errorf("VIEWER GET /bindings = %d, want 403: it carries shared/agent_count, which "+
			"names an identity used by several agents — lost attribution", r.code)
	}
	if r := h.getPosture("/v1/m/governance/bindings", editor, tenant); r.code != http.StatusOK {
		t.Errorf("EDITOR GET /bindings = %d, want 200", r.code)
	}
}
