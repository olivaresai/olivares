// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/cron"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// routinepolicy.go ENFORCES the five routine-governance controls.
// (commit 61d64721) defined, persisted and CRUD-ed them but wired no
// consumer, so an operator could author a cadence floor, a concurrency cap, an
// approval requirement, a cron allowlist or a blocked-environment list and none
// of them decided anything. Is where that was written down and where the
// decision to build this engine was taken (§"Fuera de alcance": "almacenan intención que nada aplica"). Here they decide.
//
// A schedule IS the routine governs (the budget gate already passes this
// module's schedule id as RoutineRef; see the routines-governance contract
// §4.1). Five seams can change or act on one, and a control enforced at fewer
// than all five is evadable through the ones it missed:
//
//	declaration  handleCreateSchedule   schedules.go   (POST   /schedules)
//	             handlePatchSchedule    schedules.go   (PATCH  /schedules/{id})
//	             handleRestoreSchedule  revisions.go   (POST   /schedules/{id}/restore)
//	actuation    handleFire             schedules.go   (POST   /schedules/{id}/fire)
//	             executeScheduleFire    workflow_run.go (the DAG schedule-fire step)
//
// WHY ACTUATION TOO. Declaration-time checks alone grandfather every routine
// that already existed when a policy was authored or tightened. The controls
// that describe an ONGOING property (the cadence floor, the blocked
// environment) are therefore re-evaluated against the CURRENT policy at fire.
// max_active_routines is deliberately NOT re-checked at fire: it is an
// ACTIVATION invariant, and "the population is over cap, so deny every member's
// fire" is not a usable selection rule — tightening a cap must block new
// activations and surface the over-cap posture, not silently freeze the estate.

// Stable machine-readable denial codes. They ride the response and the audit so
// an operator (and a client) can branch on the control that refused, without
// parsing prose or reading the policy body.
const (
	codeRoutineFloor      = "routine_policy_floor"
	codeRoutineCron       = "routine_policy_cron"
	codeRoutineEnv        = "routine_policy_environment"
	codeRoutineActiveCap  = "routine_policy_active_cap"
	codeRoutineUnreadable = "routine_policy_unreadable"
	codeRoutineApproval   = "routine_policy_approval_required"
)

// routineDenial is one refusal by the routine policy. It names the control and
// carries opaque policy references plus the composed digest — never the policy
// body, and never a policy VALUE. That matters because the denial reaches a
// caller holding only orchestration:schedule:write: echoing the floor, the cap
// or the current active count would let that caller enumerate the governance
// posture without governance:routine:read. An operator who holds that
// permission resolves policy_refs instead.
type routineDenial struct {
	code       string
	message    string
	httpStatus int
	policyRefs []string
	digest     string
	// retryAfter is set only for a cadence-floor refusal at fire time, where
	// the caller CAN succeed later; it is the whole point of 429 over 403.
	retryAfter time.Duration
}

func (d *routineDenial) Error() string { return d.code + ": " + d.message }

// body renders the denial as the module's error envelope plus the machine
// fields. Policy ids are opaque references, never a policy body (docs/SECURITY-HARDENING.md).
func (d *routineDenial) body() map[string]any {
	out := map[string]any{
		"error": map[string]string{"message": d.message},
		"code":  d.code,
	}
	if len(d.policyRefs) > 0 {
		out["policy_refs"] = d.policyRefs
	}
	if d.digest != "" {
		out["policy_digest"] = d.digest
	}
	if d.retryAfter > 0 {
		out["retry_after_seconds"] = int64(d.retryAfter.Seconds())
	}
	return out
}

// writeRoutineDenial writes the refusal. A cadence refusal also carries the
// standard Retry-After header so a well-behaved client backs off rather than
// hammering the gate.
func writeRoutineDenial(w http.ResponseWriter, d *routineDenial) {
	if d.retryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int64(d.retryAfter.Seconds()+0.999)))
	}
	writeJSON(w, d.httpStatus, d.body())
}

// denyUnreadable is the fail-closed refusal used whenever the policy (or the
// authoritative target context) cannot be READ. It is deliberately 503 and not
// 403: nothing was decided against the caller, the plane could not decide at
// all — and an undecidable policy must never resolve to "allowed".
func denyUnreadable(what string) *routineDenial {
	return &routineDenial{
		code: codeRoutineUnreadable, httpStatus: http.StatusServiceUnavailable,
		message: what + "; refused (deny-closed)",
	}
}

