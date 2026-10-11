// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// Standalone observation does not require a process runner, provider credential,
// optional module, or communication custody. Startup must preserve that posture.
func TestDependenciesObserveOnlyStart(t *testing.T) {
	m := New()
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if m.RunnerTransport() != "" || m.ProviderVaultWired() || m.CommunicationSessionCredentialsEnabled() || m.CommunicationCursorTokenKeyringBound() {
		t.Fatal("observation-only startup enabled an unwired capability")
	}
}

// A missing required engine port is reported before workers or recovery start.
func TestDependenciesRefuseIncompleteEngineBeforeRecovery(t *testing.T) {
	recovered := false
	d := &Dependencies{ApprovalRecoveryTenants: func(context.Context) ([]model.TenantID, error) { recovered = true; return nil, nil }}
	m := NewWithDependencies(d)
	if m.Dependencies != d || m.rt.Dependencies != d {
		t.Fatal("constructor installed separate dependency records")
	}
	err := m.Start(context.Background())
	if err == nil {
		t.Fatal("incomplete engine started")
	}
	for _, name := range []string{"Data", "Standing", "LaunchGate", "StopGate", "Recorder", "WorkSessionCreds", "RecoveryData", "WorkAuthorizer", "CommunicationAuthority", "ManagedStopAuthority", "SessionMCP"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("startup did not name %s: %v", name, err)
		}
	}
	if recovered {
		t.Fatal("incomplete engine performed approval recovery")
	}
	if m.rt.sweepCancel != nil {
		t.Fatal("incomplete engine started its stop sweep")
	}
}
