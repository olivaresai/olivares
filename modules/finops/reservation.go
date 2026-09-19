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

// errReservationScanIncomplete is returned by a SETTLEMENT (commit/release) or a
// sweep whose row enumeration could not be completed. The admission paths answer
// the same fact with an explicit denial, because they have a "no" to give;
// settlement does not — its rows already exist — so the honest outcome is an
// error that transitions NOTHING and leaves the caller (or the TTL) to resolve it.
// Reporting success after moving a prefix would strand the unread rows holding
// headroom that no settlement will ever return.
var errReservationScanIncomplete = errors.New("finops: reservation enumeration incomplete")

// THE ACTIVATION FRONTIER — A NEW COMPATIBILITY REQUIREMENT ON ALL FIVE COVERED
// WRAPPERS, and it is deliberately not described as preserved legacy behavior.
//
// From the commit of BeginLifecycleActivation for a tenant, the five reservation
// wrappers of THIS binary — ReserveBudget, ReserveSpendLimit, CommitReservation,
// ReleaseReservation, SweepExpiredReservations — may no longer create or
// terminalize a covered obligation for that tenant. Each consults the durable
// frontier row inside its own transaction, under the writer lock it already takes,
// and refuses with lifecycle_api_required, changing nothing.
//
// The three answers, and the middle one is the one that costs something:
//
//   - CONFIRMED INACTIVE (a successful lookup that found no row): every wrapper
//     behaves EXACTLY as it did before this cut. This is the overwhelmingly common
//     case and it is preserved unchanged, verifier or no verifier.
//   - UNKNOWN (no data handle, an unavailable store, an unreadable or corrupt
//     row): NO ADMISSION. The wrapper refuses with Allowed=false and a typed error,
//     and writes nothing. It does NOT return the historical fail-open Allowed:true,
//     because that value is a permission, and a permission must never be produced
//     by a lookup that did not happen. THIS IS A BEHAVIOR CHANGE: on a store
//     outage these calls used to admit and now refuse.
//   - FRONTIER PRESENT: lifecycle_api_required, Allowed=false, no handle, no
//     writes, on both quiescing and active.
//
// The refusal is returned as BOTH a false Allowed and a typed error on purpose. A
// caller that inspects only the boolean is refused; a caller that inspects only the
// error gets a code it can act on; and a caller that maps errors to "fail open"
// still cannot obtain a handle, because none is issued. Migrating those callers off
// error-as-permission is a precondition of ACTIVATION, not something this guard can
// do for them.
//
// The guard requires NO evidence verifier. A confirmed-inactive tenant must keep
// working on a deployment that configures none, and the alternative would have
// switched off every legacy reservation in the product on the day this shipped.

// frontierRefusal is the reservation result a covered wrapper returns when the
// frontier forbids the mutation or could not be established. It carries no handle:
// nothing was reserved, so there is nothing to settle.
func frontierRefusal(reason string) BudgetReservation {
	return BudgetReservation{Allowed: false, Action: "block", Reason: reason}
}

// guardCoveredMutation opens a transaction purely to consult the frontier under
// the writer lock, for the covered path that has nothing else to do inside one.
// It writes nothing; the transaction exists so the read is serialized with the
// writers the boundary is about.
func (m *Module) guardCoveredMutation(ctx context.Context, tenant model.TenantID) error {
	if m.data == nil {
		return attemptErr(errCodeCapabilityUnavailable, nil)
	}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			// Typed here rather than raw: the caller classifies this refusal, and an
			// untyped store error would be indistinguishable from a frontier verdict.
			return storeErr(err)
		}
		return guardLegacyReservationMutation(ctx, sc)
	})
	if err != nil && attemptCode(err) == "" {
		// A failure of the transaction itself (it never opened, it did not commit).
		// The guard did not run, so nothing was confirmed.
		return storeErr(err)
	}
	return err
}

// frontierReasonFor renders the refusal reason for a guard outcome.
func frontierReasonFor(err error) string {
	if attemptCode(err) == errCodeLifecycleAPIRequired {
		return "tenant is under a FinOps lifecycle activation frontier; the legacy reservation API cannot create or terminalize a covered obligation"
	}
	return "tenant FinOps lifecycle activation state could not be established; reservation refused fail-closed"
}

// reservationScanState is the TYPED outcome of the reservation pagination. The
// text this loop already produced is fine for a settlement's error message, but a
// financial classification cannot be keyed on prose: A4.2 needs to tell a paging
// enumeration that stopped early (the obligations it did not read are still
// non-negative) from a row whose value contradicts the domain, and the string form
// cannot carry that distinction without being parsed.
type reservationScanState string

const (
	resvScanComplete      reservationScanState = "complete"
	resvScanCursorMissing reservationScanState = "cursor_missing"
	resvScanCursorStalled reservationScanState = "cursor_not_advancing"
	resvScanCursorCycle   reservationScanState = "cursor_cycle"
	resvScanPageCap       reservationScanState = "page_cap"
)

// complete reports that every matching row was read.
func (s reservationScanState) complete() bool { return s == resvScanComplete }

// text is the diagnostic the settlement and sweep paths have always produced. It
// stays byte-identical so their messages and their tests do not move.
func (s reservationScanState) text() string {
	switch s {
	case resvScanCursorMissing:
		return "the reservation ledger reported more rows without a cursor"
	case resvScanCursorStalled:
		return "the reservation ledger paging cursor did not advance"
	case resvScanCursorCycle:
		return "the reservation ledger paging cursor returned to a page already read"
	case resvScanPageCap:
		return "the reservation ledger scan hit the page cap with rows still to come"
	}
	return ""
}

// reservedState is the typed classification of a reserved total for the financial
// evaluation. Admission does not read it — its contract is still "established or
// not" — but the alert evidence must know WHY, because only one of the two
// not-established shapes can still carry a non-negative lower bound.
type reservedState string

