// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
)

func TestWorkspaceKnowledgeSourceChangesRefuseIngest(t *testing.T) {
	for _, change := range []string{"kb-workspace", "connector-disabled", "source-forbid", "session-revoked", "session-expired", "session-revoked-preparation", "session-revoked-embedding"} {
		t.Run(change, func(t *testing.T) {
			for _, cfg := range wireBackends(t) {
				t.Run(string(cfg.Engine), func(t *testing.T) {
					eng, token, tenant := wireBoot(t, cfg, t.TempDir())
					request := func(method, path string, body any, want int) map[string]any {
						t.Helper()
						code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
						if code != want {
							t.Fatalf("%s %s = %d, want %d (%s)", method, path, code, want, raw)
						}
						return out
					}
					if err := eng.store.Mutate(t.Context(), tenant, func(sc store.Scope) error {
						_, err := sc.Workspaces().Create(t.Context(), model.Workspace{Name: "Marketing", Slug: "marketing"})
						return err
					}); err != nil {
						t.Fatal(err)
					}
					embedPolicy := "local_only"
					recorder := &wireIngestEmbedder{}
					if change == "session-revoked" || change == "session-revoked-preparation" || change == "session-revoked-embedding" {
						knowledge.WithEmbedder(recorder)(eng.knowledgeMod)
						embedPolicy = "model_backed"
					}
					kb := request("POST", "/v1/m/knowledge/kbs", map[string]any{"name": "source-changes", "classification": "public", "embed_policy": embedPolicy}, http.StatusCreated)
					kbID := kb["id"].(string)
					binding := request("POST", "/v1/m/sourcescope/bindings", map[string]any{"source_type": "knowledge", "source_ref": kbID, "scope_tree": "workspace", "scope_ref": "default", "enabled": true}, http.StatusCreated)
					principal, err := eng.authr.Authenticate(t.Context(), token)
					if err != nil {
						t.Fatal(err)
					}
					var approver string
					if change == "kb-workspace" {
						request("POST", "/v1/users", map[string]any{"email": "change-approver@example.invalid", "password": "fixture-password-2", "tenant": tenant.String(), "role": "admin"}, http.StatusCreated)
						code, login, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/auth/login", "", "", map[string]any{"email": "change-approver@example.invalid", "password": "fixture-password-2"})
						if code != http.StatusOK {
							t.Fatalf("distinct approver login = %d", code)
						}
						approver = login["token"].(string)
					}
					var expiresAt time.Time
					changeSession := func(revoke bool) {
						t.Helper()
						if err := eng.store.AuthMutate(t.Context(), func(as store.AuthScope) error {
							sessions, _, err := as.Sessions().List(t.Context(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: principal.UserID.String()}}})
							if err != nil {
								return err
							}
							if len(sessions) != 1 {
								t.Fatalf("fixture sessions = %d", len(sessions))
							}
							session := sessions[0]
							if revoke {
								session.Revoked = true
							} else {
								now, err := as.(store.TransactionClock).TransactionNow(t.Context())
								if err != nil {
									return err
								}
								expiresAt = now.Time().Add(2 * time.Second)
								session.ExpiresAt = model.NewTimestamp(expiresAt)
							}
							_, err = as.Sessions().Update(t.Context(), session)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					var mutate func()
					var once sync.Once
					var fetched atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						once.Do(mutate)
						w.Header().Set("Content-Type", "application/json")
						if strings.Contains(r.URL.Path, "/spaces/") {
							if change == "session-revoked-embedding" {
								_, _ = w.Write([]byte(`{"results":[{"id":"page-1","title":"Calibration"},{"id":"page-2","title":"Second calibration"}]}`))
							} else {
								_, _ = w.Write([]byte(`{"results":[{"id":"page-1","title":"Calibration"}]}`))
							}
						} else {
							fetched.Add(1)
							pageID := "page-1"
							if strings.Contains(r.URL.Path, "page-2") {
								pageID = "page-2"
							}
							_, _ = w.Write([]byte(`{"id":"` + pageID + `","title":"Calibration","body":{"storage":{"value":"<p>Workspace A only</p>"}},"space":{"key":"ENG"}}`))
						}
					}))
					t.Cleanup(srv.Close)
					connectorBody := map[string]any{"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true, "config": map[string]string{"mode": "live", "base_url": srv.URL, "space_key": "ENG"}}
					connector := request("POST", "/v1/m/sourcescope/workspace-connectors", connectorBody, http.StatusCreated)
					if change == "session-expired" {
						changeSession(false)
					}
					if change == "session-revoked-preparation" {
						var preparation sync.Once
						knowledge.WithSensitivityClassifier(wireRevokeDuringPreparation{revoke: func() {
							preparation.Do(func() { changeSession(true) })
						}})(eng.knowledgeMod)
					}
					if change == "session-revoked-embedding" {
						recorder.afterFirst = func() { changeSession(true) }
					}
					mutate = func() {
						switch change {
						case "kb-workspace":
							pending := request("DELETE", "/v1/m/sourcescope/bindings/"+binding["id"].(string), nil, http.StatusAccepted)
							code, out, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sourcescope/posture-requests/"+pending["id"].(string)+"/approve", approver, tenant.String(), nil)
							if code != http.StatusOK || out["status"] != "approved" {
								t.Fatalf("approve workspace rebinding = %d", code)
							}
							request("POST", "/v1/m/sourcescope/bindings", map[string]any{"source_type": "knowledge", "source_ref": kbID, "scope_tree": "workspace", "scope_ref": "marketing", "enabled": true}, http.StatusCreated)
						case "connector-disabled":
							connectorBody["enabled"] = false
							request("PUT", "/v1/m/sourcescope/workspace-connectors/"+connector["id"].(string), connectorBody, http.StatusOK)
						case "session-revoked":
							changeSession(true)
						case "session-expired":
							// No row changes after admission: only time crosses the
							// original credential horizon while the source is pulling.
							timer := time.NewTimer(time.Until(expiresAt.Add(100 * time.Millisecond)))
							defer timer.Stop()
							select {
							case <-timer.C:
							case <-t.Context().Done():
								t.Fatal(t.Context().Err())
							}
						case "source-forbid":
							request("POST", "/v1/m/sourcescope/bindings", map[string]any{"source_type": "knowledge", "source_ref": kbID, "scope_tree": "user", "scope_ref": principal.UserID.String(), "effect": "forbid", "enabled": true}, http.StatusCreated)
						}
					}
					request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs"}, http.StatusConflict)
					var wantCalls int32
					if change == "session-revoked-embedding" {
						wantCalls = 1
						if fetched.Load() != 2 {
							t.Fatal("fixture did not prepare both documents")
						}
					}
					if calls := recorder.calls.Load(); calls != wantCalls {
						t.Fatalf("embedding calls = %d, want %d; revoked authority reached embedding", calls, wantCalls)
					}
					if err := eng.store.View(t.Context(), tenant, func(sc store.Scope) error {
						repo, err := sc.Ext("knowledge.base")
						if err != nil {
							return err
						}
						after, err := repo.Get(t.Context(), model.ID(kbID))
						if err != nil {
							return err
						}
						if after.Int("doc_count") != 0 || after.Int("chunk_count") != 0 {
							t.Fatal("changed source persisted knowledge")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					ingested := false
					if err := eng.store.View(t.Context(), tenant, func(sc store.Scope) error {
						return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
							ingested = ingested || event.Action == "knowledge.ingest"
							return nil
						})
					}); err != nil || ingested {
						t.Fatalf("refused source claimed an ingest audit: %v", err)
					}
				})
			}
		})
	}
}

type wireRevokeDuringPreparation struct{ revoke func() }

func (c wireRevokeDuringPreparation) Classify(string) ([]knowledge.SensitivityHit, error) {
	c.revoke()
	return nil, nil
}
func (wireRevokeDuringPreparation) Version() string { return "fixture-preparation-revocation" }

// The fixture counts the real embedding port, without transmitting private data.
type wireIngestEmbedder struct {
	knowledge.LocalHashEmbedder
	calls      atomic.Int32
	afterFirst func()
}

func (e *wireIngestEmbedder) Embed(ctx context.Context, tenant model.TenantID, texts []string) ([][]float32, string, error) {
	n := e.calls.Add(1)
	vectors, modelRef, err := e.LocalHashEmbedder.Embed(ctx, tenant, texts)
	if n == 1 && err == nil && e.afterFirst != nil {
		e.afterFirst()
	}
	return vectors, modelRef, err
}
func (*wireIngestEmbedder) AllowsEgress() bool { return true }
func (*wireIngestEmbedder) ModelRef() string   { return "fixture-hosted-embedding" }
