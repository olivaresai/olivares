// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/sdk/event"
)

// osGetenv falls back to the activation manifest, so a durable bus config can come from a
// manifest entry as well as the process environment. The Community refusal must send the
// operator to both places: "unset" alone leaves the manifest entry and the refusal returns.
func TestCommunityDurableBusRemedyNamesTheActivationManifest(t *testing.T) {
	env := map[string]string{envDurableBusConfig: "/etc/olivares/durable-bus.json"}
	_, err := communityDurableBus(getenvFrom(env), map[event.Type]natsbus.PayloadDecoder{}, nil, slog.Default(), "", "")
	if err == nil {
		t.Fatal("a set durable bus config must refuse in Community, got nil error")
	}
	for _, want := range []string{envDurableBusConfig, "environment", "activation manifest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the remedy does not mention %q: %v", want, err)
		}
	}
}
