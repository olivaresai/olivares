// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Start does not depend on the recovery of launches that wait for an approval:
// when the composition cannot list its tenants (PostgreSQL without the admin
// pool refuses a cross-tenant read), Start still completes and the active
// kill-switch sweep still stops a running session.
func TestStartRunsTheKillSwitchSweepWhenWaitingLaunchesCannotBeRecovered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &flipStopGate{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "sess-start"}), WithCredentialSource(staticCred()),
		WithStopGate(gate), WithKillSwitchSweep(20*time.Millisecond))
	m.UseApprovalRecoveryTenants(func(context.Context) ([]model.TenantID, error) {
		return nil, fmt.Errorf("%w: no admin pool", store.ErrEnumerationNotAuthoritative)
	})
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start with an unreadable tenant list = %v, want nil", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "agent:a1", ActorKind: "agent",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.State != stateRunning {
		t.Fatalf("session should be running, got %s", dto.State)
	}
	gate.flip()
	waitFor(t, "running session terminated by the kill-switch sweep", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped
	})
}
