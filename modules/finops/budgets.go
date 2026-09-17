// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// policyKindBudget is the core Policy.Kind a budget is stored under.
const policyKindBudget = "budget"

// defaultThresholds are the consumption fractions a budget alerts at when it
// declares none: half, four-fifths and the full limit.
var defaultThresholds = []float64{0.5, 0.8, 1.0}

// validDimensions are the named spend dimensions accepted by the module. Most are
// read-model columns; user_group and agent_group are budget-only preventive scopes
// fanned out through actor/agent_ref because the read-model has no group columns.
var validDimensions = map[string]bool{
	"global": true, "model": true, "provider": true,
	"agent": true, "session": true, "team": true, "project": true,
	"workspace": true, "api_key": true, "actor": true, "service_tier": true,
	"context_window": true, "inference_geo": true, "gateway": true, "cost_type": true,
	"user_group": true, "agent_group": true,
	// identity: the FIRM roster identity (NHI/SPIFFE/service-account) — the
	// per-identity dollar-budget scoping key, resolved at ingest from agent.IdentityID
	// / api_key / actor. workspace (the Anthropic workspace) and actor (the developer/
	// user) are already dimensions above — together they are the three identity flavors
	// Denominates budgets on.
	"identity": true,
	// cost_center: the accounting cost center resolved at ingestion from
	// mapping rules. Enables per-CC spend slicing and per-CC budgets.
	"cost_center": true,
}

// budgetDimensions are the dimensions a BUDGET can scope on — validDimensions MINUS
// cost_type. cost_type rides BOTH the billed cost_report server-tool breakdown AND the
// estimated fallback_attempt lines, so it is not "billed-only"; it is excluded
// as a budget scope because a single cost_type is not a useful enforceable spend
// boundary (it cuts across every identity/model/workspace) and would mostly no-op — it
// is rejected rather than sold as an enforceable dimension. Its estimated
// fallback_attempt slice IS surfaced, as the creditable breakout in the value panels.
var budgetDimensions = func() map[string]bool {
	m := map[string]bool{}
	for d := range validDimensions {
		if d != "cost_type" {
			m[d] = true
		}
	}
	return m
}()

