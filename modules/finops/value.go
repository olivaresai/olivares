// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// value.go is the VALUE-ATTRIBUTION half of FinOps 2.0: it crosses spend with
// outcomes so a CFO sees cost-per-outcome and a cancellation-risk signal (burn without
// successful outcomes — the Gartner ">40% of agentic projects canceled by 2027 over
// unclear cost/value" early warning). It is built on two honest facts verified against
// the tree:
//
//   - outcome verdicts are NOT queryable today (they ride a ≤Medium FindingReport
//     that modules/security drops, and the session ref is hashed away), and there is no
//     orchestration task entity. So outcomes reach FinOps via the SAME operator/
//     automation bridge as the seat counts (the producing connector cannot reach
//     module HTTP), persisted into a finops-owned read-model that DOES keep the join
//     key. The $-VALUE of an outcome is a business input the operator supplies; the
//     plane never fabricates it (0 = not reported).
//   - The join key spend↔outcome that survives to a store is the resolved subject ref
//     (session→agent→identity). Both sides stamp agent_ref/identity_ref/session_ref at
//     ingest, so the cross is a column match, not a per-query re-resolution.
//
// Where core EvalResult rows exist for an agent (module XII), the per-agent eval
// pass-rate enriches the agent view as a second, real outcome signal.

// Outcome subject kinds — the join axis an outcome is attributed on.
const (
	outcomeSubjectSession  = "session"
	outcomeSubjectAgent    = "agent"
	outcomeSubjectIdentity = "identity"
)

var validOutcomeSubjects = map[string]bool{
	outcomeSubjectSession: true, outcomeSubjectAgent: true, outcomeSubjectIdentity: true,
}

// outcomeVerdictSatisfied is the only SUCCESSFUL terminal verdict (mirrors the CMA
// grader vocabulary: failed|max_iterations_reached|interrupted are unsuccessful
// terminals). Other strings are accepted verbatim (forward-compatible) and treated as
// unsuccessful unless explicitly satisfied — never guessed as success.
const outcomeVerdictSatisfied = "satisfied"

func isSatisfied(verdict string) bool { return verdict == outcomeVerdictSatisfied }

// costTypeFallbackAttempt is the CostType the claude-api connector tags onto a billed
// mid-stream fallback decline (connectors/claude-api/forensic.go). It is real
// estimated burn (it counts toward spend and budgets), but it is CREDITABLE — Anthropic
// may credit it, and the credit settles only in the billed cost_report reconciliation
// stream. So the value panels surface it as a distinct line: fallback credit neither
// inflates the burn (we count actual estimated spend) nor hides it (we break it out).
const costTypeFallbackAttempt = "fallback_attempt"

// validValueDimensions are the join axes the value views slice on. All three exist on
// both the cost read-model and the outcome read-model as resolved refs.
var validValueDimensions = map[string]bool{
	outcomeSubjectAgent: true, outcomeSubjectIdentity: true, outcomeSubjectSession: true,
}

const defaultValueDimension = outcomeSubjectAgent

// valueDimensionColumn maps a value dimension to the read-model column shared by the
// cost_sample and outcome tables (so cost and outcomes join on the same key).
func valueDimensionColumn(dim string) string {
	switch dim {
	case outcomeSubjectAgent:
		return colAgentRef
	case outcomeSubjectIdentity:
		return colIdentityRef
	case outcomeSubjectSession:
		return colSessionRef
	}
	return ""
}

// --- outcome ingest ----------------------------------------------------------