// routineScopeOfPrincipal freezes the DECLARING principal's governance axes.
// Called once, at create — never re-derived later, because a later caller may
// be a different (more privileged) principal acting on someone else's routine.
func routineScopeOfPrincipal(mc api.ModuleContext) RoutineScope {
	// The declaring principal is present, so the user axis is always ANSWERED
	// here — either with a user id or with a definite "this routine has none".
	s := RoutineScope{UserKnown: true}
	if !mc.Principal.UserID.IsZero() {
		s.UserRef = mc.Principal.UserID.String()
	}
	if ws, ok := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); ok {
		s.WorkspaceRef = ws.String()
	}
	return s
}

// routineScopeOfSchedule reads back the scope frozen on a schedule row.
//
// A row written BEFORE has neither column. Rather than let those routines
// answer "unknown" on the user axis — which refuses every patch, restore and
// fire of every legacy routine the moment one user-scoped policy exists — the
// owner is recovered from owner_actor, which has carried the declaring
// principal since the entity was introduced and is exactly "user:<id>" for a
// user (core/auth.Principal.Actor). A token-declared routine yields no user,
// which is the truth about it, not a gap in the record.
func routineScopeOfSchedule(rec model.Record) RoutineScope {
	s := RoutineScope{
		UserRef:      rec.String(colOwnerUserRef),
		WorkspaceRef: rec.String(colWorkspaceRef),
	}
	if s.UserRef != "" {
		s.UserKnown = true
		return s
	}
	// owner_actor is "user:<id>" or "token:<id>" (core/auth.Principal.Actor).
	// The first recovers the owner; the second is a definite "no owning user",
	// which is an ANSWER — a token-declared routine is provably outside a policy
	// scoped to a named user. Only an absent/unrecognized actor stays unknown.
	actor := rec.String(colOwnerActor)
	if id, ok := strings.CutPrefix(actor, "user:"); ok {
		s.UserRef, s.UserKnown = id, true
	} else if strings.HasPrefix(actor, "token:") {
		s.UserKnown = true
	}
	return s
}

// resolvePolicy consults the gate and normalizes both failure modes into one
// deny-closed refusal: a transport/store error, and a resolution the gate could
// not complete (Indeterminate — an enabled policy scopes an axis this routine
// cannot answer for, so "nothing matched" would be a silent bypass).
func (m *Module) resolvePolicy(ctx context.Context, tenant model.TenantID, scope RoutineScope) (RoutinePolicy, *routineDenial) {
	pol, err := m.routineGate.Resolve(ctx, tenant, scope)
	if err != nil {
		m.errorf("orchestration: routine-policy gate error; failing CLOSED", "err", err)
		return RoutinePolicy{}, denyUnreadable("routine policy unreadable")
	}
	if pol.Indeterminate {
		d := denyUnreadable("routine policy scopes " + pol.IndeterminateAxis +
			", which this routine does not record; it cannot be proven inapplicable")
		d.policyRefs, d.digest = pol.PolicyRefs, pol.Digest
		return pol, d
	}
	return pol, nil
}

// routineShape is the governed shape of a routine as a declaration path is
// about to persist it — the exact values the policy judges.
type routineShape struct {
	triggerKind string
	cadenceSpec string
	intervalSec int64
	graceFactor int64
	subjectKind string
	subjectRef  string
	active      bool
}

