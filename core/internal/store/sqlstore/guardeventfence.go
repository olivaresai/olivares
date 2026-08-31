// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardeventfence.go VERIFIES the deny-closed event-trigger fence. It never installs it.
//
// The division is forced by the server, not chosen: `CREATE EVENT TRIGGER` is superuser-only
// and every role this product uses is NOSUPERUSER (measured on 15.18/16.14/17.10/18.4 —
// the database OWNER gets `permission denied to create event trigger`). The DDL an operator
// applies lives in dialect.GuardEventFenceStmts; this file answers, from the connection the
// process will actually use, WHICH OF FIVE THINGS is true about it.
//
// FIVE ANSWERS, BECAUSE FOUR OF THEM ARE NOT THE SAME FAILURE:
//
//	installed   both legs present, both ALWAYS, handler byte-identical, handler not
//	            rewritable by the application role
//	absent      neither leg is there — this deployment never applied the DDL. It is NOT
//	            evidence of tampering, and until today no deployment could have had it, so
//	            refusing here would brick every upgrade. Loud, typed, and not fatal unless
//	            the operator declares the fence required.
//	divergent   somebody had it and it changed: a leg missing while the other stands, a leg
//	            not ALWAYS, a rewritten handler, or a handler the application role can
//	            rewrite. Refused, in the same class as a lookalike guard (guardunits.go:100).
//	unverified  the catalog could not be read. Never a pass, and never synthesized into
//	            "absent" — the third answer this repository has already paid for twice.
//	not_applicable
//	            this ENGINE cannot carry the object (SQLite has no event triggers), so its
//	            absence is evidence of nothing and there is nothing to repair. Added
//	            2026-08-06: it used to be delivered AS `unverified`, with the difference
//	            surviving only in the prose reason — recoverable by a human reading the log,
//	            lost to every parser and alert rule keying off the typed field.
//
// WHY THE HANDLER BODY IS PART OF THE IDENTITY, and the measurement that makes it the
// central fact rather than a nicety: on all four majors, in the SingleRole topology that
// deploy/postgres/01-app-role.sql provisions and the test harness defaults to, the
// application role OWNS THE SCHEMA and therefore the handler. One
// `CREATE OR REPLACE FUNCTION olivares_guard_fence() … BEGIN END` later, the fence accepts
// the DROP it existed to refuse — and pg_event_trigger still reads
// `evt=sql_drop state=A fn=olivares_guard_fence()`, character for character what a healthy
// fence reads. Projecting the event-trigger rows alone reports a neutralized fence as sound.
// So the projection carries the handler's 28-field canonical form, and the verdict also asks
// whether the application role could rewrite it — because a body that is correct now and
// rewritable by the caller is a fence with a hinge on the wrong side.

// guardEventFenceForm is one event trigger's structural projection, with evtenabled EXCLUDED for
// the same reason guardTriggerForm excludes tgenabled: a state transition is not a
// redefinition, and folding the two would make an enable change read as a rewritten object.
type guardEventFenceForm struct {
	Name             string  // evtname
	Event            string  // evtevent
	FunctionSchema   string  // the handler's schema
	FunctionName     string  // the handler's name
	TagFilterIsNull  bool    // evttags IS NULL — no tag filter, which is what "every command" means
	TagFilterCount   int64   // array_length(evttags, 1), 0 when NULL
	OwnerIsSuperuser bool    // rolsuper of evtowner
	Owner            optText // diagnostic only: the owner's NAME varies per deployment and is not part of the golden
}

