// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

func TestConsoleURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listen   string
		insecure bool
		want     string
	}{
		{"127.0.0.1:8443", false, "https://127.0.0.1:8443"},
		{"127.0.0.1:8443", true, "http://127.0.0.1:8443"},
		{"0.0.0.0:443", false, "https://0.0.0.0:443"},
	}
	for _, c := range cases {
		if got := consoleURL(c.listen, c.insecure); got != c.want {
			t.Errorf("consoleURL(%q, %v) = %q, want %q", c.listen, c.insecure, got, c.want)
		}
	}
}

// TestNewQuickstartCmdSecureByConstruction asserts quickstart cannot be talked
// into the insecure / demo paths: those flags do not exist on it, and its listen
// default is loopback. The secure posture is structural, not a runtime check.
func TestNewQuickstartCmdSecureByConstruction(t *testing.T) {
	t.Parallel()
	cmd := newQuickstartCmd()
	if cmd.Use != "quickstart" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "quickstart")
	}
	if f := cmd.Flags().Lookup("listen"); f == nil {
		t.Fatal("missing --listen flag")
	} else if f.DefValue != "127.0.0.1:8443" {
		t.Errorf("--listen default = %q, want loopback 127.0.0.1:8443", f.DefValue)
	}
	for _, forbidden := range []string{"insecure", "seed-demo"} {
		if cmd.Flags().Lookup(forbidden) != nil {
			t.Errorf("quickstart must not expose --%s (secure by construction)", forbidden)
		}
	}
}