// checkDeclaration applies the DECLARATION-time controls (cadence floor, cron
// allowlist, blocked environment) to a shape a caller is about to persist.
// A refusal here is 422: the editor is authorized to submit the request, but
// governance policy refuses the SHAPE — the repo's existing meaning for 422 on
// an orchestration capacity/admission refusal (workflow.go, models/owned.go).
func (m *Module) checkDeclaration(ctx context.Context, pol RoutinePolicy, sh routineShape) *routineDenial {
	deny := func(code, msg string) *routineDenial {
		return &routineDenial{
			code: code, httpStatus: http.StatusUnprocessableEntity, message: msg,
			policyRefs: pol.PolicyRefs, digest: pol.Digest,
		}
	}

	if pol.MinIntervalSec > 0 && sh.triggerKind == "cron" {
		// A cadence floor with an UNDECLARED interval cannot be satisfied:
		// expected_interval_seconds = 0 disables the cadence-miss check, and
		// admitting it would let "declare nothing" be the bypass.
		if sh.intervalSec <= 0 {
			return deny(codeRoutineFloor,
				"a routine policy sets a cadence floor; a cron routine must declare expected_interval_seconds (0 is not admissible under a floor)")
		}
		if sh.intervalSec < pol.MinIntervalSec {
			return deny(codeRoutineFloor, "expected_interval_seconds is below the routine-policy cadence floor")
		}
	}

	// The cron expression is checked against BOTH controls in one parse.
	spec := strings.TrimSpace(sh.cadenceSpec)
	if pol.CronAllowlistInForce {
		// An allowlist enumerates the cadences routines may have. A non-cron
		// trigger has no cadence to enumerate, so admitting it would make
		// trigger_kind the bypass.
		if sh.triggerKind != "cron" {
			return deny(codeRoutineCron, "a routine policy restricts routines to an allow-listed cron cadence; trigger_kind must be cron")
		}
		if spec == "" {
			return deny(codeRoutineCron, "a routine policy restricts routines to an allow-listed cron cadence; cadence_spec is required")
		}
		if !cronAllowed(pol.CronAllowed, spec) {
			return deny(codeRoutineCron, "cadence_spec is not in the routine policy's allowed cron patterns")
		}
	}
	if pol.MinIntervalSec > 0 && sh.triggerKind == "cron" && spec != "" {
		// The declared interval and the cron text are independent fields, so
		// they can disagree: expected_interval_seconds=3600 with
		// cadence_spec="* * * * *" satisfies the floor above while describing a
		// one-minute routine. Parse the cron and hold it to the same floor.
		// Unparseable DENIES: "parse it if you can, otherwise allow" makes any
		// unsupported spelling the bypass.
		parsed, err := cron.Parse(spec)
		if err != nil {
			return deny(codeRoutineCron, "a routine policy sets a cadence floor, so cadence_spec must be a supported 5-field cron expression: "+err.Error())
		}
		if gap, bounded := parsed.MinGap(); bounded && gap < time.Duration(pol.MinIntervalSec)*time.Second {
			return deny(codeRoutineFloor, "cadence_spec fires more often than the routine-policy cadence floor")
		}
	}

	if d := m.checkEnvironmentAtDeclaration(ctx, pol, sh.subjectKind, sh.subjectRef); d != nil {
		return d
	}
	return nil
}

// cronAllowed reports whether spec matches an allow-listed pattern, comparing
// canonical (whitespace-normalized) spellings so re-spacing is not a bypass.
func cronAllowed(allowed []string, spec string) bool {
	c := cron.Canonical(spec)
	for _, a := range allowed {
		if a == c {
			return true
		}
	}
	return false
}

// checkEnvironment enforces blocked_environments against the AUTHORITATIVE
// actuation environment — the dispatcher route the fire path will really pick,
// never a caller-declared field. status lets the caller pick the declaration
// (422) or actuation (403) mapping.
//
// With no blocked list in force it does not consult the resolver at all, so a
// deployment that never authored one is completely unaffected.
// atDeclaration relaxes ONE case: a subject with no dispatcher route at all.
// Such a subject cannot actuate (Dispatcher.Fire returns "no actuation route"),
// so refusing to DECLARE a routine for it buys no safety and makes the control
// unusable on any deployment that has not provisioned a dispatcher — authoring
// a blocked list there would 422 every create. Actuation stays deny-closed, so
// the routine still cannot fire until its environment can be named.
func (m *Module) checkEnvironmentAtDeclaration(ctx context.Context, pol RoutinePolicy, subjectKind, subjectRef string) *routineDenial {
	return m.checkEnv(ctx, pol, subjectKind, subjectRef, http.StatusUnprocessableEntity, true)
}

func (m *Module) checkEnvironment(ctx context.Context, pol RoutinePolicy, subjectKind, subjectRef string, status int) *routineDenial {
	return m.checkEnv(ctx, pol, subjectKind, subjectRef, status, false)
}

