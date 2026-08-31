// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// --- test doubles: the governed gate and the write-capable actuator ----------

// fakeGate is an in-test LifecycleGate returning a scripted decision; it records the
// last request so tests can assert the action/plan binding/break-glass selection.
type fakeGate struct {
	status  string
	err     error
	lastReq governance.LifecycleGateRequest
}

func (g *fakeGate) Authorize(_ context.Context, _ model.TenantID, req governance.LifecycleGateRequest) (governance.LifecycleGateDecision, error) {
	g.lastReq = req
	if g.err != nil {
		return governance.LifecycleGateDecision{}, g.err
	}
	st := g.status
	if st == "" {
		st = governance.GateStatusApproved
	}
	return governance.LifecycleGateDecision{Status: st, ApprovalRef: "appr-1", PlanHash: req.PlanHash}, nil
}

// fakeActuator implements identitysource.LifecycleActuator with scripted behavior.
type fakeActuator struct {
	caps       []identitysource.ActuatorCapability
	secret     string
	newRef     string
	rotateErr  error
	disabled   bool
	restored   bool
	finalized  bool
	lastRotate identitysource.ActuationRequest
}

func (a *fakeActuator) Capabilities() []identitysource.ActuatorCapability { return a.caps }
func (a *fakeActuator) Disable(_ context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	a.disabled = true
	return identitysource.ActuationReceipt{Op: identitysource.OpDisable, Ref: req.Ref, Detail: "disabled at source"}, nil
}
func (a *fakeActuator) Restore(_ context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	a.restored = true
	return identitysource.ActuationReceipt{Op: identitysource.OpRestore, Ref: req.Ref, Detail: "re-enabled at source"}, nil
}
func (a *fakeActuator) Finalize(_ context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	a.finalized = true
	return identitysource.ActuationReceipt{Op: identitysource.OpFinalize, Ref: req.Ref, Detail: "definitively revoked"}, nil
}
func (a *fakeActuator) Rotate(_ context.Context, req identitysource.ActuationRequest) (identitysource.RotatedCredential, error) {
	a.lastRotate = req
	if a.rotateErr != nil {
		return identitysource.RotatedCredential{}, a.rotateErr
	}
	return identitysource.RotatedCredential{
		Secret: a.secret,
		Receipt: identitysource.RotationReceipt{
			ActuationReceipt: identitysource.ActuationReceipt{Op: identitysource.OpRotate, Ref: req.Ref, Detail: "rotated"},
			NewCredentialRef: a.newRef,
		},
	}, nil
}
func (a *fakeActuator) Retire(_ context.Context, _ identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return identitysource.ActuationReceipt{Op: identitysource.OpRetire}, nil
}

// rotateCaps is a capability set declaring rotate/disable/restore/finalize for a kind.
func rotateCaps(kind string) []identitysource.ActuatorCapability {
	return []identitysource.ActuatorCapability{
		{Op: identitysource.OpRotate, TargetKind: kind, RequiresTargetRef: true, Detail: "mints a new secret-id"},
		{Op: identitysource.OpDisable, TargetKind: kind, Detail: "disables the entity"},
		{Op: identitysource.OpRestore, TargetKind: kind, Detail: "re-enables the entity"},
		{Op: identitysource.OpFinalize, TargetKind: kind, Detail: "keeps disabled"},
	}
}

// --- helpers -----------------------------------------------------------------

// seedIdentity inserts a roster identity directly into the store (no connector).
func (h *harness) seedIdentity(tenant model.TenantID, externalID, kind, provider, principalType string, disabled bool) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: externalID, Kind: kind, ExternalID: externalID, Provider: provider,
			Metadata: map[string]any{"principal_type": principalType, "disabled": disabled},
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed identity %s: %v", externalID, err)
	}
}

// nhiAdmin sets up a tenant + an admin-tier session token for the NHI endpoints.
func (h *harness) nhiTenant() (model.TenantID, string) {
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, tok := h.roleUser(admin, tenant, "nhiadmin@x.io", "admin")
	return tenant, tok
}

// hasFinding reports whether the host captured a finding of the given kind.
func hasFinding(h *harness, kind string) bool {
	return contains(findingKinds(h.host.findings()), kind)
}

// --- ownership / sponsorship -------------------------------------------------

