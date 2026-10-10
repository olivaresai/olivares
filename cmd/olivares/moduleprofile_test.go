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
		{nil, []string{"governance", "sessions"}},
		{[]string{"orchestration"}, []string{"finops", "governance", "notify", "orchestration", "sessions"}},
		{[]string{"siemforward"}, []string{"eventing", "governance", "sessions", "siemforward"}},
		{[]string{"inferenceproxy"}, []string{"accessmap", "compliance", "finops", "governance", "inferenceproxy", "knowledge", "models", "sessions", "sourcescope"}},
		{[]string{"sandbox", "sandbox"}, []string{"evals", "governance", "sandbox", "sessions"}},
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
		for _, req := range spec.Requires {
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

// Consumers keep their data/event owners active even when the administrator did
// not select those owners; the console explains why and releases them afterwards.
func TestModuleProfileKeepsDataAndEventOwnersActive(t *testing.T) {
	for _, tc := range []struct {
		selected   []string
		owner      string
		requiredBy []string
	}{
		{[]string{"compliance"}, "accessmap", []string{"compliance"}},
		{[]string{"posture"}, "accessmap", []string{"posture"}},
		{[]string{"compliance", "posture"}, "accessmap", []string{"compliance", "posture"}},
		{[]string{"reporting"}, "accessmap", []string{"compliance"}},
	} {
		t.Run(tc.owner+"/"+tc.selected[0], func(t *testing.T) {
			p, err := resolveModuleProfile(tc.selected)
			if err != nil {
				t.Fatal(err)
			}
			if !p.Active(tc.owner) {
				t.Errorf("%s dormant with %v selected", tc.owner, tc.selected)
			}
			if got := p.requiredBy(tc.owner); !slices.Equal(got, tc.requiredBy) {
				t.Errorf("requiredBy(%s) = %v, want %v", tc.owner, got, tc.requiredBy)
			}
			if slices.Contains(p.Selected(), tc.owner) {
				t.Errorf("dependency %s added to saved selection", tc.owner)
			}
			empty, err := resolveModuleProfile(nil)
			if err != nil {
				t.Fatal(err)
			}
			if empty.Active(tc.owner) {
				t.Errorf("%s still active without a consumer", tc.owner)
			}
		})
	}
}

// The 26.10.1 upgrade keeps liveingest on (sessions forced it there); that must
// not start voice or finops, which that release did not run.
func TestModuleProfileLiveIngestDoesNotStartVoiceOrFinops(t *testing.T) {
	p, err := resolveModuleProfile([]string{"capabilities", "claude-policy", "consoleviews", "identity", "liveingest"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"voice", "finops"} {
		if p.Active(name) {
			t.Errorf("%s runs because liveingest is selected", name)
		}
	}
}