// budgetDimensionList is the sorted, comma-joined budget dimensions, for the
// validation error message (kept in sync with budgetDimensions, not hardcoded).
var budgetDimensionList = func() string {
	ds := make([]string, 0, len(budgetDimensions))
	for d := range budgetDimensions {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	return strings.Join(ds, ", ")
}()

var validPeriods = map[string]bool{"daily": true, "weekly": true, "monthly": true, "total": true}

// Budget enforcement actions (FIN-08). "alert" is showback-only (the v1 default and
// the safe default — never silently enforces). "throttle"/"block" additionally emit
// a hard-cap signal an actuation seam (orchestration HITL gate / modelrouter) can
// consume to slow or deny new spend; FinOps emits the signal, it does not itself
// deny a request.
var validActions = map[string]bool{"alert": true, "throttle": true, "block": true}

const budgetActionAlert = "alert"

// budgetSpec is the typed view of a budget Policy's Spec.
type budgetSpec struct {
	Dimension     string    `json:"dimension"`
	Key           string    `json:"key,omitempty"`
	LimitMicroUSD int64     `json:"limit_micro_usd"`
	Period        string    `json:"period"`
	Thresholds    []float64 `json:"thresholds,omitempty"`
	Currency      string    `json:"currency,omitempty"`
	// FIN-08: enforcement. Action is alert (default) | throttle | block.
	// ReservedMicroUSD is committed/reserved capacity (e.g. a Priority Tier
	// commitment) counted toward the limit so a reservation cannot be silently
	// over-consumed; it is an accounting line, not a charge.
	Action           string `json:"action,omitempty"`
	ReservedMicroUSD int64  `json:"reserved_micro_usd,omitempty"`
	FailClosed       bool   `json:"fail_closed,omitempty"`
}

// validate normalizes and checks a budget spec, returning a message on a problem.
func (s *budgetSpec) validate() string {
	s.fillDefaults()
	if !budgetDimensions[s.Dimension] {
		return "dimension must be one of " + budgetDimensionList
	}
	if s.Dimension != "global" && s.Key == "" {
		return "key is required for a non-global dimension"
	}
	if !validPeriods[s.Period] {
		return "period must be one of daily, weekly, monthly, total"
	}
	if s.LimitMicroUSD <= 0 {
		return "limit_micro_usd must be positive"
	}
	if !validActions[s.Action] {
		return "action must be one of alert, throttle, block"
	}
	if s.ReservedMicroUSD < 0 {
		return "reserved_micro_usd must not be negative"
	}
	return ""
}

// fillDefaults applies the defaults without rejecting (used on the evaluation
// path, where a stored budget is trusted).
func (s *budgetSpec) fillDefaults() {
	if s.Dimension == "" {
		s.Dimension = "global"
	}
	if s.Period == "" {
		s.Period = "monthly"
	}
	if s.Currency == "" {
		s.Currency = "USD"
	}
	if s.Action == "" {
		s.Action = budgetActionAlert
	}
	if len(s.Thresholds) == 0 {
		s.Thresholds = append([]float64{}, defaultThresholds...)
	} else {
		out := s.Thresholds[:0:0]
		seen := map[float64]bool{}
		for _, t := range s.Thresholds {
			if t > 0 && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		sort.Float64s(out)
		s.Thresholds = out
	}
}

func (s budgetSpec) toSpecMap() map[string]any {
	out := map[string]any{
		"dimension": s.Dimension, "key": s.Key, "limit_micro_usd": s.LimitMicroUSD,
		"period": s.Period, "thresholds": s.Thresholds, "currency": s.Currency,
		"action": s.Action, "reserved_micro_usd": s.ReservedMicroUSD,
	}
	if s.FailClosed {
		out["fail_closed"] = true
	}
	return out
}

func parseBudgetSpec(spec map[string]any) budgetSpec {
	return budgetSpec{
		Dimension:        specString(spec, "dimension"),
		Key:              specString(spec, "key"),
		LimitMicroUSD:    specInt64(spec, "limit_micro_usd"),
		Period:           specString(spec, "period"),
		Thresholds:       specFloats(spec, "thresholds"),
		Currency:         specString(spec, "currency"),
		Action:           specString(spec, "action"),
		ReservedMicroUSD: specInt64(spec, "reserved_micro_usd"),
		FailClosed:       specBool(spec, "fail_closed"),
	}
}

// matches reports whether a cost attribution falls under this budget.
func (s budgetSpec) matches(attr attribution) bool {
	switch s.Dimension {
	case "global":
		return true
	case "provider":
		return attr.ProviderRef == s.Key
	case "model":
		return attr.ModelRef == s.Key
	case "agent":
		return attr.AgentRef == s.Key
	case "agent_group":
		return contains(attr.AgentGroupRefs, s.Key)
	case "session":
		return attr.SessionRef == s.Key
	case "team":
		return attr.Team == s.Key
	case "project":
		return attr.Project == s.Key
	case "workspace":
		return attr.WorkspaceRef == s.Key
	case "api_key":
		return attr.APIKeyRef == s.Key
	case "actor":
		return attr.Actor == s.Key
	case "user_group":
		return contains(attr.UserGroupRefs, s.Key)
	case "service_tier":
		return attr.ServiceTier == s.Key
	case "context_window":
		return attr.ContextWindow == s.Key
	case "inference_geo":
		return attr.InferenceGeo == s.Key
	case "gateway":
		return attr.Gateway == s.Key
	case "cost_type":
		return attr.CostType == s.Key
	case "identity":
		return attr.IdentityRef == s.Key
	case "routine":
		return attr.RoutineRef == s.Key
	case "cost_center":
		return attr.CostCenterRef == s.Key
	}
	return false
}

// sampleFilters returns the read-model filters that select this budget's spend
// (empty for global, which selects everything).
func (s budgetSpec) sampleFilters() []model.Filter {
	if s.Dimension == "global" {
		return nil
	}
	col := dimensionColumn(s.Dimension)
	if col == "" {
		return nil
	}
	return []model.Filter{eq(col, s.Key)}
}

// pendingAlert is a budget-threshold crossing recorded in the transaction and
// emitted as a FindingReport after it commits.
type pendingAlert struct {
	BudgetID     model.ID
	BudgetName   string
	Dimension    string
	DimKey       string
	Period       string
	ThresholdPct int
	Spend        int64
	Limit        int64
	Severity     model.Severity
	// Action is the budget's enforcement action (alert|throttle|block). When the
	// limit (100%) is crossed under throttle/block, emitAlert raises a distinct
	// hard-cap signal an actuation seam can act on.
	Action string
	// AlertID and Digest come from the row that was actually written, in the same
	// transaction, so what is published references durable evidence instead of a
	// hash recomputed from four in-memory values (A4.2).
	AlertID model.ID
	Digest  string
	// AmountClass is the classification of Spend: exact, or a proven lower bound.
	// A crossing proved by a bound is a real crossing; the class says the AMOUNT's
	// evidence is incomplete, never that the decision is.
	AmountClass amountClass
	// Captured from the exact envelope written in the ingestion transaction.
	BudgetEvidence *sdkmodel.BudgetAlertEvidenceSummary
}

// pendingDiagnostic is a budget whose evaluation could not establish an amount. It
// is NOT an alert: it carries no money, creates no threshold row and never becomes
// a cap. It is emitted after the commit so an operator can see that a budget could
// not be evaluated, instead of that fact living only in a log line.
type pendingDiagnostic struct {
	BudgetID   model.ID
	BudgetName string
	Dimension  string
	DimKey     string
	Period     string
	Causes     []amountCause
}

// listAllBudgets drains EVERY page of the tenant's budget policies by the id keyset
// (D-04), so the enforcement/evaluation paths never silently ignore a budget
// that sorts onto a page beyond the store's max page — an enforcing block budget on
// an unread page would otherwise let spend proceed uncapped. It returns truncated=
// true only past the bounded page budget (maxScanPages × listCap budgets), which the
// pre-flight gate turns into an explicit deny (fail-closed), never a silent allow.
func listAllBudgets(ctx context.Context, sc store.Scope) ([]model.Policy, bool, error) {
	var out []model.Policy
	q := model.Query{Filters: []model.Filter{eq("kind", policyKindBudget)}, Limit: listCap}
	for pages := 0; ; pages++ {
		budgets, page, err := sc.Policies().List(ctx, q)
		if err != nil {
			return nil, false, err
		}
		out = append(out, budgets...)
		if !page.HasMore || page.Cursor == "" {
			return out, false, nil
		}
		if pages+1 >= maxScanPages {
			return out, true, nil
		}
		q.Cursor = page.Cursor
	}
}

// evaluateBudgets evaluates every enabled budget the ingested sample touches and
// records (de-duplicated) any newly crossed thresholds, returning them to emit
// after the transaction commits.
//
// A4.2 moved the money here onto the shared strict evaluation: the float64 money
// comparison and the old aggregate are gone from THIS path, an unknown amount can
// no longer produce a crossing, and each written row carries its own durable
// evidence. What did not change: the period comes from the sample's OccurredAt, a
// real read/storage failure still aborts the ingestion, the dedup identity is
// untouched, and publication still happens only after the commit.
func (m *Module) evaluateBudgets(ctx context.Context, sc store.Scope, attr attribution) ([]pendingAlert, []pendingDiagnostic, error) {
	// A4.2 CORRECTION (R5). Serialize this tenant's alert writers for the rest of the
	// transaction BEFORE anything is read, so the existence check below and the INSERT
	// that follows it cannot be interleaved with another ingestion's.
	//
	// THE BOUNDARY (D02 ingest cut): this entry point is for a caller that does NOT
	// already hold the writer lock, and it is the one every caller outside this file
	// uses. The cost ingestion holds the lock from the top of its transaction —
	// earlier than here, because its own decisive read (the natural-key lookup) and
	// its audit hook come first — so it calls evaluateBudgetsLocked instead. Taking
	// the key twice would be harmless on both engines (SQLite no-ops it, PostgreSQL
	// reference-counts an advisory lock inside one transaction) but it would still be
	// a second acquisition claiming to establish something the first already did.
	if err := lockFinOpsWriter(ctx, sc); err != nil {
		return nil, nil, err
	}
	return m.evaluateBudgetsLocked(ctx, sc, attr)
}

// evaluateBudgetsLocked is evaluateBudgets for a caller that ALREADY holds this
// tenant's FinOps writer lock on sc for the rest of the transaction. It never
// acquires it: a caller that has not taken it must go through evaluateBudgets, and
// nothing here re-establishes serialization on its own.
func (m *Module) evaluateBudgetsLocked(ctx context.Context, sc store.Scope, attr attribution) ([]pendingAlert, []pendingDiagnostic, error) {
	// Drain ALL budget pages (D-04): a single Limit:listCap page silently ignored
	// budgets past the first page, so a threshold crossing on such a budget never
	// alerted. The accounting path cannot deny (post-spend), so a truncation past the
	// bounded page budget is not fail-closed here; the pre-flight CheckBudget gate is.
	//
	// A4.2 does NOT claim the catalogue is complete from this call: when the census is
	// truncated the batch says so as a diagnostic, the budgets actually read keep
	// their proven crossings, and nothing here asserts "all budgets evaluated" or "no
	// budget exceeded". Certifying every enumeration of the catalogue stays a
	// registered D02 dependency.
	budgets, censusTruncated, err := listAllBudgets(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	var (
		pending     []pendingAlert
		diagnostics []pendingDiagnostic
	)
	if censusTruncated {
		diagnostics = append(diagnostics, pendingDiagnostic{
			BudgetName: "budget catalogue", Causes: []amountCause{causeBudgetCensusTruncated},
		})
	}
	now := m.clock.Now().Time()
	for _, p := range budgets {
		if !p.Enabled {
			continue
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		if !spec.matches(attr) {
			continue
		}
		eval, err := evaluateBudgetAmount(ctx, sc, p, spec, attr.OccurredAt, now)
		if err != nil {
			// A real read/storage failure. The ingestion aborts with it, exactly as it
			// did before: it is not downgraded into a diagnostic or a zero.
			return nil, nil, err
		}
		if eval.Amount.Class == amountUnknown {
			// No amount was established, so no crossing can be proved and no row is
			// written. This is reported as its own signal rather than as an alert with
			// a fabricated zero.
			diagnostics = append(diagnostics, diagnosticFor(eval))
			continue
		}
		for _, te := range eval.Thresholds {
			if te.Decision.Result != crossingProven {
				continue
			}
			if !te.LegacyPctOK {
				// The crossing is real but cannot be given the historical identity
				// (tenant, budget, period, integer percent). It is surfaced rather than
				// silently saturated into another threshold's row; changing that
				// identity is a separate contract.
				diagnostics = append(diagnostics, pendingDiagnostic{
					BudgetID: p.ID, BudgetName: p.Name, Dimension: spec.Dimension, DimKey: spec.Key,
					Period: spec.Period, Causes: []amountCause{causeThresholdIdentityUnrepresentable},
				})
				continue
			}
			recorded, err := recordAlertWithEvidence(ctx, sc, eval, te)
			if err != nil {
				return nil, nil, err
			}
			if recorded == nil {
				// The dedup guard already holds this crossing. The first row's evidence
				// is not rewritten and nothing is re-emitted.
				continue
			}
			pending = append(pending, *recorded)
		}
	}
	return pending, diagnostics, nil
}

// diagnosticFor turns an unestablished evaluation into the post-commit signal.
func diagnosticFor(eval budgetEvaluation) pendingDiagnostic {
	return pendingDiagnostic{
		BudgetID: eval.PolicyID, BudgetName: eval.PolicyName, Dimension: eval.Spec.Dimension,
		DimKey: eval.Spec.Key, Period: eval.Spec.Period, Causes: eval.causes(),
	}
}

// alertWriterLockKeyPrefix names the transaction lock every participating FinOps
// writer of one tenant takes. ONE key per tenant, not one per budget: a per-budget
// key would make the acquisition ORDER of a multi-budget ingestion part of the
// protocol, and two ingestions touching the same two budgets in different orders
// would deadlock. The key carries the canonical tenant id and nothing else — no
// amount, no policy, no operator data.
//
// The NAME and the VALUE are the ones the alert writer has always used, and that is
// deliberate: the cost ingestion and the reservation ledger now take the SAME key,
// so they serialize against the alert writers that already hold it. A second,
// "cleaner" namespace would have been a lock that no existing caller takes — it
// would serialize nothing, while reading as if it did.
const alertWriterLockKeyPrefix = "finops.budget_alert.writer.v1:"

// lockFinOpsWriter takes that lock on the CALLER's own transaction, and FAILS
// CLOSED when the scope cannot provide it.
//
// WHY A LOCK AT ALL, when the check and the insert already share a transaction: on
// SQLite they do serialize — one writer — but on PostgreSQL they do not. Under READ
// COMMITTED two concurrent ingestions can both find no alert row for the same
// (budget, period, threshold) and both proceed to INSERT; the unique index stops the
// second one, and on PostgreSQL a unique violation ABORTS THE WHOLE TRANSACTION.
// That is the defect the independent review identified: the losing ingestion loses
// its cost sample and its ledger entry too, for a crossing that merely needed to be
// deduplicated. The lock makes the existence check authoritative, so the second
// writer sees the row and never attempts the INSERT.
//
// The SAME shape appears in the two writers that were left outside it: the cost
// sample + CostRecord upsert (a lookup by natural key followed by an INSERT under a
// UNIQUE index) and the reservation ledger (a seq probe and a reserved sum followed
// by an INSERT/UPDATE). Each of those pairs is a decision that only holds if nothing
// commits between the lookup and the write, so all three now take ONE key, before
// their own decisive read.
//
// MEASURED, and it bounds what the key can be credited with (D02 ingest cut): on the
// store this module actually runs on, two write transactions of one tenant CANNOT
// interleave in the first place — sqlstore.Mutate takes an exclusive per-tenant
// advisory lock before it calls the callback (lineageWriteTracker.start,
// lineage_writer.go), and SQLite admits a single writer. So on THIS store the key is
// defense in depth, and the race described above is not reachable through Mutate
// today. What it buys is a guarantee that belongs to this module rather than to one
// store implementation: a scope decorator, a future selective-mutation path (which
// takes only its planned keys and no lineage enrollment) or another store leaves
// these writers serialized among themselves, at a point in the transaction that is
// declared here instead of inferred.
//
// Failing closed is the rule store.TransactionLocker states for a correctness-
// sensitive caller, and here it is not ceremony: a scope that silently dropped the
// capability would restore exactly the race above, and an ingestion that refuses can
// be retried while a lost sample cannot be recovered. Failing closed is about the
// LOCK, not about money: each caller keeps its own declared posture for the failure
// it returns (the reservation seam still fails open on an error, and this adds no
// deny it did not already have).
//
// WHAT THIS COSTS, said plainly because it is a NEW SUPPORTED-SCOPE REQUIREMENT and
// not a preserved legacy behaviour (R1 of the independent review, adjudicated): the
// alert writer already refused a scope without the capability, but the reservation
// ledger did not. Through a decorator that embeds store.Scope without forwarding
// LockTransaction, reserve/settle/sweep used to work — reserve within headroom, deny
// an exhausted budget, settle a handle, sweep expired rows — and now they fail here
// before their decisive read. That scope is unsupported for these writes; the public
// GoDoc on ReserveBudget carries the boundary and the decorator migration, and the
// controls in ingest_transaction_test.go pin BOTH sides of it: a forwarding decorator
// keeps every one of those outcomes, a hiding one gets the documented refusal.
//
// The two things this function must never become, because either one turns the
// requirement back into the defect: it must not SKIP the lock when the capability is
// absent, and it must not UNWRAP an embedding decorator to reach the scope beneath.
// Unwrapping would also break confinement — a decorator is how a caller narrows what
// a scope may do, and reaching past it to take a lock is reaching past it, period.
func lockFinOpsWriter(ctx context.Context, sc store.Scope) error {
	// Type assertion on the scope AS GIVEN. No unwrapping: see above.
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return fmt.Errorf("finops: writer cannot serialize: store scope provides no transaction lock")
	}
	if err := locker.LockTransaction(ctx, alertWriterLockKeyPrefix+sc.Tenant().String()); err != nil {
		return fmt.Errorf("finops: writer lock: %w", err)
	}
	return nil
}

// recordAlertWithEvidence writes the crossing row, its evidence envelope and its
// digest in ONE insert, under an id pre-assigned before the envelope is built — so
// the evidence commits to the row it belongs to and no second transaction can leave
// a row without its proof.
//
// Dedup is decided by an EXACT LOOKUP of the historical identity under the writer
// lock, not by catching the INSERT's conflict. The two are not equivalent: this path
// runs inside the ingestion's transaction, and on PostgreSQL a unique violation
// aborts that transaction, so "absorb ErrConflict and report success without
// creating" left the caller committing an already-dead transaction — the cost sample
// and ledger row of a perfectly good second ingestion went with it. An unexpected
// conflict from the INSERT is therefore PROPAGATED now: after the lookup found
// nothing under the lock, a conflict is a fact this writer cannot explain, and a
// fact it cannot explain is not a successful deduplication.
//
// The lookup uses the historical identity exactly as the unique index defines it —
// tenant (added by the repository), budget, period start, threshold percent — with
// the same serializations the INSERT uses. Nothing else joins that identity.
func recordAlertWithEvidence(ctx context.Context, sc store.Scope, eval budgetEvaluation, te thresholdEvaluation) (*pendingAlert, error) {
	repo, err := sc.Ext(budgetAlertKind)
	if err != nil {
		return nil, err
	}
	periodStartText := model.NewTimestamp(eval.PeriodStart).String()
	existing, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			eq(colBudgetID, eval.PolicyID.String()),
			eq(colPeriodStart, periodStartText),
			eqInt(colThresholdPct, int64(te.LegacyPct)),
		},
		Limit: 1,
	})
	if err != nil {
		// A real read failure. It is not a deduplication and it is not a crossing: the
		// ingestion aborts with it, like every other read failure on this path.
		return nil, err
	}
	if len(existing) > 0 {
		// The crossing is already recorded. The first row's evidence is not rewritten,
		// not re-emitted, and a historical row without evidence is NOT upgraded into a
		// v1 envelope: deduplication preserves what is there, it does not reconstruct
		// it from today's evaluation.
		return nil, nil
	}
	alertID := model.NewID()
	env, legacySpend, digest, err := buildAlertEvidence(alertID, sc.Tenant(), eval, te)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("finops: encode alert evidence: %w", err)
	}
	rec := model.Record{
		colBudgetID:          eval.PolicyID.String(),
		colPeriod:            eval.Spec.Period,
		colPeriodStart:       model.NewTimestamp(eval.PeriodStart).String(),
		colThresholdPct:      int64(te.LegacyPct),
		colDimension:         eval.Spec.Dimension,
		colDimKey:            eval.Spec.Key,
		colAlertSpend:        legacySpend,
		colAlertLimit:        eval.Spec.LimitMicroUSD,
		colSeverity:          string(severityForPct(te.LegacyPct)),
		colTriggeredAt:       model.NewTimestamp(eval.SampleAt).String(),
		colAlertEvidence:     string(body),
		colAlertEvidenceHash: digest,
	}
	if _, err := repo.CreateWithID(ctx, alertID, rec); err != nil {
		// Including a conflict: see the doc comment. Under the writer lock the lookup
		// above already answered the dedup question, so an INSERT that still collides
		// is an unexplained state — and on PostgreSQL the transaction is already
		// aborted, which no success return can undo.
		return nil, err
	}
	// Every published field is captured from this same envelope before returning
	// to the transaction owner. No policy or financial read happens at emission.
	return &pendingAlert{
		BudgetID: model.ID(env.BudgetID), BudgetName: env.Policy.Name,
		Dimension: env.Policy.Dimension, DimKey: env.Policy.Key, Period: env.Policy.Period,
		ThresholdPct: env.Decision.LegacyPct, Spend: legacySpend, Limit: eval.Spec.LimitMicroUSD,
		Severity: severityForPct(env.Decision.LegacyPct), Action: env.Policy.Action,
		AlertID: alertID, Digest: digest, AmountClass: amountClass(env.Amount.Class),
		BudgetEvidence: summaryForAlert(env),
	}, nil
}

