// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
)

// "full" (bypassPermissions) is a run administrator's own decision: anyone else
// is refused before anything durable happens, and an administrator's launch
// starts without an approval.
func TestFullPreset_OnlyARunAdministrator(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	_, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "bypassPermissions",
		Actor: actorU, ActorKind: actorKindU,
	})
	if statusOf(err) != http.StatusForbidden {
		t.Fatalf("full without run administration = %v, want 403", err)
	}
	fr.mu.Lock()
	launched := len(fr.specs)
	fr.mu.Unlock()
	if len(gate.seen) != 0 || launched != 0 {
		t.Fatal("a refused full launch reached the gate or the runner")
	}

	if _, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "bypassPermissions",
		Actor: actorU, ActorKind: actorKindU, MayRunUnrestricted: true,
	}); err != nil {
		t.Fatalf("full by a run administrator: %v", err)
	}
	if got := gate.last(t).PermissionMode; got != "bypassPermissions" {
		t.Fatalf("gate saw mode %q", got)
	}

	// Every other level is unaffected.
	if _, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "acceptEdits",
		Actor: actorU, ActorKind: actorKindU,
	}); err != nil {
		t.Fatalf("acceptEdits: %v", err)
	}
}
