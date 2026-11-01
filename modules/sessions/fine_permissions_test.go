// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
)

type narrowRouteCapture struct {
	permCapture
	refs map[string]api.EntityRef
}

func (c narrowRouteCapture) HandleEntity(method, pattern string, perm auth.Permission, ref api.EntityRef, h api.ModuleHandler) {
	c.permCapture.HandleEntity(method, pattern, perm, ref, h)
	c.refs[method+" "+pattern] = ref
}

func TestRuntimeNarrowRoutePermissions(t *testing.T) {
	c := narrowRouteCapture{permCapture: permCapture{perms: map[string]auth.Permission{}}, refs: map[string]api.EntityRef{}}
	New().runtimeRoutes(c)
	for _, path := range []string{"events", "attach", "changes", "changes/file", "git", "diff", "diff/file", "handoff", "activity", "activity/agents/{agent}", "messages", "attachments/{id}"} {
		if got := c.perms["GET /runs/{ref}/"+path]; got != "sessions:output:read" {
			t.Errorf("%s uses %s, want output read", path, got)
		}
	}
	if got := c.perms["POST /runs/{ref}/stop"]; got != "sessions:stop:write" {
		t.Errorf("stop uses %s", got)
	}
	for _, path := range []string{"input", "resume", "git/commit"} {
		if got := c.perms["POST /runs/{ref}/"+path]; got != "sessions:run:write" {
			t.Errorf("%s uses %s", path, got)
		}
	}
	for route, ref := range c.refs {
		kind := ref.ResourceKind
		if kind == "" {
			kind = c.perms[route].Resource()
		}
		if kind != "run" {
			t.Errorf("%s targets %s, want stored run", route, kind)
		}
	}
	if got := c.perms["GET /runs"]; got != "sessions:run:read" {
		t.Errorf("listing uses %s", got)
	}
}

func TestViewerListsRunsWithoutReadingOutputHTTP(t *testing.T) {
	fr := &fakeRunner{initSID: "session-output"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	root := h.adminLogin()
	tenant := h.createOrg(root, "output-split")
	viewer := h.viewerToken(root, tenant, "viewer@output.test")
	r := h.doJSON("POST", "/v1/m/sessions/workspaces", root, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("workspace: %d %s", r.code, r.raw)
	}
	r = h.doJSON("POST", "/v1/m/sessions/runs", root, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "workspace_ref": r.body["workspace_ref"], "transport": "stream-json", "permission_mode": "default", "isolation": "native"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("launch: %d %s", r.code, r.raw)
	}
	ref := r.body["run_ref"].(string)
	t.Cleanup(func() { h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", root, nil, tenantHdr(tenant)) })
	if r = h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("list: %d %s", r.code, r.raw)
	}
	// A stopped run has a closed attach stream, so the pre-fix read returns
	// content rather than leaving the RED probe waiting on an open stream.
	if r = h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", root, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop: %d %s", r.code, r.raw)
	}
	for _, path := range []string{"events", "attach", "messages", "diff", "changes", "activity", "handoff"} {
		if r = h.do("GET", "/v1/m/sessions/runs/"+ref+"/"+path, viewer, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Errorf("%s = %d %s", path, r.code, r.raw)
		}
	}
}

func TestStopOnlyGrantHTTP(t *testing.T) {
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	WithRunner(&fakeRunner{initSID: "helpdesk-stop"})(f.m)
	WithCredentialSource(staticCred())(f.m)
	root := f.adminLogin()
	tenant := f.createOrg(root, "stop-only")
	viewer := f.viewerToken(root, tenant, "helpdesk@stop.test")
	p, err := f.authr.Authenticate(context.Background(), viewer)
	if err != nil {
		t.Fatal(err)
	}
	testsupport.SeedCedar(t, f.st, tenant, fmt.Sprintf(`permit(principal in User::%q, action == Action::"sessions:stop:write", resource);`, p.UserID.String()), f.gov)
	r := f.doJSON("POST", "/v1/m/sessions/workspaces", root, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("workspace: %d %s", r.code, r.raw)
	}
	r = f.doJSON("POST", "/v1/m/sessions/runs", root, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, f.m, tenant), "workspace_ref": r.body["workspace_ref"], "transport": "stream-json", "permission_mode": "default", "isolation": "native"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("launch: %d %s", r.code, r.raw)
	}
	base := "/v1/m/sessions/runs/" + r.body["run_ref"].(string)
	for _, path := range []string{"input", "resume", "git/commit"} {
		if r = f.doJSON("POST", base+"/"+path, viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Errorf("%s = %d %s", path, r.code, r.raw)
		}
	}
	if r = f.do("GET", base+"/events", viewer, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("output = %d %s", r.code, r.raw)
	}
	if r = f.doJSON("POST", base+"/stop", viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	if r.body["state"] != stateStopped {
		t.Errorf("not stopped: %s", r.raw)
	}
}

func TestOutputOnlyGrantHTTP(t *testing.T) {
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	root := f.adminLogin()
	tenant := f.createOrg(root, "output-only")
	r := f.doJSON("POST", "/v1/users", root, map[string]any{"email": "output@only.test", "password": "outputpass1234"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("user: %d %s", r.code, r.raw)
	}
	r = f.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "output@only.test", "password": "outputpass1234"}, nil)
	token := r.body["token"].(string)
	p, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.RoleIn(tenant); ok {
		t.Fatal("output reader has membership")
	}
	ref := "output-only-run"
	if err := f.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{colRunRef: ref, colRunName: "output only", colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateStopped, colLastEventSeq: int64(0)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedCedar(t, f.st, tenant, fmt.Sprintf(`permit(principal in User::%q, action == Action::"sessions:output:read", resource);`, p.UserID.String()), f.gov)
	if r = f.do("GET", "/v1/m/sessions/runs/"+ref+"/events", token, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("output = %d %s", r.code, r.raw)
	}
	if r = f.do("GET", "/v1/m/sessions/runs", token, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("list = %d %s", r.code, r.raw)
	}
}