// summaryForAlert projects the committed envelope, without amounts or raw policy.
// The digest still refers to the complete envelope, not just these visible fields.
func summaryForAlert(env alertEvidenceEnvelope) *sdkmodel.BudgetAlertEvidenceSummary {
	var causes []amountCause
	for _, list := range [][]string{env.Amount.Causes, env.Decision.Causes,
		env.Components.Cost.Causes, env.Components.Static.Causes, env.Components.Dynamic.Causes} {
		for _, cause := range list {
			causes = append(causes, amountCause(cause))
		}
	}
	if env.Policy.ConfigFault != "" {
		causes = append(causes, amountCause(env.Policy.ConfigFault))
	}
	return &sdkmodel.BudgetAlertEvidenceSummary{
		SchemaVersion: uint32(env.SchemaVersion), AlertID: env.AlertID,
		AmountClass: env.Amount.Class, Crossing: env.Decision.Result,
		Causes: sortedCauses(causes), DigestVersion: uint32(env.DigestVersion),
	}
}

// recordAlert inserts the alert-history row for a crossing, returning created=false
// when this (budget, period, threshold) was already alerted (the unique guard) so
// the module alerts once per threshold per period, not on every ingest.
// triggeredAt is the time of the usage that pushed spend over the threshold (the
// sample's OccurredAt) — meaningful and deterministic, not a wall clock.
//
// ⚠ IT HAS NO PRODUCTION CALLER, and its conflict catch must not be read as a safe
// PostgreSQL fallback: inside a transaction, the unique violation it absorbs has
// already aborted that transaction on PostgreSQL, so the success it reports is a
// success the commit cannot deliver. A writer that returns here must take the
// alert-writer lock and do the exact lookup that recordAlertWithEvidence does.
func recordAlert(ctx context.Context, sc store.Scope, budgetID model.ID, spec budgetSpec, pStart, triggeredAt time.Time, pct int, spend int64) (bool, error) {
	repo, err := sc.Ext(budgetAlertKind)
	if err != nil {
		return false, err
	}
	rec := model.Record{
		colBudgetID:     budgetID.String(),
		colPeriod:       spec.Period,
		colPeriodStart:  model.NewTimestamp(pStart).String(),
		colThresholdPct: int64(pct),
		colDimension:    spec.Dimension,
		colDimKey:       spec.Key,
		colAlertSpend:   spend,
		colAlertLimit:   spec.LimitMicroUSD,
		colSeverity:     string(severityForPct(pct)),
		colTriggeredAt:  model.NewTimestamp(triggeredAt).String(),
	}
	if _, err := repo.Create(ctx, rec); err != nil {
		if isConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// emitAlert publishes a budget-threshold crossing as a FindingReport on the bus,
// so an output connector / health-and-integrations can deliver it to
// Slack/SIEM/PagerDuty. The module emits the SIGNAL; it does not deliver it.
func (m *Module) emitAlert(ctx context.Context, tenant model.TenantID, a pendingAlert) {
	if m.host == nil {
		return
	}
	scope := "all spend"
	if a.Dimension != "global" {
		scope = fmt.Sprintf("%s=%s", a.Dimension, a.DimKey)
	}
	// A limit crossing on an enforcing budget is a HARD-CAP signal (distinct kind +
	// raised severity) so an actuation seam (orchestration HITL gate / modelrouter)
	// can throttle or deny new spend. Below the limit, or under the alert action, it
	// is the ordinary showback alert. FinOps emits the signal; it never denies here.
	kind, severity := "finops_budget", sdkmodel.Severity(string(a.Severity))
	title := fmt.Sprintf("Budget %q at %d%% (%s)", a.BudgetName, a.ThresholdPct, scope)
	if a.ThresholdPct >= 100 && (a.Action == "throttle" || a.Action == "block") {
		kind = "finops_budget_cap"
		title = fmt.Sprintf("Budget %q hit its %s cap at %d%% (%s)", a.BudgetName, a.Action, a.ThresholdPct, scope)
		if a.Action == "block" {
			severity = sdkmodel.SeverityCritical
		} else {
			severity = sdkmodel.SeverityHigh
		}
	}
	// A4.2: DetailHash carries the digest of the evidence actually persisted with
	// this alert, so a consumer's reference points at a durable record rather than at
	// a hash recomputed from four in-memory values. The older construction remains
	// for a row written without evidence.
	detail := a.Digest
	if detail == "" {
		detail = detailHash(a)
	}
	finding := sdkmodel.FindingReport{
		Kind:           kind,
		Severity:       severity,
		SubjectKind:    "budget",
		SubjectRef:     a.BudgetID.String(),
		Title:          title,
		DetailHash:     detail,
		BudgetEvidence: a.BudgetEvidence.Clone(),
		OccurredAt:     m.clock.Now().Time(),
	}
	ev := event.FromObservation(tenant.String(), Name, finding)
	if err := m.host.Publish(ctx, ev); err != nil {
		m.debugf("finops: emit alert failed", "err", err)
	}
}

// emitEvaluationIncomplete publishes the fact that a budget could NOT be evaluated.
// It is deliberately not an alert: no amount, no threshold, no cap, and it never
// creates or replaces a row. It exists so an unestablished evaluation is visible
// outside a log line, and it is published only after the ingestion committed —
// a storage failure still aborts and is never reduced to this signal.
func (m *Module) emitEvaluationIncomplete(ctx context.Context, tenant model.TenantID, d pendingDiagnostic) {
	if m.host == nil {
		return
	}
	scope := "all spend"
	if d.Dimension != "" && d.Dimension != "global" {
		scope = fmt.Sprintf("%s=%s", d.Dimension, d.DimKey)
	}
	causes := sortedCauses(d.Causes)
	finding := sdkmodel.FindingReport{
		Kind:        "finops_budget_evaluation_incomplete",
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "budget",
		SubjectRef:  d.BudgetID.String(),
		Title:       fmt.Sprintf("Budget %q could not be evaluated (%s)", d.BudgetName, scope),
		DetailHash:  diagnosticHash(d, causes),
		BudgetEvidence: &sdkmodel.BudgetAlertEvidenceSummary{
			SchemaVersion: 1, AmountClass: "unknown", Crossing: "unproven",
			Causes: causes, DigestVersion: 1,
		},
		OccurredAt: m.clock.Now().Time(),
	}
	ev := event.FromObservation(tenant.String(), Name, finding)
	if err := m.host.Publish(ctx, ev); err != nil {
		m.debugf("finops: emit evaluation diagnostic failed", "err", err)
	}
}

// diagnosticHash commits to the diagnostic's closed vocabulary only: the budget, its
// scope and the sorted causes. No amount and no storage error text.
func diagnosticHash(d pendingDiagnostic, causes []string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"olivares.finops.budget-evaluation-incomplete.v1\x00%s\x00%s\x00%s\x00%s\x00%s",
		d.BudgetID, d.Dimension, d.DimKey, d.Period, strings.Join(causes, ","))))
	return hex.EncodeToString(sum[:])
}

