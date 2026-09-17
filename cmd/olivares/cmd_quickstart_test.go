// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/core/webaddr"
)

// This was TestConsoleURL, and the third row is the reason it changed.
// consoleURL was `scheme + "://" + listen`, so a bind was printed as if it were
// an address: "0.0.0.0:443" came out as "https://0.0.0.0:443" and the bare
// ":8443" of the flagship Compose file came out as "https://:8443", which no
// browser can open. The rows below are the new, MEASURED behavior of what
// replaced it — the address is openable, the wildcard is named as a wildcard, and
// the default port is dropped the way a browser drops it.
func TestConsoleAddressFromABind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listen   string
		insecure bool
		want     string
		advice   bool
	}{
		{"127.0.0.1:8443", false, "https://127.0.0.1:8443", true}, // an IP is no relying party
		{"127.0.0.1:8443", true, "http://127.0.0.1:8443", true},   // loopback http is still a secure context
		{"0.0.0.0:443", false, "https://localhost", true},         // was "https://0.0.0.0:443"
		{":8443", false, "https://localhost:8443", true},          // was "https://:8443"
		{"panel.example.com:8443", false, "https://panel.example.com:8443", false},
	}
	for _, c := range cases {
		got := resolveConsoleAddress(webaddr.Address{}, c.listen, c.insecure).withPlan(webAuthnPlan{Source: "per-request"})
		if got.URL() != c.want {
			t.Errorf("resolveConsoleAddress(_, %q, %v) = %q, want %q", c.listen, c.insecure, got.URL(), c.want)
		}
		if c.advice != (got.Advice != "") {
			t.Errorf("resolveConsoleAddress(_, %q, %v) advice present = %v, want %v (%q)",
				c.listen, c.insecure, got.Advice != "", c.advice, got.Advice)
		}
		if got.Declared {
			t.Errorf("resolveConsoleAddress(_, %q, _) reported a declared address when none was given", c.listen)
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
