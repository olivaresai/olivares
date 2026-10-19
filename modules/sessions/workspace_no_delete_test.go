// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceRegistrationAcceptsWriteNoDeleteAndStillRefusesUnknownModes(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "no-delete-registration")
	folder := t.TempDir()
	r := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin,
		map[string]any{"root_path": folder, "mount_mode": "write_no_delete"}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["mount_mode"] != "write_no_delete" {
		t.Fatalf("registration = %d %s, want 201 with mount_mode write_no_delete", r.code, r.raw)
	}
	bad := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin,
		map[string]any{"root_path": folder, "mount_mode": "write_no_remove"}, tenantHdr(tenant))
	if bad.code != http.StatusBadRequest || !strings.Contains(bad.raw, "write_no_delete") {
		t.Fatalf("unknown mode = %d %s, want 400 naming the accepted modes", bad.code, bad.raw)
	}
}

// The file API is the same promise as the kernel grant: a person or agent using the
// governed API may create and change, and may not delete or move.
func TestWorkspaceWriteNoDeleteFileAPIChangesButNeverDeletesOrMoves(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := t.Context()
	for _, tc := range []struct {
		mode        string
		wantRemoved bool
	}{{mountNoDelete, false}, {mountRW, true}} {
		t.Run(tc.mode, func(t *testing.T) {
			folder := t.TempDir()
			for name, content := range map[string]string{"keep.txt": "keep", "other.txt": "other"} {
				if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ws, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{RootPath: folder, MountMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			ref := ws.WorkspaceRef
			if _, err := m.writeFile(ctx, tenant, ref, "new.txt", []byte("new"), actorU, actorKindU); err != nil {
				t.Fatalf("create refused: %v", err)
			}
			if _, err := m.writeFile(ctx, tenant, ref, "keep.txt", []byte("changed"), actorU, actorKindU); err != nil {
				t.Fatalf("overwrite refused: %v", err)
			}
			if _, err := m.mkdir(ctx, tenant, ref, "made", actorU, actorKindU); err != nil {
				t.Fatalf("mkdir refused: %v", err)
			}
			deleteErr := m.deleteFile(ctx, tenant, ref, "other.txt", false, actorU, actorKindU)
			moveErr := m.moveFile(ctx, tenant, ref, "keep.txt", "moved.txt", actorU, actorKindU)
			if tc.wantRemoved {
				if deleteErr != nil || moveErr != nil {
					t.Fatalf("control workspace refused delete=%v move=%v", deleteErr, moveErr)
				}
				return
			}
			for name, err := range map[string]error{"delete": deleteErr, "move": moveErr} {
				if re, ok := err.(*runErr); !ok || re.status != http.StatusForbidden {
					t.Errorf("%s = %v, want 403", name, err)
				}
			}
			for name, want := range map[string]string{"keep.txt": "changed", "other.txt": "other", "new.txt": "new"} {
				if b, err := os.ReadFile(filepath.Join(folder, name)); err != nil || string(b) != want {
					t.Errorf("%s = %q, %v; want %q", name, b, err, want)
				}
			}
			if _, err := os.Stat(filepath.Join(folder, "moved.txt")); !os.IsNotExist(err) {
				t.Errorf("a refused move left moved.txt: %v", err)
			}
		})
	}
}

func TestWriteNoDeleteWorkspaceTakesNoNewWorktree(t *testing.T) {
	ws := &resolvedWorkspace{mountMode: mountNoDelete}
	if got := worktreeWorkspaceProblem(ws, IsolationNative); !strings.Contains(got, "no-delete") {
		t.Fatalf("problem = %q, want the no-delete refusal", got)
	}
	ws.mountMode = mountRW
	if got := worktreeWorkspaceProblem(ws, IsolationNative); got != "" {
		t.Fatalf("a read-write workspace was refused: %q", got)
	}
}

// A container or sandbox mount would be plain read-write, so the launch is refused at
// the one place create and resume share, before any program is resolved.
func TestWriteNoDeleteFolderRefusesContainerAndSandboxIsolation(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	for _, tc := range []struct {
		isolation Isolation
		mode      string
		refused   bool
	}{
		{IsolationContainer, mountNoDelete, true},
		{IsolationSandbox, mountNoDelete, true},
		{IsolationContainer, mountRW, false},
		{IsolationNative, mountNoDelete, false},
	} {
		t.Run(string(tc.isolation)+"/"+tc.mode, func(t *testing.T) {
			ws := &resolvedWorkspace{mountMode: tc.mode, rootReal: t.TempDir()}
			if got := ws.noDeleteIsolationRefusal(tc.isolation) != nil; got != tc.refused {
				t.Fatalf("refusal = %v, want %v", got, tc.refused)
			}
			if !tc.refused {
				return
			}
			// The launch itself refuses, before any program is resolved.
			_, _, stage, err := m.launchChild(t.Context(), t.Context(), "tenant", "run", &CreateRunParams{Isolation: tc.isolation}, childDecision{ws: ws})
			if err == nil || stage != "folder access mode" || !strings.Contains(err.Error(), "native sessions only") {
				t.Fatalf("launchChild: stage %q, err %v; want the native-only refusal", stage, err)
			}
		})
	}
}

// The agent's folder leaves the writable grants and comes back as the no-delete
// grant, and the launch is required to confine. Landlock grants add up, so a folder
// left in ReadWrite would hand the delete right back.
func TestWriteNoDeleteLaunchSpecMovesTheFolderToTheNoDeleteGrant(t *testing.T) {
	folder, data := t.TempDir(), t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, false))
	for _, tc := range []struct {
		mode         string
		wantNoDelete bool
		wantRequired bool
	}{{mountNoDelete, true, true}, {mountRW, false, false}, {mountRO, false, false}} {
		t.Run(tc.mode, func(t *testing.T) {
			ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, MountMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resolved.closeReadOnlyHandles)
			spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
			if got := len(spec.ConfinementNoDelete) == 1 && spec.ConfinementNoDelete[0] == resolved.rootReal; got != tc.wantNoDelete {
				t.Fatalf("no-delete grant = %v, want folder granted: %v", spec.ConfinementNoDelete, tc.wantNoDelete)
			}
			writable := slices.Contains(spec.Confinement.ReadWrite, resolved.rootReal)
			if tc.wantNoDelete == writable {
				t.Fatalf("folder writable with delete = %v beside no-delete = %v: grants add up", writable, tc.wantNoDelete)
			}
			if tc.mode == mountRW && !writable {
				t.Fatal("a read-write workspace lost its writable grant")
			}
			if spec.ConfinementRequired != tc.wantRequired {
				t.Fatalf("ConfinementRequired = %v, want %v", spec.ConfinementRequired, tc.wantRequired)
			}
		})
	}
}
