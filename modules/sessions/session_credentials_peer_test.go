// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The helpers a broker starts answer only the run's own processes, and the broker cannot
// know which those are until the child exists: the launch tells it the child's process id
// the moment the runner has started it (BrokeredCredentials.Bind, LaunchSpec.OnStart).

func TestCredentials_TheChildsProcessIsToldToTheBrokerWhenItStarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); err != nil {
		t.Fatalf("create with credentials: %v", err)
	}
	spec := fr.lastSpec()
	if spec.OnStart == nil {
		t.Fatal("the launch does not ask the runner for the child's process id: the helpers could not tell the run's processes from any other")
	}
	spec.OnStart(4242)
	select {
	case pid := <-broker.bound:
		if pid != 4242 {
			t.Fatalf("the broker was told process %d, want 4242", pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the broker was not told the child's process id")
	}
}

func TestCredentials_ABrokerWithNoWayToBindItsHelpersStartsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.noBind = true
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a broker that cannot bind its helpers to the run: create = %v, want 422", err)
	}
	if n := launchCount(fr); n != 0 {
		t.Fatalf("%d sessions started with helpers that admit anybody", n)
	}
	broker.awaitRelease(t)
	select {
	case again := <-broker.released:
		t.Fatalf("the helpers of %s were released twice", again)
	case <-time.After(300 * time.Millisecond):
	}
}

// A companion process the engine starts for the run (an MCP helper) is not the run's first
// process: its start must not tell the broker's helpers whom to serve.
func TestCredentials_ACompanionDoesNotBindTheHelpers(t *testing.T) {
	t.Parallel()
	spec := LaunchSpec{Program: "claude", OnStart: func(int) { t.Error("a companion's start reached the broker") }}
	if got := companionSpec(spec); got.OnStart != nil {
		got.OnStart(1)
	}
}
