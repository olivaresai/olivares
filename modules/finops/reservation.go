// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// reservationTTL is how long a dynamic reservation holds headroom before it
// self-expires. It is the CRASH backstop, not the normal path: a completed
// actuation calls CommitReservation, a failed one ReleaseReservation, so the TTL
// only reclaims headroom leaked by a caller that died mid-call. It is a package
// var (not a const) so a test can shrink it; production keeps the default, which
// must comfortably exceed the slowest governed actuation (a long completion) so a
// still-running request is never dropped from the ceiling. See the ADR.
var reservationTTL = 5 * time.Minute

// maxReserveRetries bounds the optimistic-concurrency retry loop. Concurrent
// reservers on the SAME (policy, period) collide on the monotonic seq UNIQUE
// index; each round at least one wins, so the loser retries. The bound scales
// past any realistic per-policy concurrency; exhausting it returns ErrConflict,
// which the actuation seam treats as a FinOps read error and fails OPEN (per
// CheckBudget's contract) — an over-reserve is impossible, a rare exhausted
// retry just declines to reserve.
const maxReserveRetries = 64

// errReservationDenied is an internal sentinel: a target has no headroom for the
// estimate. It is returned from the Mutate closure to ROLL BACK the transaction
// (so no partial reservation rows survive when one of several budgets denies),
// and the caller unwraps it into a normal Allowed:false result — never an error.
var errReservationDenied = errors.New("finops: reservation denied")

