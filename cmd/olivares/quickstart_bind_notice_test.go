// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/webaddr"
)

// QUICKSTART SAYS WHAT `serve` SAYS ABOUT A WILDCARD BIND, ONCE (#849). Its default
// panel listed the console addresses and never stated that the default bind is
// every interface, nor how to keep it on this host. The sentence is the one the
// serve banner prints (wildcardBindNotice), not a second wording.
func TestQuickstartStatesTheEveryInterfaceBindOnce(t *testing.T) {
	eng := bootPendingSetup(t)
	render := func(addr consoleAddress) string {
		t.Helper()
		var out strings.Builder
		if err := announceQuickstart(context.Background(), &out, eng, addr); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	wildcard := resolveConsoleAddress(webaddr.Address{}, ":8443", false)
	wildcard.Container = false
	notice := wildcardBindNotice(wildcard)
	for _, want := range []string{"EVERY interface", "bind 127.0.0.1 on the same", "--listen " + loopbackHTTPListen, "--grpc-listen " + loopbackGRPCListen} {
		if !strings.Contains(notice, want) {
			t.Fatalf("the shared notice lacks %q:\n%s", want, notice)
		}
	}
	// A custom port keeps the operator's port: the remedy never tells them to move to 8443.
	custom := resolveConsoleAddress(webaddr.Address{}, ":9443", false)
	custom.Container = false
	if n := strings.Count(render(custom), wildcardBindNotice(custom)); n != 1 || !strings.Contains(wildcardBindNotice(custom), "on the same") {
		t.Errorf("a custom-port panel printed the notice %d times or without the same-port remedy:\n%s", n, render(custom))
	}
	// In a container a loopback bind would hide the published port: the exposure is
	// still stated once, the loopback remedy is not offered.
	boxed := wildcard
	boxed.Container = true
	if got := render(boxed); strings.Count(got, "EVERY interface") != 1 || strings.Contains(got, "127.0.0.1") {
		t.Errorf("the container panel must state the bind once without a loopback remedy:\n%s", got)
	}
	got := render(wildcard)
	if n := strings.Count(got, notice); n != 1 {
		t.Errorf("quickstart printed the serve notice %d times, want once:\n%s", n, got)
	}
	serve := wildcard.withPlan(webAuthnPlan{Source: "per-request"}).Advice
	if !strings.Contains(serve, notice) {
		t.Errorf("serve's advice no longer carries the shared notice:\n%s", serve)
	}
	// Non-firing: a loopback bind and a declared address print no wildcard notice.
	for _, addr := range []consoleAddress{
		resolveConsoleAddress(webaddr.Address{}, loopbackHTTPListen, false),
		declaredConsoleAddress(t, "https://console.example.com", false),
	} {
		if got := render(addr); strings.Contains(got, "EVERY interface") {
			t.Errorf("a non-wildcard panel printed the wildcard notice:\n%s", got)
		}
	}
}
