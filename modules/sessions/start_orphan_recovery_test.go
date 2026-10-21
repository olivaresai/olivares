// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A crash leaves the run's row running with no process behind it. Nothing read
// that row as stopped until somebody asked to stop it, so after a restart a dead
// session showed as running. Start now reads it as stopped and says why.
func TestStartStopsTheRunsACrashedEngineLeftRunning(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "crashed"}), WithCredentialSource(staticCred()))
	m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
	crashed := startedRun(t, m, tenant)
	live := startedRun(t, m, tenant)
	stopped := startedRun(t, m, tenant)
	if _, err := m.stopRun(ctx, tenant, stopped, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	before, err := m.getRun(ctx, tenant, stopped)
	if err != nil {
		t.Fatal(err)
	}
	// A launch that had reserved its row and not yet started its process, and a run
	// that had already failed: the second is over, whatever the engine did next.
	pending := startedRun(t, m, tenant)
	mutateRunForCredentialTest(t, m.Data, tenant, pending, func(r model.Record) { r[colState] = statePending })
	failed := startedRun(t, m, tenant)
	mutateRunForCredentialTest(t, m.Data, tenant, failed, func(r model.Record) { r[colState] = stateFailed })
	// The crash: the row says running, the engine that held its process is gone.
	m.rt.dropLive(tenant, crashed)
	m.rt.dropLive(tenant, pending)
	m.rt.dropLive(tenant, failed)

	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if d, err := m.getRun(ctx, tenant, pending); err != nil || d.State != stateStopped || !strings.HasPrefix(d.Reason, engineDiedReason) {
		t.Errorf("a pending run: state %q reason %q (%v), want stopped saying the engine died", d.State, d.Reason, err)
	}
	if d, err := m.getRun(ctx, tenant, failed); err != nil || d.State != stateFailed {
		t.Errorf("a failed run: state %q (%v), want it left failed", d.State, err)
	}
	got, err := m.getRun(ctx, tenant, crashed)
	if err != nil || got.State != stateStopped {
		t.Fatalf("crashed run: state %q (%v), want %q", got.State, err, stateStopped)
	}
	if !strings.HasPrefix(got.Reason, "the engine died; resume the session to continue") {
		t.Errorf("crashed run reason = %q, want it to say the engine died", got.Reason)
	}
	if !strings.Contains(got.Reason, "not confirmed terminated") {
		t.Errorf("crashed run reason = %q, want it to keep saying the process is not confirmed terminated", got.Reason)
	}
	if d, err := m.getRun(ctx, tenant, live); err != nil || d.State != stateRunning {
		t.Errorf("a run this engine holds: state %q (%v), want it left running", d.State, err)
	}
	if d, err := m.getRun(ctx, tenant, stopped); err != nil || d.State != stateStopped || d.Reason != before.Reason {
		t.Errorf("a run a person stopped: state %q reason %q (%v), want it as it was: %q", d.State, d.Reason, err, before.Reason)
	}
}

// Start reads a row as the crashed engine's only where it is the serving engine
// and no other runtime may hold the row. A command that is not serving has no tenant
// list, and the runtime that owns a row says another engine may hold its process
// (communication credentials): there only leader recovery decides.
func TestStartLeavesRunsAloneWhereAnotherEngineMayHoldThem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Module, model.TenantID)
	}{
		{"communication credentials on", func(m *Module, _ model.TenantID) { m.rt.CommunicationCredentialsEnabled = true }},
		{"no tenant list, as in an offline command", func(m *Module, _ model.TenantID) { m.ApprovalRecoveryTenants = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "held-elsewhere"}), WithCredentialSource(staticCred()))
			m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
			run := startedRun(t, m, tenant)
			m.rt.dropLive(tenant, run)
			tc.setup(m, tenant)

			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != stateRunning {
				t.Fatalf("state %q (%v), want running: Start must not stop a row another engine may hold", d.State, err)
			}
		})
	}
}

// fixedElector answers the one question Start asks of the store's leader fence.
type fixedElector struct {
	store.LeaderElector
	active bool
}

func (e fixedElector) Active() bool { return e.active }

// A standby is not the writer: it revokes a row's credentials before it writes the
// row, so on a standby Start would take them from a session the leader still runs.
func TestStartLeavesRunsToTheLeaderOnAStandby(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		want   string
	}{
		{"leader", true, stateStopped},
		{"standby", false, stateRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "ha"}), WithCredentialSource(staticCred()))
			m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
			m.rt.ManagedStopAuthority = &managedStopPorts{elector: fixedElector{active: tc.active}}
			run := startedRun(t, m, tenant)
			m.rt.dropLive(tenant, run)

			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != tc.want {
				t.Fatalf("state %q (%v), want %q", d.State, err, tc.want)
			}
		})
	}
}

// Start never fails over the runs a crashed engine left: a tenant list it cannot
// read leaves them as they are for the next start.
func TestStartCompletesWhenTheTenantsOfCrashedRunsCannotBeListed(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "unlisted"}), WithCredentialSource(staticCred()))
	m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) {
		return nil, fmt.Errorf("%w: no admin pool", store.ErrEnumerationNotAuthoritative)
	}
	run := startedRun(t, m, tenant)
	m.rt.dropLive(tenant, run)

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start with an unreadable tenant list = %v, want nil", err)
	}
	if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != stateRunning {
		t.Fatalf("state %q (%v), want running: nothing could say the engine died", d.State, err)
	}
}

// A tenant with more crashed runs than one listing reads has them all stopped.
func TestStopOrphanedTenantRunsReadsEveryPage(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "paged"}), WithCredentialSource(staticCred()))
	var runs []string
	for range 5 {
		run := startedRun(t, m, tenant)
		m.rt.dropLive(tenant, run)
		runs = append(runs, run)
	}

	if err := m.stopOrphanedTenantRuns(ctx, tenant, 2); err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != stateStopped {
			t.Errorf("run %s: state %q (%v), want stopped: a page was skipped", run, d.State, err)
		}
	}
}

// startedRun launches one run on the fake runner and returns its ref. Its process
// is stopped when the test ends, as in TestRuntime_OrphanReconciledOnStop.
func startedRun(t *testing.T, m *Module, tenant model.TenantID) string {
	t.Helper()
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("created run has no live handle")
	}
	t.Cleanup(func() {
		_ = lr.proc.Stop(context.Background())
		select {
		case <-lr.finalizedCh:
		case <-time.After(3 * time.Second):
			t.Error("the run's process did not finalize")
		}
	})
	return dto.RunRef
}
