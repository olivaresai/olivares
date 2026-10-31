// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/governance"
)

const (
	// A non-critical action: one approver is a quorum (a critical one asks for two).
	statusGatedAction  = "gitpublish.push"
	statusGatedSubject = "gitpublish.target"
)

// changeSystemStub is the change system a status-reading gate's tenant is wired to: one
// record whose state a test moves, or an outage.
type changeSystemStub struct {
	mu    sync.Mutex
	state string
	err   error
}

func (c *changeSystemStub) set(state string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state, c.err = state, err
}

func (c *changeSystemStub) read(context.Context, model.TenantID, string, string, string) (governance.ChangeRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return governance.ChangeRecord{State: c.state}, c.err
}

// statusGateWorld is a tenant whose approvals of one action are bound to CHG0030001, with
// the bridge a status-reading gate (deploy, orchestration, voice, the one-shot gates)
// asks "is it approved" through.
type statusGateWorld struct {
	h        *harness
	bridge   *approvalBridge
	system   *changeSystemStub
	tenant   model.TenantID
	approver string
}

func newStatusGateWorld(t *testing.T) *statusGateWorld {
	t.Helper()
	h := newHarness(t)
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	system := &changeSystemStub{state: "Scheduled"}
	h.set.gov.UseChangeRecordReader(system.read)
	tenant := model.TenantID(h.tenantA)
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(t.Context(), model.Policy{Name: "push under CHG0030001", Kind: "approval", Enabled: true, Spec: map[string]any{
			"required_approvals": 1, "match": map[string]any{"action": statusGatedAction},
			"change_record": map[string]any{"system": "servicenow", "key": "CHG0030001"},
		}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, approver := h.createApprover(t, "status-gate-approver@bridge.test")
	return &statusGateWorld{h: h, bridge: localDefaultBridge(t, h, h.h), system: system, tenant: tenant, approver: approver}
}

// approved opens the approval of one target and has it approved while the record
// is Scheduled.
func (w *statusGateWorld) approved(t *testing.T, target string) (ref, planHash string) {
	t.Helper()
	planHash = "plan-" + target
	ref, status, _, err := w.bridge.Request(t.Context(), w.tenant, statusGatedAction, statusGatedSubject, target, planHash, "apply", "tester")
	if err != nil || status != "pending" {
		t.Fatalf("request = %q %v, want pending", status, err)
	}
	if code, body := w.h.decide(t, w.approver, ref, "approve"); code != http.StatusOK {
		t.Fatalf("approve with the record Scheduled = %d %s", code, body)
	}
	return ref, planHash
}

func (w *statusGateWorld) stored(t *testing.T, ref string) string {
	t.Helper()
	status, _ := w.h.getJSON(w.h.adminToken, w.h.tenantA, "/v1/m/governance/approvals/"+ref)["status"].(string)
	return status
}

// statusGatePaths are the ways the bridge tells a gate "approved" without a spend. Each asks
// the question of an approved grant and returns the status it was told.
func statusGatePaths() []struct {
	name string
	ask  func(t *testing.T, w *statusGateWorld, ref, planHash, target string) (status string, err error)
} {
	return []struct {
		name string
		ask  func(t *testing.T, w *statusGateWorld, ref, planHash, target string) (status string, err error)
	}{
		{"Status (deploy apply, voice)", func(t *testing.T, w *statusGateWorld, ref, planHash, _ string) (string, error) {
			status, _, err := w.bridge.Status(t.Context(), w.tenant, ref, planHash)
			return status, err
		}},
		{"StatusScoped (orchestration, MCP call)", func(t *testing.T, w *statusGateWorld, ref, planHash, target string) (string, error) {
			status, _, err := w.bridge.StatusScoped(t.Context(), w.tenant, ref, planHash, statusGatedAction, statusGatedSubject, target)
			return status, err
		}},
		{"GateOnce, the bridge that opened the approval (security gate, admin actions, export, inference proxy)", func(t *testing.T, w *statusGateWorld, ref, planHash, target string) (string, error) {
			_, status, _, err := w.bridge.GateOnce(t.Context(), w.tenant, statusGatedAction, statusGatedSubject, target, planHash, "apply", "tester")
			return status, err
		}},
		{"GateOnce, a bridge that never saw the approval (after a restart: durable lookup)", func(t *testing.T, w *statusGateWorld, ref, planHash, target string) (string, error) {
			fresh := localDefaultBridge(t, w.h, w.h.h)
			gotRef, status, _, err := fresh.GateOnce(t.Context(), w.tenant, statusGatedAction, statusGatedSubject, target, planHash, "apply", "tester")
			if err == nil && (status == "approved" && gotRef != ref || status == "break_glass" && !strings.HasPrefix(gotRef, "breakglass:")) {
				t.Fatalf("GateOnce found approval %q, want the approved %q", gotRef, ref)
			}
			return status, err
		}},
	}
}

// A gate that reads an approval's status instead of spending it must not be authorized by
// an approval whose change record was canceled after the approval: each way the bridge
// hands back "approved" re-checks the record first, and the grant is revoked.
func TestStatusReadingGatesAreRefusedOnceTheChangeRecordIsCanceled(t *testing.T) {
	for i, p := range statusGatePaths() {
		t.Run(p.name, func(t *testing.T) {
			w := newStatusGateWorld(t)
			target := "prod-" + string(rune('a'+i))
			ref, planHash := w.approved(t, target)

			// While the record is Scheduled the gate is authorized, as before.
			if status, err := p.ask(t, w, ref, planHash, target); err != nil || status != "approved" {
				t.Fatalf("with the record Scheduled the gate was told %q %v, want approved", status, err)
			}

			w.system.set("Canceled", nil)
			if status, err := p.ask(t, w, ref, planHash, target); err != nil || status != "canceled" {
				t.Fatalf("after the record was canceled the gate was told %q %v, want canceled", status, err)
			}
			if st := w.stored(t, ref); st != "canceled" {
				t.Fatalf("the approval after the canceled record = %q, want canceled", st)
			}
		})
	}
}

// A grant a round trip already spent is still handed back as "approved" by every gate that
// reads a status, and that authorizes new actions for the rest of its window: it is
// re-checked like an unspent one. The spend itself cannot be taken back, so the gate is
// refused and the stored grant is left as it was.
func TestStatusReadingGatesAreRefusedWhenASpentGrantsChangeRecordIsCanceled(t *testing.T) {
	for i, p := range statusGatePaths() {
		t.Run(p.name, func(t *testing.T) {
			w := newStatusGateWorld(t)
			target := "spent-" + string(rune('a'+i))
			ref, planHash := w.approved(t, target)
			if granted, _, err := w.bridge.ConsumeApproval(t.Context(), w.tenant, ref, "round-trip-1", ""); err != nil || !granted {
				t.Fatalf("consume = %v %v, want granted", granted, err)
			}

			// While the record is Scheduled the spent grant keeps authorizing, as before.
			if status, err := p.ask(t, w, ref, planHash, target); err != nil || status != "approved" {
				t.Fatalf("with the record Scheduled the gate was told %q %v, want approved", status, err)
			}

			w.system.set("Canceled", nil)
			if status, err := p.ask(t, w, ref, planHash, target); err == nil || status == "approved" || !strings.Contains(err.Error(), "CHG0030001") {
				t.Fatalf("after the record was canceled the gate was told %q %v, want the record's refusal and not approved", status, err)
			}
			if st := w.stored(t, ref); st != "approved" {
				t.Fatalf("the spent approval after the canceled record = %q, want it left approved", st)
			}
		})
	}
}

// An outage of the change system leaves the approval standing but authorizes nothing
// through a status-reading gate: the gate gets an error, never "approved", and the gate
// is authorized again once the system answers.
func TestStatusReadingGatesDenyWhileTheChangeSystemIsDown(t *testing.T) {
	for i, p := range statusGatePaths() {
		t.Run(p.name, func(t *testing.T) {
			w := newStatusGateWorld(t)
			target := "outage-" + string(rune('a'+i))
			ref, planHash := w.approved(t, target)
			w.system.set("", errors.New("timeout"))

			if status, err := p.ask(t, w, ref, planHash, target); err == nil || status == "approved" {
				t.Fatalf("with the change system down the gate was told %q %v, want an error and not approved", status, err)
			}
			if st := w.stored(t, ref); st != "approved" {
				t.Fatalf("an outage changed the stored approval to %q, want approved", st)
			}
			w.system.set("Scheduled", nil)
			if status, err := p.ask(t, w, ref, planHash, target); err != nil || status != "approved" {
				t.Fatalf("after recovery the gate was told %q %v, want approved", status, err)
			}
		})
	}
}

// The review's failure case, through the real deploy gate of a bridge configured with a
// service token (it reads the approval over the API, not through the local service): a
// deploy.apply approved while CHG0030001 is Scheduled applies, and after the record is
// canceled the same approval reference authorizes nothing.
func TestDeployApplyIsRefusedOnceItsChangeRecordIsCanceled(t *testing.T) {
	h := newHarness(t)
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	system := &changeSystemStub{state: "Scheduled"}
	h.set.gov.UseChangeRecordReader(system.read)
	tenant := model.TenantID(h.tenantA)
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(t.Context(), model.Policy{Name: "apply under CHG0030001", Kind: "approval", Enabled: true, Spec: map[string]any{
			"required_approvals": 1, "match": map[string]any{"action": "deploy.apply"},
			"change_record": map[string]any{"system": "servicenow", "key": "CHG0030001"},
		}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// deploy.apply is CRITICAL: two distinct approvers.
	_, first := h.createApprover(t, "apply-approver-1@bridge.test")
	_, second := h.createApprover(t, "apply-approver-2@bridge.test")
	bridge := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	bridge.UseLocalProposer(h.set.gov.EngineApprovals()) // as boot binds it
	gate := bridge.DeployGate()
	planHash := "p1a1b2c3d4e5f6a7b8c9d0"

	dec, err := gate.Request(t.Context(), deploy.ApprovalRequest{
		Tenant: tenant, Action: "deploy.apply", SubjectKind: "deployment", SubjectRef: "svc/api", PlanHash: planHash, RequestedBy: "user:svc",
	})
	if err != nil || dec.Status != deploy.StatusPending {
		t.Fatalf("request = %+v %v, want pending", dec, err)
	}
	for _, token := range []string{first, second} {
		if code, body := h.decide(t, token, dec.ApprovalRef, "approve"); code != http.StatusOK {
			t.Fatalf("approve with the record Scheduled = %d %s", code, body)
		}
	}
	if got, err := gate.Status(t.Context(), tenant, dec.ApprovalRef, planHash); err != nil || !got.Allowed() {
		t.Fatalf("apply with the record Scheduled = %+v %v, want allowed", got, err)
	}

	system.set("Canceled", nil)
	got, err := gate.Status(t.Context(), tenant, dec.ApprovalRef, planHash)
	if err == nil && got.Allowed() {
		t.Fatalf("apply after the record was canceled = %+v, want it refused", got)
	}
	read := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+dec.ApprovalRef)
	if read["status"] != "canceled" {
		t.Fatalf("approval after the record was canceled = %v, want canceled", read["status"])
	}
}
