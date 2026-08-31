// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// nhilifecycle_sweep.go — the staleness/expiry enforcement sweep, the NHI posture
// surface, the PEP enforcement query and the event-driven lifecycle trigger.
//
// Like the approval sweep, expiry/staleness is materialized by an EXPLICIT,
// tenant-scoped sweep (operator- or cron-triggered) — a module cannot enumerate
// tenants for a background guarantee (docs/contracts). Safety never
// depends on the sweep running: the enforcement state it materializes is what the
// PEP consults; the sweep also ensures a lifecycle row exists for every roster NHI
// (posture coverage) and emits the alert/block findings the notify rail delivers.

// pendingNHIFinding is a finding collected inside the sweep transaction and emitted
// only AFTER it commits (so a rolled-back transition never signals).
type pendingNHIFinding struct {
	kind     string
	ref      string
	severity sdkmodel.Severity
	title    string
}

// nhiSweepReport summarizes a sweep.
type nhiSweepReport struct {
	Scanned     int `json:"scanned"`
	Registered  int `json:"registered"`  // lifecycle rows created for newly-seen NHIs
	Stale       int `json:"stale"`       // rows now stale
	Blocked     int `json:"blocked"`     // rows escalated to block this sweep
	Orphaned    int `json:"orphaned"`    // rows whose sponsor is disabled/missing
	Unsponsored int `json:"unsponsored"` // rows missing owner or sponsor
}

