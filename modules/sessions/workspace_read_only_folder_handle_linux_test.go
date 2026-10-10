// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Redirect only the new workspace grant after the launch resolver and specification
// finish. The real runner/helper must retain the protected-directory exclusion.
func TestSR2CWorkspaceFolderGrantCannotRedirectIntoProtectedDataAtSpawn(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Fatal("this named kernel check needs Landlock: " + state.String())
	}
	for _, change := range []string{"canonical_control", "redirect_before_spawn", "ancestor_redirect", "directory_replacement"} {
		t.Run(change, func(t *testing.T) {
			swapped := change != "canonical_control"
			folder, data := t.TempDir(), t.TempDir()
			tools := filepath.Join(t.TempDir(), "tools")
			if err := os.Mkdir(tools, 0700); err != nil {
				t.Fatal(err)
			}
			protectedChild := filepath.Join(data, "tools")
			if err := os.Mkdir(protectedChild, 0700); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(protectedChild, "fixture-key")
			const canary = "SR2C_SYNTHETIC_PROTECTED_CANARY"
			if err := os.WriteFile(secret, []byte(canary+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, true))
			workspace, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, ReadOnlyFolders: []string{tools}})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, workspace.WorkspaceRef)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resolved.closeReadOnlyHandles)
			spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
			if spec.Confinement == nil || !spec.ConfinementRequired {
				t.Fatal("configured workspace grant did not require confinement")
			}
			spec.Program, spec.Args = "/bin/sh", []string{"-c", "if [ -e /proc/self/fd/3 ]; then echo inherited-grant-handle; exit 7; fi; echo confined-child-ran; /bin/cat \"$1\"", "sh", secret}
			if swapped {
				changed := tools
				if change == "ancestor_redirect" {
					changed = filepath.Dir(tools)
				}
				if err := os.Rename(changed, changed+"-original"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(changed + "-original") })
				switch change {
				case "directory_replacement":
					if err := os.Rename(protectedChild, tools); err != nil {
						t.Fatal(err)
					}
					spec.Args[len(spec.Args)-1] = filepath.Join(tools, "fixture-key")
				case "ancestor_redirect":
					if err := os.Symlink(data, changed); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.Symlink(protectedChild, tools); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			process, err := NewProcRunner().Launch(ctx, spec)
			if err != nil {
				if !swapped {
					t.Fatalf("canonical control did not launch: %v", err)
				}
				t.Log("redirected grant refused before spawn: ", err)
				return
			}
			t.Cleanup(func() {
				stop, done := context.WithTimeout(context.Background(), 2*time.Second)
				defer done()
				_ = process.Stop(stop)
			})
			var output strings.Builder
			for frame := range process.Output() {
				output.Write(frame.Data)
			}
			code, err := process.Wait()
			if err != nil {
				t.Fatal(err)
			}
			if !swapped && (code != 1 || !strings.Contains(output.String(), "confined-child-ran")) {
				t.Fatalf("canonical control did not run and deny the file: exit %d output %q", code, output.String())
			}
			if strings.Contains(output.String(), "inherited-grant-handle") {
				t.Fatal("the tool inherited a launch resolver's open directory handle")
			}
			if strings.Contains(output.String(), canary) {
				t.Fatalf("protected synthetic content was exposed after validated workspace grant handoff (exit %d)", code)
			}
			if code == 0 {
				t.Fatalf("protected file read succeeded without the expected denial: %s", output.String())
			}
		})
	}
}

// A retained object must not be widened by a writable ancestor after validation.
func TestValidatedWorkspaceFolderMovedUnderWriteGrantStaysReadOnly(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Fatal("this named kernel check needs Landlock with truncation protection: " + state.String())
	}
	folder, tools, data := t.TempDir(), t.TempDir(), t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, true))
	ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, ReadOnlyFolders: []string{tools}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolved.closeReadOnlyHandles)
	spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
	moved := filepath.Join(folder, "moved-tools")
	if err := os.Rename(tools, moved); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(moved, "escape")
	spec.Program, spec.Args = "/bin/sh", []string{"-c", "echo escaped > \"$1\"", "sh", escape}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, spec)
	if err == nil {
		t.Cleanup(func() {
			stop, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			_ = proc.Stop(stop)
		})
		for range proc.Output() {
		}
		if code, err := proc.Wait(); err != nil || code == 0 {
			t.Fatalf("a writable ancestor reopened the validated read-only object: exit=%d err=%v", code, err)
		}
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatalf("the moved read-only directory was changed: %v", err)
	}
}
