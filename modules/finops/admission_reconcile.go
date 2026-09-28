// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const findingKindReservationDrift = "finops_reservation_drift"

// AdmissionReconciliation is the operator-visible comparison of reservations against
// commits, with what recovery did and left. Drift is a posture finding, never a silent
// rewrite.
type AdmissionReconciliation struct {
	SweptExpired       int `json:"swept_expired"`
	Active             int `json:"active"`
	Committed          int `json:"committed"`
	Released           int `json:"released"`
	ExpiredUnsettled   int `json:"expired_unsettled"`
	ActiveLapsed       int `json:"active_lapsed"`
	IdempotencyOrphans int `json:"idempotency_orphans"`
	// AdmissionRecovery is, for the job, the recovery pass it ran first; for the read, what
	// the admission rows show is left. Its counters travel beside the ones above.
	AdmissionRecovery
	// Drift is true when a caller left headroom without Commit/Release, an idempotency row
	// points at a handle that no longer exists, a recovery write is unresolved, or an
	// admission row's owed list does not decode or the row fails its integrity check.
	Drift      bool   `json:"drift"`
	FindingRef string `json:"finding_ref,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Quiet reports that the job did nothing and found nothing to do: no sweep, no lapsed hold,
// no orphaned row, and a quiet recovery. Expired, committed, released and active rows are
// history or live holds, not work.
func (r AdmissionReconciliation) Quiet() bool {
	return r.SweptExpired == 0 && r.ActiveLapsed == 0 && r.IdempotencyOrphans == 0 && r.AdmissionRecovery.Quiet()
}

// inspection is what one read of every row found beyond the report: the corrupt rows; on
// rows no recovery pass reads, published and settled ones, the owed holds a row under
// which is still active and the lists that do not decode; and, for each row whose list
// does not decode, the digest of its text.
type inspection struct {
	corrupt              corruptRows
	owedPublished        int
	undecodablePublished int
	undecodable          map[model.ID]string
}

// InspectReservations REPORTS the reservation ledger against its commits and releases,
// and the admission rows, and writes nothing at all: no recovery, no sweep, no finding, no
// event. It is the answer the budget-READ route gives, so holding read permission never
// moves a hold or files evidence. A lapsed hold the job has not swept yet is visible here
// as ActiveLapsed, which is drift by the same definition, and a row that fails its
// integrity check is counted and never fails the read.
func (m *Module) InspectReservations(ctx context.Context, tenant model.TenantID) (AdmissionReconciliation, error) {
	out, _, err := m.inspect(ctx, tenant)
	return out, err
}

// inspect reads every reservation row and every admission row of the tenant in one read
// transaction.
func (m *Module) inspect(ctx context.Context, tenant model.TenantID) (AdmissionReconciliation, inspection, error) {
	var (
		out  AdmissionReconciliation
		seen inspection
	)
	if m.data == nil {
		return out, seen, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	now := m.clock.Now()
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		out = AdmissionReconciliation{}
		seen = inspection{corrupt: corruptRows{}, undecodable: map[model.ID]string{}}
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
		handles, held := map[string]bool{}, map[string]bool{}
		for _, r := range rows {
			handles[r.String(colResvHandle)] = true
			switch r.String(colResvState) {
			case resvStateActive:
				held[r.String(colResvHandle)] = true
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
		for _, row := range decodeAdmissionRows(keys, seen.corrupt) {
			recovered := row.state == admStatePending || row.state == admStateOwesRelease
			if row.owedErr != nil {
				seen.undecodable[row.id] = storedDigest(row.owedRaw)
				out.Undecodable++
				if !recovered {
					seen.undecodablePublished++
				}
				continue
			}
			switch {
			case row.state == admStateOwesRelease:
				out.LegacyOwesRelease++
			case row.state == admStatePending && row.handle.isZero():
				out.LegacyPending++
			}
			owed := len(row.owed)
			if !recovered {
				// No pass reads a published row, and its list is money only while a row
				// under a hold it names is active: afterwards it names nothing to settle.
				owed = 0
				for _, h := range row.owed {
					if held[h.String()] {
						owed++
					}
				}
				seen.owedPublished += owed
			}
			out.OwedRemaining += owed
			if row.state == admStateReserved && !row.handle.isZero() && !handles[row.handle.String()] {
				out.IdempotencyOrphans++
			}
		}
		out.Corrupt = len(seen.corrupt)
		out.LegacyStop = string(legacyStopStateFor(m.legacyStop, now, newestLegacyDate(keys)))
		return nil
	})
	if err != nil {
		return AdmissionReconciliation{}, inspection{}, err
	}
	out.Drift = drifted(out)
	out.Note = driftNote(out.Drift)
	return out, seen, nil
}

// decodeAdmissionRows decodes every admission row an inspection read and returns those that
// pass their integrity check, adding the others to corrupt: a row that does not decode, and
// every row of a key two rows carry, of a hold two rows name in the handle slot, or — no
// row naming it there — of a hold two publishing rows name in the spend slot. These are the
// tests the lookups apply.
func decodeAdmissionRows(recs []model.Record, corrupt corruptRows) []admissionRow {
	var decoded []admissionRow
	byKey := map[string][]model.ID{}
	byHandle := map[holdID][]model.ID{}
	bySpend := map[holdID][]model.ID{}
	for _, rec := range recs {
		id := model.ID(rec.String(model.ColID))
		byKey[rec.String(colAdmKey)] = append(byKey[rec.String(colAdmKey)], id)
		row, err := admissionRowFrom(rec)
		if err != nil {
			corrupt.add(id, corruptColumnOf(rec))
			continue
		}
		decoded = append(decoded, row)
		if !row.handle.isZero() {
			byHandle[row.handle] = append(byHandle[row.handle], id)
		}
		if !row.spendHandle.isZero() && publishesHold(row.state) {
			bySpend[row.spendHandle] = append(bySpend[row.spendHandle], id)
		}
	}
	for _, ids := range byKey {
		markShared(corrupt, ids, colAdmKey)
	}
	for _, ids := range byHandle {
		markShared(corrupt, ids, colAdmHandle)
	}
	for h, ids := range bySpend {
		if len(byHandle[h]) == 0 {
			markShared(corrupt, ids, colAdmSpendHandle)
		}
	}
	var healthy []admissionRow
	for _, row := range decoded {
		if _, bad := corrupt[row.id]; !bad {
			healthy = append(healthy, row)
		}
	}
	return healthy
}

// storedDigest is the SHA-256, in hex, of a stored cell's bytes: the text of a text cell
// and the bytes of a binary one. A cell of any other type has no stored text; its value's
// printed form is digested instead.
func storedDigest(cell any) string {
	var b []byte
	switch v := cell.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		b = []byte(fmt.Sprint(v))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// markShared adds every row of ids to corrupt when there is more than one.
func markShared(corrupt corruptRows, ids []model.ID, column string) {
	if len(ids) < 2 {
		return
	}
	for _, id := range ids {
		corrupt.add(id, column)
	}
}

// drifted reports whether a report is drift.
func drifted(r AdmissionReconciliation) bool {
	return r.ExpiredUnsettled > 0 || r.ActiveLapsed > 0 || r.IdempotencyOrphans > 0 ||
		r.Unresolved > 0 || r.Undecodable > 0 || r.Corrupt > 0
}

// driftNote is the report's note.
func driftNote(drift bool) string {
	if drift {
		return "reservation ledger drifted from caller settlement"
	}
	return "reservation ledger matches commits and releases"
}

// ReconcileReservations is the JOB an operator or a scheduler runs: it runs a recovery
// pass, sweeps expired holds, inspects what is left and emits a posture finding when the
// ledger drifted from what its callers settled. Drift is a finding, never a silent rewrite:
// the job moves money only as recovery and the sweep do. It needs budget WRITE, which is the
// difference between it and InspectReservations. A corrupt row is counted once, whether the
// recovery pass, the inspection or both found it.
func (m *Module) ReconcileReservations(ctx context.Context, tenant model.TenantID) (AdmissionReconciliation, error) {
	if m.data == nil {
		return AdmissionReconciliation{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	recovery, corrupt, err := m.recoverAdmissions(ctx, tenant)
	if err != nil {
		return AdmissionReconciliation{}, fmt.Errorf("finops: recover admissions: %w", err)
	}
	swept, err := m.SweepExpiredReservations(ctx, tenant)
	if err != nil {
		return AdmissionReconciliation{}, fmt.Errorf("finops: sweep expired reservations: %w", err)
	}
	out, seen, err := m.inspect(ctx, tenant)
	if err != nil {
		return AdmissionReconciliation{}, err
	}
	for id, column := range seen.corrupt {
		corrupt.add(id, column)
	}
	recovery.Corrupt = len(corrupt)
	recovery.OwedRemaining += seen.owedPublished
	recovery.Undecodable += seen.undecodablePublished
	out.AdmissionRecovery = recovery
	out.SweptExpired = swept
	out.Drift = drifted(out)
	out.Note = driftNote(out.Drift)
	if out.Drift {
		if ref := m.emitReservationDrift(ctx, tenant, out, corrupt, seen.undecodable); ref != "" {
			out.FindingRef = ref
			out.Note = "reservation ledger drifted from caller settlement; see finding"
		}
	}
	return out, nil
}

func (m *Module) emitReservationDrift(ctx context.Context, tenant model.TenantID, report AdmissionReconciliation, corrupt corruptRows, undecodable map[model.ID]string) string {
	if m.host == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(driftDetail(report, corrupt, undecodable)))
	finding := sdkmodel.FindingReport{
		Kind:        findingKindReservationDrift,
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "finops.admission",
		SubjectRef:  tenant.String(),
		Title:       "FinOps reservation ledger drifted from commits",
		DetailHash:  hex.EncodeToString(sum[:]),
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

// driftDetail is the finding's detail, of which only the digest leaves the module: the
// counts; each corrupt row by id with the column its fault is in; and each row whose owed
// list does not decode by id, with the digest of that list's text. No stored value is in
// it.
func driftDetail(r AdmissionReconciliation, corrupt corruptRows, undecodable map[model.ID]string) string {
	var b strings.Builder
	for _, c := range []struct {
		name string
		n    int
	}{
		{"expired_unsettled", r.ExpiredUnsettled}, {"active_lapsed", r.ActiveLapsed},
		{"orphans", r.IdempotencyOrphans}, {"swept", r.SweptExpired}, {"unresolved", r.Unresolved},
		{"undecodable", r.Undecodable}, {"corrupt", r.Corrupt},
	} {
		if b.Len() > 0 {
			b.WriteByte('|')
		}
		b.WriteString(c.name + "=" + strconv.Itoa(c.n))
	}
	for _, id := range sortedRowIDs(corrupt) {
		b.WriteString("|corrupt_row=" + id.String() + ":" + corrupt[id])
	}
	for _, id := range sortedRowIDs(undecodable) {
		b.WriteString("|undecodable_row=" + id.String() + ":" + colAdmOwedHandles + ":sha256=" + undecodable[id])
	}
	return b.String()
}

// sortedRowIDs returns the ids of rows in a stable order.
func sortedRowIDs(rows map[model.ID]string) []model.ID {
	ids := make([]model.ID, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