// handleNHISweep materializes staleness/escalation, orphan and unsponsored state
// for the tenant's NHI lifecycle rows, and ensures a row exists for every roster
// NHI. Admin-tier, self-audited, idempotent (re-deriving from the live data).
func (m *Module) handleNHISweep(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var (
		report   nhiSweepReport
		findings []pendingNHIFinding
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		now := m.clock.Now()
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}

		// Pass 1 — ensure a lifecycle row for every roster NHI (posture coverage).
		reg, err := m.ensureNHIRows(r.Context(), sc)
		if err != nil {
			return err
		}
		report.Registered = reg

		// Pass 2 — recompute every lifecycle row.
		rows, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		for _, rec := range rows {
			report.Scanned++
			ref := rec.String(colNHIIdentityRef)
			changed, fs := m.recomputeStaleness(rec, now)
			for _, f := range fs {
				switch f.kind {
				case "nhi_credential_stale":
					report.Stale++
				case "nhi_credential_blocked":
					report.Blocked++
				}
			}
			// Orphan + unsponsored need the roster (a sponsor lookup).
			orphChanged, orph, unsp, of := m.checkSponsorship(r.Context(), sc, rec, ref)
			rowFindings := append(append([]pendingNHIFinding{}, fs...), of...)
			if orph {
				report.Orphaned++
			}
			if unsp {
				report.Unsponsored++
			}
			if changed || orphChanged {
				if _, err := repo.Update(r.Context(), rec); err != nil {
					if isConflict(err) {
						continue
					}
					return err
				}
				m.recordStateEvents(r.Context(), sc, ref, rowFindings)
				findings = append(findings, rowFindings...)
				continue
			}
			// Findings that did not require a state write remain informational.
			findings = append(findings, rowFindings...)
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.sweep", nhiLifecycleKind, "", map[string]any{
			"scanned": report.Scanned, "stale": report.Stale, "blocked": report.Blocked,
			"orphaned": report.Orphaned, "unsponsored": report.Unsponsored,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, f := range findings { // emit AFTER commit
		m.emitNHIFinding(r.Context(), mc.Tenant, f.kind, f.ref, f.severity, f.title)
	}
	writeJSON(w, http.StatusOK, report)
}

// ensureNHIRows scans the roster identities and find-or-creates a lifecycle row for
// every NHI (principal_type=nhi), so posture covers the whole estate. It returns the
// number of rows created. Bounded by the roster size (the same listCap pagination
// the bindings scan uses).
func (m *Module) ensureNHIRows(ctx context.Context, sc store.Scope) (int, error) {
	repo, err := sc.Ext(nhiLifecycleKind)
	if err != nil {
		return 0, err
	}
	created := 0
	q := model.Query{Limit: listCap}
	for {
		ids, page, err := sc.Identities().List(ctx, q)
		if err != nil {
			return created, err
		}
		for _, id := range ids {
			pt, _ := id.Metadata["principal_type"].(string)
			if pt != string(identitysource.PrincipalNHI) {
				continue
			}
			if _, found, err := findOne(ctx, repo, eq(colNHIIdentityRef, id.ExternalID)); err != nil {
				return created, err
			} else if found {
				continue
			}
			if _, err := repo.Create(ctx, newLifecycleRecord(id.ExternalID, id.Provider, "")); err != nil {
				if isConflict(err) {
					continue
				}
				return created, err
			}
			created++
		}
		if !page.HasMore || page.Cursor == "" {
			return created, nil
		}
		q.Cursor = page.Cursor
	}
}

// recomputeStaleness re-derives a row's staleness/enforcement state from the live
// rotation policy and clock (never a stored snapshot of the verdict). It mutates rec
// in place and returns whether it changed plus the findings to emit. Offboarded rows
// keep their block (offboarding owns enforcement, not staleness).
func (m *Module) recomputeStaleness(rec model.Record, now model.Timestamp) (changed bool, findings []pendingNHIFinding) {
	ref := rec.String(colNHIIdentityRef)
	switch rec.String(colNHIOffboard) {
	case offboardFinal:
		return false, nil
	case offboardSoft:
		// Recovery window elapsed → remind the operator to finalize (the block stays).
		if until, ok := tsValue(rec, colNHIRecoverUntil); ok && !now.Before(until) {
			findings = append(findings, pendingNHIFinding{
				kind: "nhi_offboard_recovery_elapsed", ref: ref, severity: sdkmodel.SeverityMedium,
				title: "NHI soft-delete recovery window elapsed — finalize (governed) or restore",
			})
		}
		return false, findings
	}

	crit := ActionRiskTier(rec.String(colNHICriticality))
	if !validRiskTier(string(crit)) {
		crit = defaultNHICriticality
	}

	rotated, haveRotated := tsValue(rec, colNHIRotatedAt)
	if !haveRotated {
		if rec.String(colNHIStaleStatus) != staleUnknown {
			rec[colNHIStaleStatus] = staleUnknown
			changed = true
		}
		findings = append(findings, pendingNHIFinding{
			kind: "nhi_rotation_unknown", ref: ref, severity: sdkmodel.SeverityInfo,
			title: "NHI rotation recency unknown — set rotated_at via the rotation policy for coverage",
		})
		return changed, findings
	}

	maxAge := time.Duration(rec.Int(colNHIMaxAgeSec)) * time.Second
	if maxAge <= 0 {
		maxAge = defaultMaxAge(crit)
	}
	age := now.Time().Sub(rotated.Time())

	if age <= maxAge { // fresh
		if rec.String(colNHIStaleStatus) != staleOK {
			rec[colNHIStaleStatus] = staleOK
			rec[colNHIStaleSince] = nil
			rec[colNHIBlockAfter] = nil
			changed = true
		}
		if rec.String(colNHIEnforce) != enforceMonitor && isStalenessReason(rec.String(colNHIEnforceWhy)) {
			rec[colNHIEnforce] = enforceMonitor
			rec[colNHIEnforceWhy] = nil
			changed = true
			findings = append(findings, pendingNHIFinding{
				kind: "nhi_credential_unblocked", ref: ref, severity: sdkmodel.SeverityInfo,
				title: "NHI credential rotated within window — staleness block cleared",
			})
		}
		return changed, findings
	}

	// stale
	if rec.String(colNHIStaleStatus) != staleStale { // first time stale
		rec[colNHIStaleStatus] = staleStale
		rec[colNHIStaleSince] = now.String()
		changed = true
		findings = append(findings, pendingNHIFinding{
			kind: "nhi_credential_stale", ref: ref, severity: sdkmodel.SeverityMedium,
			title: "NHI credential not rotated within its window (OWASP NHI Top-10)",
		})
		if crit == RiskTierCritical {
			// a CRITICAL credential blocks immediately (no 30-day grace).
			rec[colNHIEnforce] = enforceBlocked
			rec[colNHIEnforceWhy] = "stale credential (critical — immediate block)"
			rec[colNHIBlockAfter] = now.String()
			findings = append(findings, pendingNHIFinding{
				kind: "nhi_credential_blocked", ref: ref, severity: sdkmodel.SeverityHigh,
				title: "CRITICAL NHI credential stale — blocked (deny-closed)",
			})
		} else {
			rec[colNHIEnforce] = enforceAlert
			rec[colNHIEnforceWhy] = "stale credential (alert)"
			rec[colNHIBlockAfter] = model.NewTimestamp(now.Time().Add(blockGraceWindow)).String()
		}
		return changed, findings
	}

	// already stale and not yet blocked. The tier is re-derived live every sweep, so
	// a credential RECLASSIFIED to CRITICAL after it went stale must block
	// immediately ("block directo solo tier CRITICAL") — it cannot wait
	// out the grace window it inherited while it was non-critical. Otherwise escalate
	// alert → block at the 30-day deadline.
	if rec.String(colNHIEnforce) != enforceBlocked {
		switch {
		case crit == RiskTierCritical:
			rec[colNHIEnforce] = enforceBlocked
			rec[colNHIEnforceWhy] = "stale credential (critical — immediate block)"
			rec[colNHIBlockAfter] = now.String()
			changed = true
			findings = append(findings, pendingNHIFinding{
				kind: "nhi_credential_blocked", ref: ref, severity: sdkmodel.SeverityHigh,
				title: "CRITICAL NHI credential stale — blocked (deny-closed)",
			})
		case rec.String(colNHIEnforce) == enforceAlert:
			if ba, ok := tsValue(rec, colNHIBlockAfter); ok && !now.Before(ba) {
				rec[colNHIEnforce] = enforceBlocked
				rec[colNHIEnforceWhy] = "stale credential (escalated to block after 30 days)"
				changed = true
				findings = append(findings, pendingNHIFinding{
					kind: "nhi_credential_blocked", ref: ref, severity: sdkmodel.SeverityHigh,
					title: "Stale NHI credential escalated to block (30-day staleness, deny-closed)",
				})
			}
		}
	}
	return changed, findings
}

// checkSponsorship derives the orphan + unsponsored state from the roster. An NHI is
// orphaned when its sponsor (a roster human) is disabled or has vanished — a lifecycle
// trigger (the Entra sponsor-departure pattern). "Sponsor deleted from the directory"
// (vs disabled) is invisible on the upsert-only roster today; that live feed is.
func (m *Module) checkSponsorship(ctx context.Context, sc store.Scope, rec model.Record, ref string) (changed, orphaned, unsponsored bool, findings []pendingNHIFinding) {
	if rec.String(colNHIOffboard) == offboardFinal {
		return false, false, false, nil
	}
	owner := rec.String(colNHIOwnerRef)
	sponsor := rec.String(colNHISponsorRef)
	unsponsored = owner == "" || sponsor == ""
	if unsponsored {
		findings = append(findings, pendingNHIFinding{
			kind: "nhi_unsponsored", ref: ref, severity: sdkmodel.SeverityLow,
			title: "NHI has no human owner/sponsor — assign ownership (Entra Agent ID parity)",
		})
	}
	// an agent (kind=agent) with no sponsor_ref at all is both unsponsored
	// AND orphaned — it should never exist (the joiner rejects it), but the sweep
	// enforces defensively so any row that bypassed the joiner is still caught.
	if rec.String(colNHIKind) == NHIKindAgent && strings.TrimSpace(sponsor) == "" {
		if !rec.Bool(colNHIOrphaned) {
			rec[colNHIOrphaned] = true
			return true, true, true, append(findings, pendingNHIFinding{
				kind: "nhi_orphaned", ref: ref, severity: sdkmodel.SeverityHigh,
				title: "Agent NHI has no sponsor — orphaned (deny-closed; re-register with a human sponsor)",
			})
		}
		return false, true, true, findings
	}
	wasOrphan := rec.Bool(colNHIOrphaned)
	sponsorOrphan := false
	var sponsorLookupErr error
	if sponsor != "" {
		found, _, disabled, err := resolveHumanIdentity(ctx, sc, sponsor)
		if err != nil {
			sponsorLookupErr = err
			m.debugf("governance: nhi sponsor lookup failed", "identity_ref", ref, "sponsor_ref", sponsor, "err", err)
		} else {
			sponsorOrphan = !found || disabled
		}
	}
	// a federated agent registry's OWN orphan assertion (an Entra agent
	// identity whose blueprint is gone, written by the roster federation bridge)
	// is ORed in — kept in its own column precisely so this sponsor-liveness
	// recomputation can never clobber it. It clears only when the registry's
	// next sync stops asserting it.
	registryOrphan := rec.Bool(colNHIRegistryOrphan)
	if sponsorLookupErr != nil {
		// Fail closed: unknown sponsor liveness must never clear an existing orphan gate.
		orphaned = wasOrphan || registryOrphan
	} else {
		orphaned = sponsorOrphan || registryOrphan
	}
	if orphaned != wasOrphan {
		rec[colNHIOrphaned] = orphaned
		changed = true
		if orphaned {
			title := "NHI orphaned — its human sponsor is disabled/departed; review for offboarding"
			if registryOrphan && !sponsorOrphan {
				title = "NHI orphaned — its agent registry reports it orphaned (e.g. blueprint deleted); review for offboarding"
			}
			findings = append(findings, pendingNHIFinding{
				kind: "nhi_orphaned", ref: ref, severity: sdkmodel.SeverityHigh,
				title: title,
			})
		}
	}
	return changed, orphaned, unsponsored, findings
}

// recordStateEvents appends the append-only lifecycle events for the state findings a
// sweep materialized (the immutable trail of escalations). Attributed to the sweep
// system actor (no human principal drives a sweep transition).
func (m *Module) recordStateEvents(ctx context.Context, sc store.Scope, ref string, findings []pendingNHIFinding) {
	for _, f := range findings {
		var evt string
		switch f.kind {
		case "nhi_credential_stale":
			evt = "stale"
		case "nhi_credential_blocked":
			evt = "blocked"
		case "nhi_credential_unblocked":
			evt = "unblocked"
		case "nhi_orphaned":
			evt = "orphaned"
		case "nhi_unsponsored":
			evt = "unsponsored"
		default:
			continue
		}
		_ = m.recordLifecycleEvent(ctx, sc, ref, evt, "connector:"+Name, "", f.title)
	}
}

// isStalenessReason reports whether an enforcement reason came from the staleness
// policy (so a fresh rotation can clear it without clearing an offboard block).
func isStalenessReason(reason string) bool {
	return strings.HasPrefix(reason, "stale credential")
}

// --- posture -----------------------------------------------------------------

// nhiPostureDTO is the NHI lifecycle posture: rotation coverage and the
// staleness/enforcement/offboarding/ownership distribution. It is API-served (no
// module-level Prometheus seam exists yet — that would be a new engine decision).
type nhiPostureDTO struct {
	Total            int     `json:"total"`
	RotationKnown    int     `json:"rotation_known"`
	RotationCoverage float64 `json:"rotation_coverage"` // fraction with a known rotated_at
	Stale            int     `json:"stale"`
	Blocked          int     `json:"blocked"`
	Alerting         int     `json:"alerting"`
	Orphaned         int     `json:"orphaned"`
	Unsponsored      int     `json:"unsponsored"`
	Owned            int     `json:"owned"`
	SoftDeleted      int     `json:"soft_deleted"`
	Finalized        int     `json:"finalized"`
	Critical         int     `json:"critical"`
}

// handleNHIPosture aggregates the lifecycle posture. Read-tier, self-audited.
func (m *Module) handleNHIPosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var p nhiPostureDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "governance.nhi.posture", nhiLifecycleKind, "", nil); err != nil {
			return err
		}
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		rows, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		for _, rec := range rows {
			p.Total++
			if rec.String(colNHIRotatedAt) != "" {
				p.RotationKnown++
			}
			switch rec.String(colNHIStaleStatus) {
			case staleStale:
				p.Stale++
			}
			switch rec.String(colNHIEnforce) {
			case enforceBlocked:
				p.Blocked++
			case enforceAlert:
				p.Alerting++
			}
			if rec.Bool(colNHIOrphaned) {
				p.Orphaned++
			}
			if rec.String(colNHIOwnerRef) == "" || rec.String(colNHISponsorRef) == "" {
				p.Unsponsored++
			} else {
				p.Owned++
			}
			switch rec.String(colNHIOffboard) {
			case offboardSoft:
				p.SoftDeleted++
			case offboardFinal:
				p.Finalized++
			}
			if ActionRiskTier(rec.String(colNHICriticality)) == RiskTierCritical {
				p.Critical++
			}
		}
		if p.Total > 0 {
			p.RotationCoverage = float64(p.RotationKnown) / float64(p.Total)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- enforcement query (the PEP risk-conditional deny) -----------------------

// NHIEnforcement reports whether the NHI with the given external_id is currently
// blocked (stale-escalated, CRITICAL-stale, or offboarded) and a non-secret reason.
// It is the seam the deny-closed PEP consults: a blocked NHI denies every governed
// action by that identity (the offboarding cascade). Deny is materialized state, but
// the verdict is read live — never a stale snapshot. A missing row is NOT blocked
// (default-open for an unmanaged NHI keeps day-1 operations working).
func (m *Module) NHIEnforcement(ctx context.Context, tenant model.TenantID, identityRef string) (blocked bool, reason string, err error) {
	if m.data == nil || strings.TrimSpace(identityRef) == "" {
		return false, "", nil
	}
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(nhiLifecycleKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(ctx, repo, eq(colNHIIdentityRef, identityRef))
		if e != nil || !ok {
			return e
		}
		if rec.String(colNHIEnforce) == enforceBlocked {
			blocked = true
			reason = rec.String(colNHIEnforceWhy)
			if reason == "" {
				reason = "NHI blocked by lifecycle policy"
			}
		}
		return nil
	})
	return blocked, reason, err
}

