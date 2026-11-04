// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

func TestWorkspaceKnowledgeIngestAndScopedRetrieval(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			eng, token, tenant := wireBoot(t, cfg, t.TempDir())
			var calls atomic.Int32
			var expected atomic.Value
			expected.Store("workspace-credential-one")
			var failSource atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if failSource.Load() {
					http.Error(w, "workspace-credential-two upstream fixture failure", http.StatusBadGateway)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
					http.Error(w, "fixture authentication refused", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/spaces/") {
					_, _ = w.Write([]byte(`{"results":[{"id":"page-1","title":"Calibration"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"id":"page-1","title":"Calibration","body":{"storage":{"value":"<p>Engineering turbine calibration note</p>"}},"space":{"key":"ENG"}}`))
				}
			}))
			t.Cleanup(srv.Close)
			request := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
				if code != want {
					t.Fatalf("%s %s = %d, want %d (%s)", method, path, code, want, raw)
				}
				if strings.Contains(raw, "workspace-credential-one") || strings.Contains(raw, "workspace-credential-two") {
					t.Fatal("workspace response disclosed a credential")
				}
				return out
			}
			// The mocked Confluence page carries no permissions, so its documents have no ACL
			// and are denied unless the KB says otherwise: default_acl anyone is that decision.
			kb := request("POST", "/v1/m/knowledge/kbs", map[string]any{"name": "engineering", "classification": "public", "embed_policy": "local_only", "default_acl": []string{"anyone"}}, http.StatusCreated)
			kbID := kb["id"].(string)
			request("POST", "/v1/m/sourcescope/bindings", map[string]any{
				"source_type": "knowledge", "source_ref": kbID, "scope_tree": "workspace", "scope_ref": "default", "enabled": true,
			}, http.StatusCreated)
			connector := request("POST", "/v1/m/sourcescope/workspace-connectors", map[string]any{
				"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true,
				"config":  map[string]string{"mode": "live", "base_url": srv.URL, "space_key": "ENG"},
				"secrets": map[string]string{"credential_ref": "workspace-credential-one"},
			}, http.StatusCreated)
			out := request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs"}, http.StatusOK)
			if out["documents"] != float64(1) || calls.Load() != 2 {
				t.Fatal("workspace connector was not pulled through the existing knowledge pipeline")
			}
			expected.Store("workspace-credential-two")
			request("PUT", "/v1/m/sourcescope/workspace-connectors/"+connector["id"].(string), map[string]any{
				"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true,
				"config":  map[string]string{"mode": "live", "base_url": srv.URL, "space_key": "ENG"},
				"secrets": map[string]string{"credential_ref": "workspace-credential-two"},
			}, http.StatusOK)
			out = request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs"}, http.StatusOK)
			if out["documents"] != float64(1) || calls.Load() != 4 {
				t.Fatal("workspace ingest reused stale connector credentials")
			}
			failSource.Store(true)
			failed := request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs"}, http.StatusBadGateway)
			if strings.Contains(failed["error"].(map[string]any)["message"].(string), srv.URL) {
				t.Fatal("workspace ingest exposed upstream connection details")
			}
			failSource.Store(false)
			audited := 0
			if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
					if event.Action == "knowledge.ingest" {
						audited++
					}
					return nil
				})
			}); err != nil || audited != 2 {
				t.Fatalf("successful ingests must be audited, failed pull must not claim ingest: %d (%v)", audited, err)
			}
			if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				ws, err := sc.DefaultWorkspace(context.Background())
				if err != nil {
					return err
				}
				other, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "Marketing", Slug: "marketing"})
				if err != nil {
					return err
				}
				for _, item := range []struct {
					ref       string
					workspace model.ID
				}{{"eng-agent", ws.ID}, {"mkt-agent", other.ID}} {
					if _, err := sc.Agents().Create(context.Background(), model.Agent{ExternalID: item.ref, Name: item.ref, WorkspaceID: item.workspace, Status: model.StatusActive}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Source confinement preserves tenant-wide RBAC by contract. An explicit
			// workspace forbid is absolute and must still deny the Marketing agent.
			request("POST", "/v1/m/sourcescope/bindings", map[string]any{
				"source_type": "knowledge", "source_ref": kbID, "scope_tree": "workspace", "scope_ref": "marketing", "effect": "forbid", "enabled": true,
			}, http.StatusCreated)
			up, err := newRetrievalUpstream(retrievalUpstreamConfig{Module: eng.knowledgeMod, Store: eng.store, Tenant: tenant, Role: "viewer", Log: quietLog()})
			if err != nil {
				t.Fatal(err)
			}
			params, _ := json.Marshal(map[string]any{"name": "search_kb", "arguments": map[string]any{"kb_id": kbID, "query": "turbine calibration", "top_k": 3}})
			for _, subject := range []string{"eng-agent", "mkt-agent"} {
				raw, err := up.handleToolsCall(context.Background(), mcp.UpstreamRequest{Subject: subject, Params: params})
				if err != nil {
					t.Fatal(err)
				}
				visible := strings.Contains(string(raw), "Engineering turbine calibration note")
				if visible != (subject == "eng-agent") {
					t.Fatalf("MCP retrieval visibility was wrong for %s", subject)
				}
			}
		})
	}
}

