// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"
)

// TestCheckinToolStateMapsEachRow pins the mapping to the contract's states. The order
// is load-bearing: a failed probe wins over installed-ness, and not-installed wins over
// a sign-in answer.
func TestCheckinToolStateMapsEachRow(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		row  toolRow
		want string
	}{
		{"signed in", toolRow{Installed: true, SignedIn: &yes}, "ready"},
		{"signed out", toolRow{Installed: true, SignedIn: &no}, "not_signed_in"},
		{"not installed", toolRow{Installed: false}, "not_installed"},
		{"not installed beats a sign-in answer", toolRow{Installed: false, SignedIn: &yes}, "not_installed"},
		{"no sign-in of its own", toolRow{Installed: true}, "unknown"},
		{"probe failed", toolRow{Installed: false, probeFailed: true}, "error"},
		{"probe failed beats installed", toolRow{Installed: true, probeFailed: true}, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkinToolState(tc.row); got != tc.want {
				t.Errorf("checkinToolState(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}
