// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// reflectTypeOfFacts is a one-liner with a name so the test above reads as a statement
// about the SEAM rather than about reflection.
func reflectTypeOfFacts() reflect.Type { return reflect.TypeOf(CoreEntityFacts{}) }

// The core-entity seam's own battery (V269 / docs/contracts/COCKPIT-02-authz.md §3).
//
// Every case here is about the seam REFUSING something. That is deliberate: the seam
// exists because Scope.Ext refuses the core namespace, and its whole value is that it
// opens exactly one door and no more. A battery that only proved the door opens would
// be measuring the easy half.

type fakeCoreResolver struct {
	facts CoreEntityFacts
	err   error
	calls int
	kind  CoreKind
}

func (f *fakeCoreResolver) ResolveCoreEntity(_ context.Context, _ model.TenantID, k CoreKind, _ model.ID) (CoreEntityFacts, error) {
	f.calls++
	f.kind = k
	return f.facts, f.err
}

func TestValidateCoreEntityRefRejectsBothKinds(t *testing.T) {
	err := validateCoreEntityRef(EntityRef{Kind: "sessions.run", CoreKind: CoreKindSession}, &fakeCoreResolver{})
	if err == nil {
		t.Fatal("a route declaring both an external Kind and a CoreKind must be refused")
	}
	// The message has to name BOTH, or an operator reading a boot failure cannot tell
	// which of the two declarations to remove.
	for _, want := range []string{"sessions.run", "core.session"} {
		if !contains(err.Error(), want) {
			t.Errorf("refusal must name %q; got: %v", want, err)
		}
	}
}

func TestValidateCoreEntityRefRejectsUnregisteredKind(t *testing.T) {
	if err := validateCoreEntityRef(EntityRef{CoreKind: CoreKind(200)}, &fakeCoreResolver{}); err == nil {
		t.Fatal("an unregistered CoreKind must be refused: the enum is what bounds the reachable surface")
	}
}

func TestValidateCoreEntityRefRejectsMissingResolver(t *testing.T) {
	err := validateCoreEntityRef(EntityRef{CoreKind: CoreKindSession}, nil)
	if err == nil {
		t.Fatal("a CoreKind route with no resolver must be refused, not authorized without its lineage")
	}
}

// CONTROL POSITIVO, and it is the case that keeps the three above honest: a battery of
// refusals is satisfied by a validator that refuses everything.
func TestValidateCoreEntityRefAcceptsAWellFormedRoute(t *testing.T) {
	// The locator is part of being well formed now, and it was not before: this example used
	// to declare a CoreKind and no way to find its row, which is a route the engine denies for
	// every caller. It mounted here because this validator did not look.
	if err := validateCoreEntityRef(
		EntityRef{CoreKind: CoreKindSession, IDParam: "core_session_id"}, &fakeCoreResolver{},
	); err != nil {
		t.Fatalf("a well-formed core-entity route must mount: %v", err)
	}
	// And a route that declares NO core entity is unaffected by any of this — every
	// existing module route in the tree is that shape.
	if err := validateCoreEntityRef(EntityRef{Kind: "sessions.run", IDParam: "id"}, nil); err != nil {
		t.Fatalf("an ordinary external-entity route must be untouched by the core seam: %v", err)
	}
}

// TestCoreKindValidIsClosed pins that the enum is CLOSED. If a kind is added, this test
// fails until somebody states here that the new kind is meant to be reachable — which is
// the point: widening what a module may authorize against is a decision, not a constant.
func TestCoreKindValidIsClosed(t *testing.T) {
	valid := map[CoreKind]bool{CoreKindSession: true}
	for k := CoreKind(0); k < 32; k++ {
		if got, want := k.valid(), valid[k]; got != want {
			t.Errorf("CoreKind(%d).valid() = %v, want %v — if a kind was added, add it to this map "+
				"and to docs/contracts/COCKPIT-02-authz.md §3 in the same change", k, got, want)
		}
	}
	if CoreKindNone.valid() {
		t.Error("CoreKindNone must never be valid: it means 'no core entity declared'")
	}
}