// canonicalGuardEventFenceForm is what each leg must look like.
//
// The owner NAME is deliberately not pinned — a deployment may install the fence as any
// superuser — but `OwnerIsSuperuser` is, because it is not a naming choice: measured on all
// four majors, `ALTER EVENT TRIGGER … OWNER TO <NOSUPERUSER>` answers
// `permission denied to change owner of event trigger`, while `OWNER TO <another superuser>`
// succeeds. So a non-superuser owner is not merely undesirable, it is unreachable — and a
// projection reporting one means the reading is wrong, which is worth refusing over.
func canonicalGuardEventFenceForm(name, event string) guardEventFenceForm {
	return guardEventFenceForm{
		Name:             name,
		Event:            event,
		FunctionSchema:   dialect.EngineSchema,
		FunctionName:     dialect.GuardEventFenceHandlerFn,
		TagFilterIsNull:  true,
		TagFilterCount:   0,
		OwnerIsSuperuser: true,
	}
}

// guardEventFenceStateAlways is the only state a fence may serve in.
//
// `CREATE EVENT TRIGGER` produces 'O', and measured on all four majors an 'O' fence DOES NOT
// FIRE for a session that has set session_replication_role='replica' — a setting an ordinary
// role can hold through `GRANT SET ON PARAMETER session_replication_role`, which is accepted
// on 15, 16, 17 and 18 alike. The hub's earlier condition watched `event_triggers`, which does
// not exist before 17; this is the wider door, open on every supported major.
const guardEventFenceStateAlways = "A"

// guardEventFenceObservation is everything one reading saw. It is a snapshot of one connection at
// one instant, not a running state.
type guardEventFenceObservation struct {
	Legs                map[string]guardEventFenceForm // by evtname, only those that exist
	States              map[string]string              // raw evtenabled, by evtname, never normalised
	Handler             guardFunctionForm
	HandlerExists       bool
	HandlerOwner        string
	HandlerOwnerIsSuper bool // whether the handler's owner is a superuser
	AppMayRewrite       bool // the application role owns the handler, or can become a role that does
	AppRoleResolved     bool // whether the rewritability question was answerable at all
}

// guardEventFenceVerdict is the FIVE-answer result. It was four, and the fifth was being
// delivered as the first (2026-08-06).
//
// SQLite has no event triggers, so this control cannot exist there at all. That branch knew
// it — its reason string says "not applicable" in as many words — and then assigned
// `unverified`, which is the verdict for "I tried to observe and could not". A human reading
// the boot log recovered the difference from the prose; a parser, an alert rule or a posture
// dashboard keying off the typed field could not, and the two demand opposite responses: one
// needs nothing, the other needs someone to go and look. The test that covered this branch
// PINNED the loss by requiring `unverified`, so the gap was locked in rather than latent.
//
// This is the same class the repository has been closing all week — a fourth answer wearing a
// third one's clothes — and the shape of the fix is the shape it always is: give the state a
// NAME, and let the level follow the name.
type guardEventFenceVerdict int

const (
	// guardEventFenceUnverified is the zero value ON PURPOSE: a verdict nobody set is "I could not
	// look", never "installed". A zero value that means success is how a check that never ran
	// reports a pass. It is deliberately NOT the not-applicable one either: an engine that cannot
	// carry the control must be SAID, not defaulted into.
	guardEventFenceUnverified guardEventFenceVerdict = iota
	guardEventFenceAbsent
	guardEventFenceDivergent
	guardEventFenceInstalled
	// guardEventFenceNotApplicable: this ENGINE cannot carry the object, so its absence is not
	// evidence of anything and there is nothing for an operator to repair. Distinct from
	// `absent` (the engine could carry it and does not) and from `unverified` (the engine could
	// carry it and the measurement did not complete).
	guardEventFenceNotApplicable
)

func (v guardEventFenceVerdict) String() string {
	switch v {
	case guardEventFenceAbsent:
		return "absent"
	case guardEventFenceDivergent:
		return "divergent"
	case guardEventFenceInstalled:
		return "installed"
	case guardEventFenceNotApplicable:
		return "not_applicable"
	default:
		return "unverified"
	}
}

// guardEventFenceStatus is the verdict plus the reasons that justify it, in a fixed order.
type guardEventFenceStatus struct {
	Verdict guardEventFenceVerdict
	Reasons []string
}