func (m *Module) checkEnv(ctx context.Context, pol RoutinePolicy, subjectKind, subjectRef string, status int, allowUnrouted bool) *routineDenial {
	if len(pol.BlockedEnvs) == 0 {
		return nil
	}
	tc, err := m.targetEnv.Resolve(ctx, subjectKind, subjectRef)
	if err != nil {
		m.errorf("orchestration: target-environment resolver error; failing CLOSED", "subject", subjectRef, "err", err)
		d := denyUnreadable("the routine's actuation environment is unreadable and a routine policy blocks environments")
		d.policyRefs, d.digest = pol.PolicyRefs, pol.Digest
		return d
	}
	deny := func(msg string) *routineDenial {
		return &routineDenial{
			code: codeRoutineEnv, httpStatus: status, message: msg,
			policyRefs: pol.PolicyRefs, digest: pol.Digest,
		}
	}
	switch {
	case !tc.RouteFound:
		if allowUnrouted {
			return nil // declaring is fine; firing it will still be refused
		}
		return deny("a routine policy blocks environments, but this subject has no dispatcher route, so its environment cannot be established")
	case strings.TrimSpace(tc.Environment) == "":
		// A route that carries no environment dimension (the A2A delegation
		// route). An absent environment is NOT an implicitly safe one.
		return deny("a routine policy blocks environments, but this subject's actuation route carries no environment")
	case envBlocked(pol.BlockedEnvs, tc.Environment):
		return deny("the routine's actuation environment is blocked by a routine policy")
	}
	return nil
}

// envBlocked compares an operator-authored blocklist against an
// operator-authored dispatcher config value. Both are hand-typed in different
// files, so the comparison is canonical (trimmed, case-folded): "Prod" in the
// dispatch config against "prod" in the blocklist must not silently fail to
// bite. This mirrors the canonical comparison the cron allowlist already uses.
func envBlocked(blocked []string, env string) bool {
	e := canonEnv(env)
	for _, b := range blocked {
		if canonEnv(b) == e {
			return true
		}
	}
	return false
}

