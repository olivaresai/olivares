// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// Who may use which AI tool and account, on the real engine and through the real routes.
// Departments are directory groups here (a group nested under another is inside it), the
// way a person who is not confined to one workspace belongs to a department; the request
// context of a person carries the boundary of their workspace if they have one, which the
// decision must not be confined by. Assignments are stored the way a Business install
// stores them: the Community build refuses to write them, which these tests also pin.
type departmentEngine struct {
	t      *testing.T
	s      stubSessionEngine
	tenant model.TenantID
}

const departmentPassword = "fixture-password-2026!"

func newDepartmentEngine(t *testing.T) departmentEngine {
	t.Helper()
	s := bootStubSessionEngine(t, false, "sourcescope")
	return departmentEngine{t: t, s: s, tenant: model.TenantID(s.tenant)}
}

func (d departmentEngine) profile(home, name string) string {
	d.t.Helper()
	got := d.s.do("POST", "/v1/m/sessions/provider-profiles", map[string]any{
		"driver": "claude", "config_home": home, "user_home": home, "auth_source": "provider_account_home", "display_name": name,
	}, http.StatusCreated)
	return got["profile_ref"].(string)
}

func (d departmentEngine) user(email string) model.ID {
	d.t.Helper()
	out := d.s.do("POST", "/v1/users", map[string]any{
		"email": email, "password": departmentPassword, "tenant": d.s.tenant, "role": "editor",
	}, http.StatusCreated)
	return model.ID(out["id"].(string))
}

