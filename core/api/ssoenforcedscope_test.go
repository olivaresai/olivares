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
)

// THE GREEN BADGE ON AN INERT CONTROL (C1).
//
// ssoEnforcedBy answered a BUILD question — "is a LoginPolicy wired?" — and the console
// printed the answer directly above each IdP row's OWN stored posture, where it reads as
// "this posture is enforced". The list handler computed it ONCE and stamped it onto every
// row. But the posture the enterprise engine actually reads is
// FederationService.Posture, which takes no scope and resolves exactly ONE row:
// (GlobalFederationScope, "default"). So a per-tenant IdP, or a second global IdP under
// another alias, showed a green "Enforced by this build" over a require-SSO and an IP
// allow-list that nothing anywhere would ever apply.
//
// That is the expensive class: not a missing control, a control REPORTED ACTIVE while
// inert. An operator who reads it stops looking.
//
// THE FIX IS NOT TO SAY "unavailable" EITHER. That would deny enforcement the build
// genuinely performs and send the operator to rebuild with a tag that changes nothing.
// The build capability and the scope the posture covers are independent facts; only their
// conjunction is "enforced", and where the conjunction cannot be established the row says
// so. Three answers, never two — clean / broken / could not look.

// enforcingPolicy is a wired LoginPolicy. Its DECISIONS are irrelevant here: the only
// thing under test is that a policy is wired, which is what EnforcesLogin() reports and
// what used to be the whole of the enforced_by answer.
type enforcingPolicy struct{}

func (enforcingPolicy) AllowNetwork(context.Context, string) error   { return nil }
func (enforcingPolicy) RequireSSO(context.Context, model.User) error { return nil }

// TestEnforcedByIsPerRowAndNeverClaimsAnUnreadPosture is the guard for C1.
//
// It asserts over the REAL HTTP surface (not the helper alone) because the defect had two
// independent halves: a signal that ignored its row, and a list handler that hoisted one
// value out of the loop. A unit test on the helper would have caught only the first.
func TestEnforcedByIsPerRowAndNeverClaimsAnUnreadPosture(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)

	// Wire the enterprise capability: from here on EnforcesLogin() is true, which is
	// exactly the state in which the old code answered "enterprise" for every row.
	h.authr.WithLoginPolicy(enforcingPolicy{})

	posture := map[string]any{
		"require_sso":       true,
		"network_allowlist": []string{"10.0.0.0/8"},
	}
	// noPosture is what a row the engine does not read can be saved with: the PUT refuses a
	// set require_sso or network_allowlist there (TestPostureOnAnUnreadRowIsRefused), and
	// this test measures the enforced_by signal, not the stored posture.
	noPosture := map[string]any{"require_sso": false, "network_allowlist": []string{}}
	put := func(path string, extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{
			"protocol": "oidc", "enabled": false,
			"oidc_issuer": "https://idp.example", "oidc_client_id": "cid",
			"oidc_client_secret": "shhh-secret",
		}
		for k, v := range posture {
			body[k] = v
		}
		for k, v := range extra {
			body[k] = v
		}
		r := h.do("PUT", path, admin, body, nil)
		if r.code != http.StatusOK {
			t.Fatalf("put %s = %d %s", path, r.code, r.raw)
		}
		return r.body
	}

	tenant := h.createOrg(admin, "acme")

	// (1) THE ONE ROW THE POSTURE READER RESOLVES: global scope, "default" alias.
	// This must still say "enterprise" — a fix that reported "out_of_scope" everywhere would
	// pass every "is it not enterprise?" assertion below while destroying the signal.
	if got := put("/v1/console/sso", nil)["enforced_by"]; got != "enterprise" {
		t.Fatalf("global default IdP enforced_by = %v, want \"enterprise\" — this row IS the one "+
			"FederationService.Posture resolves, so denying it would make the signal useless", got)
	}

	// (2) A PER-TENANT IdP. Its posture is stored on its own row and Posture() never
	// reads it. Claiming "enterprise" here is the false green badge.
	tenantPath := "/v1/console/sso/tenants/" + tenant.String()
	if got := put(tenantPath, noPosture)["enforced_by"]; got != "out_of_scope" {
		t.Fatalf("per-tenant IdP enforced_by = %v, want \"out_of_scope\": its require_sso and allow-list are "+
			"stored on a row FederationService.Posture never resolves (it reads the global default only)", got)
	}

	// (3) A SECOND GLOBAL IdP UNDER ANOTHER ALIAS. The scope matches, the ALIAS does not
	// — the same defect on the axis the original report did not name.
	if got := put("/v1/console/sso/idps/okta", noPosture)["enforced_by"]; got != "out_of_scope" {
		t.Fatalf("non-default global IdP enforced_by = %v, want \"out_of_scope\": Posture() resolves alias "+
			"%q only, so this row's posture is not the enforced one either", got, model.DefaultFederationAlias)
	}

	// (4) THE LIST. This is where one value was stamped onto every row. The global list
	// holds both the default (enforced) and "okta" (not), so a hoisted value cannot
	// satisfy this: the two rows must DISAGREE.
	r := h.do("GET", "/v1/console/sso/idps", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list idps = %d %s", r.code, r.raw)
	}
	items, ok := r.body["idps"].([]any)
	if !ok || len(items) < 2 {
		t.Fatalf("list idps returned %v — need at least the default and \"okta\" for this assertion to mean anything", r.body)
	}
	seen := map[string]string{}
	for _, it := range items {
		row, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("list row is not an object: %v", it)
		}
		alias, _ := row["alias"].(string)
		enforced, _ := row["enforced_by"].(string)
		seen[alias] = enforced
	}
	if seen[model.DefaultFederationAlias] != "enterprise" {
		t.Errorf("list: default row enforced_by = %q, want \"enterprise\"", seen[model.DefaultFederationAlias])
	}
	if seen["okta"] != "out_of_scope" {
		t.Errorf("list: \"okta\" row enforced_by = %q, want \"out_of_scope\" — one value hoisted out of the "+
			"loop is what marked every IdP as enforced", seen["okta"])
	}
	if seen[model.DefaultFederationAlias] == seen["okta"] {
		t.Fatalf("every row in the list carries the same enforced_by (%q). That is the hoisted-value defect: "+
			"the signal depends on the row, so two rows in different positions must be able to differ.",
			seen["okta"])
	}
}

