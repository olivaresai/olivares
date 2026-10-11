// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// storedRunSessions launches two sessions for the tenant admin and returns their
// intents, credentials and stored run row IDs, keyed "denied" and "sibling".
func storedRunSessions(t *testing.T, h *harness, mode string) (*sessionHookCredentials, map[string]sessions.LaunchIntent, map[string]string, map[string]model.ID) {
	t.Helper()
	tenant := model.TenantID(h.tenantA)
	admin, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intents, tokens, ids := map[string]sessions.LaunchIntent{}, map[string]string{}, map[string]model.ID{}
	for _, name := range []string{"denied", "sibling"} {
		intent := claimHookTestSession(t, h, admin, tenant, "stored-id-"+name)
		if mode != "" {
			intent.PermissionMode = mode
		}
		if tokens[name], err = c.mintForPrincipal(admin, tenant, intent); err != nil {
			t.Fatal(err)
		}
		intents[name], ids[name] = intent, hookTestRunID(t, h.st, tenant, intent.RunRef)
	}
	return c, intents, tokens, ids
}

// A Cedar forbid naming one session's stored run ID refuses that session's tool
// calls at the hook PEP, the resource the native run routes authorize, while a
// sibling session stays allowed.
func TestSessionHookAdmissionAuthorizesTheStoredRunID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	c, _, tokens, ids := storedRunSessions(t, h, "")
	testsupport.SeedCedar(t, h.st, tenant, `forbid(principal, action, resource == Resource::"`+ids["denied"].String()+`") when { context.permission == "sessions:run:write" && resource.kind == "run" };`, h.set.gov)
	var log bytes.Buffer
	dec := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Log: slog.New(slog.NewTextHandler(&log, nil))})
	input := hookLedgerInput(tenant, "Write", "file", "/tmp/fixture", "write")
	if verdict, err := dec.Decide(ctx, input, tokens["denied"]); err != nil || verdict.Permission != "deny" || !strings.Contains(verdict.Reason, "launcher") {
		t.Errorf("tool call of the forbidden session = %+v %v, want the launcher refusal", verdict, err)
	}
	// The policy refused, not a missing run: the operator's reason says which.
	if got := log.String(); !strings.Contains(got, "launcher's run authority refused") || strings.Contains(got, "could not be read") || strings.Contains(got, "session run is unknown") {
		t.Errorf("operator reason = %q, want the policy refusal", got)
	}
	if verdict, err := dec.Decide(ctx, input, tokens["sibling"]); err != nil || verdict.Permission != "allow" {
		t.Errorf("tool call of the sibling session = %+v %v", verdict, err)
	}
}

// A Cedar forbid naming one session's stored run ID refuses its provider approvals
// through the launcher's run question, while a sibling session stays allowed.
func TestProviderApprovalAuthorizesTheStoredRunID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	c, intents, _, ids := storedRunSessions(t, h, "")
	testsupport.SeedCedar(t, h.st, tenant, `forbid(principal, action, resource == Resource::"`+ids["denied"].String()+`") when { context.permission == "sessions:run:write" && resource.kind == "run" };`, h.set.gov)
	g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), authz: harnessAuthz(h), approvals: h.set.gov.EngineApprovals(), store: h.st}
	for name, want := range map[string]sessions.ProviderApprovalDisposition{"denied": sessions.ProviderApprovalDeny, "sibling": sessions.ProviderApprovalAllow} {
		p, scope, err := c.ResolveRun(ctx, tenant, intents[name].RunRef)
		if err != nil {
			t.Fatal(err)
		}
		req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intents[name].RunRef, SessionRef: scope.SessionRef, Principal: p, Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "go test ./...", FactsComplete: true}
		out, err := g.Decide(ctx, tenant, req)
		if err != nil || out.Disposition != want || (want == sessions.ProviderApprovalDeny && !strings.Contains(out.Reason, "launcher")) {
			t.Errorf("provider action of the %s session = %+v %v, want %s", name, out, err, want)
		}
	}
}

