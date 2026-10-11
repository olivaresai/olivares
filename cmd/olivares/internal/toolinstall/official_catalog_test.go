// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"testing"

	"github.com/olivaresai/olivares/core/driverfacts"
)

func TestOfficialCatalogFollowsDeclaredInstallers(t *testing.T) {
	e := NewEngineWithCapabilities(NewOfficialCatalog(nil, UnavailableVerifier{}), EngineOptions{})
	wantCount := 0
	for _, f := range driverfacts.All() {
		_, err := e.Capability(f.Key)
		if f.Installer == "" {
			if KindOf(err) != KindUnsupportedProvider {
				t.Fatalf("%s has no installer but catalog returned %v", f.Key, err)
			}
			continue
		}
		wantCount++
		if err != nil {
			t.Fatalf("%s declares an installer but catalog returned %v", f.Key, err)
		}
	}
	if got := e.DriverKeys(); len(got) != wantCount {
		t.Fatalf("catalog has undeclared tools: %v", got)
	}
}

func TestGeminiCLIIsInstallableFromTheOfficialCatalog(t *testing.T) {
	cat := NewOfficialCatalog(nil, UnavailableVerifier{})
	p, err := cat.LookupV2("gemini-cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key() != "gemini-cli" {
		t.Fatalf("Gemini installer must retain the official release checksum policy: %v", p)
	}
}
