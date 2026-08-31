// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// installedEventFenceObservation is a healthy reading, built from the SAME canonical
// constructors production compares against — so a change to the golden moves this fixture with
// it instead of leaving a stale literal that passes for the wrong reason.
func installedEventFenceObservation() guardEventFenceObservation {
	obs := guardEventFenceObservation{
		Legs:                map[string]guardEventFenceForm{},
		States:              map[string]string{},
		Handler:             canonicalGuardEventFenceHandler(),
		HandlerExists:       true,
		HandlerOwner:        "postgres",
		HandlerOwnerIsSuper: true,
		AppMayRewrite:       false,
		AppRoleResolved:     true,
	}
	for name, event := range dialect.GuardEventFenceEvents() {
		obs.Legs[name] = canonicalGuardEventFenceForm(name, event)
		obs.States[name] = guardEventFenceStateAlways
	}
	return obs
}

func TestAHealthyEventFenceReadingIsJudgedInstalled(t *testing.T) {
	status := judgeGuardEventFence(installedEventFenceObservation())
	if status.Verdict != guardEventFenceInstalled {
		t.Fatalf("verdict: want %s, got %s (reasons: %v)", guardEventFenceInstalled, status.Verdict, status.Reasons)
	}
	if len(status.Reasons) != 0 {
		t.Fatalf("an installed fence carries no reasons; got %v", status.Reasons)
	}
}

// TestTheEventFenceComparatorNamesEveryFieldOfItsForm is the totality proof, and it walks the
// TYPE rather than a list of field names.
//
// A hand-written list shares its own omissions: a field added to guardEventFenceForm and not
// added to the list would be invisible to the comparator AND to the test that is supposed to
// notice, and both would stay green. Reflection makes the type itself the enumeration, and the
// literal count below makes a field ADDED to the type visible as a failure rather than
// absorbed silently.
func TestTheEventFenceComparatorNamesEveryFieldOfItsForm(t *testing.T) {
	const declaredFields = 8
	form := reflect.TypeOf(guardEventFenceForm{})
	if form.NumField() != declaredFields {
		t.Fatalf("guardEventFenceForm carries %d fields and this test expects %d: a field added without a mutation here is a field the comparator may ignore in silence, which is the only way this fence can be wrong and green",
			form.NumField(), declaredFields)
	}

	// Owner is diagnostic by declaration: a deployment may install the fence as any
	// superuser, so pinning the NAME would report drift on a legitimate install. What is
	// pinned is OwnerIsSuperuser, which is not a naming choice.
	diagnostic := map[string]string{
		"Owner": "the owner's NAME varies per deployment; OwnerIsSuperuser carries the part that does not",
	}

	for i := 0; i < form.NumField(); i++ {
		field := form.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			if why, ok := diagnostic[field.Name]; ok {
				t.Logf("EVENT_FENCE_FIELD|%s|diagnostic: %s", field.Name, why)
				return
			}
			want := canonicalGuardEventFenceForm(dialect.GuardEventFenceDropTrigger, "sql_drop")
			got := want
			mutateEventFenceField(t, &got, field.Name)
			diff := guardEventFenceFormDiff(want, got)
			if len(diff) == 0 {
				t.Fatalf("mutating %s changed nothing the comparator reports: this field can drift and the fence will call it canonical", field.Name)
			}
		})
	}
}

// mutateEventFenceField changes exactly one field to a value that is not its canonical one.
// It is a switch rather than a reflective "set the zero value" because two of the canonical
// values ARE the zero value, and mutating those to zero would be a mutation that does not
// mutate — the compile-red-is-not-a-mutation trap in another dress.
func mutateEventFenceField(t *testing.T, f *guardEventFenceForm, name string) {
	t.Helper()
	switch name {
	case "Name":
		f.Name += "_x"
	case "Event":
		f.Event = "ddl_command_start"
	case "FunctionSchema":
		f.FunctionSchema += "_x"
	case "FunctionName":
		f.FunctionName += "_x"
	case "TagFilterIsNull":
		f.TagFilterIsNull = !f.TagFilterIsNull
	case "TagFilterCount":
		f.TagFilterCount++
	case "OwnerIsSuperuser":
		f.OwnerIsSuperuser = !f.OwnerIsSuperuser
	default:
		t.Fatalf("guardEventFenceForm gained the field %q and this switch does not mutate it: the totality test above would then pass over a field nothing compares", name)
	}
}

