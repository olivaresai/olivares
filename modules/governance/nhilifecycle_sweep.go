// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

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
