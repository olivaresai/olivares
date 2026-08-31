// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// capDoer records the writes the governed actuator's transport issued — so a test can
// assert exactly which upstream call the backstop made, and (crucially) that NO call is
// made when the PEP/backstop declines. It is mutex-guarded so the async bus-delivery test
// can poll it race-free; synchronous tests read the snapshot too.
type capDoer struct {
	mu     sync.Mutex
	status int // 0 => 200
	reqs   []capReq
}
type capReq struct{ method, path, body string }

func (d *capDoer) Do(req *http.Request) (*http.Response, error) {
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	d.mu.Lock()
	d.reqs = append(d.reqs, capReq{req.Method, req.URL.Path, string(b)})
	st := d.status
	d.mu.Unlock()
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}

func (d *capDoer) snapshot() []capReq {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]capReq(nil), d.reqs...)
}

// cmdStubAdminGate is a configurable AdminActionGate (approves with a set of approvers).
type cmdStubAdminGate struct {
	status    claudeapi.AdminActionStatus
	approvers []string
}

func (g cmdStubAdminGate) Authorize(_ context.Context, req claudeapi.AdminActionRequest) (claudeapi.AdminActionDecision, error) {
	// N approvers models N distinct PEOPLE, each acting through one credential, so the
	// stub states both identities (the quorum counts people).
	return claudeapi.AdminActionDecision{
		ApprovalRef: "appr", Status: g.status, PlanHash: req.PlanHash,
		Approvers: g.approvers, ApproverPersons: g.approvers,
	}, nil
}

// stubCapResolver canned-resolves a budget id to a (dimension, key) cap target.
type stubCapResolver struct {
	dimension, key string
	ok             bool
	err            error
}

func (r stubCapResolver) BudgetCapTarget(context.Context, model.TenantID, string) (string, string, bool, error) {
	return r.dimension, r.key, r.ok, r.err
}

func backstopWith(tid model.TenantID, gate claudeapi.AdminActionGate, allow []claudeapi.AdminAllowRule, res budgetCapResolver, escalate bool) (*finopsBackstop, *capDoer) {
	doer := &capDoer{}
	act := claudeapi.NewActuator(claudeapi.ActuatorConfig{
		AdminKey: "sk-ant-admin-test", Doer: doer,
		Allowlist: claudeapi.NewAdminActionAllowlist(allow), Gate: gate,
	})
	bs := &finopsBackstop{
		actuators:       map[model.TenantID]*claudeapi.Actuator{tid: act},
		targets:         res,
		escalateArchive: escalate,
		log:             discardLog(),
	}
	return bs, doer
}

func capEvent(tenant string, severity sdkmodel.Severity, kind, budgetID string) event.Event {
	return event.FromObservation(tenant, "src:test", sdkmodel.FindingReport{
		Kind: kind, Severity: severity, SubjectKind: "budget", SubjectRef: budgetID,
		Title: "cap", OccurredAt: time.Unix(1, 0).UTC(),
	})
}

// TestFinopsBackstopDefaultOff proves the backstop is INERT unless explicitly enabled —
// even with tenants + admin keys provisioned, Enabled=false yields a nil (no-op) backstop.
func TestFinopsBackstopDefaultOff(t *testing.T) {
	cfg := claudeAdminActuatorConfig{
		Tenants: []claudeAdminActuatorTenant{{Tenant: mustTenant(t).String(), AdminKey: "sk-ant-admin-test"}},
		// Backstop.Enabled defaults false.
	}
	if bs := newFinopsBackstop(cfg, stubCapResolver{}, &approvalBridge{log: discardLog()}, discardLog()); bs != nil {
		t.Fatal("default-off backstop must be nil (inert)")
	}
}

// TestFinopsBackstopFailClosedWithoutProvisioning proves the backstop stays inert when
// enabled but unprovisioned: no bridge, or no tenant admin key.
func TestFinopsBackstopFailClosedWithoutProvisioning(t *testing.T) {
	enabled := finopsBackstopConfig{Enabled: true}
	// Enabled but no bridge → nil.
	if bs := newFinopsBackstop(claudeAdminActuatorConfig{Backstop: enabled, Tenants: []claudeAdminActuatorTenant{{Tenant: mustTenant(t).String(), AdminKey: "k"}}}, stubCapResolver{}, nil, discardLog()); bs != nil {
		t.Fatal("enabled backstop with no bridge must be nil (fail-closed)")
	}
	// Enabled, bridge present, but tenant has no admin_key → no usable actuator → nil.
	cfg := claudeAdminActuatorConfig{Backstop: enabled, Tenants: []claudeAdminActuatorTenant{{Tenant: mustTenant(t).String(), AdminKey: ""}}}
	if bs := newFinopsBackstop(cfg, stubCapResolver{}, &approvalBridge{log: discardLog()}, discardLog()); bs != nil {
		t.Fatal("enabled backstop with no admin_key must be nil (fail-closed)")
	}
}

