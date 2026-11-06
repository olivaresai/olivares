// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// One person at the cap is refused on the next launch with a sentence and a 429,
// resuming a stopped session included; another person is not.
func TestSessionCapPerPersonRefusesTheNextLaunch(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner),
		WithCredentialSource(staticCred()), WithSessionLimits(SessionLimits{MaxPerUser: 2}))
	m.QueuedCredentialCapture = sealCredential
	launch := func(person model.ID) (runDTO, error) {
		return createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
			queuedCredential: launchCredential(person),
		})
	}
	refusedAt := func(what string, err error) {
		t.Helper()
		var refused *runErr
		if !errors.As(err, &refused) || refused.status != http.StatusTooManyRequests {
			t.Fatalf("%s at a cap of 2: %v, want a 429", what, err)
		}
		if !strings.Contains(refused.msg, "2 sessions") || strings.ContainsAny(refused.msg, ".\n") {
			t.Fatalf("refusal %q must be one sentence naming the cap", refused.msg)
		}
	}
	alice, bob := model.NewID(), model.NewID()
	first, err := launch(alice)
	if err != nil {
		t.Fatalf("first launch under the cap: %v", err)
	}
	if _, err := launch(alice); err != nil {
		t.Fatalf("second launch under the cap: %v", err)
	}
	_, err = launch(alice)
	refusedAt("third launch of one person", err)
	if _, err := launch(bob); err != nil {
		t.Fatalf("another person's first launch: %v", err)
	}

	// A stopped session frees its place; a new launch takes it, and resuming the
	// stopped one then needs a place again.
	if _, err := m.stopRun(ctx, tenant, first.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stopped session", func() bool { d, _ := m.getRun(ctx, tenant, first.RunRef); return d.State == stateStopped })
	if _, err := launch(alice); err != nil {
		t.Fatalf("launch into the place a stop freed: %v", err)
	}
	_, err = m.resumeRun(ctx, tenant, first.RunRef, actorU, actorKindU, "")
	refusedAt("resuming a stopped session", err)
	if n := len(runner.specs); n != 4 {
		t.Fatalf("%d processes started, want 4 (a refused launch starts none)", n)
	}
}
