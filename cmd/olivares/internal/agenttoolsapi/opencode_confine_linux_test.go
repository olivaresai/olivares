// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package agenttoolsapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// The release container's HOME is inside its protected data volume. OpenCode
// creates its XDG directories even for --version. Landlock grants only paths
// that exist when the child starts; the engine home itself must stay protected.
func TestOpenCodeProbePreparesItsHomesInsideProtectedEngineHome(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this reproducer requires Landlock: " + s.Reason)
	}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default XDG", true: "explicit XDG"}[explicit], func(t *testing.T) {
			data, bin := toolinstalltest.ExecCapableDir(t), toolinstalltest.ExecCapableDir(t)
			home := filepath.Join(data, "home")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			for _, name := range []string{"OPENCODE_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
				t.Setenv(name, "")
			}
			if explicit {
				for name, suffix := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache"} {
					t.Setenv(name, filepath.Join(home, suffix))
				}
			}
			engineOnly := filepath.Join(home, "engine-only")
			if err := os.WriteFile(engineOnly, []byte("engine-only"), 0600); err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(bin, "opencode")
			script := `#!/bin/sh
set -eu
for base in "${XDG_CONFIG_HOME:-$HOME/.config}" "${XDG_DATA_HOME:-$HOME/.local/share}" "${XDG_STATE_HOME:-$HOME/.local/state}" "${XDG_CACHE_HOME:-$HOME/.cache}"; do
 mkdir -p "$base/opencode"
 touch "$base/opencode/native-state"
done
if cat "$HOME/engine-only" >/dev/null 2>&1; then echo EngineFileRead; exit 9; fi
if touch "$HOME/planted" 2>/dev/null; then echo EngineHomeWritten; exit 9; fi
case "$1" in
 --version) echo 1.19.0;;
 auth) printf '└  0 credentials\n';;
 models) echo local/fixture;;
esac
`
			if err := os.WriteFile(program, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			installer := toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{})
			m, err := New(t.Context(), installer, filepath.Join(data, "tools"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.SetProgramResolver(func(string) string { return program })
			m.SetChildCommand(func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error) {
				cmd, _, err := confine.Command(ctx, confine.Policy{ReadWrite: rw, ReadOnly: ro, Protect: []string{data}}, program, args...)
				return cmd, err
			})
			env, cfg, err := ownLogin("opencode")
			if err != nil {
				t.Fatal(err)
			}
			snap, err := m.probeProvider(t.Context(), providerInstance{id: "opencode", driver: "opencode", own: true, env: env, configDir: cfg})
			if err != nil || snap.Version != "1.19.0" || snap.State != stateNotSignedIn {
				t.Fatalf("confined OpenCode probe: %+v, %v", snap, err)
			}
		})
	}
}
