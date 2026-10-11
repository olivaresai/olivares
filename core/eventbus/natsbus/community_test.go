// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package natsbus

import (
	"strings"
	"testing"
)

func TestCommunityNATSBridgeUnavailable(t *testing.T) {
	b, err := New(Config{Backend: "nats", URL: "nats://127.0.0.1:1"}, Options{})
	if b != nil {
		_ = b.Close()
	}
	if b != nil || err == nil || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("Community constructed a NATS bridge: bus=%T, err=%v", b, err)
	}
}
