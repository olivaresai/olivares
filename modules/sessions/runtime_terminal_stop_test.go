// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRuntime_TerminalStopReportsUnfinishedProcess(t *testing.T) {
	for _, state := range []string{stateStopped, stateFailed, stateCleaned, stateDeclined, stateExpired} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			m, st, tenant, clk := newRuntimeHarness(t, WithRunner(NewProcRunner()),
				WithProgram(writeFakeClaude(t)), WithCredentialSource(staticCred()),
				WithStopWaitDelay(time.Second))
			dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser,
			})
			if err != nil {
				t.Fatal(err)
			}
			lr, ok := m.rt.getLive(tenant, dto.RunRef)
			if !ok {
				t.Fatal("created run has no supervised process")
			}
			proc := lr.proc.(*procProcess)
			waitFor(t, "native child session id", func() bool {
				run, err := m.getRun(ctx, tenant, dto.RunRef)
				return err == nil && run.ClaudeSessionID == "sess-real-1"
			})

			// A second runtime has the same SQLite store but no process handle.
			// Its orphan reconciliation retires the launch while the first runtime
			// still owns a real OS child, as an offline boot could do.
			offline := New(WithClock(clk))
			offline.UseData(api.NewModuleData(st))
			bindStoreStanding(offline, st)
			stopped, err := offline.stopRun(ctx, tenant, dto.RunRef, model.ActorSystem, model.ActorSystem)
			if err != nil || stopped.State != stateStopped {
				t.Fatalf("orphan reconciliation: state=%s err=%v", stopped.State, err)
			}
			if state != stateStopped {
				if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(runKind)
					if err != nil {
						return err
					}
					rec, err := findRunRec(ctx, repo, dto.RunRef)
					if err != nil {
						return err
					}
					rec[colState] = state
					_, err = repo.Update(ctx, rec)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-proc.waitDone:
				t.Fatal("child exited before the stop probe")
			default:
			}
			if err := syscall.Kill(proc.PID(), 0); err != nil {
				t.Fatalf("native child is not alive: %v", err)
			}
			before, err := m.loadRun(ctx, tenant, dto.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			if before.String(colRuntimeLaunchID) != "" {
				t.Fatal("orphan reconciliation did not retire the launch generation")
			}

			_, err = m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
			var refusal *runErr
			if !errors.As(err, &refusal) || refusal.status != http.StatusConflict ||
				!strings.Contains(err.Error(), "supervised process has not finished") {
				t.Fatalf("stop on a terminal row with a live native child must report it, got %v", err)
			}
			after, err := m.loadRun(ctx, tenant, dto.RunRef)
			if err != nil || after.String(colState) != state || after.Int(colLastEventSeq) != before.Int(colLastEventSeq) {
				t.Fatalf("refused stop changed terminal state or lifecycle events: %v", err)
			}

			// After the real child exits, its retained attach tail must not turn
			// an idempotent stop into a conflict or append another lifecycle event.
			if err := proc.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-lr.finalizedCh:
			case <-time.After(3 * time.Second):
				t.Fatal("native child did not finalize")
			}
			if retained, ok := m.rt.getLive(tenant, dto.RunRef); !ok || retained != lr || len(lr.ring.readFrom(0).frames) == 0 {
				t.Fatal("finalized child lost its retained attach tail")
			}
			for _, dropHandle := range []bool{false, true} {
				if dropHandle {
					m.rt.dropLive(tenant, dto.RunRef)
				}
				got, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
				if err != nil || got.State != state || got.LastEventSeq != before.Int(colLastEventSeq) {
					t.Fatalf("idempotent stop (dropHandle=%v): state=%s err=%v", dropHandle, got.State, err)
				}
			}
		})
	}
}

func TestRuntime_TerminalStopReportsUnverifiedCollection(t *testing.T) {
	runner := &waitErrRunner{err: ErrChildNotReaped}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	ref, _ := launchedRun(t, m, tenant)
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("created run has no supervised handle")
	}
	runner.last().finish()
	select {
	case <-lr.finalizedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("unverified wait did not finalize")
	}
	_, err := m.stopRun(context.Background(), tenant, ref, "user:u1", model.ActorUser)
	var refusal *runErr
	if !errors.As(err, &refusal) || refusal.status != http.StatusConflict {
		t.Fatalf("stop must report unconfirmed collection even after finalization, got %v", err)
	}
}