// guardEventFenceProjectionSQL reads both legs and their owners in one statement.
//
// Every catalog is schema-qualified against search_path shadowing, evttags keeps its NULL
// distinct from an empty array (a fence with an empty tag filter is not a fence without one),
// and evtenabled is read RAW so a fifth value is refused downstream rather than mapped onto
// one of the four this code knows.
// The leg names are BOUND, following the rule this package already paid for: a catalog
// identifier packed into a delimited string loses any name containing the delimiter
// (appendonly_acl.go:183-188). These particular names are compile-time constants, but the
// pattern is the one that stays correct when they stop being.
func guardEventFenceProjectionSQL(names []string) (string, []any) {
	list, args := tableParams(nil, names)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$1,$2,…" placeholders. The names travel as bound args.
	return `SELECT e.evtname,
       e.evtevent,
       e.evtenabled,
       fn.nspname AS function_schema,
       p.proname  AS function_name,
       e.evttags IS NULL AS tags_is_null,
       COALESCE(pg_catalog.array_length(e.evttags, 1), 0) AS tags_count,
       o.rolname  AS owner_name,
       o.rolsuper AS owner_is_superuser
FROM pg_catalog.pg_event_trigger e
JOIN pg_catalog.pg_proc p ON p.oid = e.evtfoid
JOIN pg_catalog.pg_namespace fn ON fn.oid = p.pronamespace
JOIN pg_catalog.pg_roles o ON o.oid = e.evtowner
WHERE e.evtname IN (` + list + `)`, args
}

// projectGuardEventFence reads the fence as it exists right now.
//
// It returns an error for a reading it could not complete — never a synthesized absence.
// "The query failed" and "the object is not there" are the two answers this whole file is
// built to keep apart.
func projectGuardEventFence(ctx context.Context, q rowQuerier, appRole string, major int) (guardEventFenceObservation, error) {
	obs := guardEventFenceObservation{
		Legs:   make(map[string]guardEventFenceForm, len(dialect.GuardEventFenceEvents())),
		States: make(map[string]string, len(dialect.GuardEventFenceEvents())),
	}
	names := guardEventFenceLegNames()
	query, args := guardEventFenceProjectionSQL(names)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return guardEventFenceObservation{}, fmt.Errorf("sqlstore: project the guard fence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			f     guardEventFenceForm
			state string
			owner sql.NullString
		)
		if err := rows.Scan(&f.Name, &f.Event, &state, &f.FunctionSchema, &f.FunctionName,
			&f.TagFilterIsNull, &f.TagFilterCount, &owner, &f.OwnerIsSuperuser); err != nil {
			return guardEventFenceObservation{}, fmt.Errorf("sqlstore: decode the guard fence projection: %w", err)
		}
		f.Owner = nullToOpt(owner)
		if _, dup := obs.Legs[f.Name]; dup {
			return guardEventFenceObservation{}, fmt.Errorf("sqlstore: the guard fence projection returned %q twice; pg_event_trigger.evtname is unique, so this reading is not one the catalog can produce", f.Name)
		}
		obs.Legs[f.Name] = f
		obs.States[f.Name] = state
	}
	if err := rows.Err(); err != nil {
		return guardEventFenceObservation{}, fmt.Errorf("sqlstore: project the guard fence: %w", err)
	}

	handler, exists, err := projectGuardFunction(ctx, q, dialect.EngineSchema, dialect.GuardEventFenceHandlerFn)
	if err != nil {
		return guardEventFenceObservation{}, err
	}
	obs.Handler, obs.HandlerExists = handler, exists

	if exists {
		owner, ownerSuper, mayRewrite, resolved, err := guardEventFenceHandlerOwnership(ctx, q, appRole, major)
		if err != nil {
			return guardEventFenceObservation{}, err
		}
		obs.HandlerOwner, obs.HandlerOwnerIsSuper = owner, ownerSuper
		obs.AppMayRewrite, obs.AppRoleResolved = mayRewrite, resolved
	}
	return obs, nil
}

