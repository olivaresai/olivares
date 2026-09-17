// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
)

func TestDRImportRejectsExportFromNewerEngine(t *testing.T) {
	if err := dr.CheckImportCompatibility(&dr.Manifest{Version: "26.9.0"}, "26.9.0"); err != nil {
		t.Fatalf("same-version export rejected: %v", err)
	}
	if err := dr.CheckImportCompatibility(&dr.Manifest{Version: "26.8.0"}, "26.9.0"); err != nil {
		t.Fatalf("older export rejected: %v", err)
	}
	err := dr.CheckImportCompatibility(&dr.Manifest{Version: "26.10.0"}, "26.9.0")
	if err == nil || !strings.Contains(err.Error(), "newer engine 26.10.0") || !strings.Contains(err.Error(), "install 26.10.0 or later") {
		t.Fatalf("newer export refusal is absent or not actionable: %v", err)
	}
}

func TestDRImportUnstampedBinaryCannotClaimCompatibility(t *testing.T) {
	err := dr.CheckImportCompatibility(&dr.Manifest{Version: "26.9.0"}, "dev")
	if err == nil || !strings.Contains(err.Error(), "unstamped") {
		t.Fatalf("unstamped binary accepted a stamped export: %v", err)
	}
}