// TestFinopsBackstopEnabledConstructsActuator proves a fully-provisioned, enabled backstop
// is constructed (non-nil) with the tenant's governed actuator.
func TestFinopsBackstopEnabledConstructsActuator(t *testing.T) {
	tid := mustTenant(t)
	cfg := claudeAdminActuatorConfig{
		Backstop: finopsBackstopConfig{Enabled: true},
		Tenants:  []claudeAdminActuatorTenant{{Tenant: tid.String(), AdminKey: "sk-ant-admin-test"}},
	}
	bs := newFinopsBackstop(cfg, stubCapResolver{}, &approvalBridge{log: discardLog()}, discardLog())
	if bs == nil || bs.actuators[tid] == nil {
		t.Fatal("enabled+provisioned backstop must construct the tenant actuator")
	}
}

// TestFinopsBackstopDeactivatesKeyOnBlockCap proves the happy path: a BLOCK cap on an
// api_key-scoped budget drives a governed DeactivateKey on the offending key (single HITL
// approval) — exactly one POST with status:inactive.
func TestFinopsBackstopDeactivatesKeyOnBlockCap(t *testing.T) {
	tid := mustTenant(t)
	bs, doer := backstopWith(tid,
		cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"alice"}},
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}},
		stubCapResolver{dimension: "api_key", key: "apikey_off", ok: true}, false)

	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "budget_1"))

	if len(doer.reqs) != 1 {
		t.Fatalf("want exactly 1 upstream write, got %d (%+v)", len(doer.reqs), doer.reqs)
	}
	w := doer.reqs[0]
	if w.method != http.MethodPost || w.path != "/v1/organizations/api_keys/apikey_off" {
		t.Errorf("write = %s %s, want POST /v1/organizations/api_keys/apikey_off", w.method, w.path)
	}
	if !strings.Contains(w.body, `"status":"inactive"`) {
		t.Errorf("body = %s, want status:inactive", w.body)
	}
}

// TestFinopsBackstopIgnoresSoftAndNonCap proves the backstop acts ONLY on a definitive
// BLOCK cap (Critical): a throttle cap (High) and an ordinary budget alert are ignored.
func TestFinopsBackstopIgnoresSoftAndNonCap(t *testing.T) {
	tid := mustTenant(t)
	mk := func() (*finopsBackstop, *capDoer) {
		return backstopWith(tid,
			cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"alice"}},
			[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"*"}}},
			stubCapResolver{dimension: "api_key", key: "apikey_off", ok: true}, false)
	}
	// throttle cap = High severity → ignored.
	bs, doer := mk()
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityHigh, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("throttle (High) cap must not actuate, got %d writes", len(doer.reqs))
	}
	// ordinary alert (not a cap kind) → ignored even at Critical.
	bs, doer = mk()
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, "finops_budget", "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("non-cap finding must not actuate, got %d writes", len(doer.reqs))
	}
}

// TestFinopsBackstopUnknownTenantNoOp proves a cap for a tenant with no provisioned
// actuator is a fail-closed no-op.
func TestFinopsBackstopUnknownTenantNoOp(t *testing.T) {
	tid := mustTenant(t)
	bs, doer := backstopWith(tid,
		cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"alice"}},
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"*"}}},
		stubCapResolver{dimension: "api_key", key: "apikey_off", ok: true}, false)
	other := "22222222-2222-2222-2222-222222222222"
	_ = bs.onFinding(context.Background(), capEvent(other, sdkmodel.SeverityCritical, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("cap for an unprovisioned tenant must not actuate, got %d writes", len(doer.reqs))
	}
}

