// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/gitpublish"
	"github.com/olivaresai/olivares/modules/governance"
)

// recordingPublisher stands in for the gitpublish verbs: it records who
// published what, so the test sees exactly what an approval let through.
type recordingPublisher struct {
	mu      sync.Mutex
	budget  time.Duration
	calls   []gitpublish.Caller
	started []time.Time
	pushes  []gitpublish.PushInput
	prs     []gitpublish.PullRequestInput
}

func (r *recordingPublisher) PublicationBudget() time.Duration { return r.budget }

func (r *recordingPublisher) Push(_ context.Context, c gitpublish.Caller, in gitpublish.PushInput) (gitpublish.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls, r.started, r.pushes = append(r.calls, c), append(r.started, time.Now()), append(r.pushes, in)
	return gitpublish.Receipt{Intent: gitpublish.Intent{ID: model.NewID(), State: gitpublish.StateApplied, Proposal: in.Proposal}, Answer: gitpublish.StateApplied}, nil
}

func (r *recordingPublisher) OpenPullRequest(_ context.Context, c gitpublish.Caller, in gitpublish.PullRequestInput) (gitpublish.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls, r.started, r.prs = append(r.calls, c), append(r.started, time.Now()), append(r.prs, in)
	return gitpublish.Receipt{Intent: gitpublish.Intent{ID: model.NewID(), State: gitpublish.StateApplied, Proposal: in.Proposal}, Answer: gitpublish.StateApplied}, nil
}

// deadlineWriter is a response writer whose transport holds write deadlines,
// as the server's connection does: it records each one, or refuses it.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	set func(time.Time) error
}

func (w deadlineWriter) SetWriteDeadline(d time.Time) error { return w.set(d) }

