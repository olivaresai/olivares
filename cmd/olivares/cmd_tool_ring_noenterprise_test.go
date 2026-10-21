// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Tool version rings are a Business command. Community has no `tool ring`: the hook in
// toolRingCmd adds nothing, the help does not offer it, and trying it is an unknown
// command that never reaches the engine.
func TestCommunityToolHasNoRingCommand(t *testing.T) {
	root := newRootCmd()
	found, _, err := root.Find([]string{"tool", "ring"})
	if err == nil && found != nil && found.Name() == "ring" {
		t.Fatalf("Community offers %s", found.CommandPath())
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"tool", "--help"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\n  ring ") {
		t.Fatalf("Community help offers ring:\n%s", out.String())
	}
	spy := newObserveSpy(t, http.StatusOK, "{}")
	if _, _, err := execRoot(t, observeArgs(spy.srv.URL, "tool", "ring", "ls")...); err == nil {
		t.Error("tool ring ran in Community")
	}
	if spy.count() != 0 {
		t.Errorf("tool ring spent %d HTTP requests in Community", spy.count())
	}
}
