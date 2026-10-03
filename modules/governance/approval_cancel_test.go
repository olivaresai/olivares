// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

type engineApprovalCanceler interface {
	Cancel(context.Context, model.TenantID, auth.Principal, string) (governance.Approval, error)
}

// Use the same resolved session principal the engine passes to its approval port.
func cancellationSession(t *testing.T, h *harness, tenant model.TenantID, launcherToken, name string) auth.Principal {
	t.Helper()
	ctx := context.Background()
	launcher, err := h.authr.Authenticate(ctx, launcherToken)
	if err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewSessionCredentials(h.authr, func(context.Context, auth.SessionScope) error { return nil })
	scope := auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder-" + name, SessionRef: "session-" + name, RunRef: "run-" + name}
	token, err := credentials.Mint(ctx, launcher, scope)
	if err != nil {
		t.Fatal(err)
	}
	principal, _, err := credentials.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func TestEngineApprovalCancelOwnRequestAndAudit(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cancel-session")
	principal := cancellationSession(t, h, tenant, admin, "own")
	service := h.gov.EngineApprovals()
	canceler, ok := any(service).(engineApprovalCanceler)
	if !ok {
		t.Fatal("EngineApprovals has no in-process Cancel port")
	}
	pending, err := service.Request(context.Background(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "run-own", SessionRef: principal.SessionIdentity})
	if err != nil {
		t.Fatal(err)
	}
	// PEP detaches the canceled turn and supplies a bounded cancellation context.
	turn, stop := context.WithCancel(context.Background())
	stop()
	ctx, deadline := context.WithTimeout(context.WithoutCancel(turn), time.Second)
	defer deadline()
	canceled, err := canceler.Cancel(ctx, tenant, principal, pending.ID)
	if err != nil || canceled.Status != "canceled" {
		t.Fatalf("own cancel = %+v, %v", canceled, err)
	}
	stored, err := service.Read(context.Background(), tenant, pending.ID)
	if err != nil || stored.Status != "canceled" {
		t.Fatalf("stored cancel = %+v, %v", stored, err)
	}
	verdict, err := service.Wait(context.Background(), tenant, pending.ID)
	if err != nil || verdict.Status != "canceled" {
		t.Fatalf("wait after cancel = %+v, %v", verdict, err)
	}
	var events []model.AuditEvent
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			if event.Action == "governance.approval.cancel" && event.TargetID.String() == pending.ID {
				events = append(events, event)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Actor != "session:"+principal.SessionIdentity || events[0].ActorKind != model.ActorAgent {
		t.Fatalf("session cancellation audit = %+v", events)
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 || resolved[0].ApprovalID != pending.ID || resolved[0].Outcome != "canceled" {
		t.Fatalf("cancel resolution events = %+v", resolved)
	}
}

func TestEngineApprovalCancelRefusesOtherSessionAndTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cancel-owner")
	otherTenant := h.createOrg(admin, "cancel-other")
	principal := cancellationSession(t, h, tenant, admin, "requester")
	other := cancellationSession(t, h, tenant, admin, "other")
	// Both sessions have the same administrator as launcher; that role must not
	// let one session cancel the other session's request.
	service := h.gov.EngineApprovals()
	canceler, ok := any(service).(engineApprovalCanceler)
	if !ok {
		t.Fatal("EngineApprovals has no in-process Cancel port")
	}
	pending, err := service.Request(context.Background(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "run-requester", SessionRef: principal.SessionIdentity})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		name      string
		tenant    model.TenantID
		principal auth.Principal
	}{
		{"other session", tenant, other}, {"other tenant", otherTenant, principal},
	} {
		t.Run(input.name, func(t *testing.T) {
			if _, err := canceler.Cancel(context.Background(), input.tenant, input.principal, pending.ID); err == nil {
				t.Fatal("unrelated session cancellation was allowed")
			}
		})
	}
	stored, err := service.Read(context.Background(), tenant, pending.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("refused cancel changed request: %+v, %v", stored, err)
	}
	if len(resolvedPayloads(t, h)) != 0 || contains(h.auditActions(tenant), "governance.approval.cancel") {
		t.Fatal("refused cancellation emitted a resolution or successful cancel audit")
	}
}

func TestEngineApprovalCancelDoesNotOverwriteDecidedRequest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cancel-terminal")
	principal := cancellationSession(t, h, tenant, admin, "terminal")
	service := h.gov.EngineApprovals()
	canceler, ok := any(service).(engineApprovalCanceler)
	if !ok {
		t.Fatal("EngineApprovals has no in-process Cancel port")
	}
	pending, err := service.Request(context.Background(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "run-terminal", SessionRef: principal.SessionIdentity})
	if err != nil {
		t.Fatal(err)
	}
	decided := h.decide(admin, tenant, pending.ID, "approve")
	if decided.code != 200 || decided.body["status"] != "approved" {
		t.Fatalf("human approval = %d %s", decided.code, decided.raw)
	}
	before, err := service.Read(context.Background(), tenant, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := canceler.Cancel(context.Background(), tenant, principal, pending.ID)
	if err == nil || !strings.Contains(err.Error(), "approved") || unchanged.Status != "approved" {
		t.Fatalf("terminal cancel must report no-op: %+v, %v", unchanged, err)
	}
	after, err := service.Read(context.Background(), tenant, pending.ID)
	if err != nil || after.Status != "approved" || after.DecidedAt != before.DecidedAt || after.ApproveCount != before.ApproveCount {
		t.Fatalf("terminal cancellation overwrote decision: before=%+v after=%+v error=%v", before, after, err)
	}
	if len(resolvedPayloads(t, h)) != 1 || contains(h.auditActions(tenant), "governance.approval.cancel") {
		t.Fatal("terminal cancellation emitted another resolution or successful cancel audit")
	}
}

func TestApprovalCancelKeepsRESTRequesterAndAdminAuthority(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cancel-rest")
	_, requester := h.roleUser(admin, tenant, "requester@example.test", "editor")
	_, reviewer := h.roleUser(admin, tenant, "reviewer@example.test", "admin")
	_, unrelated := h.roleUser(admin, tenant, "unrelated@example.test", "editor")
	created := h.createApproval(requester, tenant, map[string]any{"action": "deploy"})
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := created.body["id"].(string)
	path := govPath + "/approvals/" + id + "/cancel"
	if got := h.do("POST", path, unrelated, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("unrelated requester cancel = %d %s", got.code, got.raw)
	}
	if got := h.do("POST", path, reviewer, nil, tenantHdr(tenant)); got.code != http.StatusOK || got.body["status"] != "canceled" {
		t.Fatalf("admin cancel = %d %s", got.code, got.raw)
	}
	if got := h.do("POST", path, requester, nil, tenantHdr(tenant)); got.code != http.StatusConflict {
		t.Fatalf("terminal requester cancel = %d %s", got.code, got.raw)
	}
	if len(resolvedPayloads(t, h)) != 1 {
		t.Fatal("refused or terminal REST cancellation published a resolution")
	}
}
