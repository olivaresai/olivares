// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cancel exactly at the executor outcome boundary, where an HTTP deadline can
// otherwise discard the result of an infrastructure operation already started.
type cancelRetireExecutor struct {
	Executor
	cancel context.CancelFunc
	fail   bool
}

func (e *cancelRetireExecutor) Retire(ctx context.Context, req ExecRequest) (ExecResult, error) {
	e.cancel()
	if e.fail {
		return ExecResult{}, ctx.Err()
	}
	return e.Executor.Retire(ctx, req)
}

func TestRetireRetainsOutcomeAfterRequestCancellation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "retired"
		if fail {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g := newFakeGate()
			ex := &cancelRetireExecutor{Executor: newMockExecutor(), cancel: cancel, fail: fail}
			h := newHarnessWith(t, WithApprovalGate(g), WithExecutor(ex), WithIdentityBinder(&fakeBinder{firm: true}))
			root := h.adminLogin()
			tid := h.createOrg(root, "cancel-retire")
			tok := h.roleToken(root, tid, "ops@cancel.io", "admin")
			id := h.createDef(tok, tid, "cancel-agent", agentSpec("img:1", "agent:cancel"))
			ref := h.applyPhase1(tok, tid, id).body["approval_ref"].(string)
			g.set(ref, StatusApproved)
			if applied := h.applyPhase2(tok, tid, id, ref); applied.code != http.StatusOK {
				t.Fatalf("apply: %d %s", applied.code, applied.raw)
			}
			path := "/v1/m/deploy/definitions/" + id + "/retire"
			ref = h.do("POST", path, tok, map[string]any{}, tenantHdr(tid)).body["approval_ref"].(string)
			g.set(ref, StatusApproved)
			body, err := json.Marshal(map[string]any{"approval_ref": ref})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("X-Olivares-Tenant", tid.String())
			rec := httptest.NewRecorder()
			h.srv.Handler().ServeHTTP(rec, req)
			if ctx.Err() != context.Canceled {
				t.Fatal("request was not canceled at the executor boundary")
			}
			wantHTTP := http.StatusOK
			if fail {
				wantHTTP = http.StatusBadGateway
			}
			if rec.Code != wantHTTP {
				t.Errorf("retire response: %d want %d: %s", rec.Code, wantHTTP, rec.Body.String())
			}
			ops := h.do("GET", "/v1/m/deploy/operations?definition_id="+id, tok, nil, tenantHdr(tid))
			var ledger listResponse[operationDTO]
			if err := json.Unmarshal([]byte(ops.raw), &ledger); err != nil {
				t.Fatal(err)
			}
			outcome := findOperation(ledger.Items, opRetire, name)
			if ops.code != http.StatusOK || outcome == nil {
				t.Fatalf("lost %s operation after cancellation: %s", name, ops.raw)
			}
			if outcome.ApprovalRef != ref {
				t.Fatalf("lost approval binding: %+v", outcome)
			}
			def := h.do("GET", "/v1/m/deploy/definitions/"+id, tok, nil, tenantHdr(tid))
			desired, appliedVersion := "retired", float64(0)
			if fail {
				desired, appliedVersion = "active", 1
			}
			if def.body["desired_status"] != desired || def.body["applied_version"] != appliedVersion {
				t.Fatalf("definition disagrees with outcome: %s", def.raw)
			}
			wirings := h.do("GET", "/v1/m/deploy/wirings?definition_id="+id, tok, nil, tenantHdr(tid))
			items, ok := wirings.body["items"].([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("lost wiring: %s", wirings.raw)
			}
			wantWiring := "revoked"
			if fail {
				wantWiring = "applied"
			}
			if items[0].(map[string]any)["status"] != wantWiring {
				t.Fatalf("wiring disagrees with outcome: %s", wirings.raw)
			}
			if !fail {
				audit := h.do("GET", "/v1/audit", tok, nil, tenantHdr(tid))
				if audit.code != http.StatusOK || !strings.Contains(audit.raw, "deploy.retire\"") {
					t.Fatalf("lost retirement audit: %s", audit.raw)
				}
			}
		})
	}
}
