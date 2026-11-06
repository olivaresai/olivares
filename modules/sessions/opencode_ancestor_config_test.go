// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OpenCode can read project configuration above its working folder and run an
// ancestor's plugins before Olivares can apply policy. Checking for one at
// launch preparation would race its creation before exec, so every native
// OpenCode launch requires the operating-system boundary.
func TestOpenCodeAncestorProjectConfigRefusesUnconfinedLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		grandparent  bool
	}{
		{name: "no ancestor config"},
		{name: "parent .opencode", config: ".opencode"},
		{name: "parent opencode.json", config: "opencode.json"},
		{name: "parent opencode.jsonc", config: "opencode.jsonc"},
		{name: "grandparent .opencode", config: ".opencode", grandparent: true},
		{name: "grandparent opencode.json", config: "opencode.json", grandparent: true},
		{name: "grandparent opencode.jsonc", config: "opencode.jsonc", grandparent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			folder := filepath.Join(root, "ancestor", "session")
			if tc.grandparent {
				folder = filepath.Join(root, "ancestor", "nested", "session")
			}
			if err := os.MkdirAll(folder, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				config := filepath.Join(root, "ancestor", tc.config)
				var err error
				if tc.config == ".opencode" {
					err = os.Mkdir(config, 0o700)
				} else {
					err = os.WriteFile(config, []byte("{}\n"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}

			m := New(WithProviderDriver(NewOpenCodeDriver()))
			spec := m.childSpec(openCodeAncestorTestParams(folder, IsolationNative), childDecision{})
			if !spec.ConfinementRequired {
				t.Error("a native OpenCode launch does not require confinement")
			}
			spec.Program, spec.Args = "/bin/sh", []string{"-c", "printf ran > marker"}
			proc, err := NewProcRunner().Launch(context.Background(), spec)
			if err == nil {
				for range proc.Output() {
				}
				_, _ = proc.Wait()
				t.Fatal("the unconfined OpenCode launch started")
			}
			if !strings.Contains(err.Error(), "confinement") {
				t.Fatalf("refusal %q does not name confinement", err)
			}
			if _, err := os.Stat(filepath.Join(folder, "marker")); !os.IsNotExist(err) {
				t.Fatalf("the refused launch spawned its program: %v", err)
			}
		})
	}
}

func openCodeAncestorTestParams(folder string, isolation Isolation) CreateRunParams {
	return CreateRunParams{
		WorkspaceDir: folder,
		Isolation:    isolation,
		Transport:    TransportStreamJSON,
		ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverOpenCode},
	}
}
