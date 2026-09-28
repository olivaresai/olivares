// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// fencedMessageControl is a communication double whose commit consumes the
// same transaction-consumed proof the sessions effect does: its preflight
// observes the tenant's directory epoch, and its commit locks that exact fact
// version inside a store transaction before counting the effect. Only the
// store and core/auth decide whether the commit may land.
type fencedMessageControl struct {
	authr *auth.Authenticator
	st    store.Store

	mu        sync.Mutex
	calls     int
	effects   int
	receipts  map[string]WorkMessageResult
	committed []auth.CredentialBinding // the binding each committed effect acted under

	reauthOnCall int          // this call answers reauthentication without acting
	pauseOn      map[int]bool // these calls pause after their preflight
	pauseBefore  bool         // pause before resolving the binding instead
	preflighted  chan struct{}
	release      map[int]chan struct{} // one release per held call, so the order is the test's
	attempted    map[int]chan struct{} // closed when a held call's commit attempt has ended
}

func newFencedMessageControl() *fencedMessageControl {
	return &fencedMessageControl{
		receipts: map[string]WorkMessageResult{}, pauseOn: map[int]bool{},
		preflighted: make(chan struct{}),
		release:     map[int]chan struct{}{2: make(chan struct{}), 3: make(chan struct{})},
		attempted:   map[int]chan struct{}{2: make(chan struct{}), 3: make(chan struct{})},
	}
}

func (c *fencedMessageControl) SendWorkMessage(ctx context.Context, tenant model.TenantID, req WorkMessageRequest) (WorkMessageResult, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if done, held := c.attempted[call]; held && c.pauseOn[call] {
		defer close(done)
	}
	if call == c.reauthOnCall {
		return WorkMessageResult{}, fmt.Errorf("%w: gate the run", ErrWorkflowReauthenticationRequired)
	}
	pause := func() {
		c.preflighted <- struct{}{}
		<-c.release[call]
	}
	if c.pauseOn[call] && c.pauseBefore {
		pause()
	}
	if err := resolveBoundActor(ctx, c.authr, tenant, req.Actor); err != nil {
		return WorkMessageResult{}, err
	}
	var fact store.AuthorizationFactRef
	if err := c.st.View(ctx, tenant, func(sc store.Scope) error {
		epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		fact = store.AuthorizationFactRef{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: epoch.Version}
		return err
	}); err != nil {
		return WorkMessageResult{}, err
	}
	if c.pauseOn[call] && !c.pauseBefore {
		pause()
	}
	if err := c.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{fact})
	}); err != nil {
		return WorkMessageResult{}, fmt.Errorf("the effect's authority proof moved before its commit: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if result, ok := c.receipts[req.IdempotencyKey]; ok {
		return result, nil
	}
	c.effects++
	c.committed = append(c.committed, req.Actor.CredentialBinding)
	result := WorkMessageResult{
		WorkItemID: req.WorkItemID, MessageID: model.NewID(), CommandID: model.NewID(),
		EventID: model.NewID(), EventSeq: int64(40 + c.effects),
	}
	c.receipts[req.IdempotencyKey] = result
	return result, nil
}

func (c *fencedMessageControl) effectCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.effects
}

func (c *fencedMessageControl) committedUnder(binding auth.CredentialBinding) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.committed {
		if b == binding {
			return true
		}
	}
	return false
}

