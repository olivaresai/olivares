// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// stubGovernor is a programmable RetentionGovernor: a fixed class→floor table, so the
// open consume sites (sweep clamp, author reject, compliance-mode delete seal, DTO
// annotation) can be exercised in the open module without the closed add-on.
type stubGovernor struct{ floors map[string]RetentionFloor }

func (g stubGovernor) Floor(_ context.Context, _ model.TenantID, class string) (RetentionFloor, bool) {
	f, ok := g.floors[class]
	return f, ok
}

// seedPurgePolicy inserts an ENABLED purge schedule directly (bypassing the author
// gate), so a sub-floor schedule can exist at sweep time to exercise the clamp as
// defense-in-depth (the PUT reject is the primary author-time guard).
func (h *harness) seedPurgePolicy(tenant model.TenantID, class string, days int64) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colDataClass:     class,
			colRPDays:        days,
			colRPDisposition: dispositionPurge,
			colRPEnabled:     true,
		})
		return err
	})
}

// TestRetentionGovernorRejectsSubFloorPurge: with a regulatory floor in force, a purge
// schedule shorter than the floor is refused at author time (422), while a compliant
// schedule (≥ floor) enables, and a retain schedule is never floor-checked.
func TestRetentionGovernorRejectsSubFloorPurge(t *testing.T) {
	gate := &stubApprovalGate{status: GateStatusApproved, ref: "ap-1", approvers: []string{"user:a"}}
	gov := stubGovernor{floors: map[string]RetentionFloor{
		"voice.session": {Class: "voice.session", MinDays: 2190, Basis: "FINRA 4511(b)", Mode: RetentionModeCompliance},
	}}
	h := newHarness(t, WithApprovalGate(gate), WithRetentionGovernor(gov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	// A 30-day purge under a 2190-day floor: rejected BEFORE the gate (422), citing the
	// basis. The gate must not even be consulted.
	r := h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 30, "disposition": "purge"})
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf("sub-floor purge must be 422, got %d %s", r.code, r.raw)
	}
	if len(gate.reqs) != 0 {
		t.Fatalf("the gate must not be consulted for a sub-floor purge: %v", gate.reqs)
	}

	// A compliant purge (≥ floor) passes the floor check and enables via the gate.
	r = h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 2555, "disposition": "purge"})
	if r.code != http.StatusOK || r.body["enabled"] != true {
		t.Fatalf("compliant purge must enable, got %d %s", r.code, r.raw)
	}

	// A retain schedule is never floor-checked (documenting retention is always allowed).
	r = h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 5, "disposition": "retain"})
	if r.code != http.StatusOK {
		t.Fatalf("retain must not be floor-checked, got %d %s", r.code, r.raw)
	}
}

