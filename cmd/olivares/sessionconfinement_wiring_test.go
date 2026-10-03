// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
)

// The engine the product boots confines every child it starts. Session launches
// and stdio MCP children both take their policy from the sessions module
// (Module.ConfinementPolicy); a composition that forgot sessions.WithConfinement
// would hand them nil and run them unconfined.
func TestProductionBootConfinesSessionAndMCPChildren(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.sessionsMod == nil {
		t.Fatal("the production composition has no sessions module")
	}
	folder := filepath.Join(dir, "folder")
	p := eng.sessionsMod.ConfinementPolicy([]string{folder}, nil)
	if p == nil {
		t.Fatal("session and MCP children would run unconfined: the boot path does not wire sessions.WithConfinement")
	}
	if !slices.Contains(p.Protect, filepath.Clean(dir)) || !slices.Contains(p.Protect, "/etc/olivares") {
		t.Fatalf("protected paths = %v, want the data directory and /etc/olivares", p.Protect)
	}
	if !slices.Equal(p.ReadWrite, []string{folder}) {
		t.Fatalf("read-write = %v", p.ReadWrite)
	}
}
