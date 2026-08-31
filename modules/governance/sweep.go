// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Sweep bounds: a batch is one bounded transaction; the iteration cap keeps a
// single sweep call from running unboundedly. A sweep is the explicit, tenant-
// scoped materialization of lazy expiry/escalation — a module cannot enumerate
// tenants for a background guarantee (docs/contracts), so this is the
// honest mechanism, triggered per tenant by an operator or a cron.
const (
	maxSweepBatch      = 200
	maxSweepIterations = 50
)

// Finding kinds emitted by the sweep (delivered to outputs).
const (
	findingEscalated = "governance_approval_escalated"
	findingExpired   = "governance_approval_expired"
)

// sweepReport summarizes what a sweep materialized for the tenant.
type sweepReport struct {
	Scanned   int  `json:"scanned"`
	Escalated int  `json:"escalated"`
	Expired   int  `json:"expired"`
	More      bool `json:"more"` // true if pending requests remain unscanned (re-run the sweep)
	// break-glass grants whose lapsed expiry this sweep materialized.
	BreakGlassExpired int `json:"breakglass_expired"`
}

// pendingFinding is a finding collected during a sweep transaction and emitted
// only AFTER it commits (so a rolled-back transition never signals).
type pendingFinding struct {
	kind     string
	approval string
	action   string
	severity sdkmodel.Severity
}

// handleSweep materializes expiry and escalation for the tenant's pending
// approvals. Admin-tier and self-audited. Idempotent: escalation is gated on a
// persisted escalated_at marker, so a repeated sweep cannot double-emit the
// escalation finding; an already-expired request leaves the pending set and is not
// revisited. Findings and expiration resolutions are emitted only after each
// batch commits.
func (m *Module) handleSweep(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var report sweepReport
	cursor := ""
	for it := 0; it < maxSweepIterations; it++ {
		var (
			batch       []pendingFinding
			resolutions []approvalDTO
			nextCur     string
			more        bool
			batchN      int
			escN        int
			expN        int
		)
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(approvalKind)
			if err != nil {
				return err
			}
			q := model.Query{Filters: []model.Filter{eq(colStatus, statusPending)}, Limit: maxSweepBatch, Cursor: cursor}
			recs, page, err := repo.List(r.Context(), q)
			if err != nil {
				return err
			}
			now := m.clock.Now()
			pols, err := loadApprovalPolicies(r.Context(), sc)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				batchN++
				didEscalate, didExpire := false, false
				if esc, ok := tsValue(rec, colEscalateAt); ok && !now.Before(esc) && rec.String(colEscalatedAt) == "" {
					rec[colEscalatedAt] = now.String()
					didEscalate = true
				}
				if exp, ok := tsValue(rec, colExpiresAt); ok && !now.Before(exp) {
					rec[colStatus] = statusExpired
					rec[colDecidedAt] = now.String()
					didExpire = true
				}
				if !didEscalate && !didExpire {
					continue
				}
				updated, err := repo.Update(r.Context(), rec)
				if err != nil {
					if isConflict(err) {
						continue // raced with a decision/cancel; a later sweep revisits it
					}
					return err
				}
				id := updated.String(model.ColID)
				action := updated.String(colAction)
				if didEscalate {
					batch = append(batch, pendingFinding{kind: findingEscalated, approval: id, action: action, severity: sdkmodel.SeverityHigh})
					escN++
				}
				if didExpire {
					batch = append(batch, pendingFinding{kind: findingExpired, approval: id, action: action, severity: sdkmodel.SeverityMedium})
					resolutions = append(resolutions, toApprovalDTO(updated, now, liveRiskTier(pols, updated)))
					expN++
				}
			}
			nextCur, more = page.Cursor, page.HasMore
			if batchN > 0 {
				return auditEvent(r.Context(), sc, mc, "governance.approval.sweep", approvalKind, "", map[string]any{
					"scanned": batchN, "escalated": escN, "expired": expN,
				})
			}
			return nil
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		for _, f := range batch { // emit AFTER the batch commit
			m.emitApprovalFinding(r.Context(), mc.Tenant, f)
		}
		for _, out := range resolutions { // emit AFTER the batch commit
			m.emitApprovalResolved(r.Context(), mc.Tenant, out)
		}
		report.Scanned += batchN
		report.Escalated += escN
		report.Expired += expN
		if !more || nextCur == "" {
			report.More = false
			break
		}
		cursor = nextCur
		if it == maxSweepIterations-1 {
			report.More = true // bounded this call; the operator re-runs to continue
		}
	}
	// materialize break-glass expiry too. The set of stored-active grants is
	// tiny (activation enforces one at a time), so a single bounded pass suffices.
	if n, err := m.sweepBreakGlass(w, r, mc); err != nil {
		return // sweepBreakGlass already wrote the error
	} else {
		report.BreakGlassExpired = n
	}
	writeJSON(w, http.StatusOK, report)
}

// sweepBreakGlass expires stored-active grants past their window and emits the
// expiry finding AFTER commit (the "post-review required" reminder). Safety does
// not depend on it — effectiveBreakGlassStatus already denies a lapsed grant at
// every consume — this materializes the terminal state and notifies. On error it
// writes the HTTP error and returns it (the caller stops).
func (m *Module) sweepBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) (int, error) {
	var expired []string
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(breakGlassKind)
		if err != nil {
			return err
		}
		grants, err := listAll(r.Context(), repo, eq(colBGStatus, bgStatusActive))
		if err != nil {
			return err
		}
		now := m.clock.Now()
		for _, g := range grants {
			if effectiveBreakGlassStatus(g, now) != bgStatusExpired {
				continue
			}
			g[colBGStatus] = bgStatusExpired
			if _, err := repo.Update(r.Context(), g); err != nil {
				if isConflict(err) {
					continue // raced with a revoke/review; a later sweep revisits it
				}
				return err
			}
			expired = append(expired, g.String(model.ColID))
		}
		if len(expired) > 0 {
			return auditEvent(r.Context(), sc, mc, "governance.breakglass.sweep", breakGlassKind, "", map[string]any{
				"expired": len(expired),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return 0, err
	}
	for _, id := range expired { // emit AFTER commit
		m.emitBreakGlassFinding(r.Context(), mc.Tenant, findingBreakGlassExpired, id, "", sdkmodel.SeverityHigh,
			"Break-glass grant expired — emergency window closed; post-review required before any new activation")
	}
	return len(expired), nil
}

// emitApprovalFinding publishes an approval escalation/expiry finding. The action
// is hashed into DetailHash (it is operator-supplied free-ish text); the title is
// a fixed non-sensitive template and the approval id rides SubjectRef (docs/SECURITY-HARDENING.md).
func (m *Module) emitApprovalFinding(ctx context.Context, tenant model.TenantID, f pendingFinding) {
	if m.host == nil {
		return
	}
	title := "Approval request escalated — awaiting a decision past its escalation window"
	if f.kind == findingExpired {
		title = "Approval request expired without a decision"
	}
	sum := sha256.Sum256([]byte(f.kind + "|" + f.approval + "|" + f.action))
	finding := sdkmodel.FindingReport{
		Kind:        f.kind,
		Severity:    f.severity,
		SubjectKind: "approval",
		SubjectRef:  f.approval,
		Title:       title,
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit approval finding failed", "err", err)
	}
}
