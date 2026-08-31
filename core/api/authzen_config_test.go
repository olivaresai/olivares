// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "testing"

// A nil config is the default: no gate (fully enabled, no network restriction).
func TestBuildAuthzenGateNil(t *testing.T) {
	g, err := buildAuthzenGate(nil)
	if err != nil || g != nil {
		t.Fatalf("buildAuthzenGate(nil) = (%v, %v), want (nil, nil)", g, err)
	}
	// Nil-gate accessors report enabled (used by the discovery handler).
	if !g.searchEnabled() || !g.exportEnabled() {
		t.Error("a nil gate must report search/export enabled")
	}
}

// A malformed CIDR fails at build time (an embedder must not silently get open exposure).
func TestBuildAuthzenGateBadCIDR(t *testing.T) {
	if _, err := buildAuthzenGate(&AuthZenConfig{AllowedCIDRs: []string{"not-a-cidr"}}); err == nil {
		t.Error("a malformed CIDR must make buildAuthzenGate fail")
	}
}

func TestAuthzenGatePeerAllowed(t *testing.T) {
	g, err := buildAuthzenGate(&AuthZenConfig{AllowedCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cases := []struct {
		peer string
		want bool
	}{
		{"10.0.0.1:1234", true},
		{"192.168.5.5:9", true},
		{"172.16.0.1:1", false},
		{"10.0.0.1", true}, // host without port
		{"garbage", false}, // unparseable ⇒ deny-closed
	}
	for _, c := range cases {
		if got := g.peerAllowed(c.peer); got != c.want {
			t.Errorf("peerAllowed(%q) = %v, want %v", c.peer, got, c.want)
		}
	}
}

func TestAuthzenGateEnabledAccessors(t *testing.T) {
	g, _ := buildAuthzenGate(&AuthZenConfig{SearchDisabled: true, ExportDisabled: true})
	if g.searchEnabled() {
		t.Error("searchEnabled must be false when SearchDisabled")
	}
	if g.exportEnabled() {
		t.Error("exportEnabled must be false when ExportDisabled")
	}
}