func TestNHIOwnershipRequiresHumanIdentity(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	h.seedIdentity(tenant, "vault:approle:ci", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)
	h.seedIdentity(tenant, "human:alice", "user", "okta", string(identitysource.PrincipalHuman), false)

	// Owner/sponsor pointing at an NHI is rejected (must be accountable people).
	if r := h.do("PUT", "/v1/m/governance/nhi/vault:approle:ci/ownership", tok,
		map[string]any{"owner_ref": "vault:approle:ci", "sponsor_ref": "human:alice"}, hdr); r.code != http.StatusBadRequest {
		t.Fatalf("owner=NHI should be 400, got %d %s", r.code, r.raw)
	}
	// A missing identity is rejected.
	if r := h.do("PUT", "/v1/m/governance/nhi/vault:approle:ci/ownership", tok,
		map[string]any{"sponsor_ref": "human:ghost"}, hdr); r.code != http.StatusBadRequest {
		t.Fatalf("missing sponsor should be 400, got %d %s", r.code, r.raw)
	}
	// A human owner+sponsor is accepted.
	if r := h.do("PUT", "/v1/m/governance/nhi/vault:approle:ci/ownership", tok,
		map[string]any{"owner_ref": "human:alice", "sponsor_ref": "human:alice"}, hdr); r.code != http.StatusNoContent {
		t.Fatalf("human owner should be 204, got %d %s", r.code, r.raw)
	}
	// The lifecycle row now exists with the owner/sponsor.
	r := h.do("GET", "/v1/m/governance/nhi/vault:approle:ci", tok, nil, hdr)
	if r.code != http.StatusOK || r.body["owner_ref"] != "human:alice" || r.body["sponsor_ref"] != "human:alice" {
		t.Fatalf("get nhi = %d %s", r.code, r.raw)
	}
}

// --- staleness sweep: alert → block, CRITICAL immediate block ----------------

func TestNHIStalenessAlertThenBlockEscalation(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:high", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)

	// Seed a HIGH credential rotated 100 days ago (default HIGH window is 90 days).
	rotated := baseTime.Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if r := h.do("PUT", "/v1/m/governance/nhi/vault:approle:high/policy", tok,
		map[string]any{"criticality": "high", "rotated_at": rotated}, hdr); r.code != http.StatusNoContent {
		t.Fatalf("set policy = %d %s", r.code, r.raw)
	}

	// First sweep → stale + alert (not blocked: HIGH gets the 30-day grace).
	if r := h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("sweep = %d %s", r.code, r.raw)
	}
	row := h.do("GET", "/v1/m/governance/nhi/vault:approle:high", tok, nil, hdr)
	if row.body["staleness_status"] != "stale" || row.body["enforcement"] != "alert" {
		t.Fatalf("expected stale+alert, got %s", row.raw)
	}
	if !hasFinding(h, "nhi_credential_stale") {
		t.Fatalf("expected a nhi_credential_stale finding")
	}

	// Advance past the 30-day grace and sweep again → escalates to block.
	h.clk.advance(31 * 24 * time.Hour)
	if r := h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("sweep2 = %d %s", r.code, r.raw)
	}
	row = h.do("GET", "/v1/m/governance/nhi/vault:approle:high", tok, nil, hdr)
	if row.body["enforcement"] != "blocked" {
		t.Fatalf("expected blocked after 30d, got %s", row.raw)
	}
	if !hasFinding(h, "nhi_credential_blocked") {
		t.Fatalf("expected a nhi_credential_blocked finding")
	}
}

func TestNHICriticalCredentialBlocksImmediately(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:crit", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)

	rotated := baseTime.Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339) // > 30d critical window
	if r := h.do("PUT", "/v1/m/governance/nhi/vault:approle:crit/policy", tok,
		map[string]any{"criticality": "critical", "rotated_at": rotated}, hdr); r.code != http.StatusNoContent {
		t.Fatalf("set policy = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("sweep = %d %s", r.code, r.raw)
	}
	row := h.do("GET", "/v1/m/governance/nhi/vault:approle:crit", tok, nil, hdr)
	// a CRITICAL credential blocks directly on staleness (no grace).
	if row.body["enforcement"] != "blocked" {
		t.Fatalf("CRITICAL stale should block immediately, got %s", row.raw)
	}
}

// --- governed rotation: gate + actuator + honest degrade ---------------------

