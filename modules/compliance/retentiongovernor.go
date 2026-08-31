// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// This file declares the RETENTION-GOVERNOR seam: the OPTIONAL enterprise
// records-management policy layer (named regulatory floors + a compliance-mode lock)
// that sits ON TOP of the open per-class retention schedules (retention.go). It is the
// same inverted-seam pattern as WithProfileResolver: the open module owns the
// schedules, the sweep, the holds and the certificates; the commercial add-on
// (built only with -tags enterprise) supplies the policy through this interface.
//
// nil governor (the open-core DEFAULT) preserves today's behavior EXACTLY: no floor is
// enforced, the sweep cuts on the operator's own retention_days, and a schedule may be
// freely authored, relaxed or deleted (retention.go is byte-identical). Nothing the
// open binary does is moved behind the wall — the governor only ADDS a deny when wired
// (no rug-pull).
//
// HONESTY BOUNDARY (docs/SECURITY-HARDENING.md): the governor governs the CONTROL-PLANE schedule, not
// the storage. The actual immutability of the archived records is the WORM substrate's
// object lock (S3 Object Lock COMPLIANCE / Azure immutable LOCKED / GCS Bucket Lock),
// surfaced honestly as ArchiveReceipt.LockVerified — never this policy flag. The copy
// must say the add-on "produces admissible records / enforces a retention floor",
// never "guarantees compliance" or "makes deletion impossible".

// Retention modes — the posture the governor reports for a floored class.
const (
	// RetentionModeCompliance is the locked posture: the regulatory floor is fixed
	// (a code/sealed-config minimum the running control plane cannot lower), and the
	// schedule that documents it cannot be deleted through the API. It mirrors S3
	// Object Lock COMPLIANCE mode (no in-band relaxation). Lowering it is an
	// off-API, auditable redeployment, not a superadmin click.
	RetentionModeCompliance = "compliance"
	// RetentionModeGovernance is the break-glass posture: the floor is still surfaced
	// and the sweep still honors it, but the operator may relax it through the add-on's
	// own configuration (an auditable change), mirroring S3 Object Lock GOVERNANCE
	// mode. The schedule may be deleted as today.
	RetentionModeGovernance = "governance"
)

// RetentionFloor is one named regulatory minimum the governor enforces on a data
// class — e.g. SEC 17a-4 (6y/3y), FINRA 4511 (6y), CFTC 1.31 (5y). It is a MINIMUM:
// the sweep never deletes a row younger than MinDays, and a purge schedule shorter
// than MinDays is refused at author time. It never caps the maximum — retaining
// longer is always permitted.
type RetentionFloor struct {
	// Class is the data-class id the floor applies to (a dataClassRegistry id).
	Class string `json:"class"`
	// MinDays is the regulatory minimum retention in days (the floor). It is the
	// effective sweep window when it exceeds the operator's retention_days.
	MinDays int `json:"min_days"`
	// Basis is a short, non-sensitive citation of the regulation that sets the floor
	// (e.g. "SEC 17a-4(a)", "FINRA 4511(b)", "CFTC 1.31(b)(3)").
	Basis string `json:"basis"`
	// Mode is RetentionModeCompliance (locked) or RetentionModeGovernance (break-glass).
	Mode string `json:"mode"`
}

// RetentionGovernor is the records-management policy seam: the regulatory floor +
// compliance-mode posture for a data class. The open engine consumes it ONLY through
// Floor; the commercial add-on (enterprise/wormretention, -tags enterprise) holds the
// floor table, the regime→class mapping and the compliance/governance mode. nil ⇒ no
// floor (open-core default, today's behavior).
type RetentionGovernor interface {
	// Floor returns the regulatory floor in force for a class, ok=false when the class
	// carries none. It is read at the sweep cutoff (clamp up to MinDays), at schedule
	// author time (refuse a sub-floor purge) and at schedule delete time (refuse when
	// Mode == RetentionModeCompliance), and it annotates the class/policy read DTOs.
	Floor(ctx context.Context, tenant model.TenantID, class string) (f RetentionFloor, ok bool)
}

// floorFor248 resolves the regulatory floor for a class, nil-safe. It returns
// ok=false (no floor) whenever the governor is un-wired (the open-core default), so
// every consume site collapses to today's exact behavior with a single guard.
func (m *Module) floorFor248(ctx context.Context, tenant model.TenantID, class string) (RetentionFloor, bool) {
	if m.governor == nil {
		return RetentionFloor{}, false
	}
	return m.governor.Floor(ctx, tenant, class)
}
