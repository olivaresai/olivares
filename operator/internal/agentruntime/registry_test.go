// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agentruntime

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestKnownCRDs_NamesPresentAllPreStable asserts the tracked CRDs are present and
// that EVERY entry is flagged not-stable with a non-empty source-cited caveat.
func TestKnownCRDs_NamesPresentAllPreStable(t *testing.T) {
	crds := KnownCRDs()
	if len(crds) == 0 {
		t.Fatal("registry is empty")
	}

	// Every entry must be honest: Stable=false + a caveat + a VerifiedAt stamp.
	for _, c := range crds {
		if c.Stable {
			t.Errorf("%s/%s %s is marked Stable=true; the registry must assert pre-stable only", c.Group, c.Version, c.Kind)
		}
		if c.Caveat == "" {
			t.Errorf("%s/%s %s has empty Caveat", c.Group, c.Version, c.Kind)
		}
		if c.VerifiedAt == "" {
			t.Errorf("%s/%s %s has empty VerifiedAt", c.Group, c.Version, c.Kind)
		}
	}

	// The specific, verified facts must be present (kind+group), so a silent
	// upstream-name drift in our own table is caught.
	want := []struct{ group, kind string }{
		{"agents.x-k8s.io", "Sandbox"},
		{"agents.x-k8s.io", "SandboxTemplate"},
		{"agents.x-k8s.io", "SandboxClaim"},
		{"agents.x-k8s.io", "SandboxWarmPool"},
		{"kagent.dev", "Agent"},
		{"kagent.dev", "ModelConfig"},
		{"kagent.dev", "MCPServer"},
		{"kagent.dev", "RemoteMCPServer"},
		{"kagent.dev", "Memory"},
		{"kagent.dev", "SandboxAgent"},
		{"kagent.dev", "ToolServer"},
	}
	for _, w := range want {
		if !hasGroupKind(crds, w.group, w.kind) {
			t.Errorf("tracked CRD %s/%s missing from registry", w.group, w.kind)
		}
	}

	// kagent migrated off kagent.io/v1alpha1 -> kagent.dev/v1alpha2; assert we
	// are not accidentally still tracking the old group.
	for _, c := range crds {
		if c.Group == "kagent.io" {
			t.Errorf("registry still tracks the OLD kagent.io group: %+v", c)
		}
		if c.Group == "kagent.dev" && c.Version != "v1alpha2" {
			t.Errorf("kagent CRD %s tracked at version %q, want v1alpha2", c.Kind, c.Version)
		}
	}
}

func TestKnownCRDsForProject(t *testing.T) {
	if got := len(KnownCRDsForProject("agent-sandbox")); got != 4 {
		t.Errorf("agent-sandbox CRD count = %d, want 4", got)
	}
	if got := len(KnownCRDsForProject("kagent")); got == 0 {
		t.Errorf("kagent CRD count = 0, want > 0")
	}
	if got := len(KnownCRDsForProject("nonexistent")); got != 0 {
		t.Errorf("nonexistent project returned %d CRDs, want 0", got)
	}
}

func TestKnownCRDs_ReturnsCopy(t *testing.T) {
	a := KnownCRDs()
	a[0].Kind = "MUTATED"
	b := KnownCRDs()
	if b[0].Kind == "MUTATED" {
		t.Error("KnownCRDs() leaks the backing slice; callers can mutate the registry")
	}
}

// fakeLister implements GroupKindLister for the opt-in presence test.
type fakeLister struct {
	groups []string
	err    error
}

func (f fakeLister) ServerGroups() (*metav1.APIGroupList, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &metav1.APIGroupList{}
	for _, g := range f.groups {
		out.Groups = append(out.Groups, metav1.APIGroup{Name: g})
	}
	return out, nil
}

func TestDiscoverInstalled(t *testing.T) {
	// Cluster has kagent.dev installed but not agents.x-k8s.io.
	l := fakeLister{groups: []string{"kagent.dev", "apps", ""}}
	reports, err := DiscoverInstalled(context.Background(), l)
	if err != nil {
		t.Fatalf("DiscoverInstalled: %v", err)
	}
	if len(reports) != len(KnownCRDs()) {
		t.Fatalf("report count = %d, want %d", len(reports), len(KnownCRDs()))
	}
	for _, r := range reports {
		switch r.CRD.Group {
		case "kagent.dev":
			if !r.Installed {
				t.Errorf("%s should be reported installed (group present)", r.CRD.Kind)
			}
		case "agents.x-k8s.io":
			if r.Installed {
				t.Errorf("%s should NOT be installed (group absent)", r.CRD.Kind)
			}
		}
	}
}

func TestDiscoverInstalled_NilClient(t *testing.T) {
	if _, err := DiscoverInstalled(context.Background(), nil); err == nil {
		t.Error("expected error for nil discovery client (opt-in must be explicit)")
	}
}

func TestDiscoverInstalled_DiscoveryError(t *testing.T) {
	l := fakeLister{err: errors.New("boom")}
	if _, err := DiscoverInstalled(context.Background(), l); err == nil {
		t.Error("expected discovery error to propagate")
	}
}

func hasGroupKind(crds []KnownCRD, group, kind string) bool {
	for _, c := range crds {
		if c.Group == group && c.Kind == kind {
			return true
		}
	}
	return false
}