func (d departmentEngine) group(name string, parent model.ID, members ...model.ID) model.ID {
	d.t.Helper()
	var id model.ID
	if err := d.s.eng.store.AuthMutate(d.t.Context(), func(as store.AuthScope) error {
		g, err := as.Groups().Create(d.t.Context(), model.UserGroup{TargetTenantID: d.tenant, DisplayName: name, ParentGroupID: parent})
		if err != nil {
			return err
		}
		id = g.ID
		for _, u := range members {
			if _, err := as.GroupMembers().Create(d.t.Context(), model.UserGroupMember{GroupID: g.ID, UserID: u}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		d.t.Fatal(err)
	}
	return id
}

// assign stores an assignment the way a Business install does.
func (d departmentEngine) assign(sourceType, sourceRef string, g model.ID) {
	d.t.Helper()
	if err := d.s.eng.store.Mutate(d.t.Context(), d.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sourcescope.binding")
		if err != nil {
			return err
		}
		_, err = repo.Create(d.t.Context(), model.Record{
			"source_type": sourceType, "source_ref": sourceRef, "scope_tree": "user_group", "scope_ref": g.String(),
			"enabled": true, "created_by": "fixture", "effect": "allow",
		})
		return err
	}); err != nil {
		d.t.Fatal(err)
	}
}

func (d departmentEngine) login(email string) string {
	d.t.Helper()
	code, out, raw := doDemoViewJSON(d.t, d.s.eng.api.Handler(), "POST", "/v1/auth/login", "", "", map[string]any{"email": email, "password": departmentPassword})
	if code != http.StatusOK {
		d.t.Fatalf("login %s = %d %s", email, code, raw)
	}
	return out["token"].(string)
}

func (d departmentEngine) as(token, method, path string, body any) (int, map[string]any, string) {
	return doDemoViewJSON(d.t, d.s.eng.api.Handler(), method, path, token, d.s.tenant, body)
}

func (d departmentEngine) listed(token string) []string {
	d.t.Helper()
	code, body, raw := d.as(token, "GET", "/v1/m/sessions/provider-profiles", nil)
	if code != http.StatusOK {
		d.t.Fatalf("list = %d %s", code, raw)
	}
	return profileRefsOf(body)
}

// readyTools are the tools the readiness answer lists for this person.
func (d departmentEngine) readyTools(token string) []string {
	d.t.Helper()
	code, body, raw := d.as(token, "GET", "/v1/m/sessions/provider-profiles/readiness", nil)
	if code != http.StatusOK {
		d.t.Fatalf("readiness = %d %s", code, raw)
	}
	var tools []string
	items, _ := body["tools"].([]any)
	for _, it := range items {
		tools = append(tools, it.(map[string]any)["driver"].(string))
	}
	return tools
}

// launch starts a session under the profile and stops it when it started.
func (d departmentEngine) launch(token, profileRef string) (int, string) {
	d.t.Helper()
	code, body, raw := d.as(token, "POST", "/v1/m/sessions/runs", map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profileRef,
	})
	if code == http.StatusCreated {
		d.as(token, "POST", "/v1/m/sessions/runs/"+body["run_ref"].(string)+"/stop", map[string]any{})
	}
	return code, raw
}

func TestProfileAssignmentFollowsTheDepartmentTree(t *testing.T) {
	d := newDepartmentEngine(t)
	forEngineering, forSales := d.profile(d.s.home, "engineering tools"), d.profile(t.TempDir(), "sales tools")

	// The Community build does not write an assignment ...
	refused := d.s.do("POST", "/v1/m/sourcescope/bindings", map[string]any{
		"source_type": "profile", "source_ref": forEngineering, "scope_tree": "user_group", "scope_ref": "any", "enabled": true,
	}, http.StatusNotImplemented)
	if e, _ := refused["error"].(map[string]any); e["code"] != "business_required" {
		t.Fatalf("assigning a profile in Community = %v, want business_required", refused)
	}

	engineer, platformer, seller := d.user("engineer@example.invalid"), d.user("platform@example.invalid"), d.user("seller@example.invalid")
	d.user("outsider@example.invalid") // in no group
	engineering := d.group("engineering", "", engineer)
	d.group("platform", engineering, platformer) // nested under engineering: inside it
	sales := d.group("sales", "", seller)

	// ... and applies the ones a Business install stored.
	d.assign("profile", forEngineering, engineering)
	d.assign("profile", forSales, sales)

	engineerToken, platformToken, sellerToken, outsiderToken := d.login("engineer@example.invalid"), d.login("platform@example.invalid"), d.login("seller@example.invalid"), d.login("outsider@example.invalid")

	if refs := d.listed(engineerToken); !slices.Contains(refs, forEngineering) || slices.Contains(refs, forSales) {
		t.Errorf("a member of engineering lists %v, want only %s", refs, forEngineering)
	}
	if refs := d.listed(platformToken); !slices.Contains(refs, forEngineering) || slices.Contains(refs, forSales) {
		t.Errorf("a member of a group nested under engineering lists %v, want the engineering profile inherited and not %s", refs, forSales)
	}
	if refs := d.listed(sellerToken); slices.Contains(refs, forEngineering) || !slices.Contains(refs, forSales) {
		t.Errorf("a member of sales lists %v, want only %s", refs, forSales)
	}
	if refs := d.listed(outsiderToken); slices.Contains(refs, forEngineering) || slices.Contains(refs, forSales) {
		t.Errorf("a person in no group lists %v, want neither assigned profile", refs)
	}

	if code, _, raw := d.as(engineerToken, "GET", "/v1/m/sessions/provider-profiles/"+forSales, nil); code != http.StatusNotFound {
		t.Errorf("reading another department's profile = %d %s, want 404", code, raw)
	}
	for who, token := range map[string]string{"sales": sellerToken, "nobody": outsiderToken} {
		if code, raw := d.launch(token, forEngineering); code != http.StatusForbidden || !strings.Contains(raw, "not assigned") {
			t.Errorf("launch by %s under engineering's profile = %d %s, want 403 not assigned", who, code, raw)
		}
	}
	if code, raw := d.launch(platformToken, forSales); code != http.StatusForbidden || !strings.Contains(raw, "not assigned") {
		t.Errorf("launch by a nested group under a sales profile = %d %s, want 403 not assigned", code, raw)
	}
	if code, raw := d.launch(platformToken, forEngineering); code != http.StatusCreated {
		t.Fatalf("launch by a nested group under the inherited profile = %d %s, want 201", code, raw)
	}

	// The administrator keeps oversight of every profile.
	if refs := profileRefsOf(d.s.do("GET", "/v1/m/sessions/provider-profiles", nil, http.StatusOK)); !slices.Contains(refs, forEngineering) || !slices.Contains(refs, forSales) {
		t.Errorf("the administrator lists %v, want both", refs)
	}
	// So does a tenant administrator who is not the bootstrap superadmin. The superadmin passes
	// before the resolver reads sessions:profile:admin, so only this one proves that term.
	d.s.do("POST", "/v1/users", map[string]any{
		"email": "tenant-admin@example.invalid", "password": departmentPassword, "tenant": d.s.tenant, "role": "admin",
	}, http.StatusCreated)
	if refs := d.listed(d.login("tenant-admin@example.invalid")); !slices.Contains(refs, forEngineering) || !slices.Contains(refs, forSales) {
		t.Errorf("a tenant administrator lists %v, want both", refs)
	}
}

// A tool is assigned on its own, to a department, and a profile of it still has to be
// assigned too: both answers have to allow. This is the tool half and the AND of the two, on
// the real resolver, which the sessions tests only fake.
func TestToolAssignmentAndProfileAssignmentBothHaveToAllow(t *testing.T) {
	d := newDepartmentEngine(t)
	open, forEngineering := d.profile(d.s.home, "open tools"), d.profile(t.TempDir(), "engineering tools")
	engineer, seller := d.user("engineer@example.invalid"), d.user("seller@example.invalid")
	engineering, sales := d.group("engineering", "", engineer), d.group("sales", "", seller)
	d.assign("tool", "claude", sales)
	d.assign("profile", forEngineering, engineering)
	engineerToken, sellerToken := d.login("engineer@example.invalid"), d.login("seller@example.invalid")

	// Claude Code is sales'. A profile nobody limited is open, but its tool is not.
	if code, raw := d.launch(engineerToken, open); code != http.StatusForbidden || !strings.Contains(raw, "not assigned") {
		t.Errorf("launch by engineering under an open profile of sales' tool = %d %s, want 403 not assigned", code, raw)
	}
	if code, raw := d.launch(sellerToken, open); code != http.StatusCreated {
		t.Errorf("launch by sales under an open profile of its tool = %d %s, want 201", code, raw)
	}
	if tools := d.readyTools(engineerToken); slices.Contains(tools, "claude") {
		t.Errorf("engineering is offered %v, want not Claude Code, which is sales'", tools)
	}
	if tools := d.readyTools(sellerToken); !slices.Contains(tools, "claude") {
		t.Errorf("sales is offered %v, want Claude Code", tools)
	}
	if refs := d.listed(engineerToken); len(refs) != 0 {
		t.Errorf("engineering lists %v, want no profile of a tool it was not assigned", refs)
	}

	// Engineering's profile is of sales' tool: the profile is engineering's and the tool is
	// not, so nobody but the administrator may start on it.
	for who, token := range map[string]string{"engineering": engineerToken, "sales": sellerToken} {
		if code, raw := d.launch(token, forEngineering); code != http.StatusForbidden || !strings.Contains(raw, "not assigned") {
			t.Errorf("launch by %s under a profile and a tool assigned to different departments = %d %s, want 403 not assigned", who, code, raw)
		}
	}
	admin := d.s.do("POST", "/v1/m/sessions/runs", map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": forEngineering,
	}, http.StatusCreated)
	d.s.do("POST", "/v1/m/sessions/runs/"+admin["run_ref"].(string)+"/stop", map[string]any{}, http.StatusOK)
}

