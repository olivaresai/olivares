// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"testing"
	"time"
)

func TestMeterKnownModel(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC) }

	cs, ok := s.Meter("claude-sonnet-4-20250514", 1000, 500, 200, time.Time{})
	if !ok {
		t.Fatal("expected pricing for claude-sonnet-4-20250514")
	}
	if cs.CostMicroUSD == 0 {
		t.Error("expected non-zero cost")
	}
	if cs.CostType != costType {
		t.Errorf("CostType = %q, want %q", cs.CostType, costType)
	}
	if cs.ModelRef != "claude-sonnet-4-20250514" {
		t.Errorf("ModelRef = %q", cs.ModelRef)
	}
}

func TestMeterUnknownModel(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC) }

	_, ok := s.Meter("unknown-model", 1000, 500, 0, time.Time{})
	if ok {
		t.Fatal("expected no pricing for unknown model")
	}
}

func TestMeterNegativeTokensClamped(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC) }

	cs, ok := s.Meter("claude-sonnet-4-20250514", -100, -50, 0, time.Time{})
	if !ok {
		t.Fatal("expected pricing even with negative tokens (clamped to 0)")
	}
	if cs.CostMicroUSD != 0 {
		t.Errorf("expected zero cost for clamped-to-zero tokens, got %d", cs.CostMicroUSD)
	}
}