// F2 barrier oracle, the run: an old effect is held after its preflight (or
// before it) while the run is reauthorized, across the claim's expiry. The old
// effect either finishes before the supersession or is refused; a refusal or a
// reauthentication outcome from the superseded binding never fails or re-gates
// the reauthorized run; the successor completes the step with one effect.
func TestBindingSupersessionBarrier(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hold          bool // hold the old effect while the run is reauthorized
		pauseBefore   bool // hold it before it resolves its binding
		expireClaim   bool // age the old claim past the lease before the race
		wantOldEffect bool // the old effect commits (before the supersession)
	}{
		{name: "effect before supersession", wantOldEffect: true},
		{name: "supersession before the effect commits", hold: true},
		{name: "claim expiry then race", hold: true, expireClaim: true},
		{name: "stale reauthentication after publication", hold: true, pauseBefore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindingRunFixture(t)
			fenced := newFencedMessageControl()
			fenced.authr, fenced.st = f.authr, f.h.st
			fenced.reauthOnCall = 1
			f.mod.workflowMessage = fenced
			wfID, run, _ := f.start(f.member, f.messageStep("msg"), f.waitStep("wait"))
			f.requireGated(run, "setup")
			rec, _ := f.row(run)
			plan := rec.String(colWrPlanHash)
			if tc.hold {
				fenced.pauseOn[2], fenced.pauseBefore = true, tc.pauseBefore
			}
			if tc.expireClaim {
				// The successor is held too, so the old effect's commit is
				// attempted first: the window claim age cannot close.
				fenced.pauseOn[3] = true
			}

			// The first continuation runs the step: its effect is the old one.
			firstDone := make(chan resp, 1)
			go func() { firstDone <- f.reauthorize(f.member, wfID, run, plan) }()
			var oldBinding auth.CredentialBinding
			if tc.hold {
				<-fenced.preflighted
				oldBinding = f.handle(run)
				if tc.expireClaim {
					f.clock.advance(executingTimeout + time.Second)
				}
				// Another step's refusal gated the run meanwhile; the run is
				// reauthorized again while the old effect is still held.
				f.mutateRun(run, func(rec model.Record, _ []runStepState) { rec[colWrPaused] = pausedReauthRequired })
				rec, _ := f.row(run)
				secondDone := make(chan resp, 1)
				go func() { secondDone <- f.reauthorize(f.member, wfID, run, rec.String(colWrPlanHash)) }()
				if tc.expireClaim {
					<-fenced.preflighted            // the successor, re-claimed after the expiry
					fenced.release[2] <- struct{}{} // the old effect commits (or is refused) first
					<-fenced.attempted[2]
					fenced.release[3] <- struct{}{} // then the successor
				} else {
					if r := <-secondDone; r.code != http.StatusOK {
						t.Fatalf("reauthorize while the old effect is held = %d %s", r.code, r.raw)
					}
					fenced.release[2] <- struct{}{}
				}
				if tc.expireClaim {
					if r := <-secondDone; r.code != http.StatusOK {
						t.Fatalf("reauthorize while the old effect is held = %d %s", r.code, r.raw)
					}
				}
			}
			if r := <-firstDone; r.code != http.StatusOK {
				t.Fatalf("first continuation = %d %s", r.code, r.raw)
			}
			if !tc.hold {
				oldBinding = f.handle(run)
				f.mutateRun(run, func(rec model.Record, _ []runStepState) { rec[colWrPaused] = pausedReauthRequired })
				rec, _ := f.row(run)
				if r := f.reauthorize(f.member, wfID, run, rec.String(colWrPlanHash)); r.code != http.StatusOK {
					t.Fatalf("reauthorize after the effect = %d %s", r.code, r.raw)
				}
			}
			if got := fenced.committedUnder(oldBinding); got != tc.wantOldEffect {
				t.Fatalf("an effect committed under the superseded binding = %t, want %t", got, tc.wantOldEffect)
			}
			rec, _ = f.row(run)
			if rec.String(colWrPaused) != "" {
				t.Fatalf("a superseded binding's outcome re-gated the reauthorized run: paused %q", rec.String(colWrPaused))
			}
			if s := f.stepOf(run, "msg"); s.Status == stepStatusFailed || s.Status == stepStatusReauthRequired {
				t.Fatalf("the superseded binding's outcome was applied: %+v", s)
			}
			// The successor completes the step, recovering an uncertain claim
			// with its original key: exactly one effect in total.
			f.clock.advance(executingTimeout + time.Second)
			f.mod.AdvanceWorkflowRuns(context.Background(), f.h.moduleCtx(f.tenant))
			if s := f.stepOf(run, "msg"); s.Status != stepStatusMessageSent {
				t.Fatalf("step after the successor acted = %+v", s)
			}
			if got := fenced.effectCount(); got != 1 {
				t.Fatalf("effects = %d, want exactly one", got)
			}
		})
	}
}