func TestRuntime_TerminalStopAfterRetention(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reaped bool
	}{
		{name: "unverified", err: ErrChildNotReaped},
		{name: "unknown-wait-error", err: errors.New("wait failed")},
		{name: "reaped", reaped: true},
		{name: "reaped-output-abandoned", err: ErrOutputAbandoned, reaped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &waitErrRunner{err: tc.err}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
			ref, _ := launchedRun(t, m, tenant)
			lr, ok := m.rt.getLive(tenant, ref)
			if !ok {
				t.Fatal("created run has no supervised handle")
			}
			runner.last().finish()
			select {
			case <-lr.finalizedCh:
			case <-time.After(3 * time.Second):
				t.Fatal("wait did not finalize")
			}
			before, err := m.getRun(context.Background(), tenant, ref)
			if err != nil {
				t.Fatal(err)
			}

			// Exercise the actual retention timer, with virtual time confined to
			// the reaper so SQLite and runtime goroutines keep their own clocks.
			synctest.Test(t, func(t *testing.T) {
				go m.reapClosed(lr)
				synctest.Wait()
				time.Sleep(closedRetention - time.Nanosecond)
				synctest.Wait()
				if retained, ok := m.rt.getLive(tenant, ref); !ok || retained != lr {
					t.Fatal("handle was discarded before retention expired")
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
			})

			_, retained := m.rt.getLive(tenant, ref)
			if retained == tc.reaped {
				t.Errorf("handle retained after expiry=%v, collection confirmed=%v", retained, tc.reaped)
			}
			got, err := m.stopRun(context.Background(), tenant, ref, "user:u1", model.ActorUser)
			if tc.reaped {
				if err != nil || got.State != before.State || got.LastEventSeq != before.LastEventSeq {
					t.Fatalf("idempotent stop after reaped-handle expiry: state=%s err=%v", got.State, err)
				}
			} else {
				var refusal *runErr
				if !errors.As(err, &refusal) || refusal.status != http.StatusConflict {
					t.Fatalf("stop after retention must report unconfirmed collection, got %v", err)
				}
			}
			after, err := m.getRun(context.Background(), tenant, ref)
			if err != nil || after.State != before.State || after.LastEventSeq != before.LastEventSeq {
				t.Fatalf("stop after retention changed terminal state or lifecycle events: %v", err)
			}
		})
	}
}