func canonEnv(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// checkFireCadence enforces the cadence floor on the ACTUAL elapsed interval
// since the routine last fired. This is the binding rate control: the module
// never self-fires from a cron expression (doc.go), so what a routine's cadence
// REALLY is, is how often something drives it through a fire path. It also
// closes the grandfathering hole — a routine declared before the floor existed
// is still held to it — and any disagreement between the declared interval and
// the cron text.
//
// It refuses with 429 + Retry-After because, unlike every other routine-policy
// refusal, the caller succeeds by waiting.
func (m *Module) checkFireCadence(pol RoutinePolicy, sched model.Record, last string) *routineDenial {
	if pol.MinIntervalSec <= 0 {
		return nil
	}
	if last == "" {
		return nil // never fired: nothing to be too soon after
	}
	ts, err := model.ParseTimestamp(last)
	if err != nil {
		// last_fired_at is ENGINE-written (advanceFired), so an unparseable
		// value means the row is corrupt. An unreadable governance input must
		// never resolve to "long enough ago" — the same rule the kill switch
		// states for an unreadable stop state.
		m.errorf("orchestration: schedule has an unreadable last_fired_at; refusing the fire (deny-closed)",
			"schedule", sched.String(model.ColID), "value", last)
		d := denyUnreadable("the routine's last-fired timestamp is unreadable and a routine policy sets a cadence floor")
		d.policyRefs, d.digest = pol.PolicyRefs, pol.Digest
		return d
	}
	// Compare in SECONDS, never by multiplying the floor into a time.Duration:
	// max_cadence_seconds is an operator-supplied int64 and a large-but-valid
	// value overflows Duration's nanosecond range, wrapping NEGATIVE so that
	// every elapsed time compares "greater" and the floor silently allows.
	elapsed := m.clock.Now().Time().Sub(ts.Time())
	elapsedSec := int64(elapsed / time.Second)
	if elapsed > 0 && elapsedSec >= pol.MinIntervalSec {
		return nil
	}
	remaining := time.Duration(0)
	if r := pol.MinIntervalSec - elapsedSec; r > 0 && r < int64(maxRetryAfterSec) {
		remaining = time.Duration(r) * time.Second
	} else {
		remaining = maxRetryAfter
	}
	// A clock that moved BACKWARDS yields a negative elapsed; that must deny
	// (the conservative direction), not wrap into "long enough ago".
	return &routineDenial{
		code: codeRoutineFloor, httpStatus: http.StatusTooManyRequests,
		message:    "this routine's subject fired too recently for the cadence floor in force",
		policyRefs: pol.PolicyRefs, digest: pol.Digest, retryAfter: remaining,
	}
}

// ---------------------------------------------------------------------------
// Atomic active-declaration admission (max_active_routines)
// ---------------------------------------------------------------------------

// maxRetryAfter bounds the Retry-After a cadence refusal advertises. It also
// keeps the header from disclosing an unusually large floor verbatim.
const (
	maxRetryAfter    = time.Hour
	maxRetryAfterSec = int64(3600)
)

// maxFenceRetries bounds the admission retry loop. Each retry is a full
// transaction replay against the winner's committed state.
const maxFenceRetries = 5

// errFenceContended is the internal sentinel for a lost CAS.
var errFenceContended = errors.New("orchestration: routine admission fence contended")

// withAdmissionFence runs fn inside a transaction that has FIRST won the
// tenant's routine admission fence, retrying the whole transaction when another
// admitter won the race.
//
// WHY A FENCE AND NOT JUST A COUNT. api.ScopedData.Mutate is atomic, but atomic
// is not serializable: the SQL store opens transactions with the driver default,
// which on PostgreSQL is READ COMMITTED, so two concurrent creates can both read
// "N-1 active" and both insert. (SQLite happens to serialize on its single
// connection, which is exactly why the default test harness would hide the bug.)
// A version CAS on one shared row makes the admission predicate serializable:
// under Read Committed the loser's UPDATE blocks on the winner, then re-evaluates
// its WHERE against the winning version, matches zero rows, and retries — where
// it now SEES the winner's committed schedule and the cap denies correctly.
//
// Every path that can change the active population takes it WHEN A CAP IS IN
// FORCE, including deactivation (which releases capacity, so it must not
// interleave with an admission that is counting). See needed= below for the
// no-cap case.
// needed=false runs fn in an ordinary transaction with NO fence. It is safe for
// the policy AS RESOLVED: a cap's population is selected by the axis value the
// candidate rows share with this routine, so a cap that could count this row is
// one this same request resolved — and it resolved none. That removes a
// tenant-wide serialization point from every schedule write in the deployments
// that never authored a cap, which is most of them.
//
// It is NOT a claim about the future. A cap authored AFTER this write applies
// from then on and counts this row like any other; it does not retroactively
// need to have serialized against it. The invariant is "no admission exceeds a
// cap in force at its own admission time", not "the population is bounded for
// all time".
func (m *Module) withAdmissionFence(ctx context.Context, mc api.ModuleContext, needed bool, fn func(sc store.Scope) error) error {
	if !needed {
		return mc.Data.Mutate(ctx, fn)
	}
	for attempt := 0; attempt < maxFenceRetries; attempt++ {
		err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
			if err := claimAdmissionFence(ctx, sc); err != nil {
				return err
			}
			return fn(sc)
		})
		if err == nil {
			return nil
		}
		// ONLY a lost fence CAS is retried. A store conflict from the BODY (a
		// duplicate schedule name, or the optimistic-concurrency check on the
		// schedule row itself) is a real answer for the caller — retrying it
		// would silently change the existing 409 semantics of those paths.
		if errors.Is(err, errFenceContended) {
			continue
		}
		return err
	}
	m.errorf("orchestration: routine admission fence exhausted its retries", "attempts", maxFenceRetries)
	// Exhaustion is a CONFLICT, never a success: the admission could not be
	// serialized, so the caller retries. It must never fall through to the write.
	return store.ErrConflict
}

// claimAdmissionFence gets-or-creates the tenant's fence row and version-CASes
// it, so exactly one concurrent transaction proceeds to count and admit.
func claimAdmissionFence(ctx context.Context, sc store.Scope) error {
	repo, err := sc.Ext(admissionFenceKind)
	if err != nil {
		return err
	}
	rec, found, err := findOne(ctx, repo, eq(colFenceKey, fenceKeyRoutine))
	if err != nil {
		return err
	}
	if !found {
		// First admitter in this tenant materializes the fence. A concurrent
		// creator collides on UNIQUE(tenant, fence_key) and retries.
		if _, cerr := repo.Create(ctx, model.Record{colFenceKey: fenceKeyRoutine}); cerr != nil {
			if errors.Is(cerr, store.ErrConflict) {
				return errFenceContended
			}
			return cerr
		}
		return nil
	}
	// The CAS itself: an unchanged record, updated purely to bump its version.
	if _, uerr := repo.Update(ctx, rec); uerr != nil {
		if errors.Is(uerr, store.ErrConflict) {
			return errFenceContended
		}
		return uerr
	}
	return nil
}

