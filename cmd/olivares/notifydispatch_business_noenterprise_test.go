//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"
)

// A notify destination of a Business connector kind is skipped with a reason that names
// Business, not the "unknown connector kind" a typo gets: the operator must learn which
// edition would send it.
func TestCommunityNotifyBusinessKindSkipNamesBusiness(t *testing.T) {
	for _, kind := range []string{"siem", "pagerduty", "s3archive", "teamsbot"} {
		conn, _, skipped := openInProcessDestination(context.Background(), notifyDestinationSpec{Name: "d", Kind: kind}, nil, buildOutputConnector)
		if conn != nil || !strings.Contains(skipped, "Business") {
			t.Errorf("destination of %q: skipped=%q conn=%v, want a skip naming Business", kind, skipped, conn)
		}
	}
	_, _, skipped := openInProcessDestination(context.Background(), notifyDestinationSpec{Name: "d", Kind: "orcle"}, nil, buildOutputConnector)
	if strings.Contains(skipped, "Business") {
		t.Errorf("a typo's destination skip %q names Business", skipped)
	}
}