// BudgetReservation is the outcome of a pre-flight RESERVE. When Allowed, Handle
// identifies the reservation so the actuation seam can CommitReservation (on
// success, stamping the actual cost) or ReleaseReservation (on failure/timeout).
// When not Allowed it names the binding policy exactly like BudgetCheck, so the
// seam can surface the same deny reason. It is the atomic replacement for the
// check→act race: the headroom is consumed the instant the reservation commits,
// so N concurrent requests can no longer all pass a stale read.
type BudgetReservation struct {
	Allowed          bool   `json:"allowed"`
	Handle           string `json:"handle,omitempty"`
	Action           string `json:"action,omitempty"`
	BudgetID         string `json:"budget_id,omitempty"`
	BudgetName       string `json:"budget_name,omitempty"`
	SpendMicroUSD    int64  `json:"spend_micro_usd,omitempty"`
	ReservedMicroUSD int64  `json:"reserved_micro_usd,omitempty"`
	LimitMicroUSD    int64  `json:"limit_micro_usd,omitempty"`
	EstimateMicroUSD int64  `json:"estimate_micro_usd,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// reservationTarget is one policy the request must fit within: its resolved
// ceiling, its period bucket, and how to read its current committed spend. The
// reserve evaluates ceiling = spend + staticReserved + active-reserved and only
// admits when there is room for estimate.
type reservationTarget struct {
	policyID   model.ID
	policyKind string
	name       string
	action     string
	dimension  string
	// scopeKey isolates one policy's independent subjects (a budget's dimension
	// key, or an actor for a per-seat spend limit). "" is a valid, concrete scope.
	scopeKey       string
	period         string
	periodStart    time.Time
	ceiling        int64
	staticReserved int64
	// spend reads this target's committed spend for its period, returning the
	// aggregate and whether it was scan-truncated (a truncated aggregate is only a
	// lower bound, so it forces a fail-closed deny, exactly like CheckBudget).
	spend func(ctx context.Context, sc store.Scope) (aggResult, error)
	// failClosed marks a target whose spend read must produce a DENY rather than
	// bubble up as an error the caller fails open on. It is set for a group budget
	// that declared fail_closed: its members may be unresolvable, and the operator
	// already said what that should mean.
	failClosed bool
}

// ReserveBudget is the ATOMIC pre-flight admission that closes the TOCTOU race in
// CheckBudget. For every enabled ENFORCING (throttle|block) budget that scopes the
// request, it reserves estimate micro-USD against the budget's remaining headroom
// (spend + static reserved + already-active reservations) in a single transaction.
// If any budget lacks room, NOTHING is reserved and the most restrictive deny is
// returned; otherwise one active reservation row is written per budget and a Handle
// is returned. Fails the same way CheckBudget does — a read error bubbles up so the
// seam can fail OPEN — and block outranks throttle.
//
// GROUP DIMENSIONS ARE RESERVED HERE TOO, and they did not use to be. The original
// ledger skipped user_group and agent_group because their spend is a member fan-out
// rather than a column, and left them on CheckBudget's read-only preventive path.
// A skipped dimension is not a smaller race: it is the SAME race. Measured on a
// group budget affording 7, eight concurrent requests were ALL admitted, because
// every one of them read a spend read-model that none had yet written. The decision
// to close it is the register's YA-97.
//
// What the fan-out changes is only WHERE the spend comes from: the reservation rows,
// the monotonic seq, the OCC retry and the all-or-none rollback are identical, so a
// group budget consumes headroom the instant its reservation commits exactly like a
// per-model one.
func (m *Module) ReserveBudget(ctx context.Context, tenant model.TenantID, dims SpendDims, estimateMicroUSD int64) (BudgetReservation, error) {
	if m.data == nil {
		return BudgetReservation{Allowed: true}, nil
	}
	if estimateMicroUSD < 0 {
		return BudgetReservation{}, fmt.Errorf("finops: reservation estimate must not be negative")
	}
	now := m.clock.Now().Time()
	attr := attributionFromDims(dims)

	// EVERY page, and a truncation is a DENY — the same enumeration CheckBudget does.
	//
	// This read used to take one page and drop `HasMore` on the floor while CheckBudget drained
	// the keyset and denied on a bounded-scan truncation. Measured by the contrast with 1,001
	// policies: CheckBudget denied and ReserveBudget admitted. The claim that the two admission
	// paths bind the same budgets was therefore FALSE, and a single unread page is enough for an
	// enforcing block budget to be invisible to the reservation that is supposed to be the
	// atomic one.
	var (
		budgets   []model.Policy
		truncated bool
	)
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var lerr error
		budgets, truncated, lerr = listAllBudgets(ctx, sc)
		return lerr
	}); err != nil {
		return BudgetReservation{Allowed: true}, err
	}
	if truncated {
		// Explicit deny, never a silent allow and never an error the caller's fail-open
		// contract would turn into one — exactly what CheckBudget does with the same fact.
		return BudgetReservation{
			Allowed: false, Action: "block",
			Reason: "budget set truncated at scan cap; enforced fail-closed",
		}, nil
	}

	// Resolve the firm identity once, only when an enforcing identity budget exists
	// and the seam did not supply it — the same gate CheckBudget uses, so the two
	// admission paths match the same budgets.
	if attr.IdentityRef == "" && hasEnforcingIdentityBudget(budgets) {
		if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
			return resolveIdentity(ctx, sc, &attr)
		}); err != nil {
			return BudgetReservation{Allowed: true}, err
		}
	}

	// Resolve the acting agent's groups on the same condition CheckBudget uses, so the
	// two admission paths match the SAME budgets. Without it a reservation would silently
	// not scope an agent_group budget the preventive check does scope, and the two would
	// disagree about which policies bind the request.
	if len(attr.AgentGroupRefs) == 0 && attr.AgentRef != "" && hasEnforcingGroupBudget(budgets, "agent_group") {
		if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
			return resolveAgentGroups(ctx, sc, &attr)
		}); err != nil {
			return BudgetReservation{Allowed: true}, err
		}
	}
	// And the user-group memberships the fan-out aggregates over. The lookup is built
	// ONCE, outside the reservation transaction, because it reads the directory through a
	// different scope (AuthView) than the ledger's Mutate — exactly as CheckBudget does.
	userGroups := resolveMatchingUserGroupMembers(ctx, authGroupReader(m.data), tenant, budgets, attr)

	targets := make([]reservationTarget, 0, len(budgets))
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
		// The spend reader is chosen by DIMENSION, and this is the only place the group
		// dimensions differ from the rest: their spend has no column of its own, so it is
		// summed over the members the directory resolves. Handing a group target the
		// column filters instead would read zero on a ledger whose members are over the
		// cap, and admit.
		var read func(ctx context.Context, sc store.Scope) (aggResult, error)
		if isGroupDimension(spec.Dimension) {
			dimension, key := spec.Dimension, spec.Key
			read = func(ctx context.Context, sc store.Scope) (aggResult, error) {
				return aggregateGroupPeriod(ctx, sc, userGroups.get, dimension, key, pStart, hasLower, pEnd, hasLower)
			}
		} else {
			filters := spec.sampleFilters()
			read = func(ctx context.Context, sc store.Scope) (aggResult, error) {
				return aggregatePeriod(ctx, sc, filters, pStart, hasLower, pEnd, hasLower)
			}
		}
		targets = append(targets, reservationTarget{
			policyID: p.ID, policyKind: policyKindBudget, name: p.Name, action: spec.Action,
			dimension: spec.Dimension, scopeKey: spec.Key, period: spec.Period, periodStart: pStart,
			ceiling: spec.LimitMicroUSD, staticReserved: spec.ReservedMicroUSD,
			spend: read,
			// A group budget that DECLARED fail-closed must deny when its members cannot be
			// resolved. CheckBudget turns exactly that failure into a normal deny; this path
			// used to return an allowed result carrying the error, which the seam's documented
			// fail-open handling then turns into an admission — the opposite of what the
			// operator configured. Measured by the contrast against the missing-group fixture.
			failClosed: isGroupDimension(spec.Dimension) && spec.FailClosed,
		})
	}
	return m.reserve(ctx, tenant, targets, estimateMicroUSD, now)
}

// ReserveSpendLimit is the per-seat analog of ReserveBudget: it atomically
// reserves estimate against the resolved apps-gateway spend cap for each period
// (daily/weekly/monthly). The reservation is keyed by the ACTOR (scopeKey), so a
// cap sourced from an org/group policy reserves each seat's headroom
// independently — the same per-actor semantics CheckSpendLimit enforces. It
// closes the identical TOCTOU race for per-seat limits.
func (m *Module) ReserveSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string, estimateMicroUSD int64) (BudgetReservation, error) {
	if m.data == nil {
		return BudgetReservation{Allowed: true}, nil
	}
	if estimateMicroUSD < 0 {
		return BudgetReservation{}, fmt.Errorf("finops: reservation estimate must not be negative")
	}
	now := m.clock.Now().Time()
	var policies []model.Policy
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var lerr error
		policies, lerr = listSpendLimitPolicies(ctx, sc)
		return lerr
	}); err != nil {
		return BudgetReservation{Allowed: true}, err
	}
	var targets []reservationTarget
	for _, period := range []string{"daily", "weekly", "monthly"} {
		resolved := resolveSpendLimitForPeriod(policies, actorRef, period, groups)
		if resolved.policy == nil || resolved.spec.Unlimited {
			continue
		}
		pStart, _ := periodStart(period, now)
		pEnd := periodEnd(period, pStart)
		targets = append(targets, reservationTarget{
			policyID: resolved.policy.ID, policyKind: policyKindSpendLimit,
			name: spendLimitWireID(resolved.policy.ID), action: "block",
			dimension: "spend_limit", scopeKey: actorRef, period: period, periodStart: pStart,
			ceiling: resolved.spec.AmountMicroUSD,
			spend: func(ctx context.Context, sc store.Scope) (aggResult, error) {
				return aggregatePeriod(ctx, sc, []model.Filter{eq(colActor, actorRef)}, pStart, true, pEnd, true)
			},
		})
	}
	return m.reserve(ctx, tenant, targets, estimateMicroUSD, now)
}

// reserve runs the atomic multi-target reservation with the optimistic-concurrency
// retry loop. All targets are reserved in ONE transaction (all-or-nothing): if any
// denies, the transaction rolls back and no reservation survives; if any INSERT
// hits the seq UNIQUE index (a concurrent reserver won the race), the whole
// transaction retries and re-reads the now-committed state.
func (m *Module) reserve(ctx context.Context, tenant model.TenantID, targets []reservationTarget, estimate int64, now time.Time) (BudgetReservation, error) {
	if len(targets) == 0 {
		return BudgetReservation{Allowed: true}, nil // no enforcing budget scopes the request
	}
	handle := model.NewID()
	expires := now.Add(reservationTTL)
	var lastErr error
	for attempt := 0; attempt < maxReserveRetries; attempt++ {
		var result BudgetReservation
		err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			result = BudgetReservation{Allowed: true, Handle: handle.String(), EstimateMicroUSD: estimate}
			repo, err := sc.Ext(budgetReservationKind)
			if err != nil {
				return err
			}
			denied := false
			// One pass per target, insert-as-we-go: a later target's denial rolls the
			// whole transaction back (errReservationDenied), discarding any row already
			// inserted for an earlier target — so a multi-budget reserve is all-or-nothing
			// without a pre-pass. We still visit EVERY target so block can outrank throttle.
			for _, tg := range targets {
				// ORDERING INVARIANT: read the max seq BEFORE the reserved sum, and
				// insert with THAT seq. A successful INSERT (no seq collision) proves the
				// seq we read was the true committed max, hence the sum — read strictly
				// later — saw every prior reservation (seq is monotonic per bucket).
				// Reversing the two reads reopens the race on Postgres READ COMMITTED: a
				// stale sum paired with a fresh, non-colliding seq would over-admit.
				maxSeq, err := maxReservationSeq(ctx, repo, tg.policyID, tg.scopeKey, tg.periodStart)
				if err != nil {
					return err
				}
				agg, err := tg.spend(ctx, sc)
				if err != nil {
					if !tg.failClosed {
						return err
					}
					// The operator declared fail-closed for this group. Record the deny the
					// same way a no-headroom target does, so the transaction rolls back and
					// the caller gets a refusal instead of an error it would fail open on.
					action := tg.action
					if action == "" {
						action = "block"
					}
					result = BudgetReservation{
						Allowed: false, Action: action,
						BudgetID: tg.policyID.String(), BudgetName: tg.name,
						LimitMicroUSD: tg.ceiling, EstimateMicroUSD: estimate,
						Reason: "group budget check failed (fail-closed)",
					}
					return errReservationDenied
				}
				reserved, err := activeReservedMicroUSD(ctx, repo, tg.policyID, tg.scopeKey, tg.periodStart, now)
				if err != nil {
					return err
				}
				effective := agg.Cost + tg.staticReserved + reserved
				// A truncated spend aggregate is a lower bound: fail closed (deny), the
				// same posture CheckBudget takes on truncation.
				if agg.Truncated || effective+estimate > tg.ceiling {
					denied = true
					if result.Allowed || (tg.action == "block" && result.Action == "throttle") {
						reason := fmt.Sprintf("budget %q %s cap reached (%s): no headroom to reserve %d µUSD", tg.name, tg.action, tg.period, estimate)
						if agg.Truncated {
							reason = fmt.Sprintf("budget %q aggregate truncated at scan cap; reservation denied fail-closed", tg.name)
						}
						result = BudgetReservation{
							Allowed: false, Action: tg.action, BudgetID: tg.policyID.String(), BudgetName: tg.name,
							SpendMicroUSD: agg.Cost, ReservedMicroUSD: reserved, LimitMicroUSD: tg.ceiling,
							EstimateMicroUSD: estimate, Reason: reason,
						}
					}
					continue // do not reserve this target; keep going for block>throttle
				}
				if denied {
					// An earlier target already denied — the whole reservation rolls
					// back at the end. Skip the insert: a spurious seq conflict on a
					// doomed target must not retry a decided denial into the fail-open
					// admit under contention (adversarial review finding).
					continue
				}
				rec := model.Record{
					colResvPolicyRef:   tg.policyID.String(),
					colResvPolicyKind:  tg.policyKind,
					colResvDimension:   tg.dimension,
					colResvScopeKey:    tg.scopeKey,
					colResvPeriod:      tg.period,
					colResvPeriodStart: model.NewTimestamp(tg.periodStart).String(),
					colResvSeq:         maxSeq + 1,
					colResvAmount:      estimate,
					colResvActual:      int64(0),
					colResvState:       resvStateActive,
					colResvHandle:      handle.String(),
					colResvExpiresAt:   model.NewTimestamp(expires).String(),
				}
				if _, err := repo.Create(ctx, rec); err != nil {
					return err // ErrConflict on the seq UNIQUE index → retry the whole tx
				}
			}
			if denied {
				return errReservationDenied // roll back: no partial reservation persists
			}
			return nil
		})
		switch {
		case errors.Is(err, errReservationDenied):
			return result, nil // a normal "no" — the roll-back is intentional
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue // lost the seq race; re-read committed state and retry
		case err != nil:
			return BudgetReservation{Allowed: true}, err // read error → caller fails open
		default:
			return result, nil
		}
	}
	return BudgetReservation{Allowed: true}, fmt.Errorf("finops: reservation retries exhausted: %w", lastErr)
}

// CommitReservation settles a reservation after the actuation completed: every
// still-active row under the handle flips to committed (stamped with the actual
// cost) and so leaves the active-reserved sum. The ACTUAL spend is recorded
// separately through the normal cost-sample ingest; committing the reservation is
// what prevents a double count (estimate + actual) once that spend lands. It is
// idempotent — a row already settled is skipped — and returns ErrNotFound only if
// the handle never existed. See the ADR on ordering (ingest the actual spend,
// then commit, to avoid a transient under-count).
func (m *Module) CommitReservation(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error {
	return m.settleReservation(ctx, tenant, handle, resvStateCommitted, actualMicroUSD)
}

// ReleaseReservation returns a reservation's headroom on failure/timeout: every
// still-active row under the handle flips to released and leaves the sum. No spend
// is recorded. Idempotent.
func (m *Module) ReleaseReservation(ctx context.Context, tenant model.TenantID, handle string) error {
	return m.settleReservation(ctx, tenant, handle, resvStateReleased, 0)
}

func (m *Module) settleReservation(ctx context.Context, tenant model.TenantID, handle string, state string, actual int64) error {
	if m.data == nil {
		return nil
	}
	id, err := model.ParseID(handle)
	if err != nil || id.IsZero() {
		return store.ErrNotFound
	}
	now := m.clock.Now()
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, err := scanReservations(ctx, repo, []model.Filter{eq(colResvHandle, id.String())})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return store.ErrNotFound
		}
		for _, r := range rows {
			if r.String(colResvState) != resvStateActive {
				continue // already settled — idempotent
			}
			r[colResvState] = state
			r[colResvSettledAt] = now.String()
			if state == resvStateCommitted {
				r[colResvActual] = actual
			}
			if _, err := repo.Update(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

// SweepExpiredReservations flips active-but-expired rows to expired. It is
// HYGIENE ONLY: the ceiling sum already excludes rows whose expires_at has passed
// (activeReservedMicroUSD filters expires_at > now), so an expired reservation
// stops holding headroom the instant it expires — there is no counter to decrement
// and thus no way to double-count. The sweep only makes the terminal state
// explicit (for observability and eventual GC). Returns the number swept.
func (m *Module) SweepExpiredReservations(ctx context.Context, tenant model.TenantID) (int, error) {
	if m.data == nil {
		return 0, nil
	}
	now := m.clock.Now()
	swept := 0
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		swept = 0
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, err := scanReservations(ctx, repo, []model.Filter{
			eq(colResvState, resvStateActive),
			{Column: colResvExpiresAt, Op: model.OpLte, Value: now.String()},
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			r[colResvState] = resvStateExpired
			r[colResvSettledAt] = now.String()
			if _, err := repo.Update(ctx, r); err != nil {
				if errors.Is(err, store.ErrConflict) {
					continue // a concurrent settle won; not our row to sweep
				}
				return err
			}
			swept++
		}
		return nil
	})
	return swept, err
}

// activeReservedMicroUSD sums the headroom held by live reservations for one
// policy+scope+period: state=active AND expires_at > now. The expiry predicate is
// what makes expiry safe — a lapsed reservation is simply not summed, so no
// decrement bookkeeping (and no double-count) is ever needed.
func activeReservedMicroUSD(ctx context.Context, repo store.GenericRepo, policyID model.ID, scopeKey string, periodStart, now time.Time) (int64, error) {
	rows, err := scanReservations(ctx, repo, []model.Filter{
		eq(colResvPolicyRef, policyID.String()),
		eq(colResvScopeKey, scopeKey),
		eq(colResvPeriodStart, model.NewTimestamp(periodStart).String()),
		eq(colResvState, resvStateActive),
		{Column: colResvExpiresAt, Op: model.OpGt, Value: model.NewTimestamp(now).String()},
	})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, r := range rows {
		total += r.Int(colResvAmount)
	}
	return total, nil
}

// dynamicReservedMicroUSD is the scope-level convenience over
// activeReservedMicroUSD: it opens the reservation repo and returns the live
// reserved headroom for a policy+period. It is what folds the DYNAMIC reserve
// ledger into the effective consumption everywhere the ceiling is evaluated
// (CheckBudget, budgetStatus, evaluateBudgets), so the pre-flight denial, the
// alert/cap signal and the status DTO all agree on spend + static + dynamic.
func dynamicReservedMicroUSD(ctx context.Context, sc store.Scope, policyID model.ID, scopeKey string, periodStart, now time.Time) (int64, error) {
	repo, err := sc.Ext(budgetReservationKind)
	if err != nil {
		return 0, err
	}
	return activeReservedMicroUSD(ctx, repo, policyID, scopeKey, periodStart, now)
}

// maxReservationSeq returns the largest seq issued for a policy+period across ALL
// states (so the seq is globally monotonic per bucket and the UNIQUE index is a
// true serialization token — a reused seq must never collide with a settled row).
// 0 means the bucket is empty.
func maxReservationSeq(ctx context.Context, repo store.GenericRepo, policyID model.ID, scopeKey string, periodStart time.Time) (int64, error) {
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			eq(colResvPolicyRef, policyID.String()),
			eq(colResvScopeKey, scopeKey),
			eq(colResvPeriodStart, model.NewTimestamp(periodStart).String()),
		},
		Sort:  []model.Sort{{Column: colResvSeq, Desc: true}},
		Limit: 1,
	})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Int(colResvSeq), nil
}

// scanReservations pages the reservation rows matching filters, bounded by the
// same scan cap the cost read-model uses so a pathological bucket cannot spin
// forever. It uses the default id keyset cursor (no custom sort), so paging is
// exact.
func scanReservations(ctx context.Context, repo store.GenericRepo, filters []model.Filter) ([]model.Record, error) {
	q := model.Query{Filters: filters, Limit: listCap}
	var out []model.Record
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore {
			return out, nil
		}
		if pages+1 >= maxScanPages {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// attributionFromDims builds the evaluation attribution from the pre-flight dims,
// mirroring CheckBudget so the reserve matches exactly the budgets the check does.
func attributionFromDims(dims SpendDims) attribution {
	return attribution{
		ProviderRef: dims.ProviderRef, ModelRef: dims.ModelRef, AgentRef: dims.AgentRef,
		SessionRef: dims.SessionRef, Team: dims.Team, Project: dims.Project,
		WorkspaceRef: dims.WorkspaceRef, APIKeyRef: dims.APIKeyRef, ServiceTier: dims.ServiceTier,
		ContextWindow: dims.ContextWindow, InferenceGeo: dims.InferenceGeo,
		Gateway: dims.Gateway, CostType: dims.CostType, IdentityRef: dims.IdentityRef,
		RoutineRef: dims.RoutineRef, CostCenterRef: dims.CostCenterRef,
		UserGroupRefs:  append([]string(nil), dims.UserGroupRefs...),
		AgentGroupRefs: append([]string(nil), dims.AgentGroupRefs...),
	}
}