// admitActive checks EVERY matched active-declaration cap and returns a denial
// when any population is already at its limit. It must run inside a fenced
// transaction. excludeID is the schedule being transitioned (so a row already
// counted in the population does not block its own re-activation).
func admitActive(ctx context.Context, sc store.Scope, pol RoutinePolicy, scope RoutineScope, excludeID model.ID) (*routineDenial, error) {
	if len(pol.ActiveCaps) == 0 {
		return nil, nil
	}
	repo, err := sc.Ext(scheduleKind)
	if err != nil {
		return nil, err
	}
	actives, err := listAll(ctx, repo, eq(colDesiredStat, "active"))
	if err != nil {
		return nil, err
	}
	for _, ac := range pol.ActiveCaps {
		var n int64
		for _, rec := range actives {
			if model.ID(rec.String(model.ColID)) == excludeID {
				continue
			}
			// The POPULATION a cap constrains is the routines that share this
			// routine's axis value — resolved from each row's own frozen scope,
			// not from the policy's scope_ref (which may be authored as a
			// workspace slug while a schedule stores the id).
			switch ac.ScopeKind {
			case "tenant":
			case "workspace":
				// Normalise the candidate the SAME way the match did: an empty
				// workspace means the tenant default, so a routine declared by
				// an unconfined principal and one confined to that workspace
				// land in the SAME population instead of two disjoint ones.
				rowWS := rec.String(colWorkspaceRef)
				if rowWS == "" {
					rowWS = pol.DefaultWorkspaceRef
				}
				if rowWS != pol.EffectiveWorkspaceRef {
					continue
				}
			case "user":
				// Same normalisation on the user axis: a pre row carries no
				// owner_user_ref, and counting the raw column would make it
				// invisible to the very cap that governs it.
				if routineScopeOfSchedule(rec).UserRef != pol.EffectiveUserRef {
					continue
				}
			default:
				continue
			}
			n++
		}
		if n >= ac.Max {
			return &routineDenial{
				code: codeRoutineActiveCap, httpStatus: http.StatusUnprocessableEntity,
				message:    "the " + ac.ScopeKind + " is at its routine-policy cap for active routines",
				policyRefs: pol.PolicyRefs, digest: pol.Digest,
			}, nil
		}
	}
	return nil, nil
}

// reserveFireSlot re-checks the cadence floor against the subject's most recent
// fire OR RESERVATION and, if eligible, stamps this schedule's reservation —
// all inside one fenced transaction, so two concurrent approved fires cannot
// both pass. It is the actuation-side twin of admitActive.
//
// Returning (nil, nil) means the fire is admitted and the reservation is
// committed with the caller's transaction.
func (m *Module) reserveFireSlot(ctx context.Context, sc store.Scope, pol RoutinePolicy, sched model.Record) (*routineDenial, error) {
	if pol.MinIntervalSec <= 0 {
		return nil, nil
	}
	repo, err := sc.Ext(scheduleKind)
	if err != nil {
		return nil, err
	}
	kind, ref := sched.String(colSubjectKind), sched.String(colSubjectRef)
	id := model.ID(sched.String(model.ColID))
	fresh, err := repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	last := maxTS(fresh.String(colLastFiredAt), fresh.String(colFireReservedAt))
	if ref != "" {
		siblings, serr := listAll(ctx, repo, eq(colSubjectRef, ref))
		if serr != nil {
			return nil, serr
		}
		for _, rec := range siblings {
			if rec.String(colSubjectKind) != kind {
				continue
			}
			// Validate EVERY stamp, not just the lexicographic maximum: an
			// unreadable sibling that happens to sort low would otherwise be
			// skipped silently, and an unreadable governance input must not
			// resolve to "long enough ago".
			for _, v := range []string{rec.String(colLastFiredAt), rec.String(colFireReservedAt)} {
				if v == "" {
					continue
				}
				if _, perr := model.ParseTimestamp(v); perr != nil {
					return nil, fmt.Errorf("schedule %s has an unreadable fire timestamp", rec.String(model.ColID))
				}
				last = maxTS(last, v)
			}
		}
	}
	if d := m.checkFireCadence(pol, fresh, last); d != nil {
		return d, nil
	}
	fresh[colFireReservedAt] = m.clock.Now().String()
	if _, uerr := repo.Update(ctx, fresh); uerr != nil {
		return nil, uerr
	}
	return nil, nil
}

