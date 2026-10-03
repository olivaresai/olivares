// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"testing"
)

func TestManagedSettingsOmittedFlagsMatchPublished26100Bytes(t *testing.T) {
	// This fixture is the published binary's output, also used by the managed
	// host upgrade checks. Compare bytes, including whitespace and the newline.
	want, err := os.ReadFile("../../modules/sessions/testdata/managed-settings-26.10.0.json")
	if err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{"agent", "managed-settings"})
	var out, diagnostics bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&diagnostics)
	if err := root.Execute(); err != nil {
		t.Fatalf("published omitted-flag invocation failed: %v", err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("published omitted-flag bytes changed:\nwant:\n%s\ngot:\n%s", want, out.Bytes())
	}
}
