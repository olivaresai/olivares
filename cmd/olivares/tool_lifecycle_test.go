// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

func TestToolUpdateUsesExistingVerifiedInstallation(t *testing.T) {
	f := newFakeToolEngine(t)
	_, stderr, err := execSessionCLI(t, nil, append([]string{"tool", "update", "codex", "--version", "0.159.3"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("update: %v %s", err, stderr)
	}
	if !strings.Contains(f.allHits(), "POST "+agentToolsPath+"/installs") {
		t.Fatal("update bypassed verified plan/install")
	}
}

func TestDoctorReportsRuntimeBinaryOverridesByName(t *testing.T) {
	checks := doctorToolVersionOverrides(func(key string) string {
		if key == envSessionCodexBin {
			return "/private/runtime/codex"
		}
		return ""
	})
	if len(checks) != 1 || checks[0].Status != "warn" || !strings.Contains(checks[0].Detail, envSessionCodexBin) || strings.Contains(checks[0].Detail, "/private/") {
		t.Fatalf("doctor = %+v", checks)
	}
}
