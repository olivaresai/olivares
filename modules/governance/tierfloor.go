// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// built-in tier floors: non-negotiable safety minimums per agent risk
// tier that apply BEFORE any operator-authored guardian rule. An operator can
// tighten via policy vocabulary but cannot relax below these floors.
//
// Floors:
//   - critical: ONE high+ finding for the agent → auto-stop (immediately).
//   - high: TWO high+ findings for the agent within 1 hour → auto-stop.
//   - medium/low/unclassified: no built-in floor.
//
// The floor fires the kill-switch with Source "tier_floor" so forensics
// distinguishes a floor-triggered stop from an operator-authored guardian stop.
// The floor check runs BEFORE the guardian rule loop in onGuardianFinding, so
// a floor stop fires even if no guardian rule matches.
//
// D-02 — DURABLE, CANONICAL, IDEMPOTENT counting. The high-tier floor's
// "two within a window" count is NOT an in-memory map (which was per-process,
// keyed on the raw finding ref, and lost on restart). Instead each qualifying
// finding records ONE row in governance_tier_floor_signal, keyed on the CANONICAL
// agent uuid (resolved within the tenant) and deduped by the finding fingerprint,
// and the count is the number of such rows inside the window. The whole flow —
// resolve → record → count → engage — runs in ONE transaction, so a concurrent
// duplicate cannot double-fire and the mandatory stop cannot be evaded by:
//   - referencing the agent under two different identifiers (UUID vs external id):
//     both resolve to the same canonical uuid, so they SUM;
//   - two tenants sharing an external id: the count is per-(tenant, canonical
//     uuid), so they never collide;
//   - a process restart between the two findings: the first signal is durable.

const (
	tierFloorHighWindow = time.Hour
	tierFloorHighCount  = 2
	// tierFloorCriticalCount is the critical-tier floor: a single high+ finding
	// stops the agent. Expressed as a threshold so the count path is uniform.
	tierFloorCriticalCount = 1
)

// checkTierFloor evaluates the built-in tier floor for a finding about an agent.
// If the floor is reached it engages the kill-switch (Source tier_floor). It is
// called BEFORE the guardian rule loop. Everything — resolving the agent to its
// canonical uuid, recording the durable signal, counting the window and claiming
// the kill-switch — happens inside ONE transaction (D-02).
func (m *Module) checkTierFloor(ctx context.Context, tenant model.TenantID, f sdkmodel.FindingReport) {
	if f.SubjectKind != "agent" || strings.TrimSpace(f.SubjectRef) == "" {
		return
	}
	if sevRank(string(f.Severity)) < sevRank(string(sdkmodel.SeverityHigh)) {
		return
	}
	if m.data == nil {
		return
	}

	agentRef := strings.TrimSpace(f.SubjectRef)
	fingerprint := guardianFindingRef(f)
	now := m.clock.Now()

	var (
		tier     string
		emitKind string // set to the created kill-switch id when a NEW stop fired
	)
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		// Resolve the finding's subject to the CANONICAL agent within the tenant.
		// An unresolvable agent means no floor applies (an agent that never reached
		// the inventory has no risk tier to floor) — the guardian rule loop and the
		// kill-switch can still stop it by the raw ref if an operator rule matches.
		agent, aerr := resolveAgent(ctx, sc, agentRef)
		if aerr != nil {
			if isNotFound(aerr) {
				return nil
			}
			return aerr
		}
		tier = agent.RiskTier
		canonicalAgentID := agent.ID.String()

		threshold := 0
		switch ActionRiskTier(tier) {
		case RiskTierCritical:
			threshold = tierFloorCriticalCount
		case RiskTierHigh:
			threshold = tierFloorHighCount
		default:
			return nil // medium / low / unclassified: no built-in floor
		}

		// Record the signal durably (idempotent on the fingerprint) and count the
		// distinct signals for THIS canonical agent inside the window.
		count, cerr := m.recordAndCountTierFloorSignal(ctx, sc, canonicalAgentID, fingerprint, f, now)
		if cerr != nil {
			return cerr
		}
		if count < threshold {
			return nil
		}

		reason := "tier floor (" + tier + "): " + f.Kind + " (" + string(f.Severity) + ")"
		if len(reason) > maxNoteLen {
			reason = reason[:maxNoteLen]
		}
		out, eerr := m.engageKillSwitchLocked(ctx, sc, ksEngageParams{
			ScopeKind: ksScopeAgent, ScopeRef: agentRef, Reason: reason,
			Source: ksSourceTierFloor,
			Actor:  model.ActorSystem, ActorKind: model.ActorSystem,
		}, now)
		if eerr != nil {
			return eerr
		}
		if !out.AlreadyActive {
			emitKind = out.Record.String(model.ColID)
		}
		return nil
	})
	if err != nil {
		if isConflict(err) {
			return // a concurrent duplicate won the signal/engage race; converged
		}
		m.debugf("governance: tier floor evaluation failed", "err", err, "agent", agentRef)
		return
	}
	if emitKind != "" {
		m.emitKillSwitchFinding(ctx, tenant, findingKillSwitchEngaged, emitKind,
			ksScopeAgent, sdkmodel.SeverityCritical,
			"Tier floor AUTO-STOP — agent '"+agentRef+"' stopped (risk tier "+tier+"); re-enable requires dual-control")
	}
}

// recordAndCountTierFloorSignal records one durable tier-floor signal for the
// canonical agent (idempotent on the finding fingerprint) and returns the number
// of distinct signals inside the high-tier window. Expired signals for this agent
// are pruned opportunistically in the same transaction — the durable analog of
// the old in-memory eviction (pruned rows are already outside the window, so the
// prune never changes the count). Runs inside the caller's transaction.
func (m *Module) recordAndCountTierFloorSignal(ctx context.Context, sc store.Scope, agentID, fingerprint string, f sdkmodel.FindingReport, now model.Timestamp) (int, error) {
	repo, err := sc.Ext(tierFloorSignalKind)
	if err != nil {
		return 0, err
	}

	// Idempotent record: one signal per (agent, fingerprint). A re-delivered
	// finding finds its row already present and does not add a second — the
	// unique (tenant_id, agent_id, fingerprint) index backstops the read race.
	_, dup, err := findOne(ctx, repo, eq(colTFSAgentID, agentID), eq(colTFSFingerprint, fingerprint))
	if err != nil {
		return 0, err
	}
	if !dup {
		if _, cerr := repo.Create(ctx, model.Record{
			colTFSAgentID:     agentID,
			colTFSFingerprint: fingerprint,
			colTFSSeverity:    string(f.Severity),
			colTFSFindingKind: f.Kind,
			colTFSObservedAt:  now.String(),
		}); cerr != nil {
			return 0, cerr // unique-index race → conflict → caller converges
		}
	}

	// Count the distinct signals inside the window; prune the rest.
	cutoff := model.NewTimestamp(now.Time().Add(-tierFloorHighWindow))
	rows, err := listAll(ctx, repo, eq(colTFSAgentID, agentID))
	if err != nil {
		return 0, err
	}
	count := 0
	for _, r := range rows {
		ts, ok := tsValue(r, colTFSObservedAt)
		if ok && cutoff.Before(ts) { // strictly within (cutoff, now]
			count++
			continue
		}
		// Expired (or unparseable): prune. Outside the window, so pruning it never
		// changes the count. A concurrent delete/absence is fine.
		if derr := repo.Delete(ctx, model.ID(r.String(model.ColID))); derr != nil && !isConflict(derr) && !isNotFound(derr) {
			return 0, derr
		}
	}
	return count, nil
}
