// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"testing"
	"time"
)

// An expiry that cannot be read never lifts a forbid: the damaged row grants nothing and
// keeps restricting, like every other reader of the column.
func TestBindingActiveFailsClosedOnUnreadableExpiry(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		b    binding
		want bool
	}{
		{"allow without expiry", binding{effect: effectAllow}, true},
		{"allow before expiry", binding{effect: effectAllow, expiresAt: "2026-10-30T00:00:00Z"}, true},
		{"allow after expiry", binding{effect: effectAllow, expiresAt: "2026-10-29T11:00:00Z"}, false},
		{"forbid after expiry lapses", binding{effect: effectForbid, expiresAt: "2026-10-29T11:00:00Z"}, false},
		{"allow with an unreadable expiry grants nothing", binding{effect: effectAllow, expiresAt: "soon"}, false},
		{"forbid with an unreadable expiry keeps forbidding", binding{effect: effectForbid, expiresAt: "soon"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.active(now); got != tc.want {
				t.Fatalf("active = %v, want %v", got, tc.want)
			}
		})
	}
}