// TestAnUnreadableEventFenceIsNeverAnAbsentOne is the third answer, stated as a test.
//
// A missing handler with the legs standing is DIVERGENT, and a reading that produced nothing at
// all is ABSENT only when nothing at all is there. The case that must never appear is a
// verdict of "installed" reached by a reading that saw less than it needed to.
func TestAnUnreadableEventFenceIsNeverAnAbsentOne(t *testing.T) {
	obs := installedEventFenceObservation()
	obs.HandlerExists = false
	obs.Handler = guardFunctionForm{}
	status := judgeGuardEventFence(obs)
	if status.Verdict != guardEventFenceDivergent {
		t.Fatalf("legs standing with no handler is divergent, not %s", status.Verdict)
	}
	if !strings.Contains(strings.Join(status.Reasons, "; "), "the handler function the fence executes is missing") {
		t.Fatalf("the reason must name the missing handler; got %v", status.Reasons)
	}
}

// TestAHandlerOwnedByANonSuperuserIsDivergent is round 18's blocker 1 at the unit level: the
// question "can the APPLICATION role reach the owner" is not the question "is this handler
// safe", and a third ordinary role owning it is exactly the gap between them.
func TestAHandlerOwnedByANonSuperuserIsDivergent(t *testing.T) {
	obs := installedEventFenceObservation()
	obs.HandlerOwner, obs.HandlerOwnerIsSuper = "some_ordinary_role", false
	status := judgeGuardEventFence(obs)
	if status.Verdict != guardEventFenceDivergent {
		t.Fatalf("a handler owned by a non-superuser must not read as installed; got %s", status.Verdict)
	}
	if !strings.Contains(strings.Join(status.Reasons, "; "), "which is not a superuser") {
		t.Fatalf("the reason must name the owner's posture; got %v", status.Reasons)
	}
}

// TestAnUnresolvedRewritabilityQuestionIsNotANegativeAnswer: not being able to establish whether
// the application role can rewrite the handler is a reason, never a pass. The zero value of
// AppMayRewrite is false, and false read as "safe" is exactly how an unasked question becomes a
// clean bill of health.
func TestAnUnresolvedRewritabilityQuestionIsNotANegativeAnswer(t *testing.T) {
	obs := installedEventFenceObservation()
	obs.AppRoleResolved = false
	obs.AppMayRewrite = false
	status := judgeGuardEventFence(obs)
	if status.Verdict != guardEventFenceDivergent {
		t.Fatalf("an unanswered rewritability question must not read as installed; got %s", status.Verdict)
	}
	if !strings.Contains(strings.Join(status.Reasons, "; "), "an unanswered question is not a negative answer") {
		t.Fatalf("the reason must say WHY it is not a pass; got %v", status.Reasons)
	}
}

// TestTheZeroVerdictIsUnverified pins the enum's zero value. A verdict nobody set must not read
// as success — the same rule the audit-spool and vulnerability gates in this repo have already
// paid for.
func TestTheZeroVerdictIsUnverified(t *testing.T) {
	var v guardEventFenceVerdict
	if v != guardEventFenceUnverified {
		t.Fatalf("the zero verdict is %s; a check that never ran would report it", v)
	}
	if v.String() != "unverified" {
		t.Fatalf("the zero verdict renders as %q", v.String())
	}
}

// TestSQLiteCannotCarryTheFenceAndSaysSo: an engine with no event triggers must not report a
// healthy fence, and must not report a FAILED one either.
//
// This test used to require the verdict to be exactly `unverified`, which is how the loss it
// was written to prevent ended up PINNED IN PLACE (2026-08-06). Its own comment already said
// "not applicable is a third answer" — and then demanded the type say "I could not look".
// The reason string carried the difference and the typed field did not, so a human reading
// the boot log could tell them apart and no parser, alert rule or posture dashboard could.
//
// The guarantee it was written for is intact and now stated directly: NOT installed (a green
// measuring an object that cannot exist) and NOT unverified (an alarm about a measurement
// that never failed). What changed is that the test asserts the intent instead of one
// spelling of it.
func TestSQLiteCannotCarryTheFenceAndSaysSo(t *testing.T) {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("the SQLite dialect must exist")
	}
	status, err := verifyGuardEventFence(context.Background(), nil, dia, store.GuardEventFenceVerify, "app", 0)
	if err != nil {
		t.Fatalf("SQLite must not fail the boot over an object it cannot have: %v", err)
	}
	if status.Verdict != guardEventFenceNotApplicable {
		t.Fatalf("SQLite reports %s, want not_applicable: an engine that CANNOT carry the fence is not the same state as one whose fence could not be read", status.Verdict)
	}
	// The two it must never be, named rather than implied — one is a false green, the other a
	// false alarm on every SQLite boot the product has ever done.
	if status.Verdict == guardEventFenceInstalled {
		t.Fatal("SQLite reports the fence installed; that is a green measuring an object that cannot exist")
	}
	if status.Verdict == guardEventFenceUnverified {
		t.Fatal("SQLite reports unverified; nothing failed to be measured, so this is an alarm nobody can act on")
	}
	if !strings.Contains(strings.Join(status.Reasons, "; "), "not applicable") {
		t.Fatalf("the reason must say it is not applicable rather than implying absence: %v", status.Reasons)
	}
}

