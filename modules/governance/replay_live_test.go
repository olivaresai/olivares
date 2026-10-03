// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

func TestProjectionHistoryKeepsPurposeAndRequester(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			opts := harnessOpts{}
			if backend == "postgres" {
				pg := enginetest.IsolatedPostgres(t)
				opts.engine, opts.dsn, opts.adminDSN = store.EnginePostgres, pg.App, pg.Admin
			}
			h := newHarnessWith(t, opts)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "projection-history")
			editorID, editor := h.roleUser(admin, tenant, "editor@projection.test", auth.RoleEditor)
			agent := h.createAgentIn(tenant, "projection-agent", "")
			requester, err := h.authr.Authenticate(t.Context(), admin)
			if err != nil {
				t.Fatal(err)
			}
			actor, err := requester.AttributableActor()
			if err != nil {
				t.Fatal(err)
			}
			q := governance.QuestionForReplay(editorID, "olivares", "agent", agent.ID.String(), "agent:write", "olivares.permission.v1")
			digest, err := q.Digest()
			if err != nil {
				t.Fatal(err)
			}
			answers := func() []model.AuthorizationDecision {
				t.Helper()
				var rows []model.AuthorizationDecision
				if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
					var err error
					rows, err = sc.AccessEvidence().AuthorizationDecisionsForQuestion(t.Context(), digest)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			checkActor := func(d model.AuthorizationDecision) {
				t.Helper()
				anchors := map[string]bool{d.ID.String(): true, d.Decision.PolicyVersionID: true}
				seen := 0
				if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
					return sc.Audit().Walk(t.Context(), 0, func(ev model.AuditEvent) error {
						if !anchors[ev.TargetID.String()] {
							return nil
						}
						seen++
						if ev.Actor != actor || ev.ActorKind != requester.ActorKind() {
							t.Errorf("cross-subject history actor = %s/%s, want authenticated requester %s/%s", ev.Actor, ev.ActorKind, actor, requester.ActorKind())
						}
						if ev.TargetID == d.ID && hex.EncodeToString(ev.Hash) != d.LedgerRef {
							t.Error("decision ledger reference lost its audit anchor")
						}
						return nil
					})
				}); err != nil {
					t.Fatal(err)
				}
				if seen != 2 {
					t.Errorf("history audit anchors = %d, want artifact and decision", seen)
				}
			}
			r := h.do("POST", "/access/v1/evaluation", admin, map[string]any{
				"subject": map[string]any{"type": "user", "id": editorID}, "action": map[string]any{"name": "agent:write"},
				"resource": map[string]any{"type": "agent", "id": agent.ID.String()},
			}, tenantHdr(tenant))
			if r.code != http.StatusOK || r.body["decision"] != true {
				t.Fatalf("real PDP query = %d %s", r.code, r.raw)
			}
			rows := answers()
			if len(rows) != 1 {
				t.Fatalf("PDP answers = %d, want one", len(rows))
			}
			checkActor(rows[0])
			if r := h.do("PATCH", "/v1/agents/"+agent.ID.String(), editor, map[string]any{"name": "actual-write"}, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("real editor write = %d %s", r.code, r.raw)
			}
			h.authorPolicy(admin, tenant, "deny-projection-write", map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "agent:write"}}})
			r = h.do("POST", "/access/v1/access-review/export", admin, map[string]any{
				"resource": map[string]any{"type": "agent", "id": agent.ID.String()}, "permissions": []string{"agent:write"},
			}, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("real access review = %d %s", r.code, r.raw)
			}
			found := false
			for _, d := range answers() {
				if d.Decision.Outcome != sdk.AccessOutcomeDeny {
					continue
				}
				found = true
				if d.Decision.Purpose != sdk.PurposeCurrentWhatIf {
					t.Errorf("access-review projection purpose = %s, want current_what_if", d.Decision.Purpose)
				}
				checkActor(d)
			}
			if !found {
				t.Error("review did not retain its evaluated editor denial")
			}
			reader := governance.New()
			reader.UseData(api.NewModuleData(h.st))
			got, err := reader.Reconstruct(t.Context(), tenant, governance.ReconstructRequest{
				At: time.Now().UTC(), Principal: editorID, Resource: agent.ID.String(), ResourceKind: "agent",
				Action: "agent:write", SourceInstance: "olivares", ActionVocabulary: "olivares.permission.v1",
			})
			if err != nil || got.Status != governance.ReconstructReconstructed || got.RecordedOutcome != sdk.AccessOutcomeAllow || got.Outcome != sdk.AccessOutcomeAllow || got.UsedLivePolicy {
				t.Fatalf("projection replaced real action's history: %+v, %v", got, err)
			}
		})
	}
}

