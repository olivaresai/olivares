// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const findingKindReservationDrift = "finops_reservation_drift"

// AdmissionReconciliation is the operator-visible comparison of reservations
// against commits. Drift is a posture finding, never a silent rewrite.
type AdmissionReconciliation struct {
	SweptExpired       int `json:"swept_expired"`
	Active             int `json:"active"`
	Committed          int `json:"committed"`
	Released           int `json:"released"`
	ExpiredUnsettled   int `json:"expired_unsettled"`
	ActiveLapsed       int `json:"active_lapsed"`
	IdempotencyOrphans int `json:"idempotency_orphans"`
	// Drift is true when a caller left headroom without Commit/Release, or an
	// idempotency row points at a handle that no longer exists.
	Drift      bool   `json:"drift"`
	FindingRef string `json:"finding_ref,omitempty"`
	Note       string `json:"note,omitempty"`
}

// ReconcileReservations compares reservation rows with their terminal states
// and emits a posture finding when drift is present. It is the job the brief
// asks for: an operator (or a scheduler) runs it; it does not rewrite money.
func (m *Module) ReconcileReservations(ctx context.Context, tenant model.TenantID) (AdmissionReconciliation, error) {
	var out AdmissionReconciliation
	if m.data == nil {
		return out, attemptErr(errCodeCapabilityUnavailable, nil)
	}

	swept, err := m.SweepExpiredReservations(ctx, tenant)
	if err != nil {
		return out, err
	}
	out.SweptExpired = swept
	now := m.clock.Now()

	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, incomplete, err := scanReservations(ctx, repo, []model.Filter{})
		if err != nil {
			return err
		}
		if incomplete != "" {
			return fmt.Errorf("%w: %s", errReservationScanIncomplete, incomplete)
		}
		handles := map[string]bool{}
		for _, r := range rows {
			h := r.String(colResvHandle)
			handles[h] = true
			switch r.String(colResvState) {
			case resvStateActive:
				out.Active++
				exp, perr := model.ParseTimestamp(r.String(colResvExpiresAt))
				if perr == nil && !exp.Time().After(now.Time()) {
					out.ActiveLapsed++
				}
			case resvStateCommitted:
				out.Committed++
			case resvStateReleased:
				out.Released++
			case resvStateExpired:
				out.ExpiredUnsettled++
			}
		}

		idemp, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		keys, incomplete, err := scanReservations(ctx, idemp, []model.Filter{})
		if err != nil {
			return err
		}
		if incomplete != "" {
			return fmt.Errorf("%w: %s", errReservationScanIncomplete, incomplete)
		}
		for _, r := range keys {
			h := r.String(colAdmHandle)
			if h != "" && !handles[h] && r.String(colAdmState) == admStateReserved {
				out.IdempotencyOrphans++
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}

	out.Drift = out.ExpiredUnsettled > 0 || out.ActiveLapsed > 0 || out.IdempotencyOrphans > 0
	if out.Drift {
		out.Note = "reservation ledger drifted from caller settlement; see finding"
		out.FindingRef = m.emitReservationDrift(ctx, tenant, out)
	} else {
		out.Note = "reservation ledger matches commits and releases"
	}
	return out, nil
}

func (m *Module) emitReservationDrift(ctx context.Context, tenant model.TenantID, report AdmissionReconciliation) string {
	if m.host == nil {
		return ""
	}
	pre := stringsJoinCounts(report)
	sum := sha256.Sum256([]byte(pre))
	detail := hex.EncodeToString(sum[:])
	finding := sdkmodel.FindingReport{
		Kind:        findingKindReservationDrift,
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "finops.admission",
		SubjectRef:  tenant.String(),
		Title:       "FinOps reservation ledger drifted from commits",
		DetailHash:  detail,
		OccurredAt:  m.clock.Now().Time(),
		OWASPLLM:    []string{"LLM10:2025"},
	}
	ev := event.FromObservation(tenant.String(), Name, finding)
	if err := m.host.Publish(ctx, ev); err != nil {
		m.debugf("finops: emit reservation drift failed", "err", err)
		return ""
	}
	return findingKindReservationDrift
}

func stringsJoinCounts(r AdmissionReconciliation) string {
	return "expired_unsettled=" + strconv.Itoa(r.ExpiredUnsettled) +
		"|active_lapsed=" + strconv.Itoa(r.ActiveLapsed) +
		"|orphans=" + strconv.Itoa(r.IdempotencyOrphans) +
		"|swept=" + strconv.Itoa(r.SweptExpired)
}
