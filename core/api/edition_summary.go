// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/license"
)

// EditionSummary is the one summary an administrator reads after an upgrade: the modules
// placed in a higher edition than the running one that run on this node. It changes nothing.
type EditionSummary struct {
	// Edition is the running edition: the lower of the build and the live license.
	Edition string `json:"edition"`
	// Needs groups the running modules by the edition each needs, lowest edition first.
	Needs []EditionNeed `json:"needs"`
}

// EditionNeed lists what needs one edition.
type EditionNeed struct {
	Edition      string              `json:"edition"`
	Capabilities []EditionCapability `json:"capabilities"`
}

// EditionCapability is one running module placed above the running edition.
type EditionCapability struct {
	Capability string `json:"capability"`
	Title      string `json:"title"`
}

// runningEdition is the edition this node serves: a Community build is always Community, a
// licensed build serves what its live license entitles (Community without one).
func (s *Server) runningEdition() string {
	if s.edition == "" || s.edition == license.EditionCommunity {
		return license.EditionCommunity
	}
	if licensed := s.licenseStatus().LicensedEdition; licensed != "" {
		return licensed
	}
	return license.EditionCommunity
}

// handleEditionSummary reads, live, which placed modules run above the running edition. Their
// stored data stays readable and exportable (reads_below); only authoring stops.
func (s *Server) handleEditionSummary(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	have := s.runningEdition()
	byEdition := map[string][]EditionCapability{}
	for _, m := range s.modules {
		ns := m.APINamespace()
		spec, placed := placedModules[ns]
		if !placed || !s.moduleEnabled(ns) || license.EditionCovers(have, spec.Edition) {
			continue
		}
		byEdition[spec.Edition] = append(byEdition[spec.Edition], EditionCapability{Capability: ns, Title: spec.Title})
	}
	needs := []EditionNeed{}
	for _, edition := range []string{license.EditionEngineer, license.EditionBusiness, license.EditionEnterprise} {
		caps := byEdition[edition]
		if len(caps) == 0 {
			continue
		}
		slices.SortFunc(caps, func(a, b EditionCapability) int { return strings.Compare(a.Capability, b.Capability) })
		needs = append(needs, EditionNeed{Edition: edition, Capabilities: caps})
	}
	writeJSON(w, http.StatusOK, EditionSummary{Edition: have, Needs: needs})
}