func TestLiveAuthorizationReplaysRetainedPolicy(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			opts := harnessOpts{}
			if backend == "postgres" {
				pg := enginetest.IsolatedPostgres(t)
				opts.engine, opts.dsn, opts.adminDSN = store.EnginePostgres, pg.App, pg.Admin
			}
			h := newHarnessWith(t, opts)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "replay-live")
			editorID, editorToken := h.roleUser(admin, tenant, "editor@replay.test", auth.RoleEditor)
			viewerID, viewerToken := h.roleUser(admin, tenant, "viewer@replay.test", auth.RoleViewer)
			agent := h.createAgentIn(tenant, "replay-agent", "")
			writes := 0
			writeAgent := func(token string, want int) time.Time {
				t.Helper()
				writes++
				r := h.do("PATCH", "/v1/agents/"+agent.ID.String(), token,
					map[string]any{"name": "replay-write-" + strconv.Itoa(writes)}, tenantHdr(tenant))
				if r.code != want {
					t.Fatalf("real agent write: %d %s, want %d", r.code, r.raw, want)
				}
				return time.Now().UTC()
			}
			ask := func(principal string, want bool) time.Time {
				t.Helper()
				r := h.do("POST", "/access/v1/evaluation", admin, map[string]any{
					"subject":  map[string]any{"type": "user", "id": principal},
					"action":   map[string]any{"name": "agent:write"},
					"resource": map[string]any{"type": "agent", "id": agent.ID.String()},
				}, tenantHdr(tenant))
				if r.code != http.StatusOK || r.body["decision"] != want {
					t.Fatalf("live decision: %d %s, want %t", r.code, r.raw, want)
				}
				return time.Now().UTC()
			}
			checkReplay := func(principal string, at time.Time, outcome string) {
				t.Helper()
				r := h.do("POST", "/v1/m/governance/decisions/replay", admin, map[string]any{
					"at": at.Format(time.RFC3339Nano), "principal": principal,
					"resource": agent.ID.String(), "resource_kind": "agent", "action": "agent:write",
					"source_instance": "olivares", "action_vocabulary": "olivares.permission.v1",
				}, tenantHdr(tenant))
				if r.code != http.StatusOK || r.body["status"] != "reconstructed" || r.body["outcome"] != outcome || r.body["recorded_outcome"] != outcome || r.body["used_live_policy"] != false {
					t.Fatalf("historical replay: %d %s, want reconstructed %s without live policy", r.code, r.raw, outcome)
				}
			}
			allowAt := ask(editorID, true)
			denyAt := ask(viewerID, false)
			writeAllowAt := writeAgent(editorToken, http.StatusOK)
			writeDenyAt := writeAgent(viewerToken, http.StatusForbidden)
			checkReplay(editorID, allowAt, "allow")
			checkReplay(viewerID, denyAt, "deny")
			// Changing the active policy must not change the earlier RBAC answers.
			h.publishGrant(admin, tenant, `forbid(principal, action == Action::"agent:write", resource);`)
			cedarDenyAt := ask(editorID, false)
			h.publishGrant(admin, tenant, `permit(principal in User::"`+viewerID+`", action == Action::"agent:write", resource);`)
			cedarAllowAt := ask(viewerID, true)
			h.publishGrant(admin, tenant, "")
			policyID := h.authorPolicy(admin, tenant, "deny-agent-write", map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "agent:write"}}})
			abacDenyAt := ask(editorID, false)
			if r := h.do("DELETE", "/v1/m/governance/policies/"+policyID, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
				t.Fatalf("remove ABAC: %d %s", r.code, r.raw)
			}
			abacAllowAt := ask(editorID, true)
			checkReplay(viewerID, cedarAllowAt, "allow")
			checkReplay(editorID, abacDenyAt, "deny")
			checkReplay(editorID, abacAllowAt, "allow")
			checkReplay(editorID, allowAt, "allow")
			checkReplay(viewerID, denyAt, "deny")
			checkReplay(editorID, cedarDenyAt, "deny")
			// A new reader has no compiled policy or authorizer from the live request.
			reader := governance.New()
			reader.UseData(api.NewModuleData(h.st))
			other := h.createOrg(admin, "replay-isolated")
			for _, historical := range []struct {
				principal string
				at        time.Time
				outcome   string
			}{
				{editorID, writeAllowAt, "allow"}, {viewerID, writeDenyAt, "deny"},
				{editorID, cedarDenyAt, "deny"}, {viewerID, cedarAllowAt, "allow"},
				{editorID, abacDenyAt, "deny"}, {editorID, abacAllowAt, "allow"},
			} {
				request := governance.ReconstructRequest{At: historical.at, Principal: historical.principal,
					Resource: agent.ID.String(), ResourceKind: "agent", Action: "agent:write",
					SourceInstance: "olivares", ActionVocabulary: "olivares.permission.v1"}
				got, err := reader.Reconstruct(t.Context(), tenant, request)
				if err != nil || got.Status != governance.ReconstructReconstructed || string(got.Outcome) != historical.outcome || got.UsedLivePolicy {
					t.Fatalf("fresh reader: %+v, %v; want %s", got, err, historical.outcome)
				}
				// An identical question in a different tenant cannot see these rows.
				foreign, err := reader.Reconstruct(t.Context(), other, request)
				if err != nil || foreign.Status != governance.ReconstructInsufficient || foreign.Missing != governance.MissingAuthorizationDecision {
					t.Fatalf("cross-tenant history: %+v, %v", foreign, err)
				}
			}
		})
	}
}
