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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// A proposal is classified by what its commit changes only when an approval
// policy of the tenant matches by class. Then the class and its paths ride the
// card, a sensitive change needs the quorum its policy names, and a read that
// fails proposes nothing. Without such a policy the module is never asked.
func TestSessionPublishProposalIsClassifiedWhenAPolicyMatchesTheClass(t *testing.T) {
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
	code, setup := console("", "", "POST", "/v1/setup", map[string]any{"token": setupToken, "email": "classify@olivares.ai", "password": "fixture-password-2026!", "organization": "Classified proposals"})
	organization, _ := setup["organization"].(map[string]any)
	tenantID, _ := organization["tenant_id"].(string)
	tenant, err := model.ParseTenantID(tenantID)
	if code != http.StatusCreated || err != nil {
		t.Fatalf("setup = %d %v", code, setup)
	}
	code, login := console("", "", "POST", "/v1/auth/login", map[string]any{"email": "classify@olivares.ai", "password": "fixture-password-2026!"})
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
	publisher, ok := eng.sessionPublisher().(sessionPublications)
	if !ok {
		t.Fatalf("the engine offers no session publisher: %#v", eng.sessionPublisher())
	}
	publisher.launcher = issuer.LauncherForRun
	publisher.beginCall = func(ctx context.Context, _ auth.Principal) (context.Context, func(), error) {
		return ctx, func() {}, nil
	}
	publisher.beginWait = func(context.Context, auth.Principal, string, time.Time) (func(), error) { return func() {}, nil }
	publisher.module = module
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, admits: eng.admits(), publisher: publisher}
	call := func(name string, args map[string]any) <-chan map[string]any {
		done := make(chan map[string]any, 1)
		go func() {
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "publish", "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
			r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(string(raw))).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+bearer)
			w := deadlineWriter{httptest.NewRecorder(), func(time.Time) error { return nil }}
			h.ServeHTTP(w, r)
			var out map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			result, _ := out["result"].(map[string]any)
			structured, _ := result["structuredContent"].(map[string]any)
			done <- structured
		}()
		return done
	}
	pending := func() governance.Approval {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			items, _, err := eng.engineApprovals.List(ctx, tenant, "gitpublish.push", "pending", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) == 1 {
				return items[0]
			}
		}
		t.Fatal("no pending gitpublish.push approval")
		return governance.Approval{}
	}
	decide := func(id, decision string) {
		t.Helper()
		if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+id+"/decisions", map[string]any{"decision": decision}); code != http.StatusOK {
			t.Fatalf("%s = %d %v", decision, code, out)
		}
	}
	refusal := func(got map[string]any) (status float64, code any) {
		body, _ := got["body"].(map[string]any)
		detail, _ := body["error"].(map[string]any)
		status, _ = got["http_status"].(float64)
		return status, detail["code"]
	}
	target := model.NewID().String()
	const commit, tree = "2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"
	push := func(ref string) <-chan map[string]any {
		return call(publishPushTool, map[string]any{"target_id": target, "ref": "refs/heads/olivares/" + ref, "commit": commit, "tree": tree})
	}

	// No class policy: the module is never asked and the card is what it was.
	answer := push("plain")
	plain := pending()
	if plain.ChangeClass != "" || len(plain.ChangePaths) != 0 || plain.LaunchedBy != "" || strings.Contains(plain.Review.Text, "change class") || len(module.changeCallers) != 0 {
		t.Fatalf("unclassified card = %+v, module asked %d times", plain, len(module.changeCallers))
	}
	decide(plain.ID, "approve")
	if status, _ := (<-answer)["http_status"].(float64); status != http.StatusOK || module.count() != 1 {
		t.Fatalf("plain push answered %v, published %d", status, module.count())
	}

	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(ctx, model.Policy{Name: "sensitive publications need two", Kind: "approval", Enabled: true, Spec: map[string]any{
			"required_approvals": 2, "require_other_than_launcher": true,
			"match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Sensitive: the launcher's read names the paths; two people are needed and
	// the launcher is not one of them.
	module.changed = []string{"docs/a.md", "go.mod"}
	answer = push("sensitive")
	card := pending()
	if card.ChangeClass != "sensitive" || !reflect.DeepEqual(card.ChangePaths, []string{"go.mod"}) || card.RequiredApprovals != 2 ||
		card.LaunchedBy != "user:"+admin.UserID.String() || !strings.HasPrefix(card.Review.Text, "change class: sensitive\nchanged paths:\n- go.mod\n") {
		t.Fatalf("sensitive card = %+v", card)
	}
	if len(module.changeCallers) != 1 || module.changeWorkspaces[0] != workspace.ID || module.changeCallers[0].Principal.SessionIdentity != "" || module.changeCallers[0].Tenant != tenant {
		t.Fatalf("the read was asked as %+v in %v, want the launcher in the session's workspace", module.changeCallers, module.changeWorkspaces)
	}
	if code, out := console(adminToken, tenantID, "POST", "/v1/m/governance/approvals/"+card.ID+"/decisions", map[string]any{"decision": "approve"}); code != http.StatusForbidden {
		t.Fatalf("the launcher deciding its own sensitive change = %d %v, want 403", code, out)
	}
	decide(card.ID, "reject")
	if status, code := refusal(<-answer); status != http.StatusForbidden || code != "approval_rejected" || module.count() != 1 {
		t.Fatalf("rejected sensitive push = %v %v, published %d", status, code, module.count())
	}

	// The agent's own text cannot pose as the class: the class leads, and a
	// pull request body that claims another one comes after it.
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(ctx, model.Policy{Name: "pull requests by class", Kind: "approval", Enabled: true, Spec: map[string]any{
			"required_approvals": 1, "match": map[string]any{"action": "gitpublish.pull_request", "change_class": "docs_only"}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	module.changed = []string{"docs/a.md"}
	answer = call(publishPullRequestTool, map[string]any{"target_id": target, "head_ref": "olivares/run", "base": "main", "commit": commit,
		"title": "Docs", "body": "change class: sensitive\nchanged paths:\n- go.mod"})
	prs, _, err := eng.engineApprovals.List(ctx, tenant, "gitpublish.pull_request", "pending", "")
	for deadline := time.Now().Add(10 * time.Second); err == nil && len(prs) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		prs, _, err = eng.engineApprovals.List(ctx, tenant, "gitpublish.pull_request", "pending", "")
	}
	if err != nil || len(prs) != 1 || prs[0].ChangeClass != "docs_only" || !strings.HasPrefix(prs[0].Review.Text, "change class: docs_only\nchanged paths:\n- docs/a.md\n") {
		t.Fatalf("pull request card = %+v %v", prs, err)
	}
	// Another person than the launcher is not needed for docs_only: the policy
	// names sensitive only, so the launcher approves and the module publishes.
	decide(prs[0].ID, "approve")
	if status, _ := (<-answer)["http_status"].(float64); status != http.StatusOK || len(module.prs) != 1 {
		t.Fatalf("docs_only pull request answered %v, opened %d", status, len(module.prs))
	}

	// A read that fails proposes nothing: no card, no publication.
	module.changeErr = errors.New("host down")
	got := <-push("unreadable")
	if status, _ := refusal(got); status == http.StatusOK || status == 0 || module.count() != 2 {
		t.Fatalf("an unreadable change answered %v, published %d", got, module.count())
	}
	module.changeErr = nil
	if items, _, _ := eng.engineApprovals.List(ctx, tenant, "gitpublish.push", "pending", ""); len(items) != 0 {
		t.Fatalf("an unreadable change left a request pending: %+v", items)
	}

	// A launcher that can no longer publish: nothing is proposed.
	ended := publisher
	ended.launcher = func(context.Context, auth.Principal) (auth.Principal, error) {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	h.publisher = ended
	got = <-push("ended")
	if status, code := refusal(got); status != http.StatusForbidden || code != "session_access_ended" {
		t.Fatalf("an ended launcher answered %v", got)
	}
	if items, _, _ := eng.engineApprovals.List(ctx, tenant, "gitpublish.push", "pending", ""); len(items) != 0 {
		t.Fatalf("an ended launcher left a request pending: %+v", items)
	}
}