// guardEventFenceHandlerOwnershipSQL asks the question that matters — not "who owns the handler"
// but "can the application role BECOME whoever owns it".
//
// The reachability predicate is the transitive closure built, and it is used here for
// the reason it was built: a direct predicate answers "can this role rewrite the function
// now", and the fence needs "can it arrange to". ADMIN on an intermediary is a capability over
// everything that intermediary reaches, and only a closure sees it.
func guardEventFenceHandlerOwnershipSQL(major int) string {
	// #nosec G202 -- guardReachableCTE and guardRoleReachability return COMPILE-TIME constants
	// of this package; the only interpolation is that constant text, and the role is bound.
	return guardReachableCTE(major) + `SELECT r.rolname,
       r.rolsuper,
       (r.rolname = $2 OR ` + guardRoleReachability(major) + `) AS app_may_rewrite
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_roles r ON r.oid = p.proowner
WHERE n.nspname = $1 AND p.proname = $3`
}

func guardEventFenceHandlerOwnership(ctx context.Context, q rowQuerier, appRole string, major int) (string, bool, bool, bool, error) {
	if strings.TrimSpace(appRole) == "" {
		// The question is unanswerable, and an unanswerable question is not a "no". The caller
		// turns this into a reason, not into a pass.
		return "", false, false, false, nil
	}
	rows, err := q.QueryContext(ctx, guardEventFenceHandlerOwnershipSQL(major),
		dialect.EngineSchema, appRole, dialect.GuardEventFenceHandlerFn)
	if err != nil {
		return "", false, false, false, fmt.Errorf("sqlstore: read who may rewrite the guard fence handler: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", false, false, false, fmt.Errorf("sqlstore: read who may rewrite the guard fence handler: %w", err)
		}
		// projectGuardFunction already said the function exists, so no row here means the two
		// readings disagree — which is a failed reading, not an absence.
		return "", false, false, false, fmt.Errorf("sqlstore: the guard fence handler %s.%s was projected but has no owner row; this reading is incomplete",
			dialect.EngineSchema, dialect.GuardEventFenceHandlerFn)
	}
	var (
		owner      string
		ownerSuper bool
		mayRewrite bool
	)
	if err := rows.Scan(&owner, &ownerSuper, &mayRewrite); err != nil {
		return "", false, false, false, fmt.Errorf("sqlstore: decode the guard fence handler's ownership: %w", err)
	}
	if rows.Next() {
		return "", false, false, false, fmt.Errorf("sqlstore: %s.%s has more than one overload, which this fence cannot represent: one handler identity carries one footprint",
			dialect.EngineSchema, dialect.GuardEventFenceHandlerFn)
	}
	return owner, ownerSuper, mayRewrite, true, rows.Err()
}