// detailHash is a hex SHA-256 of the alert's non-sensitive summary; the
// FindingReport carries the hash, not raw detail (docs/SECURITY-HARDENING.md).
func detailHash(a pendingAlert) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", a.BudgetID, a.ThresholdPct, a.Spend, a.Limit)))
	return hex.EncodeToString(sum[:])
}

// isConflict reports whether err is a unique-index/version conflict.
func isConflict(err error) bool { return errors.Is(err, store.ErrConflict) }

// budgetStatus computes a budget's current consumption against its limit, with a
// run-rate projection of the period. It recomputes authoritatively from the
// read-model rather than trusting any cached counter.
func budgetStatus(ctx context.Context, sc store.Scope, p model.Policy, now time.Time) (budgetStatusDTO, error) {
	spec := parseBudgetSpec(p.Spec)
	spec.fillDefaults()
	pStart, hasLower := periodStart(spec.Period, now)
	pEnd := periodEnd(spec.Period, pStart)
	var agg aggResult
	if isGroupDimension(spec.Dimension) {
		// Group budgets are preventive-only in this slice. budgetStatus has no auth
		// reader for user groups and no exact group column for either group type, so
		// it must not fall through to sampleFilters (which would sum everything).
		agg.Truncated = true
	} else {
		var err error
		agg, err = aggregatePeriod(ctx, sc, spec.sampleFilters(), pStart, hasLower, pEnd, hasLower)
		if err != nil {
			return budgetStatusDTO{}, err
		}
	}
	out := budgetStatusDTO{
		ID: p.ID.String(), Name: p.Name, Enabled: p.Enabled,
		Dimension: spec.Dimension, Key: spec.Key, Period: spec.Period,
		Action: spec.Action, LimitMicroUSD: spec.LimitMicroUSD, ReservedMicroUSD: spec.ReservedMicroUSD,
		SpendMicroUSD: agg.Cost,
		Currency:      spec.Currency, Samples: agg.Count, Truncated: agg.Truncated,
	}
	if hasLower {
		out.PeriodStart = model.NewTimestamp(pStart).String()
	}
	// Reserved capacity is committed against the limit, so ALL consumption signals
	// (consumed %, remaining, projection, over) are computed on effective spend
	// (actual + static reserved + dynamic reservations) — consistent with
	// RemainingMicroUSD and with CheckBudget's enforcement, so the status DTO never
	// reports "10% consumed, not over" while the same budget is actively throttling.
	// SpendMicroUSD stays the raw actual spend.
	// The version-aware hold reader, given this status's REAL window rather than
	// letting it infer an end from now (D02). It combines the legacy branch this
	// status has always read with the v1 obligations of held attempt parents.
	dyn, derr := heldReservedForWindow(ctx, sc, p.ID, spec.Key, pStart, pEnd, hasLower, now)
	if derr != nil {
		return budgetStatusDTO{}, derr
	}
	effective := sumInt64(agg.Cost, spec.ReservedMicroUSD, dyn.MicroUSD)
	// A reservation total that could not be established — or an effective total the
	// ledger cannot represent — makes every figure derived from it a BOUND, not a
	// measurement. Say so through the same truncated flag the spend aggregate
	// already uses, and publish no REMAINING headroom: remaining is the one figure a
	// reader acts on, and from a partial total it would be an overstatement of the
	// room that is actually left.
	established := dyn.established() && effective.OK
	if !established {
		out.Truncated = true
	}
	if projected := sumInt64(projectSpend(agg.Cost, spec.Period, pStart, now, hasLower), spec.ReservedMicroUSD, dyn.MicroUSD); projected.OK {
		out.ProjectedMicroUSD = projected.Value
	} else {
		out.Truncated = true
	}
	if spec.LimitMicroUSD > 0 {
		if effective.OK {
			out.ConsumedPct = pctOf(effective.Value, spec.LimitMicroUSD)
			out.Over = effective.Value >= spec.LimitMicroUSD
			// Remaining is limit − effective, and a large enough signed CREDIT puts that
			// subtraction outside the int64 range. Declining to publish a figure is
			// right; leaving the DTO's default zero behind and still calling the status
			// complete is not — zero is an amount a reader acts on, and it is not this
			// one. So the failure raises the same incompleteness signal everything else
			// here uses, and no saturated value is invented. The credit itself stays
			// perfectly legitimate: spend_micro_usd carries it unchanged.
			remaining := subInt64(spec.LimitMicroUSD, effective.Value)
			switch {
			case !remaining.OK:
				out.Truncated = true
			case established:
				out.RemainingMicroUSD = remaining.Value
			}
		} else {
			// The total left the int64 range from above: it exceeds every possible
			// ceiling. That is a fact worth reporting, unlike any amount that could be
			// printed for it — so Over is set and no saturated figure is invented.
			out.Over = effective.Above
		}
		out.ProjectedPct = pctOf(out.ProjectedMicroUSD, spec.LimitMicroUSD)
	}
	// A4.2: the AUTHORITATIVE section, from the same strict evaluation the alert path
	// uses. Everything computed above stays as the legacy projection it always was;
	// this is where a reader learns whether those numbers are established at all.
	//
	// What this traversal actually PUBLISHED travels with the request, because the
	// authoritative section classifies those fields and cannot do it from its own
	// second read: the two traversals are independent and, under READ COMMITTED, may
	// not have seen the same rows.
	legacy := legacyStatusFigures{
		Cost: agg.Cost, CostEstablished: !agg.Truncated,
		Effective: effective.Value, EffectiveEstablished: established,
		Remaining: out.RemainingMicroUSD, RemainingPublished: established && effective.OK && spec.LimitMicroUSD > 0,
		Over: out.Over,
	}
	if authoritative, aerr := budgetAmountStatus(ctx, sc, p, spec, now, legacy); aerr != nil {
		return budgetStatusDTO{}, aerr
	} else if authoritative != nil {
		out.Amount = authoritative
		if authoritative.State != "complete" {
			// A conservative signal for a client that only knows the old fields.
			out.Truncated = true
		}
	}

	// budget exhaustion from EWA daily rate — only for bounded periods.
	if spec.LimitMicroUSD > 0 && hasLower && !isGroupDimension(spec.Dimension) {
		winStart := now.UTC().AddDate(0, 0, -defaultForecastWindowDays)
		series, _, seriesErr := dailySeries(ctx, sc, winStart, now)
		if seriesErr == nil && len(series) > 0 {
			// Partition series to this budget's dimension if non-global.
			if spec.Dimension != "global" && spec.Dimension != "" {
				series, _, seriesErr = dailySeriesFiltered(ctx, sc, winStart, now, spec.sampleFilters())
				if seriesErr != nil {
					series = nil
				}
			}
		}
		if len(series) > 0 {
			ewaRate, ewaVar := ewaForecast(series, defaultEWAAlpha)
			ex := budgetExhaustion(ewaRate, ewaVar, agg.Cost, spec.LimitMicroUSD, spec.ReservedMicroUSD)
			out.ExhaustionDaysRemaining = ex.DaysRemaining
			out.ExhaustionConfidence = ex.Confidence
		}
	}
	return out, nil
}

