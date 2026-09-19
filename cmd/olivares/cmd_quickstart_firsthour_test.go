// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestQuickstartWelcomeNamesFirstHourNextSteps(t *testing.T) {
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
	got := out.String()
	for _, want := range []string{
		"Complete setup with this one-time token (shown once, single-use):",
		"olivares doctor",
		"olivares agent tool detect",
		"Privileged login",
		"OLIVARES_HOOK_PEP_CONFIG",
		"First hour guide",
		"POST /v1/agents",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("welcome panel lacks %q:\n%s", want, got)
		}
	}
}

func TestQuickstartPendingSetupStillNamesDoctor(t *testing.T) {
	eng := bootPendingSetup(t)
	var out strings.Builder
	if err := announceQuickstart(context.Background(), &out, eng, declaredConsoleAddress(t, "https://127.0.0.1:8443", false)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "olivares doctor") {
		t.Errorf("pending-setup panel does not name doctor:\n%s", got)
	}
	if strings.Contains(got, "Complete setup with this one-time token") {
		t.Errorf("pending-setup panel offered a token it does not have:\n%s", got)
	}
}
