// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modulespec

import (
	"slices"
	"testing"
)

func TestModuleSpecIsUnambiguous(t *testing.T) {
	specs := All()
	if len(specs) == 0 {
		t.Fatal("empty module spec")
	}
	names, namespaces, retirement := map[string]bool{}, map[string]bool{}, map[int]bool{}
	views := map[string]string{}
	for _, spec := range specs {
		for _, view := range spec.ConsoleEntries {
			if view == "" || views[view] != "" {
				t.Errorf("%s names console view %q, already named by %q", spec.Namespace, view, views[view])
			}
			views[view] = spec.Namespace
		}
		if spec.Namespace == "" || spec.Name == "" || spec.Constructor == "" || spec.Package == "" || spec.Edition == "" {
			t.Fatalf("incomplete spec: %+v", spec)
		}
		if names[spec.Name] || namespaces[spec.Namespace] {
			t.Fatalf("duplicate module: %+v", spec)
		}
		names[spec.Name], namespaces[spec.Namespace] = true, true
		if spec.Kind != "kernel" && spec.Kind != "catalog" {
			t.Errorf("%s has invalid kind %q", spec.Namespace, spec.Kind)
		}
		if spec.RetirementOrder > 0 {
			if retirement[spec.RetirementOrder] {
				t.Errorf("duplicate retirement order %d", spec.RetirementOrder)
			}
			retirement[spec.RetirementOrder] = true
		}
		switch spec.DeliveryClass {
		case "":
			if spec.DeliveryName != "" {
				t.Errorf("%s has a delivery name without a class", spec.Namespace)
			}
		case "state", "telemetry":
			if spec.DeliveryName == "" {
				t.Errorf("%s has no delivery name", spec.Namespace)
			}
		default:
			t.Errorf("%s has unknown delivery class %q", spec.Namespace, spec.DeliveryClass)
		}
	}
	for _, spec := range specs {
		for _, required := range spec.Requires {
			if !namespaces[required] {
				t.Errorf("%s requires unknown module %q", spec.Namespace, required)
			}
		}
	}
}

// Kernel classification is independent of the selectable catalog: the edition
// cockpit stays outside the profile, while governance and sessions remain listed.
func TestModuleSpecKernel(t *testing.T) {
	var kernel []string
	for _, spec := range All() {
		if spec.Kind == "kernel" {
			kernel = append(kernel, spec.Namespace)
			if spec.Selectable != (spec.Namespace != "session-cockpit") {
				t.Errorf("%s: selectable = %v", spec.Namespace, spec.Selectable)
			}
		}
	}
	slices.Sort(kernel)
	if want := []string{"governance", "session-cockpit", "sessions"}; !slices.Equal(kernel, want) {
		t.Fatalf("kernel = %v, want %v", kernel, want)
	}
}

func TestModuleSpecRequiresDataAndEventOwners(t *testing.T) {
	for _, tc := range []struct{ consumer, owner string }{
		{"compliance", "accessmap"}, // ReconciledDrift reads the live access graph.
		{"posture", "accessmap"},    // The posture export reads the same graph.
		{"posture", "inventory"},    // Preserve the existing inventory dependency.
	} {
		t.Run(tc.consumer+"/"+tc.owner, func(t *testing.T) {
			for _, spec := range All() {
				if spec.Namespace == tc.consumer {
					if !slices.Contains(spec.Requires, tc.owner) {
						t.Errorf("%s requires %v, missing %s", tc.consumer, spec.Requires, tc.owner)
					}
					return
				}
			}
			t.Fatalf("missing consumer %s", tc.consumer)
		})
	}
}

// liveingest has no production voice producer, so it must not pull voice (and
// through it finops) into an installation that did not select them.
func TestModuleSpecLiveIngestDoesNotRequireVoice(t *testing.T) {
	for _, spec := range All() {
		if spec.Namespace == "liveingest" {
			if slices.Contains(spec.Requires, "voice") {
				t.Errorf("liveingest requires %v", spec.Requires)
			}
			return
		}
	}
	t.Fatal("missing liveingest")
}