// legacyStatusFigures is what budgetStatus's OWN traversal published in the older
// numeric fields, handed to the authoritative section so it can classify THOSE
// numbers instead of certifying them from a second, independent read.
type legacyStatusFigures struct {
	// Cost is the raw actual spend the old aggregate summed, and CostEstablished says
	// that aggregate was not scan-truncated.
	Cost            int64
	CostEstablished bool
	// Effective is spend + static + dynamic as that traversal computed it, and
	// EffectiveEstablished says the reservation total was established and the sum
	// representable.
	Effective            int64
	EffectiveEstablished bool
	// Remaining is the published limit − effective, and RemainingPublished says a
	// figure was actually written into the DTO rather than left as its zero value.
	Remaining          int64
	RemainingPublished bool
	// Over is the published boolean.
	Over bool
}

// budgetAmountStatus runs the shared evaluation for the CURRENT period and projects
// it into the status DTO. A real read failure is returned as an error, exactly like
// every other status read; an unestablished amount is reported as unknown with its
// causes, never as zero remaining or "not over".
func budgetAmountStatus(ctx context.Context, sc store.Scope, p model.Policy, spec budgetSpec, now time.Time, legacy legacyStatusFigures) (*budgetAmountStatusDTO, error) {
	eval, err := evaluateBudgetAmount(ctx, sc, p, spec, now, now)
	if err != nil {
		return nil, err
	}
	out := &budgetAmountStatusDTO{
		State: "incomplete", Class: string(eval.Amount.Class), Currency: eval.Spec.Currency,
		Causes: sortedCauses(eval.causes()),
		Components: alertEvidenceComponents{
			Cost:    costComponentEvidence(eval.Cost),
			Static:  staticComponentEvidence(eval.Static),
			Dynamic: dynamicComponentEvidence(eval.Dynamic),
		},
	}
	if eval.Amount.Class == amountExact {
		out.State = "complete"
	}
	if decimal := eval.Amount.Decimal(); decimal != "" {
		out.EffectiveMicroUSD = &decimal
	}
	// The limit itself, decided authoritatively whether or not the operator
	// configured a 1.0 threshold. `over` in the legacy fields is a boolean and cannot
	// carry "unproven"; this can, and it is the decision a reader should act on.
	overDecision := evaluateThresholdCrossing(eval.Amount, eval.Spec.LimitMicroUSD, 1)
	out.OverLimit = budgetThresholdStatusDTO{
		Threshold: overDecision.Threshold, TargetMicroUSD: overDecision.TargetDecimal,
		Result: string(overDecision.Result), Causes: sortedCauses(overDecision.Causes),
	}
	// Remaining is published ONLY when the amount is established and the subtraction
	// is representable. Otherwise it is null: not established is not "zero left".
	var remainingWide *big.Int
	if eval.Amount.Class == amountExact && eval.Spec.LimitMicroUSD > 0 && eval.Amount.Value != nil {
		remainingWide = new(big.Int).Sub(big.NewInt(eval.Spec.LimitMicroUSD), eval.Amount.Value)
		text := remainingWide.String()
		out.RemainingMicroUSD = &text
	}
	out.LegacyFields = classifyLegacyStatusFields(eval, remainingWide, overDecision, legacy)
	out.LegacyProjection = weakestLegacyClass(out.LegacyFields)
	for _, te := range eval.Thresholds {
		td := budgetThresholdStatusDTO{
			Threshold: te.Decision.Threshold, TargetMicroUSD: te.Decision.TargetDecimal,
			Result: string(te.Decision.Result), Causes: sortedCauses(te.Decision.Causes),
		}
		if te.LegacyPctOK {
			td.LegacyPct = te.LegacyPct
		}
		out.Thresholds = append(out.Thresholds, td)
	}
	return out, nil
}

