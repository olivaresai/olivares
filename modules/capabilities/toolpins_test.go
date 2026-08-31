// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/capabilities"
)

// fakePinAdmin implements the connector's ToolPinAdmin seam for route tests. Its
// ApplyPinChange is a SYNCHRONOUS in-memory stand-in for the enterprise durable applier: it
// models the OperationID idempotency dedup, the base-version CAS and the drift CAS, and
// settles immediately (AppliedState="applied") so a test can assert the resulting pin. The
// real applier is async (pending→settle via outbox) and lives in the absent enterprise
// overlay (UNVERIFIED here).
type fakePinAdmin struct {
	pins    map[string]mcpc.PinSnapshot        // key server|tool → current durable view
	version map[string]int64                   // key server|tool → current base version
	ops     map[string]string                  // operation_id → effect_digest (idempotency)
	outcome map[string]mcpc.ToolPinApplyResult // operation_id → original result
}

func newFakePinAdmin() *fakePinAdmin {
	return &fakePinAdmin{
		pins:    map[string]mcpc.PinSnapshot{},
		version: map[string]int64{},
		ops:     map[string]string{},
		outcome: map[string]mcpc.ToolPinApplyResult{},
	}
}

// Pins reports the CURRENT base version with each snapshot, exactly as the durable
// applier does: the version map is this fake's durable row counter, and a read that
// omitted it would hand every client a precondition it could not satisfy — the
// defect. Stamping it here is what makes "GET then POST with what you read" a real round
// trip in these tests instead of a constant 0 that happens to match an empty map.
func (f *fakePinAdmin) Pins() []mcpc.PinSnapshot {
	out := make([]mcpc.PinSnapshot, 0, len(f.pins))
	for key, p := range f.pins {
		p.Version = f.version[key]
		out = append(out, p)
	}
	return out
}

func (f *fakePinAdmin) RecordPin(_ context.Context, server, tool, fingerprint string) error {
	key := server + "|" + tool
	p := f.pins[key]
	p.Server, p.Tool, p.Fingerprint = server, tool, fingerprint
	p.DriftFingerprint, p.DriftAt = "", time.Time{}
	p.PinCount++
	f.pins[key] = p
	return nil
}

func (f *fakePinAdmin) Unpin(_ context.Context, server, tool string) error {
	key := server + "|" + tool
	if _, ok := f.pins[key]; !ok {
		return fmt.Errorf("no pin for %s", key)
	}
	delete(f.pins, key)
	return nil
}

func (f *fakePinAdmin) ApplyPinChange(_ context.Context, ch mcpc.ToolPinChange) (mcpc.ToolPinApplyResult, error) {
	if f.ops == nil {
		f.ops, f.outcome, f.version = map[string]string{}, map[string]mcpc.ToolPinApplyResult{}, map[string]int64{}
	}
	// Idempotency dedup by OperationID.
	if dg, ok := f.ops[ch.OperationID]; ok {
		if dg != ch.EffectDigest {
			return mcpc.ToolPinApplyResult{}, mcpc.ErrPinReplay
		}
		return f.outcome[ch.OperationID], nil // original outcome, no re-effect
	}
	key := ch.Server + "|" + ch.Tool
	cur := f.pins[key]
	// Base-version CAS against the durable row.
	if ch.ExpectedVersion != f.version[key] {
		return mcpc.ToolPinApplyResult{}, mcpc.ErrPinVersionConflict
	}
	// Drift CAS (from_drift approve): the tool must still show exactly the reviewed drift.
	if ch.ExpectedDriftFingerprint != "" && cur.DriftFingerprint != ch.ExpectedDriftFingerprint {
		return mcpc.ToolPinApplyResult{}, mcpc.ErrPinDriftChanged
	}
	switch ch.Action {
	case "approve":
		cur.Server, cur.Tool, cur.Fingerprint = ch.Server, ch.Tool, ch.DesiredFingerprint
		cur.DriftFingerprint, cur.DriftAt = "", time.Time{}
		cur.PinCount++
		f.pins[key] = cur
	case "unpin":
		delete(f.pins, key)
	}
	newVer := f.version[key] + 1
	f.version[key] = newVer
	res := mcpc.ToolPinApplyResult{OperationID: ch.OperationID, StateVersion: newVer, AppliedState: "applied"}
	f.ops[ch.OperationID] = ch.EffectDigest
	f.outcome[ch.OperationID] = res
	return res, nil
}

