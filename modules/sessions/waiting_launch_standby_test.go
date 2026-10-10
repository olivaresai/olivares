// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestApprovedWaitingLaunchOnStandbyRefusesBeforeProviderSpawn(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "standby-approval"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:launcher", ActorKind: "user",
	})
	if err != nil || run.State != stateWaitingApproval {
		t.Fatalf("parked launch = %q, err %v, want waiting_approval", run.State, err)
	}
	m.stopApprovalWorkers(t.Context())
	gate.approved.Store(true)
	data := m.Data
	standby := &standbyRuntimeData{inner: data}
	m.Data = standby
	_, err = m.resumeRunInternal(t.Context(), tenant, run.RunRef, "", "", "", true, callerAsks{})
	m.Data = data
	if !errors.Is(err, store.ErrNotLeader) || standby.writeCount() == 0 {
		t.Fatalf("approved standby continuation = %v, writes %d, want fenced ErrNotLeader", err, standby.writeCount())
	}
	if count := launchCount(runner); count != 0 {
		t.Fatalf("approved standby spawned %d provider children", count)
	}
	row, err := m.loadRun(t.Context(), tenant, run.RunRef)
	if err != nil || row.String(colState) != stateWaitingApproval {
		t.Fatalf("standby changed parked launch: state %q, err %v", row.String(colState), err)
	}
}