// TestGuardEventFenceVerdictsAreAllDistinctlyNamed: a verdict added to the enum without a case
// in String() serializes as "unverified", which is exactly how the not-applicable state spent
// its life being reported as something else. The enum and its spelling stay in step here so
// the next one cannot arrive silently.
func TestGuardEventFenceVerdictsAreAllDistinctlyNamed(t *testing.T) {
	all := []guardEventFenceVerdict{
		guardEventFenceUnverified, guardEventFenceAbsent, guardEventFenceDivergent,
		guardEventFenceInstalled, guardEventFenceNotApplicable,
	}
	seen := map[string]bool{}
	for _, v := range all {
		name := v.String()
		if seen[name] {
			t.Errorf("two verdicts serialize as %q; one of them has no case in String() and is being reported as another state", name)
		}
		seen[name] = true
	}
	// And the guard against the enum growing past what this test knows: an unhandled value
	// must not quietly become "unverified" without anyone noticing.
	if next := guardEventFenceNotApplicable + 1; next.String() != "unverified" {
		t.Fatalf("the default arm changed to %q; if a new verdict was added, add it above", next.String())
	}
	if len(seen) != len(all) {
		t.Fatalf("%d verdicts produced %d distinct names", len(all), len(seen))
	}
}

// TestEveryEventFencePolicyIsAccountedFor keeps the vocabulary and its validator from drifting
// apart, and pins that the empty value resolves to verify rather than to off.
func TestEveryEventFencePolicyIsAccountedFor(t *testing.T) {
	policies := store.GuardEventFencePolicies()
	if len(policies) != 3 {
		t.Fatalf("the policy vocabulary has %d values and this test expects 3: one added without a decision here is one no boot path considers", len(policies))
	}
	for _, p := range policies {
		if !p.Valid() {
			t.Errorf("%q is in the vocabulary and its own validator rejects it", p)
		}
	}
	if got := store.GuardEventFencePolicy("").Resolve(); got != store.GuardEventFenceVerify {
		t.Errorf("the empty policy resolves to %q; a deployment that sets nothing must still VERIFY, not fall silent", got)
	}
	for _, bad := range []string{"requried", "on", "true", "VERIFY"} {
		if store.GuardEventFencePolicy(bad).Valid() {
			t.Errorf("%q is accepted as a policy; an unrecognized value must be refused rather than resolved to the default", bad)
		}
	}
}

// TestTheRenderedFenceDDLCarriesItsEnableAlways is the cheap guard over the measured fact that
// makes the DDL correct rather than merely present: CREATE EVENT TRIGGER leaves evtenabled='O',
// and an 'O' fence does not fire for a session that has set session_replication_role='replica'.
//
// The PostgreSQL matrix proves the consequence against real servers. This proves the statement
// is in the rendered DDL at all, which is the part that can be deleted by an editor who reads
// it as redundant.
func TestTheRenderedFenceDDLCarriesItsEnableAlways(t *testing.T) {
	stmts := dialect.GuardEventFenceStmts()
	if len(stmts) != 6 {
		t.Fatalf("the fence DDL renders %d statements and this test expects 6 (one handler, two event triggers, two ENABLE ALWAYS, one OWNER TO): a statement added or removed without updating this count is one nothing checks", len(stmts))
	}
	// The OWNER TO is round 18's blocker 1 and it is load-bearing: CREATE OR REPLACE
	// preserves a pre-existing owner, so without it a role that pre-created the handler
	// stays its owner through the operator's re-apply and can rewrite it back to a no-op.
	wantOwner := fmt.Sprintf("ALTER FUNCTION %s.%s() OWNER TO CURRENT_USER", dialect.EngineSchema, dialect.GuardEventFenceHandlerFn)
	foundOwner := false
	for _, s := range stmts {
		if s == wantOwner {
			foundOwner = true
		}
	}
	if !foundOwner {
		t.Error("the rendered DDL never converges the handler's owner: a role that pre-created the function keeps ownership through the re-apply and can replace the body afterwards, with pg_event_trigger reading exactly the same")
	}
	for _, leg := range []string{dialect.GuardEventFenceDropTrigger, dialect.GuardEventFenceEndTrigger} {
		want := fmt.Sprintf("ALTER EVENT TRIGGER %s ENABLE ALWAYS", leg)
		found := false
		for _, s := range stmts {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the rendered DDL never makes %s ALWAYS: it would install at ORIGIN, which does not fire for a session holding session_replication_role='replica' — a setting an ordinary role can be granted on every certified major", leg)
		}
	}
	if !strings.Contains(stmts[0], dialect.GuardEventFenceHandlerBody) {
		t.Error("the rendered handler does not carry the exact body the verifier compares against, so this build could install a fence its own check calls divergent")
	}
}
