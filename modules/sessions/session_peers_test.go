// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSessionPeersPersistAndConstrainGenericWorkCommands(t *testing.T) {
	f, p, _ := newSessionPeersFixture(t, false)
	peer, _ := newSessionPeer(t, f, "", "")
	other, _ := newSessionPeer(t, f, "", "")
	command := func(path string, body any, version string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/v1/m/sessions/"+path+"?mode=apply", bytes.NewReader(raw))
		r.Header.Set("Idempotency-Key", model.NewID().String())
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		w := httptest.NewRecorder()
		f.h.m.CallSessionWork(w, r, p, f.tenant)
		return w
	}
	create := workAPICreateBody(f.workspace, "", "Message to a peer")
	create["command"] = "item.create"
	create["owner_kind"], create["owner_ref"] = "session", peer
	create["provenance_kind"], create["provenance_ref"] = "mcp", f.sid
	if w := command("work-items", create, ""); w.Code != http.StatusForbidden {
		t.Fatalf("default peer authority = %d, want 403", w.Code)
	}
	unset := f.h.do("GET", "/v1/m/sessions/runs/"+f.run, f.admin, tenantHdr(f.tenant))
	if peers, ok := unset.body["peers"].([]any); unset.code != http.StatusOK || !ok || len(peers) != 0 || unset.body["peers_rule"] != nil {
		t.Fatalf("absent historical selection must render empty, without a rule: %d %s", unset.code, unset.raw)
	}
	for _, invalid := range []map[string]any{
		{"peers": []string{peer}, "peers_rule": "same-template"},
		{"peers": []string{peer, peer}},
		{"peers": []string{model.NewID().String()}},
		{"peers_rule": "all"},
	} {
		got := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, invalid, tenantHdr(f.tenant))
		if got.code != http.StatusBadRequest {
			t.Fatalf("invalid peer choice = %d %s", got.code, got.raw)
		}
	}
	setPeers := func(peers []string) {
		t.Helper()
		got := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers": peers}, tenantHdr(f.tenant))
		if got.code != http.StatusOK {
			t.Fatalf("set peers = %d %s", got.code, got.raw)
		}
		read := f.h.do("GET", "/v1/m/sessions/runs/"+f.run, f.admin, tenantHdr(f.tenant))
		stored, ok := read.body["peers"].([]any)
		if read.code != http.StatusOK || !ok || len(stored) != len(peers) {
			t.Fatalf("persisted peers = %d %s", read.code, read.raw)
		}
		for i := range peers {
			if stored[i] != peers[i] {
				t.Fatal("stored peer is not the granted canonical SID")
			}
		}
	}
	setPeers([]string{peer})
	created := command("work-items", create, "")
	if created.Code != http.StatusOK {
		t.Fatalf("allowed peer create = %d %s", created.Code, created.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(created.Body.Bytes(), &result) != nil {
		t.Fatal("create returned no work result")
	}
	id, _ := result["result_id"].(string)
	if id == "" {
		t.Fatal("create returned no work item")
	}
	if w := command("work-items/"+id+"/assign", map[string]any{"command": "item.assign", "work_item_id": id, "owner_kind": "session", "owner_ref": other}, `"v1"`); w.Code != http.StatusForbidden {
		t.Fatalf("generic assign bypassed the peer list: %d", w.Code)
	}
	setPeers([]string{})
	if w := command("work-items", create, ""); w.Code != http.StatusForbidden {
		t.Fatalf("removed peer retained authority: %d", w.Code)
	}
	audit := f.h.do("GET", "/v1/audit?limit=200", f.admin, tenantHdr(f.tenant))
	if audit.code != http.StatusOK {
		t.Fatalf("audit read = %d", audit.code)
	}
	denied, granted := false, false
	items, _ := audit.body["items"].([]any)
	for _, item := range items {
		row := item.(map[string]any)
		denied = denied || row["action"] == "sessions.work.tool.denied"
		granted = granted || row["action"] == "sessions.run.peers"
	}
	if !denied || !granted {
		t.Fatal("peer grants and refusals were not audited")
	}
}

func newSessionPeersFixture(t *testing.T, templated bool) (*orchestrationFixture, auth.Principal, string) {
	t.Helper()
	f := newOrchestrationFixture(t, []string{"work.create", "work.assign", "work.read"})
	// Relaunch the sender in a registered folder so the peer choice matches the
	// console's same-folder candidates; the initial fixture run had its own folder.
	if _, err := f.h.m.stopRun(t.Context(), f.tenant, f.run, "fixture", model.ActorSystem); err != nil {
		t.Fatal(err)
	}
	// The shared session credential carries the launcher's narrowed authority;
	// these ordinary sessions do not need a legacy agent orchestration grant.
	f.patchGrant(t, nil)
	folder := f.h.doJSON("POST", "/v1/m/sessions/workspaces", f.admin, map[string]any{"root_path": t.TempDir(), "name": "Peer folder"}, tenantHdr(f.tenant))
	if folder.code != http.StatusCreated {
		t.Fatalf("peer folder = %d", folder.code)
	}
	// These module-level process doubles need no provider conversation frame;
	// canonical session identity and liveness still come from the real runtime.
	f.h.m.rt.runner.(*fakeRunner).initSID = ""
	templateID := ""
	if templated {
		tpl := f.h.doJSON("POST", "/v1/m/sessions/templates", f.admin, map[string]any{"name": "Peer cohort", "body": map[string]any{}}, tenantHdr(f.tenant))
		if tpl.code != http.StatusCreated {
			t.Fatalf("peer template = %d %s", tpl.code, tpl.raw)
		}
		templateID = tpl.body["id"].(string)
	}
	sender, err := f.h.m.createRun(t.Context(), f.tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: f.source.actor.Actor(), ActorKind: model.ActorSystem,
		WorkspaceRef: folder.body["workspace_ref"].(string), ProviderProfileRef: f.profile, TemplateID: templateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.run, f.sid = sender.RunRef, f.source.last.SessionRef
	f.resolver.admit("session", f.sid)
	a := auth.NewAuthenticator(f.h.st, nil)
	launcher, err := a.Authenticate(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := f.h.m.rt.getLive(f.tenant, f.run)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
		TenantID: f.tenant, WorkspaceID: f.workspace, FolderRef: "test-folder", FolderPath: live.companion.Dir,
		SessionRef: f.sid, RunRef: f.run, AgentRef: live.agentRef, Holder: live.claim.Holder, Fence: live.claim.Fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.Authenticate(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	return f, p, templateID
}

// Separate account homes give each fixture process its own provider identity;
// these are empty owned directories, never signed-in vendor accounts.
func newSessionPeer(t *testing.T, f *orchestrationFixture, templateID, folderRef string) (string, string) {
	t.Helper()
	created := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin, map[string]any{
		"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(),
	}, tenantHdr(f.tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("peer profile = %d", created.code)
	}
	actor, err := auth.NewSystemOperator("test:peer-runtime", "exercise the run peer API with owned protocol fixtures")
	if err != nil {
		t.Fatal(err)
	}
	sender := f.h.do("GET", "/v1/m/sessions/runs/"+f.run, f.admin, tenantHdr(f.tenant))
	if sender.code != http.StatusOK {
		t.Fatalf("sender folder read = %d", sender.code)
	}
	if folderRef == "" {
		folderRef = sender.body["workspace_ref"].(string)
	}
	run, err := f.h.m.createRun(t.Context(), f.tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actor.Actor(), ActorKind: model.ActorSystem,
		WorkspaceRef: folderRef, ProviderProfileRef: created.body["profile_ref"].(string), TemplateID: templateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := f.source.last.SessionRef
	f.resolver.admit("session", sid)
	return sid, run.RunRef
}

func TestSessionPeersSameTemplateRuleResolvesLivePeersAtSendTime(t *testing.T) {
	f, p, templateID := newSessionPeersFixture(t, true)
	chosen := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers_rule": "same-template"}, tenantHdr(f.tenant))
	if chosen.code != http.StatusOK {
		t.Fatalf("choose template peers = %d %s", chosen.code, chosen.raw)
	}
	read := f.h.do("GET", "/v1/m/sessions/runs/"+f.run, f.admin, tenantHdr(f.tenant))
	if read.code != http.StatusOK || read.body["peers_rule"] != "same-template" {
		t.Fatalf("persisted peer rule = %d %s", read.code, read.raw)
	}
	// Launch AFTER storing the rule: a snapshot at configuration time would fail.
	peer, peerRun := newSessionPeer(t, f, templateID, "")
	otherTemplate := f.h.doJSON("POST", "/v1/m/sessions/templates", f.admin, map[string]any{"name": "Other cohort", "body": map[string]any{}}, tenantHdr(f.tenant))
	if otherTemplate.code != http.StatusCreated {
		t.Fatalf("other template = %d %s", otherTemplate.code, otherTemplate.raw)
	}
	other, _ := newSessionPeer(t, f, otherTemplate.body["id"].(string), "")
	folder := f.h.doJSON("POST", "/v1/m/sessions/workspaces", f.admin, map[string]any{"root_path": t.TempDir(), "name": "Other folder"}, tenantHdr(f.tenant))
	if folder.code != http.StatusCreated {
		t.Fatalf("other folder = %d %s", folder.code, folder.raw)
	}
	otherFolder, _ := newSessionPeer(t, f, templateID, folder.body["workspace_ref"].(string))
	send := func(to string, want int) {
		t.Helper()
		body := workAPICreateBody(f.workspace, "", "Template peer message")
		body["command"], body["owner_kind"], body["owner_ref"] = "item.create", "session", to
		body["provenance_kind"], body["provenance_ref"] = "mcp", f.sid
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/v1/m/sessions/work-items?mode=apply", bytes.NewReader(raw))
		r.Header.Set("Idempotency-Key", model.NewID().String())
		w := httptest.NewRecorder()
		f.h.m.CallSessionWork(w, r, p, f.tenant)
		if w.Code != want {
			t.Fatalf("peer send = %d %s, want %d", w.Code, w.Body.String(), want)
		}
	}
	send(peer, http.StatusOK)
	send(other, http.StatusForbidden)
	// Explicit same-template peers may retain distinct authorized folders.
	send(otherFolder, http.StatusOK)
	if _, err := f.h.m.stopRun(t.Context(), f.tenant, peerRun, "fixture", model.ActorSystem); err != nil {
		t.Fatal(err)
	}
	send(peer, http.StatusForbidden)
	peerAfterStop, _ := newSessionPeer(t, f, templateID, "")
	send(peerAfterStop, http.StatusOK)
	cleared := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers": []string{}}, tenantHdr(f.tenant))
	if cleared.code != http.StatusOK || cleared.body["peers_rule"] != nil {
		t.Fatalf("clear template rule = %d %s", cleared.code, cleared.raw)
	}
	send(peerAfterStop, http.StatusForbidden)
	selected := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers": []string{otherFolder}}, tenantHdr(f.tenant))
	if selected.code != http.StatusOK {
		t.Fatalf("explicit peer in another authorized folder = %d %s", selected.code, selected.raw)
	}
	send(otherFolder, http.StatusOK)
}
