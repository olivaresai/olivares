// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE SILENCE IS THE HARD HALF, and it is the one a careless fix loses.
//
// `finalize` used to throw its transition's error away entirely (`_, _ = m.transition`)
// — the only non-test caller in the module that did, while its own neighbour three lines
// up handles OwnerDied's error with a warnf. Handling it with a bare
// `if err != nil { warn }` would be the wrong repair: guardRuntimeLaunch refuses with
// conflictErr whenever a NEWER incarnation already took the row, which is legitimate and
// ordinary, and a warning that fires on the ordinary case gets silenced wholesale — with
// the real failure inside it.
func TestSettlingTheRuntimeRowStaysSILENTWhenANewerIncarnationWon(t *testing.T) {
	var zero model.ID
	guardRefusal := guardRuntimeLaunch(zero)(model.Record{})
	if guardRefusal == nil {
		t.Fatal("the guard did not refuse, so this test measures nothing")
	}
	if runtimeSettleWarrantsWarning(guardRefusal) {
		t.Fatalf("a supersession by a newer incarnation was reported as a failure: %v\n"+
			"  guardRuntimeLaunch refuses with this whenever the row moved on. Warning "+
			"here turns the ordinary case into noise.", guardRefusal)
	}

	// ⛔ AND THE PREDICATE THAT LOOKS RIGHT IS NOT: `conflictErr` returns a *runErr, which
	// neither wraps the store sentinel nor implements Is. Matching on store.ErrConflict
	// alone answers FALSE here — it would warn on exactly the case that must stay quiet.
	if errors.Is(guardRefusal, store.ErrConflict) {
		t.Fatal("this assertion is stale: the guard's refusal now IS a store.ErrConflict, " +
			"so the note above about why store.ErrConflict alone is not enough needs rewriting")
	}
	if !isRunConflict(guardRefusal) {
		t.Fatal("the guard's refusal is no longer a 409 runErr; the predicate needs revisiting")
	}
	// The store's own sentinel means the same thing and must be quiet too: after the
	// change to transition(), a CAS that survives its retry arrives as a 409, but a raw
	// one reaching here would be the same fact wearing the other type.
	if runtimeSettleWarrantsWarning(fmt.Errorf("settle: %w", store.ErrConflict)) {
		t.Fatal("a wrapped store conflict was reported as a failure")
	}
}

// AND IT MUST SPEAK WHEN THE SETTLE REALLY FAILED, which is the whole point of not
// throwing the error away.
func TestSettlingTheRuntimeRowSPEAKSWhenItReallyFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a plain failure", errors.New("the store was unavailable")},
		{"the row is gone", fmt.Errorf("settle: %w", store.ErrNotFound)},
		{"authority was refused", forbiddenErr("the session authority is no longer valid")},
	} {
		if !runtimeSettleWarrantsWarning(tc.err) {
			t.Errorf("%s was swallowed in silence: %v\n"+
				"  this is the class the old `_, _ = m.transition(...)` lost entirely — "+
				"the row stayed unsettled and nobody learned it.", tc.name, tc.err)
		}
	}
	if runtimeSettleWarrantsWarning(nil) {
		t.Error("a successful settle produced a warning")
	}
}