// guardEventFenceLegNames returns the leg names in a FIXED order, so reasons and messages cannot
// depend on map iteration.
func guardEventFenceLegNames() []string {
	events := dialect.GuardEventFenceEvents()
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// judgeGuardEventFence turns one observation into one of the four answers.
//
// ABSENT IS THE UNANIMOUS CASE ONLY. One leg standing and the other gone is DIVERGENT, not
// absent: nobody installs half a fence, so half a fence means the other half was removed.
func judgeGuardEventFence(obs guardEventFenceObservation) guardEventFenceStatus {
	events := dialect.GuardEventFenceEvents()
	names := guardEventFenceLegNames()

	present := 0
	for _, name := range names {
		if _, ok := obs.Legs[name]; ok {
			present++
		}
	}
	if present == 0 && !obs.HandlerExists {
		return guardEventFenceStatus{Verdict: guardEventFenceAbsent}
	}

	var reasons []string
	for _, name := range names {
		leg, ok := obs.Legs[name]
		if !ok {
			reasons = append(reasons, fmt.Sprintf("%s: the event trigger is missing while the rest of the fence stands", name))
			continue
		}
		want := canonicalGuardEventFenceForm(name, events[name])
		reasons = append(reasons, prefixEach(name, guardEventFenceFormDiff(want, leg))...)
		if state := obs.States[name]; state != guardEventFenceStateAlways {
			reasons = append(reasons, fmt.Sprintf(
				"%s: evtenabled is %q and must be %q; an event trigger that is not ALWAYS does not fire for a session that has set session_replication_role='replica', which is a setting an ordinary role can be granted on every supported major",
				name, state, guardEventFenceStateAlways))
		}
	}

	switch {
	case !obs.HandlerExists:
		reasons = append(reasons, fmt.Sprintf("%s.%s: the handler function the fence executes is missing, so the fence cannot refuse anything",
			dialect.EngineSchema, dialect.GuardEventFenceHandlerFn))
	default:
		reasons = append(reasons, prefixEach(dialect.GuardEventFenceHandlerFn,
			guardFunctionDiff(canonicalGuardEventFenceHandler(), obs.Handler))...)
		switch {
		case !obs.AppRoleResolved:
			reasons = append(reasons, fmt.Sprintf("%s: whether the application role can rewrite the handler could not be established, and an unanswered question is not a negative answer",
				dialect.GuardEventFenceHandlerFn))
		case obs.AppMayRewrite:
			reasons = append(reasons, fmt.Sprintf(
				"%s: the handler is owned by %q, which the application role owns or can become — one CREATE OR REPLACE leaves pg_event_trigger reading exactly as it does now and the fence refusing nothing",
				dialect.GuardEventFenceHandlerFn, obs.HandlerOwner))
		case !obs.HandlerOwnerIsSuper:
			// ASKING ONLY ABOUT THE APPLICATION ROLE WAS THE WRONG QUESTION, and the
			// difference is not academic: measured on all four majors, a THIRD ordinary
			// role owning the handler rewrote it into a no-op while this check answered
			// "the app cannot reach it", the fence verified as installed, and the guard
			// was dropped through it moments later. The owner of each leg is already
			// required to be a superuser — PostgreSQL enforces it — and the handler is the
			// other half of the same object, so it carries the same requirement.
			reasons = append(reasons, fmt.Sprintf(
				"%s: the handler is owned by %q, which is not a superuser; any role that owns this function can replace its body, and the event-trigger rows do not change when it does",
				dialect.GuardEventFenceHandlerFn, obs.HandlerOwner))
		}
	}

	if len(reasons) == 0 {
		return guardEventFenceStatus{Verdict: guardEventFenceInstalled}
	}
	return guardEventFenceStatus{Verdict: guardEventFenceDivergent, Reasons: reasons}
}

func prefixEach(prefix string, in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, prefix+": "+s)
	}
	return out
}

// guardEventFenceFormDiff names every field in which an observed leg differs from the canonical
// one, in a fixed order and with the same "want X, got Y" contract guardDefinitionDiff uses.
func guardEventFenceFormDiff(want, got guardEventFenceForm) []string {
	var out []string
	add := func(field, w, g string) {
		if w != g {
			out = append(out, fmt.Sprintf("%s: want %s, got %s", field, w, g))
		}
	}
	add("evtname", want.Name, got.Name)
	add("evtevent", want.Event, got.Event)
	add("handler schema", want.FunctionSchema, got.FunctionSchema)
	add("handler name", want.FunctionName, got.FunctionName)
	add("evttags IS NULL", fmt.Sprintf("%t", want.TagFilterIsNull), fmt.Sprintf("%t", got.TagFilterIsNull))
	add("evttags length", fmt.Sprintf("%d", want.TagFilterCount), fmt.Sprintf("%d", got.TagFilterCount))
	add("owner is superuser", fmt.Sprintf("%t", want.OwnerIsSuperuser), fmt.Sprintf("%t", got.OwnerIsSuperuser))
	return out
}