func TestWorkspaceKnowledgeIngestRefusals(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			eng, token, tenant := wireBoot(t, cfg, t.TempDir())
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "refused requests must never reach this source", http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)
			request := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
				if code != want {
					t.Fatalf("%s %s = %d, want %d (%s)", method, path, code, want, raw)
				}
				return out
			}
			request("POST", "/v1/users", map[string]any{"email": "wire-approver@example.invalid", "password": "fixture-password-2", "tenant": tenant.String(), "role": "admin"}, http.StatusCreated)
			code, login, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/auth/login", "", "", map[string]any{"email": "wire-approver@example.invalid", "password": "fixture-password-2"})
			if code != http.StatusOK {
				t.Fatalf("distinct approver login = %d", code)
			}
			approver := login["token"].(string)
			for _, tc := range []struct {
				name       string
				binding    string
				kind       string
				mode       string
				secret     string
				disabled   bool
				exportPath bool
				status     int
			}{
				{name: "unbound", status: http.StatusConflict},
				{name: "role-global", binding: "role", status: http.StatusConflict},
				{name: "mixed-global", binding: "mixed", status: http.StatusConflict},
				{name: "unknown", binding: "workspace", kind: "unknown", status: http.StatusBadRequest},
				{name: "disabled", binding: "workspace", disabled: true, status: http.StatusNotFound},
				{name: "env", binding: "workspace", secret: "env:WIREB_FIXTURE_ONLY", status: http.StatusForbidden},
				{name: "file-ref", binding: "workspace", secret: "file:/fixture-only/not-read", status: http.StatusForbidden},
				{name: "global-ref", binding: "workspace", secret: "store:global-fixture", status: http.StatusForbidden},
				{name: "foreign-ref", binding: "workspace", secret: "store:ws/other-workspace/fixture", status: http.StatusForbidden},
				{name: "export", binding: "workspace", mode: "export", status: http.StatusBadRequest},
				{name: "export-path", binding: "workspace", exportPath: true, status: http.StatusBadRequest},
				{name: "filesystem", binding: "workspace", kind: "filesystem", status: http.StatusBadRequest},
				{name: "s3-fallback", binding: "workspace", kind: "s3content", status: http.StatusBadRequest},
				{name: "postgres-files", binding: "workspace", kind: "postgres", status: http.StatusBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					name := "kb-" + tc.name
					kb := request("POST", "/v1/m/knowledge/kbs", map[string]any{"name": name, "classification": "public", "embed_policy": "local_only"}, http.StatusCreated)
					if tc.binding == "workspace" || tc.binding == "mixed" {
						request("POST", "/v1/m/sourcescope/bindings", map[string]any{"source_type": "knowledge", "source_ref": kb["id"].(string), "scope_tree": "workspace", "scope_ref": "default", "enabled": true}, http.StatusCreated)
					}
					if tc.binding == "role" || tc.binding == "mixed" {
						want := http.StatusCreated
						if tc.binding == "mixed" {
							want = http.StatusAccepted
						}
						created := request("POST", "/v1/m/sourcescope/bindings", map[string]any{"source_type": "knowledge", "source_ref": kb["id"].(string), "scope_tree": "role", "scope_ref": "admin", "enabled": true}, want)
						if want == http.StatusAccepted {
							code, out, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sourcescope/posture-requests/"+created["id"].(string)+"/approve", approver, tenant.String(), nil)
							if code != http.StatusOK || out["status"] != "approved" {
								t.Fatalf("approve mixed binding = %d", code)
							}
						}
					}
					kind, mode := tc.kind, tc.mode
					if kind == "" {
						kind = "confluence"
					}
					if mode == "" {
						mode = "live"
					}
					config := map[string]string{"mode": mode, "base_url": srv.URL, "space_key": "ENG"}
					if tc.exportPath {
						config["export_path"] = "/fixture-only/not-read.json"
					}
					secrets := map[string]string{}
					if tc.secret != "" {
						secrets["credential_ref"] = tc.secret
					}
					request("POST", "/v1/m/sourcescope/workspace-connectors", map[string]any{"name": tc.name, "kind": kind, "workspace_ref": "default", "enabled": !tc.disabled, "config": config, "secrets": secrets}, http.StatusCreated)
					request("POST", "/v1/m/knowledge/kbs/"+kb["id"].(string)+"/ingest", map[string]any{"source": "workspace:" + tc.name}, tc.status)
					if calls.Load() != 0 {
						t.Fatal("refused ingest opened the source")
					}
				})
			}
		})
	}
}