// NHIEnforcementForAgentRef resolves an agent (by its external ref) to the NHI it is
// bound to and reports that NHI's enforcement. It is the hooks-PEP join: a tool-call
// by an agent whose bound NHI is blocked is denied. A firm attribution is the caller's
// responsibility (the agent ref hint is advisory until validated) — the method
// returns not-blocked when the agent or its binding cannot be resolved (default-open),
// so the PEP's own firm-identity gate remains the authority on attribution strength.
func (m *Module) NHIEnforcementForAgentRef(ctx context.Context, tenant model.TenantID, agentRef string) (blocked bool, reason string, err error) {
	if m.data == nil || strings.TrimSpace(agentRef) == "" {
		return false, "", nil
	}
	var identityRef string
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		agents, _, e := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", agentRef)}, Limit: 1})
		if e != nil || len(agents) == 0 {
			return e
		}
		a := agents[0]
		if a.IdentityID.IsZero() {
			return nil
		}
		id, e := sc.Identities().Get(ctx, a.IdentityID)
		if e != nil {
			if isNotFound(e) {
				return nil
			}
			return e
		}
		identityRef = id.ExternalID
		return nil
	})
	if err != nil || identityRef == "" {
		return false, "", err
	}
	return m.NHIEnforcement(ctx, tenant, identityRef)
}

