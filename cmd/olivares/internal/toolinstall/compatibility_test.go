// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// An installed release outside the range this engine supports says so in the inventory,
// with the range in the sentence; one inside it is marked supported. Both stay listed and
// installed: the state informs and never refuses.
func TestListSaysWhenAReleaseIsOutsideTheSupportedRange(t *testing.T) {
	facts, _ := driverfacts.Lookup(DriverCodex)
	inside := facts.SupportedFrom
	const outside = "0.1.0"
	s := newTLSMap(t)
	root := filepath.Join(toolinstalltest.ExecCapableDir(t), "tools")
	c := NewCodex(CodexOptions{BaseURL: s.url(), Client: s.client(), Verifier: HashMatchingVerifier{}, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	cat, err := NewCapabilityCatalog(NewCatalog(), c)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngineWithCapabilities(cat, EngineOptions{InstallerVersion: "test"})
	for _, version := range []string{outside, inside} {
		publishCodexPackage(t, s, version)
		req := RequestV2{Driver: DriverCodex, Version: version, Platform: PlatformV2{OS: "linux", Arch: "amd64", Libc: "musl"}, DestRoot: root, Source: s.url()}
		if _, _, err := eng.InstallV2(context.Background(), req, nil, nil); err != nil {
			t.Fatalf("install %s: %v", version, err)
		}
	}
	inv, err := eng.List(context.Background(), root)
	if err != nil || len(inv.Installed) != 2 {
		t.Fatalf("list %+v %v", inv, err)
	}
	for _, in := range inv.Installed {
		if in.State != StateInstalled {
			t.Fatalf("%s: state %q, the compatibility state must not change it", in.Version, in.State)
		}
		switch in.Version {
		case outside:
			if in.Compatibility != "unsupported" || !strings.Contains(in.CompatibilityNote, inside) {
				t.Fatalf("%s: compatibility %q note %q, want unsupported naming the range from %s", in.Version, in.Compatibility, in.CompatibilityNote, inside)
			}
		case inside:
			if in.Compatibility != "supported" || in.CompatibilityNote != "" {
				t.Fatalf("%s: compatibility %q note %q, want supported and no note", in.Version, in.Compatibility, in.CompatibilityNote)
			}
		}
	}
}
