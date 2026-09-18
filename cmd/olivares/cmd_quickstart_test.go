// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/spf13/cobra"

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
// into the insecure / demo paths: those flags do not exist on it. The secure
// posture is structural, not a runtime check.
//
// IT NO LONGER ASSERTS A LOOPBACK BIND, and the reason is the point of the
// server-defaults change: the
// bind was never what made this path secure. TLS is on, there are no default
// credentials and first setup is gated by a single-use token — none of which
// depends on where the socket is. What the loopback default did do was hide a
// correctly-secured console from the server it was installed on. The default is
// now the wildcard, and the flags that WOULD make the bind load-bearing
// (--insecure, --seed-demo) are still absent from this command.
func TestNewQuickstartCmdSecureByConstruction(t *testing.T) {
	t.Parallel()
	cmd := newQuickstartCmd()
	if cmd.Use != "quickstart" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "quickstart")
	}
	if f := cmd.Flags().Lookup("listen"); f == nil {
		t.Fatal("missing --listen flag")
	} else if f.DefValue != defaultHTTPListen {
		t.Errorf("--listen default = %q, want %q", f.DefValue, defaultHTTPListen)
	}
	for _, forbidden := range []string{"insecure", "seed-demo"} {
		if cmd.Flags().Lookup(forbidden) != nil {
			t.Errorf("quickstart must not expose --%s (secure by construction)", forbidden)
		}
	}
}

// TestServeFamilyBindDefaultsAreTheWildcard is the regression row for the
// server defaults: every
// command that BINDS defaults to every interface, and every default is the one
// constant. A command that spells its own default is how the product came to have
// eight of them.
func TestServeFamilyBindDefaultsAreTheWildcard(t *testing.T) {
	t.Parallel()
	if defaultHTTPListen != ":8443" || defaultGRPCListen != ":8444" {
		t.Fatalf("bind defaults are %q/%q, want the dual-stack wildcards :8443/:8444",
			defaultHTTPListen, defaultGRPCListen)
	}
	// ":8443" must not be readable as loopback by the classifier that gates
	// --insecure and --seed-demo: if it were, widening the default would have
	// silently widened plaintext exposure too.
	for _, addr := range []string{defaultHTTPListen, defaultGRPCListen} {
		if hostIsLoopback(addr) {
			t.Errorf("hostIsLoopback(%q) = true; the wildcard default must not pass the plaintext guard", addr)
		}
	}
	for _, c := range []struct {
		name string
		cmd  *cobra.Command
		grpc bool
	}{
		{name: "serve", cmd: newServeCmd(), grpc: true},
		{name: "quickstart", cmd: newQuickstartCmd(), grpc: true},
	} {
		if f := c.cmd.Flags().Lookup("listen"); f == nil {
			t.Errorf("%s: missing --listen", c.name)
		} else if f.DefValue != defaultHTTPListen {
			t.Errorf("%s: --listen default = %q, want %q", c.name, f.DefValue, defaultHTTPListen)
		}
		if !c.grpc {
			continue
		}
		if f := c.cmd.Flags().Lookup("grpc-listen"); f == nil {
			t.Errorf("%s: missing --grpc-listen", c.name)
		} else if f.DefValue != defaultGRPCListen {
			t.Errorf("%s: --grpc-listen default = %q, want %q", c.name, f.DefValue, defaultGRPCListen)
		}
	}
	// `quickstart governed-rag` carries its own options struct rather than cobra
	// defaults, so it is checked where it is actually set.
	rag := newQuickstartGovernedRAGOptions()
	if rag.listen != defaultHTTPListen || rag.grpcListen != defaultGRPCListen {
		t.Errorf("quickstart governed-rag binds %q/%q, want %q/%q",
			rag.listen, rag.grpcListen, defaultHTTPListen, defaultGRPCListen)
	}
	// The agent gateway is NOT part of this change and stays loopback: it is an
	// opt-in surface with its own guard, and this row exists so widening it later
	// is a decision somebody makes on purpose.
	if rag.agentGatewayListen != "127.0.0.1:8446" {
		t.Errorf("agent gateway bind = %q, want the unchanged loopback default", rag.agentGatewayListen)
	}
}