func TestNHIRotationGovernedAndReturnsSecretOnce(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:ci", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)

	gate := &fakeGate{status: governance.GateStatusApproved}
	act := &fakeActuator{caps: rotateCaps("vault_entity"), secret: "s.NEWSECRET", newRef: "accessor-9"}
	h.gov.UseLifecycleGate(gate)
	h.gov.UseLifecycleActuators([]governance.LifecycleActuatorBinding{
		{Source: "vault", TenantRef: tenant.String(), Actuator: act},
	})

	r := h.do("POST", "/v1/m/governance/nhi/vault:approle:ci/rotate", tok,
		map[string]any{"target_ref": "approle:ci"}, hdr)
	if r.code != http.StatusOK || r.body["status"] != "done" {
		t.Fatalf("rotate = %d %s", r.code, r.raw)
	}
	if r.body["new_secret"] != "s.NEWSECRET" || r.body["new_credential_ref"] != "accessor-9" {
		t.Fatalf("expected the minted secret returned once, got %s", r.raw)
	}
	// The gate saw the CRITICAL rotation action, plan-bound, break-glass permitted.
	if gate.lastReq.Action != "nhi.rotate" || gate.lastReq.PlanHash == "" || !gate.lastReq.AllowBreakGlass {
		t.Fatalf("gate request wrong: %+v", gate.lastReq)
	}
	if act.lastRotate.TargetRef != "approle:ci" {
		t.Fatalf("actuator got wrong target: %+v", act.lastRotate)
	}
	// rotated_at is now fresh; the secret is NOT persisted anywhere readable.
	row := h.do("GET", "/v1/m/governance/nhi/vault:approle:ci", tok, nil, hdr)
	if row.body["rotated_at"] == nil || row.body["staleness_status"] != "ok" {
		t.Fatalf("rotation should refresh the row, got %s", row.raw)
	}
	ev := h.do("GET", "/v1/m/governance/nhi/vault:approle:ci/events", tok, nil, hdr)
	if !bodyContains(ev.raw, "rotated") || bodyContains(ev.raw, "NEWSECRET") {
		t.Fatalf("event trail must record rotation but never the secret: %s", ev.raw)
	}
}

func TestNHIRotationPendingWhenNotApproved(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:ci", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)
	h.gov.UseLifecycleGate(&fakeGate{status: governance.GateStatusPending})
	h.gov.UseLifecycleActuators([]governance.LifecycleActuatorBinding{
		{Source: "vault", TenantRef: tenant.String(), Actuator: &fakeActuator{caps: rotateCaps("vault_entity")}},
	})
	r := h.do("POST", "/v1/m/governance/nhi/vault:approle:ci/rotate", tok, map[string]any{"target_ref": "approle:ci"}, hdr)
	if r.code != http.StatusAccepted || r.body["status"] != "pending" {
		t.Fatalf("expected 202 pending, got %d %s", r.code, r.raw)
	}
}

func TestNHIRotationHonestDegradeWithoutActuator(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "sops:age:1", "recipient", "sops", string(identitysource.PrincipalNHI), false)
	h.gov.UseLifecycleGate(&fakeGate{status: governance.GateStatusApproved})
	// No actuator wired for "sops" → honest degrade, never a fabricated rotation.
	r := h.do("POST", "/v1/m/governance/nhi/sops:age:1/rotate", tok, nil, hdr)
	if r.code != http.StatusOK || r.body["status"] != "unavailable" {
		t.Fatalf("expected unavailable degrade, got %d %s", r.code, r.raw)
	}
	if !hasFinding(h, "nhi_rotation_unavailable") {
		t.Fatalf("expected a coverage finding for the honest degrade")
	}
}

func TestNHIRotationDeniedWhenNoGate(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:ci", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)
	// No gate wired → deny-closed default (no_gate) → 503, never actuated.
	act := &fakeActuator{caps: rotateCaps("vault_entity"), secret: "s.X"}
	h.gov.UseLifecycleActuators([]governance.LifecycleActuatorBinding{{Source: "vault", TenantRef: tenant.String(), Actuator: act}})
	r := h.do("POST", "/v1/m/governance/nhi/vault:approle:ci/rotate", tok, map[string]any{"target_ref": "approle:ci"}, hdr)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("no gate should be 503, got %d %s", r.code, r.raw)
	}
	if act.lastRotate.Ref != "" {
		t.Fatalf("actuator must NOT be called without an approval")
	}
}

// --- governed offboarding: cascade + soft-delete + finalize ------------------

