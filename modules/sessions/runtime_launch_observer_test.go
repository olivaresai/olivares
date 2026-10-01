// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestRuntimeLaunchObserverReceivesOriginalCreateAndResume(t *testing.T) {
	var observed []RuntimeLaunchCompletion
	runner := &fakeRunner{initSID: "observer-provider-session"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()),
		WithRuntimeLaunchObserver(func(_ context.Context, completion RuntimeLaunchCompletion) error {
			observed = append(observed, completion)
			return nil
		}))
	ctx := context.Background()
	first, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0] != first.Completion {
		t.Fatalf("create observations = %d; original completion was not delivered exactly once", len(observed))
	}
	original, ok := observed[0].Identity()
	if !ok || original.Tenant != tenant || original.RunRef != first.RunRef {
		t.Fatal("observer did not receive the committed original attempt")
	}
	waitFor(t, "provider identity", func() bool { run, _ := m.getRun(ctx, tenant, first.RunRef); return run.ClaudeSessionID != "" })
	if _, err := m.stopRun(ctx, tenant, first.RunRef, "user:operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	second, err := m.resumeRun(ctx, tenant, first.RunRef, "user:operator", model.ActorUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 || observed[1] != second.Completion {
		t.Fatalf("resume observations = %d; new completion was not delivered exactly once", len(observed))
	}
	resumed, ok := observed[1].Identity()
	if !ok || resumed.RuntimeLaunchID == original.RuntimeLaunchID || resumed.RunRef != original.RunRef {
		t.Fatal("resume observation reused the previous attempt")
	}
	if _, err := m.getRun(ctx, tenant, first.RunRef); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 {
		t.Fatal("a read emitted a launch observation")
	}
}

func TestRuntimeLaunchObserverWorkReplayEmitsNothing(t *testing.T) {
	var observed []RuntimeLaunchCompletion
	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
		WithRuntimeLaunchObserver(func(_ context.Context, completion RuntimeLaunchCompletion) error {
			observed = append(observed, completion)
			return nil
		}))
	item, _, agent := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(item, agent)
	first, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0] != first.Completion || !replayed.Replayed {
		t.Fatalf("fresh and replay observations = %d; replayed = %v", len(observed), replayed.Replayed)
	}
	if _, ok := replayed.Completion.Identity(); ok {
		t.Fatal("dispatch replay reconstructed an original completion")
	}
	finishWorkRuntimeRun(t, m, tenant, first.RunRef, runner.lastProc())
}

func TestRuntimeLaunchObserverFailurePreservesCommittedResult(t *testing.T) {
	for _, panicCallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicCallback], func(t *testing.T) {
			runner := &fakeRunner{}
			calls := 0
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()),
				WithRuntimeLaunchObserver(func(context.Context, RuntimeLaunchCompletion) error {
					calls++
					if panicCallback {
						panic("private observer panic value")
					}
					return errors.New("private-observer-error-value")
				}))
			logs := &runtimeObserverLogs{}
			m.log = slog.New(logs)
			result, err := m.createRun(context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
			if err != nil || result.State != stateRunning || calls != 1 {
				t.Fatalf("observer changed launch: state=%q calls=%d error=%v", result.State, calls, err)
			}
			if _, ok := result.Completion.Identity(); !ok {
				t.Fatal("observer failure erased the original completion")
			}
			logs.mu.Lock()
			defer logs.mu.Unlock()
			found := false
			for _, record := range logs.records {
				if record.Message == "session runtime launch observer failed" && record.Level == slog.LevelError {
					found = true
				}
				record.Attrs(func(attr slog.Attr) bool {
					if strings.Contains(attr.Value.String(), "private observer panic value") {
						t.Error("observer panic value reached the log")
					}
					return true
				})
			}
			if !found {
				t.Error("observer failure was not reported")
			}
		})
	}
}

func TestRuntimeLaunchObserverRefusedLaunchEmitsNothing(t *testing.T) {
	calls := 0
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{launchErr: errors.New("runner refused")}), WithCredentialSource(staticCred()),
		WithRuntimeLaunchObserver(func(context.Context, RuntimeLaunchCompletion) error { calls++; return nil }))
	if _, err := m.createRun(context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser}); err == nil || calls != 0 {
		t.Fatalf("refused launch observations = %d, error = %v", calls, err)
	}
}

func TestRuntimeLaunchObserverRegistrationOwnsOnlyItsSubscription(t *testing.T) {
	m := New()
	observer := func(context.Context, RuntimeLaunchCompletion) error { return nil }
	unregister, err := m.UseRuntimeLaunchObserver(observer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UseRuntimeLaunchObserver(observer); !errors.Is(err, ErrRuntimeLaunchObserverRegistered) {
		t.Fatalf("second subscriber = %v", err)
	}
	ignore, err := m.UseRuntimeLaunchObserver(nil)
	if err != nil {
		t.Fatal(err)
	}
	ignore()
	if _, err := m.UseRuntimeLaunchObserver(observer); !errors.Is(err, ErrRuntimeLaunchObserverRegistered) {
		t.Fatal("nil registration erased the current observer")
	}
	unregister()
	second, err := m.UseRuntimeLaunchObserver(observer)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	unregister()
	if _, err := m.UseRuntimeLaunchObserver(observer); !errors.Is(err, ErrRuntimeLaunchObserverRegistered) {
		t.Fatal("stale unregister erased the replacement observer")
	}
}

func TestRuntimeLaunchObserverCanUnregisterInsideItsCallback(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()), WithRuntimeLaunchObserver(nil))
	calls := 0
	var unregister func()
	var err error
	unregister, err = m.UseRuntimeLaunchObserver(func(context.Context, RuntimeLaunchCompletion) error {
		calls++
		unregister()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.createRun(context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unregistered observer received %d launches", calls)
	}
}

type runtimeObserverLogs struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*runtimeObserverLogs) Enabled(context.Context, slog.Level) bool { return true }
func (h *runtimeObserverLogs) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}
func (h *runtimeObserverLogs) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *runtimeObserverLogs) WithGroup(string) slog.Handler      { return h }
