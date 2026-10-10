// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// NewOfficialCatalog is the catalog for both installation and host observation.
// Callers choose transport and signature verification, never a different tool set.
// It constructs adapters only; no network or executable is used here.
func NewOfficialCatalog(client *http.Client, verifier SignatureVerifier) *CapabilityCatalog {
	var v1 []Provider
	var v2 []PackageProviderV2
	for _, facts := range driverfacts.All() {
		switch facts.Installer {
		case "":
			continue
		case driverfacts.InstallManifest:
			v1 = append(v1, NewClaude(ClaudeOptions{Client: client, Verifier: verifier}))
		case driverfacts.InstallPackage:
			v2 = append(v2, NewGrok(GrokOptions{Client: client}))
		case driverfacts.InstallRelease:
			v2 = append(v2, newReleaseArchive(facts.Key, ReleaseArchiveOptions{Client: client}))
		default:
			panic(fmt.Sprintf("invalid built-in installer for %q: %q", facts.Key, facts.Installer))
		}
	}
	cat, err := NewCapabilityCatalog(NewCatalog(v1...), v2...)
	if err != nil {
		// A malformed built-in declaration is a programming error. Falling back
		// to a partial catalog would silently remove installed tools from reads.
		panic(fmt.Sprintf("invalid built-in tool catalog: %v", err))
	}
	return cat
}