func (r *recordingPublisher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// The agent proposes and a person confirms. The request waits in the approval
// queue as the session's own; the launcher approves it in the console, and only
// then the publication runs, once, as the launcher's exact credential with the
// session run and the approval attached. A declined or masked request publishes
// nothing.
func TestSessionPublishProposalWaitsForAPersonThenPublishesAsTheLauncher(t *testing.T) {
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ctx := t.Context()
	console := func(token, tenant, method, path string, body any) (int, map[string]any) {
		t.Helper()
		code, out, _ := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant, body)
		return code, out
	}
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, setup := console("", "", "POST", "/v1/setup", map[string]any{"token": setupToken, "email": "publish@olivares.ai", "password": "fixture-password-2026!", "organization": "Publish proposals"})
	organization, _ := setup["organization"].(map[string]any)
	tenantID, _ := organization["tenant_id"].(string)
	tenant, err := model.ParseTenantID(tenantID)
	if code != http.StatusCreated || err != nil {
		t.Fatalf("setup = %d %v", code, setup)
	}
	code, login := console("", "", "POST", "/v1/auth/login", map[string]any{"email": "publish@olivares.ai", "password": "fixture-password-2026!"})
	adminToken, _ := login["token"].(string)
	if code != http.StatusOK || adminToken == "" {
		t.Fatalf("login = %d", code)
	}
	admin, err := eng.authr.Authenticate(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	var workspace model.Workspace
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) (err error) {
		workspace, err = sc.DefaultWorkspace(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(eng.authr, func(context.Context, auth.SessionScope) error { return nil })
	run, sid := model.NewID().String(), "osn_"+model.NewID().String()
	bearer, err := issuer.Mint(ctx, admin, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace.ID, FolderRef: "fixture", SessionRef: sid, RunRef: run, Fence: 1})
	if err != nil {
		t.Fatal(err)
	}
	module := &recordingPublisher{budget: eng.gitpublish.PublicationBudget()}
	if module.budget < 4*time.Minute {
		t.Fatalf("gitpublish's publication budget is %s; the deadline checks below need its default phases", module.budget)
	}
	// Each write deadline the call sets on its transport, when it was set, or
	// the transport's refusal.
	type held struct{ at, until time.Time }
	var (
		heldMu   sync.Mutex
		holds    []held
		holdFail error
	)
	hold := func(d time.Time) error {
		heldMu.Lock()
		defer heldMu.Unlock()
		if holdFail != nil {
			return holdFail
		}
		holds = append(holds, held{time.Now(), d})
		return nil
	}
	var waits []string
	// spendFirst, when set, runs as the session starts waiting: it lets a
	// person decide and another consumer spend the approval first.
	var spendFirst func(ref string)
	// The engine's own publisher, as the session MCP edges receive it; only the
	// live turn and wait, which need a running provider, and the test's issuer
	// are replaced.
	publisher, ok := eng.sessionPublisher().(sessionPublications)
	if !ok || publisher.module != eng.gitpublish {
		t.Fatalf("the engine offers no session publisher: %#v", eng.sessionPublisher())
	}
	publisher.launcher = issuer.LauncherForRun
	publisher.beginCall = func(ctx context.Context, _ auth.Principal) (context.Context, func(), error) {
		return ctx, func() {}, nil
	}
	publisher.beginWait = func(_ context.Context, _ auth.Principal, ref string, _ time.Time) (func(), error) {
		waits = append(waits, ref)
		if spendFirst != nil {
			spendFirst(ref)
		}
		return func() {}, nil
	}
	real := publisher
	publisher.module = module
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, admits: eng.admits(), publisher: publisher}
	rpc := func(method string, params any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "publish", "method": method, "params": params})
		r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(string(raw))).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := deadlineWriter{httptest.NewRecorder(), hold}
		h.ServeHTTP(w, r)
		var out map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Errorf("%s = %d %s", method, w.Code, w.Body.String())
		}
		return out
	}
	listed := map[string]bool{}
	for _, tool := range rpc("tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any) {
		listed[tool.(map[string]any)["name"].(string)] = true
	}
	if !listed[publishPushTool] || !listed[publishPullRequestTool] {
		t.Fatalf("publish tools not listed: %v", listed)
	}
	// call proposes in the background and returns the tool's API answer.
	call := func(name string, args map[string]any) <-chan map[string]any {
		done := make(chan map[string]any, 1)
		go func() {
			out := rpc("tools/call", map[string]any{"name": name, "arguments": args})
			result, _ := out["result"].(map[string]any)
			structured, _ := result["structuredContent"].(map[string]any)
			done <- structured
		}()
		return done
	}
	pending := func(action string) governance.Approval {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			items, _, err := eng.engineApprovals.List(ctx, tenant, action, "pending", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) == 1 {
				return items[0]
			}
		}
		t.Fatalf("no pending %s approval", action)
		return governance.Approval{}
	}
	decide := func(id, decision string) {
		t.Helper()
		if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+id+"/decisions", map[string]any{"decision": decision}); code != http.StatusOK {
			t.Fatalf("%s = %d %v", decision, code, out)
		}
	}
	target := model.NewID().String()
	const commit, tree = "2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"

	// Approved: the launcher approves its own session's request in the console.
	answer := call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/run", "commit": commit, "tree": tree})
	approval := pending("gitpublish.push")
	if approval.SessionRef != sid || approval.RequestedBy != "session:"+sid || approval.Review == nil || !strings.Contains(approval.Review.Text, commit) || !strings.Contains(approval.Review.Text, run) {
		t.Fatalf("queued request = %+v", approval)
	}
	if module.count() != 0 {
		t.Fatal("published before a person decided")
	}
	decided := time.Now()
	decide(approval.ID, "approve")
	got := <-answer
	if status, _ := got["http_status"].(float64); status != http.StatusOK {
		t.Fatalf("approved push answered %v", got)
	}
	if module.count() != 1 {
		t.Fatalf("approved push published %d times", module.count())
	}
	// The receipt outlives gitpublish's own phases counted after the person
	// decided, not from the start of the call: the last deadline set before
	// the run was set after the decision and covers the module's whole budget.
	heldMu.Lock()
	var last held
	for _, d := range holds {
		if !d.at.After(module.started[0]) {
			last = d
		}
	}
	heldMu.Unlock()
	if last.at.Before(decided) || last.until.Before(module.started[0].Add(module.budget)) {
		t.Fatalf("deadline before the run = %+v (decided %v, run %v), want at least the run plus %s, set after the decision", last, decided, module.started[0], module.budget)
	}
	c, in := module.calls[0], module.pushes[0]
	wantRef, _ := admin.Ref()
	if ref, ok := c.Principal.Ref(); !ok || ref != wantRef || c.Principal.SessionIdentity != "" || c.Tenant != tenant {
		t.Fatalf("published as %+v, want the launcher's exact credential", c.Principal)
	}
	want := gitpublish.Proposal{SessionRun: run, Workspace: workspace.ID, Approval: approval.ID}
	if in.Proposal != want || in.SessionRun != run || in.OperationID != "proposal:"+approval.ID || in.Commit != commit || in.Target != model.ID(target) {
		t.Fatalf("push input = %+v", in)
	}
	if len(waits) != 1 || waits[0] != approval.ID {
		t.Fatalf("session waits = %v", waits)
	}
	if spent, err := eng.engineApprovals.Consume(ctx, tenant, approval.ID, newSingleUseConsumerID(), ""); err != nil || spent.Granted {
		t.Fatalf("approval spendable again: %+v %v", spent, err)
	}

	// Declined: nothing publishes, and the agent hears why.
	answer = call(publishPullRequestTool, map[string]any{"target_id": target, "head_ref": "olivares/run", "base": "main", "commit": commit, "title": "Agent change"})
	approval = pending("gitpublish.pull_request")
	decide(approval.ID, "reject")
	got = <-answer
	body, _ := got["body"].(map[string]any)
	refusal, _ := body["error"].(map[string]any)
	if status, _ := got["http_status"].(float64); status != http.StatusForbidden || refusal["code"] != "approval_rejected" || module.count() != 1 {
		t.Fatalf("declined pull request = %v, published %d", got, module.count())
	}

	// Masked: what a reviewer would see differs from what would run.
	got = <-call(publishPullRequestTool, map[string]any{"target_id": target, "head_ref": "olivares/run", "base": "main", "commit": commit,
		"title": "Leak", "body": "token " + "ghp" + "_" + strings.Repeat("a1B2c3D4e5", 4) + "f6"})
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "not_reviewable" || module.count() != 1 {
		t.Fatalf("masked pull request = %v, published %d", got, module.count())
	}
	if items, _, _ := eng.engineApprovals.List(ctx, tenant, "gitpublish.pull_request", "pending", ""); len(items) != 0 {
		t.Fatalf("masked request left pending: %+v", items)
	}

	// Spent elsewhere: an approval another consumer already used publishes nothing.
	spendFirst = func(ref string) {
		if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+ref+"/decisions", map[string]any{"decision": "approve"}); code != http.StatusOK {
			t.Errorf("approve = %d %v", code, out)
		}
		if spent, err := eng.engineApprovals.Consume(ctx, tenant, ref, newSingleUseConsumerID(), ""); err != nil || !spent.Granted {
			t.Errorf("first spend = %+v %v", spent, err)
		}
	}
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/other", "commit": commit, "tree": tree})
	spendFirst = nil
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "approval_spent" || module.count() != 1 {
		t.Fatalf("spent approval = %v, published %d", got, module.count())
	}

	// A transport that cannot hold the reply open is refused before anything
	// queues: an effect whose answer would be cut never starts.
	heldMu.Lock()
	holdFail = errors.New("deadline refused")
	heldMu.Unlock()
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/held", "commit": commit, "tree": tree})
	heldMu.Lock()
	holdFail = nil
	heldMu.Unlock()
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "approval_unavailable" || module.count() != 1 {
		t.Fatalf("unheld reply = %v, published %d", got, module.count())
	}
	if items, _, _ := eng.engineApprovals.List(ctx, tenant, "gitpublish.push", "pending", ""); len(items) != 0 {
		t.Fatalf("an unheld reply queued: %+v", items)
	}

	// A reply that cannot be held after a person approved publishes nothing
	// and spends nothing; the answer names the approval.
	var unheldRef string
	spendFirst = func(ref string) {
		unheldRef = ref
		if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+ref+"/decisions", map[string]any{"decision": "approve"}); code != http.StatusOK {
			t.Errorf("approve = %d %v", code, out)
		}
		heldMu.Lock()
		holdFail = errors.New("deadline refused")
		heldMu.Unlock()
	}
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/held-late", "commit": commit, "tree": tree})
	spendFirst = nil
	heldMu.Lock()
	holdFail = nil
	heldMu.Unlock()
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if message, _ := refusal["message"].(string); refusal["code"] != "approval_unavailable" || !strings.Contains(message, unheldRef) || module.count() != 1 {
		t.Fatalf("reply unheld after the approval = %v, published %d", got, module.count())
	}
	if spent, err := eng.engineApprovals.Consume(ctx, tenant, unheldRef, newSingleUseConsumerID(), ""); err != nil || !spent.Granted {
		t.Fatalf("an unheld reply spent the approval: %+v %v", spent, err)
	}

	// An argument the tool does not list is refused before anything queues.
	out := rpc("tools/call", map[string]any{"name": publishPushTool, "arguments": map[string]any{"target_id": target, "ref": "refs/heads/olivares/run", "commit": commit, "tree": tree, "session_run": run}})
	if out["error"] == nil {
		t.Fatalf("unknown argument accepted: %v", out)
	}
	// A target that is not an id is refused before anything queues.
	for what, args := range map[string]map[string]any{
		"target":  {"target_id": "widgets", "ref": "refs/heads/olivares/run", "commit": commit, "tree": tree},
		"ref":     {"target_id": target, "ref": "olivares/run", "commit": commit, "tree": tree},
		"commit":  {"target_id": target, "ref": "refs/heads/olivares/run", "commit": "HEAD", "tree": tree},
		"no tree": {"target_id": target, "ref": "refs/heads/olivares/run", "commit": commit},
	} {
		out := rpc("tools/call", map[string]any{"name": publishPushTool, "arguments": args})
		if rpcErr, _ := out["error"].(map[string]any); rpcErr == nil || !strings.Contains(rpcErr["message"].(string), "invalid") {
			t.Errorf("invalid %s = %v", what, out)
		}
	}
	// A title that would forge review lines is refused the same way.
	out = rpc("tools/call", map[string]any{"name": publishPullRequestTool, "arguments": map[string]any{"target_id": target, "head_ref": "olivares/run", "base": "main",
		"commit": commit, "title": "Fix\nbase: release"}})
	if rpcErr, _ := out["error"].(map[string]any); rpcErr == nil || !strings.Contains(rpcErr["message"].(string), "one line") {
		t.Errorf("multi-line title = %v", out)
	}
	for _, action := range []string{"gitpublish.push", "gitpublish.pull_request"} {
		if items, _, _ := eng.engineApprovals.List(ctx, tenant, action, "pending", ""); len(items) != 0 {
			t.Fatalf("an invalid request queued: %+v", items)
		}
	}

	// One request waits at a time per session.
	answer = call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/first", "commit": commit, "tree": tree})
	first := pending("gitpublish.push")
	got = <-call(publishPullRequestTool, map[string]any{"target_id": target, "head_ref": "olivares/first", "base": "main", "commit": commit, "title": "Second"})
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "proposal_pending" {
		t.Fatalf("second waiting request = %v", got)
	}
	decide(first.ID, "reject")
	<-answer

	// The real admission admits the launcher the approval runs as: its exact
	// credential, rebuilt and authorized for a push in the session's workspace.
	session, err := issuer.Authenticate(ctx, bearer)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := issuer.LauncherForRun(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := launcher.Ref()
	admitCtx, cancelAdmit := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAdmit()
	resolved, err := eng.authr.ResolvePrincipalScope(admitCtx, ref, tenant)
	if err != nil {
		t.Fatal(err)
	}
	push := auth.Permission("gitpublish:push:write")
	if _, err := eng.authz.AuthorizeRouteMutation(admitCtx, auth.Request{Principal: resolved, Permission: push, Tenant: tenant,
		Resource: auth.ResourceAttrs{Kind: push.Resource(), ID: target, WorkspaceID: workspace.ID}, Route: auth.RouteMetadata{CedarAction: "publication:push"}}); err != nil {
		t.Fatalf("the launcher is not admitted for a push: %v", err)
	}

	// Through the engine's own gitpublish module the approved request runs as
	// the launcher: its answer is the module's, never runtime_credential_refused.
	h.publisher = real
	answer = call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/real", "commit": commit, "tree": tree})
	decide(pending("gitpublish.push").ID, "approve")
	got = <-answer
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "not_found" {
		t.Fatalf("real module answered %v, want not_found for a target that does not exist", got)
	}

	// The session ends after the approval: nothing publishes, and the approval
	// is left unspent.
	h.publisher = publisher
	var endedRef string
	spendFirst = func(ref string) {
		endedRef = ref
		if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+ref+"/decisions", map[string]any{"decision": "approve"}); code != http.StatusOK {
			t.Errorf("approve = %d %v", code, out)
		}
		issuer.Revoke(tenant, run)
	}
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/ended", "commit": commit, "tree": tree})
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "session_access_ended" || module.count() != 1 {
		t.Fatalf("ended session = %v, published %d", got, module.count())
	}
	if spent, err := eng.engineApprovals.Consume(ctx, tenant, endedRef, newSingleUseConsumerID(), ""); err != nil || !spent.Granted {
		t.Fatalf("the ended session spent its approval: %+v %v", spent, err)
	}

	// A new generation of the same run for the cases below.
	if bearer, err = issuer.Mint(ctx, admin, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace.ID, FolderRef: "fixture", SessionRef: sid, RunRef: run, Fence: 1}); err != nil {
		t.Fatal(err)
	}
	approveOnWait := func(ref string) {
		if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+ref+"/decisions", map[string]any{"decision": "approve"}); code != http.StatusOK {
			t.Errorf("approve = %d %v", code, out)
		}
	}

	// A launcher that cannot be read for a reason other than an ended session
	// is the service's failure, not the session's end: the approval stays unspent.
	var faultRef string
	spendFirst = func(ref string) { faultRef = ref; approveOnWait(ref) }
	faulty := publisher
	faulty.launcher = func(context.Context, auth.Principal) (auth.Principal, error) {
		return auth.Principal{}, errors.New("store fault")
	}
	h.publisher = faulty
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/fault", "commit": commit, "tree": tree})
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "approval_unavailable" || module.count() != 1 {
		t.Fatalf("unreadable launcher = %v, published %d", got, module.count())
	}
	if spent, err := eng.engineApprovals.Consume(ctx, tenant, faultRef, newSingleUseConsumerID(), ""); err != nil || !spent.Granted {
		t.Fatalf("an unreadable launcher spent the approval: %+v %v", spent, err)
	}

	// A decision read as the wait window closes is carried out: the launcher
	// and the spend do not run on the ended wait.
	spendFirst = approveOnWait
	late := publisher
	var endWindow context.CancelFunc
	late.beginCall = func(ctx context.Context, _ auth.Principal) (context.Context, func(), error) {
		ctx, endWindow = context.WithCancel(ctx)
		return ctx, endWindow, nil
	}
	late.launcher = func(ctx context.Context, p auth.Principal) (auth.Principal, error) {
		endWindow()
		return issuer.LauncherForRun(ctx, p)
	}
	h.publisher = late
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/late", "commit": commit, "tree": tree})
	if got["http_status"] != float64(http.StatusOK) || module.count() != 2 {
		t.Fatalf("decision at the window's end = %v, published %d", got, module.count())
	}
	spendFirst = nil

	// A person who decides as the wait ends is answered, not told nobody
	// decided: the wait comes back with the deadline and the approval.
	closing := publisher
	closing.beginCall = func(ctx context.Context, _ auth.Principal) (context.Context, func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		return ctx, cancel, nil
	}
	closing.beginWait = func(_ context.Context, _ auth.Principal, ref string, deadline time.Time) (func(), error) {
		approveOnWait(ref)
		time.Sleep(time.Until(deadline) + 50*time.Millisecond)
		return func() {}, nil
	}
	h.publisher = closing
	got = <-call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/closing", "commit": commit, "tree": tree})
	if got["http_status"] != float64(http.StatusOK) || module.count() != 3 {
		t.Fatalf("decision as the wait ends = %v, published %d", got, module.count())
	}

	// One pending request per session holds past the first page of the queue.
	h.publisher = publisher
	for i := range 200 {
		other := "osn_other_" + strconv.Itoa(i)
		if _, err := eng.engineApprovals.Request(ctx, tenant, auth.Principal{SessionIdentity: other}, governance.ApprovalRequest{
			SessionRef: other, Action: "gitpublish.push", SubjectKind: publishSubjectKind, SubjectRef: "other-" + strconv.Itoa(i), Reason: "another session"}); err != nil {
			t.Fatal(err)
		}
	}
	answer = call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/last", "commit": commit, "tree": tree})
	var own string
	for deadline := time.Now().Add(10 * time.Second); own == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for cursor, more := "", true; more && own == ""; {
			items, page, err := eng.engineApprovals.List(ctx, tenant, "gitpublish.push", "pending", cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.SessionRef == sid {
					own = item.ID
				}
			}
			cursor, more = page.Cursor, page.HasMore
		}
	}
	if first, _, _ := eng.engineApprovals.List(ctx, tenant, "gitpublish.push", "pending", ""); own == "" || slices.ContainsFunc(first, func(a governance.Approval) bool { return a.ID == own }) {
		t.Fatalf("the session's request is not past the first page (%q)", own)
	}
	got = <-call(publishPullRequestTool, map[string]any{"target_id": target, "head_ref": "olivares/last", "base": "main", "commit": commit, "title": "Second"})
	body, _ = got["body"].(map[string]any)
	refusal, _ = body["error"].(map[string]any)
	if refusal["code"] != "proposal_pending" {
		t.Fatalf("second request with the first past page one = %v", got)
	}
	decide(own, "reject")
	<-answer
}