// classifyLegacyStatusFields classifies each older numeric field of the status DTO
// against what this evaluation can actually re-derive for it.
//
// The rule for every money field is the same and it is a comparison: the field is
// `exact` only when the authoritative evaluation establishes that quantity AND the
// number that was published equals it. Anything else — an unestablished amount, a
// figure outside int64, a field the traversal never published, or two traversals
// that saw different data — is `unavailable`, which means "this older field is not a
// projection of this evaluation", not "the budget has nothing left".
//
//   - spend_micro_usd is the RAW actual cost, so its counterpart is the strict cost
//     component, not the effective amount. They are the same quantity read twice.
//   - remaining_micro_usd is limit − effective. This is the field the review measured
//     being certified as exact while it carried an unpublished zero, because the
//     authoritative remaining did not fit int64.
//   - over is a decision, and takes the authoritative 1.0 crossing. A published
//     boolean that disagrees with a decided crossing is not a projection of it.
//   - projected_micro_usd is never certified by A4.2: it comes from the run-rate
//     projection over aggregates this increment does not certify, which is the same
//     statement ForecastCertified makes.
func classifyLegacyStatusFields(eval budgetEvaluation, remainingWide *big.Int, over thresholdDecision, legacy legacyStatusFigures) budgetLegacyFieldsDTO {
	out := budgetLegacyFieldsDTO{
		SpendMicroUSD:     legacyValueUnavailable,
		RemainingMicroUSD: legacyValueUnavailable,
		ProjectedMicroUSD: legacyValueUnavailable,
		Over:              string(over.Result),
	}
	if legacy.CostEstablished && eval.Cost.Complete && eval.Cost.Total != nil &&
		eval.Cost.Total.IsInt64() && eval.Cost.Total.Int64() == legacy.Cost {
		out.SpendMicroUSD = legacyValueExact
	}
	if remainingWide != nil && legacy.RemainingPublished &&
		remainingWide.IsInt64() && remainingWide.Int64() == legacy.Remaining {
		out.RemainingMicroUSD = legacyValueExact
	}
	// The published boolean must agree with the decided crossing, or it is not this
	// decision's projection. An unproven crossing has no boolean at all.
	switch over.Result {
	case crossingProven:
		if !legacy.EffectiveEstablished || !legacy.Over {
			out.Over = string(crossingUnproven)
		}
	case crossingNotReached:
		if !legacy.EffectiveEstablished || legacy.Over {
			out.Over = string(crossingUnproven)
		}
	}
	return out
}

// weakestLegacyClass reduces the per-field classification to the one label an older
// client sees. Its subject is EXACTLY THREE fields — spend, remaining and over — and
// it is the weakest of them: a single one that is not a projection of this evaluation
// is enough to make "the older numbers are exact" false.
//
// THE FORECAST IS NOT IN IT, deliberately. projected_micro_usd is always
// `unavailable` because A4.2 certifies no forecast, so folding it in would pin this
// label to `unavailable` forever and it would stop saying anything about the three
// fields it exists for. A reader learns the forecast's standing from LegacyFields and
// from ForecastCertified, which are the fields that state it.
//
// `lower_bound` is reachable in this vocabulary — the alert envelope's legacy
// classification uses it for a proven bound written into the historical column — but
// not from status today: status publishes a money figure only when the amount is
// exact, so its fields are either that exact projection or nothing.
func weakestLegacyClass(f budgetLegacyFieldsDTO) string {
	for _, c := range []string{f.SpendMicroUSD, f.RemainingMicroUSD} {
		if c != legacyValueExact {
			return legacyValueUnavailable
		}
	}
	if f.Over != string(crossingProven) && f.Over != string(crossingNotReached) {
		return legacyValueUnavailable
	}
	return legacyValueExact
}

// periodStart returns the start instant of the period containing at (UTC), and
// whether the period has a lower bound at all ("total" does not).
func periodStart(period string, at time.Time) (time.Time, bool) {
	at = at.UTC()
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "daily":
		return day, true
	case "weekly":
		// ISO week: shift so Monday is the first day.
		offset := (int(at.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset), true
	case "total":
		return time.Time{}, false
	default: // monthly
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC), true
	}
}

// periodEnd returns the end instant of the period that starts at pStart.
func periodEnd(period string, pStart time.Time) time.Time {
	switch period {
	case "daily":
		return pStart.AddDate(0, 0, 1)
	case "weekly":
		return pStart.AddDate(0, 0, 7)
	case "monthly":
		return pStart.AddDate(0, 1, 0)
	default:
		return pStart
	}
}

// projectSpend projects period spend at the current run rate: spend scaled by the
// inverse fraction of the period elapsed. "total" and degenerate windows project
// the spend itself (no run-rate).
func projectSpend(spend int64, period string, pStart, now time.Time, hasLower bool) int64 {
	if !hasLower || period == "total" {
		return spend
	}
	pEnd := periodEnd(period, pStart)
	total := pEnd.Sub(pStart)
	elapsed := now.UTC().Sub(pStart)
	if total <= 0 || elapsed <= 0 {
		return spend
	}
	if elapsed >= total {
		return spend
	}
	frac := elapsed.Seconds() / total.Seconds()
	projected := float64(spend) / frac
	// Bound the projection so a huge spend in the first instants of a period
	// cannot overflow int64; 1e18 µUSD (a trillion USD) is a display ceiling no
	// real projection meaningfully exceeds.
	const ceilingMicroUSD = 1e18
	if projected > ceilingMicroUSD {
		return ceilingMicroUSD
	}
	return int64(projected)
}

// pctOf returns part/whole as an integer percentage (0 when whole is 0).
func pctOf(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(float64(part) / float64(whole) * 100)
}

// SpendDims is the attribution of a prospective request, used by the pre-flight
// budget check. It is the public, provider-neutral subset of the cost dimensions an
// actuation seam (modelrouter / orchestration HITL gate) knows BEFORE a call.
type SpendDims struct {
	ProviderRef, ModelRef, AgentRef, SessionRef, Team, Project string
	WorkspaceRef, APIKeyRef, ServiceTier, ContextWindow        string
	InferenceGeo, Gateway, CostType                            string
	// IdentityRef is the FIRM roster identity (NHI/SPIFFE) the request runs as,
	// if the seam already knows it. Usually empty: the actuation seams carry only the
	// AgentRef (a free-text ref), so CheckBudget resolves the firm identity itself from
	// AgentRef/APIKeyRef/Actor when an identity-scoped budget exists — the seam stays
	// money-free and unchanged.
	IdentityRef string
	// RoutineRef is the Claude Code Routine (trigger) ref that originated the
	// spend. Populated by the orchestration seam when a scheduled/routine fire carries
	// a trigger id. Enables per-routine enforcing budgets (Denial-of-Wallet for
	// autonomous periodic agents).
	RoutineRef string
	// CostCenterRef is the accounting cost center code the request is
	// attributed to, if the seam already knows it. Usually empty at pre-flight:
	// CheckBudget resolves the CC from the mapping rules when a CC-scoped budget
	// exists and the seam did not supply it.
	CostCenterRef string
	// UserGroupRefs are directory group ids the acting user is a member of in the
	// tenant (the same identifiers auth.Principal.GroupsIn returns). FinOps cannot
	// resolve these from SpendDims because the seam deliberately carries no user id.
	UserGroupRefs []string
	// AgentGroupRefs are agent-group slugs the acting agent belongs to. Seams may
	// supply them, but CheckBudget resolves them from AgentRef only when an enforcing
	// agent_group budget exists.
	AgentGroupRefs []string
}

