// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

// PUBLIC CATALOG EXPORT — the data behind three URLs this module SEALS into
// documents an auditor reads.
//
// WHY THIS EXISTS, and it is not a marketing convenience. `oscal.go` stamps three
// absolute URLs into every exported OSCAL document:
//
//	oscal.go:109  source          = https://olivares.ai/compliance/frameworks/<id>
//	oscal.go:195  import-ap.href  = https://olivares.ai/compliance/assessment-plan/<id>
//	oscal.go:242  target-resource = https://olivares.ai/compliance/capabilities
//
// and `oscalprofile.go:159` makes the first one LOAD-BEARING in code:
// `FrameworkForHref` is its documented inverse, "deliberately exact (no fuzzy
// guessing — Decision #2)". An operator authoring a profile against our
// published catalog imports exactly that href.
//
// Measured 2026-08-27: all three were 404 in production. A sealed, signed evidence
// package whose catalog href does not resolve is the "amateur project" impression the
// canon forbids, and it is worse than a broken marketing link because the document
// outlives the page.
//
// So this file exports the catalog the URLs promise. It publishes ONLY what is
// already open-core: LICENSING.md §"Open-core (AGPL — modules/compliance)" names "the
// framework map, the OSCAL plumbing" as open. No tenant data, no evidence, no live
// status — those need a tenant and a license, and they are NOT in this document.
//
// The honesty line is carried, not restated: every framework's own Disclaimer and
// version Pin travel with it, plus the module-level reportDisclaimer. A capability's
// CLASS travels too, so a reader can tell "evidenced only by real tenant data" from
// "guaranteed by construction, cited to a design document".

// PublicCatalogVersion is the schema version of the exported document. Bump it when
// the SHAPE changes (a field added or removed), never when the catalog content
// changes — the website pins the shape, and the content is expected to move.
const PublicCatalogVersion = 1

// PublicCatalogURLs are the canonical public URLs this module stamps into OSCAL
// exports. They are exported as DATA so the website that has to serve them and the
// exporter that seals them cannot disagree silently: the generator writes them from
// the same constants the exporter uses, and the site's own gate compares its routes
// against this list.
type PublicCatalogURLs struct {
	// Frameworks is the prefix a framework catalog page hangs off (id appended).
	Frameworks string `json:"frameworks"`
	// AssessmentPlan is the prefix of the synthetic assessment-plan reference
	// (framework id appended). oscal.go discloses that we author no separate plan.
	AssessmentPlan string `json:"assessment_plan"`
	// Capabilities is the single capability reference-model page.
	Capabilities string `json:"capabilities"`
}

// PublicCatalogDoc is the whole exported document.
type PublicCatalogDoc struct {
	Description string `json:"$description"`
	// Version is PublicCatalogVersion — the SHAPE contract, not the content date.
	Version int `json:"version"`
	// Source names the files this document is derived from, so a reader of the
	// published JSON can find the truth in the open-source tree.
	Source []string `json:"source"`
	// Disclaimer is the module-level line stamped on every reporting response
	// (report.go): a technical control mapping, never a certification.
	Disclaimer string `json:"disclaimer"`
	// URLs are the canonical public URLs the OSCAL export seals.
	URLs PublicCatalogURLs `json:"urls"`
	// Capabilities is the FIXED capability vocabulary — the shared pivot every
	// framework maps to. Order is the catalog order (operational then architectural),
	// which is meaningful and therefore preserved.
	Capabilities []Capability `json:"capabilities"`
	// Frameworks is the ordered framework catalog, controls included.
	Frameworks []Framework `json:"frameworks"`
}

const publicCatalogDescription = "GENERATED — DO NOT EDIT (task compliance:catalog). " +
	"The Olivares.AI in-repo compliance control catalog and capability reference model, " +
	"exported for the three public URLs the OSCAL exporter seals into evidence packages " +
	"(modules/compliance/oscal.go). Technical control mapping for an AI control plane only: " +
	"NOT legal advice, NOT a certification, and NOT a statement of any tenant's compliance " +
	"status — live control status requires tenant evidence and never appears here."

// PublicCatalog returns the public, deterministic catalog document. Same tree → the
// same bytes: the two slices are copied in catalog order and nothing is sorted by a
// map, so `task compliance:catalog:check` can diff it.
func PublicCatalog() PublicCatalogDoc {
	caps := make([]Capability, len(capabilityCatalog))
	copy(caps, capabilityCatalog)
	fws := make([]Framework, 0, len(catalog))
	for _, fw := range catalog {
		// Copy the control slice: the exported document must not alias the live
		// catalog a running engine reads.
		controls := make([]Control, len(fw.Controls))
		copy(controls, fw.Controls)
		for i := range controls {
			// ⛔ A NIL SLICE MARSHALS AS `null`, NOT `[]`, and Control.Capabilities has
			// no omitempty — so a control with no mapped capability (an HONEST GAP, and
			// there are several by design) shipped `"capabilities": null`. Any consumer
			// doing the obvious `.length` on it crashes; the website's route test did,
			// which is how this was found, before the pages were ever built. An empty
			// list is the honest encoding of "we claim nothing evidences this", and it is
			// also the one no consumer can trip over. Normalized here, at the export, so
			// nobody downstream has to defend against it.
			if controls[i].Capabilities == nil {
				controls[i].Capabilities = []CapabilityKey{}
			}
		}
		fw.Controls = controls
		fws = append(fws, fw)
	}
	return PublicCatalogDoc{
		Description:  publicCatalogDescription,
		Version:      PublicCatalogVersion,
		Source:       []string{"modules/compliance/frameworks.go", "modules/compliance/capabilities.go"},
		Disclaimer:   reportDisclaimer,
		URLs:         PublicCatalogURLs{Frameworks: oscalSourcePrefix, AssessmentPlan: oscalAssessmentPlanPrefix, Capabilities: oscalCapabilitiesURL},
		Capabilities: caps,
		Frameworks:   fws,
	}
}
