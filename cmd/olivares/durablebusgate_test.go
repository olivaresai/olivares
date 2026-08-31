// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/sdk/event"
)

// getenvFrom returns a getenv backed by a map (no process env mutation).
func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestNewDurableBusCommunityUnset: with no durable config, the community stub is
// inert — (nil,nil) — so boot's bus selection falls through unchanged.
func TestNewDurableBusCommunityUnset(t *testing.T) {
	bus, err := newDurableBus(getenvFrom(nil), nil, nil, slog.Default(), "", "")
	if err != nil {
		t.Fatalf("unconfigured durable bus must not error: %v", err)
	}
	if bus != nil {
		t.Fatalf("unconfigured durable bus must be nil, got %T", bus)
	}
}

// TestNewDurableBusCommunityConfiguredFailsClosed: a community binary handed a
// durable config FAILS the boot (never silently runs non-durable). The error must
// name the env var and direct to the enterprise edition.
func TestNewDurableBusCommunityConfiguredFailsClosed(t *testing.T) {
	env := map[string]string{envDurableBusConfig: "/etc/olivares/durable-bus.json"}
	bus, err := newDurableBus(getenvFrom(env), map[event.Type]natsbus.PayloadDecoder{}, nil, slog.Default(), "", "")
	if err == nil {
		t.Fatal("community binary with a durable config must fail the boot, got nil error")
	}
	if bus != nil {
		t.Fatalf("a failed durable boot must return a nil bus, got %T", bus)
	}
	if !strings.Contains(err.Error(), envDurableBusConfig) || !strings.Contains(err.Error(), "enterprise") {
		t.Fatalf("error must name %s and the enterprise edition: %v", envDurableBusConfig, err)
	}
}
