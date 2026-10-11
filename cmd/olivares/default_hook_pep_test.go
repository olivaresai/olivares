// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// discardHookAuditor drops the per-decision log; the tests assert the governed ledger.
type discardHookAuditor struct{}

func (discardHookAuditor) Record(context.Context, claude.HookDecisionInput, claude.HookDecisionResult, bool) {
}

func TestDefaultHookPEPMountedWithoutOperatorConfig(t *testing.T) {
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", "")
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Logger: discardLog(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	server, err := buildClaudeHookPEPServer(eng, discardLog())
	if err != nil || server == nil {
		t.Fatalf("default PEP not mounted: %v", err)
	}
	if server.Addr != defaultHookPEPListen {
		t.Fatalf("addr: %s", server.Addr)
	}
	if eng.sessionHooks == nil {
		t.Fatal("launch credentials not wired")
	}
}

func TestDefaultHookPEPAllowDenyKillSwitchAndAudit(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	principal, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, principal, tenant, "hook-real-http")
	token, err := c.mintForPrincipal(principal, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	stops := h.set.gov
	dec := newClaudeHookDecider(&hookpep.Decider{Tenants: map[model.TenantID]hookpep.ResolvedTenant{}, DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Stops: stops, StopDeny: newStopDenyRecorder(h.st, discardLog()).record, Clock: time.Now, Log: discardLog()})
	server := httptest.NewServer(claude.NewHookPEP(dec, discardHookAuditor{}, time.Now))
	defer server.Close()
	call := func(tool string) string {
		body, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "vendor-session", "tool_name": tool, "tool_use_id": model.NewID().String(), "tool_input": map[string]any{"file_path": "/tmp/fixture"}})
		var out bytes.Buffer
		if err := claude.RunHookClient(context.Background(), bytes.NewReader(body), &out, claude.HookClientConfig{Endpoint: server.URL, Token: token, Tenant: tenant.String()}); err != nil {
			t.Fatal(err)
		}
		var wire struct {
			HookSpecificOutput struct {
				Decision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &wire); err != nil {
			t.Fatal(err)
		}
		return wire.HookSpecificOutput.Decision
	}
	if got := call("Read"); got != "allow" {
		t.Fatalf("default Read: %s", got)
	}
	code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{
		"name": "deny-shell-test", "kind": "abac", "enabled": true,
		"spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "claude.tool.use:use"}}},
	})
	if code != 201 {
		t.Fatalf("author deny policy: %d %s", code, raw)
	}
	if got := call("Bash"); got != "deny" {
		t.Fatalf("denied Bash: %s", got)
	}
	code, raw = h.req("POST", "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": "estate", "reason": "hook proof"})
	if code != 201 && code != 200 {
		t.Fatalf("engage kill switch: %d %s", code, raw)
	}
	if got := call("Read"); got != "deny" {
		t.Fatalf("kill switch Read: %s", got)
	}
	events := canonicalLedgerEventsFrom(t, h.st, tenant, 0)
	decisions := map[string]int{}
	for _, ev := range events {
		if strings.HasPrefix(ev.event.Action, "hook.tool.") {
			if ev.meta["session_ref"] != intent.ClaimSID || ev.meta["run_ref"] != intent.RunRef || ev.event.Actor != "session:"+intent.ClaimSID {
				t.Fatalf("hook audit lost its bound session identity: %+v %+v", ev.event, ev.meta)
			}
			value, _ := ev.meta["decision"].(string)
			decisions[value]++
		}
	}
	if decisions["allow"] != 1 || decisions["deny"] != 2 {
		t.Fatalf("audit decisions: %v", decisions)
	}
}