// --- event-driven lifecycle trigger ------------------------------------------

// onLifecycleSignal reacts to an external identity-revocation finding (the CAEP
// session/credential-revoked signals the ssf connector emits, SubjectKind=identity,
// SubjectRef converging on the roster external_id). It is the event-driven half of
// lifecycle (deploy/pipeline/sunset is timer-only today; a hard external revoke is a
// real push signal): a revoked NHI is blocked, and a revoked SPONSOR orphans the NHIs
// they sponsor. It ignores everything else — including the module's own nhi_* findings
// — so it never loops. Fast and idempotent (state-change-guarded).
func (m *Module) onLifecycleSignal(ctx context.Context, e event.Event) {
	if m.data == nil {
		return
	}
	f, ok := event.FindingOf(e)
	if !ok {
		return
	}
	if f.Kind != "caep_credential_revoked" && f.Kind != "caep_session_revoked" {
		return
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return
	}
	subject := strings.TrimSpace(f.SubjectRef)
	if subject == "" {
		return
	}
	var emit []pendingNHIFinding
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		// (a) the subject IS a managed NHI → external revoke ⇒ block it.
		if rec, found, err := findOne(ctx, repo, eq(colNHIIdentityRef, subject)); err != nil {
			return err
		} else if found && rec.String(colNHIEnforce) != enforceBlocked && rec.String(colNHIOffboard) != offboardFinal {
			rec[colNHIEnforce] = enforceBlocked
			rec[colNHIEnforceWhy] = "external credential revocation (CAEP)"
			if _, err := repo.Update(ctx, rec); err != nil {
				if !isConflict(err) {
					return err
				}
			} else {
				_ = m.recordLifecycleEvent(ctx, sc, subject, "external_revoke", "connector:ssf", "", "CAEP "+f.Kind)
				emit = append(emit, pendingNHIFinding{kind: "nhi_external_revoke_blocked", ref: subject, severity: sdkmodel.SeverityHigh,
					title: "NHI blocked on external credential/session revocation (CAEP)"})
			}
		}
		// (b) the subject is a SPONSOR of managed NHIs → orphan them.
		sponsored, err := listAll(ctx, repo, eq(colNHISponsorRef, subject))
		if err != nil {
			return err
		}
		for _, rec := range sponsored {
			if rec.Bool(colNHIOrphaned) {
				continue
			}
			ref := rec.String(colNHIIdentityRef)
			rec[colNHIOrphaned] = true
			if _, err := repo.Update(ctx, rec); err != nil {
				if isConflict(err) {
					continue
				}
				return err
			}
			_ = m.recordLifecycleEvent(ctx, sc, ref, "orphaned", "connector:ssf", "", "sponsor "+subject+" revoked (CAEP)")
			emit = append(emit, pendingNHIFinding{kind: "nhi_orphaned", ref: ref, severity: sdkmodel.SeverityHigh,
				title: "NHI orphaned — its human sponsor's credential/session was revoked (CAEP)"})
		}
		return nil
	})
	if err != nil {
		m.debugf("governance: nhi lifecycle signal failed", "err", err)
		return
	}
	for _, f := range emit {
		m.emitNHIFinding(ctx, tenant, f.kind, f.ref, f.severity, f.title)
	}
}

