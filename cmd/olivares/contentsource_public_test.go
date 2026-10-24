// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// The document source's "public" setting is the operator's explicit decision that
// documents the source reports without an ACL are visible to the whole tenant
// (#3030). Only the literal "true" counts: a typo stays denied.
func TestWrapContentSourceConfigPublic(t *testing.T) {
	type publicSource interface{ Public() bool }
	sources := map[string]contentsource.Source{
		"export wrapper": minimalContentSource{},
		"live wrapper":   buildMustSource(t, "gdrive"),
	}
	cases := []struct {
		name     string
		settings map[string]string
		want     bool
	}{
		{"unset", nil, false},
		{"mode only", map[string]string{"mode": "live"}, false},
		{"false", map[string]string{"public": "false"}, false},
		{"true", map[string]string{"public": "true"}, true},
		{"true with spaces", map[string]string{"public": " true "}, true},
		{"yes is not true", map[string]string{"public": "yes"}, false},
		{"upper case is not true", map[string]string{"public": "TRUE"}, false},
	}
	for kind, src := range sources {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				wrapped := wrapContentSourceConfig(src, tc.settings)
				p, ok := wrapped.(publicSource)
				if !ok {
					t.Fatalf("%T must expose Public()", wrapped)
				}
				if p.Public() != tc.want {
					t.Errorf("Public() = %v, want %v for %v", p.Public(), tc.want, tc.settings)
				}
				if m, ok := wrapped.(contentSourceMode); !ok || m.Mode() != normalizeContentSourceMode(tc.settings["mode"]) {
					t.Errorf("the mode must survive the public wrapper, got %v", wrapped)
				}
			})
		}
	}
}