// fireAlreadyClaimed reports whether this effect has ALREADY been claimed — a
// retry of a fire that ran (or is running), not a new one.
//
// It gates the whole routine-policy admission at actuation, because the
// D-05 contract is that a re-POST REPLAYS the recorded outcome rather than
// re-actuating. Running the cadence floor first breaks that: the first fire
// stamps the reservation and advances last_fired_at, so the retry is refused
// 429 instead of replaying — and on the DAG path a crash-recovered step would
// record "blocked" for a step whose effect really actuated, which dependents
// then branch on. A replay actuates nothing, so it consumes no cadence slot and
// needs no admission.
//
// col is colOpApprovalRef for the direct fire (keyed by the single-use
// approval) or colOpOperationID for a workflow step (keyed by its deterministic
// operation id).
func (m *Module) fireAlreadyClaimed(ctx context.Context, mc api.ModuleContext, col, key string) (bool, error) {
	if key == "" {
		return false, nil
	}
	found := false
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(operationKind)
		if err != nil {
			return err
		}
		_, ok, ferr := findOne(ctx, repo, eq(col, key))
		found = ok
		return ferr
	})
	return found, err
}

// claimActivationApproval spends an activation approval EXACTLY once, inside
// the caller's transaction. UNIQUE(tenant, approval_ref) is the whole guard: a
// second attempt to ride the same decision collides and is refused.
//
// This is what makes "every way INTO the active population needs a human
// decision" true rather than aspirational. ApprovalGate.Status is a pure read —
// governance reports a decided approval's stored status unchanged, even past
// its own expiry, and re-anchors the time-box only on consume — so a plan hash
// that survives a pause/activate cycle (it does: pausing changes none of the
// bound fields) would otherwise let one approval re-activate a routine an
// operator keeps pausing, indefinitely and with no human in the loop.
func claimActivationApproval(ctx context.Context, sc store.Scope, approvalRef, scheduleRef, planHash string) (*routineDenial, error) {
	repo, err := sc.Ext(activationClaimKind)
	if err != nil {
		return nil, err
	}
	rec := model.Record{colAcApprovalRef: approvalRef, colAcPlanHash: planHash}
	setIf(rec, colAcScheduleRef, scheduleRef)
	if _, cerr := repo.Create(ctx, rec); cerr != nil {
		if errors.Is(cerr, store.ErrConflict) {
			return &routineDenial{
				code: codeRoutineApproval, httpStatus: http.StatusConflict,
				message: "this approval has already been used to activate a routine; a new decision is required",
			}, nil
		}
		return nil, cerr
	}
	return nil, nil
}

// auditRoutineDenial records a DECLARATION-time refusal as a semantic
// self-audit. It deliberately does NOT append to the decision ledger: that
// ledger is documented as the append-only FIRE/MISS evidence trail, and a
// refused declaration is neither. id is zero for a refused create (the schedule
// never existed) — model.AuditDraft explicitly permits a zero target.
//
// Best-effort, mirroring the kill-switch and budget denials: a lost evidence
// write is logged loudly, and NEVER turns a denial into an admission.
func (m *Module) auditRoutineDenial(ctx context.Context, mc api.ModuleContext, action string, id model.ID, d *routineDenial) {
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return auditEvent(ctx, sc, mc, action, scheduleKind, id, map[string]any{
			"code": d.code, "policy_refs": d.policyRefs, "policy_digest": d.digest,
		})
	}); err != nil {
		m.errorf("orchestration: failed to record routine-policy denial evidence (the denial stands)",
			"action", action, "code", d.code, "err", err)
	}
}