func TestRuntime_TerminalStopAfterCleanupOrResume(t *testing.T) {
	for _, operation := range []string{"cleanup", "resume"} {
		for _, tc := range []struct {
			name    string
			native  bool
			failed  bool
			waitErr error
			reaped  bool
		}{
			{name: "native-stopped", native: true},
			{name: "native-failed", native: true, failed: true},
			{name: "unverified-wait", waitErr: ErrChildNotReaped},
			{name: "unknown-wait-error", waitErr: errors.New("wait failed")},
			{name: "confirmed-exit", reaped: true},
			{name: "confirmed-output-abandoned", waitErr: ErrOutputAbandoned, reaped: true},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				runner := &waitErrRunner{err: tc.waitErr}
				gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
				var processRunner Runner = runner
				if tc.native {
					processRunner = NewProcRunner()
				}
				m, st, tenant, clk := newRuntimeHarness(t, WithRunner(processRunner),
					WithProgram(writeFakeClaude(t)), WithCredentialSource(staticCred()),
					WithStopWaitDelay(time.Second), WithLaunchGate(gate))
				created, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
					Transport: TransportStreamJSON, Isolation: IsolationNative,
					Actor: "user:u1", ActorKind: model.ActorUser,
				})
				if err != nil {
					t.Fatal(err)
				}
				ref := created.RunRef
				lr, ok := m.rt.getLive(tenant, ref)
				if !ok {
					t.Fatal("created run has no supervised handle")
				}
				if tc.native {
					// Keep test cleanup independent of the registry that the bug loses.
					t.Cleanup(func() { _ = lr.proc.Stop(context.Background()) })
					waitFor(t, "native child session id", func() bool {
						run, err := m.getRun(ctx, tenant, ref)
						return err == nil && run.ClaudeSessionID == "sess-real-1"
					})
					offline := New(WithClock(clk))
					offline.UseData(api.NewModuleData(st))
					bindStoreStanding(offline, st)
					if _, err := offline.stopRun(ctx, tenant, ref, model.ActorSystem, model.ActorSystem); err != nil {
						t.Fatal(err)
					}
					if tc.failed {
						mutateRunForCredentialTest(t, m.Data, tenant, ref, func(rec model.Record) { rec[colState] = stateFailed })
					}
				} else {
					runner.last().finish()
					select {
					case <-lr.finalizedCh:
					case <-time.After(3 * time.Second):
						t.Fatal("wait did not finalize")
					}
				}
				before, err := m.loadRun(ctx, tenant, ref)
				if err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(before.String(colRunWorkspacePath), "keep.txt")
				if err := os.WriteFile(marker, []byte("session work"), 0o600); err != nil {
					t.Fatal(err)
				}

				gate.called = false

				// Use the real lifecycle entry points: a directly edited cleaned
				// row cannot expose cleanup dropping custody or resume replacing it.
				var got runDTO
				if operation == "cleanup" {
					got, err = m.cleanupRunWith(ctx, tenant, ref, "user:u1", model.ActorUser, cleanupOptions{DiscardWorktree: true})
				} else {
					got, err = m.resumeRun(ctx, tenant, ref, "user:u1", model.ActorUser, "")
				}
				if tc.reaped {
					if err != nil {
						t.Fatalf("%s after confirmed collection: %v", operation, err)
					}
					if operation == "cleanup" {
						if got.State != stateCleaned {
							t.Errorf("cleanup state=%s", got.State)
						}
						if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
							t.Errorf("confirmed cleanup did not remove its directory: %v", err)
						}
					} else {
						if current, ok := m.rt.getLive(tenant, ref); !ok || current == lr {
							t.Error("confirmed resume did not install a successor")
						}
						// The successor also exits with confirmed collection.
						runner.last().finish()
						current, _ := m.rt.getLive(tenant, ref)
						select {
						case <-current.finalizedCh:
						case <-time.After(3 * time.Second):
							t.Fatal("successor did not finalize")
						}
					}
					if _, err := m.stopRun(ctx, tenant, ref, "user:u1", model.ActorUser); err != nil {
						t.Errorf("stop after confirmed %s: %v", operation, err)
					}
					return
				}
				if gate.called {
					t.Errorf("refused %s reached launch admission", operation)
				}
				if !tc.native {
					runner.mu.Lock()
					launches := len(runner.procs)
					runner.mu.Unlock()
					if launches != 1 {
						t.Errorf("refused %s spawned a successor: launches=%d", operation, launches)
					}
				}
				var refusal *runErr
				if !errors.As(err, &refusal) || refusal.status != http.StatusConflict ||
					!strings.Contains(err.Error(), "supervised process has not finished") {
					t.Errorf("%s must refuse unconfirmed collection, got %v", operation, err)
				}
				if current, ok := m.rt.getLive(tenant, ref); !ok || current != lr {
					t.Errorf("%s lost the original supervised handle", operation)
				}
				if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "session work" {
					t.Errorf("refused %s changed the session's directory: %v", operation, err)
				}
				after, err := m.loadRun(ctx, tenant, ref)
				if err != nil {
					t.Fatal(err)
				}
				for _, column := range []string{colState, colRuntimeLaunchID, colClaimHolder, colClaimFence, colLastEventSeq, colClaudeSessionID} {
					if after[column] != before[column] {
						t.Errorf("refused %s changed %s: %v -> %v", operation, column, before[column], after[column])
					}
				}
				_, err = m.stopRun(ctx, tenant, ref, "user:u1", model.ActorUser)
				if !errors.As(err, &refusal) || refusal.status != http.StatusConflict {
					t.Errorf("stop after refused %s hid unconfirmed collection: %v", operation, err)
				}
				if tc.native {
					if err := syscall.Kill(lr.proc.PID(), 0); err != nil {
						t.Fatalf("native child exited before shutdown: %v", err)
					}
					if err := m.stopAllRuns(ctx); err != nil {
						t.Fatal(err)
					}
					select {
					case <-lr.proc.(*procProcess).waitDone:
					default:
						t.Error("shutdown no longer supervises the original native child")
					}
				}
			})
		}
	}
}

