// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestApprovalListsUseEffectiveStatusWithoutLosingPagination(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "effective-status")
	expired := map[string]bool{}
	for i := 0; i < 3; i++ {
		made := h.createApproval(admin, tenant, map[string]any{"action": "deploy", "expires_in_seconds": 60})
		if made.code != http.StatusCreated {
			t.Fatalf("create=%d %s", made.code, made.raw)
		}
		expired[made.body["id"].(string)] = true
	}
	h.clk.advance(61 * time.Second)
	live := map[string]bool{}
	for i := 0; i < 2; i++ {
		made := h.createApproval(admin, tenant, map[string]any{"action": "deploy", "expires_in_seconds": 60})
		if made.code != http.StatusCreated {
			t.Fatalf("create=%d %s", made.code, made.raw)
		}
		live[made.body["id"].(string)] = true
	}
	// Interleave stored deadline expiry after live rows. Its TTL remains in the
	// future, so the expired list must combine both forms without losing a cursor.
	for i := 0; i < 2; i++ {
		made := h.createApproval(admin, tenant, map[string]any{"action": "deploy", "expires_in_seconds": 60})
		if made.code != http.StatusCreated {
			t.Fatalf("create=%d %s", made.code, made.raw)
		}
		id := made.body["id"].(string)
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		answer, err := h.gov.EngineApprovals().Wait(ctx, tenant, id)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || answer.Status != "expired" {
			t.Fatalf("interleaved deadline expiry=%+v %v", answer, err)
		}
		expired[id] = true
	}
	for _, status := range []string{"pending", "expired"} {
		want := live
		if status == "expired" {
			want = expired
		}
		got := map[string]bool{}
		cursor := ""
		for page := 0; page < 6; page++ {
			resp := h.do("GET", govPath+"/approvals?status="+status+"&action=deploy&limit=1&cursor="+url.QueryEscape(cursor), admin, nil, tenantHdr(tenant))
			if resp.code != http.StatusOK {
				t.Fatalf("list=%d %s", resp.code, resp.raw)
			}
			items, _ := resp.body["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("effective %s page has %d items, want1", status, len(items))
			}
			item := items[0].(map[string]any)
			id := item["id"].(string)
			if item["status"] != status || !want[id] || got[id] {
				t.Fatalf("wrong effective page: %v", item)
			}
			got[id] = true
			more, _ := resp.body["has_more"].(bool)
			if !more {
				break
			}
			cursor, _ = resp.body["cursor"].(string)
			if cursor == "" {
				t.Fatal("matching continuation lost its cursor")
			}
		}
		if len(got) != len(want) {
			t.Fatalf("effective %s pagination found %d, want%d", status, len(got), len(want))
		}
		items, _, err := h.gov.EngineApprovals().List(t.Context(), tenant, "deploy", status, "")
		if err != nil || len(items) != len(want) {
			t.Fatalf("in-process effective %s=%d, %v", status, len(items), err)
		}
		for _, item := range items {
			if item.Status != status || !want[item.ID] {
				t.Fatalf("in-process list disagrees: %+v", item)
			}
		}
	}
}

func TestEngineApprovalWaitDeadlineKeepsTerminalDecision(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "expiry-terminal")
			principal := cancellationSession(t, h, tenant, admin, decision)
			service := h.gov.EngineApprovals()
			pending, err := service.Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "expiry-terminal", SessionRef: principal.SessionIdentity, ExpiresInSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			resp := h.do("POST", govPath+"/approvals/"+pending.ID+"/decisions", admin, map[string]any{"decision": decision}, tenantHdr(tenant))
			if resp.code != http.StatusOK {
				t.Fatalf("decision=%d %s", resp.code, resp.raw)
			}
			ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer cancel()
			answer, err := service.Wait(ctx, tenant, pending.ID)
			want := "approved"
			if decision == "reject" {
				want = "rejected"
			}
			if !errors.Is(err, context.DeadlineExceeded) || answer.Status != want {
				t.Fatalf("deadline replaced terminal decision: %+v %v", answer, err)
			}
			if contains(h.auditActions(tenant), "governance.approval.expire") {
				t.Fatal("deadline overwrote a human decision with expiry")
			}
			if resolved := resolvedPayloads(t, h); len(resolved) != 1 || resolved[0].Outcome != want {
				t.Fatalf("deadline published a second resolution: %+v", resolved)
			}
		})
	}
}

