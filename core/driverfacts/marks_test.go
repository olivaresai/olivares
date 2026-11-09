// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The console draws a tool by its Vendor word (web/src/components/ui/vendor-marks.ts);
// a vendor without an entry there shows its Monogram letters. Every tool the engine
// knows must have its logo: no vendor is exempt, because the icon set the console uses
// (lobe-icons) draws all of them.
func TestEveryToolVendorHasAConsoleMark(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "components", "ui", "vendor-marks.ts"))
	if err != nil {
		t.Fatal(err)
	}
	drawn := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z]+): \{$`).FindAllSubmatch(data, -1) {
		drawn[string(m[1])] = true
	}
	for _, facts := range All() {
		if !drawn[facts.Vendor] {
			t.Errorf("%s (vendor %q) has no mark in vendor-marks.ts: the console shows %q letters", facts.Name, facts.Vendor, facts.Monogram)
		}
	}
}
