// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

func TestManagedGrokInstallPinsSessionDriver(t *testing.T) {
	dir := t.TempDir()
	obs := newHostToolObserverAt(filepath.Join(dir, "tools"), nil, dir, dir)
	if got := obs.latestProgram("grok"); got != "" {
		t.Fatalf("empty root must not pin: %q", got)
	}

	t.Setenv("OLIVARES_DATA_DIR", dir)
	opts := buildSessionRuntimeOptions(func(k string) string {
		if k == envSessionGrokBin || k == envSessionCodexBin {
			return ""
		}
		return os.Getenv(k)
	}, nil, nil)
	m := sessions.New(opts...)
	if m == nil {
		t.Fatal("module")
	}
}
