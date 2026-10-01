// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// RuntimeLaunchObserver receives the original successful create/resume completion
// after the native launch transition commits, including fresh work launches.
// Reads and dispatch replays emit nothing. The completion is historical identity,
// never permission, current authority or a replacement for the runtime's guards.
//
// Delivery is synchronous and occurs before the launch returns, while a resume
// still holds its run operation lock. An observer must perform bounded in-memory
// custody only: no I/O, waiting or runtime lifecycle calls. It may copy and retain
// the opaque completion. Errors and panics are logged and cannot change the
// committed launch result. There is no retry, queue or durable replay.
type RuntimeLaunchObserver func(context.Context, RuntimeLaunchCompletion) error

var (
	ErrRuntimeLaunchObserverRegistered  = errors.New("runtime launch observer already registered")
	ErrRuntimeLaunchObserverUnavailable = errors.New("runtime launch observer registration unavailable")
)

type runtimeLaunchSubscription struct {
	observer RuntimeLaunchObserver
}

type runtimeLaunchObserverState struct {
	mu           sync.Mutex
	subscription *runtimeLaunchSubscription
}

// UseRuntimeLaunchObserver registers one optional observer. A nil observer is a
// no-op and cannot erase a registration. A duplicate registration is refused.
// Unregister is idempotent and removes only this registration; an already copied
// callback may finish after it returns. The callback runs outside this registry's
// mutex, so it may unregister itself without deadlocking.
func (m *Module) UseRuntimeLaunchObserver(observer RuntimeLaunchObserver) (unregister func(), err error) {
	if observer == nil {
		return func() {}, nil
	}
	if m == nil || m.rt == nil {
		return nil, ErrRuntimeLaunchObserverUnavailable
	}
	state := &m.rt.launchObserver
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.subscription != nil {
		return nil, ErrRuntimeLaunchObserverRegistered
	}
	subscription := &runtimeLaunchSubscription{observer: observer}
	state.subscription = subscription
	return func() {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.subscription == subscription {
			state.subscription = nil
		}
	}, nil
}

// WithRuntimeLaunchObserver installs the same observer at construction. Nil
// preserves the default with no observer; duplicate options fail at construction.
func WithRuntimeLaunchObserver(observer RuntimeLaunchObserver) Option {
	return func(m *Module) {
		if _, err := m.UseRuntimeLaunchObserver(observer); err != nil {
			panic(err)
		}
	}
}

func (m *Module) observeRuntimeLaunch(ctx context.Context, completion RuntimeLaunchCompletion) {
	state := &m.rt.launchObserver
	state.mu.Lock()
	subscription := state.subscription
	state.mu.Unlock()
	if subscription == nil {
		return
	}
	if _, ok := completion.Identity(); !ok {
		m.logRuntimeLaunchObserverFailure(ctx, completion, errors.New("original launch completion unavailable"))
		return
	}
	defer func() {
		if recover() != nil {
			// The panic value can contain private data; report only the condition.
			m.logRuntimeLaunchObserverFailure(ctx, completion, errors.New("runtime launch observer panicked"))
		}
	}()
	if err := subscription.observer(ctx, completion); err != nil {
		m.logRuntimeLaunchObserverFailure(ctx, completion, err)
	}
}

func (m *Module) logRuntimeLaunchObserverFailure(ctx context.Context, completion RuntimeLaunchCompletion, err error) {
	log := m.log
	if log == nil {
		log = slog.Default()
	}
	identity, _ := completion.Identity()
	log.ErrorContext(ctx, "session runtime launch observer failed", "failure", "observer_callback_failed",
		"run_ref", identity.RunRef, "runtime_launch_id", identity.RuntimeLaunchID.String())
}