// outcomeIngestRequest is the snake_case DTO for one graded outcome posted over HTTP
// (POST /v1/m/finops/outcomes) — the sanctioned bridge for outcome data a connector
// cannot push directly, mirroring the seat ingest (seats.go). value_micro_usd is the
// OPERATOR-supplied business value (integer micro-USD, never a float; 0/absent = not
// reported, never inferred).
type outcomeIngestRequest struct {
	SubjectKind   string    `json:"subject_kind"` // session|agent|identity
	SubjectRef    string    `json:"subject_ref"`
	OutcomeRef    string    `json:"outcome_ref,omitempty"` // the outcome/task id (e.g. outc_…)
	Verdict       string    `json:"verdict"`
	ValueMicroUSD int64     `json:"value_micro_usd,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
	Source        string    `json:"source,omitempty"` // cma|operator|eval|…
}

// validate returns a human error for a malformed outcome ("" = valid).
func (in outcomeIngestRequest) validate() string {
	if !validOutcomeSubjects[in.SubjectKind] {
		return "subject_kind must be one of session, agent, identity"
	}
	if in.SubjectRef == "" {
		return "subject_ref is required"
	}
	if in.Verdict == "" {
		return "verdict is required"
	}
	if in.ValueMicroUSD < 0 {
		return "value_micro_usd must not be negative"
	}
	// Idempotency: with no outcome_ref the dedup key falls back to the instant, so a
	// server-minted clock would make a retried POST a NEW row (double-counting value).
	// Require a caller-controlled occurred_at in that case so retries collapse.
	if in.OutcomeRef == "" && in.OccurredAt.IsZero() {
		return "occurred_at is required when outcome_ref is absent (so a retried post stays idempotent)"
	}
	return ""
}

// outcomeRefs is the resolved join keys for an outcome's subject.
type outcomeRefs struct {
	agentRef, identityRef, sessionRef string
}

// ingestOutcome upserts one graded outcome, resolving and stamping its join keys
// (subject → session/agent/identity) so the cost cross is a later column match. Dedup
// is by natural key: a re-posted outcome (same source+subject+outcome_ref) REPLACES its
// row (verdict/value can revise) rather than double-counting — the seat/cost upsert
// spirit. The principal's privileged write is audited atomically with its effect.
func (m *Module) ingestOutcome(ctx context.Context, tenant model.TenantID, in outcomeIngestRequest, audit auditHook) error {
	at := in.OccurredAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if audit != nil {
			if err := audit(ctx, sc); err != nil {
				return err
			}
		}
		refs, err := resolveOutcomeSubject(ctx, sc, in.SubjectKind, in.SubjectRef)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(outcomeKind)
		if err != nil {
			return err
		}
		nk := outcomeKey(in, at)
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colOutcomeKey, nk)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			rec := rows[0]
			rec[colOutcomeVerdict] = in.Verdict
			rec[colOutcomeValue] = in.ValueMicroUSD
			// Re-stamp the DERIVED join keys only when they re-resolve non-empty — a
			// re-post never blanks a prior attribution (mirrors applySampleValues'
			// identity guard); the subject itself is fixed (it is in the natural key).
			if refs.agentRef != "" {
				rec[colAgentRef] = refs.agentRef
			}
			if refs.identityRef != "" {
				rec[colIdentityRef] = refs.identityRef
			}
			if refs.sessionRef != "" {
				rec[colSessionRef] = refs.sessionRef
			}
			_, err = repo.Update(ctx, rec)
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			colOutcomeKey:         nk,
			colOutcomeSubjectKind: in.SubjectKind,
			colOutcomeSubjectRef:  in.SubjectRef,
			colOutcomeRef:         in.OutcomeRef,
			colOutcomeVerdict:     in.Verdict,
			colOutcomeValue:       in.ValueMicroUSD,
			colOccurredAt:         model.NewTimestamp(at).String(),
			colOutcomeSource:      in.Source,
			colAgentRef:           refs.agentRef,
			colIdentityRef:        refs.identityRef,
			colSessionRef:         refs.sessionRef,
		})
		if errors.Is(err, store.ErrConflict) {
			return nil // raced with a concurrent insert of the same outcome
		}
		return err
	})
}

// outcomeKey is the dedup key for an outcome. When an outcome_ref is present (e.g. a
// CMA outc_…), the key is source+subject+outcome_ref WITHOUT the instant, so a
// re-graded outcome collapses onto the same row (verdict/value revise in place). With
// no outcome_ref, the instant disambiguates distinct events.
func outcomeKey(in outcomeIngestRequest, at time.Time) string {
	parts := []string{in.Source, in.SubjectKind, in.SubjectRef, in.OutcomeRef}
	if in.OutcomeRef == "" {
		parts = append(parts, at.UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// resolveOutcomeSubject resolves an outcome's subject to the join keys the cost stream
// also carries: a session resolves through its agent to a firm identity; an agent
// resolves to its firm identity; an identity subject is itself the identity ref. It is
// best-effort attribution (an unresolved subject keeps only the refs it has — honest,
// never fabricated) but it propagates genuine store errors, only swallowing not-found.
func resolveOutcomeSubject(ctx context.Context, sc store.Scope, kind, ref string) (outcomeRefs, error) {
	var out outcomeRefs
	switch kind {
	case outcomeSubjectIdentity:
		out.identityRef = ref
		return out, nil
	case outcomeSubjectSession:
		out.sessionRef = ref
		s, ok, err := findOne(ctx, sc.Sessions(), eq("external_id", ref))
		if err != nil || !ok {
			return out, err
		}
		if s.AgentID.IsZero() {
			return out, nil
		}
		return agentRefs(ctx, sc, s.AgentID, out)
	case outcomeSubjectAgent:
		out.agentRef = ref
		id, ok, err := resolveAgentID(ctx, sc, ref)
		if err != nil || !ok {
			return out, err
		}
		return agentRefs(ctx, sc, id, out)
	}
	return out, nil
}

// agentRefs fills the agent's normalized ref and firm identity ref into out.
func agentRefs(ctx context.Context, sc store.Scope, agentID model.ID, out outcomeRefs) (outcomeRefs, error) {
	a, err := sc.Agents().Get(ctx, agentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		return out, err
	}
	out.agentRef = a.ExternalID
	if out.agentRef == "" {
		out.agentRef = a.Name
	}
	if !a.IdentityID.IsZero() {
		i, err := sc.Identities().Get(ctx, a.IdentityID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return out, err
		}
		if err == nil {
			out.identityRef = i.ExternalID
		}
	}
	return out, nil
}

// --- cost-per-outcome / value analytics --------------------------------------

// costAgg is a per-key spend aggregate for the value join: total cost plus the
// creditable (fallback_attempt) breakout.
type costAgg struct {
	cost       int64
	creditable int64
}

// outcomeAgg is a per-key outcome aggregate: business value plus the verdict counts.
type outcomeAgg struct {
	value       int64
	outcomes    int
	satisfied   int
	unsatisfied int
}

// scanOutcomes iterates the outcome read-model rows matching filters, paging by the
// default keyset cursor, calling fn for each; truncated=true if it hit the page cap.
func scanOutcomes(ctx context.Context, sc store.Scope, filters []model.Filter, fn func(model.Record)) (bool, error) {
	repo, err := sc.Ext(outcomeKind)
	if err != nil {
		return false, err
	}
	q := model.Query{Filters: filters, Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return false, err
		}
		for _, r := range recs {
			fn(r)
		}
		if !page.HasMore {
			return false, nil
		}
		if pages+1 >= maxScanPages {
			return true, nil
		}
		q.Cursor = page.Cursor
	}
}

// aggregateCostByKey sums the estimated cost (and the creditable fallback_attempt
// portion) by the value dimension column over the window. Rows with an empty key are
// kept under "" (unattributed) — honest, not dropped.
func aggregateCostByKey(ctx context.Context, sc store.Scope, col string, since time.Time, hasSince bool, until time.Time, hasUntil bool) (map[string]*costAgg, bool, error) {
	out := map[string]*costAgg{}
	trunc, err := scanSamples(ctx, sc, windowFilters(nil, since, hasSince, until, hasUntil), func(r model.Record) {
		key := r.String(col)
		a := out[key]
		if a == nil {
			a = &costAgg{}
			out[key] = a
		}
		c := r.Int(colCostMicroUSD)
		a.cost += c
		if r.String(colCostType) == costTypeFallbackAttempt {
			a.creditable += c
		}
	})
	return out, trunc, err
}

// aggregateOutcomesByKey sums outcome value + verdict counts by the same column.
func aggregateOutcomesByKey(ctx context.Context, sc store.Scope, col string, since time.Time, hasSince bool, until time.Time, hasUntil bool) (map[string]*outcomeAgg, bool, error) {
	out := map[string]*outcomeAgg{}
	var filters []model.Filter
	if hasSince {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(since).String()})
	}
	if hasUntil {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpLte, Value: model.NewTimestamp(until).String()})
	}
	trunc, err := scanOutcomes(ctx, sc, filters, func(r model.Record) {
		key := r.String(col)
		a := out[key]
		if a == nil {
			a = &outcomeAgg{}
			out[key] = a
		}
		a.value += r.Int(colOutcomeValue)
		a.outcomes++
		if isSatisfied(r.String(colOutcomeVerdict)) {
			a.satisfied++
		} else {
			a.unsatisfied++
		}
	})
	return out, trunc, err
}

// costPerOutcome joins spend with outcomes by the requested dimension, returning the
// per-key cost-per-outcome view and a cancellation-risk flag for each. minCostMicroUSD
// floors which buckets qualify for the cancellation-risk flag (a CFO-tunable "material
// burn" threshold; 0 flags any positive no-outcome spend).
func (m *Module) costPerOutcome(ctx context.Context, sc store.Scope, dim string, since time.Time, hasSince bool, until time.Time, hasUntil bool, minCostMicroUSD int64) (valueResponse, error) {
	col := valueDimensionColumn(dim)
	costs, tc, err := aggregateCostByKey(ctx, sc, col, since, hasSince, until, hasUntil)
	if err != nil {
		return valueResponse{}, err
	}
	outcomes, to, err := aggregateOutcomesByKey(ctx, sc, col, since, hasSince, until, hasUntil)
	if err != nil {
		return valueResponse{}, err
	}
	out := valueResponse{Dimension: dim, Truncated: tc || to}

	// Union of keys present on either side (skip the unattributed "" bucket — it is not
	// a subject and would conflate unrelated spend/outcomes).
	keys := map[string]bool{}
	for k := range costs {
		if k != "" {
			keys[k] = true
		}
	}
	for k := range outcomes {
		if k != "" {
			keys[k] = true
		}
	}

	// EvalResult enrichment (agent dimension only): per-agent pass-rate where present,
	// indexed by the agent's external ref so the per-bucket lookup is O(1) (no N+1).
	var evalByRef map[string]evalRate
	if dim == outcomeSubjectAgent {
		evalByRef, err = evalRateByAgentRef(ctx, sc)
		if err != nil {
			return valueResponse{}, err
		}
	}

	for k := range keys {
		c := costs[k]
		o := outcomes[k]
		b := valueBucketDTO{Key: k}
		if c != nil {
			b.CostMicroUSD = c.cost
			b.CreditableMicroUSD = c.creditable
		}
		if o != nil {
			b.ValueMicroUSD = o.value
			b.Outcomes = o.outcomes
			b.Satisfied = o.satisfied
			b.Unsatisfied = o.unsatisfied
			b.HasOutcomes = o.outcomes > 0
		}
		b.NetValueMicroUSD = b.ValueMicroUSD - b.CostMicroUSD
		if b.Outcomes > 0 {
			b.CostPerOutcomeMicroUSD = b.CostMicroUSD / int64(b.Outcomes)
			b.SatisfiedRatePct = int(float64(b.Satisfied) / float64(b.Outcomes) * 100)
		}
		if b.Satisfied > 0 {
			b.CostPerSatisfiedMicroUSD = b.CostMicroUSD / int64(b.Satisfied)
		}
		if reason := cancellationRiskReason(b.CostMicroUSD, b.Outcomes, b.Satisfied, minCostMicroUSD); reason != "" {
			b.CancellationRisk = true
			b.RiskReason = reason
		}
		if r, ok := evalByRef[k]; ok {
			pct := r.passRatePct()
			b.EvalPassRatePct = &pct
		}
		out.TotalValueMicroUSD += b.ValueMicroUSD
		out.TotalOutcomes += b.Outcomes
		out.Buckets = append(out.Buckets, b)
	}
	// total_cost is ALL estimated spend in the window (the true CFO denominator),
	// INCLUDING the unattributed remainder — for the agent/session dimensions most
	// spend carries no subject (the cost stream attributes none; ingest.go note),
	// so silently dropping the "" bucket would make total_cost and net_value wrong.
	// Buckets are the attributed subjects only; unattributed is surfaced distinctly so
	// sum(buckets.cost) + unattributed_cost == total_cost (honest, reconcilable).
	for key, c := range costs {
		out.TotalCostMicroUSD += c.cost
		if key == "" {
			out.UnattributedCostMicroUSD = c.cost
		}
	}
	out.NetValueMicroUSD = out.TotalValueMicroUSD - out.TotalCostMicroUSD
	if out.Buckets == nil {
		out.Buckets = []valueBucketDTO{}
	}
	// Highest net cost-without-value first (the CFO's attention order): sort by cost
	// descending, then key for stability.
	sort.Slice(out.Buckets, func(i, j int) bool {
		if out.Buckets[i].CostMicroUSD != out.Buckets[j].CostMicroUSD {
			return out.Buckets[i].CostMicroUSD > out.Buckets[j].CostMicroUSD
		}
		return out.Buckets[i].Key < out.Buckets[j].Key
	})
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// cancellationRiskReason returns the cancellation-risk reason for a bucket, or "" when
// it is not at risk. Material burn (cost >= floor) with no recorded outcomes, or with
// outcomes but none satisfied, is the Gartner cancellation-risk early warning.
func cancellationRiskReason(cost int64, outcomes, satisfied int, minCost int64) string {
	if cost <= 0 || cost < minCost {
		return ""
	}
	if outcomes == 0 {
		return "spend with no recorded outcomes"
	}
	if satisfied == 0 {
		return "outcomes recorded but none satisfied"
	}
	return ""
}

// valueSummary is the CFO panel: spend-vs-outcome totals, the creditable (fallback)
// breakout, cost-per-outcome, and the cancellation-risk list (ranked by burn) for the
// requested dimension.
func (m *Module) valueSummary(ctx context.Context, sc store.Scope, dim string, since time.Time, hasSince bool, until time.Time, hasUntil bool, minCostMicroUSD int64) (valueSummaryResponse, error) {
	view, err := m.costPerOutcome(ctx, sc, dim, since, hasSince, until, hasUntil, minCostMicroUSD)
	if err != nil {
		return valueSummaryResponse{}, err
	}
	out := valueSummaryResponse{
		Dimension:                dim,
		Since:                    view.Since,
		Until:                    view.Until,
		TotalCostMicroUSD:        view.TotalCostMicroUSD,
		UnattributedCostMicroUSD: view.UnattributedCostMicroUSD,
		TotalValueMicroUSD:       view.TotalValueMicroUSD,
		NetValueMicroUSD:         view.NetValueMicroUSD,
		TotalOutcomes:            view.TotalOutcomes,
		Truncated:                view.Truncated,
		CancellationRisk:         []cancellationRiskDTO{},
	}
	for _, b := range view.Buckets {
		out.CreditableMicroUSD += b.CreditableMicroUSD
		out.Satisfied += b.Satisfied
		out.Unsatisfied += b.Unsatisfied
		if b.CancellationRisk {
			out.CancellationRisk = append(out.CancellationRisk, cancellationRiskDTO{
				Dimension: dim, Key: b.Key, CostMicroUSD: b.CostMicroUSD,
				Outcomes: b.Outcomes, Satisfied: b.Satisfied, Reason: b.RiskReason,
			})
		}
	}
	// cost-per-outcome divides the ATTRIBUTED cost (total minus the unattributed
	// remainder) by outcomes — outcomes only attach to attributed subjects, so folding
	// in unattributed spend would inflate the ratio and diverge from the per-bucket
	// figures (which use attributed bucket cost). net_value above uses the full total.
	attributedCost := out.TotalCostMicroUSD - out.UnattributedCostMicroUSD
	if out.TotalOutcomes > 0 {
		out.SatisfiedRatePct = int(float64(out.Satisfied) / float64(out.TotalOutcomes) * 100)
		out.CostPerOutcomeMicroUSD = attributedCost / int64(out.TotalOutcomes)
	}
	if out.Satisfied > 0 {
		out.CostPerSatisfiedMicroUSD = attributedCost / int64(out.Satisfied)
	}
	if out.TotalOutcomes == 0 {
		out.Note = "No outcomes ingested in this window — cost-per-outcome and value are unavailable. Post graded outcomes to POST /v1/m/finops/outcomes (the operator/automation bridge) to attribute value. Every spend bucket is a cancellation-risk candidate until then."
	}
	return out, nil
}

// --- EvalResult enrichment (module XII) --------------------------------------

// evalRate is an agent's eval pass/total tally from core EvalResult rows.
type evalRate struct{ passed, total int }

func (e evalRate) passRatePct() int {
	if e.total == 0 {
		return 0
	}
	return int(float64(e.passed) / float64(e.total) * 100)
}

// evalRateByAgentRef indexes agent-subject EvalResult pass/total by the agent's external
// ref (ExternalID, else Name) — the SAME key the cost/outcome buckets carry — so the
// value join is an O(1) map lookup with no per-bucket re-resolution (no N+1). Two scans
// (evals by SubjectID, then agents to map id→ref); genuine store errors PROPAGATE and
// never silently degrade the panel. EvalResult.SubjectID is often zero (the subject ref
// does not parse to an id), so the join is intentionally sparse — a real signal WHERE it
// exists, never fabricated where it does not.
func evalRateByAgentRef(ctx context.Context, sc store.Scope) (map[string]evalRate, error) {
	byID := map[model.ID]evalRate{}
	q := model.Query{Filters: []model.Filter{eq("subject_kind", "agent")}, Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := sc.Evals().List(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, e := range recs {
			if e.SubjectID.IsZero() {
				continue
			}
			r := byID[e.SubjectID]
			r.total++
			if e.Passed {
				r.passed++
			}
			byID[e.SubjectID] = r
		}
		if !page.HasMore || pages+1 >= maxScanPages {
			break
		}
		q.Cursor = page.Cursor
	}
	if len(byID) == 0 {
		return nil, nil
	}
	out := make(map[string]evalRate, len(byID))
	aq := model.Query{Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := sc.Agents().List(ctx, aq)
		if err != nil {
			return nil, err
		}
		for _, a := range recs {
			r, ok := byID[a.ID]
			if !ok {
				continue
			}
			ref := a.ExternalID
			if ref == "" {
				ref = a.Name
			}
			if ref != "" {
				out[ref] = r
			}
		}
		if !page.HasMore || pages+1 >= maxScanPages {
			break
		}
		aq.Cursor = page.Cursor
	}
	return out, nil
}

// --- HTTP handlers -----------------------------------------------------------

// handleIngestOutcome ingests one graded outcome over HTTP (the operator/automation
// bridge), auditing the principal's privileged write atomically with its effect.
// Returns 202 Accepted (dedup can make the write a no-op).
func (m *Module) handleIngestOutcome(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in outcomeIngestRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	// Audit the REAL principal (docs/SECURITY-HARDENING.md): WHO graded which subject — ids/verdict/
	// value only, never a payload or PII. Inside ingestOutcome's transaction so a
	// rolled-back ingest leaves no phantom audit.
	audit := func(ctx context.Context, sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      mc.Principal.Actor(),
			ActorKind:  mc.Principal.ActorKind(),
			Action:     "finops.outcome.ingest",
			TargetKind: outcomeKind,
			Meta: map[string]any{
				"subject_kind":    in.SubjectKind,
				"subject_ref":     in.SubjectRef,
				"verdict":         in.Verdict,
				"value_micro_usd": in.ValueMicroUSD,
			},
		})
		return err
	}
	if err := m.ingestOutcome(r.Context(), mc.Tenant, in, audit); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// handleListOutcomes lists graded outcomes, optionally filtered by subject.
func (m *Module) handleListOutcomes(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if sk := r.URL.Query().Get("subject_kind"); sk != "" {
		q.Filters = append(q.Filters, eq(colOutcomeSubjectKind, sk))
	}
	if sr := r.URL.Query().Get("subject_ref"); sr != "" {
		q.Filters = append(q.Filters, eq(colOutcomeSubjectRef, sr))
	}
	out := listResponse[outcomeDTO]{Items: []outcomeDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(outcomeKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toOutcomeDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleValue serves the cost-per-outcome breakdown by ?dimension (agent|identity|
// session, default agent) over the standard since/until window.
func (m *Module) handleValue(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dim := r.URL.Query().Get("dimension")
	if dim == "" {
		dim = defaultValueDimension
	}
	if !validValueDimensions[dim] {
		writeJSON(w, http.StatusBadRequest, errorBody("dimension must be one of agent, identity, session"))
		return
	}
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	minCost := minCostParam(r)
	var out valueResponse
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, e = m.costPerOutcome(r.Context(), sc, dim, since, hasSince, until, hasUntil, minCost)
		return e
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleValueSummary serves the CFO panel (totals + cancellation-risk list).
func (m *Module) handleValueSummary(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dim := r.URL.Query().Get("dimension")
	if dim == "" {
		dim = defaultValueDimension
	}
	if !validValueDimensions[dim] {
		writeJSON(w, http.StatusBadRequest, errorBody("dimension must be one of agent, identity, session"))
		return
	}
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	minCost := minCostParam(r)
	var out valueSummaryResponse
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, e = m.valueSummary(r.Context(), sc, dim, since, hasSince, until, hasUntil, minCost)
		return e
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// minCostParam reads the cancellation-risk "material burn" floor in micro-USD (a
// CFO-tunable threshold; absent/invalid/non-positive = 0, which flags any positive
// no-outcome spend).
func minCostParam(r *http.Request) int64 {
	if v := r.URL.Query().Get("min_cost_micro_usd"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
