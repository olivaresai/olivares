// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"path/filepath"
	"testing"
)

// The session credential is scoped to the folder the child works in, so the gate
// must be told that folder, resolved by the server, on create AND on resume — and
// it must be the directory the child is actually started in, not a second guess.
func TestLaunchIntent_CarriesTheResolvedFolder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{initSID: "sess-folder"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	root := t.TempDir()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: root})
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: ws.WorkspaceRef,
		Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if got := gate.last(t); got.FolderPath != real || got.WorkspaceRef != ws.WorkspaceRef {
		t.Errorf("create intent folder = (%q,%q), want (%q,%q)", got.WorkspaceRef, got.FolderPath, ws.WorkspaceRef, real)
	}
	if fr.lastSpec().Dir != real {
		t.Errorf("the child started in %q, the intent named %q", fr.lastSpec().Dir, real)
	}

	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-folder"
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, actorU, actorKindU, ""); err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if got := gate.last(t); got.Action != LaunchActionResume || got.FolderPath != real {
		t.Errorf("resume intent = (%q,%q), want a resume in %q", got.Action, got.FolderPath, real)
	}

	// No folder named: the run gets a directory of its own, and that is the folder.
	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	}); err != nil {
		t.Fatalf("createRun without a folder: %v", err)
	}
	if got := gate.last(t); got.FolderPath == "" || got.FolderPath != fr.lastSpec().Dir {
		t.Errorf("no-folder intent = %q, the child started in %q", got.FolderPath, fr.lastSpec().Dir)
	}
}