// Community posture: no admin wired → the surface answers 501, never a panic.
func TestToolPinsRoutesAreEnterprisePending(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	r := h.do("GET", "/v1/m/capabilities/toolpins", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusNotImplemented {
		t.Fatalf("community toolpins = %d %s, want 501", r.code, r.raw)
	}
}

func TestToolPinsListApproveUnpin(t *testing.T) {
	fake := newFakePinAdmin()
	h := newHarness(t, capabilities.WithToolPinAdmin(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	other := h.createOrg(admin, "globex")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	fake.pins[tenant.String()+"|search"] = mcpc.PinSnapshot{
		Server: tenant.String(), Tool: "search", Fingerprint: "fp-1",
		PinnedAt: now, UpdatedAt: now, PinCount: 1,
		DriftFingerprint: "fp-evil", DriftAt: now.Add(time.Hour),
	}
	fake.pins[other.String()+"|secret-tool"] = mcpc.PinSnapshot{
		Server: other.String(), Tool: "secret-tool", Fingerprint: "fp-x",
		PinnedAt: now, UpdatedAt: now, PinCount: 1,
	}

	// List: tenant-scoped — the other tenant's tool must never appear.
	r := h.do("GET", "/v1/m/capabilities/toolpins", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, "secret-tool") {
		t.Fatalf("cross-tenant pin leaked: %s", r.raw)
	}
	if !strings.Contains(r.raw, `"drift_fingerprint":"fp-evil"`) {
		t.Fatalf("drift missing from list: %s", r.raw)
	}

	// Approve from drift (write tier: viewer forbidden, editor allowed). The operator
	// supplies the reviewed drift + base version + an Idempotency-Key (D-09 contract).
	body := map[string]any{"tool": "search", "from_drift": true, "expected_version": 0, "expected_drift_fingerprint": "fp-evil"}
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", viewer, body, tenantIdem(tenant, "k-approve-1")); r.code != http.StatusForbidden {
		t.Fatalf("viewer approve = %d, want 403", r.code)
	}
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor, body, tenantIdem(tenant, "k-approve-1")); r.code != http.StatusAccepted {
		t.Fatalf("approve from drift = %d %s, want 202", r.code, r.raw)
	}
	if got := fake.pins[tenant.String()+"|search"].Fingerprint; got != "fp-evil" {
		t.Fatalf("approve did not record the drifted fingerprint: %q", got)
	}
	appliedCount := fake.pins[tenant.String()+"|search"].PinCount
	// Idempotent retry: SAME Idempotency-Key + same binding → the original 202, no re-effect
	// (pin_count must not advance past the single apply).
	if r := h.do("POST", "/v1/m/capabilities/toolpins/approve", editor, body, tenantIdem(tenant, "k-approve-1")); r.code != http.StatusAccepted {
		t.Fatalf("idempotent approve retry = %d, want 202", r.code)
	}
	if got := fake.pins[tenant.String()+"|search"].PinCount; got != appliedCount {
		t.Fatalf("idempotent retry re-applied the effect: pin_count = %d, want %d", got, appliedCount)
	}

	// Unpin the now-applied pin (base version advanced to 1 by the approve).
	unpin := map[string]any{"tool": "search", "expected_version": 1}
	if r := h.do("POST", "/v1/m/capabilities/toolpins/unpin", editor, unpin, tenantIdem(tenant, "k-unpin-1")); r.code != http.StatusAccepted {
		t.Fatalf("unpin = %d %s, want 202", r.code, r.raw)
	}
	if _, ok := fake.pins[tenant.String()+"|search"]; ok {
		t.Fatal("unpin did not revoke the pin")
	}
	// The action can never cross tenants: globex's pin is untouched.
	if _, ok := fake.pins[other.String()+"|secret-tool"]; !ok {
		t.Fatal("cross-tenant pin was mutated")
	}
}