// The model path stays with the assignment, on the real resolver: a provider key some
// assignment limits is the key of the people its profile was assigned to, so a profile made on
// it opens it to everyone who may run a session. Only a profile administrator does that, also
// when the caller may use the profile that runs on the key now.
func TestAKeyAnAssignmentLimitsIsSharedByProfileAdministratorsOnly(t *testing.T) {
	d := newDepartmentEngine(t)
	key := d.s.do("POST", "/v1/m/sessions/providers", map[string]any{
		"kind": "anthropic", "display_name": "Finance key", "api_key": "sk-ant-fixture-0123456789",
	}, http.StatusCreated)["provider_ref"].(string)
	onKey := map[string]any{"driver": "claude", "auth_source": "managed_injection", "provider_record_ref": key, "display_name": "On the key"}
	forEngineering := d.s.do("POST", "/v1/m/sessions/provider-profiles", onKey, http.StatusCreated)["profile_ref"].(string)

	engineer, seller := d.user("engineer@example.invalid"), d.user("seller@example.invalid")
	engineering := d.group("engineering", "", engineer)
	d.group("sales", "", seller)
	engineerToken, sellerToken := d.login("engineer@example.invalid"), d.login("seller@example.invalid")

	// Nothing limits the key yet: any editor shares it, as before assignments existed.
	if code, _, raw := d.as(engineerToken, "POST", "/v1/m/sessions/provider-profiles", onKey); code != http.StatusCreated {
		t.Fatalf("a profile made on a key nobody limited = %d %s, want 201", code, raw)
	}

	d.assign("profile", forEngineering, engineering)
	for who, token := range map[string]string{"engineering, which may use the profile on it": engineerToken, "sales": sellerToken} {
		if code, _, raw := d.as(token, "POST", "/v1/m/sessions/provider-profiles", onKey); code != http.StatusForbidden {
			t.Errorf("a profile made on a limited key by %s = %d %s, want 403", who, code, raw)
		}
	}
	if code, _, raw := d.as(engineerToken, "GET", "/v1/m/sessions/provider-profiles/"+forEngineering, nil); code != http.StatusOK {
		t.Errorf("the profile on the key stays engineering's = %d %s, want 200", code, raw)
	}
	d.s.do("POST", "/v1/m/sessions/provider-profiles", onKey, http.StatusCreated)
}