func claimHookTestSession(t *testing.T, h *harness, p auth.Principal, tenant model.TenantID, run string) sessions.LaunchIntent {
	t.Helper()
	hookTestRunID(t, h.st, tenant, run)
	sid, err := h.set.sessions.ResolveSession(context.Background(), tenant, sessions.SessionBinding{Provider: sessions.ProviderOperated, ExternalID: run, Origin: sessions.OriginOperated})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := h.set.sessions.Claim(context.Background(), tenant, sid, p.Actor(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return sessions.LaunchIntent{PermissionMode: "dontAsk", TemplateBuiltin: true, AllowedTools: []string{"Read", "Edit", "Write", "Bash"}, RunRef: run, Actor: p.Actor(), ActorKind: p.ActorKind(), ClaimSID: sid, Holder: lease.Holder, Fence: lease.Fence}
}

// hookTestRunID stores the run row a launched session names, as the runtime does
// before it mints the session's credential, and returns the row's stored ID.
func hookTestRunID(t *testing.T, st store.Store, tenant model.TenantID, run string) model.ID {
	t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		rows, _, err := runs.List(ctx, model.Query{Filters: []model.Filter{{Column: "run_ref", Op: model.OpEq, Value: run}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 1 {
			id = model.ID(rows[0].String(model.ColID))
			return nil
		}
		workspace, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		row, err := runs.Create(ctx, model.Record{"run_ref": run, "transport": "stream-json", "permission_mode": "", "isolation": "native", "state": "stopped", "last_event_seq": int64(0), "authz_workspace_id": workspace.ID.String()})
		if err != nil {
			return err
		}
		id = model.ID(row.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if id.IsZero() || id.String() == run {
		t.Fatal("fixture run reference must name a distinct stored row")
	}
	return id
}

func TestSessionHookCredentialIsScopedRotatedAndRevoked(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	p, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, p, tenant, "rotation-run")
	first, err := c.mintForPrincipal(p, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.mintForPrincipal(p, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("credential reused")
	}
	if _, err := c.Authenticate(context.Background(), first); err == nil {
		t.Fatal("previous launch credential accepted")
	}
	if _, err := c.Authenticate(context.Background(), second); err != nil {
		t.Fatalf("current credential: %v", err)
	}
	dec := newClaudeHookDecider(&hookpep.Decider{Authr: c, DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Store: h.st, Log: discardLog()})
	input := hookLedgerInput(model.TenantID(h.tenantB), "Read", "file", "/tmp/a", "read")
	result, err := dec.Decide(context.Background(), input, second)
	if err != nil || result.Permission != "deny" {
		t.Fatalf("cross-tenant: %+v %v", result, err)
	}
	if err := h.set.sessions.Release(context.Background(), tenant, intent.ClaimSID, intent.Holder, intent.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(context.Background(), second); err == nil {
		t.Fatal("released session claim accepted")
	}
	intent = claimHookTestSession(t, h, p, tenant, "revoke-run")
	second, err = c.mintForPrincipal(p, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := h.authr.RevokeSession(context.Background(), p, p.CredID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(context.Background(), second); err == nil {
		t.Fatal("revoked parent accepted")
	}
}

func TestSharedSessionCredentialCarriesBoundScope(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	p, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	intent := claimHookTestSession(t, h, p, tenant, "shared-session")
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	token, err := c.mintForPrincipal(p, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	principal, scope, err := c.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Superadmin || len(principal.Tenants()) != 1 || scope.TenantID != tenant || scope.RunRef != intent.RunRef || scope.SessionRef != intent.ClaimSID || principal.SessionRunRef != intent.RunRef {
		t.Fatalf("unbound session principal: %+v %+v", principal, scope)
	}
	_, runScope, err := c.ResolveRun(context.Background(), tenant, intent.RunRef)
	if err != nil || !reflect.DeepEqual(runScope, scope) {
		t.Fatalf("resolve run: %+v %v", runScope, err)
	}
	c.Revoke(tenant, intent.RunRef)
	if _, err := c.Authenticate(context.Background(), token); err == nil {
		t.Fatal("revoked shared token accepted")
	}
}

func TestSessionHookPEPRespectsLauncherRoleReduction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	admin, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	user, err := h.authr.CreateUser(ctx, admin, auth.NewUser{Email: "hook-editor@example.invalid", Password: "hook-editor-password", Tenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	login, _, err := h.authr.Login(ctx, user.Email, "hook-editor-password", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.authr.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, p, tenant, "role-reduction")
	token, err := c.mintForPrincipal(p, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	dec := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Log: discardLog()})
	input := hookLedgerInput(tenant, "Write", "file", "/tmp/fixture", "write")
	verdict, err := dec.Decide(ctx, input, token)
	if err != nil || verdict.Permission != "allow" {
		t.Fatalf("editor: %+v %v", verdict, err)
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: user.ID.String()}}, Limit: 10})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TargetTenantID == tenant {
				row.Role = auth.RoleViewer
				_, err = as.Memberships().Update(ctx, row)
				return err
			}
		}
		return store.ErrNotFound
	}); err != nil {
		t.Fatal(err)
	}
	verdict, err = dec.Decide(ctx, input, token)
	if err != nil || verdict.Permission != "deny" {
		t.Fatalf("viewer retained edit authority: %+v %v", verdict, err)
	}
}

// harnessAuthz is the request authorizer boot wires as the engine's authz, over the
// harness's governance: the one a session PEP asks about its launcher's authority.
func harnessAuthz(h *harness) *auth.Authorizer {
	return auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants()))
}

// scopedGrantLauncher is a viewer in tenant A whose only authority to run sessions is
// an authored scoped grant. It returns the viewer, a session claimed by the viewer
// and that session's hook credential.
func scopedGrantLauncher(t *testing.T, h *harness) (*sessionHookCredentials, sessions.LaunchIntent, string) {
	t.Helper()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	admin, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	user, err := h.authr.CreateUser(ctx, admin, auth.NewUser{Email: "hook-scoped@example.invalid", Password: "hook-scoped-password", Tenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	testsupport.SeedCedar(t, h.st, tenant, `permit(principal in User::"`+user.ID.String()+`", action == Action::"sessions:run:write", resource);`, h.set.gov)
	login, _, err := h.authr.Login(ctx, user.Email, "hook-scoped-password", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.authr.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, p, tenant, "scoped-grant")
	token, err := c.mintForPrincipal(p, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	return c, intent, token
}

// TestScopedGrantLauncherIsAdmittedAtToolCalls: a principal whose authority to run a
// session comes only from a scoped grant is admitted by the launch's authorizer, so its
// first tool call must not be refused as if it had none. Tool policy still narrows it.
func TestScopedGrantLauncherIsAdmittedAtToolCalls(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	c, intent, token := scopedGrantLauncher(t, h)
	authz := harnessAuthz(h)
	p, scope, err := c.ResolveRun(ctx, tenant, intent.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	launch := auth.Request{Principal: p, Tenant: tenant, Permission: "sessions:run:write",
		Resource: auth.ResourceAttrs{Kind: auth.Permission("sessions:run:write").Resource(), ID: hookTestRunID(t, h.st, tenant, scope.RunRef).String(), WorkspaceID: scope.WorkspaceID}}
	if d := authz.Authorize(ctx, launch); !d.Allow || !strings.Contains(d.Reason, "scoped grant") {
		t.Fatalf("the scoped grant does not admit the launch question: %+v", d)
	}

	t.Run("hook PEP", func(t *testing.T) {
		dec := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Scoped: h.set.gov.ScopedGrants(), Authz: authz, Store: h.st, Log: discardLog()})
		input := hookLedgerInput(tenant, "Write", "file", "/tmp/fixture", "write")
		if verdict, err := dec.Decide(ctx, input, token); err != nil || verdict.Permission != "allow" {
			t.Fatalf("first tool call of a scoped-grant launcher: %+v %v", verdict, err)
		}
		// Tool policy can still only narrow.
		dec.DefaultPolicy = &hookpep.PolicyDoc{Default: "deny"}
		if verdict, err := dec.Decide(ctx, input, token); err != nil || verdict.Permission != "deny" || strings.Contains(verdict.Reason, "launcher") {
			t.Fatalf("tool policy widened by the scoped grant: %+v %v", verdict, err)
		}
	})

	t.Run("provider approval", func(t *testing.T) {
		g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), authz: authz, approvals: h.set.gov.EngineApprovals(), store: h.st}
		req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p, Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "go test ./...", FactsComplete: true}
		if out, err := g.Decide(ctx, tenant, req); err != nil || out.Disposition != sessions.ProviderApprovalAllow {
			t.Fatalf("first provider action of a scoped-grant launcher: %+v %v", out, err)
		}
		code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "deny-codex-scoped", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "codex.tool.use:use"}}}})
		if code != 201 {
			t.Fatalf("policy: %d %s", code, raw)
		}
		if out, err := g.Decide(ctx, tenant, req); err != nil || out.Disposition != sessions.ProviderApprovalDeny || strings.Contains(out.Reason, "launcher") {
			t.Fatalf("tool policy widened by the scoped grant: %+v %v", out, err)
		}
	})
}
