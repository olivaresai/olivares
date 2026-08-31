// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// E3d — the observe PROMOTION report. It reads a tenant's tamper-evident ledger and
// aggregates the constrained-observe SHADOWS ("what would enforce have denied/asked?"), so an
// operator can promote a tenant from observe→enforce with evidence, not a guess. It reads the
// STORED CANONICAL meta (Walk discards Meta — scanAudit nils it; WalkCanonical yields the
// authoritative string), runs a chain Verify first and flags any break/declared-gap as an
// INCOMPLETE report (never a silent zero), and separates calls that PROCEEDED (effective allow)
// from those that fail-closed to a DENY (effective_downgrade) — the two mean opposite things.

// observeReportEvent is one parsed ledger event fed to the pure aggregator.
type observeReportEvent struct {
	seq  int64
	meta map[string]any
}

type observeReportIntegrity struct {
	Verified     bool   `json:"verified"`      // the hash chain over [from, head] is intact
	Complete     bool   `json:"complete"`      // no sanctioned (declared) evidence gaps were crossed
	Checked      int64  `json:"checked"`       // events structurally verified
	DeclaredGaps int64  `json:"declared_gaps"` // marker-declared holes (evidence deliberately dropped)
	BreakAt      int64  `json:"break_at,omitempty"`
	Note         string `json:"note,omitempty"`
}

type observeReportGrant struct {
	GrantID    string         `json:"observe_grant_id"`
	Total      int            `json:"total"`
	Proceeded  int            `json:"proceeded_allow"` // effective ALLOW: the shadowed call actually RAN
	Downgraded int            `json:"downgraded_deny"` // fail-closed to deny (no evidence): the call did NOT run
	BySource   map[string]int `json:"by_source"`       // shadow_source -> count
	ByDecision map[string]int `json:"by_decision"`     // shadowed_decision -> count
}

type observeReport struct {
	Tenant     string                 `json:"tenant"`
	FromSeq    int64                  `json:"from_seq"`
	Integrity  observeReportIntegrity `json:"integrity"`
	Total      int                    `json:"total_would_have_denied_or_asked"`
	Proceeded  int                    `json:"proceeded_allow"`
	Downgraded int                    `json:"downgraded_deny"`
	Malformed  int                    `json:"malformed_meta_skipped"`
	Grants     []observeReportGrant   `json:"grants"`
}

// validShadowSources / validShadowedDecisions gate the enum axes; an unrecognized value is
// surfaced (counted malformed / bucketed "unknown"), never silently folded into a known bucket.
var validShadowSources = map[string]bool{
	shadowSourcePDP: true, shadowSourceScoped: true, shadowSourceLocalRule: true,
	shadowSourceLocalDefault: true, shadowSourceBashPath: true,
}
var validShadowedDecisions = map[string]bool{claude.DecisionDeny: true, claude.DecisionAsk: true}

// buildObserveReport is the PURE aggregator (no store), so it is exhaustively unit-testable.
// It dedupes an ambiguous-commit double-write by decision_attempt_id (an ALLOW + a downgraded
// DENY sharing one id is ONE decision whose effective terminal is the DENY), then groups the
// logical decisions by observe_grant_id and buckets them by source and would-be decision.
func buildObserveReport(events []observeReportEvent) observeReport {
	rep := observeReport{}

	// 1. Collapse events into logical decisions keyed by decision_attempt_id. Events with no id
	//    (pre-E3 or a non-shadow) are their own decision, keyed by a unique per-seq sentinel.
	type logical struct {
		grantID    string
		source     string
		decision   string
		downgraded bool // effective was a fail-closed DENY (the call did NOT run)
		malformed  bool
	}
	byAttempt := map[string]*logical{}
	order := []string{}
	for _, e := range events {
		if observeMetaStr(e.meta[metaEnforcementMode]) != enforcementModeObserve {
			continue // not an observe shadow
		}
		key := observeMetaStr(e.meta[metaDecisionAttemptID])
		if key == "" {
			key = fmt.Sprintf("__seq_%d", e.seq) // no correlation id ⇒ standalone decision
		}
		lg, ok := byAttempt[key]
		if !ok {
			lg = &logical{}
			byAttempt[key] = lg
			order = append(order, key)
		}
		shDec := observeMetaStr(e.meta[metaShadowedDecision])
		if !validShadowedDecisions[shDec] {
			lg.malformed = true // an observe event with no valid would-be decision cannot be classified
			continue
		}
		// The effective terminal wins across a double-write: a downgraded DENY supersedes an ALLOW.
		effDowngrade := e.meta[metaEffectiveDowngrade] == true
		if lg.decision == "" || effDowngrade {
			lg.grantID = observeMetaStr(e.meta[metaObserveGrantID])
			lg.source = observeMetaStr(e.meta[metaShadowSource])
			lg.decision = shDec
		}
		if effDowngrade {
			lg.downgraded = true
		}
	}

	// 2. Aggregate logical decisions, grouped by grant id.
	grants := map[string]*observeReportGrant{}
	grantOrder := []string{}
	for _, key := range order {
		lg := byAttempt[key]
		// ANY malformed row for this attempt id taints the whole logical decision: a corrupt
		// sibling could be the DOWNGRADE (effective deny) of an otherwise-valid allow, so counting
		// the valid row as "proceeded" would understate enforcement impact — the unsafe direction
		// for a promotion gate. Flag it Malformed (⇒ the report is INCOMPLETE) rather than guess.
		if lg.malformed {
			rep.Malformed++
			continue
		}
		if lg.decision == "" {
			continue // an attempt id seen only on rows with no valid verdict (already counted above)
		}
		g, ok := grants[lg.grantID]
		if !ok {
			g = &observeReportGrant{GrantID: lg.grantID, BySource: map[string]int{}, ByDecision: map[string]int{}}
			grants[lg.grantID] = g
			grantOrder = append(grantOrder, lg.grantID)
		}
		src := lg.source
		if !validShadowSources[src] {
			src = "unknown" // surfaced as its own bucket, never merged into a known source
		}
		g.Total++
		g.BySource[src]++
		g.ByDecision[lg.decision]++
		rep.Total++
		if lg.downgraded {
			g.Downgraded++
			rep.Downgraded++
		} else {
			g.Proceeded++
			rep.Proceeded++
		}
	}
	for _, gid := range grantOrder {
		rep.Grants = append(rep.Grants, *grants[gid])
	}
	return rep
}