// The key a profile runs on is the assigned people's in every route that shows or changes
// it, on the real resolver: the key's own routes and the models list answer a person of
// another department as if the key did not exist. A key nobody limited stays open.
func TestAKeyAnAssignmentLimitsIsTheAssignedPeoplesOnlyInEveryRoute(t *testing.T) {
	s := bootStubSessionEngine(t, false, "sourcescope", "models")
	d := departmentEngine{t: t, s: s, tenant: model.TenantID(s.tenant)}
	key := d.s.do("POST", "/v1/m/sessions/providers", map[string]any{
		"kind": "anthropic", "display_name": "Finance key", "api_key": "sk-ant-fixture-0123456789",
	}, http.StatusCreated)["provider_ref"].(string)
	forEngineering := d.s.do("POST", "/v1/m/sessions/provider-profiles", map[string]any{
		"driver": "claude", "auth_source": "managed_injection", "provider_record_ref": key, "display_name": "On the key",
	}, http.StatusCreated)["profile_ref"].(string)
	engineer, platformer, seller := d.user("engineer@example.invalid"), d.user("platform@example.invalid"), d.user("seller@example.invalid")
	engineering := d.group("engineering", "", engineer)
	d.group("platform", engineering, platformer) // nested under engineering: inside it
	d.group("sales", "", seller)
	engineerToken, platformToken, sellerToken := d.login("engineer@example.invalid"), d.login("platform@example.invalid"), d.login("seller@example.invalid")

	keyPath := "/v1/m/sessions/providers/" + key
	keyRefs := func(token, path, field string) []string {
		d.t.Helper()
		code, body, raw := d.as(token, "GET", path, nil)
		if code != http.StatusOK {
			d.t.Fatalf("GET %s = %d %s", path, code, raw)
		}
		var refs []string
		items, _ := body["items"].([]any)
		for _, it := range items {
			if ref, ok := it.(map[string]any)[field].(string); ok {
				refs = append(refs, ref)
			}
		}
		return refs
	}
	listedKeys := func(token string) []string { return keyRefs(token, "/v1/m/sessions/providers", "provider_ref") }
	modelKeys := func(token string) []string { return keyRefs(token, "/v1/m/models/availability", "provider_ref") }

	// Nothing limits the key yet: it is open to the people who hold the routes' permissions.
	if code, _, raw := d.as(sellerToken, "GET", keyPath, nil); code != http.StatusOK {
		t.Fatalf("a key nobody limited, read by sales = %d %s, want 200", code, raw)
	}
	if !slices.Contains(listedKeys(sellerToken), key) || !slices.Contains(modelKeys(sellerToken), key) {
		t.Fatalf("a key nobody limited is not listed to sales: %v, models %v", listedKeys(sellerToken), modelKeys(sellerToken))
	}

	d.assign("profile", forEngineering, engineering)
	// Revoking needs sessions:provider:admin, which an editor does not hold; that route's guard is
	// the sessions module's test (a caller who holds the permission and is not assigned).
	for _, route := range []struct {
		name, method, path string
		body               any
	}{
		{"read", "GET", keyPath, nil},
		{"rename", "PATCH", keyPath, map[string]any{"display_name": "Taken over by sales"}},
		{"change its model", "PATCH", keyPath, map[string]any{"default_model": "claude-haiku-5"}},
		{"test", "POST", keyPath + "/test", map[string]any{}},
	} {
		if code, _, raw := d.as(sellerToken, route.method, route.path, route.body); code != http.StatusNotFound || !strings.Contains(raw, "provider not found") {
			t.Errorf("%s of engineering's key by sales = %d %s, want 404 provider not found", route.name, code, raw)
		}
	}
	if slices.Contains(listedKeys(sellerToken), key) || slices.Contains(modelKeys(sellerToken), key) {
		t.Errorf("sales is shown engineering's key: providers %v, models %v", listedKeys(sellerToken), modelKeys(sellerToken))
	}

	// Engineering, whose profile runs on the key, and the administrator keep it.
	if !slices.Contains(listedKeys(engineerToken), key) || !slices.Contains(modelKeys(engineerToken), key) {
		t.Errorf("engineering is not shown its own key: providers %v, models %v", listedKeys(engineerToken), modelKeys(engineerToken))
	}
	if code, _, raw := d.as(engineerToken, "PATCH", keyPath, map[string]any{"display_name": "Engineering key"}); code != http.StatusOK {
		t.Errorf("rename of its own key by engineering = %d %s, want 200", code, raw)
	}
	// A group nested under engineering is inside it: the key is inherited, with its model list.
	if code, _, raw := d.as(platformToken, "GET", keyPath, nil); code != http.StatusOK {
		t.Errorf("read of engineering's key by a group nested under it = %d %s, want 200", code, raw)
	}
	if !slices.Contains(listedKeys(platformToken), key) || !slices.Contains(modelKeys(platformToken), key) {
		t.Errorf("a group nested under engineering is not shown the inherited key: providers %v, models %v", listedKeys(platformToken), modelKeys(platformToken))
	}
	got := d.s.do("GET", "/v1/m/sessions/providers/"+key, nil, http.StatusOK)
	if got["display_name"] != "Engineering key" || got["state"] != "active" || got["default_model"] != "" {
		t.Errorf("the key after sales' refused writes and engineering's rename = %v, want only the rename", got)
	}
}

