// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestHostIsLoopback locks the secure-default bind classifier used to gate
// --seed-demo (a public-password superadmin must never be reachable off-host,
// docs/SECURITY-HARDENING.md). A wildcard bind is NOT loopback.
func TestHostIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8443", true},
		{"127.0.0.1", true},
		{"localhost:8443", true},
		{"[::1]:8444", true},
		{"0.0.0.0:8443", false}, // wildcard — all interfaces
		{":8443", false},        // empty host — all interfaces
		{"10.0.0.5:8443", false},
		{"192.168.1.10:8443", false},
		{"example.com:8443", false}, // a name we can't prove is loopback
		// A DNS label is case-insensitive, and since the plaintext refusal moved
		// into serveHTTP this classifier decides whether an auxiliary listener may
		// serve at all — an operator config spelling it in caps must not be read
		// as a public bind and refused.
		{"LOCALHOST:8443", true},
		{"LocalHost", true},
		// U+017F (ſ) is in the Unicode fold orbit of `s`, so strings.EqualFold
		// accepts this as "localhost". It is NOT that name, and this classifier
		// gates whether a listener may serve plaintext at all.
		{"localhoſt:8443", false},
		{"LOCALHOſT:8443", false},
	}
	for _, c := range cases {
		if got := hostIsLoopback(c.addr); got != c.want {
			t.Errorf("hostIsLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