func TestNHIOffboardCascadeSoftDeleteFinalizeRestore(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:gone", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)
	// Bind two agents to the NHI so the offboard cascade has a fan-out to report.
	id := h.identityID(tenant, "vault:approle:gone")
	a1 := h.createAgent(tenant, "agent-1", "ag-1")
	h.bindAgentIdentity(tenant, a1.ID, id)
	a2 := h.createAgent(tenant, "agent-2", "ag-2")
	h.bindAgentIdentity(tenant, a2.ID, id)

	gate := &fakeGate{status: governance.GateStatusApproved}
	act := &fakeActuator{caps: rotateCaps("vault_entity")}
	h.gov.UseLifecycleGate(gate)
	h.gov.UseLifecycleActuators([]governance.LifecycleActuatorBinding{{Source: "vault", TenantRef: tenant.String(), Actuator: act}})

	// Finalize before soft-delete is rejected (must soft-delete first).
	if r := h.do("POST", "/v1/m/governance/nhi/vault:approle:gone/offboard/finalize", tok, nil, hdr); r.code != http.StatusConflict {
		t.Fatalf("finalize before soft should be 409, got %d %s", r.code, r.raw)
	}

	// Soft-delete: blocked in-product + disabled at source + recovery window.
	r := h.do("POST", "/v1/m/governance/nhi/vault:approle:gone/offboard", tok, map[string]any{"reason": "leaver"}, hdr)
	if r.code != http.StatusOK || r.body["status"] != "done" {
		t.Fatalf("offboard = %d %s", r.code, r.raw)
	}
	if !act.disabled {
		t.Fatalf("expected source disable on soft-delete")
	}
	if gate.lastReq.Action != "nhi.offboard" || !gate.lastReq.AllowBreakGlass {
		t.Fatalf("soft-delete gate wrong: %+v", gate.lastReq)
	}
	row := h.do("GET", "/v1/m/governance/nhi/vault:approle:gone", tok, nil, hdr)
	if row.body["offboard_state"] != "soft_deleted" || row.body["enforcement"] != "blocked" {
		t.Fatalf("expected soft_deleted+blocked, got %s", row.raw)
	}
	// The cascade: both bound agents now resolve to a blocked NHI (enforcement query).
	blocked, _, err := h.gov.NHIEnforcement(context.Background(), tenant, "vault:approle:gone")
	if err != nil || !blocked {
		t.Fatalf("offboarded NHI should be blocked: %v %v", blocked, err)
	}
	bl, _, _ := h.gov.NHIEnforcementForAgentRef(context.Background(), tenant, "ag-1")
	if !bl {
		t.Fatalf("a bound agent should be blocked by the offboard cascade")
	}

	// Finalize: CRITICAL, NO break-glass (the erase-gate precedent).
	r = h.do("POST", "/v1/m/governance/nhi/vault:approle:gone/offboard/finalize", tok, nil, hdr)
	if r.code != http.StatusOK || r.body["status"] != "done" {
		t.Fatalf("finalize = %d %s", r.code, r.raw)
	}
	if !act.finalized {
		t.Fatalf("expected source finalize")
	}
	if gate.lastReq.Action != "nhi.offboard.finalize" || gate.lastReq.AllowBreakGlass {
		t.Fatalf("finalize must forbid break-glass: %+v", gate.lastReq)
	}
	// A finalized NHI cannot be restored.
	if r := h.do("POST", "/v1/m/governance/nhi/vault:approle:gone/restore", tok, nil, hdr); r.code != http.StatusConflict {
		t.Fatalf("restore after finalize should be 409, got %d %s", r.code, r.raw)
	}
}

func TestNHIRestoreReversesSoftDelete(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:back", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)
	act := &fakeActuator{caps: rotateCaps("vault_entity")}
	h.gov.UseLifecycleGate(&fakeGate{status: governance.GateStatusApproved})
	h.gov.UseLifecycleActuators([]governance.LifecycleActuatorBinding{{Source: "vault", TenantRef: tenant.String(), Actuator: act}})

	h.do("POST", "/v1/m/governance/nhi/vault:approle:back/offboard", tok, nil, hdr)
	r := h.do("POST", "/v1/m/governance/nhi/vault:approle:back/restore", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("restore = %d %s", r.code, r.raw)
	}
	if !act.restored {
		t.Fatalf("expected source restore")
	}
	row := h.do("GET", "/v1/m/governance/nhi/vault:approle:back", tok, nil, hdr)
	if row.body["offboard_state"] != "none" || row.body["enforcement"] != "monitor" {
		t.Fatalf("restore should clear the block, got %s", row.raw)
	}
}

