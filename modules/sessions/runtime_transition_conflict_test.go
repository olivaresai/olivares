// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A LOST CAS IS A 409, NOT A RAW STORE SENTINEL — and the difference decided whether a
// launched process survived its own bookkeeping.
//
// `transition` already reports "another actor got here first" as a 409 when the state
// machine sees it (`illegal transition <event> from state <s>`). But when the SAME event
// arrived as a lost optimistic-concurrency CAS, it left as `store.ErrConflict` in the raw:
// a different TYPE for the same fact. `isRunConflict` matches only `*runErr`, and its two
// callers -- createRun and resumeRun -- are the identical compensation, "raced finalize:
// report the real state". With the raw sentinel in hand neither fired: the just-launched
// process was torn down and the caller got a bare "version conflict" instead of its row.
//
// The witness is a CI red of 2026-08-24 (`control-plane`, job 97305805094):
//
//	winner = {RunRef: Name: Transport: ...} / version conflict
//
// an EMPTY runDTO with exactly that bare text -- which is what resumeRun returns down that
// path, and nothing else in it produces that pairing.
//
// The conflict here is FORCED, not awaited: the mutate stamps a stale version on every
// attempt, so `repo.Update` loses its CAS on the first try and again on the one retry
// `transition` makes. No sleeps, no goroutines, nothing that can pass by luck.
func TestATransitionThatLosesItsCASReportsA409AndNotTheStoreSentinel(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	ctx := context.Background()
	created, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:cas", ActorKind: model.ActorAgent, AgentRef: "agent:cas",
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	_, err = m.transition(ctx, tenant, created.RunRef, transitionInput{
		event: "stopping", actor: "test", actorKind: "user",
		mutate: func(rec model.Record) {
			attempts++
			// A version nobody can be holding: the CAS cannot match it.
			rec[model.ColVersion] = int64(-1)
		},
	})

	// The control that keeps this test honest: if the CAS never actually lost, there is
	// no conflict to classify and a green below would mean nothing.
	if attempts < 2 {
		t.Fatalf("the stale version was stamped %d time(s); transition retries once on a "+
			"lost CAS, so fewer than 2 means the conflict never happened and this test "+
			"proved nothing", attempts)
	}
	if err == nil {
		t.Fatal("a transition that stamps an impossible version SUCCEEDED; the store is not " +
			"enforcing its optimistic-concurrency column and this test cannot measure anything")
	}

	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("a lost CAS surfaced as %T (%v), not a 409 runErr.\n"+
			"  isRunConflict() matches only *runErr, so createRun's and resumeRun's "+
			"\"raced finalize: report the real state\" compensation does not fire, the "+
			"just-launched process is torn down, and the caller gets this text bare.", err, err)
	}
	if !isRunConflict(err) {
		t.Fatalf("isRunConflict rejected the error the transition returned: %v", err)
	}
	// And it must NOT claim the transition was illegal -- it was legal and lost a race,
	// which is a different fact and is reported as a different one.
	if got := re.Error(); got == "" || strings.Contains(got, "illegal transition") {
		t.Fatalf("a lost CAS was reported as an illegal transition: %q", got)
	}
	if !strings.Contains(re.Error(), "stopping") {
		t.Fatalf("the 409 does not name the event that lost: %q", re.Error())
	}
	// The sentinel is still reachable underneath for anyone matching on it.
	_ = store.ErrConflict
}
