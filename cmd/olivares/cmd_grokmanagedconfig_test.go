// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

func TestGrokManagedConfigValidate(t *testing.T) {
	cmd := newGrokManagedConfigCmd()
	cmd.SetArgs([]string{"--policy", "-", "--validate"})
	cmd.SetIn(strings.NewReader(`{"sandbox_profile":"strict"}`))
	var out strings.Builder
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ok:") {
		t.Fatalf("out %q", out.String())
	}
}

func TestGrokManagedConfigRejectsUnknownProfile(t *testing.T) {
	cmd := newGrokManagedConfigCmd()
	cmd.SetArgs([]string{"--policy", "-", "--validate"})
	cmd.SetIn(strings.NewReader(`{"sandbox_profile":"yolo"}`))
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected unknown profile to fail")
	}
}