// --- finding emission + small helpers ----------------------------------------

// emitNHIFinding publishes an NHI lifecycle finding on the bus (the notify module
// routes nhi_* kinds to the output connectors with zero new wiring). Minimal
// data: a fixed non-sensitive title, the identity ref on SubjectRef, a hashed detail.
func (m *Module) emitNHIFinding(ctx context.Context, tenant model.TenantID, kind, ref string, sev sdkmodel.Severity, title string) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte(kind + "|" + ref + "|" + title))
	finding := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: "identity",
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit nhi finding failed", "err", err)
	}
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// quoteOrDash renders a possibly-empty source name for an operator message.
func quoteOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown)"
	}
	return s
}

// gateHTTPStatus maps a non-allowed gate status to an HTTP status: pending is a 202
// (queued for human approval), an unconfigured gate is a 503 (the governance path is
// not wired), and a rejection/expiry is a 403/409.
func gateHTTPStatus(status string) int {
	switch status {
	case GateStatusPending:
		return http.StatusAccepted
	case GateStatusExpired:
		return http.StatusConflict
	case GateStatusNoGate:
		return http.StatusServiceUnavailable
	default: // rejected / unknown
		return http.StatusForbidden
	}
}

// parseFlexibleTimestamp parses either the canonical fixed-width form or plain
// RFC3339 (what an operator types), returning a normalized model.Timestamp.
func parseFlexibleTimestamp(s string) (model.Timestamp, bool) {
	if ts, err := model.ParseTimestamp(s); err == nil {
		return ts, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return model.NewTimestamp(t), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return model.NewTimestamp(t), true
	}
	return model.Timestamp{}, false
}