// TestFinopsBackstopWorkspaceArchiveEscalation proves the workspace-archive escalation is
// OFF by default (a workspace cap is a no-op) and, when enabled, archives the workspace —
// but ONLY with the dual-control quorum the irreversible action requires.
func TestFinopsBackstopWorkspaceArchiveEscalation(t *testing.T) {
	tid := mustTenant(t)
	allow := []claudeapi.AdminAllowRule{{Action: claudeapi.ActionArchiveWorkspace, Subjects: []string{"wrkspc_z"}}}
	res := stubCapResolver{dimension: "workspace", key: "wrkspc_z", ok: true}

	// Escalation OFF → no actuation on a workspace cap.
	bs, doer := backstopWith(tid, cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"a", "b"}}, allow, res, false)
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("archive escalation OFF must not actuate, got %d writes", len(doer.reqs))
	}

	// Escalation ON but only ONE distinct approver → dual-control denies (no write).
	bs, doer = backstopWith(tid, cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"solo"}}, allow, res, true)
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("single-approver archive must be denied by dual-control, got %d writes", len(doer.reqs))
	}

	// Escalation ON with TWO distinct approvers → workspace archived.
	bs, doer = backstopWith(tid, cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"a", "b"}}, allow, res, true)
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 1 {
		t.Fatalf("dual-control archive must actuate exactly once, got %d", len(doer.reqs))
	}
	if w := doer.reqs[0]; w.method != http.MethodPost || w.path != "/v1/organizations/workspaces/wrkspc_z/archive" {
		t.Errorf("write = %s %s, want POST .../workspaces/wrkspc_z/archive", w.method, w.path)
	}
}

// TestFinopsBackstopNoSurgicalTargetNoOp proves a cap whose dimension has no single
// surgical upstream key (identity/global) is an honest no-op — the backstop never guesses.
func TestFinopsBackstopNoSurgicalTargetNoOp(t *testing.T) {
	tid := mustTenant(t)
	bs, doer := backstopWith(tid,
		cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"a", "b"}},
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"*"}}},
		stubCapResolver{dimension: "identity", key: "id_x", ok: true}, true)
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("identity-scoped cap has no surgical target; must not actuate, got %d writes", len(doer.reqs))
	}
}

// TestFinopsBackstopResolverErrorFailsClosed proves a cap-target lookup error declines to
// actuate (fail-closed) rather than guessing.
func TestFinopsBackstopResolverErrorFailsClosed(t *testing.T) {
	tid := mustTenant(t)
	bs, doer := backstopWith(tid,
		cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"a"}},
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"*"}}},
		stubCapResolver{err: context.DeadlineExceeded}, false)
	_ = bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "b"))
	if len(doer.reqs) != 0 {
		t.Fatalf("resolver error must fail closed (no actuation), got %d writes", len(doer.reqs))
	}
}

// TestFinopsBackstopSubscribeDeliversCap proves the bus wiring boot.go relies on actually
// works end-to-end: subscribe(bus) registers for finding.reported, and a published BLOCK
// cap is delivered to onFinding and drives the governed key-deactivate.
func TestFinopsBackstopSubscribeDeliversCap(t *testing.T) {
	tid := mustTenant(t)
	bs, doer := backstopWith(tid,
		cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"alice"}},
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}},
		stubCapResolver{dimension: "api_key", key: "apikey_off", ok: true}, false)

	bus := eventbus.NewInProc(eventbus.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	if err := bs.subscribe(bus); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "budget_1")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Bus delivery is async (a drain goroutine); poll the mutex-guarded snapshot.
	var reqs []capReq
	for i := 0; i < 400; i++ {
		if reqs = doer.snapshot(); len(reqs) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(reqs) != 1 || reqs[0].path != "/v1/organizations/api_keys/apikey_off" {
		t.Fatalf("subscribe→publish must drive exactly the key-deactivate write, got %+v", reqs)
	}
}

// TestFinopsBackstopReportsTransportFailure proves a transport failure (5xx) on the cut is
// attempted exactly once and routed to report()'s TRANSPORT branch (not the governed-deny
// branch) — and never breaks the bus handler (onFinding returns nil).
func TestFinopsBackstopReportsTransportFailure(t *testing.T) {
	tid := mustTenant(t)
	var buf bytes.Buffer
	doer := &capDoer{status: http.StatusBadGateway}
	act := claudeapi.NewActuator(claudeapi.ActuatorConfig{
		AdminKey: "sk-ant-admin-test", Doer: doer,
		Allowlist: claudeapi.NewAdminActionAllowlist([]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}}),
		Gate:      cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"alice"}},
	})
	bs := &finopsBackstop{
		actuators: map[model.TenantID]*claudeapi.Actuator{tid: act},
		targets:   stubCapResolver{dimension: "api_key", key: "apikey_off", ok: true},
		log:       slog.New(slog.NewTextHandler(&buf, nil)),
	}
	if err := bs.onFinding(context.Background(), capEvent(tid.String(), sdkmodel.SeverityCritical, finopsBudgetCapKind, "b")); err != nil {
		t.Fatalf("onFinding must never break bus delivery, got %v", err)
	}
	if len(doer.snapshot()) != 1 {
		t.Fatalf("the cut must be attempted exactly once, got %d", len(doer.snapshot()))
	}
	if !strings.Contains(buf.String(), "transport") {
		t.Fatalf("a 5xx must be reported on the transport branch, log=%q", buf.String())
	}
}