// The full-permissions question asks the live policy about the session's run as
// the native routes name it: kind "run" and its stored ID. The launcher's own
// authorizer carries no forbid here, so only that question can refuse.
func TestProviderFullPermissionsAskAboutTheStoredRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	c, intents, _, ids := storedRunSessions(t, h, "bypassPermissions")
	eval, err := governance.NewCedarEvaluator(`forbid(principal, action, resource == Resource::"`+ids["denied"].String()+`") when { context.permission == "sessions:run:admin" && resource.kind == "run" };`, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: eval, authz: harnessAuthz(h), approvals: h.set.gov.EngineApprovals(), store: h.st}
	for name, want := range map[string]sessions.ProviderApprovalDisposition{"denied": sessions.ProviderApprovalDeny, "sibling": sessions.ProviderApprovalAllow} {
		p, scope, err := c.ResolveRun(ctx, tenant, intents[name].RunRef)
		if err != nil || scope.Preset != sessions.PresetFull {
			t.Fatalf("full launch binding absent: %+v %v", scope, err)
		}
		req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intents[name].RunRef, SessionRef: scope.SessionRef, Principal: p, TurnID: "full-turn", Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "printf full-proof", FactsComplete: true}
		out, err := g.Decide(ctx, tenant, req)
		if err != nil || out.Disposition != want || (want == sessions.ProviderApprovalDeny && strings.Contains(out.Reason, "launcher")) {
			t.Errorf("full provider action of the %s session = %+v %v, want %s", name, out, err, want)
		}
	}
	// The launcher's own administration question names the same resource.
	testsupport.SeedCedar(t, h.st, tenant, `forbid(principal, action, resource == Resource::"`+ids["denied"].String()+`") when { context.permission == "sessions:run:admin" && resource.kind == "run" };`, h.set.gov)
	launcher := sessionProviderPolicy{credentials: c.SessionCredentials, eval: h.set.gov.Evaluator(), authz: harnessAuthz(h), approvals: h.set.gov.EngineApprovals(), store: h.st}
	for name, want := range map[string]sessions.ProviderApprovalDisposition{"denied": sessions.ProviderApprovalDeny, "sibling": sessions.ProviderApprovalAllow} {
		p, scope, err := c.ResolveRun(ctx, tenant, intents[name].RunRef)
		if err != nil {
			t.Fatal(err)
		}
		req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intents[name].RunRef, SessionRef: scope.SessionRef, Principal: p, TurnID: "full-turn", Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "printf full-proof", FactsComplete: true}
		out, err := launcher.Decide(ctx, tenant, req)
		if err != nil || out.Disposition != want || (want == sessions.ProviderApprovalDeny && !strings.Contains(out.Reason, "launcher's current authority does not permit full session permissions")) {
			t.Errorf("full provider action of the %s session under the launcher's forbid = %+v %v, want %s", name, out, err, want)
		}
	}
	// The sibling control: the same forbid evaluated directly refuses only the
	// denied run's native resource.
	for name, allow := range map[string]bool{"denied": false, "sibling": true} {
		d, err := eval.Evaluate(ctx, auth.Request{Tenant: tenant, Permission: "sessions:run:admin", Resource: auth.ResourceAttrs{Kind: "run", ID: ids[name].String()}})
		if err != nil || d.Allow != allow {
			t.Fatalf("control %s: %+v %v", name, d, err)
		}
	}
}

// A session whose run row cannot be read is refused at both the hook PEP and
// provider approval: no run, no run question, no allow.
func TestSessionRunQuestionsRefuseWhenTheRunIsGone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	c, intents, tokens, ids := storedRunSessions(t, h, "")
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		return runs.Delete(ctx, ids["denied"])
	}); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	dec := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Log: slog.New(slog.NewTextHandler(&log, nil))})
	input := hookLedgerInput(tenant, "Write", "file", "/tmp/fixture", "write")
	if verdict, err := dec.Decide(ctx, input, tokens["denied"]); err != nil || verdict.Permission != "deny" || !strings.Contains(log.String(), "session run could not be read") {
		t.Errorf("tool call of a session with no run = %+v %v, operator reason %q", verdict, err, log.String())
	}
	if verdict, err := dec.Decide(ctx, input, tokens["sibling"]); err != nil || verdict.Permission != "allow" {
		t.Errorf("tool call of the sibling session = %+v %v", verdict, err)
	}
	g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), authz: harnessAuthz(h), approvals: h.set.gov.EngineApprovals(), store: h.st}
	p, scope, err := c.ResolveRun(ctx, tenant, intents["denied"].RunRef)
	if err != nil {
		t.Fatal(err)
	}
	req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intents["denied"].RunRef, SessionRef: scope.SessionRef, Principal: p, Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "go test ./...", FactsComplete: true}
	if out, err := g.Decide(ctx, tenant, req); err != nil || out.Disposition != sessions.ProviderApprovalDeny || !strings.Contains(out.Reason, "session run is unavailable") {
		t.Errorf("provider action of a session with no run = %+v %v", out, err)
	}
}
