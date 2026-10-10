// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
)

// A session that ended before its first turn has no conversation to continue
// (HU-26). Resuming it starts it again, in the same folder, with the same
// settings: one new process, no --resume, the same directory.
func TestResume_ANeverStartedSessionStartsAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{} // reports no session id: the conversation never started
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))

	root := t.TempDir()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: root})
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "acceptEdits",
		WorkspaceRef: ws.WorkspaceRef, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	first := fr.lastSpec()
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if d, _ := m.getRun(ctx, tenant, dto.RunRef); d.ClaudeSessionID != "" {
		t.Fatalf("fixture captured a session id %q; this test needs none", d.ClaudeSessionID)
	}

	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, actorU, actorKindU, ""); err != nil {
		t.Fatalf("resume of a never-started session = %v, want it started again", err)
	}
	again := fr.lastSpec()
	if id, ok := resumeFlagValue(again); ok {
		t.Fatalf("a session with no conversation was resumed into %q", id)
	}
	if again.Dir != first.Dir {
		t.Fatalf("started again in %q, first ran in %q", again.Dir, first.Dir)
	}
	if mode, _ := argvValue(again.Args, "--permission-mode"); mode != "acceptEdits" {
		t.Fatalf("started again with permission mode %q, want the session's own", mode)
	}
}
