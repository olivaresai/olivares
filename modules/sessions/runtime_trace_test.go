// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
)

// A finalizing run removes only ITS OWN bearer link: a resume that reused the
// bearer has already pointed the digest at the new handle, and the old run's
// late finalize must not unlink it.
func TestUnlinkTraceKeepsAReplacedLink(t *testing.T) {
	t.Parallel()

	m, _, _, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	digest := [32]byte{1}
	older, newer := &liveRun{traceToken: digest}, &liveRun{traceToken: digest}
	m.rt.traceTokens[digest] = newer

	m.unlinkTrace(older)
	if m.rt.traceTokens[digest] != newer {
		t.Fatal("the older run's finalize removed the newer run's link")
	}
	m.unlinkTrace(newer)
	if _, ok := m.rt.traceTokens[digest]; ok {
		t.Fatal("a run's own link survived its finalize")
	}
}

// Without a trace provider the runtime behaves exactly as before: launches
// work, and the trace lookups answer nothing.
func TestNoTraceProviderChangesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))

	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if _, _, ok := m.SessionTrace(tenant, dto.RunRef); ok {
		t.Error("an untraced run exposed a session trace")
	}
	if _, _, ok := m.SessionTraceForToken("tok-secret"); ok {
		t.Error("an untraced run exposed a bearer link")
	}
}