// TestRetentionGovernorClampsSweep: a stored sub-floor purge schedule still never
// deletes a row younger than the floor — the sweep clamps the cutoff UP to the floor
// (defense-in-depth). Without a governor the same rows are purged.
func TestRetentionGovernorClampsSweep(t *testing.T) {
	row := model.Record{"session_ref": "vs", "agent_ref": "ag", "duration_ms": int64(5)}

	// (a) With a 3650-day floor: a 30-day schedule cannot reach 40-day-old rows.
	clock := &movableClock{t: time.Now().UTC()}
	gov := stubGovernor{floors: map[string]RetentionFloor{
		"voice.session": {Class: "voice.session", MinDays: 3650, Basis: "SEC 17a-4(a)", Mode: RetentionModeCompliance},
	}}
	h := newHarness(t, WithClock(clock), WithRetentionGovernor(gov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")
	h.seedExtRows(tenant, voiceSessionStandInKind, 3, row)
	h.seedPurgePolicy(tenant, "voice.session", 30)
	clock.advance(40 * 24 * time.Hour)
	sw := h.sweepNow(adm, tenant)
	if intOf(sw.body["purged"]) != 0 || h.countExtRows(tenant, voiceSessionStandInKind) != 3 {
		t.Fatalf("floor must clamp the sweep — rows must survive: %s", sw.raw)
	}

	// (b) Same setup WITHOUT a governor: the 30-day schedule purges the aged rows.
	clock2 := &movableClock{t: time.Now().UTC()}
	h2 := newHarness(t, WithClock(clock2))
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "beta")
	adm2 := h2.roleToken(admin2, tenant2, "b@x.io", "admin")
	h2.seedExtRows(tenant2, voiceSessionStandInKind, 3, row)
	h2.seedPurgePolicy(tenant2, "voice.session", 30)
	clock2.advance(40 * 24 * time.Hour)
	sw2 := h2.sweepNow(adm2, tenant2)
	if intOf(sw2.body["purged"]) != 3 || h2.countExtRows(tenant2, voiceSessionStandInKind) != 0 {
		t.Fatalf("without a floor the aged rows must be purged: %s", sw2.raw)
	}
}

// TestRetentionGovernorComplianceModeSealsDelete: in compliance mode the schedule is
// sealed (DELETE 403); in governance mode the schedule may be deleted as today.
func TestRetentionGovernorComplianceModeSealsDelete(t *testing.T) {
	gov := stubGovernor{floors: map[string]RetentionFloor{
		"voice.session":    {Class: "voice.session", MinDays: 2190, Basis: "SEC 17a-4(a)", Mode: RetentionModeCompliance},
		"session.timeline": {Class: "session.timeline", MinDays: 1825, Basis: "CFTC 1.31(b)(3)", Mode: RetentionModeGovernance},
	}}
	h := newHarness(t, WithRetentionGovernor(gov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	// Compliance mode: author a retain schedule, then DELETE is sealed (403).
	if r := h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 2555, "disposition": "retain"}); r.code != http.StatusOK {
		t.Fatalf("retain author = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", s138Base+"/retention/policies/voice.session", adm, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("compliance-mode delete must be 403, got %d %s", r.code, r.raw)
	}

	// Governance mode: the schedule may be deleted (today's free direction).
	if r := h.putPolicy(adm, tenant, "session.timeline", map[string]any{"retention_days": 2000, "disposition": "retain"}); r.code != http.StatusOK {
		t.Fatalf("governance retain author = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", s138Base+"/retention/policies/session.timeline", adm, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("governance-mode delete must be 204, got %d %s", r.code, r.raw)
	}
}

// TestRetentionGovernorAnnotatesDTOs: the floor is surfaced on the class registry and
// the policy read DTOs (nil/absent in the open-core build).
func TestRetentionGovernorAnnotatesDTOs(t *testing.T) {
	gov := stubGovernor{floors: map[string]RetentionFloor{
		"voice.session": {Class: "voice.session", MinDays: 2190, Basis: "FINRA 4511(b)", Mode: RetentionModeCompliance},
	}}
	h := newHarness(t, WithRetentionGovernor(gov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	classes := itemsOf(t, h.do("GET", s138Base+"/retention/classes", adm, nil, tenantHdr(tenant)))
	var floored, unfloored map[string]any
	for _, c := range classes {
		switch c["id"] {
		case "voice.session":
			floored = c
		case "finops.cost_sample":
			unfloored = c
		}
	}
	rf, ok := floored["regulatory_floor"].(map[string]any)
	if !ok || intOf(rf["min_days"]) != 2190 || rf["basis"] != "FINRA 4511(b)" || rf["mode"] != RetentionModeCompliance {
		t.Fatalf("voice.session must carry the floor annotation: %v", floored)
	}
	if _, present := unfloored["regulatory_floor"]; present {
		t.Fatalf("an unfloored class must omit regulatory_floor: %v", unfloored)
	}

	// The policy DTO carries it too.
	if r := h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 2555, "disposition": "retain"}); r.code != http.StatusOK {
		t.Fatalf("author = %d %s", r.code, r.raw)
	}
	pols := itemsOf(t, h.do("GET", s138Base+"/retention/policies", adm, nil, tenantHdr(tenant)))
	if len(pols) != 1 {
		t.Fatalf("want 1 policy, got %v", pols)
	}
	if rf, ok := pols[0]["regulatory_floor"].(map[string]any); !ok || intOf(rf["min_days"]) != 2190 {
		t.Fatalf("policy DTO must carry the floor: %v", pols[0])
	}
}
