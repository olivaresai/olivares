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
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionPeersSQLiteWithGovernance(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	h := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	m := h.m
	WithRunner(&fakeRunner{})(m)
	WithCredentialSource(staticCred())(m)
	resolver := newK2WorkIdentity()
	WithWorkIdentityResolver(resolver)(m)
	WithWorkContentGuard(allowWorkContent{})(m)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseSessionWorkspaceRoot(t.TempDir())
	WithWorkAuthorizer(h.authz)(m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sqlite-peers")
	workspace := workAPIWorkspace(t, h.harness, tenant)
	actor, err := auth.NewSystemOperator("test:sqlite-peers", "owned peer runtime protocol fixtures")
	if err != nil {
		t.Fatal(err)
	}
	source := &orchestrationTestCredentials{m: m, a: h.authr, actor: actor}
	m.WorkSessionCreds = source
	m.OrchestrationScopes = orchestrationTestScope{h.harness}
	folder := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "name": "Sender folder"}, tenantHdr(tenant))
	profile := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir()}, tenantHdr(tenant))
	if folder.code != http.StatusCreated || profile.code != http.StatusCreated {
		t.Fatalf("setup folder=%d profile=%d", folder.code, profile.code)
	}
	sender, err := m.createRun(t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: folder.body["workspace_ref"].(string), ProviderProfileRef: profile.body["profile_ref"].(string), Actor: actor.Actor(), ActorKind: actor.ActorKind()})
	if err != nil {
		t.Fatal(err)
	}
	f := &orchestrationFixture{h: h.harness, resolver: resolver, source: source, tenant: tenant, workspace: workspace, admin: admin, run: sender.RunRef, sid: source.last.SessionRef}
	sameFolder, _ := newSessionPeer(t, f, "", "")
	otherFolder := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "name": "Other folder"}, tenantHdr(tenant))
	if otherFolder.code != http.StatusCreated {
		t.Fatal(otherFolder.raw)
	}
	differentFolder, _ := newSessionPeer(t, f, "", otherFolder.body["workspace_ref"].(string))
	want := []string{sameFolder, differentFolder}
	raw, err := json.Marshal(map[string]any{"peers": want})
	if err != nil {
		t.Fatal(err)
	}
	// The production scoped-grant engine reads the same single-connection store.
	// Bound the request so a nested transaction fails promptly instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r := httptest.NewRequest("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+admin)
	r.Header.Set("X-Olivares-Tenant", tenant.String())
	w := httptest.NewRecorder()
	start := time.Now()
	h.srv.Handler().ServeHTTP(w, r)
	t.Logf("peer choice elapsed=%s status=%d body=%s", time.Since(start), w.Code, w.Body.String())
	if w.Code != http.StatusOK || ctx.Err() != nil {
		t.Fatalf("fresh SQLite peer choice = %d %s; request error=%v", w.Code, w.Body.String(), ctx.Err())
	}
	read := h.do("GET", "/v1/m/sessions/runs/"+f.run, admin, tenantHdr(tenant))
	stored, _ := read.body["peers"].([]any)
	if read.code != http.StatusOK || len(stored) != len(want) || stored[0] != want[0] || stored[1] != want[1] {
		t.Fatalf("peer read-back = %d %s", read.code, read.raw)
	}
	audit := h.do("GET", "/v1/audit?limit=200", admin, tenantHdr(tenant))
	audited := false
	items, _ := audit.body["items"].([]any)
	for _, item := range items {
		audited = audited || item.(map[string]any)["action"] == "sessions.run.peers"
	}
	if audit.code != http.StatusOK || !audited {
		t.Fatalf("peer choice audit = %d %s", audit.code, audit.raw)
	}
}

type peerChoiceAuthorizer struct {
	WorkAuthorizer
	peerID    string
	triggered atomic.Bool
	after     func(context.Context)
}

func (a *peerChoiceAuthorizer) Authorize(ctx context.Context, req auth.Request) auth.Decision {
	decision := a.WorkAuthorizer.Authorize(ctx, req)
	if decision.Allow && req.Permission == permRunRead && req.Resource.ID == a.peerID && !a.triggered.Swap(true) {
		a.after(ctx)
	}
	return decision
}

func TestSessionPeersRevalidateAuthorizedLiveGeneration(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "stopped"
		if resume {
			name = "restarted"
		}
		t.Run(name, func(t *testing.T) {
			f, _, _ := newSessionPeersFixture(t, false)
			previous, _ := newSessionPeer(t, f, "", "")
			peerSID, peerRun := newSessionPeer(t, f, "", "")
			selected := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers": []string{previous}}, tenantHdr(f.tenant))
			if selected.code != http.StatusOK {
				t.Fatal(selected.raw)
			}
			peer, err := f.h.m.loadRun(t.Context(), f.tenant, peerRun)
			if err != nil {
				t.Fatal(err)
			}
			auditCount := func() int {
				t.Helper()
				out := f.h.do("GET", "/v1/audit?limit=200", f.admin, tenantHdr(f.tenant))
				if out.code != http.StatusOK {
					t.Fatal(out.raw)
				}
				items, _ := out.body["items"].([]any)
				count := 0
				for _, item := range items {
					if item.(map[string]any)["action"] == "sessions.run.peers" {
						count++
					}
				}
				return count
			}
			before := auditCount()
			observer := &peerChoiceAuthorizer{WorkAuthorizer: f.h.m.WorkAuthorizer, peerID: peer.String(model.ColID), after: func(ctx context.Context) {
				if _, err := f.h.m.stopRun(ctx, f.tenant, peerRun, "fixture", model.ActorSystem); err != nil {
					t.Fatal(err)
				}
				if resume {
					if _, err := f.h.m.resumeRun(ctx, f.tenant, peerRun, "fixture", model.ActorSystem, ""); err != nil {
						t.Fatal(err)
					}
					live, ok := f.h.m.rt.getLive(f.tenant, peerRun)
					if !ok || live.claim.SID != peerSID || live.claim.Fence == peer.Int(colClaimFence) {
						t.Fatal("restart did not retain the session ID with a new live fence")
					}
				}
			}}
			WithWorkAuthorizer(observer)(f.h.m)
			raw, err := json.Marshal(map[string]any{"peers": []string{peerSID}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			r := httptest.NewRequest("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", bytes.NewReader(raw)).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+f.admin)
			r.Header.Set("X-Olivares-Tenant", f.tenant.String())
			w := httptest.NewRecorder()
			f.h.srv.Handler().ServeHTTP(w, r)
			if !observer.triggered.Load() || ctx.Err() != nil || w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("changed peer choice = %d %s, request error=%v", w.Code, w.Body.String(), ctx.Err())
			}
			read := f.h.do("GET", "/v1/m/sessions/runs/"+f.run, f.admin, tenantHdr(f.tenant))
			peers, _ := read.body["peers"].([]any)
			if read.code != http.StatusOK || len(peers) != 1 || peers[0] != previous || read.body["peers_rule"] != nil {
				t.Fatalf("refused peer choice changed selection = %d %s", read.code, read.raw)
			}
			if auditCount() != before {
				t.Fatal("refused peer choice appended a successful peer-choice audit")
			}
		})
	}
}
