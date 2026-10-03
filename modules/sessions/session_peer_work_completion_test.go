// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// F1's real peer journey: a live recipient must consume its addressed work
// through the same workspace-confined port as every session work tool.
func TestSessionPeerRecipientLeasesSubmitsAndCompletesWithinWorkspace(t *testing.T) {
	f, sender, _ := newSessionPeersFixture(t, false)
	peer, run := newSessionPeer(t, f, "", "")
	recipient := peerWorkPrincipal(t, f, peer, run, f.workspace)
	chosen := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers": []string{peer}}, tenantHdr(f.tenant))
	if chosen.code != http.StatusOK {
		t.Fatalf("select recipient = %d %s", chosen.code, chosen.raw)
	}
	call := func(p auth.Principal, method, path string, body any, version int64, want int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "/v1/m/sessions/"+path+"?mode=apply", bytes.NewReader(raw))
		if method != http.MethodGet {
			r.Header.Set("Idempotency-Key", model.NewID().String())
		}
		if version > 0 {
			r.Header.Set("If-Match", fmt.Sprintf(`"v%d"`, version))
		}
		w := httptest.NewRecorder()
		f.h.m.CallSessionWork(w, r, p, f.tenant)
		if w.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body.String(), want)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	body := workAPICreateBody(f.workspace, "", "Addressed work for B")
	body["command"], body["owner_kind"], body["owner_ref"] = "item.create", "session", peer
	body["provenance_kind"], body["provenance_ref"] = "mcp", f.sid
	created := call(sender, "POST", "work-items", body, 0, http.StatusOK)
	id := created["result_id"].(string)
	version := int64(created["version"].(float64))
	current := call(recipient, "GET", "work-items/"+id, nil, 0, http.StatusOK)
	criterion := current["acceptance"].([]any)[0].(map[string]any)["id"].(string)
	lease := map[string]any{"command": "lease.acquire", "holder_sid": peer, "holder_run_ref": run, "ttl_seconds": 60}
	ready := call(recipient, "POST", "work-items/"+id+"/transitions", map[string]any{"command": "item.ready", "work_item_id": id}, version, http.StatusOK)
	version = int64(ready["version"].(float64))
	acquired := call(recipient, "POST", "work-items/"+id+"/lease/acquire", lease, version, http.StatusOK)
	version = int64(acquired["version"].(float64))
	fence, ok := acquired["lease_fence"].(float64)
	if !ok || fence < 1 {
		t.Fatalf("acquisition did not return the session's lease fence: %#v", acquired)
	}
	// Renewal exercises the claim CAS touch as well as the acquisition read.
	renewed := call(recipient, "POST", "work-items/"+id+"/lease/renew", map[string]any{"command": "lease.renew", "holder_sid": peer, "holder_run_ref": run, "fence": fence, "ttl_seconds": 60}, version, http.StatusOK)
	version = int64(renewed["version"].(float64))
	evaluated := call(recipient, "POST", "work-items/"+id+"/acceptance", map[string]any{"command": "acceptance.evaluate", "work_item_id": id, "criterion_id": criterion, "fence": fence, "holder_sid": peer, "holder_run_ref": run,
		"acceptance": []any{map[string]any{"state": "passed", "evidence_ref": "test:peer-work-completed", "evidence_hash": strings.Repeat("a", 64)}}}, version, http.StatusOK)
	version = int64(evaluated["version"].(float64))
	submitted := call(recipient, "POST", "work-items/"+id+"/transitions", map[string]any{"command": "item.submit", "work_item_id": id, "fence": fence, "holder_sid": peer, "holder_run_ref": run}, version, http.StatusOK)
	version = int64(submitted["version"].(float64))
	call(recipient, "POST", "work-items/"+id+"/transitions", map[string]any{"command": "item.complete", "work_item_id": id}, version, http.StatusOK)
	final := call(recipient, "GET", "work-items/"+id, nil, 0, http.StatusOK)
	if final["item"].(map[string]any)["status"] != "completed" {
		t.Fatal("recipient did not complete addressed work")
	}
	// A genuine live session scoped to another core workspace cannot see or
	// lease B's item, even though its launcher has tenant owner authority.
	var foreignWorkspace model.ID
	if err := f.h.st.Mutate(t.Context(), f.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(t.Context(), model.Workspace{Name: "Other peer workspace", Slug: "other-peer-workspace", Status: model.StatusActive})
		foreignWorkspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	profile := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin, map[string]any{
		"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(),
		"session_work_grant": map[string]any{"role": "orchestrator", "workspace_id": foreignWorkspace, "capabilities": []string{"work.read"}},
	}, tenantHdr(f.tenant))
	if profile.code != http.StatusCreated {
		t.Fatalf("foreign profile = %d %s", profile.code, profile.raw)
	}
	foreignRun, err := f.h.m.createRun(t.Context(), f.tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: f.source.actor.Actor(), ActorKind: model.ActorSystem, AgentRef: "orchestrator:" + model.NewID().String(), ProviderProfileRef: profile.body["profile_ref"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	foreign := peerWorkPrincipal(t, f, f.source.last.SessionRef, foreignRun.RunRef, foreignWorkspace)
	call(foreign, "GET", "work-items/"+id, nil, 0, http.StatusNotFound)
	call(foreign, "POST", "work-items/"+id+"/lease/acquire", lease, version, http.StatusNotFound)
}

func peerWorkPrincipal(t *testing.T, f *orchestrationFixture, sid, run string, workspace model.ID) auth.Principal {
	t.Helper()
	a := auth.NewAuthenticator(f.h.st, nil)
	launcher, err := a.Authenticate(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	live, ok := f.h.m.rt.getLive(f.tenant, run)
	if !ok {
		t.Fatal("recipient is not live")
	}
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	token, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{TenantID: f.tenant, WorkspaceID: workspace, FolderRef: "fixture-folder", FolderPath: live.companion.Dir, SessionRef: sid, RunRef: run, AgentRef: live.agentRef, Holder: live.claim.Holder, Fence: live.claim.Fence})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
