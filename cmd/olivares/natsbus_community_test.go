// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package main

import (
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"strings"
	"testing"
)

func TestCommunityNATSConstructorUnavailable(t *testing.T) {
	bus, err := natsbus.New(natsbus.Config{URL: "nats://127.0.0.1:1"}, natsbus.Options{})
	if bus != nil || err == nil || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("paid bridge=%T,%v", bus, err)
	}
}