// BudgetCheck is the pre-flight decision: whether a prospective request is within
// the budgets that scope it, and if not, the most restrictive enforcing action.
type BudgetCheck struct {
	Allowed       bool   `json:"allowed"`
	Action        string `json:"action,omitempty"` // "" when allowed; else throttle|block
	BudgetID      string `json:"budget_id,omitempty"`
	BudgetName    string `json:"budget_name,omitempty"`
	SpendMicroUSD int64  `json:"spend_micro_usd,omitempty"`
	LimitMicroUSD int64  `json:"limit_micro_usd,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// CheckBudget is the PRE-FLIGHT admission seam (FIN-08). Unlike onCost — which
// accounts AFTER spend already happened and so cannot deny — this is consulted
// BEFORE a request by an actuation seam (modelrouter / orchestration HITL gate). It
// returns deny/throttle when an ENFORCING budget (action=block/throttle) that scopes
// the request is at or over its limit, counting reserved capacity toward the limit.
// Alert-only budgets never deny (showback). It fails OPEN on any error — a FinOps
// outage must not take down inference; the emitted hard-cap signal remains the
// backstop. block outranks throttle.
func (m *Module) CheckBudget(ctx context.Context, tenant model.TenantID, dims SpendDims) (BudgetCheck, error) {
	allow := BudgetCheck{Allowed: true}
	if m.data == nil {
		return allow, nil
	}
	attr := attribution{
		ProviderRef: dims.ProviderRef, ModelRef: dims.ModelRef, AgentRef: dims.AgentRef,
		SessionRef: dims.SessionRef, Team: dims.Team, Project: dims.Project,
		WorkspaceRef: dims.WorkspaceRef, APIKeyRef: dims.APIKeyRef, ServiceTier: dims.ServiceTier,
		ContextWindow: dims.ContextWindow, InferenceGeo: dims.InferenceGeo,
		Gateway: dims.Gateway, CostType: dims.CostType, IdentityRef: dims.IdentityRef,
		RoutineRef: dims.RoutineRef, CostCenterRef: dims.CostCenterRef,
		UserGroupRefs:  append([]string(nil), dims.UserGroupRefs...),
		AgentGroupRefs: append([]string(nil), dims.AgentGroupRefs...),
	}
	now := m.clock.Now().Time()
	var (
		budgets   []model.Policy
		truncated bool
	)
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var lerr error
		budgets, truncated, lerr = listAllBudgets(ctx, sc)
		return lerr
	})
	if err != nil {
		return allow, err
	}
	if truncated {
		// The budget set could not be fully enumerated (D-04): an enforcing block
		// budget could be on an unread page. Deny-closed with an EXPLICIT block rather
		// than let spend proceed uncapped — never a silent allow, and never an error the
		// caller's fail-open contract would turn into an allow.
		return BudgetCheck{
			Allowed: false, Action: "block",
			Reason: "budget set truncated at scan cap; enforced fail-closed",
		}, nil
	}
	userGroups := resolveMatchingUserGroupMembers(ctx, authGroupReader(m.data), tenant, budgets, attr)
	result := allow
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		// Resolve the FIRM identity once, only when an enforcing identity-scoped budget
		// actually exists and the seam did not already supply it — so the common path
		// (no identity budget) pays no roster lookup. It runs inside the fail-open View,
		// so a resolve error fails open exactly like any other read error (below).
		if attr.IdentityRef == "" && hasEnforcingIdentityBudget(budgets) {
			if err := resolveIdentity(ctx, sc, &attr); err != nil {
				return err
			}
		}
		// Resolve agent-group memberships only when an enforcing agent_group budget
		// exists and the seam did not already supply the acting agent's groups. The
		// authored budget key is the AgentGroup.Slug (the same stable handle used by
		// model/source-scope policies), with id accepted only as a lookup fallback.
		if len(attr.AgentGroupRefs) == 0 && attr.AgentRef != "" && hasEnforcingGroupBudget(budgets, "agent_group") {
			if err := resolveAgentGroups(ctx, sc, &attr); err != nil {
				return err
			}
		}
		for _, p := range budgets {
			if !p.Enabled {
				continue
			}
			spec := parseBudgetSpec(p.Spec)
			spec.fillDefaults()
			if spec.Action == budgetActionAlert || !spec.matches(attr) {
				continue // alert-only never denies; non-matching budgets don't apply
			}
			pStart, hasLower := periodStart(spec.Period, now)
			pEnd := periodEnd(spec.Period, pStart)
			var (
				agg aggResult
				err error
			)
			if isGroupDimension(spec.Dimension) {
				agg, err = aggregateGroupPeriod(ctx, sc, userGroups.get, spec.Dimension, spec.Key, pStart, hasLower, pEnd, hasLower)
			} else {
				agg, err = aggregatePeriod(ctx, sc, spec.sampleFilters(), pStart, hasLower, pEnd, hasLower)
			}
			if err != nil {
				if isGroupDimension(spec.Dimension) && spec.FailClosed {
					if result.Allowed || (spec.Action == "block" && result.Action == "throttle") {
						action := spec.Action
						if action == "" {
							action = "block"
						}
						result = BudgetCheck{
							Allowed: false, Action: action,
							BudgetID: p.ID.String(), BudgetName: p.Name,
							LimitMicroUSD: spec.LimitMicroUSD,
							Reason:        "group budget check failed (fail-closed)",
						}
					}
					continue
				}
				return err
			}
			if agg.Truncated {
				if result.Allowed || (spec.Action == "block" && result.Action == "throttle") {
					result = BudgetCheck{
						Allowed: false, Action: spec.Action,
						BudgetID: p.ID.String(), BudgetName: p.Name,
						SpendMicroUSD: agg.Cost, LimitMicroUSD: spec.LimitMicroUSD,
						Reason: "budget aggregate truncated at scan cap; enforced fail-closed",
					}
				}
				continue
			}
			// Fold the DYNAMIC reserve ledger into effective consumption: an
			// in-flight request that already RESERVED headroom counts against the limit
			// here too, so the pre-flight denial reflects reservations, not only settled
			// spend. Static ReservedMicroUSD (Priority-Tier capacity) is a separate line.
			// The version-aware hold reader over this budget's real window (D02): the
			// preventive check and the atomic reserve must bind the same obligations,
			// and an imported v1 hold is one of them.
			dyn, err := heldReservedForWindow(ctx, sc, p.ID, spec.Key, pStart, pEnd, hasLower, now)
			if err != nil {
				return err
			}
			// The reservation ledger could not be enumerated (prefix, unusable cursor,
			// malformed row) or its total does not fit in an int64: the effective
			// consumption has no established bound. Deny EXPLICITLY, in the same shape
			// the truncated aggregate above uses — returning an error here would reach
			// a seam documented to fail OPEN and become an admission, and admitting on
			// the partial figure would hand out headroom that may not exist.
			effective := sumInt64(agg.Cost, spec.ReservedMicroUSD, dyn.MicroUSD)
			if !dyn.established() || !effective.OK {
				if result.Allowed || (spec.Action == "block" && result.Action == "throttle") {
					reason := fmt.Sprintf("budget %q reserved total could not be established (%s); enforced fail-closed", p.Name, dyn.Incomplete)
					if !effective.OK {
						reason = fmt.Sprintf("budget %q effective consumption is not representable; enforced fail-closed", p.Name)
					}
					result = BudgetCheck{
						Allowed: false, Action: spec.Action,
						BudgetID: p.ID.String(), BudgetName: p.Name,
						SpendMicroUSD: agg.Cost, LimitMicroUSD: spec.LimitMicroUSD,
						Reason: reason,
					}
				}
				continue
			}
			if effective.Value < spec.LimitMicroUSD {
				continue // within budget
			}
			// Over the (reservation-adjusted) limit under an enforcing action.
			if result.Allowed || (spec.Action == "block" && result.Action == "throttle") {
				result = BudgetCheck{
					Allowed: false, Action: spec.Action,
					BudgetID: p.ID.String(), BudgetName: p.Name,
					SpendMicroUSD: agg.Cost, LimitMicroUSD: spec.LimitMicroUSD,
					Reason: fmt.Sprintf("budget %q %s cap reached (%s)", p.Name, spec.Action, spec.Period),
				}
			}
		}
		return nil
	})
	if err != nil {
		if !result.Allowed {
			// A refusal was ESTABLISHED before this read failed, and the loop only
			// carried on so a block could outrank a throttle. The failure says nothing
			// about the budget that already refused, so returning the outage posture
			// here (allow plus the error) would hand a decided refusal to a seam
			// documented to fail OPEN — the refusal is not undone by a later outage.
			// The refusal leaves with a NIL error, like every other normal deny, so a
			// caller keying on the error alone cannot read it as an outage either.
			//
			// With nothing decided, the line below keeps the declared posture exactly.
			return result, nil
		}
		return allow, err // fail open: never block inference on a FinOps read error
	}
	return result, nil
}

// BudgetCapTarget is the read-only seam the FinOps defense-in-depth backstop uses to
// turn a finops_budget_cap finding — whose subject is the BUDGET id, not the offending
// resource — into the concrete upstream subject to actuate on. Given a budget id it
// returns the budget's spend dimension and its scoping key (e.g. dimension "api_key", key
// "apikey_123"; or "workspace", key "wrkspc_01"). ok is false when the id is not a budget
// (or was deleted between the cap finding and this lookup). It carries NO money — only the
// scoping references the backstop needs to bind a governed actuation — and fails CLOSED:
// a real read error is returned, not swallowed (the backstop then declines to actuate).
func (m *Module) BudgetCapTarget(ctx context.Context, tenant model.TenantID, budgetID string) (dimension, key string, ok bool, err error) {
	if m.data == nil {
		return "", "", false, nil
	}
	id := model.ID(budgetID)
	if id.IsZero() {
		return "", "", false, nil
	}
	verr := m.data.View(ctx, tenant, func(sc store.Scope) error {
		p, gerr := sc.Policies().Get(ctx, id)
		if gerr != nil {
			return gerr
		}
		if p.Kind != policyKindBudget {
			return nil // a non-budget policy id: ok stays false
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		dimension, key, ok = spec.Dimension, spec.Key, true
		return nil
	})
	if verr != nil {
		if errors.Is(verr, store.ErrNotFound) {
			return "", "", false, nil // deleted between the cap and the lookup — not an error
		}
		return "", "", false, verr
	}
	return dimension, key, ok, nil
}

// hasEnforcingIdentityBudget reports whether any enabled, ENFORCING (throttle|block)
// budget scopes on the firm identity dimension — the gate that decides whether
// CheckBudget pays for the roster identity resolution on the pre-flight path.
func hasEnforcingIdentityBudget(budgets []model.Policy) bool {
	for _, p := range budgets {
		if !p.Enabled {
			continue
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		if spec.Dimension == "identity" && spec.Action != budgetActionAlert {
			return true
		}
	}
	return false
}

// groupAuthReader is an optional, composition-root-provided extension to the normal
// tenant-scoped ModuleData handle. It lets FinOps read directory-group membership
// for user_group budget fan-out without widening core/api.ModuleData for every module.
type groupAuthReader interface {
	AuthView(context.Context, func(store.AuthScope) error) error
}

func authGroupReader(d any) groupAuthReader {
	r, _ := d.(groupAuthReader)
	return r
}

// hasEnforcingGroupBudget reports whether any enabled enforcing budget scopes on
// a group dimension, so CheckBudget only pays group-resolution costs on the gated path.
func hasEnforcingGroupBudget(budgets []model.Policy, dimension string) bool {
	for _, p := range budgets {
		if !p.Enabled {
			continue
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		if spec.Dimension == dimension && spec.Action != budgetActionAlert {
			return true
		}
	}
	return false
}

func isGroupDimension(dim string) bool {
	return dim == "user_group" || dim == "agent_group"
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// resolveAgentGroups resolves attr.AgentRef to the agent-group slugs it belongs to.
// Agent-group budget keys are authored as AgentGroup.Slug, matching the model/source
// scope policy surfaces. Empty slug falls back to the group id only for legacy rows.
func resolveAgentGroups(ctx context.Context, sc store.Scope, attr *attribution) error {
	agentID, ok, err := resolveAgentID(ctx, sc, attr.AgentRef)
	if err != nil || !ok {
		return err
	}
	members, err := listAgentGroupMembersByAgent(ctx, sc, agentID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	refs := make([]string, 0, len(members))
	for _, member := range members {
		g, err := sc.AgentGroups().Get(ctx, member.GroupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		ref := g.Slug
		if ref == "" {
			ref = g.ID.String()
		}
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	attr.AgentGroupRefs = refs
	return nil
}

func userGroupMemberRefs(ctx context.Context, reader groupAuthReader, tenant model.TenantID, key string) ([]string, error) {
	groupID, err := model.ParseID(key)
	if err != nil {
		return nil, fmt.Errorf("finops: user_group key %q must be a group id: %w", key, err)
	}
	var refs []string
	err = reader.AuthView(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Get(ctx, groupID)
		if err != nil {
			return err
		}
		if g.TargetTenantID != tenant {
			return fmt.Errorf("finops: user_group %q is not scoped to tenant %s", key, tenant)
		}
		seen := map[string]bool{}
		q := model.Query{Filters: []model.Filter{eq("group_id", groupID.String())}, Limit: listCap}
		for {
			members, page, err := as.GroupMembers().List(ctx, q)
			if err != nil {
				return err
			}
			for _, member := range members {
				ref := member.UserID.String()
				if ref == "" || seen[ref] {
					continue
				}
				seen[ref] = true
				refs = append(refs, ref)
			}
			if !page.HasMore || page.Cursor == "" {
				sort.Strings(refs)
				return nil
			}
			q.Cursor = page.Cursor
		}
	})
	return refs, err
}

type userGroupMemberLookup struct {
	refs map[string][]string
	errs map[string]error
}

func (l userGroupMemberLookup) get(key string) ([]string, error) {
	if err := l.errs[key]; err != nil {
		return nil, err
	}
	if refs, ok := l.refs[key]; ok {
		return refs, nil
	}
	return nil, fmt.Errorf("finops: user_group %q members were not resolved", key)
}

// resolveMatchingUserGroupMembers resolves only the user_group budgets that already
// match the request's supplied group refs. The cost read-model has no group column;
// the later tenant scan fans out across these user ids via the actor column.
func resolveMatchingUserGroupMembers(ctx context.Context, reader groupAuthReader, tenant model.TenantID, budgets []model.Policy, attr attribution) userGroupMemberLookup {
	out := userGroupMemberLookup{refs: map[string][]string{}, errs: map[string]error{}}
	if len(attr.UserGroupRefs) == 0 || !hasEnforcingGroupBudget(budgets, "user_group") {
		return out
	}
	for _, p := range budgets {
		if !p.Enabled {
			continue
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		if spec.Dimension != "user_group" || spec.Action == budgetActionAlert || !contains(attr.UserGroupRefs, spec.Key) {
			continue
		}
		if _, done := out.refs[spec.Key]; done {
			continue
		}
		if _, failed := out.errs[spec.Key]; failed {
			continue
		}
		if reader == nil {
			out.errs[spec.Key] = fmt.Errorf("finops: user_group %q cannot resolve members without auth reader", spec.Key)
			continue
		}
		refs, err := userGroupMemberRefs(ctx, reader, tenant, spec.Key)
		if err != nil {
			out.errs[spec.Key] = err
			continue
		}
		out.refs[spec.Key] = refs
	}
	return out
}

// severityForPct grades an alert: at/over the limit is high, near it medium, else
// low (an early heads-up).
func severityForPct(pct int) model.Severity {
	switch {
	case pct >= 100:
		return model.SeverityHigh
	case pct >= 80:
		return model.SeverityMedium
	default:
		return model.SeverityLow
	}
}
