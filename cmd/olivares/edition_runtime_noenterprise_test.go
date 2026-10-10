// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"reflect"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/modules/sessioncockpit"
)

// Community is the zero value of editionPorts plus its real behaviors. A port filled
// here would be a Community stand-in for a Business capability; a behavior dropped
// here would leave a caller with a nil it never handled.
func TestCommunityEditionIsTheZeroValuePlusItsBehaviors(t *testing.T) {
	want := []string{"name", "seatPolicy", "upstreamCredentialProvider", "durableBus",
		"moduleRegistrars", "circuitBreakerDeclarations"}
	ports := reflect.ValueOf(editionPortsForBuild())
	var filled []string
	for i := range ports.NumField() {
		if !ports.Field(i).IsZero() {
			filled = append(filled, ports.Type().Field(i).Name)
		}
	}
	slices.Sort(filled)
	slices.Sort(want)
	if !slices.Equal(filled, want) {
		t.Fatalf("Community fills %v, want exactly %v", filled, want)
	}
	if thisEdition.name != "community" {
		t.Fatalf("edition name %q, want community", thisEdition.name)
	}
	mods := thisEdition.moduleRegistrars(EditionConfig{})
	if len(mods) != 1 {
		t.Fatalf("Community mounts %d edition modules, want only the placeholder", len(mods))
	}
	if _, ok := mods[0].(*sessioncockpit.Placeholder); !ok {
		t.Fatalf("Community edition module is %T, want the session-cockpit placeholder", mods[0])
	}
	if limit, ok := thisEdition.seatPolicy(nil, nil).MaxActiveUsers(); ok || limit > 0 {
		t.Fatalf("Community seat policy reports %d (ok=%v), want unlimited", limit, ok)
	}
	provider, ok := thisEdition.upstreamCredentialProvider("Bearer static").(*staticCredentialProvider)
	if !ok || provider.authHeader != "Bearer static" {
		t.Fatalf("Community upstream credential provider %#v, want the static header", provider)
	}
}