// Exercise the public wait over a real transaction that rolls back its row
// update when the expiry anchor fails. Recovered storage must still expire it.
func TestEngineApprovalDeadlineRecoversRolledBackExpiry(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "expiry-rollback")
	principal := cancellationSession(t, h, tenant, admin, "rollback")
	service := h.gov.EngineApprovals()
	pending, err := service.Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "expiry-rollback", SessionRef: principal.SessionIdentity, ExpiresInSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &expiryAuditRollbackStore{Store: h.st}
	h.gov.UseData(api.NewModuleData(wrapped))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	answer, err := service.Wait(ctx, tenant, pending.ID)
	if !errors.Is(err, context.DeadlineExceeded) || answer.Status != "expired" {
		t.Fatalf("recovered expiry verdict=%+v %v", answer, err)
	}
	if wrapped.attempts.Load() != 2 {
		t.Fatalf("expiry audit attempts=%d, want rollback then recovery", wrapped.attempts.Load())
	}
	items, _, err := service.List(t.Context(), tenant, "claude.tool.use", "pending", "")
	if err != nil || len(items) != 0 {
		t.Fatalf("recovered expiry remains pending: %d %v", len(items), err)
	}
	actions := h.auditActions(tenant)
	if !contains(actions, "governance.approval.expire") || contains(actions, "governance.approval.cancel") {
		t.Fatalf("deadline recovered through a different transition: %v", actions)
	}
	if resolved := resolvedPayloads(t, h); len(resolved) != 1 || resolved[0].Outcome != "expired" {
		t.Fatalf("rolled-back expiry leaked a resolution: %+v", resolved)
	}
}

type expiryAuditRollbackStore struct {
	store.Store
	attempts atomic.Int32
}

func (s *expiryAuditRollbackStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(expiryAuditRollbackScope{Scope: sc, attempts: &s.attempts})
	})
}

type expiryAuditRollbackScope struct {
	store.Scope
	attempts *atomic.Int32
}

func (s expiryAuditRollbackScope) Audit() store.AuditLog {
	return expiryAuditRollbackLog{AuditLog: s.Scope.Audit(), attempts: s.attempts}
}

type expiryAuditRollbackLog struct {
	store.AuditLog
	attempts *atomic.Int32
}

func (l expiryAuditRollbackLog) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if draft.Action == "governance.approval.expire" && l.attempts.Add(1) == 1 {
		return model.AuditEvent{}, errors.New("expiry anchor temporarily unavailable")
	}
	return l.AuditLog.Append(ctx, draft)
}

func TestEngineApprovalWaitPersistsExpiryBeforeReturning(t *testing.T) {
	for _, scenario := range []string{"request expiry", "caller deadline"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "wait-expiry")
			principal := cancellationSession(t, h, tenant, admin, scenario)
			service := h.gov.EngineApprovals()
			pending, err := service.Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "expiry-run", SessionRef: principal.SessionIdentity, Reason: "Bash command: printf reviewed", ExpiresInSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if scenario == "request expiry" {
				h.clk.advance(61 * time.Second)
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			answer, err := service.Wait(ctx, tenant, pending.ID)
			if scenario == "caller deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wait lost deadline: %+v %v", answer, err)
			}
			if scenario == "request expiry" && (err != nil || answer.Status != "expired") {
				t.Fatalf("expiry verdict=%+v %v", answer, err)
			}
			if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("governance.approval")
				if err != nil {
					return err
				}
				rec, err := repo.Get(t.Context(), model.ID(pending.ID))
				if err != nil {
					return err
				}
				if rec.String("status") != "expired" || rec.String("decided_at") == "" || rec.String("reason") != pending.Reason {
					t.Fatalf("expiry did not commit while preserving review facts: %v", rec)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			items, _, err := service.List(t.Context(), tenant, "claude.tool.use", "pending", "")
			if err != nil || len(items) != 0 {
				t.Fatalf("expired request still pending: %d %v", len(items), err)
			}
			if _, err := service.Wait(t.Context(), tenant, pending.ID); err != nil {
				t.Fatal(err)
			}
			if resolved := resolvedPayloads(t, h); len(resolved) != 1 || resolved[0].Outcome != "expired" {
				t.Fatalf("expiry resolution count/verdict=%+v", resolved)
			}
			if !contains(h.auditActions(tenant), "governance.approval.expire") {
				t.Fatal("expiry state has no transaction audit")
			}
		})
	}
}
