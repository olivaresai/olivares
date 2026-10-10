// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/webaddr"
)

func TestQuickstartAddressesMatchTheListenBind(t *testing.T) {
	eng := bootPendingSetup(t)
	for _, tc := range []struct {
		name, listen, declared string
		want                   []string
		localOnly              bool
	}{
		{"all interfaces", ":8443", "", []string{
			"Console: https://localhost:8443",
			"Console (LAN): https://192.168.1.20:8443",
			"Console (LAN): https://[2001:db8::20]:8443",
		}, false},
		{"declared public address", ":8443", "https://console.example.com", []string{
			"Console: https://console.example.com",
			"Console (local): https://localhost:8443",
			"Console (LAN): https://192.168.1.20:8443",
			"Console (LAN): https://[2001:db8::20]:8443",
		}, false},
		{"IPv4 loopback", "127.0.0.1:8443", "", []string{
			"Console: https://127.0.0.1:8443",
		}, true},
		{"IPv6 loopback", "[::1]:8443", "", []string{
			"Console: https://[::1]:8443",
		}, true},
		{"proxy on loopback", "127.0.0.1:8443", "https://console.example.com", []string{
			"Console: https://console.example.com",
		}, true},
		{"specific LAN interface", "192.168.1.20:8443", "", []string{
			"Console: https://192.168.1.20:8443",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var declared webaddr.Address
			if tc.declared != "" {
				var err error
				declared, err = webaddr.Parse("test", tc.declared)
				if err != nil {
					t.Fatal(err)
				}
			}
			addr := resolveConsoleAddress(declared, tc.listen, false)
			if tc.listen == ":8443" {
				// Stable addresses for the panel, independent of this test host.
				addr.Reachable = []webaddr.Address{}
				for _, origin := range []string{"https://192.168.1.20:8443", "https://[2001:db8::20]:8443", "https://127.0.0.1:8443"} {
					a, err := webaddr.Parse("test", origin)
					if err != nil {
						t.Fatal(err)
					}
					addr.Reachable = append(addr.Reachable, a)
				}
			}
			var out strings.Builder
			if err := announceQuickstart(context.Background(), &out, eng, addr); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if prefix := strings.Join(tc.want, "\n") + "\n"; !strings.HasPrefix(got, prefix) {
				t.Errorf("address lines:\n%s\nwant prefix:\n%s", got, prefix)
			}
			if count := strings.Count(got, "Console"); count != len(tc.want) {
				t.Errorf("printed %d address lines, want %d", count, len(tc.want))
			}
			if strings.Contains(got, "This engine listens on loopback only (this computer).") != tc.localOnly {
				t.Errorf("loopback guidance does not match %q:\n%s", tc.listen, got)
			}
		})
	}
}

func TestQuickstartWelcomePointsToConsoleFlow(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	var out strings.Builder
	if err := announceQuickstart(context.Background(), &out, eng, declaredConsoleAddress(t, "https://127.0.0.1:8443", false)); err != nil {
		t.Fatal(err)
	}
	got := setupTokenShape.ReplaceAllString(out.String(), "<REDACTED_SETUP_TOKEN>")
	for _, want := range []string{
		"Console: https://127.0.0.1:8443",
		"Next: Open the console; it guides setup, sign-in and your first session.",
		"one-time token",
		"Ctrl-C",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("welcome panel lacks %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"passkey", "POST /v1/", "auth bootstrap", "OLIVARES_HOOK_PEP_CONFIG", "FIRST RUN"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(forbidden)) {
			t.Errorf("default panel contains %q:\n%s", forbidden, got)
		}
	}

	// scripts/quickstart-argv-smoke.sh asserts this panel on the built binary, but only the
	// export check runs it, so a reworded panel left it failing a correct quickstart. Every
	// fixed string it greps for must be printed here, where the package tests catch a reword.
	script, err := os.ReadFile("../../scripts/quickstart-argv-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	smokeExpects := regexp.MustCompile(`command grep -qF ('[^']+'|"[^"]+") "\$LOG"`).FindAllStringSubmatch(string(script), -1)
	if n := strings.Count(string(script), "grep -qF"); n == 0 || n != len(smokeExpects) {
		t.Fatalf("scripts/quickstart-argv-smoke.sh has %d grep -qF assertions; this test read %d", n, len(smokeExpects))
	}
	for _, m := range smokeExpects {
		want := strings.ReplaceAll(m[1][1:len(m[1])-1], "$PORT", "8443")
		if !strings.Contains(got, want) {
			t.Errorf("scripts/quickstart-argv-smoke.sh expects %q, which the first-run panel does not print:\n%s", want, got)
		}
	}
}

func TestQuickstartPendingSetupPointsToConsoleAndTokenRecovery(t *testing.T) {
	eng := bootPendingSetup(t)
	var out strings.Builder
	if err := announceQuickstart(context.Background(), &out, eng, declaredConsoleAddress(t, "https://127.0.0.1:8443", false)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Console:", "Next:", "issued earlier", "first-boot", "--new-token"} {
		if !strings.Contains(got, want) {
			t.Errorf("pending-setup panel lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Complete setup with this one-time token") {
		t.Errorf("pending-setup panel offered a token it does not have:\n%s", got)
	}
}

func TestQuickstartReturningRunUsesReturningGuidance(t *testing.T) {
	dir := t.TempDir()
	// Use quickstart's fresh module profile, rather than a profile-free legacy
	// installation whose first serving start deliberately asks main to re-exec.
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ApplyModuleProfile: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.authr.BootstrapSuperadmin(context.Background(), "returning@example.invalid", "Returning-fixture-password-123!"); err != nil {
		_ = eng.Close()
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"returning", "returning again"} {
		if !t.Run(name, func(t *testing.T) {
			got := runQuickstart(t, regexp.MustCompile("sign in"), "--data-dir", dir,
				"--listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
				"--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)))
			if !strings.Contains(got, "Next: Open the console and sign in to continue your work.") {
				t.Errorf("returning run lacks its next step:\n%s", got)
			}
			for _, forbidden := range []string{"FIRST RUN", "setup token", "passkey", "POST /v1/", "olivares doctor"} {
				if strings.Contains(strings.ToLower(got), strings.ToLower(forbidden)) {
					t.Errorf("returning output contains %q:\n%s", forbidden, got)
				}
			}
		}) {
			break
		}
	}
}