// A provider key in the models list is judged by the key rule of its own routes and by the
// key's reference; a tool account or a tool's login by the assignment and by its own
// references; with no way to ask about a key, or an unreadable assignment, nothing is shown.
func TestModelsAccessJudgesAProviderKeyByTheKeyRuleAndAnAccountByTheAssignment(t *testing.T) {
	type asked struct{ profile, driver, record string }
	var got []asked
	answer := func(ok bool, err error) (func(context.Context, model.TenantID, auth.Principal, string, string) (bool, error), func(context.Context, model.TenantID, auth.Principal, string) (bool, error)) {
		return func(_ context.Context, _ model.TenantID, _ auth.Principal, profile, driver string) (bool, error) {
				got = append(got, asked{profile: profile, driver: driver})
				return ok && err == nil, err
			}, func(_ context.Context, _ model.TenantID, _ auth.Principal, ref string) (bool, error) {
				got = append(got, asked{record: ref})
				return ok && err == nil, err
			}
	}
	key := models.AvailabilitySource{Ref: "prv_key", ProviderRef: "prv_key", ProviderKind: "anthropic"}
	account := models.AvailabilitySource{Ref: "ppf_a", AccountRef: "ppf_a", Driver: "claude"}
	login := models.AvailabilitySource{Ref: "login:codex", Driver: "codex"}
	unreadable := errors.New("store unavailable")
	for name, c := range map[string]struct {
		source  models.AvailabilitySource
		ok      bool
		err     error
		noKeyFn bool
		want    bool
		wantErr bool
		asked   asked
	}{
		"a key the caller may use":             {source: key, ok: true, want: true, asked: asked{record: "prv_key"}},
		"a key the caller may not use":         {source: key, asked: asked{record: "prv_key"}},
		"a key, assignment unreadable":         {source: key, ok: true, err: unreadable, wantErr: true, asked: asked{record: "prv_key"}},
		"a key with no way to ask":             {source: key, ok: true, noKeyFn: true},
		"an account the caller may use":        {source: account, ok: true, want: true, asked: asked{profile: "ppf_a", driver: "claude"}},
		"an account the caller may not use":    {source: account, asked: asked{profile: "ppf_a", driver: "claude"}},
		"a tool's own login":                   {source: login, ok: true, want: true, asked: asked{driver: "codex"}},
		"a tool's own login, not assigned":     {source: login, asked: asked{driver: "codex"}},
		"an account, assignment unreadable":    {source: account, err: unreadable, wantErr: true, asked: asked{profile: "ppf_a", driver: "claude"}},
		"an account when keys cannot be asked": {source: account, ok: true, noKeyFn: true, want: true, asked: asked{profile: "ppf_a", driver: "claude"}},
	} {
		got = nil
		mayUse, mayUseRecord := answer(c.ok, c.err)
		if c.noKeyFn {
			mayUseRecord = nil
		}
		shown, err := modelsAccess(mayUse, mayUseRecord)(t.Context(), "tnt", auth.Principal{}, c.source)
		if shown != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: shown=%v err=%v, want shown=%v err=%v", name, shown, err, c.want, c.wantErr)
		}
		if (c.asked == asked{}) != (len(got) == 0) || (len(got) > 0 && (len(got) != 1 || got[0] != c.asked)) {
			t.Errorf("%s: asked %+v, want %+v", name, got, c.asked)
		}
	}
}

func profileRefsOf(body map[string]any) []string {
	var refs []string
	items, _ := body["items"].([]any)
	for _, it := range items {
		refs = append(refs, it.(map[string]any)["profile_ref"].(string))
	}
	return refs
}
