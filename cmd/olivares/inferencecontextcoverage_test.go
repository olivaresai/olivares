// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

// Exercise admission and read the canonical metadata actually retained by the ledger.
func TestInferenceContextCoverageRetained(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		ctx := context.Background()
		ipx := inferenceproxy.New()
		st, tenant := provisionTenantWithConfig(t, ipx, "", cfg)
		inf := claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "test", Gateway: "direct", Doer: countTokensDoer{}})
		d := storeBackedDecider(st, tenant, ipx, "", nil, inf)
		cases := []struct {
			name  string
			edits []string
			want  string
		}{
			{"inactive", nil, "inference_metadata_only"},
			{"compaction", []string{"compact_20260112"}, "provider_context_may_change"},
			{"clear tools", []string{"clear_tool_uses_20250919"}, "provider_context_may_change"},
			{"clear thinking", []string{"clear_thinking_20251015"}, "provider_context_may_change"},
			{"unknown edit", []string{"future_edit"}, "provider_context_unknown"},
			{"header is not an edit", []string{"context-management-2025-06-27"}, "provider_context_unknown"},
			{"mixed unknown", []string{"compact_20260112", "future_edit"}, "provider_context_unknown"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var after int64
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					head, _, err := sc.Audit().Head(ctx)
					after = head.Seq
					return err
				}); err != nil {
					t.Fatal(err)
				}
				req := userReq("coverage fixture", false)
				if tc.edits != nil {
					req.ContextManagement = &claudeapi.ContextManagement{}
					for _, edit := range tc.edits {
						req.ContextManagement.Edits = append(req.ContextManagement.Edits, claudeapi.ContextEdit{Type: edit})
					}
				}
				dec := d.Authorize(ctx, req, "fixture")
				if !dec.Allow {
					t.Fatalf("admission refused: %d %s", dec.Status, dec.Reason)
				}
				d.Finalize(ctx, dec.Session, claudeapi.ProxyForwardResult{UpstreamStatus: 200})
				batch := d.AuthorizeBatch(ctx, []claudeapi.BatchRequest{
					{CustomID: "plain", Params: userReq("plain fixture", false)},
					{CustomID: "context", Params: req},
				}, "fixture")
				if !batch.Allow {
					t.Fatalf("batch admission refused: %d %s", batch.Status, batch.Reason)
				}
				d.FinalizeBatch(ctx, batch.Session, claudeapi.ProxyBatchForwardResult{Entries: 2, UpstreamStatus: 200})
				seen := 0
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					return sc.Audit().(store.CanonicalWalker).WalkCanonical(ctx, after+1, func(ev model.AuditEvent, raw string, _ []byte) error {
						switch ev.Action {
						case "inference.proxy.authorized", "inference.proxy.recorded", "inference.proxy.batch.authorized", "inference.proxy.batch.recorded":
						default:
							return nil
						}
						var meta map[string]any
						if err := json.Unmarshal([]byte(raw), &meta); err != nil {
							return err
						}
						if meta["context_coverage"] != tc.want {
							t.Errorf("%s coverage = %v, want %s", ev.Action, meta["context_coverage"], tc.want)
						}
						seen++
						return nil
					})
				}); err != nil {
					t.Fatal(err)
				}
				if seen != 4 {
					t.Fatalf("retained %d inference events, want single and batch intent/outcome", seen)
				}
			})
		}
	})
}
