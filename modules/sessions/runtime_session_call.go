// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// A call belongs to one live launch and turn. Its owner closes done only after
// the same-call approval adapter has withdrawn any abandoned request.
type runtimeSessionCall struct {
	cancel context.CancelFunc
	done   chan struct{}
	turn   string
}

// BeginSessionCall binds a session tool call before it can request approval or
// dispatch. Lifecycle cancellation ends the call even if its HTTP client stays
// connected. Governance still owns the approval and exact-requester withdrawal.
func (m *Module) BeginSessionCall(ctx context.Context, principal auth.Principal) (context.Context, func(), error) {
	companion, err := m.RuntimeCompanion(ctx, principal.SessionScope(), principal)
	if err != nil {
		return nil, nil, err
	}
	lr, ok := m.rt.getLive(principal.SessionScope(), principal.SessionRunRef)
	if !ok {
		return nil, nil, auth.ErrUnauthenticated
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.launchID != companion.LaunchID || lr.finalized || lr.stopRequested || lr.launchFailed || lr.sessionCallsEnded || ctx.Err() != nil || companion.Context.Err() != nil {
		return nil, nil, auth.ErrUnauthenticated
	}
	turn := ""
	if lr.session != nil {
		turn = lr.session.ActiveTurn()
		if turn == "" {
			return nil, nil, auth.ErrUnauthenticated
		}
	}
	if lr.session == nil && lr.claudeTurnDone == nil {
		lr.claudeTurnDone = make(chan struct{})
		lr.claudePendingTurns = 1 // An initial provider turn observed through its call.
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(companion.Context, cancel)
	call := &runtimeSessionCall{cancel: cancel, done: make(chan struct{}), turn: turn}
	lr.sessionCalls = append(lr.sessionCalls, call)
	var once sync.Once
	return callCtx, func() {
		once.Do(func() {
			cancel()
			stop()
			lr.mu.Lock()
			lr.sessionCalls = slices.DeleteFunc(lr.sessionCalls, func(c *runtimeSessionCall) bool { return c == call })
			close(call.done)
			lr.mu.Unlock()
		})
	}, nil
}

// Claude's interrupt acknowledgement can precede its old result. Do not write
// a successor input into that gap: otherwise the late result could cancel the
// successor's calls. This waits on the owned output pump, not a second queue.
func (lr *liveRun) beginSessionTurn(ctx context.Context) error {
	lr.mu.Lock()
	previous, ended := lr.claudeTurnDone, lr.sessionCallsEnded
	lr.mu.Unlock()
	if ended && previous != nil {
		waitCtx, cancel := context.WithTimeout(ctx, claudeInterruptWait)
		defer cancel()
		select {
		case <-previous:
		case <-waitCtx.Done():
			return &runErr{http.StatusGatewayTimeout, "the previous session turn has not finished; input was not sent"}
		}
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if lr.finalized || lr.stopRequested || lr.launchFailed {
		return conflictErr("the session has ended; input was not sent")
	}
	lr.sessionCallsEnded = false
	if lr.session == nil {
		lr.claudePendingTurns++
		if lr.claudeTurnDone == nil {
			lr.claudeTurnDone = make(chan struct{})
		}
	}
	return nil
}

// With no provider turn id (Claude, interrupt or stop), close admission until
// the next authorized user input. Driver completions cancel only their own id,
// so a concurrently starting successor turn cannot lose its calls.
func (m *Module) endSessionCalls(lr *liveRun, turn string) error {
	return m.cancelSessionCalls(lr, turn, true)
}

func (m *Module) cancelSessionCalls(lr *liveRun, turn string, closeAdmission bool) error {
	lr.mu.Lock()
	if closeAdmission {
		lr.sessionCallsEnded = true
	}
	calls := slices.Clone(lr.sessionCalls)
	lr.mu.Unlock()
	return cancelCalls(calls, turn)
}

// cancelCalls cancels the calls of turn ("" for all of them) and waits for their
// owners to close them.
func cancelCalls(calls []*runtimeSessionCall, turn string) error {
	waiting := calls[:0]
	for _, call := range calls {
		if turn == "" || call.turn == turn {
			call.cancel()
			waiting = append(waiting, call)
		}
	}
	if len(waiting) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, call := range waiting {
		select {
		case <-call.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *Module) finishSessionCalls(lr *liveRun, turn string) {
	// Close raw-Claude admission before the snapshot. A result cannot leave a
	// window for an unowned call; already authorized queued inputs reopen it below.
	if err := m.cancelSessionCalls(lr, turn, lr.session == nil); err != nil {
		m.warnf("session tool call cancellation incomplete", "run_ref", lr.runRef)
	}
	if lr.session == nil {
		lr.mu.Lock()
		if lr.claudeTurnDone != nil {
			close(lr.claudeTurnDone)
			lr.claudeTurnDone = nil
		}
		if lr.claudePendingTurns > 0 {
			lr.claudePendingTurns--
		}
		if lr.claudePendingTurns > 0 && !lr.stopRequested && !lr.finalized && !lr.launchFailed {
			lr.claudeTurnDone = make(chan struct{})
			lr.sessionCallsEnded = false
		}
		lr.mu.Unlock()
	}
}

// A reserved input is not a confirmed successor if authority was lost or the
// write failed. An ambiguous write never grants MCP admission to a phantom turn.
func (m *Module) withdrawSessionInput(lr *liveRun, attempted bool) {
	lr.mu.Lock()
	if lr.claudePendingTurns > 0 {
		lr.claudePendingTurns--
	}
	lr.mu.Unlock()
	if err := m.endSessionCalls(lr, ""); err != nil {
		m.warnf("session tool call cancellation incomplete", "run_ref", lr.runRef)
	}
	if !attempted {
		lr.mu.Lock()
		if lr.claudePendingTurns == 0 && lr.claudeTurnDone != nil {
			close(lr.claudeTurnDone)
			lr.claudeTurnDone = nil
		}
		lr.mu.Unlock()
	}
}
