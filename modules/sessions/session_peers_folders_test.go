// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type crossFolderPeerPolicy struct {
	runID     string
	workspace model.ID
	deny      atomic.Bool
}

func (p *crossFolderPeerPolicy) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	if req.Permission == permRunRead && req.Resource.ID == p.runID {
		return auth.Decision{Allow: !p.deny.Load() && req.Resource.Kind == "run" && req.Resource.WorkspaceID == p.workspace}, nil
	}
	return auth.Decision{Allow: true}, nil
}

// These are owned process protocol fixtures with empty account homes. Message
// effects use the real API, session principal and workspace-confined work store.
func TestSessionPeersAcrossAuthorizedFolders(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			if be.cfg.Engine == store.EnginePostgres {
				db, err := sql.Open("pgx", be.cfg.DSN)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				var superuser, bypassRLS bool
				var version int
				if err := db.QueryRowContext(t.Context(), `SELECT rolsuper, rolbypassrls, current_setting('server_version_num')::int FROM pg_roles WHERE rolname=current_user`).Scan(&superuser, &bypassRLS, &version); err != nil {
					t.Fatal(err)
				}
				if superuser || bypassRLS || version < 160000 || version >= 170000 {
					t.Fatalf("peer protocol requires PG16 app role: superuser=%t bypassRLS=%t major=%d", superuser, bypassRLS, version/10000)
				}
				t.Log("PG16 runtime traffic: app role, NOSUPERUSER, NOBYPASSRLS; owner only for DDL")
			}
			resolver := newK2WorkIdentity()
			m := New(WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
				WithWorkIdentityResolver(resolver), WithWorkContentGuard(allowWorkContent{}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			m.UseSessionWorkspaceRoot(t.TempDir())
			f := newReadinessFixture(t, be, m)
			workspace := workAPIWorkspace(t, f.h, f.tenant)
			actor, err := auth.NewSystemOperator("test:folder-peers", "owned cross-folder peer protocol fixtures")
			if err != nil {
				t.Fatal(err)
			}
			authenticator := auth.NewAuthenticator(f.h.st, nil)
			m.WorkSessionCreds = &orchestrationTestCredentials{m: m, a: authenticator, actor: actor}
			m.OrchestrationScopes = orchestrationTestScope{f.h}
			launch := func(tenant model.TenantID, coreWorkspace model.ID) (string, string, string) {
				t.Helper()
				folder := f.h.doJSON("POST", "/v1/m/sessions/workspaces", f.admin,
					map[string]any{"name": "Owned peer folder", "root_path": t.TempDir()}, tenantHdr(tenant))
				if folder.code != http.StatusCreated {
					t.Fatalf("register folder = %d %s", folder.code, folder.raw)
				}
				body := map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir()}
				if !coreWorkspace.IsZero() {
					body["session_work_grant"] = map[string]any{"role": "orchestrator", "workspace_id": coreWorkspace, "capabilities": []string{"work.read"}}
				}
				profile := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin, body, tenantHdr(tenant))
				if profile.code != http.StatusCreated {
					t.Fatalf("register profile = %d %s", profile.code, profile.raw)
				}
				var ref string
				if coreWorkspace.IsZero() {
					run := f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin, map[string]any{
						"transport": "stream-json", "isolation": "native", "permission_mode": "default",
						"provider_profile_ref": profile.body["profile_ref"], "workspace_ref": folder.body["workspace_ref"],
					}, tenantHdr(tenant))
					if run.code != http.StatusCreated {
						t.Fatalf("launch = %d %s", run.code, run.raw)
					}
					ref = run.body["run_ref"].(string)
				} else {
					// The existing orchestration profile explicitly binds this
					// fixture to another core workspace at first launch.
					run, err := m.createRun(t.Context(), tenant, CreateRunParams{
						Transport: TransportStreamJSON, Isolation: IsolationNative,
						ProviderProfileRef: profile.body["profile_ref"].(string), WorkspaceRef: folder.body["workspace_ref"].(string),
						AgentRef: "fixture:" + model.NewID().String(), Actor: actor.Actor(), ActorKind: actor.ActorKind(),
					})
					if err != nil {
						t.Fatal(err)
					}
					ref = run.RunRef
				}
				live, ok := m.rt.getLive(tenant, ref)
				if !ok {
					t.Fatal("launched peer has no supervised process")
				}
				resolver.admit("session", live.claim.SID)
				return live.claim.SID, ref, live.companion.Dir
			}
			_, senderRun, senderFolder := launch(f.tenant, "")
			peerSID, peerRun, peerFolder := launch(f.tenant, "")
			if senderFolder == peerFolder {
				t.Fatal("fixture did not use distinct folders")
			}
			live, _ := m.rt.getLive(f.tenant, senderRun)
			launcher, err := authenticator.Authenticate(t.Context(), f.admin)
			if err != nil {
				t.Fatal(err)
			}
			issuer := auth.NewSessionCredentials(authenticator, func(context.Context, auth.SessionScope) error { return nil })
			bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
				TenantID: f.tenant, WorkspaceID: workspace, FolderRef: "owned-sender-folder", FolderPath: senderFolder,
				SessionRef: live.claim.SID, RunRef: senderRun, AgentRef: live.agentRef, Holder: live.claim.Holder, Fence: live.claim.Fence,
			})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := issuer.Authenticate(t.Context(), bearer)
			if err != nil {
				t.Fatal(err)
			}
			peerRecord, err := m.loadRun(t.Context(), f.tenant, peerRun)
			if err != nil {
				t.Fatal(err)
			}
			policy := &crossFolderPeerPolicy{runID: peerRecord.String(model.ColID), workspace: workspace}
			WithWorkAuthorizer(auth.NewAuthorizer(policy))(m)
			selectPeer := func(sid string, want int) {
				t.Helper()
				got := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+senderRun+"/peers", f.admin, map[string]any{"peers": []string{sid}}, tenantHdr(f.tenant))
				if got.code != want {
					t.Fatalf("select peer = %d %s; want %d", got.code, got.raw, want)
				}
				if want == http.StatusOK && got.body["authz_workspace_id"] != workspace.String() {
					t.Fatalf("peer chooser lacks recorded authz workspace: %s", got.raw)
				}
			}
			send := func(want int) {
				t.Helper()
				body := workAPICreateBody(workspace, "", "Cross-worktree report")
				body["command"], body["owner_kind"], body["owner_ref"] = "item.create", "session", peerSID
				body["provenance_kind"], body["provenance_ref"] = "mcp", principal.SessionIdentity
				raw, _ := json.Marshal(body)
				r := httptest.NewRequest("POST", "/v1/m/sessions/work-items?mode=apply", bytes.NewReader(raw))
				r.Header.Set("Idempotency-Key", model.NewID().String())
				w := httptest.NewRecorder()
				m.CallSessionWork(w, r, principal, f.tenant)
				if w.Code != want {
					t.Fatalf("cross-folder send = %d %s; want %d", w.Code, w.Body.String(), want)
				}
			}
			send(http.StatusForbidden)
			selectPeer(peerSID, http.StatusOK)
			for ref, folder := range map[string]string{senderRun: senderFolder, peerRun: peerFolder} {
				read := f.h.do("GET", "/v1/m/sessions/runs/"+ref, f.admin, tenantHdr(f.tenant))
				if read.code != http.StatusOK || read.body["authz_workspace_id"] != workspace.String() || read.body["workspace_path"] != folder {
					t.Fatalf("authorized peer DTO = %d %s", read.code, read.raw)
				}
			}
			send(http.StatusOK)
			inbox := f.h.do("GET", "/v1/m/sessions/work-items?owner_kind=session&owner_ref="+peerSID, f.admin, tenantHdr(f.tenant))
			items, _ := inbox.body["items"].([]any)
			if inbox.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["owner_ref"] != peerSID {
				t.Fatalf("recipient inbox = %d %s", inbox.code, inbox.raw)
			}
			companion, err := m.RuntimeCompanion(t.Context(), f.tenant, principal)
			if err != nil || companion.Dir != senderFolder {
				t.Fatalf("peer message changed sender folder: %v", err)
			}
			_, scope, err := issuer.Resolve(t.Context(), bearer)
			if err != nil || scope.FolderPath != senderFolder || scope.WorkspaceID != workspace {
				t.Fatalf("peer message changed session scope: %v", err)
			}
			policy.deny.Store(true)
			send(http.StatusForbidden)
			selectPeer(peerSID, http.StatusUnprocessableEntity)
			policy.deny.Store(false)
			foreignSID, _, _ := launch(f.other, "")
			selectPeer(foreignSID, http.StatusUnprocessableEntity)
			otherWorkspace := workAPICreateWorkspace(t, f.h, f.tenant, "other-peer-core-workspace")
			foreignWorkspaceSID, _, _ := launch(f.tenant, otherWorkspace)
			selectPeer(foreignWorkspaceSID, http.StatusUnprocessableEntity)
			cleared := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+senderRun+"/peers", f.admin, map[string]any{"peers": []string{}}, tenantHdr(f.tenant))
			if cleared.code != http.StatusOK {
				t.Fatal(cleared.raw)
			}
			send(http.StatusForbidden)
			selectPeer(peerSID, http.StatusOK)
			stopped := f.h.do("POST", "/v1/m/sessions/runs/"+peerRun+"/stop", f.admin, tenantHdr(f.tenant))
			if stopped.code != http.StatusOK {
				t.Fatalf("stop peer = %d %s", stopped.code, stopped.raw)
			}
			send(http.StatusForbidden)
			selectPeer(peerSID, http.StatusUnprocessableEntity)
		})
	}
}