const (
	// reservedKnown: the enumeration finished, every observed row was valid and in
	// scope, and the sum is representable.
	reservedKnown reservedState = "known"
	// reservedNonNegativeUnknown: the enumeration stopped early for a PAGING reason
	// and every row it did observe was valid, in scope and non-negative. Nothing
	// observed contradicts the domain invariant, so a caller may rest a lower bound
	// on the invariant — never on the prefix, which is not published as a total.
	reservedNonNegativeUnknown reservedState = "nonnegative_unknown"
	// reservedIndeterminate: something observed cannot be reconciled with the domain
	// (a negative or malformed amount, a row outside the requested scope, a sum that
	// does not fit). No bound may rest on it.
	reservedIndeterminate reservedState = "indeterminate"
)

// reservationFault names, in a closed vocabulary, the observation that made a
// reserved total indeterminate.
type reservationFault string

const (
	resvFaultNone               reservationFault = ""
	resvFaultRowAmountMalformed reservationFault = "row_amount_malformed"
	resvFaultRowAmountNegative  reservationFault = "row_amount_negative"
	resvFaultRowOutOfScope      reservationFault = "row_outside_requested_scope"
	resvFaultSumUnrepresentable reservationFault = "sum_not_representable"
	// The two tenant faults are the A4.2 correction. The row's OWN tenant evidence is
	// a separate fact from the policy/scope/period attribution: unusable evidence and
	// another tenant's well-formed identity are different observations and are named
	// as such, exactly as the strict cost reader names them.
	resvFaultRowTenantMalformed reservationFault = "row_tenant_malformed"
	resvFaultRowOtherTenant     reservationFault = "row_other_tenant"
)

// reservedTotal is the headroom held by live reservations for one policy+scope+
// period, and — when that figure could NOT be established — why.
//
// Incomplete is NEVER "a smaller total". A prefix of the ledger, a row whose
// amount is malformed and a sum that does not fit in an int64 all mean the same
// thing to an admission path: the required bound could not be established, so the
// request must be refused. It must not be refused by returning an ERROR either:
// the actuation seams treat a FinOps error as an outage and fail open, which would
// turn "I could not read the ceiling" into "there is room".
type reservedTotal struct {
	MicroUSD   int64
	Incomplete string
	// State and Scan/Fault are the TYPED form of the same facts, added by A4.2 for
	// the financial evaluation. Incomplete keeps its existing diagnostic meaning and
	// existing readers keep working: established() is still "Incomplete is empty".
	State reservedState
	Scan  reservationScanState
	Fault reservationFault
	// UnallocatedHistorical says the enumeration COMPLETED and every
	// observed row was clean, and that what could not be established is the
	// ALLOCATION of a held obligation with no accounting instant to a window that
	// closed before now. It is a temporal fact, not a scan gap and not a corruption,
	// and it exists so the alert evaluation can name the right reason from the
	// EXISTING cause vocabulary instead of borrowing "scan incomplete".
	//
	// It only ever accompanies reservedNonNegativeUnknown with a complete scan.
	UnallocatedHistorical bool
}

