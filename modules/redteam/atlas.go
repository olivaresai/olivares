// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import "sort"

// This file is the MITRE ATLAS version stamp + dated coverage view. The
// battery's technique ids were a static inline snapshot; this pins the matrix RELEASE
// they were verified against and exposes the coverage the catalog already computes as
// a DATED view, so an auditor sees both the ids and when they were last reconciled.
//
// SOURCE (verified, jun-2026): MITRE ATLAS — Adversarial Threat Landscape for AI
// Systems. Live matrix https://atlas.mitre.org/. The authoritative data that GENERATES
// that site is https://github.com/mitre-atlas/atlas-data — release 2026.05 (data
// format v6.0.0), file dist/v6/ATLAS-2026.05.yaml, release-date 2026-05-27. Every id
// and title below was read from that release; none is invented.
//
// HONESTY (binding): we do NOT assert a guaranteed update cadence. MITRE's
// atlas-data CHANGELOG states content releases "follow a YYYY.MM.N versioning scheme" —
// a versioning intent, not an SLA — and the historical release interval is not
// uniformly monthly. So we stamp ONLY the verified version + as-of date, and the view
// is explicitly a dated snapshot, never a claim of continuous parity with the live site.
const (
	// ATLASVersion is the atlas-data content release the battery ids were verified against.
	ATLASVersion = "2026.05"
	// ATLASVersionAsOf is that release's date (release-date in dist/manifest.yaml).
	ATLASVersionAsOf = "2026-05-27"
	// ATLASDataFormat is the atlas-data format version of that release.
	ATLASDataFormat = "v6.0.0"
	atlasMatrixURL  = "https://atlas.mitre.org/"
)

// atlasTechniques maps each ATLAS technique id the battery references to its VERIFIED
// canonical title (read verbatim from the atlas-data 2026.05 release — note T0024 is
// "AI" not "ML" Inference API in the current matrix). It names a technique in the
// coverage view rather than showing a bare id, and is the allow-list of ids the
// battery is permitted to cite (an id with no entry here is surfaced with an empty
// title so an un-reconciled id is visible, never silently legitimized).
var atlasTechniques = map[string]string{
	"AML.T0024":     "Exfiltration via AI Inference API",
	"AML.T0051.000": "LLM Prompt Injection: Direct",
	"AML.T0051.001": "LLM Prompt Injection: Indirect",
	"AML.T0054":     "LLM Jailbreak",
	"AML.T0056":     "Extract LLM System Prompt",
	"AML.T0057":     "LLM Data Leakage",
	// 2026 agent techniques (new in the current matrix; verified against atlas-data 2026.05).
	"AML.T0104": "Publish Poisoned AI Agent Tool",
	"AML.T0105": "Escape to Host",
	"AML.T0110": "AI Agent Tool Poisoning",
}

// atlasTechniqueDTO is one technique in the dated coverage view.
type atlasTechniqueDTO struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Probes int    `json:"probes"`
}

// atlasCoverageDTO is the DATED ATLAS coverage view: the matrix release the
// ids were verified against, the as-of date and source, and the per-technique probe
// counts derived from the battery's already-computed ATLASCovered.
type atlasCoverageDTO struct {
	Version    string              `json:"version"`
	AsOf       string              `json:"as_of"`
	DataFormat string              `json:"data_format"`
	Source     string              `json:"source"`
	Techniques []atlasTechniqueDTO `json:"techniques"`
	Note       string              `json:"note"`
}

// atlasCoverage builds the dated coverage view from the battery's covered map
// (id→probe count). Ids are emitted sorted; an id without a verified title is still
// listed (empty title) so coverage never silently hides a technique.
func atlasCoverage(covered map[string]int) atlasCoverageDTO {
	ids := make([]string, 0, len(covered))
	for id := range covered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	techs := make([]atlasTechniqueDTO, 0, len(ids))
	for _, id := range ids {
		techs = append(techs, atlasTechniqueDTO{ID: id, Title: atlasTechniques[id], Probes: covered[id]})
	}
	return atlasCoverageDTO{
		Version:    ATLASVersion,
		AsOf:       ATLASVersionAsOf,
		DataFormat: ATLASDataFormat,
		Source:     atlasMatrixURL,
		Techniques: techs,
		Note: "ATLAS technique ids verified against the published atlas-data " + ATLASVersion +
			" release (as of " + ATLASVersionAsOf + "); a dated snapshot, not a claim of continuous parity with the live matrix.",
	}
}
