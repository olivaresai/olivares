// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

func TestProxyToolVisibilitySurvivesForwardAndCanonicalReadback(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testProxyToolVisibilityReadback(t, store.EngineSQLite) })
	if enginetest.PostgresAvailable(t) {
		t.Run("postgres", func(t *testing.T) { testProxyToolVisibilityReadback(t, store.EnginePostgres) })
	}
}

func testProxyToolVisibilityReadback(t *testing.T, backend store.Engine) {
	for _, tc := range []struct {
		name, want string
		tools      []any
		batch      bool
	}{
		{"ordinary", "full", nil, false},
		{"programmatic", "partial", []any{map[string]any{"type": "code_execution_20260120", "name": "code_execution"}, map[string]any{"name": "TOOL-NAME-CANARY", "input_schema": map[string]any{"type": "object"}, "allowed_callers": []string{"code_execution_20260120"}}}, false},
		{"search", "partial", []any{map[string]any{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}, map[string]any{"name": "TOOL-NAME-CANARY", "defer_loading": true}}, false},
		{"later_batch_entry", "partial", []any{map[string]any{"name": "TOOL-NAME-CANARY", "defer_loading": true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := audit.NewSigner(key)
			if err != nil {
				t.Fatal(err)
			}
			cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent}
			if backend == store.EnginePostgres {
				dsns := enginetest.IsolatedPostgres(t)
				cfg.Engine, cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = backend, dsns.App, dsns.Owner, dsns.Admin
			}
			st, tenant := provisionTenantWithConfig(t, inferenceproxy.New(), "", cfg)
			var calls atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/messages/batches" {
					_, _ = w.Write([]byte(`{"id":"batch-fixture","type":"message_batch","processing_status":"in_progress"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"msg-fixture","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"RESPONSE-CANARY"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			t.Cleanup(upstream.Close)
			inf := claudeapi.NewInference(claudeapi.InferenceConfig{BaseURL: upstream.URL, APIKey: "API-KEY-CANARY", Doer: upstream.Client()})
			a, mg, bg, kg, pol := allowAll()
			a.p = auth.ScopedPrincipal(model.ID("u1"), "fixture", tenant, "editor")
			pol.pol.RecordMandatory = true
			pol.pol.RecordMandatoryChosen = true
			d := newTestDecider(a, mg, bg, kg, pol)
			d.Store, d.Inference = st, inf
			req := userReq("PROMPT-CANARY", false)
			req.Tools = tc.tools
			path := "/v1/messages"
			var body any = req
			if tc.batch {
				path = "/v1/messages/batches"
				body = map[string]any{"requests": []claudeapi.BatchRequest{{CustomID: "ordinary", Params: userReq("PROMPT-CANARY", false)}, {CustomID: "deferred", Params: req}}}
			}
			proxy := claudeapi.NewMessagesProxy(inf, d, nil, nil)
			response := serveProxy(t, proxy, path, body)
			if response.Code != http.StatusOK || calls.Load() != 1 {
				t.Fatalf("forward = %d, calls=%d: %s", response.Code, calls.Load(), response.Body.String())
			}
			found := 0
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().(store.CanonicalWalker).WalkCanonical(context.Background(), 1, func(ev model.AuditEvent, meta string, _ []byte) error {
					if !strings.HasPrefix(ev.Action, "inference.proxy.") {
						return nil
					}
					found++
					var fields map[string]any
					if err := json.Unmarshal([]byte(meta), &fields); err != nil {
						return err
					}
					if fields["tool_visibility"] != tc.want || len(ev.Sig) == 0 {
						t.Fatalf("retained signed coverage = %s, want %s", meta, tc.want)
					}
					for _, canary := range []string{"PROMPT-CANARY", "RESPONSE-CANARY", "TOOL-NAME-CANARY", "API-KEY-CANARY"} {
						if strings.Contains(meta, canary) {
							t.Fatalf("retained inference content: %s", meta)
						}
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if found != 2 {
				t.Fatalf("retained %d intent/outcome records, want 2", found)
			}
			if err := st.View(context.Background(), model.NewTenantID(), func(sc store.Scope) error {
				return sc.Audit().(store.CanonicalWalker).WalkCanonical(context.Background(), 1, func(ev model.AuditEvent, _ string, _ []byte) error {
					if strings.HasPrefix(ev.Action, "inference.proxy.") {
						t.Fatal("foreign tenant read request evidence")
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			a.err = auth.ErrUnauthenticated
			d.Auth = a
			denied := serveProxy(t, proxy, path, body)
			if denied.Code != http.StatusUnauthorized || calls.Load() != 1 {
				t.Fatal("refused request reached upstream")
			}
			// Evidence remains a prerequisite even when the tool annotation is partial.
			a.err = nil
			d.Auth, d.Store = a, nil
			unrecorded := serveProxy(t, proxy, path, body)
			if unrecorded.Code != http.StatusServiceUnavailable || calls.Load() != 1 {
				t.Fatal("tool annotation bypassed mandatory evidence")
			}
		})
	}
}