func TestRuntime_TerminalCleanupKeepsUncollectedWorktree(t *testing.T) {
	h, ctx := newWorktreeHarness(t, WithRunner(NewProcRunner()), WithProgram(writeFakeClaude(t)))
	created, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := h.m.rt.getLive(h.tenant, created.RunRef)
	if !ok {
		t.Fatal("created worktree run has no supervised child")
	}
	t.Cleanup(func() { _ = lr.proc.Stop(context.Background()) })
	waitFor(t, "native worktree conversation", func() bool {
		d, err := h.m.getRun(ctx, h.tenant, created.RunRef)
		return err == nil && d.ClaudeSessionID == "sess-real-1"
	})
	offline := New()
	offline.UseData(api.NewModuleData(h.st))
	bindStoreStanding(offline, h.st)
	if _, err := offline.stopRun(ctx, h.tenant, created.RunRef, model.ActorSystem, model.ActorSystem); err != nil {
		t.Fatal(err)
	}
	before, err := h.m.getRun(ctx, h.tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(before.WorkspacePath, "uncommitted.txt")
	if err := os.WriteFile(marker, []byte("keep uncommitted work"), 0o600); err != nil {
		t.Fatal(err)
	}
	branches, worktrees := h.branches(), h.worktreeCount()
	for _, discard := range []bool{false, true} {
		_, err := h.release(ctx, created.RunRef, discard)
		var refusal *runErr
		if !errors.As(err, &refusal) || refusal.status != http.StatusConflict ||
			!strings.Contains(err.Error(), "supervised process has not finished") {
			t.Errorf("cleanup (discard=%v) must refuse before git effects: %v", discard, err)
		}
		if h.branches() != branches || h.worktreeCount() != worktrees {
			t.Errorf("cleanup (discard=%v) changed the live child's branch/worktree", discard)
		}
		if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "keep uncommitted work" {
			t.Errorf("cleanup (discard=%v) removed the live child's work: %v", discard, err)
		}
		if _, err := h.m.stopRun(ctx, h.tenant, created.RunRef, "user:u1", model.ActorUser); !isRunConflict(err) {
			t.Errorf("stop after cleanup (discard=%v) hid the native child: %v", discard, err)
		}
	}
}

func TestRuntime_TerminalDeleteKeepsUncollectedProcess(t *testing.T) {
	ctx := context.Background()
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(NewProcRunner()),
		WithProgram(writeFakeClaude(t)), WithCredentialSource(staticCred()))
	created, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, created.RunRef)
	if !ok {
		t.Fatal("created run has no supervised child")
	}
	t.Cleanup(func() { _ = lr.proc.Stop(context.Background()) })
	waitFor(t, "native conversation", func() bool {
		d, err := m.getRun(ctx, tenant, created.RunRef)
		return err == nil && d.ClaudeSessionID == "sess-real-1"
	})
	// Another runtime cannot see this child's handle. Exercise its actual
	// cleanup rather than directly editing a row to manufacture cleaned state.
	offline := New()
	offline.UseData(api.NewModuleData(st))
	bindStoreStanding(offline, st)
	if _, err := offline.stopRun(ctx, tenant, created.RunRef, model.ActorSystem, model.ActorSystem); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.cleanupRun(ctx, tenant, created.RunRef, model.ActorSystem, model.ActorSystem); err != nil {
		t.Fatal(err)
	}
	before, err := m.getRun(ctx, tenant, created.RunRef)
	if err != nil || before.State != stateCleaned {
		t.Fatalf("offline cleanup: state=%s err=%v", before.State, err)
	}
	if err := m.deleteRun(ctx, tenant, created.RunRef); !isRunConflict(err) {
		t.Errorf("delete must retain the uncollected child's row: %v", err)
	}
	after, err := m.getRun(ctx, tenant, created.RunRef)
	if err != nil || after.State != before.State || after.LastEventSeq != before.LastEventSeq {
		t.Errorf("refused delete changed the row: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, created.RunRef, "user:u1", model.ActorUser); !isRunConflict(err) {
		t.Errorf("stop after refused delete no longer reports uncollected custody: %v", err)
	}
	if err := m.stopAllRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.deleteRun(ctx, tenant, created.RunRef); err != nil {
		t.Errorf("delete after confirmed collection: %v", err)
	}
}
