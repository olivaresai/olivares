// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
)

// Steering your own session is Community; steering a session another person started is
// Business (docs/editions.md, "Steer a session"). The three steering routes (input, resume,
// peers) ask for sessions:run:write as ever; on top of that, a Community engine answers a
// write-capable colleague 501 business_required, and an engine with the Business seam
// (Dependencies.SteerOthers) lets the same call through.

// steeringFixture is a journey engine with an administrator, a viewer and two editors.
type steeringFixture struct {
	t                               *testing.T
	m                               *Module
	h                               *harness
	admin, viewer, owner, colleague string
	tenant                          model.TenantID
	workspaceRef, profileRef        any
}

func newSteeringFixture(t *testing.T) *steeringFixture {
	t.Helper()
	m, h, admin, tenant := journeyOnProductionTransport(t)
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "name": "ws-steer"}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	return &steeringFixture{
		t: t, m: m, h: h, admin: admin, tenant: tenant,
		viewer:       h.viewerToken(admin, tenant, "viewer@steer.test"),
		owner:        h.userToken(admin, tenant, "owner@steer.test", auth.RoleEditor),
		colleague:    h.userToken(admin, tenant, "colleague@steer.test", auth.RoleEditor),
		workspaceRef: wr.body["workspace_ref"], profileRef: ensureRuntimeTestProfileRef(t, m, tenant),
	}
}

// launch starts a session as the holder of token and returns its run_ref.
func (f *steeringFixture) launch(token string) string {
	f.t.Helper()
	created := f.h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{
		"workspace_ref": f.workspaceRef, "provider_profile_ref": f.profileRef,
		"transport": "stream-json", "isolation": "native", "permission_mode": "default",
	}, tenantHdr(f.tenant))
	if created.code != http.StatusCreated {
		f.t.Fatalf("launch = %d %s", created.code, created.raw)
	}
	ref, _ := created.body["run_ref"].(string)
	if ref == "" {
		f.t.Fatalf("launch returned no run_ref: %s", created.raw)
	}
	return ref
}

type steeringCall struct {
	name, method, suffix string
	body                 any
}

var steeringCalls = []steeringCall{
	{"input", "POST", "/input", map[string]any{"line": `{"type":"user","message":{"role":"user","content":"continue"}}`}},
	{"resume", "POST", "/resume", nil},
	{"peers", "PUT", "/peers", map[string]any{"peers": []string{}}},
	{"rename", "PUT", "/name", map[string]any{"name": "x"}},
	{"archive", "POST", "/archive", nil},
	{"unarchive", "DELETE", "/archive", nil},
}

func (f *steeringFixture) do(token, ref string, c steeringCall) resp {
	return f.h.doJSON(c.method, "/v1/m/sessions/runs/"+ref+c.suffix, token, c.body, tenantHdr(f.tenant))
}

// requireBusinessRefusal fails unless got is the Community edition refusal.
func requireBusinessRefusal(t *testing.T, who, call string, got resp) {
	t.Helper()
	errBody, _ := got.body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if got.code != http.StatusNotImplemented || errBody["code"] != "business_required" || !strings.Contains(msg, "Business") {
		t.Errorf("%s: %s = %d %s, want 501 business_required naming Business", who, call, got.code, got.raw)
	}
}

// requireReached fails when the route answered with the role check (403) or the edition
// refusal (501) instead of its own handler.
func requireReached(t *testing.T, who, call string, got resp) {
	t.Helper()
	if got.code == http.StatusNotImplemented || got.code == http.StatusForbidden {
		t.Errorf("%s: %s = %d %s, want the route's own answer", who, call, got.code, got.raw)
	}
}

func TestSteeringAnotherPersonsSessionRequiresBusiness(t *testing.T) {
	f := newSteeringFixture(t)
	ref := f.launch(f.owner)

	// Business: the colleague's calls are the calls they were before the edition split.
	f.m.rt.SteerOthers = steerOthersLicensed
	if got := f.do(f.colleague, ref, steeringCalls[0]); got.code != http.StatusAccepted {
		t.Errorf("Business colleague: input = %d %s, want 202", got.code, got.raw)
	}
	for _, c := range steeringCalls[1:] {
		requireReached(t, "Business colleague", c.name, f.do(f.colleague, ref, c))
	}

	// Community: the launcher steers their own session.
	f.m.rt.SteerOthers = nil
	if got := f.do(f.owner, ref, steeringCalls[0]); got.code != http.StatusAccepted {
		t.Errorf("Community launcher: input = %d %s, want 202", got.code, got.raw)
	}
	for _, c := range steeringCalls[1:] {
		requireReached(t, "Community launcher", c.name, f.do(f.owner, ref, c))
	}
	// Community: the colleague is refused on all three, and the answer names Business.
	for _, c := range steeringCalls {
		requireBusinessRefusal(t, "Community colleague", c.name, f.do(f.colleague, ref, c))
	}
	// The role check still comes first: a viewer is 403, not an edition refusal.
	for _, c := range steeringCalls {
		if got := f.do(f.viewer, ref, c); got.code != http.StatusForbidden {
			t.Errorf("viewer: %s = %d %s, want 403", c.name, got.code, got.raw)
		}
	}
}

// steerOthersLicensed is the Business seam under a licence that covers steering.
func steerOthersLicensed() error { return nil }