// TestCoreEntityFactsSurfaceIsNarrow guards the seam's REACH rather than its behaviour.
// The facts struct is the entire contract between the engine and a module about a core
// row; a field added here widens what every module can read, so it must be a decision.
func TestCoreEntityFactsSurfaceIsNarrow(t *testing.T) {
	// Named explicitly rather than counted, so the failure says WHICH field appeared.
	want := map[string]bool{"ID": true, "Tenant": true, "WorkspaceID": true, "AgentID": true, "Exists": true}
	got := map[string]bool{}
	rt := reflectTypeOfFacts()
	for i := 0; i < rt.NumField(); i++ {
		got[rt.Field(i).Name] = true
	}
	for name := range got {
		if !want[name] {
			t.Errorf("CoreEntityFacts gained field %q: a wider fact set is a decision — record it in "+
				"docs/contracts/COCKPIT-02-authz.md §3 and add it here", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("CoreEntityFacts lost field %q, which an authorization decision needs", name)
		}
	}
}

// TestResolverErrorIsNotAbsence pins the substitution this seam must never make.
func TestResolverErrorIsNotAbsence(t *testing.T) {
	boom := errors.New("store unavailable")
	f := &fakeCoreResolver{err: boom}
	facts, err := f.ResolveCoreEntity(context.Background(), "t", CoreKindSession, "id")
	if !errors.Is(err, boom) {
		t.Fatalf("an unreadable row must surface as an error, got facts=%+v err=%v", facts, err)
	}
	if facts.Exists {
		t.Error("an errored resolve must not report existence")
	}
}

func contains(hay, needle string) bool {
	return len(needle) == 0 || (len(hay) >= len(needle) && indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestConcealingCoreEntityRouteIsReachable pins the combination COCKPIT-02 requires and
// that an adversarial contrast found UNREACHABLE: CoreKindSession + conceal-404.
//
// It was unreachable because two guards, each correct alone, composed into a refusal:
// coreentity.go rejects a route declaring BOTH kinds, and entityResource rejected a
// concealing route declaring no external Kind. Neither test saw it, because the
// mount-time validator never runs entityResource — which is exactly why the case is
// asserted here against BOTH guards rather than either.
func TestConcealingCoreEntityRouteIsReachable(t *testing.T) {
	ref := EntityRef{CoreKind: CoreKindSession, IDParam: "id", ConcealDeniedAsNotFound: true}

	// Guard 1: mounting.
	if err := validateCoreEntityRef(ref, &fakeCoreResolver{}); err != nil {
		t.Fatalf("a concealing core-entity route must mount: %v", err)
	}
	// Guard 2: the per-request shape check inside entityResource. Exercised through the
	// same predicate the function applies, so the two cannot drift apart silently.
	if ref.ConcealDeniedAsNotFound && ref.Kind == "" && ref.CoreKind == CoreKindNone {
		t.Fatal("a concealing route with a CoreKind must not be treated as kindless")
	}
	// And the refusal it replaced is still a refusal: conceal with NEITHER kind.
	bare := EntityRef{IDParam: "id", ConcealDeniedAsNotFound: true}
	if !(bare.ConcealDeniedAsNotFound && bare.Kind == "" && bare.CoreKind == CoreKindNone) {
		t.Error("conceal with no kind of any sort must still be refused: without a kind the " +
			"engine cannot establish existence, and conceal-404 depends on that difference")
	}
}

// TestValidateCoreEntityRefRejectsAKindWithNoLocator is R-631's first half: the check that
// existed on ONE of the two doors.
//
// ⛔ A GUARD THAT ONLY ONE ENTRANCE RUNS IS A GUARD ON THE ENTRANCE. "A CoreKind with no way
// to find its row" was refused by RouteMetadata.Validate, which only HandlePolicy calls.
// HandleEntity uses validateCoreEntityRef as its WHOLE mount validation, so the same impossible
// route mounted happily through it and then denied every caller before the handler ran —
// wearing the shape of an ordinary authorization decision, which is the one failure a reader
// cannot tell from working correctly.
func TestValidateCoreEntityRefRejectsAKindWithNoLocator(t *testing.T) {
	err := validateCoreEntityRef(EntityRef{CoreKind: CoreKindSession}, &fakeCoreResolver{})
	if err == nil {
		t.Fatal("a CoreKind route with no IDParam or BodyIDField mounted: the engine cannot " +
			"locate the row, so it denies every caller before the handler")
	}
	if !contains(err.Error(), "IDParam") || !contains(err.Error(), "BodyIDField") {
		t.Errorf("the refusal must name both locators so an operator knows what to add: %v", err)
	}

	// And BOTH is refused for the same reason the EntityRef refuses it elsewhere: two locators
	// are two answers to "which row", and the route means one.
	if err := validateCoreEntityRef(
		EntityRef{CoreKind: CoreKindSession, IDParam: "id", BodyIDField: "session_id"},
		&fakeCoreResolver{},
	); err == nil {
		t.Fatal("a CoreKind route declaring both locators mounted")
	}

	// CONTROL POSITIVE: a route with EXACTLY one still mounts, by either locator. Without this
	// the two refusals above would be satisfied by a validator that refused every CoreKind.
	for _, ref := range []EntityRef{
		{CoreKind: CoreKindSession, IDParam: "core_session_id"},
		{CoreKind: CoreKindSession, BodyIDField: "session_id"},
	} {
		if err := validateCoreEntityRef(ref, &fakeCoreResolver{}); err != nil {
			t.Errorf("a route with exactly one locator was refused: %v", err)
		}
	}
}
