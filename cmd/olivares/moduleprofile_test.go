// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"testing"
)

// A selection runs what it names, what those modules require, and the kernel.
func TestModuleProfileRunsTheSelectionItsRequirementsAndTheKernel(t *testing.T) {
	for _, tc := range []struct {
		selected []string
		active   []string
	}{
		{nil, []string{"governance", "liveingest", "sessions"}},
		{[]string{"orchestration"}, []string{"finops", "governance", "liveingest", "notify", "orchestration", "sessions"}},
		{[]string{"siemforward"}, []string{"eventing", "governance", "liveingest", "sessions", "siemforward"}},
		{[]string{"inferenceproxy"}, []string{"compliance", "finops", "governance", "inferenceproxy", "knowledge", "liveingest", "models", "sessions", "sourcescope"}},
		{[]string{"sandbox", "sandbox"}, []string{"evals", "governance", "liveingest", "sandbox", "sessions"}},
	} {
		p, err := resolveModuleProfile(tc.selected)
		if err != nil {
			t.Fatalf("%v: %v", tc.selected, err)
		}
		if got := p.ActiveNames(); !slices.Equal(got, tc.active) {
			t.Errorf("%v: active = %v, want %v", tc.selected, got, tc.active)
		}
	}
}

func TestModuleProfileRefusesAnUnknownModule(t *testing.T) {
	if _, err := resolveModuleProfile([]string{"sessions", "teleport"}); err == nil {
		t.Fatal("an unknown module name was accepted")
	}
}

// The profile only turns off modules it knows; edition and host modules run.
func TestModuleProfileKeepsModulesOutsideTheCatalogActive(t *testing.T) {
	p, err := resolveModuleProfile(nil)
	if err != nil {
		t.Fatal(err)
	}
	for ns, want := range map[string]bool{"session-cockpit": true, "agenttools": true, "eventing": false, "governance": true} {
		if got := p.Active(ns); got != want {
			t.Errorf("Active(%q) = %v, want %v", ns, got, want)
		}
	}
}

// An administrator sees why a module they did not select still runs.
func TestModuleProfileNamesWhatRequiresAModule(t *testing.T) {
	p, err := resolveModuleProfile([]string{"orchestration", "voice"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.requiredBy("finops"); !slices.Equal(got, []string{"orchestration", "voice"}) {
		t.Errorf("requiredBy(finops) = %v", got)
	}
	if got := p.Selected(); !slices.Equal(got, []string{"orchestration", "voice"}) {
		t.Errorf("Selected = %v", got)
	}
}

// Every requirement names a catalog module, and every catalog module resolves.
func TestModuleCatalogRequirementsNameCatalogModules(t *testing.T) {
	for name, spec := range moduleCatalog {
		for _, req := range spec.requires {
			if _, ok := moduleCatalog[req]; !ok {
				t.Errorf("%s requires %q, which is not in the catalog", name, req)
			}
		}
		if _, err := resolveModuleProfile([]string{name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := resolveModuleProfile(allModuleSelection()); err != nil {
		t.Fatal(err)
	}
}

// An engine composed without a profile (tests, embedders) runs every module.
func TestZeroModuleProfileRunsEveryModule(t *testing.T) {
	var p moduleProfile
	for name := range moduleCatalog {
		if !p.Active(name) {
			t.Fatalf("zero profile turned %s off", name)
		}
	}
}