func adminParamHash(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func encodedAdminSubject(action claudeapi.AdminAction, subjectKind, subjectRef, paramLabel string) string {
	plan := claudeapi.AdminPlanHash(action, subjectKind, subjectRef, adminParamHash(paramLabel))
	return encodeSubjectRef(subjectRef, plan)
}

func bridgeForResolvedApproval(t *testing.T, tid model.TenantID, approvalID, action, subjectKind, encodedSubject, status string) *approvalBridge {
	t.Helper()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	decidedAt := model.NewTimestamp(now.Add(-time.Minute)).String()
	br := &approvalBridge{
		creds: map[model.TenantID]serviceCred{tid: {
			tenant: tid, tenantStr: tid.String(), token: "svc-token", expiresIn: 3600,
		}},
		log:   discardLog(),
		clock: func() time.Time { return now },
		memo:  map[string]string{},
	}
	br.useHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/m/governance/approvals/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": approvalID, "status": status, "action": action,
				"subject_kind": subjectKind, "subject_ref": encodedSubject, "decided_at": decidedAt,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/m/governance/approvals":
			items := []map[string]any{}
			if r.URL.Query().Get("status") == status && r.URL.Query().Get("action") == action {
				items = append(items, map[string]any{
					"id": approvalID, "status": status, "subject_kind": subjectKind,
					"subject_ref": encodedSubject, "decided_at": decidedAt,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false})
		default:
			http.NotFound(w, r)
		}
	}))
	return br
}

func reDriverBackstop(tid model.TenantID, br *approvalBridge, allow []claudeapi.AdminAllowRule, escalate bool, status int) (*finopsBackstop, *capDoer) {
	doer := &capDoer{status: status}
	act := claudeapi.NewActuator(claudeapi.ActuatorConfig{
		AdminKey: "sk-ant-admin-test", Doer: doer,
		Allowlist: claudeapi.NewAdminActionAllowlist(allow), Gate: br.adminGate(tid),
	})
	return &finopsBackstop{
		actuators:       map[model.TenantID]*claudeapi.Actuator{tid: act},
		bridge:          br,
		escalateArchive: escalate,
		log:             discardLog(),
	}, doer
}

func resolvedAdminEvent(tid model.TenantID, approvalID, action, outcome string) event.Event {
	return event.ApprovalResolved(tid.String(), "module:governance", time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC), event.ApprovalResolution{
		ApprovalID: approvalID,
		Action:     action,
		Outcome:    outcome,
	})
}

func TestFinopsBackstopApprovalResolvedRedrivesKeyDeactivate(t *testing.T) {
	tid := mustTenant(t)
	const approvalID = "appr_key_1"
	encoded := encodedAdminSubject(claudeapi.ActionDeactivateKey, "api_key", "apikey_off", "status=inactive")
	br := bridgeForResolvedApproval(t, tid, approvalID, adminCapKeyDeactivate, "claude_admin.api_key", encoded, nbApproved)
	bs, doer := reDriverBackstop(tid, br,
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}},
		false, 0)

	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(tid, approvalID, adminCapKeyDeactivate, "approved")); err != nil {
		t.Fatalf("onApprovalResolved returned error: %v", err)
	}
	reqs := doer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("want exactly 1 upstream write, got %d (%+v)", len(reqs), reqs)
	}
	w := reqs[0]
	if w.method != http.MethodPost || w.path != "/v1/organizations/api_keys/apikey_off" {
		t.Errorf("write = %s %s, want POST /v1/organizations/api_keys/apikey_off", w.method, w.path)
	}
	if strings.TrimSpace(w.body) != `{"status":"inactive"}` {
		t.Errorf("body = %s, want status inactive JSON", w.body)
	}
}

