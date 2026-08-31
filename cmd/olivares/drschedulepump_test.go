// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestDRScheduleInterval(t *testing.T) {
	log := slog.Default()

	if d, ok := drScheduleInterval("", log); !ok || d != defaultDRScheduleInterval {
		t.Fatalf("empty = (%v, %v), want default enabled", d, ok)
	}
	if d, ok := drScheduleInterval("5m", log); !ok || d != 5*time.Minute {
		t.Fatalf("5m = (%v, %v), want 5m enabled", d, ok)
	}
	// "0" is the explicit disable.
	if _, ok := drScheduleInterval("0", log); ok {
		t.Fatal("\"0\" must disable the pump")
	}
	// A typo keeps the default rather than changing behavior.
	if d, ok := drScheduleInterval("nonsense", log); !ok || d != defaultDRScheduleInterval {
		t.Fatalf("typo = (%v, %v), want default enabled", d, ok)
	}
	if d, ok := drScheduleInterval("-1m", log); !ok || d != defaultDRScheduleInterval {
		t.Fatalf("negative = (%v, %v), want default enabled", d, ok)
	}
}

func TestNewDRSchedulePumpNilWithoutAPI(t *testing.T) {
	if p := newDRSchedulePump(func(string) string { return "" }, nil, nil, slog.Default()); p != nil {
		t.Fatal("pump must be nil without an API server")
	}
}
