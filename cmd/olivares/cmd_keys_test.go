// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestKeysHelpDescribesCustodyAndShowsStatusExample(t *testing.T) {
	help := helpFor(t, "keys")
	if !strings.Contains(help, "local signing-key custody") {
		t.Errorf("keys help does not describe local signing-key custody:\n%s", help)
	}
	if !strings.Contains(help, "Examples:\n  olivares keys status") {
		t.Errorf("keys help does not show a status example:\n%s", help)
	}
}

func TestVersionReportsFIPSMode(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fips140=") || !strings.Contains(out.String(), "module=") ||
		!strings.Contains(out.String(), "license-key=") || !strings.Contains(out.String(), "ota-key=") {
		t.Fatalf("version output does not report FIPS mode and both trust anchors: %s", out.String())
	}
}