// established reports that MicroUSD is the exact live reserved total.
func (r reservedTotal) established() bool { return r.Incomplete == "" }

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
	scopeKey    string
	period      string
	periodStart time.Time
	// periodEnd/hasBounds are the target's REAL window, carried explicitly so the
	// hold reader never has to infer an end from "now". "total" has no lower bound
	// and therefore no window: hasBounds is false and nothing is excluded by time.
	periodEnd      time.Time
	hasBounds      bool
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
//
// # Supported scope: the transaction must be able to lock (NEW)
//
// The store.Scope this method's transaction runs on MUST implement
// store.TransactionLocker. The reservation ledger is a participating FinOps monetary
// writer: it takes this tenant's shared writer lock (see alertWriterLockKeyPrefix)
// before its decisive read, the same physical key the cost ingestion and the alert
// writer take, so a reservation cannot be decided on a ledger another FinOps writer
// is changing underneath it.
//
// This is a NEW REQUIREMENT ON THE SUPPORTED SCOPE, adjudicated after independent
// review, and it is deliberately not described as unchanged legacy behaviour. The
// native SQL scopes provide the capability. A wrapper that embeds store.Scope and
// does not forward LockTransaction HIDES it, and such a scope is NOT SUPPORTED for
// this call: before the ingest cut it could reserve within headroom, deny an
// exhausted budget, settle a handle and sweep expired rows through that wrapper, and
// now every participating callback fails before it reads the ledger. MIGRATION for
// such a decorator: forward the method to the scope it wraps —
//
//	func (s myScope) LockTransaction(ctx context.Context, key string) error {
//		locker, ok := s.Scope.(store.TransactionLocker)
//		if !ok {
//			return fmt.Errorf("my scope: wrapped scope provides no transaction lock")
//		}
//		return locker.LockTransaction(ctx, key)
//	}
//
// — or accept being rejected. What this package will NOT do is skip the lock because
// the capability is absent, or unwrap the decorator to reach the scope underneath:
// either would put an unserialized monetary write back exactly where the requirement
// exists to remove one.
//
// # Error posture: it depends on whether this attempt CONFIRMED the tenant inactive
//
// CORRECTED BY R1 (return of 2026-09-08). This block used to say the posture was
// "unchanged": a failure that decides nothing returns Allowed:true with the error
// and no handle. That is the historical fail-open contract, and it is still the
// posture AFTER this attempt has confirmed, under the writer lock, that the tenant
// carries no activation frontier.
//
// BEFORE that confirmation it is not available, because the routes that reach it
// have not looked at the frontier: the budget census, the identity and agent-group
// resolutions, opening the transaction and taking the lock all happen first, and a
// committed frontier may already exist. Allowed:true is a permission whatever
// accompanies it, and a permission cannot be issued by a path that never read the
// boundary it would be crossing. Those routes now return Allowed=false with a typed
// error and no handle.
//
// The confirmation is per attempt. A previous request that confirmed the tenant
// inactive says nothing about this one — the row can have been committed in
// between — so the guard is re-read inside every transaction attempt.
func (m *Module) ReserveBudget(ctx context.Context, tenant model.TenantID, dims SpendDims, estimateMicroUSD int64) (BudgetReservation, error) {
	if m.data == nil {
		// FAST PATH, AND IT CROSSES THE GUARD TOO — by being unable to. With no data
		// handle there is no scope, so this tenant's activation frontier cannot be
		// looked up, and an unlooked-up frontier is UNKNOWN, never inactive. The
		// previous return here was Allowed:true with a NIL error: an affirmative
		// admission issued by a module that cannot read the ledger it is admitting
		// against. It refuses now.
		return frontierRefusal(frontierReasonFor(nil)), attemptErr(errCodeCapabilityUnavailable, nil)
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
		// The census this admission binds could not be read, and the frontier has not
		// been consulted either — this attempt has confirmed nothing.
		return frontierRefusal(frontierReasonFor(nil)), storeErr(err)
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
			return frontierRefusal(frontierReasonFor(nil)), storeErr(err)
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
			return frontierRefusal(frontierReasonFor(nil)), storeErr(err)
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
			periodEnd: pEnd, hasBounds: hasLower,
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
//
// It is a participating FinOps monetary writer and carries the SAME supported-scope
// requirement and the same error posture as ReserveBudget: a store.Scope that does
// not implement store.TransactionLocker is not supported here and fails before the
// ledger read, and a failure BEFORE this attempt confirmed the tenant inactive
// refuses with Allowed=false and a typed error rather than admitting. See
// ReserveBudget.
func (m *Module) ReserveSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string, estimateMicroUSD int64) (BudgetReservation, error) {
	if m.data == nil {
		// Same as ReserveBudget: no handle means the frontier cannot be established,
		// and an unestablished frontier never admits.
		return frontierRefusal(frontierReasonFor(nil)), attemptErr(errCodeCapabilityUnavailable, nil)
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
		return frontierRefusal(frontierReasonFor(nil)), storeErr(err)
	}
	var targets []reservationTarget
	for _, period := range []string{"daily", "weekly", "monthly"} {
		resolved := resolveSpendLimitForPeriod(policies, actorRef, period, groups)
		if resolved.policy == nil || resolved.spec.Unlimited {
			continue
		}
		pStart, hasLower := periodStart(period, now)
		pEnd := periodEnd(period, pStart)
		targets = append(targets, reservationTarget{
			policyID: resolved.policy.ID, policyKind: policyKindSpendLimit,
			name: spendLimitWireID(resolved.policy.ID), action: "block",
			dimension: "spend_limit", scopeKey: actorRef, period: period, periodStart: pStart,
			periodEnd: pEnd, hasBounds: hasLower,
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
		// NO ENFORCING BUDGET SCOPES THE REQUEST — and this path still crosses the
		// frontier guard, because Allowed:true is an admission whatever produced it.
		// A tenant under a committed frontier must not receive one from a wrapper
		// that happened to find nothing to evaluate.
		//
		// It opens a write transaction to do it, which is a real cost on a hot path
		// that writes nothing: the frontier is read under the SAME writer lock the
		// four writing wrappers take, and that lock is only available inside Mutate.
		// The cheaper shape — reading the row through View — was rejected because it
		// would make this the one covered wrapper deciding on an unserialized read,
		// and "the case with no targets" is precisely where a missing guard would be
		// least likely to be noticed. If the cost is ever measured to matter, the
		// decision to relax it belongs with the evidence, not here.
		if err := m.guardCoveredMutation(ctx, tenant); err != nil {
			return frontierRefusal(frontierReasonFor(err)), err
		}
		return BudgetReservation{Allowed: true}, nil
	}
	handle := model.NewID()
	// A RESERVATION OF ZERO HOLDS NOTHING, so it inserts nothing and issues no
	// handle. The ceiling sums the amounts of live rows, and a zero row adds zero
	// to it: it withheld no headroom from any concurrent caller and there was
	// nothing for a settlement to return. All it ever did was accumulate — the
	// in-process gates that ask a YES/NO question have no cost to settle with, so
	// every one of their calls left an active row that only the TTL would retire,
	// and the reconciliation read then reported that as drift the engine had
	// produced itself.
	//
	// The EVALUATION is untouched: every enforcing target is still read under the
	// writer lock, a cap already over its limit still refuses, and a caller that
	// does carry an estimate still takes a real hold with the same seq proof. What
	// goes away is a row that proved nothing and a handle that promised a
	// settlement nobody could make.
	holds := estimate > 0
	issuedHandle := ""
	if holds {
		issuedHandle = handle.String()
	}
	expires := now.Add(reservationTTL)
	var lastErr error
	// guardErr records a frontier refusal established inside the attempt, so the
	// outcome switch can tell it from an ordinary read failure without inspecting
	// the error the store returned around it.
	var guardErr error
	// confirmedInactive records that THIS attempt read the frontier row under the
	// writer lock and found none. Until it is true, no failure may be answered with
	// the fail-open admission: the attempt has not looked at the boundary, so it
	// cannot know it is not crossing one. It is reset at the top of every attempt —
	// a previous attempt's confirmation is not this one's (R1).
	var confirmedInactive bool
	for attempt := 0; attempt < maxReserveRetries; attempt++ {
		// RESET BEFORE Mutate IS CALLED, not only inside its callback. The callback is
		// where the previous version cleared these, and a callback that never runs
		// clears nothing: an attempt whose transaction failed to OPEN inherited the
		// previous attempt's confirmation and answered with the fail-open admission,
		// having looked at no boundary at all. They are reset again inside the
		// callback because an adapter may run it more than once for one Mutate, and
		// each run is its own attempt at the same facts.
		guardErr = nil
		confirmedInactive = false
		var result BudgetReservation
		// decided records that a refusal was ESTABLISHED during this attempt, so a
		// failure arriving afterwards cannot be mistaken for "nothing was decided".
		// It is reset with result at the top of the attempt, never carried across one.
		decided := false
		err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			result = BudgetReservation{Allowed: true, Handle: issuedHandle, EstimateMicroUSD: estimate}
			decided = false
			guardErr = nil
			confirmedInactive = false
			// Serialize this tenant's FinOps writers for the rest of the attempt,
			// BEFORE the first decisive read (the seq probe below), and under the SAME
			// key the alert writer and the cost ingestion take — so the seq probe and
			// the reserved sum are authoritative for this attempt, and the ingestion
			// that later records the ACTUAL spend cannot interleave with the
			// reservation it settles against.
			//
			// It does NOT replace the monotonic seq or the OCC retry below, and it is
			// not credited with the exactly-M-1 admission: that proof is the UNIQUE
			// index on the seq, which stands on its own. See lockFinOpsWriter for what
			// this key is and is not worth on the current store.
			//
			// A lock that cannot be taken returns an ERROR, deliberately: the seam this
			// path serves is documented to fail OPEN on an error, and nothing here is
			// decided, so the posture is the one this function already had for a failed
			// read. It is not turned into a deny.
			if err := lockFinOpsWriter(ctx, sc); err != nil {
				return err
			}
			// THE FRONTIER, RE-READ INSIDE THIS ATTEMPT AND UNDER THIS LOCK. A check
			// made before the transaction — or on a previous retry — is not the fact
			// this attempt decides on: the row can have been committed in between, and
			// the whole purpose of the boundary is that no covered creation crosses it.
			if gerr := guardLegacyReservationMutation(ctx, sc); gerr != nil {
				guardErr = gerr
				return gerr
			}
			// From here — and only from here — this attempt knows the tenant carries no
			// frontier, so the legacy postures below are the adjudicated ones again.
			confirmedInactive = true
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
				// deny records this target's refusal with the same precedence the
				// no-headroom path has always used (first denial wins; a block outranks
				// a throttle already recorded) and marks the transaction for rollback.
				deny := func(reason string, spend, reserved int64) {
					denied = true
					decided = true
					// An enforcing target with no configured action refuses as a BLOCK —
					// the default the fail-closed group branch has always applied, now
					// applied wherever a refusal is recorded, and to the precedence test
					// as well as to the reported action: an action that reads as a block
					// but does not outrank a throttle would be a block in name only.
					// (A budget cannot reach here with an empty action: fillDefaults maps
					// "" to alert and alert-only budgets never become targets. It is the
					// defensive default, kept because it is the declared one.)
					action := tg.action
					if action == "" {
						action = "block"
					}
					if result.Allowed || (action == "block" && result.Action == "throttle") {
						result = BudgetReservation{
							Allowed: false, Action: action, BudgetID: tg.policyID.String(), BudgetName: tg.name,
							SpendMicroUSD: spend, ReservedMicroUSD: reserved, LimitMicroUSD: tg.ceiling,
							EstimateMicroUSD: estimate, Reason: reason,
						}
					}
				}
				// ORDERING INVARIANT: read the max seq BEFORE the reserved sum, and
				// insert with THAT seq. A successful INSERT (no seq collision) proves the
				// seq we read was the true committed max, hence the sum — read strictly
				// later — saw every prior reservation (seq is monotonic per bucket).
				// Reversing the two reads reopens the race on Postgres READ COMMITTED: a
				// stale sum paired with a fresh, non-colliding seq would over-admit.
				maxSeq, seqIssue, err := maxReservationSeq(ctx, repo, tg.policyID, tg.scopeKey, tg.periodStart)
				if err != nil {
					return err
				}
				// The seq is not a counter, it is the token that serializes concurrent
				// reservers: a successful INSERT at max+1 is the PROOF that nobody
				// committed under this read. max+1 past math.MaxInt64 wraps to
				// math.MinInt64 — a value that collides with nothing, so the insert
				// succeeds and the proof silently stops being one. That is an inability
				// to reserve, so it denies; it is not a smaller ceiling, and it must not
				// leave as an error the seam converts into an admission.
				nextSeq := addInt64(maxSeq, 1)
				if seqIssue != "" || !nextSeq.OK {
					reason := seqIssue
					if reason == "" {
						reason = "sequence space exhausted"
					}
					deny(fmt.Sprintf("budget %q cannot be reserved: %s; denied fail-closed", tg.name, reason), 0, 0)
					continue
				}
				agg, err := tg.spend(ctx, sc)
				if err != nil {
					if !tg.failClosed {
						return err
					}
					// The operator declared fail-closed for this group. Record the deny the
					// same way a no-headroom target does, so the transaction rolls back and
					// the caller gets a refusal instead of an error it would fail open on.
					//
					// It goes through deny() and CONTINUES, and both halves are the fix: it
					// used to assign result directly and return the sentinel on the spot, so
					// (1) this target's throttle overwrote a block another budget had already
					// established, and (2) every later target went unevaluated — a stronger
					// block further down the list could not be found because the evaluation
					// was already over. CheckBudget's equivalent branch has always recorded
					// through its precedence guard and carried on, so the two admission paths
					// answered the same three policies differently. Same refusal, same reason,
					// same rollback; the precedence and the decided bookkeeping are what is
					// new. The spend aggregate failed, so there are no spend/reserved figures
					// to report and none are invented.
					deny("group budget check failed (fail-closed)", 0, 0)
					continue // keep evaluating: a later block must still be able to outrank
				}
				// The VERSION-AWARE reader: the legacy branch this ledger has always
				// had, plus the v1 obligations of held attempt parents, over the
				// target's real window. An imported hold counts here — that is the
				// whole point of importing it — and it does so without a TTL.
				reserved, err := heldReservedForWindow(ctx, sc, tg.policyID, tg.scopeKey,
					tg.periodStart, tg.periodEnd, tg.hasBounds, now)
				if err != nil {
					return err
				}
				// A reserved total that could not be established is not a lower ceiling:
				// the ledger read stopped short (page cap, a page claiming more rows
				// without a usable cursor) or a row is malformed, so how much headroom is
				// held is simply unknown. Deny, the same way a truncated aggregate does.
				if !reserved.established() {
					deny(fmt.Sprintf("budget %q reserved total could not be established (%s); reservation denied fail-closed", tg.name, reserved.Incomplete), agg.Cost, 0)
					continue // do not reserve this target; keep going for block>throttle
				}
				effective := sumInt64(agg.Cost, tg.staticReserved, reserved.MicroUSD)
				// The ceiling comparison is where the wrap used to pay: unchecked,
				// effective+estimate past math.MaxInt64 lands negative, compares below
				// any ceiling, and admits the LARGEST possible request. An arithmetic
				// result the ledger cannot represent is not an amount, so it is never
				// presented as one (no saturation) — it denies.
				ceilingSum := checkedSum{}
				if effective.OK {
					ceilingSum = addInt64(effective.Value, estimate)
				}
				// A truncated spend aggregate is a lower bound: fail closed (deny), the
				// same posture CheckBudget takes on truncation.
				if agg.Truncated || !effective.OK || !ceilingSum.OK || ceilingSum.Value > tg.ceiling {
					reason := fmt.Sprintf("budget %q %s cap reached (%s): no headroom to reserve %d µUSD", tg.name, tg.action, tg.period, estimate)
					switch {
					case agg.Truncated:
						reason = fmt.Sprintf("budget %q aggregate truncated at scan cap; reservation denied fail-closed", tg.name)
					case !effective.OK || !ceilingSum.OK:
						reason = fmt.Sprintf("budget %q effective consumption plus estimate is not representable; reservation denied fail-closed", tg.name)
					}
					deny(reason, agg.Cost, reserved.MicroUSD)
					continue // do not reserve this target; keep going for block>throttle
				}
				if denied {
					// An earlier target already denied — the whole reservation rolls
					// back at the end. Skip the insert: a spurious seq conflict on a
					// doomed target must not retry a decided denial into the fail-open
					// admit under contention (adversarial review finding).
					continue
				}
				if !holds {
					// Nothing to hold. The target was read and judged exactly as it
					// would have been for an amount; there is simply no row to write.
					continue
				}
				rec := model.Record{
					colResvPolicyRef:   tg.policyID.String(),
					colResvPolicyKind:  tg.policyKind,
					colResvDimension:   tg.dimension,
					colResvScopeKey:    tg.scopeKey,
					colResvPeriod:      tg.period,
					colResvPeriodStart: model.NewTimestamp(tg.periodStart).String(),
					colResvSeq:         nextSeq.Value,
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
		case guardErr != nil:
			// The activation frontier forbade the mutation, or its state could not be
			// established. Neither is an admission and neither is retried: the answer
			// does not change by asking again in the same transaction loop.
			return frontierRefusal(frontierReasonFor(guardErr)), guardErr
		case err != nil && !confirmedInactive:
			// The transaction could not be opened, or the writer lock could not be
			// taken: the guard never ran, so this attempt has confirmed nothing. The
			// fail-open admission below is not available to a path that did not look
			// at the boundary (R1).
			return frontierRefusal(frontierReasonFor(nil)), storeErr(err)
		case errors.Is(err, errReservationDenied):
			return result, nil // a normal "no" — the roll-back is intentional
		case decided:
			// A refusal was ESTABLISHED and then a later read failed or lost the write
			// race. The two are different facts and only one of them is undecided: the
			// failure says nothing about the budget that already refused, and this
			// evaluation visits the remaining targets precisely so block can outrank
			// throttle. Handing back the outage posture here (Allowed:true plus the
			// error) gives a decided refusal to a seam documented to fail OPEN, and
			// retrying it hands the decision to a race — so the refusal is returned,
			// with a nil error, exactly as if the transaction had rolled itself back
			// on the denial sentinel. It HAS rolled back: nothing was written.
			//
			// This is not "deny on any outage": with nothing decided, the cases below
			// keep their declared postures untouched.
			return result, nil
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue // lost the seq race; re-read committed state and retry
		case err != nil:
			return BudgetReservation{Allowed: true}, err // read error → caller fails open
		default:
			return result, nil
		}
	}
	if !confirmedInactive {
		return frontierRefusal(frontierReasonFor(nil)), storeErr(lastErr)
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
//
// It is a participating FinOps monetary writer: the store.Scope of its transaction
// MUST implement store.TransactionLocker (the new supported-scope requirement
// documented on ReserveBudget, including the decorator migration). A scope that
// hides the capability makes this call fail and transition NOTHING — which is the
// posture a settlement has always had for a failure before its first Update, and is
// still not a silent success: the rows stay active until they are settled or expire.
func (m *Module) CommitReservation(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error {
	return m.settleReservation(ctx, tenant, handle, resvStateCommitted, actualMicroUSD)
}

// ReleaseReservation returns a reservation's headroom on failure/timeout: every
// still-active row under the handle flips to released and leaves the sum. No spend
// is recorded. Idempotent. It is a participating FinOps monetary writer and carries
// the same new supported-scope requirement as CommitReservation.
func (m *Module) ReleaseReservation(ctx context.Context, tenant model.TenantID, handle string) error {
	return m.settleReservation(ctx, tenant, handle, resvStateReleased, 0)
}

func (m *Module) settleReservation(ctx context.Context, tenant model.TenantID, handle string, state string, actual int64) error {
	if m.data == nil {
		// It used to return nil — reporting a settlement it had no store to perform.
		// With no handle the tenant's activation frontier cannot be established, and
		// terminalizing a covered obligation is exactly what the frontier forbids, so
		// the honest answer is an error that transitions nothing.
		return attemptErr(errCodeCapabilityUnavailable, nil)
	}
	id, err := model.ParseID(handle)
	if err != nil || id.IsZero() {
		return store.ErrNotFound
	}
	now := m.clock.Now()
	// confirmed records that this transaction's callback ran and its guard
	// established the tenant has no frontier. Until it is true a failure has
	// classified nothing — including a transaction that never opened, whose error
	// used to come back raw from Mutate.
	confirmed := false
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		confirmed = false
		// The same key, before the enumeration this settlement decides on: the rows it
		// is about to read are the ones a concurrent sweep or a second settlement of
		// the same handle would also be transitioning, and "already settled — skip" is
		// only idempotent if the state it read cannot change under it.
		//
		// Typed, like every other refusal of the five covered wrappers: a settlement
		// that cannot take the lock has not read this tenant's frontier either, and a
		// caller classifies all five the same way.
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return storeErr(err)
		}
		// A settlement TERMINALIZES an obligation, which is the second half of what a
		// committed frontier forbids: a pending group must not become historical
		// through the old API and then be treated as exempt.
		if gerr := guardLegacyReservationMutation(ctx, sc); gerr != nil {
			return gerr
		}
		confirmed = true
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, incomplete, err := scanReservations(ctx, repo, []model.Filter{eq(colResvHandle, id.String())})
		if err != nil {
			return err
		}
		// A handle covers ONE row per budget the request had to fit within, so an
		// enumeration that stopped short would settle a PREFIX and report success:
		// the unread rows stay active, holding headroom until the TTL, while the
		// caller has been told the actuation is accounted for. Refuse before the
		// first Update — the settlement is all-or-none like the reserve that made it.
		if incomplete != "" {
			return fmt.Errorf("%w: %s (handle %s)", errReservationScanIncomplete, incomplete, id)
		}
		if len(rows) == 0 {
			return store.ErrNotFound
		}
		// A V1 CHILD IS NEVER TERMINALIZED THROUGH THIS ROUTE, AND NOT ONLY AFTER A
		// FRONTIER EXISTS. An imported hold is resolved by the lifecycle operations —
		// with the evidence, the cost association and the audit they require — so a
		// legacy settlement of its group would retire an obligation without any of
		// that. The refusal covers the WHOLE group rather than the v1 rows in it,
		// because settling a prefix is precisely the defect the enumeration check
		// above already refuses.
		for _, r := range rows {
			if link, _ := linkageOf(r); link != linkageLegacy {
				return attemptErr(errCodeLifecycleAPIRequired, nil)
			}
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
	return classifyPreConfirmationFailure(err, confirmed)
}

// classifyPreConfirmationFailure types a failure that happened BEFORE this
// transaction's guard confirmed the tenant inactive — most importantly a
// transaction that never opened, whose error used to come straight back from
// Mutate with no code on it.
//
// After the confirmation it changes NOTHING. The adjudicated results of these
// paths — store.ErrNotFound for a handle that never existed, the enumeration
// refusal, lifecycle_api_required, a store conflict — are the answers the ledger
// gave with the boundary established, and reclassifying them would be a new
// posture nobody asked for. errors.Is keeps working either way: the typed wrapper
// unwraps to the store cause.
func classifyPreConfirmationFailure(err error, confirmed bool) error {
	if err == nil || confirmed || attemptCode(err) != "" {
		return err
	}
	return storeErr(err)
}

// SweepExpiredReservations flips active-but-expired rows to expired. It is
// HYGIENE ONLY: the ceiling sum already excludes rows whose expires_at has passed
// (activeReservedMicroUSD filters expires_at > now), so an expired reservation
// stops holding headroom the instant it expires — there is no counter to decrement
// and thus no way to double-count. The sweep only makes the terminal state
// explicit (for observability and eventual GC). Returns the number swept.
//
// It is a participating FinOps monetary writer: the store.Scope of its transaction
// MUST implement store.TransactionLocker (the new supported-scope requirement
// documented on ReserveBudget, including the decorator migration). A scope that
// hides the capability makes this call return 0 AND an error, never a count for work
// it did not do.
func (m *Module) SweepExpiredReservations(ctx context.Context, tenant model.TenantID) (int, error) {
	if m.data == nil {
		// A count of zero with a nil error reads as "there was nothing to sweep".
		// Without a store there is no such observation, and the frontier that would
		// forbid the sweep cannot be established either.
		return 0, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	now := m.clock.Now()
	swept := 0
	confirmed := false
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		swept = 0
		confirmed = false
		// The same key, before the enumeration whose size becomes the reported count.
		// The conflict tolerance below (a concurrent settle won the row) is kept as it
		// is: the lock removes the interleaving inside one store, it does not turn a
		// lost row into an error the operator has to read.
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return storeErr(err)
		}
		// The sweep terminalizes too — and a lapsed TTL is the one route by which a
		// pending legacy obligation could quietly become "expired, therefore not my
		// problem" after the boundary was taken.
		if gerr := guardLegacyReservationMutation(ctx, sc); gerr != nil {
			return gerr
		}
		confirmed = true
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, incomplete, err := scanReservations(ctx, repo, []model.Filter{
			eq(colResvState, resvStateActive),
			{Column: colResvExpiresAt, Op: model.OpLte, Value: now.String()},
			// THE TTL IS A LEGACY MECHANISM AND A V1 HOLD DOES NOT HAVE ONE. An
			// imported child keeps the expires_at it had, as history — the row is not
			// rewritten — so without this predicate the very first sweep after an
			// import would expire a hold whose legacy TTL lapsed years ago, and the
			// money would leave the ceiling with no evidence and no audit. It is
			// enforced again in the loop below, because a total that depends on the
			// store honoring a filter is not a guarantee.
			{Column: colResvAttemptRef, Op: model.OpIsNull},
		})
		if err != nil {
			return err
		}
		// The count this function returns is what an operator (and, later,
		// reconciliation) reads as "the expired rows were dealt with". Produced from
		// an enumeration that stopped short, it describes a job that did not happen,
		// so the sweep refuses instead of sweeping a prefix and reporting a number.
		if incomplete != "" {
			return fmt.Errorf("%w: %s", errReservationScanIncomplete, incomplete)
		}
		for _, r := range rows {
			if link, _ := linkageOf(r); link != linkageLegacy {
				// Not this sweep's row. It is SKIPPED rather than counted or refused:
				// the sweep is hygiene over the legacy TTL, and one imported hold in a
				// tenant must not stop the legacy rows beside it from being tidied.
				continue
			}
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
	if err != nil {
		// The transaction rolled back, so nothing was swept whatever the loop had
		// counted before failing: returning that count would report partial success.
		return 0, classifyPreConfirmationFailure(err, confirmed)
	}
	return swept, nil
}

// activeReservedMicroUSD sums the headroom held by live reservations for one
// policy+scope+period: state=active AND expires_at > now. The expiry predicate is
// what makes expiry safe — a lapsed reservation is simply not summed, so no
// decrement bookkeeping (and no double-count) is ever needed.
//
// The sum is CHECKED, and an incomplete enumeration is reported rather than
// returned as a total. Both used to be silent: rows past the page cap were dropped
// (a prefix presented as the whole ledger) and the running total wrapped on
// overflow, which turns the held headroom negative — the one value that admits
// everything.
func activeReservedMicroUSD(ctx context.Context, repo store.GenericRepo, tenant model.TenantID, policyID model.ID, scopeKey string, periodStart, now time.Time) (reservedTotal, error) {
	periodText := model.NewTimestamp(periodStart).String()
	rows, scan, err := scanReservationsTyped(ctx, repo, []model.Filter{
		eq(colResvPolicyRef, policyID.String()),
		eq(colResvScopeKey, scopeKey),
		eq(colResvPeriodStart, periodText),
		eq(colResvState, resvStateActive),
		{Column: colResvExpiresAt, Op: model.OpGt, Value: model.NewTimestamp(now).String()},
		// THIS BRANCH IS THE LEGACY ONE, AND THE TWO MUST BE DISJOINT. Without
		// this predicate an imported v1 child whose original expires_at is still in
		// the future and whose period bucket matches would be summed HERE as well as
		// by the v1 branch — the same obligation counted twice against the ceiling.
		// The row loop below repeats the classification, because a total that depends
		// on the store honoring a filter is a total resting on someone else's
		// promise.
		{Column: colResvAttemptRef, Op: model.OpIsNull},
	})
	if err != nil {
		// A real read failure stays an error. It is never converted into a total, a
		// bound or a quiet zero, whatever the caller would prefer to do with it.
		return reservedTotal{}, err
	}
	// EVERY OBSERVED ROW IS VALIDATED FIRST, and the order is the point (A4.2). The
	// previous version returned as soon as the enumeration was structurally
	// incomplete, so a prefix containing a NEGATIVE or malformed row was never
	// inspected — and an incomplete-but-clean prefix could not be told apart from an
	// incomplete-and-corrupt one. Only a prefix whose observed rows are all valid,
	// in scope and non-negative may carry the non-negative invariant forward.
	total := checkedSum{OK: true}
	for _, r := range rows {
		switch link, _ := linkageOf(r); link {
		case linkageV1:
			// A v1 child belongs to the other branch. It is skipped, not faulted: it
			// is a perfectly valid obligation, just not this branch's.
			continue
		case linkageMalformed:
			// An unknown lifecycle version, an empty reference or one column set
			// without the other. It does NOT degrade to legacy — reading it here would
			// put a v1 obligation on a branch that drops holds on a TTL.
			return indeterminateReserved(scan, resvFaultRowOutOfScope), nil
		}
		if fault := reservationRowFault(r, tenant, policyID, scopeKey, periodText, now); fault != resvFaultNone {
			return indeterminateReserved(scan, fault), nil
		}
		amount, _ := int64Cell(r, colResvAmount)
		if total = addInt64(total.Value, amount); !total.OK {
			return indeterminateReserved(scan, resvFaultSumUnrepresentable), nil
		}
	}
	if !scan.complete() {
		// The rows that were read are consistent with the domain, and the ones that
		// were not read are obligations that cannot be negative. The prefix sum is
		// NOT published: MicroUSD stays zero and only the invariant travels.
		return reservedTotal{
			Incomplete: scan.text(), State: reservedNonNegativeUnknown, Scan: scan,
		}, nil
	}
	return reservedTotal{MicroUSD: total.Value, State: reservedKnown, Scan: scan}, nil
}

// indeterminateReserved is the observation-contradicts-the-domain outcome. The
// diagnostic text keeps the words the admission paths have always produced for the
// two faults that already existed, so their messages and tests do not move.
func indeterminateReserved(scan reservationScanState, fault reservationFault) reservedTotal {
	text := "a reservation row could not be attributed to the requested scope"
	switch fault {
	case resvFaultRowTenantMalformed:
		text = "a reservation row carries no usable tenant evidence"
	case resvFaultRowOtherTenant:
		text = "a reservation row belongs to another tenant"
	case resvFaultRowAmountNegative:
		text = "a reservation row holds a negative amount"
	case resvFaultRowAmountMalformed:
		text = "a reservation row holds a malformed amount"
	case resvFaultSumUnrepresentable:
		text = "the reserved total is not representable"
	}
	return reservedTotal{Incomplete: text, State: reservedIndeterminate, Scan: scan, Fault: fault}
}

// reservationRowFault checks one observed reservation row against the domain and
// against the scope the read asked for. A reservation holds PROSPECTIVE spend, so
// its amount is a non-negative int64 cell; both Reserve* entry points refuse a
// negative estimate at the door, and a negative row would SUBTRACT from the held
// headroom and manufacture room nobody reserved. The rejection is scoped to this
// ledger deliberately: cost samples carry legitimate signed amounts (credits,
// refunds, adjustments) and keep aggregating exactly as before.
//
// The attribution check mirrors the filters the read was issued with, so a row that
// does not evidence the requested tenant/policy/scope/period/state/validity is
// classified rather than repaired with the caller's own scope.
//
// The TENANT was missing from this check until A4.2's independent review found it.
// The repository is tenant-pinned and appends its own predicate, so this is not
// evidence that SQL ever leaked a row across tenants — it is the integrity of the
// observed row, which is what the helper promises before it calls a reserve exact.
// Without it, a row carrying no tenant cell at all (the shape a fixture produces)
// was summed as this tenant's held headroom, and a full final page of such rows was
// published as a KNOWN exact reserve.
func reservationRowFault(r model.Record, tenant model.TenantID, policyID model.ID, scopeKey, periodText string, now time.Time) reservationFault {
	// The row's OWN tenant evidence, checked through the one implementation the cost
	// reader uses. It is checked FIRST because it is the widest claim a row makes: a
	// row that cannot evidence this tenant is not this tenant's obligation whatever
	// its amount, policy or period say. The cell is read entry-then-type-then-value
	// and is never filled in from the scope that asked for the read — an absent tenant
	// cell is refused, not completed.
	switch tenantCellOf(r, tenant) {
	case tenantCellUnusable:
		return resvFaultRowTenantMalformed
	case tenantCellOtherTenant:
		return resvFaultRowOtherTenant
	}
	amount, ok := int64Cell(r, colResvAmount)
	if !ok {
		return resvFaultRowAmountMalformed
	}
	if amount < 0 {
		return resvFaultRowAmountNegative
	}
	if !textCellEquals(r, colResvPolicyRef, policyID.String()) ||
		!textCellEquals(r, colResvScopeKey, scopeKey) ||
		!textCellEquals(r, colResvPeriodStart, periodText) ||
		!textCellEquals(r, colResvState, resvStateActive) {
		return resvFaultRowOutOfScope
	}
	expires, err := model.ParseTimestamp(r.String(colResvExpiresAt))
	if err != nil || !expires.Time().After(now) {
		// The read asked for rows still holding headroom; one that does not evidence
		// that is not summed into the held total.
		return resvFaultRowOutOfScope
	}
	return resvFaultNone
}

// textCellEquals compares a text cell by TYPE first: Record.String reports "" for a
// missing, null or wrongly typed cell, which would let a malformed row satisfy an
// empty expected value — the same coercion the cost reader already refuses.
func textCellEquals(r model.Record, col, want string) bool {
	cell, present := r[col]
	if !present || cell == nil {
		return false
	}
	text, isText := cell.(string)
	return isText && text == want
}

// dynamicReservedMicroUSD is the scope-level convenience over
// activeReservedMicroUSD: it opens the reservation repo and returns the live
// reserved headroom for a policy+period. It is what folds the DYNAMIC reserve
// ledger into the effective consumption everywhere the ceiling is evaluated
// (CheckBudget, budgetStatus, evaluateBudgets), so the pre-flight denial, the
// alert/cap signal and the status DTO all agree on spend + static + dynamic.
func dynamicReservedMicroUSD(ctx context.Context, sc store.Scope, policyID model.ID, scopeKey string, periodStart, now time.Time) (reservedTotal, error) {
	repo, err := sc.Ext(budgetReservationKind)
	if err != nil {
		return reservedTotal{}, err
	}
	// The EXPECTED tenant is the scope's own: the repository this scope hands out is
	// pinned to it, so it is the identity every returned row has to evidence. It is
	// carried as a value the row is checked AGAINST, never written into a row that
	// arrived without one.
	return activeReservedMicroUSD(ctx, repo, sc.Tenant(), policyID, scopeKey, periodStart, now)
}

// maxReservationSeq returns the largest seq issued for a policy+period across ALL
// states (so the seq is globally monotonic per bucket and the UNIQUE index is a
// true serialization token — a reused seq must never collide with a settled row).
// 0 means the bucket is empty.
//
// The second result names a bucket whose stored seq cannot serve as that token —
// today, one that is negative, which only a wrapped or hand-written row can be.
// Issuing max+1 from such a bucket produces a seq that collides with nothing, so
// the INSERT stops proving anything about concurrent reservers; the caller must
// refuse to reserve rather than treat it as a smaller ceiling.
func maxReservationSeq(ctx context.Context, repo store.GenericRepo, policyID model.ID, scopeKey string, periodStart time.Time) (int64, string, error) {
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
		return 0, "", err
	}
	if len(rows) == 0 {
		return 0, "", nil
	}
	seq := rows[0].Int(colResvSeq)
	if seq < 0 {
		return 0, "the reservation sequence of this bucket is negative", nil
	}
	return seq, "", nil
}

// scanReservations pages the reservation rows matching filters, bounded by the
// same scan cap the cost read-model uses so a pathological bucket cannot spin
// forever. It uses the default id keyset cursor (no custom sort), so paging is
// exact.
//
// The second result names why the enumeration ENDED EARLY, and is empty only when
// every matching row was read. Three ways it can end early, each measured on the
// page loop's own signals rather than assumed:
//
//   - the page cap is reached while the store still reports HasMore. The rows read
//     are a PREFIX. This loop used to return them with a nil error, so every caller
//     — the reserved sum, the settlement, the sweep — took a prefix for the ledger.
//   - the store reports HasMore without handing back a cursor. There is nothing to
//     resume from: re-issuing the query re-reads page one, so the loop would append
//     the same rows until the cap and then call that inflated result complete.
//   - the continuation cursor RETURNS to one already used. Comparing against the
//     immediately previous cursor only catches the adjacent case: an A → B → A cycle
//     re-reads a page already summed, and if the store then says HasMore=false the
//     scan would report a complete enumeration built from duplicated rows. So every
//     continuation cursor handed out while more rows are promised is remembered, and
//     a repeat of any of them ends the scan as incomplete. The set is bounded by the
//     page cap, like the scan itself.
//
// The cap itself is not a defect: a scan that fills exactly maxScanPages pages and
// is then told there is nothing more IS complete, and stays complete here. Neither is
// the cursor on a FINAL page: nobody resumes from it, so HasMore=false may legitimately
// carry an empty, stale or repeated value and the checks above are not applied to it.
func scanReservations(ctx context.Context, repo store.GenericRepo, filters []model.Filter) ([]model.Record, string, error) {
	rows, state, err := scanReservationsTyped(ctx, repo, filters)
	return rows, state.text(), err
}

// scanReservationsTyped is that same loop with its outcome kept TYPED. It is the
// one paginator: scanReservations is a thin adapter over it for the settlement and
// sweep callers, so there are not two sets of paging rules to keep in step.
func scanReservationsTyped(ctx context.Context, repo store.GenericRepo, filters []model.Filter) ([]model.Record, reservationScanState, error) {
	q := model.Query{Filters: filters, Limit: listCap}
	var out []model.Record
	used := make(map[string]bool)
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, resvScanComplete, err
		}
		out = append(out, recs...)
		if !page.HasMore {
			return out, resvScanComplete, nil
		}
		if page.Cursor == "" {
			return out, resvScanCursorMissing, nil
		}
		if page.Cursor == q.Cursor {
			return out, resvScanCursorStalled, nil
		}
		if used[page.Cursor] {
			return out, resvScanCursorCycle, nil
		}
		if pages+1 >= maxScanPages {
			return out, resvScanPageCap, nil
		}
		used[page.Cursor] = true
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
