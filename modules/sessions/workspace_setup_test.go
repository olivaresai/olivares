// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWorkspaceSetupApprovalBindsExactTextAndGuardsWorktreeStart(t *testing.T) {
	runner := &setupRunner{session: &fakeRunner{}}
	m := New(WithRunner(runner), WithCredentialSource(staticCred()))
	root := t.TempDir()
	if err := m.UseSessionWorktrees(root, ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "setup-command")
	repo := gitRepoFixture(t)
	marker := filepath.Join(repo, "must-not-run")
	command := "touch " + marker
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register: %d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	path := "/v1/m/sessions/workspaces/" + ref
	set := func(text string) {
		t.Helper()
		r := h.doJSON("PUT", path+"/setup-command", admin, map[string]any{"command": text}, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["setup_command"] != text {
			t.Fatalf("set: %d %s", r.code, r.raw)
		}
		r = h.do("GET", path, admin, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["setup_command"] != text || r.body["setup_command_sha256"] != sha256Hex([]byte(text)) {
			t.Fatalf("read: %d %s", r.code, r.raw)
		}
		if r.body["setup_approved"] != false {
			t.Fatalf("text set after the last approval reads as approved: %s", r.raw)
		}
	}
	launch := func(worktree, skip bool, want int) {
		t.Helper()
		r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "workspace_ref": ref, "worktree": worktree, "no_setup": skip}, tenantHdr(tenant))
		if r.code != want {
			t.Fatalf("start worktree=%v skip=%v: %d %s, want %d", worktree, skip, r.code, r.raw, want)
		}
		if want != http.StatusCreated && (!strings.Contains(string(r.raw), "touch") || !strings.Contains(string(r.raw), "approve-setup")) {
			t.Fatalf("refusal lacks command/next step: %s", r.raw)
		}
	}
	set(command)
	launch(true, false, http.StatusConflict)
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("refused start created worktree: %v %v", entries, err)
	}
	launch(false, false, http.StatusCreated)
	launch(true, true, http.StatusCreated)
	launch(true, false, http.StatusConflict)
	hash := sha256Hex([]byte(command))
	for _, bad := range []string{"", hash[:63], strings.Repeat("A", 64), hash[:63] + "g"} {
		r := h.doJSON("POST", path+"/setup-command/approve", admin, map[string]any{"sha256": bad}, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Fatalf("approve sha256=%q: %d %s, want 400", bad, r.code, r.raw)
		}
	}
	approved := h.doJSON("POST", path+"/setup-command/approve", admin, map[string]any{"sha256": hash}, tenantHdr(tenant))
	if approved.code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.code, approved.raw)
	}
	if approved.body["setup_approved"] != true {
		t.Fatalf("approved text reads as not approved: %s", approved.raw)
	}
	approval, ok := approved.body["setup_approval"].(map[string]any)
	if !ok || approval["sha256"] != hash || approval["actor"] == "" {
		t.Fatalf("approval missing identity/hash: %s", approved.raw)
	}
	if _, err := time.Parse(time.RFC3339Nano, approval["at"].(string)); err != nil {
		t.Fatal(err)
	}
	launch(true, false, http.StatusCreated)
	set(command + " ") // one byte: even trailing whitespace invalidates the approval.
	launch(true, false, http.StatusConflict)
	void := h.do("GET", path, admin, tenantHdr(tenant))
	if kept, ok := void.body["setup_approval"].(map[string]any); !ok || kept["sha256"] != hash || void.body["setup_approved"] != false {
		t.Fatalf("void approval must stay as evidence and read as not approved: %s", void.raw)
	}
	stale := h.doJSON("POST", path+"/setup-command/approve", admin, map[string]any{"sha256": hash}, tenantHdr(tenant))
	if stale.code != http.StatusConflict {
		t.Fatalf("stale approval accepted: %d %s", stale.code, stale.raw)
	}
	approved = h.doJSON("POST", path+"/setup-command/approve", admin, map[string]any{"sha256": sha256Hex([]byte(command + " "))}, tenantHdr(tenant))
	if approved.code != http.StatusOK || approved.body["setup_approved"] != true {
		t.Fatalf("approve new text: %d %s", approved.code, approved.raw)
	}
	launch(true, false, http.StatusCreated)
	var events []model.AuditEvent
	configured := 0
	if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		if report, err := sc.Audit().Verify(t.Context(), 0); err != nil {
			return err
		} else if !report.OK {
			t.Fatalf("audit chain: %s", report.Reason)
		}
		return sc.Audit().Walk(t.Context(), 0, func(ev model.AuditEvent) error {
			switch ev.Action {
			case "sessions.workspace.approve-setup":
				events = append(events, ev)
			case "sessions.workspace.configure-setup":
				configured++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Actor != approval["actor"] {
		t.Fatalf("approval audit: %+v", events)
	}
	if configured != 2 { // the command, then the command plus a space; the clear comes after this walk
		t.Fatalf("set-side audit entries = %d, want 2", configured)
	}
	want := workspacePayloadHash(wsMutationInput{op: "approve-setup", workspaceRef: ref, path: repo, contentHash: hash})
	found := false
	for _, event := range events {
		if bytes.Equal(event.PayloadHash, want[:]) {
			found = true
		}
	}
	if !found {
		t.Fatal("approval audit does not anchor exact command hash")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("setup command ran: %v", err)
	}
	set("")
	launch(true, false, http.StatusCreated)
}

func TestWorkspaceSetupRequiresAdministrationAndAtomicAudit(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "setup-admin")
	other := h.createOrg(admin, "setup-other")
	viewer := h.viewerToken(admin, tenant, "setup-viewer@example.invalid")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	ref := created.body["workspace_ref"].(string)
	path := "/v1/m/sessions/workspaces/" + ref + "/setup-command"
	for _, op := range []struct {
		method, path string
		body         map[string]any
	}{
		{"PUT", path, map[string]any{"command": "npm ci"}},
		{"POST", path + "/approve", map[string]any{"sha256": sha256Hex([]byte("npm ci"))}},
	} {
		for _, tc := range []struct {
			token  string
			tenant model.TenantID
			code   int
		}{{"", tenant, 401}, {viewer, tenant, 404}, {admin, other, 404}} {
			r := h.doJSON(op.method, op.path, tc.token, op.body, tenantHdr(tc.tenant))
			if r.code != tc.code {
				t.Fatalf("authority: %s %d %s want %d", op.method, r.code, r.raw, tc.code)
			}
		}
	}
	for _, body := range []map[string]any{{}, {"command": nil}, {"command": "x\x00y"}, {"command": strings.Repeat("x", 8193)}, {"command": "x", "setup_approval": map[string]any{}}} {
		r := h.doJSON("PUT", path, admin, body, tenantHdr(tenant))
		if r.code != 400 {
			t.Fatalf("invalid command: %d %s", r.code, r.raw)
		}
	}
	if r := h.doJSON("PUT", path, admin, map[string]any{"command": "npm ci"}, tenantHdr(tenant)); r.code != 200 {
		t.Fatalf("set: %d %s", r.code, r.raw)
	}
	agent := k2AgentToken(t, h, tenant, "setup-agent", "", auth.RoleOwner)
	if r := h.doJSON("POST", path+"/approve", agent, map[string]any{"sha256": sha256Hex([]byte("npm ci"))}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("agent approval: %d %s", r.code, r.raw)
	}
	if events := acctAuditsOf(t, m, tenant, "sessions.workspace.approve-setup"); len(events) != 0 {
		t.Fatal("agent approval left an audit")
	}
	storedBefore := h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if storedBefore.body["setup_approval"] != nil {
		t.Fatal("agent approved command")
	}
	original := m.Data
	m.Data = metadataRollbackData{original}
	r := h.doJSON("POST", path+"/approve", admin, map[string]any{"sha256": sha256Hex([]byte("npm ci"))}, tenantHdr(tenant))
	m.Data = original
	if r.code < 400 {
		t.Fatalf("audit failure approved: %d %s", r.code, r.raw)
	}
	stored := h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if stored.body["setup_approval"] != nil {
		t.Fatalf("failed approval persisted: %s", stored.raw)
	}
	if events := acctAuditsOf(t, m, tenant, "sessions.workspace.approve-setup"); len(events) != 0 {
		t.Fatal("failed approval left audit")
	}
	// The extension's additive fields are nullable: existing folders have no command.
	if err := h.st.Mutate(t.Context(), model.TenantID(tenant), func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := findWorkspaceRec(t.Context(), repo, ref)
		if err != nil {
			return err
		}
		rec["setup_command"] = nil
		_, err = repo.Update(t.Context(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stored = h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if stored.code != 200 || stored.body["setup_command"] != "" {
		t.Fatalf("upgrade default: %d %s", stored.code, stored.raw)
	}
}

func TestWorkspaceSetupDoesNotGateResume(t *testing.T) {
	h, ctx := newWorktreeHarness(t)
	run := h.launch(ctx, true)
	h.stop(ctx, run.RunRef)
	command := "touch must-not-run"
	if _, err := h.m.configureWorkspaceSetup(ctx, h.tenant, h.ws, &command, "", "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.resume(ctx, run.RunRef); err != nil {
		t.Fatalf("setup approval blocked resume: %v", err)
	}
}

func TestWorkspaceSetupRejectsLossyJSON(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "setup-unicode")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	ref := created.body["workspace_ref"].(string)
	path := "/v1/m/sessions/workspaces/" + ref
	if r := h.doJSON("PUT", path+"/setup-command", admin, map[string]any{"command": "keep me"}, tenantHdr(tenant)); r.code != 200 {
		t.Fatal(r.raw)
	}
	for _, raw := range []string{"{\"command\":\"echo \xff\"}", `{"command":"echo \ud800"}`, `{"command":"echo \udc00"}`, `{"command":"echo \ud800\u0041"}`} {
		req := httptest.NewRequest("PUT", path+"/setup-command", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("lossy command accepted: HTTP %d", rec.Code)
		}
		got := h.do("GET", path, admin, tenantHdr(tenant))
		if got.body["setup_command"] != "keep me" {
			t.Errorf("invalid request changed stored command: %s", got.raw)
		}
	}
	for _, tc := range []struct{ raw, text string }{
		{`{"command":"echo \ud83d\ude00"}`, "echo 😀"},
		{`{"command":"echo \ufffd"}`, "echo �"},
		{`{"command":"echo \/"}`, "echo /"},
		{`{"command":"echo \\ud800"}`, `echo \ud800`},
	} {
		req := httptest.NewRequest("PUT", path+"/setup-command", strings.NewReader(tc.raw))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("valid Unicode refused: HTTP %d %s", rec.Code, rec.Body.String())
		}
		got := h.do("GET", path, admin, tenantHdr(tenant))
		if got.body["setup_command"] != tc.text {
			t.Errorf("valid text changed: got %q want %q", got.body["setup_command"], tc.text)
		}
	}

}

// The start gate trusts an approval only when it names the stored text and
// carries who and when. Each variant here is one the gate must refuse.
func TestWorkspaceSetupStartGateRequiresACompleteMatchingApproval(t *testing.T) {
	const command = "npm ci"
	hash := sha256Hex([]byte(command))
	wantSentence := `Setup command "npm ci" needs approval; run olivares agent workspace approve-setup ws-1 --sha256 ` + hash + `, or start once with --no-setup.`
	approved := func(mutate func(*workspaceSetupApproval)) *workspaceSetupApproval {
		a := &workspaceSetupApproval{SHA256: hash, Actor: "user:u1", At: "2026-10-14T09:00:00Z"}
		mutate(a)
		return a
	}
	for _, tc := range []struct {
		name     string
		command  string
		approval *workspaceSetupApproval
		skip     bool
		refused  bool
	}{
		{"no command needs nothing", "", nil, false, false},
		{"complete matching approval", command, approved(func(*workspaceSetupApproval) {}), false, false},
		{"skip for this start", command, nil, true, false},
		{"no approval", command, nil, false, true},
		{"approval of other text", command, approved(func(a *workspaceSetupApproval) { a.SHA256 = sha256Hex([]byte(command + " ")) }), false, true},
		{"hash without who", command, approved(func(a *workspaceSetupApproval) { a.Actor = "" }), false, true},
		{"hash without when", command, approved(func(a *workspaceSetupApproval) { a.At = "" }), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := workspaceSetupStartError(&resolvedWorkspace{ref: "ws-1", setupCommand: tc.command, setupApproval: tc.approval}, tc.skip)
			switch {
			case !tc.refused && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.refused && (err == nil || err.Error() != wantSentence):
				t.Fatalf("refusal = %v, want %q", err, wantSentence)
			}
		})
	}
}
