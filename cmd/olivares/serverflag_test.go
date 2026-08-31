// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// serverAliasGroups are the command groups that each invented their own spelling
// for "where the control plane is", and now also answer to --server.
var serverAliasGroups = []struct {
	path        []string
	legacyFlag  string
	legacyEnv   string
	description string
}{
	{[]string{"hookpep", "versions"}, "url", "OLIVARES_HOOK_PEP_URL", "policy authoring"},
	{[]string{"codex-hook"}, "endpoint", "OLIVARES_CODEX_HOOK_URL", "Codex PEP hook client"},
	{[]string{"claude-hook"}, "endpoint", "OLIVARES_HOOK_PEP_URL", "Claude Code PEP hook client"},
}

// TestServerFlagReachesEveryGroup is the E7 guarantee: one spelling works
// everywhere. Before it, an operator who had learned --server from `status`,
// `agent` or `findings` got "unknown flag" from hookpep and the hook clients.
func TestServerFlagReachesEveryGroup(t *testing.T) {
	root := newRootCmd()
	for _, g := range serverAliasGroups {
		name := strings.Join(g.path, " ")
		cmd, _, err := root.Find(g.path)
		if err != nil || cmd == nil {
			t.Fatalf("cannot resolve %q: %v", name, err)
		}
		if cmd.Flags().Lookup(canonicalServerFlag) == nil &&
			cmd.InheritedFlags().Lookup(canonicalServerFlag) == nil {
			t.Errorf("%q (%s) has no --%s", name, g.description, canonicalServerFlag)
		}
		// And the legacy spelling must survive: scripts and manifests use it.
		if cmd.Flags().Lookup(g.legacyFlag) == nil && cmd.InheritedFlags().Lookup(g.legacyFlag) == nil {
			t.Errorf("%q lost --%s; removing it breaks every existing invocation", name, g.legacyFlag)
		}
	}
}

// TestServerAliasPrecedence pins the resolution order, which is the part that
// could surprise somebody mid-migration.
func TestServerAliasPrecedence(t *testing.T) {
	const legacyEnv = "OLIVARES_TEST_LEGACY_URL"

	newProbe := func(t *testing.T) (*cobra.Command, *string, func() string, *bytes.Buffer) {
		t.Helper()
		cmd := &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { return nil }}
		var legacy string
		cmd.Flags().StringVar(&legacy, "url", "", "legacy")
		resolve := addServerAliasFlag(cmd, &legacy, "url", legacyEnv, false)
		var errb bytes.Buffer
		cmd.SetErr(&errb)
		cmd.SetOut(&bytes.Buffer{})
		return cmd, &legacy, resolve, &errb
	}

	t.Run("canonical flag alone", func(t *testing.T) {
		t.Setenv(canonicalServerEnv, "")
		t.Setenv(legacyEnv, "")
		cmd, _, resolve, errb := newProbe(t)
		cmd.SetArgs([]string{"--server", "https://a.example/"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := resolve(); got != "https://a.example" {
			t.Fatalf("resolved %q", got)
		}
		if strings.Contains(errb.String(), "still works") {
			t.Errorf("--server alone must not warn:\n%s", errb.String())
		}
	})

	t.Run("legacy flag wins and warns", func(t *testing.T) {
		t.Setenv(canonicalServerEnv, "")
		t.Setenv(legacyEnv, "")
		cmd, _, resolve, errb := newProbe(t)
		cmd.SetArgs([]string{"--url", "https://legacy.example", "--server", "https://new.example"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		// Somebody who passed the legacy flag meant it.
		if got := resolve(); got != "https://legacy.example" {
			t.Fatalf("resolved %q, want the explicitly passed legacy value", got)
		}
		if !strings.Contains(errb.String(), "both") {
			t.Errorf("passing both must say which one was used:\n%s", errb.String())
		}
	})

	t.Run("legacy env beats the canonical env", func(t *testing.T) {
		t.Setenv(legacyEnv, "https://legacy-env.example")
		t.Setenv(canonicalServerEnv, "https://canonical-env.example")
		cmd, _, resolve, _ := newProbe(t)
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		// A deployment that already sets the legacy variable must not change
		// behavior because a new variable exists.
		if got := resolve(); got != "https://legacy-env.example" {
			t.Fatalf("resolved %q, want the legacy environment value", got)
		}
	})

	t.Run("whitespace in the legacy env does not shadow a real canonical one", func(t *testing.T) {
		// firstNonEmptyEnv counts whitespace as a value, so this used to resolve
		// to "" — a stale variable holding spaces silently beat a valid one.
		t.Setenv(legacyEnv, "   ")
		t.Setenv(canonicalServerEnv, "https://canonical-env.example")
		cmd, _, resolve, _ := newProbe(t)
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := resolve(); got != "https://canonical-env.example" {
			t.Fatalf("resolved %q; whitespace is not a configured value", got)
		}
	})

	t.Run("conflicting env vars say which one won", func(t *testing.T) {
		t.Setenv(legacyEnv, "https://legacy-env.example")
		t.Setenv(canonicalServerEnv, "https://canonical-env.example")
		cmd, _, resolve, errb := newProbe(t)
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		_ = resolve()
		if !strings.Contains(errb.String(), legacyEnv) ||
			!strings.Contains(errb.String(), canonicalServerEnv) {
			t.Fatalf("two conflicting variables must name both and say which won:\n%s", errb.String())
		}
	})

	t.Run("canonical env when no legacy env", func(t *testing.T) {
		t.Setenv(legacyEnv, "")
		t.Setenv(canonicalServerEnv, "https://canonical-env.example/")
		cmd, _, resolve, _ := newProbe(t)
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := resolve(); got != "https://canonical-env.example" {
			t.Fatalf("resolved %q", got)
		}
	})
}