// A Business build asks its gate on every colleague's call: without a licence that covers
// steering, the colleague hears 403 addon_requires_license and the call never runs, while
// the launcher still steers their own session.
func TestSteeringAnotherPersonsSessionNeedsALiveLicence(t *testing.T) {
	f := newSteeringFixture(t)
	ref := f.launch(f.owner)
	asked := 0
	f.m.rt.SteerOthers = func() error {
		asked++
		return license.AddonRequired("session-steering", "steer-others")
	}
	for _, c := range steeringCalls {
		got := f.do(f.colleague, ref, c)
		errBody, _ := got.body["error"].(map[string]any)
		if got.code != http.StatusForbidden || errBody["code"] != "addon_requires_license" {
			t.Errorf("unlicensed colleague: %s = %d %s, want 403 addon_requires_license", c.name, got.code, got.raw)
		}
	}
	if asked != len(steeringCalls) {
		t.Errorf("the gate was asked %d times for %d colleague calls; want once per call", asked, len(steeringCalls))
	}
	if got := f.do(f.owner, ref, steeringCalls[0]); got.code != http.StatusAccepted {
		t.Errorf("unlicensed launcher: input = %d %s, want 202", got.code, got.raw)
	}
	for _, c := range steeringCalls[1:] {
		requireReached(t, "unlicensed launcher", c.name, f.do(f.owner, ref, c))
	}
}

// The refusal comes before the handler reads its body: a colleague who sends images, a
// fenced input or a malformed message hears 501, not the images path or a 400, and a run
// that does not exist is the route's own 404 on all three routes.
func TestSteeringRefusalComesBeforeTheHandlerReadsTheBody(t *testing.T) {
	f := newSteeringFixture(t)
	ref := f.launch(f.owner)
	for name, body := range map[string]any{
		"images":       map[string]any{"text": "look", "attachments": []map[string]any{{"id": "att_1"}}},
		"fenced input": map[string]any{"text": "go", "work_lease_fence": 7},
		"bad fence":    map[string]any{"work_lease_fence": -1},
		"empty":        map[string]any{},
	} {
		got := f.do(f.colleague, ref, steeringCall{"input", "POST", "/input", body})
		requireBusinessRefusal(t, "Community colleague, "+name, "input", got)
	}
	for _, c := range steeringCalls {
		if got := f.do(f.colleague, "no-such-run", c); got.code != http.StatusNotFound {
			t.Errorf("unknown run: %s = %d %s, want 404", c.name, got.code, got.raw)
		}
	}
}

// A launch by API token is the token owner's session: another account is refused, and the
// owner's own login steers it.
func TestSteeringATokenLaunchedSessionBelongsToTheTokenOwner(t *testing.T) {
	f := newSteeringFixture(t)
	issued := f.h.doJSON("POST", "/v1/tokens", f.admin, map[string]any{"name": "ci", "tenant": f.tenant.String(), "role": auth.RoleEditor}, nil)
	token, _ := issued.body["token"].(string)
	if issued.code != http.StatusCreated || token == "" {
		t.Fatalf("token = %d %s", issued.code, issued.raw)
	}
	ref := f.launch(token)
	for _, c := range steeringCalls {
		requireBusinessRefusal(t, "colleague on a token launch", c.name, f.do(f.colleague, ref, c))
	}
	if got := f.do(f.admin, ref, steeringCalls[0]); got.code != http.StatusAccepted {
		t.Errorf("token owner's login: input = %d %s, want 202", got.code, got.raw)
	}
}

// runLauncher and foreignSteering compare accounts, not credentials: the launcher's own API
// token is the launcher, a resume does not hand the session to the resumer, and a session
// the engine cannot attribute to an account is not "another person's".
func TestForeignSteeringComparesAccounts(t *testing.T) {
	a, b := model.NewID(), model.NewID()
	user := func(id model.ID) auth.Principal {
		return auth.Principal{Kind: auth.KindUser, UserID: id, CredID: model.NewID()}
	}
	token := func(owner model.ID) auth.Principal {
		return auth.Principal{Kind: auth.KindToken, UserID: owner, CredID: model.NewID()}
	}
	for _, c := range []struct {
		name     string
		holder   string // claim_holder
		launcher string // launcher_user_id
		caller   auth.Principal
		want     bool
	}{
		{"same login", "user:" + a.String(), a.String(), user(a), false},
		{"other login", "user:" + a.String(), a.String(), user(b), true},
		{"row from before the launcher stamp: same login", "user:" + a.String(), "", user(a), false},
		{"row from before the launcher stamp: other login", "user:" + a.String(), "", user(b), true},
		{"launcher's own token", "user:" + a.String(), a.String(), token(a), false},
		{"other account's token", "user:" + a.String(), a.String(), token(b), true},
		{"standalone token on a user's session", "user:" + a.String(), a.String(), token(""), false},
		{"token launch, owner logs in", "token:" + model.NewID().String(), a.String(), user(a), false},
		{"token launch, other login", "token:" + model.NewID().String(), a.String(), user(b), true},
		{"token launch before the stamp: nobody to compare", "token:" + model.NewID().String(), "", user(a), false},
		{"resumed by another account: still the launcher's", "user:" + b.String(), a.String(), user(a), false},
		{"resumed by another account: the resumer is not the launcher", "user:" + b.String(), a.String(), user(b), true},
		{"launched by an agent or work lease", "agent:build", "", user(a), false},
		{"no recorded launcher", "", "", user(a), false},
	} {
		rec := model.Record{colClaimHolder: c.holder}
		if c.launcher != "" {
			rec[colRunLauncherUserID] = c.launcher
		}
		if got := foreignSteering(runLauncher(rec), c.caller); got != c.want {
			t.Errorf("%s: foreignSteering = %v, want %v", c.name, got, c.want)
		}
	}
}