// TestEnforcedByIsUnavailableWithoutTheCapability is the NON-FIRING direction. Without it,
// a signal that answered "unavailable"/"out_of_scope" for everything would pass the test above
// on rows 2-4 and only fail row 1 — and a control that never says "enforced" is as useless
// as one that always does. The open build must still report the honest "unavailable", and
// it must do so for EVERY row, including the ones that answer "out_of_scope" when a policy
// is wired: with no policy, no row is enforced, so singling one out would be false.
func TestEnforcedByIsUnavailableWithoutTheCapability(t *testing.T) {
	h := newConsoleHarness(t) // no WithLoginPolicy: the open build
	admin := h.adminLogin()
	h.elevate(admin)

	if h.authr.EnforcesLogin() {
		t.Fatal("harness wired a login policy; this test measures the OPEN build")
	}

	body := map[string]any{
		"protocol": "oidc", "enabled": false,
		"oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "shhh-secret",
		"require_sso": true,
	}
	for _, path := range []string{"/v1/console/sso", "/v1/console/sso/idps/okta"} {
		r := h.do("PUT", path, admin, body, nil)
		if r.code != http.StatusOK {
			t.Fatalf("put %s = %d %s", path, r.code, r.raw)
		}
		if got := r.body["enforced_by"]; got != "unavailable" {
			t.Errorf("open build %s enforced_by = %v, want \"unavailable\" — with no policy wired nothing is "+
				"enforced anywhere, and \"out_of_scope\" would imply some other row IS enforced", path, got)
		}
	}
}