// canonicalGuardEventFenceHandler is the handler's declared 28-field footprint.
//
// It reuses guardFunctionForm — the SAME form and the SAME comparator the shared row-guard
// function is judged by — so the handler cannot drift under a comparison written specially
// for it. The two differences from a row guard's function are consequences of what it is: it
// returns pg_catalog.event_trigger rather than trigger, and its body is
// dialect.GuardEventFenceHandlerBody.
func canonicalGuardEventFenceHandler() guardFunctionForm {
	return guardFunctionForm{
		Schema:           dialect.EngineSchema,
		Name:             dialect.GuardEventFenceHandlerFn,
		Kind:             "f",
		ReturnTypeSchema: "pg_catalog",
		ReturnTypeName:   "event_trigger",
		Language:         "plpgsql",
		Variadic:         "0",
		AllArgTypesNull:  true,
		ArgModesNull:     true,
		ArgNamesNull:     true,
		ArgDefaultsNull:  true,
		Src:              dialect.GuardEventFenceHandlerBody,
		Volatile:         "v",
		Parallel:         "u",
		Cost:             100,
		Rows:             0,
		Support:          "0",
		TransformsNull:   true,
		ConfigNull:       true,
	}
}

// GuardEventFencePolicy is the operator's declared posture toward the fence.
type GuardEventFencePolicy = store.GuardEventFencePolicy

// runGuardEventFenceCheck is the boot leg's caller: it resolves what the check needs from this
// connection, states the outcome out loud, and returns an error only when the posture says
// the boot must stop.
//
// SAYING IT OUT LOUD IS HALF THE JOB. A fence that is absent under the default posture does
// not stop the boot, so the only thing standing between "not installed" and "nobody ever
// noticed" is this log line. An absent control that logs nothing is indistinguishable from a
// present one.
func runGuardEventFenceCheck(ctx context.Context, q rowQuerier, dia dialect.Dialect, policy GuardEventFencePolicy, appRole string) error {
	if !policy.Valid() {
		return fmt.Errorf("sqlstore: %q is not a guard fence policy this build understands; the accepted values are %v — an unrecognized one is refused rather than resolved to the default, because a typo that read as %q would be a required fence quietly downgraded",
			string(policy), store.GuardEventFencePolicies(), string(store.GuardEventFenceVerify))
	}
	resolved := policy.Resolve()

	major := 0
	if dia.Name() == store.EnginePostgres && resolved != store.GuardEventFenceOff {
		m, err := postgresServerMajor(ctx, q)
		if err != nil {
			// The reachability predicate is major-dependent, so an unknown major makes the
			// reading unsound rather than merely awkward. That is UNVERIFIED, and under a
			// required posture it is a refusal.
			if resolved == store.GuardEventFenceRequired {
				return fmt.Errorf("sqlstore: the guard fence is required and this connection's PostgreSQL major could not be read, so the check could not be performed soundly: %w", err)
			}
			// NOT APPLICABLE is a FOURTH answer traveling as the third: the reason field
			// itself says "the sqlite engine has no event triggers, so the deny-closed fence
			// is not applicable and its absence is not evidence of anything". An engine that
			// cannot have the thing is not a deployment missing it.
			slog.Info("the deny-closed guard fence was NOT verified: this connection's PostgreSQL major could not be read, and the check is major-dependent",
				"fence_status", guardEventFenceUnverified.String(), "err", err)
			return nil
		}
		major = m
	}

	status, err := verifyGuardEventFence(ctx, q, dia, resolved, appRole, major)
	if err != nil {
		return err
	}
	switch status.Verdict {
	case guardEventFenceInstalled:
		slog.Info("the deny-closed guard fence is installed and matches what this build declares",
			"fence_status", status.Verdict.String(), "policy", string(resolved))
	case guardEventFenceAbsent:
		slog.Warn("the deny-closed guard fence is NOT INSTALLED: nothing prevents a DROP TRIGGER or an ALTER TABLE ... DISABLE TRIGGER from removing an append-only guard between two boots. It is a superuser deployment step this engine cannot perform for you — apply the DDL this build renders",
			"fence_status", status.Verdict.String(), "policy", string(resolved),
			"statements", len(dialect.GuardEventFenceStmts()))
	case guardEventFenceNotApplicable:
		// INFO, and the level follows the verdict rather than the other way round. This used
		// to print "the deny-closed guard fence was NOT verified" at WARN on every single
		// SQLite boot — an alarm about a control the engine cannot have, which teaches the
		// operator to ignore that line, which is the cost a false alarm actually charges.
		// There is nothing to repair here and nothing was left unmeasured.
		slog.Info("the deny-closed guard fence does not apply to this engine",
			"fence_status", status.Verdict.String(), "policy", string(resolved),
			"reasons", strings.Join(status.Reasons, "; "))
	default:
		slog.Warn("the deny-closed guard fence was NOT verified",
			"fence_status", status.Verdict.String(), "policy", string(resolved),
			"reasons", strings.Join(status.Reasons, "; "))
	}
	return nil
}

