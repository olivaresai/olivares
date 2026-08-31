// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
func (m *Module) evaluateBudgets(ctx context.Context, sc store.Scope, attr attribution) ([]pendingAlert, error) {
	// Drain ALL budget pages (D-04): a single Limit:listCap page silently ignored
	// budgets past the first page, so a threshold crossing on such a budget never
	// alerted. The accounting path cannot deny (post-spend), so a truncation past the
	// bounded page budget is not fail-closed here; the pre-flight CheckBudget gate is.
	budgets, _, err := listAllBudgets(ctx, sc)
	if err != nil {
		return nil, err
	}
	var pending []pendingAlert
	for _, p := range budgets {
		if !p.Enabled {
			continue
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		if !spec.matches(attr) {
			continue
		}
		pStart, hasLower := periodStart(spec.Period, attr.OccurredAt)
		pEnd := periodEnd(spec.Period, pStart)
		agg, err := aggregatePeriod(ctx, sc, spec.sampleFilters(), pStart, hasLower, pEnd, hasLower)
		if err != nil {
			return nil, err
		}
		// Reserved capacity is committed against the limit, so thresholds and the
		// hard-cap fire on effective consumption (spend + static reserved + dynamic
		// reservations) — the SAME figure CheckBudget enforces, so the alert/cap signal
		// and the pre-flight denial agree.
		dyn, err := dynamicReservedMicroUSD(ctx, sc, p.ID, spec.Key, pStart, attr.OccurredAt)
		if err != nil {
			return nil, err
		}
		effective := agg.Cost + spec.ReservedMicroUSD + dyn
		for _, thr := range spec.Thresholds {
			if float64(effective) < float64(spec.LimitMicroUSD)*thr {
				continue
			}
			pct := int(thr*100 + 0.5)
			created, err := recordAlert(ctx, sc, p.ID, spec, pStart, attr.OccurredAt, pct, effective)
			if err != nil {
				return nil, err
			}
			if created {
				pending = append(pending, pendingAlert{
					BudgetID: p.ID, BudgetName: p.Name, Dimension: spec.Dimension, DimKey: spec.Key,
					Period: spec.Period, ThresholdPct: pct, Spend: effective, Limit: spec.LimitMicroUSD,
					Severity: severityForPct(pct), Action: spec.Action,
				})
			}
		}
	}
	return pending, nil
}

// recordAlert inserts the alert-history row for a crossing, returning created=false
// when this (budget, period, threshold) was already alerted (the unique guard) so
// the module alerts once per threshold per period, not on every ingest.
// triggeredAt is the time of the usage that pushed spend over the threshold (the
// sample's OccurredAt) — meaningful and deterministic, not a wall clock.
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
	finding := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    severity,
		SubjectKind: "budget",
		SubjectRef:  a.BudgetID.String(),
		Title:       title,
		DetailHash:  detailHash(a),
		OccurredAt:  m.clock.Now().Time(),
	}
	ev := event.FromObservation(tenant.String(), Name, finding)
	if err := m.host.Publish(ctx, ev); err != nil {
		m.debugf("finops: emit alert failed", "err", err)
	}
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
	dyn, derr := dynamicReservedMicroUSD(ctx, sc, p.ID, spec.Key, pStart, now)
	if derr != nil {
		return budgetStatusDTO{}, derr
	}
	effective := agg.Cost + spec.ReservedMicroUSD + dyn
	out.ProjectedMicroUSD = projectSpend(agg.Cost, spec.Period, pStart, now, hasLower) + spec.ReservedMicroUSD + dyn
	if spec.LimitMicroUSD > 0 {
		out.ConsumedPct = pctOf(effective, spec.LimitMicroUSD)
		out.RemainingMicroUSD = spec.LimitMicroUSD - effective
		out.ProjectedPct = pctOf(out.ProjectedMicroUSD, spec.LimitMicroUSD)
		out.Over = effective >= spec.LimitMicroUSD
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
			dyn, err := dynamicReservedMicroUSD(ctx, sc, p.ID, spec.Key, pStart, now)
			if err != nil {
				return err
			}
			effective := agg.Cost + spec.ReservedMicroUSD + dyn
			if effective < spec.LimitMicroUSD {
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