// --- orphan detection (disabled sponsor) + posture ---------------------------

func TestNHIOrphanDetectionAndPosture(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	h.seedIdentity(tenant, "vault:approle:owned", "vault_entity", "vault", string(identitysource.PrincipalNHI), false)
	h.seedIdentity(tenant, "human:bob", "user", "okta", string(identitysource.PrincipalHuman), false)

	if r := h.do("PUT", "/v1/m/governance/nhi/vault:approle:owned/ownership", tok,
		map[string]any{"owner_ref": "human:bob", "sponsor_ref": "human:bob"}, hdr); r.code != http.StatusNoContent {
		t.Fatalf("ownership = %d %s", r.code, r.raw)
	}
	// Sponsor healthy → sweep reports not orphaned.
	h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr)
	if row := h.do("GET", "/v1/m/governance/nhi/vault:approle:owned", tok, nil, hdr); row.body["orphaned"] != false {
		t.Fatalf("healthy sponsor should not orphan, got %s", row.raw)
	}
	// Disable the sponsor in the roster, sweep → orphaned + finding.
	h.setIdentityDisabled(tenant, "human:bob", true)
	h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr)
	row := h.do("GET", "/v1/m/governance/nhi/vault:approle:owned", tok, nil, hdr)
	if row.body["orphaned"] != true {
		t.Fatalf("disabled sponsor should orphan the NHI, got %s", row.raw)
	}
	if !hasFinding(h, "nhi_orphaned") {
		t.Fatalf("expected a nhi_orphaned finding")
	}

	// Posture aggregates the estate (coverage + counts).
	p := h.do("GET", "/v1/m/governance/nhi/posture", tok, nil, hdr)
	if p.code != http.StatusOK {
		t.Fatalf("posture = %d %s", p.code, p.raw)
	}
	if p.body["total"] == nil || p.body["orphaned"] == nil || p.body["rotation_coverage"] == nil {
		t.Fatalf("posture missing fields: %s", p.raw)
	}
}

// --- enforcement query (the PEP risk-conditional deny seam) -------------------

func TestNHIEnforcementDefaultsOpenForUnmanaged(t *testing.T) {
	h := newHarness(t)
	tenant, _ := h.nhiTenant()
	// An NHI with no lifecycle row is NOT blocked (day-1 operations keep working).
	blocked, _, err := h.gov.NHIEnforcement(context.Background(), tenant, "unmanaged:thing")
	if err != nil || blocked {
		t.Fatalf("unmanaged NHI must default open, got blocked=%v err=%v", blocked, err)
	}
	// An unknown agent ref likewise defaults open.
	bl, _, err := h.gov.NHIEnforcementForAgentRef(context.Background(), tenant, "no-such-agent")
	if err != nil || bl {
		t.Fatalf("unknown agent must default open, got blocked=%v err=%v", bl, err)
	}
}

// --- test-only store helpers (kept here to avoid touching the shared harness) -

func (h *harness) identityID(tenant model.TenantID, externalID string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		list, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: externalID}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(list) > 0 {
			id = list[0].ID
		}
		return nil
	}); err != nil {
		h.t.Fatalf("identityID: %v", err)
	}
	return id
}

func (h *harness) bindAgentIdentity(tenant model.TenantID, agentID, identityID model.ID) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(context.Background(), agentID)
		if err != nil {
			return err
		}
		a.IdentityID = identityID
		_, err = sc.Agents().Update(context.Background(), a)
		return err
	}); err != nil {
		h.t.Fatalf("bind agent: %v", err)
	}
}

func (h *harness) setIdentityDisabled(tenant model.TenantID, externalID string, disabled bool) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		list, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: externalID}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return nil
		}
		id := list[0]
		if id.Metadata == nil {
			id.Metadata = map[string]any{}
		}
		id.Metadata["disabled"] = disabled
		_, err = sc.Identities().Update(context.Background(), id)
		return err
	}); err != nil {
		h.t.Fatalf("set disabled: %v", err)
	}
}

func bodyContains(raw, sub string) bool {
	return len(raw) >= len(sub) && (indexOf(raw, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