func TestFinopsBackstopApprovalResolvedNoOps(t *testing.T) {
	tid := mustTenant(t)
	const approvalID = "appr_noop"
	encoded := encodedAdminSubject(claudeapi.ActionDeactivateKey, "api_key", "apikey_off", "status=inactive")
	br := bridgeForResolvedApproval(t, tid, approvalID, adminCapKeyDeactivate, "claude_admin.api_key", encoded, nbApproved)
	allow := []claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}}

	bs, doer := reDriverBackstop(tid, br, allow, false, 0)
	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(tid, approvalID, adminCapKeyDeactivate, "rejected")); err != nil {
		t.Fatalf("rejected outcome returned error: %v", err)
	}
	if got := len(doer.snapshot()); got != 0 {
		t.Fatalf("rejected outcome must not actuate, got %d writes", got)
	}

	bs, doer = reDriverBackstop(tid, br, allow, false, 0)
	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(tid, approvalID, "unknown.action", "approved")); err != nil {
		t.Fatalf("unknown action returned error: %v", err)
	}
	if got := len(doer.snapshot()); got != 0 {
		t.Fatalf("unknown action must not actuate, got %d writes", got)
	}

	bs, doer = reDriverBackstop(tid, br, allow, false, 0)
	other, err := model.ParseTenantID("22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(other, approvalID, adminCapKeyDeactivate, "approved")); err != nil {
		t.Fatalf("unknown tenant returned error: %v", err)
	}
	if got := len(doer.snapshot()); got != 0 {
		t.Fatalf("tenant without actuator must not actuate, got %d writes", got)
	}
}

func TestFinopsBackstopApprovalResolvedSubjectKindMismatchNoOp(t *testing.T) {
	tid := mustTenant(t)
	const approvalID = "appr_mismatch"
	encoded := encodedAdminSubject(claudeapi.ActionDeactivateKey, "api_key", "apikey_off", "status=inactive")
	br := bridgeForResolvedApproval(t, tid, approvalID, adminCapKeyDeactivate, "claude_admin.workspace", encoded, nbApproved)
	bs, doer := reDriverBackstop(tid, br,
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}},
		false, 0)

	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(tid, approvalID, adminCapKeyDeactivate, "approved")); err != nil {
		t.Fatalf("subject mismatch returned error: %v", err)
	}
	if got := len(doer.snapshot()); got != 0 {
		t.Fatalf("subject kind mismatch must not actuate, got %d writes", got)
	}
}

func TestFinopsBackstopApprovalResolvedWorkspaceArchiveDisabledNoOp(t *testing.T) {
	tid := mustTenant(t)
	const approvalID = "appr_workspace"
	encoded := encodedAdminSubject(claudeapi.ActionArchiveWorkspace, "workspace", "wrkspc_z", "archive_workspace")
	br := bridgeForResolvedApproval(t, tid, approvalID, adminCapWorkspaceArchive, "claude_admin.workspace", encoded, nbApproved)
	bs, doer := reDriverBackstop(tid, br,
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionArchiveWorkspace, Subjects: []string{"wrkspc_z"}}},
		false, 0)

	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(tid, approvalID, adminCapWorkspaceArchive, "approved")); err != nil {
		t.Fatalf("archive disabled returned error: %v", err)
	}
	if got := len(doer.snapshot()); got != 0 {
		t.Fatalf("workspace archive with escalation disabled must not actuate, got %d writes", got)
	}
}

func TestFinopsBackstopApprovalResolvedActuationErrorReturnsNil(t *testing.T) {
	tid := mustTenant(t)
	const approvalID = "appr_key_error"
	encoded := encodedAdminSubject(claudeapi.ActionDeactivateKey, "api_key", "apikey_off", "status=inactive")
	br := bridgeForResolvedApproval(t, tid, approvalID, adminCapKeyDeactivate, "claude_admin.api_key", encoded, nbApproved)
	bs, doer := reDriverBackstop(tid, br,
		[]claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"apikey_off"}}},
		false, http.StatusBadGateway)

	if err := bs.onApprovalResolved(context.Background(), resolvedAdminEvent(tid, approvalID, adminCapKeyDeactivate, "approved")); err != nil {
		t.Fatalf("actuation error must not break bus delivery, got %v", err)
	}
	if got := len(doer.snapshot()); got != 1 {
		t.Fatalf("actuation should be attempted exactly once despite upstream error, got %d writes", got)
	}
}
