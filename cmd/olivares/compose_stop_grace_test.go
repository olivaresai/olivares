// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestComposeStopGraceOutlastsTheDrain pins that the shipped Compose files let the
// engine finish its stop. Docker kills a container 10 s after SIGTERM unless
// stop_grace_period says otherwise, and the engine drains its listeners for up to
// shutdownDrainBudget before it stops sessions and closes the store; a kill inside
// that window is an unclean stop on every `docker compose up -d` after a pull. The
// grace leaves the drain budget plus as much again for what follows it. An overlay
// that leaves the key unset keeps the base value.
func TestComposeStopGraceOutlastsTheDrain(t *testing.T) {
	want := 2 * shutdownDrainBudget
	paths, err := filepath.Glob(filepath.Join("..", "..", "deploy", "compose", "docker-compose*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no compose files found: %v", err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		raw := readComposeOlivares(t, name).StopGracePeriod
		if raw == "" && name != "docker-compose.yml" {
			continue
		}
		if got, err := time.ParseDuration(raw); err != nil || got < want {
			t.Errorf("%s: services.olivares.stop_grace_period = %q (%v); want at least %s", name, raw, err, want)
		}
	}
}
