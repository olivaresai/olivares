// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A replay of a work launch keeps the profile its run started on, but the owner agent's
// assignment is still asked: once the tool is no longer assigned to the agent, the replay is
// refused and nothing more is spawned (authorization over a quiet replay).
func TestLaunchForWorkReplayAfterTheAssignmentIsRevokedIsRefused(t *testing.T) {
	m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	m.rt.ProfileAccess = &agentAssignments{to: map[string]string{"tool:claude": agentRef}}
	spec := workLaunchSpec(t, m, tenant, itemID, agentRef, "")

	var control RuntimeControl = m
	first, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil || first.State != stateRunning {
		t.Fatalf("first launch with the tool assigned to the owner agent = %#v, %v", first, err)
	}
	m.rt.ProfileAccess = &agentAssignments{to: map[string]string{"tool:claude": "agent:someone-else"}}

	_, err = control.LaunchForWork(context.Background(), tenant, spec)
	if !errors.Is(err, ErrProfileNotAssigned) {
		t.Fatalf("replay after the assignment was revoked = %v, want ErrProfileNotAssigned", err)
	}
	if n := len(runner.specs); n != 1 {
		t.Fatalf("runner launches = %d, want the first one only", n)
	}
}

// The replay lookup reuses the run's profile only while that run holds the lease: past the
// lease's expiry the default is resolved again, so a profile from an ended generation is never
// carried into a new one.
func TestWorkLaunchDefaultProfileReusesTheRunsProfileOnlyWhileItsLeaseIsLive(t *testing.T) {
	m, st, tenant, _, stub := defaultProfileWorkHarness(t, true)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(t, m, tenant, itemID, agentRef, "")

	var control RuntimeControl = m
	first, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil || first.State != stateRunning {
		t.Fatalf("first launch = %#v, %v", first, err)
	}
	rec, err := m.loadRun(context.Background(), tenant, first.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	firstProfile := rec.String(colRunProfileID)
	_, lease, owner, now, err := m.readWorkLaunchOwner(context.Background(), tenant, itemID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil || state.ExpiresAt.IsZero() {
		t.Fatalf("lease state = %+v, %v; want a live lease with an expiry", state, err)
	}
	// The default can no longer be resolved, so only the reuse can answer.
	stub.mu.Lock()
	stub.installed["claude"] = false
	stub.mu.Unlock()
	agent := api.ModuleContext{Tenant: tenant, Principal: auth.Principal{AgentIdentity: owner}}

	live, err := m.workLaunchDefaultProfile(context.Background(), agent, spec, lease, now)
	if err != nil || live != firstProfile {
		t.Fatalf("default profile while the run holds the lease = %q, %v; want the run's %q", live, err, firstProfile)
	}
	expired := model.NewTimestamp(state.ExpiresAt.Add(time.Second))
	if got, err := m.workLaunchDefaultProfile(context.Background(), agent, spec, lease, expired); err == nil {
		t.Fatalf("default profile past the lease's expiry = %q, want a fresh resolve (refused: the tool is gone), not the run's profile", got)
	}
}