func observeMetaStr(v any) string {
	s, _ := v.(string)
	return s
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func auditObserveReportCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant string
	var fromSeq int64
	var asJSON, strict bool
	cmd := &cobra.Command{
		Use:   "observe-report",
		Short: "Summarize constrained-observe shadows for an observe→enforce promotion decision",
		Long: "observe-report reads a tenant's tamper-evident ledger and aggregates the constrained-observe " +
			"shadows — the would-be deny/ask verdicts that observe mode ALLOWED but recorded — grouped by observe " +
			"grant, source and would-be decision, and separates calls that PROCEEDED from those that fail-closed to " +
			"a deny.\n\n" +
			"INTEGRITY: this runs the STRUCTURAL chain verification (hash linkage + declared gaps) and flags the " +
			"report INCOMPLETE on any break, declared gap or malformed row. It does NOT verify per-event Ed25519 " +
			"signatures or checkpoints — run `olivares audit verify --strict` (with your pinned keys) for that deeper, " +
			"tamper-resistant check BEFORE acting on this report. A clean result means 'no shadows in the verified " +
			"range'; confirm the grant was actually active over representative traffic before promoting to enforce.",
		Example: `  # Human summary for a tenant (default $OLIVARES_TENANT)
  olivares audit observe-report --tenant t_abc123

  # Machine-readable JSON from a Postgres store
  olivares audit observe-report --tenant t_abc123 --json --engine postgres --dsn "env:DATABASE_URL"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolved)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			if fromSeq < 1 {
				fromSeq = 1
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			var rep observeReport
			err = eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				// Snapshot the head FIRST so Verify and the aggregation walk share a fixed upper
				// bound: an append that lands BETWEEN Verify and WalkCanonical is then ignored, never
				// aggregated without having been verified (a TOCTOU otherwise possible under Postgres
				// read-committed). An empty chain ⇒ head.Seq==0 ⇒ no events.
				head, _, herr := sc.Audit().Head(cmd.Context())
				if herr != nil {
					return fmt.Errorf("read ledger head: %w", herr)
				}
				// Integrity: a broken chain, a declared gap, OR any malformed row means the aggregate
				// below is NOT a complete census — say so loudly rather than present a confident count.
				// NOTE: this is the STRUCTURAL chain verification (hashes + declared gaps); it does NOT
				// verify per-event Ed25519 signatures or checkpoints — run `olivares audit verify` for
				// that deeper, key-pinned integrity check before a promotion (documented in --help).
				vr, verr := sc.Audit().Verify(cmd.Context(), fromSeq)
				if verr != nil {
					return fmt.Errorf("verify ledger: %w", verr)
				}
				integ := observeReportIntegrity{
					Verified: vr.OK, Complete: vr.OK && vr.DeclaredGaps == 0,
					Checked: vr.Checked, DeclaredGaps: vr.DeclaredGaps, BreakAt: vr.BreakAt,
				}
				if !vr.OK {
					integ.Note = "chain verification FAILED (" + vr.Reason + "); the report is INCOMPLETE — evidence may be missing or tampered"
				} else if vr.DeclaredGaps > 0 {
					integ.Note = "the chain crosses sanctioned declared gaps; some observe evidence was deliberately dropped — the report is INCOMPLETE"
				}

				walker, ok := sc.Audit().(store.CanonicalWalker)
				if !ok {
					return fmt.Errorf("this ledger does not expose canonical metadata; cannot build an observe report")
				}
				var events []observeReportEvent
				malformedJSON := 0
				if werr := walker.WalkCanonical(cmd.Context(), fromSeq, func(ev model.AuditEvent, canonical string, _ []byte) error {
					if ev.Seq > head.Seq {
						return nil // beyond the verified snapshot — do not aggregate an unverified append (an empty chain has head.Seq==0 ⇒ nothing aggregates)
					}
					var meta map[string]any
					if jerr := json.Unmarshal([]byte(canonical), &meta); jerr != nil {
						malformedJSON++ // a row whose canonical meta will not parse is surfaced, never skipped silently
						return nil
					}
					events = append(events, observeReportEvent{seq: ev.Seq, meta: meta})
					return nil
				}); werr != nil {
					return werr
				}
				agg := buildObserveReport(events)
				agg.Tenant, agg.FromSeq = t.String(), fromSeq
				agg.Malformed += malformedJSON
				// Any malformed/unclassifiable row means the census is not authoritative ⇒ INCOMPLETE.
				if agg.Malformed > 0 {
					integ.Complete = false
					if integ.Note == "" {
						integ.Note = "the report includes malformed/unclassifiable observe rows; treat it as INCOMPLETE"
					}
				}
				agg.Integrity = integ
				rep = agg
				return nil
			})
			if err != nil {
				return err
			}
			// E2 (sol-max contrast): the local --json was the ONLY way to get
			// the machine form; a global `-o json` printed the human report.
			// outputExplicitlySelected + selectedOutput is the same answer the rest
			// of the CLI gives.
			wantJSON := asJSON
			if !wantJSON && outputExplicitlySelected(cmd) {
				if format, ferr := selectedOutput(cmd); ferr == nil && format == "json" {
					wantJSON = true
				}
			}
			if werr := writeObserveReport(cmd, rep, wantJSON); werr != nil {
				return werr
			}
			if strict && !rep.Integrity.Complete {
				// A promotion gate must FAIL on an incomplete census (non-zero exit for CI).
				return fmt.Errorf("observe report is INCOMPLETE (--strict): %s", rep.Integrity.Note)
			}
			return nil
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id to report on (default $OLIVARES_TENANT)")
	cmd.Flags().Int64Var(&fromSeq, "from", 1, "first ledger sequence to include (a recovered epoch begins at its recover_seq)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON instead of a human summary")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if the report is INCOMPLETE (chain break, declared gap, or malformed rows) — use to gate an observe→enforce promotion in CI")
	return cmd
}

func writeObserveReport(cmd *cobra.Command, rep observeReport, asJSON bool) error {
	out := cmd.OutOrStdout()
	if asJSON {
		// E2: through the shared renderer, so this report's JSON is shaped
		// and indented like every other command's.
		return renderReportOut(cmd, rep)
	}
	fmt.Fprintf(out, "Observe promotion report — tenant %s (from seq %d)\n", rep.Tenant, rep.FromSeq)
	if !rep.Integrity.Verified || !rep.Integrity.Complete {
		fmt.Fprintf(out, "  ⚠ INCOMPLETE: %s\n", rep.Integrity.Note)
	} else {
		// Be precise: this is the STRUCTURAL chain check, not signature verification.
		fmt.Fprintf(out, "  integrity: chain-verified structurally, complete (%d events; run `audit verify` for signatures)\n", rep.Integrity.Checked)
	}
	fmt.Fprintf(out, "  would-have-denied/asked: %d  (proceeded: %d, fail-closed downgrade: %d)\n", rep.Total, rep.Proceeded, rep.Downgraded)
	if rep.Malformed > 0 {
		fmt.Fprintf(out, "  malformed/unclassifiable observe rows skipped: %d\n", rep.Malformed)
	}
	if rep.Total == 0 {
		if rep.Integrity.Verified && rep.Integrity.Complete {
			fmt.Fprintln(out, "  no observe shadows in range — nothing to promote (or observe never ran).")
		} else {
			fmt.Fprintln(out, "  no observe shadows COUNTED, but the report is INCOMPLETE (see warning) — do NOT read this as a clean 'nothing to promote'.")
		}
		return nil
	}
	for _, g := range rep.Grants {
		gid := g.GrantID
		if gid == "" {
			gid = "(unattributed)"
		}
		fmt.Fprintf(out, "  grant %s: %d (proceeded %d, downgraded %d)\n", gid, g.Total, g.Proceeded, g.Downgraded)
		for _, src := range sortedKeys(g.BySource) {
			fmt.Fprintf(out, "      source=%-14s %d\n", src, g.BySource[src])
		}
		for _, dec := range sortedKeys(g.ByDecision) {
			fmt.Fprintf(out, "      would-be=%-10s %d\n", dec, g.ByDecision[dec])
		}
	}
	fmt.Fprintln(out, "  Promote to enforce only after reviewing the above would-be verdicts.")
	return nil
}
