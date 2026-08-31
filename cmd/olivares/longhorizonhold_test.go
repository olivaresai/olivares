// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// fakeRecon is a no-op long-horizon reconciler for the loop's env/wiring tests.
type fakeRecon struct{}

func (fakeRecon) ReconcileTenant(context.Context, model.TenantID) error { return nil }

func TestLongHorizonHoldIntervalEnv(t *testing.T) {
	log := discardLog()
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", defaultLongHorizonHoldInterval, true},
		{"12h", 12 * time.Hour, true},
		{" 30m ", 30 * time.Minute, true},
		{"0", 0, false},  // the explicit zero is the only disable
		{"0s", 0, false}, //
		{"garbage", defaultLongHorizonHoldInterval, true},
		{"-5m", defaultLongHorizonHoldInterval, true},
	}
	for _, c := range cases {
		got, ok := longHorizonHoldInterval(c.raw, log)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("longHorizonHoldInterval(%q) = (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestNewLongHorizonHoldLoopSemantics(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	// No reconciler (the default/community build) ⇒ no loop, regardless of cadence.
	if l := newLongHorizonHoldLoop(getenv, nil, nil, discardLog()); l != nil {
		t.Fatal("a nil reconciler must yield no loop (community/no-add-on)")
	}
	// Reconciler present, default cadence.
	if l := newLongHorizonHoldLoop(getenv, nil, fakeRecon{}, discardLog()); l == nil || l.interval != defaultLongHorizonHoldInterval {
		t.Fatalf("unset env with a reconciler must yield the default-cadence loop, got %+v", l)
	}
	// Explicit disable.
	env[longHorizonHoldIntervalEnv] = "0"
	if l := newLongHorizonHoldLoop(getenv, nil, fakeRecon{}, discardLog()); l != nil {
		t.Fatal("\"0\" must disable the loop (nil) even with a reconciler")
	}
	// Explicit interval honored.
	env[longHorizonHoldIntervalEnv] = "3h"
	if l := newLongHorizonHoldLoop(getenv, nil, fakeRecon{}, discardLog()); l == nil || l.interval != 3*time.Hour {
		t.Fatalf("explicit interval not honored: %+v", l)
	}
}
