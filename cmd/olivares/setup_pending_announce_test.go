// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// An unfinished first boot must never be silent, and must never print a blank
// token. Measured on 2026-08-05 against the shipped binary: restarting `serve`
// before setup was completed printed NOTHING about the pending setup, and
// `quickstart` printed its whole welcome panel with an EMPTY line where the token
// belongs — because SetupToken.Ensure returns empty plaintext once a token exists
// (it stores only a hash) and both call sites discarded or ignored that fact.

package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// bootPendingSetup returns an engine whose data dir already has a setup token and
// still has no administrator — the exact state a restart-before-setup leaves.
func bootPendingSetup(t *testing.T) *engine {
	t.Helper()
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	// Mint once, exactly as the first boot does; the plaintext is now unrecoverable.
	if _, created, err := eng.setupTok.Ensure(); err != nil || !created {
		t.Fatalf("first Ensure did not mint: created=%v err=%v", created, err)
	}
	return eng
}

func TestServeAnnouncesAPendingSetupInsteadOfSayingNothing(t *testing.T) {
	eng := bootPendingSetup(t)
	var out strings.Builder
	if err := announceSetup(context.Background(), &out, eng, "https://127.0.0.1:8443", false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.TrimSpace(got) == "" {
		t.Fatal("a restart with setup still pending printed nothing at all")
	}
	// It has to say the token cannot be reshown AND how to get out of it; either
	// half alone leaves the operator stuck.
	for _, want := range []string{"SETUP STILL PENDING", "CANNOT be shown again", "setup.token"} {
		if !strings.Contains(got, want) {
			t.Errorf("pending-setup notice lacks %q:\n%s", want, got)
		}
	}
	// And it must not offer a token, blank or otherwise.
	if strings.Contains(got, "Token:") {
		t.Errorf("pending-setup notice still offers a token field:\n%s", got)
	}
}

func TestQuickstartDoesNotOfferABlankOneTimeToken(t *testing.T) {
	eng := bootPendingSetup(t)
	var out strings.Builder
	if err := announceQuickstart(context.Background(), &out, eng, "https://127.0.0.1:8443"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "Complete setup with this one-time token") {
		t.Errorf("quickstart told the customer to paste a token it does not have:\n%s", got)
	}
	for _, want := range []string{"SETUP STILL PENDING", "CANNOT be shown again", "setup.token"} {
		if !strings.Contains(got, want) {
			t.Errorf("quickstart pending notice lacks %q:\n%s", want, got)
		}
	}
}