// TestPostureOnAnUnreadRowIsRefused guards against a stored posture that nothing enforces.
//
// require_sso and network_allowlist are stored per IdP row, but the engine reads the posture
// of exactly one row: (GlobalFederationScope, "default"). A PUT that set them on a tenant
// IdP, or on a non-default global one, answered 200 and stored a control nothing applies:
// the operator saw it saved while password, LDAP and SSO-start sign-ins stayed open from
// every address (P1 and P2 of the Business qualification). Silent acceptance is the defect,
// so the PUT refuses it with a 400 that names the global route. The refusal is per row, only
// while the build enforces, and only for a SET posture: clearing one (a row saved before
// this refusal) and every other field keep working.
func TestPostureOnAnUnreadRowIsRefused(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	tenant := h.createOrg(admin, "acme")

	tenantPath := "/v1/console/sso/tenants/" + tenant.String()
	tenantAlias := tenantPath + "/idps/okta"
	globalAlias := "/v1/console/sso/idps/okta"

	body := func(extra map[string]any) map[string]any {
		b := map[string]any{
			"protocol": "oidc", "enabled": false,
			"oidc_issuer": "https://idp.example", "oidc_client_id": "cid",
			"oidc_client_secret": "shhh-secret",
		}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	requireSSO := map[string]any{"require_sso": true}
	allowlist := map[string]any{"network_allowlist": []string{"10.255.255.0/24"}}

	// A row saved BEFORE the refusal, or by an open build later upgraded: it stores a posture
	// the engine never read. It is created with no policy wired, which is the open build.
	if r := h.do("PUT", tenantPath, admin, body(requireSSO), nil); r.code != http.StatusOK {
		t.Fatalf("open build: put tenant posture = %d %s, want 200 (the open build stores it and never enforces)", r.code, r.raw)
	}

	h.authr.WithLoginPolicy(enforcingPolicy{})

	for _, path := range []string{tenantPath, tenantAlias, globalAlias} {
		for name, extra := range map[string]map[string]any{"require_sso": requireSSO, "network_allowlist": allowlist} {
			r := h.do("PUT", path, admin, body(extra), nil)
			if r.code != http.StatusBadRequest {
				t.Errorf("PUT %s with %s = %d %s, want 400: the engine never reads this row's posture", path, name, r.code, r.raw)
				continue
			}
			e, _ := r.body["error"].(map[string]any)
			if msg, _ := e["message"].(string); !strings.Contains(msg, "/v1/console/sso") {
				t.Errorf("PUT %s with %s: error %q does not name the global route that holds the posture", path, name, msg)
			}
		}
	}

	// The refusal stores nothing: the row saved before it still carries exactly what it
	// carried (require_sso on, no allow-list), and the two rows that did not exist still do not.
	if r := h.do("GET", tenantPath, admin, nil, nil); r.code != http.StatusOK || r.body["require_sso"] != true ||
		len(r.body["network_allowlist"].([]any)) != 0 {
		t.Errorf("a refused PUT changed the stored tenant row: %d %s", r.code, r.raw)
	}
	for _, path := range []string{tenantAlias, globalAlias} {
		if r := h.do("GET", path, admin, nil, nil); r.code != http.StatusOK || r.body["configured"] != false {
			t.Errorf("a refused PUT created %s: %d %s", path, r.code, r.raw)
		}
	}

	// The test route persists nothing, so it is not a silent acceptance and stays open.
	if r := h.do("POST", tenantPath+"/test", admin, body(requireSSO), nil); r.code != http.StatusOK {
		t.Errorf("POST %s/test with a posture = %d %s, want 200 (it stores nothing)", tenantPath, r.code, r.raw)
	}

	// Blank CIDR entries are no posture: the service drops them, so they clear it.
	if r := h.do("PUT", tenantPath, admin, body(map[string]any{"network_allowlist": []string{"", "  "}}), nil); r.code != http.StatusOK {
		t.Errorf("PUT %s with only blank CIDRs = %d %s, want 200 (it sets nothing)", tenantPath, r.code, r.raw)
	}
	if r := h.do("PUT", tenantPath, admin, body(requireSSO), nil); r.code != http.StatusBadRequest {
		t.Fatalf("PUT %s with require_sso after the blank-CIDR save = %d %s, want 400", tenantPath, r.code, r.raw)
	}

	// Clearing is not a set posture: the row saved before the refusal can be cleaned, and an
	// IdP with no posture saves as ever, on every row.
	for _, path := range []string{tenantPath, tenantAlias, globalAlias} {
		if r := h.do("PUT", path, admin, body(nil), nil); r.code != http.StatusOK {
			t.Errorf("PUT %s with no posture = %d %s, want 200", path, r.code, r.raw)
		}
	}
	if r := h.do("GET", tenantPath, admin, nil, nil); r.body["require_sso"] != false {
		t.Errorf("clearing the posture did not clear it: %s", r.raw)
	}

	// THE ROW THE ENGINE READS keeps taking a posture: refusing here would break the one
	// place the control lives.
	both := body(map[string]any{"require_sso": true, "network_allowlist": []string{"10.0.0.0/8"}})
	if r := h.do("PUT", "/v1/console/sso", admin, both, nil); r.code != http.StatusOK || r.body["enforced_by"] != "enterprise" {
		t.Errorf("PUT global default with posture = %d %s, want 200 enforced_by enterprise", r.code, r.raw)
	}
}

// TestPostureOnAnUnreadRowIsStoredByTheOpenBuild is the NON-FIRING direction: with no
// LoginPolicy wired nothing is enforced on ANY row, the open build says so
// (enforced_by "unavailable") and its published PUT contract keeps storing the posture it
// never enforces. Refusing there would break a published behavior for no gain.
func TestPostureOnAnUnreadRowIsStoredByTheOpenBuild(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	tenant := h.createOrg(admin, "acme")
	if h.authr.EnforcesLogin() {
		t.Fatal("harness wired a login policy; this test measures the OPEN build")
	}
	b := map[string]any{
		"protocol": "oidc", "enabled": false,
		"oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "shhh-secret",
		"require_sso": true, "network_allowlist": []string{"10.255.255.0/24"},
	}
	r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String(), admin, b, nil)
	if r.code != http.StatusOK || r.body["require_sso"] != true {
		t.Fatalf("open build: PUT tenant posture = %d %s, want 200 with the posture stored", r.code, r.raw)
	}
}

var _ auth.LoginPolicy = enforcingPolicy{}
