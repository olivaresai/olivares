// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

const otherApprovedWorkspace model.ID = "ws-other"

// fakeApprovedRemotes answers the organization's approved push remotes of a
// workspace and records which workspaces it was asked for.
type fakeApprovedRemotes struct {
	mu    sync.Mutex
	asked []model.ID
}

func (f *fakeApprovedRemotes) ApprovedRemotes(_ context.Context, _ model.TenantID, workspace model.ID) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, workspace)
	if workspace == otherApprovedWorkspace {
		return []string{"https://other.example.test/"}, nil
	}
	return []string{"https://forge.example.test:8443/", "ssh://forge.example.test/"}, nil
}

func (f *fakeApprovedRemotes) workspaces() []model.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// A confined session reaches the push remote of its workspace's approved
// repository binding, and nothing else beyond what it had: a remote the agent
// adds itself is not a destination. A session on its own login keeps the host
// network.
func TestApprovedRemoteIsASessionDestination(t *testing.T) {
	t.Parallel()

	// The same launch with no approved remotes wired: the policy before.
	m0, tenant0, runner0, _, _, _, prof0 := boundHarness(t)
	if _, err := launchBound(m0, tenant0, prof0); err != nil {
		t.Fatalf("launch without approved remotes: %v", err)
	}
	before := runner0.lastSpec().NetworkPolicy
	if before == nil {
		t.Fatal("a record-bound session has no network boundary")
	}

	m, tenant, runner, _, _, _, prof := boundHarness(t)
	src := &fakeApprovedRemotes{}
	m.ApprovedRemotes = src
	dto, err := launchBound(m, tenant, prof)
	if err != nil {
		t.Fatalf("launch with approved remotes: %v", err)
	}
	if asked := src.workspaces(); dto.AuthzWorkspaceID == "" || !slices.Equal(asked, []model.ID{model.ID(dto.AuthzWorkspaceID)}) {
		t.Fatalf("approved remotes asked for %v, want the run's workspace %q", asked, dto.AuthzWorkspaceID)
	}
	after := runner.lastSpec().NetworkPolicy
	if after == nil {
		t.Fatal("approved remotes removed the session's network boundary")
	}
	if want := append(slices.Clone(before.Providers), "https://forge.example.test:8443/"); !slices.Equal(after.Providers, want) {
		t.Fatalf("providers = %v, want %v", after.Providers, want)
	}
	// An ssh remote is not granted: the proxy tunnels ssh only to a public listed host,
	// and a forge on the organization's network would refuse the whole launch.
	if !slices.Equal(after.PublicProviders, before.PublicProviders) {
		t.Fatalf("listed providers = %v, want %v unchanged", after.PublicProviders, before.PublicProviders)
	}
	if len(after.Controls) != len(before.Controls) || after.CredentialSHA256 != before.CredentialSHA256 {
		t.Fatalf("approved remotes changed the relays or the key pin: %+v, before %+v", after, before)
	}
	for _, u := range append(slices.Clone(after.Providers), after.PublicProviders...) {
		if u == "https://other.example.test/" {
			t.Fatal("another workspace's approved remote reached this session")
		}
	}

	// A session on its own login has no boundary to widen.
	fr := &fakeRunner{}
	open, _, openTenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	stopModuleAtCleanup(t, open)
	open.ApprovedRemotes = &fakeApprovedRemotes{}
	if _, err := createProfiledTestRun(t, open, context.Background(), openTenant, secretEnvParams(nil, false)); err != nil {
		t.Fatalf("own-login launch: %v", err)
	}
	if policy := fr.lastSpec().NetworkPolicy; policy != nil {
		t.Fatalf("approved remotes confined an unconfined session: %+v", policy)
	}
}
