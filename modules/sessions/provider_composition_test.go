// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestProfiledCompositionCannotBypassRequestedK3Readiness(t *testing.T) {
	runner := &fakeRunner{}
	inference := &countingCredentialSource{}
	gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
	m, st, tenant, profile, _ := profiledHarness(t, WithRunner(runner), WithCredentialSource(inference), WithLaunchGate(gate))
	m.EnableProfiledLaunches()
	m.UseCommunicationStoreReadinessWitness(&communicationReadinessStub{})
	probe := &dualCredentialProbe{now: m.now}
	m.UseWorkSessionCredentialSource(dualWorkSource{probe})
	m.UseCommunicationSessionCredentialSource(dualCommunicationSource{probe})
	m.EnableCommunicationSessionCredentials()
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, ProviderProfileRef: profile.Ref,
		Actor: "user:composition", ActorKind: model.ActorUser,
	})
	var refusal *runErr
	if !errors.As(err, &refusal) || refusal.status != http.StatusServiceUnavailable {
		t.Fatalf("profiled launch without effective K3 readiness = %v, want 503", err)
	}
	inference.mu.Lock()
	inferenceCalls := inference.calls
	inference.mu.Unlock()
	calls := probe.snapshot()
	if gate.called || inferenceCalls != 0 || calls.workMint != 0 || calls.commMint != 0 || launchCount(runner) != 0 || countRuns(t, st, tenant) != 0 {
		t.Fatal("profile selection bypassed requested K3 readiness and left launch effects")
	}
}
