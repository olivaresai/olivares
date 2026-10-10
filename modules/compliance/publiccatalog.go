// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package compliance

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
	// published JSON can identify its source in the private Business build tree.
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
