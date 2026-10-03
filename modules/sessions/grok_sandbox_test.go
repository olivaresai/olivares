// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

func init() {
	// The servers these tests run on need not have bubblewrap; the tests about it stand in
	// for a missing one themselves (withoutBubblewrap).
	grokLookPath = func(string) (string, error) { return "/usr/bin/bwrap", nil }
}

func withoutBubblewrap(t *testing.T) {
	t.Helper()
	old := grokLookPath
	grokLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { grokLookPath = old })
}

// HU2-34: without bubblewrap a Grok launch whose preset runs Grok's sandbox is refused before
// anything is spawned, with the reason, instead of "the owned provider process ended during
// initialize (HTTP 502)".
func TestGrokLaunchWithoutBubblewrapIsRefusedWithTheReason(t *testing.T) {
	withoutBubblewrap(t)
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	_, err := grokLaunch(t, m, tenant, prof)
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "bubblewrap (bwrap), which is not installed on this server") {
		t.Fatalf("launch without bubblewrap = %v, want 409 naming bubblewrap", err)
	}
}

// And the resolve rule does not offer Grok for a new session (the ask preset) on such a server.
func TestResolveDoesNotOfferGrokWithoutBubblewrap(t *testing.T) {
	withoutBubblewrap(t)
	m, tenant, _ := resolveHarness(t)
	_, err := m.ResolveProfile(context.Background(), tenant, "grok")
	var coded *codedRunErr
	if !errors.As(err, &coded) || coded.code != resolveCodeToolNotInstalled || !strings.Contains(err.Error(), "install the bubblewrap package") {
		t.Fatalf("resolve Grok without bubblewrap = %v, want tool_not_installed naming bubblewrap", err)
	}
	if _, err := m.ResolveProfile(context.Background(), tenant, "claude"); err != nil && strings.Contains(err.Error(), "bubblewrap") {
		t.Fatalf("another tool was refused for Grok's sandbox: %v", err)
	}
}