// verifyGuardEventFence is the boot leg. It returns the status it reached AND an error only when
// the posture says the boot must not continue.
//
// The asymmetry between absent and divergent is the whole design, and it is not timidity:
//
//   - ABSENT means this deployment never applied the superuser DDL. No deployment in the
//     world has, because until this edition the DDL did not exist. A boot that refused here
//     would turn every upgrade into an outage and would make the fence impossible to adopt.
//   - DIVERGENT means the fence was there and no longer matches. That is the same observation
//     the rollout already treats as a refusal for a row guard, and the runtime must not weaken
//     a fact merely because it learned it earlier than the next boot.
//   - UNVERIFIED is never a pass. Under GuardEventFenceRequired it refuses, because a required
//     control that cannot be read is not a control.
func verifyGuardEventFence(ctx context.Context, q rowQuerier, dia dialect.Dialect, policy GuardEventFencePolicy, appRole string, major int) (guardEventFenceStatus, error) {
	if dia.Name() != store.EnginePostgres {
		// SQLite has no event triggers, and a status of "installed" on an engine that cannot
		// carry the object would be a green measuring nothing. It is not applicable, said out
		// loud rather than by returning success.
		return guardEventFenceStatus{
			Verdict: guardEventFenceNotApplicable,
			Reasons: []string{fmt.Sprintf("the %s engine has no event triggers, so the deny-closed fence is not applicable and its absence is not evidence of anything", dia.Name())},
		}, nil
	}
	if policy == store.GuardEventFenceOff {
		return guardEventFenceStatus{
			Verdict: guardEventFenceUnverified,
			Reasons: []string{"the guard fence check is switched off by configuration, so this boot states nothing about it"},
		}, nil
	}

	obs, err := projectGuardEventFence(ctx, q, appRole, major)
	if err != nil {
		status := guardEventFenceStatus{Verdict: guardEventFenceUnverified, Reasons: []string{err.Error()}}
		if policy == store.GuardEventFenceRequired {
			return status, fmt.Errorf("sqlstore: the guard fence is required and could not be read, which is not the same as absent: %w", err)
		}
		return status, nil
	}

	status := judgeGuardEventFence(obs)
	switch status.Verdict {
	case guardEventFenceInstalled:
		return status, nil
	case guardEventFenceAbsent:
		if policy == store.GuardEventFenceRequired {
			return status, fmt.Errorf("sqlstore: the guard fence is required by configuration and is not installed; apply the superuser DDL this build renders (%d statements) and retry",
				len(dialect.GuardEventFenceStmts()))
		}
		return status, nil
	default:
		return status, fmt.Errorf("sqlstore: the guard fence no longer matches what this build declares, which means it was installed and then changed: %s",
			strings.Join(status.Reasons, "; "))
	}
}