// A workspace source set to identity "user" reads with the credential the requesting
// person keeps in their own vault, never with a shared one: no credential in the vault
// means "connect your account" and no request to the source, and what is read is stored
// private to that person.
func TestWorkspaceKnowledgeIngestReadsAsTheUser(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			const personToken = "person-credential-one"
			eng, token, tenant := wireBoot(t, cfg, t.TempDir())
			person, err := eng.authr.Authenticate(context.Background(), token)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var seen atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				seen.Store(r.Header.Get("Authorization"))
				if r.Header.Get("Authorization") != "Bearer "+personToken {
					http.Error(w, "fixture authentication refused", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/spaces/") {
					_, _ = w.Write([]byte(`{"results":[{"id":"page-1","title":"Calibration"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"id":"page-1","title":"Calibration","body":{"storage":{"value":"<p>Engineering turbine calibration note</p>"}},"space":{"key":"ENG"}}`))
				}
			}))
			t.Cleanup(srv.Close)
			request := func(method, path string, body any, want int) (map[string]any, string) {
				t.Helper()
				code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
				if code != want {
					t.Fatalf("%s %s = %d, want %d (%s)", method, path, code, want, raw)
				}
				if strings.Contains(raw, personToken) {
					t.Fatal("a response disclosed the person's credential")
				}
				return out, raw
			}
			kb, _ := request("POST", "/v1/m/knowledge/kbs", map[string]any{"name": "engineering", "classification": "public", "embed_policy": "local_only", "default_acl": []string{"anyone"}}, http.StatusCreated)
			kbID := kb["id"].(string)
			request("POST", "/v1/m/sourcescope/bindings", map[string]any{
				"source_type": "knowledge", "source_ref": kbID, "scope_tree": "workspace", "scope_ref": "default", "enabled": true,
			}, http.StatusCreated)
			connector := map[string]any{
				"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true,
				"config": map[string]string{"mode": "live", "base_url": srv.URL, "space_key": "ENG", "identity": "user"},
			}
			request("POST", "/v1/m/sourcescope/workspace-connectors", connector, http.StatusCreated)
			ingest := func(want int) (map[string]any, string) {
				return request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs"}, want)
			}

			// No credential in the person's vault: fail closed, the source is never asked.
			_, raw := ingest(http.StatusForbidden)
			if !strings.Contains(raw, "connect your account") || calls.Load() != 0 {
				t.Fatalf("without a credential: %s, source calls %d; want \"connect your account\" and no call", raw, calls.Load())
			}

			// With one, the source is read with that credential and with no other.
			request("PUT", "/v1/console/secrets?scope=tenant", map[string]any{
				"name":  "vault/" + person.UserID.String() + "/sources/docs",
				"value": `{"type":"login","password":"` + personToken + `","url":"` + srv.URL + `"}`,
			}, http.StatusOK)
			out, _ := ingest(http.StatusOK)
			if out["documents"] != float64(1) || seen.Load() != "Bearer "+personToken {
				t.Fatalf("ingest as the user: documents=%v, source saw %v", out["documents"], seen.Load())
			}

			// What was read is private to the person: the source's own rights, not the KB default.
			docs, _ := request("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", nil, http.StatusOK)
			items := docs["items"].([]any)
			acl, _ := items[0].(map[string]any)["acl"].([]any)
			if len(items) != 1 || len(acl) != 1 || acl[0] != "user:"+person.UserID.String() {
				t.Fatalf("documents read as the user must be private to them: %v", items)
			}

			// The token goes only where the person stored it: a connector later pointed at
			// another host, or a login that names no host, is refused and nothing is sent.
			var elsewhere atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				elsewhere.Add(1)
				http.Error(w, "must not be reached", http.StatusTeapot)
			}))
			t.Cleanup(other.Close)
			connector["name"] = "docs-moved"
			connector["config"] = map[string]string{"mode": "live", "base_url": other.URL, "space_key": "ENG", "identity": "user"}
			request("POST", "/v1/m/sourcescope/workspace-connectors", connector, http.StatusCreated)
			for _, item := range []string{
				`{"type":"login","password":"` + personToken + `","url":"` + srv.URL + `"}`,
				`{"type":"login","password":"` + personToken + `"}`,
			} {
				request("PUT", "/v1/console/secrets?scope=tenant", map[string]any{
					"name": "vault/" + person.UserID.String() + "/sources/docs-moved", "value": item,
				}, http.StatusOK)
				_, raw := request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs-moved"}, http.StatusForbidden)
				if !strings.Contains(raw, "connect your account") || strings.Contains(raw, personToken) || elsewhere.Load() != 0 {
					t.Fatalf("token bound to another origin: %s, requests to the other host %d; want a refusal and none", raw, elsewhere.Load())
				}
			}

			// A stored credential that is not a login item is named, not called missing.
			request("PUT", "/v1/console/secrets?scope=tenant", map[string]any{
				"name": "vault/" + person.UserID.String() + "/sources/docs-moved", "value": `{"type":"note","note":"not a login"}`,
			}, http.StatusOK)
			_, raw = request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs-moved"}, http.StatusForbidden)
			if !strings.Contains(raw, "not a login") {
				t.Fatalf("a note stored as the credential: %s; want it named", raw)
			}

			// A shared credential on a source that reads as the user is ambiguous: refused.
			connector["name"] = "docs-shared"
			connector["config"] = map[string]string{"mode": "live", "base_url": srv.URL, "space_key": "ENG", "identity": "user"}
			connector["secrets"] = map[string]string{"credential_ref": "workspace-credential-one"}
			request("POST", "/v1/m/sourcescope/workspace-connectors", connector, http.StatusCreated)
			before := calls.Load()
			request("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", map[string]any{"source": "workspace:docs-shared"}, http.StatusBadRequest)
			if calls.Load() != before {
				t.Fatal("a source holding both a shared credential and identity user reached the network")
			}
		})
	}
}

// Only a signed-in person has a vault: an API token or an agent session asking to read as
// the user is refused before the vault is touched (the nil vault would panic if it were).
func TestPersonCredentialRefusesANonPerson(t *testing.T) {
	id := model.NewID()
	fields := map[string]sdk.ConfigField{"credential_ref": {Key: "credential_ref", Secret: true}}
	for label, p := range map[string]auth.Principal{
		"agent":   {Kind: auth.KindUser, UserID: id, AgentIdentity: "agent-1"},
		"session": {Kind: auth.KindUser, UserID: id, SessionIdentity: "session-1"},
	} {
		_, refusal, status := workspaceKnowledgeSources{}.personCredential(context.Background(), api.ModuleContext{Principal: p}, "docs", "confluence", map[string]string{"base_url": "https://wiki.example"}, fields)
		if status != http.StatusForbidden || !strings.Contains(refusal, "signed-in person") {
			t.Errorf("%s: got %d %q, want 403 and the signed-in person refusal", label, status, refusal)
		}
	}
}
