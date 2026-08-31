// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// SPDX 3.0.1 AI Profile export, ALONGSIDE the CycloneDX 1.6 AIBOM: the
// same governed inventory (model versions, lineage datasets, signed-admission
// verdicts) rendered as an SPDX 3.0.1 JSON-LD document with ai_AIPackage and
// dataset_DatasetPackage elements, for consumers standardized on SPDX rather than
// CycloneDX. Read-only export; the SEALED, ledger-anchored artifact remains the
// CycloneDX AIBOM (one canonical seal, two serializations would mean two truths).
//
// The shape is mirrored faithfully from the SPDX 3.0.1 release (verified 2026-06-10
// against spdx.github.io/spdx-spec/v3.0.1 + the published JSON-LD context):
//   - envelope: {"@context": spdx-context.jsonld, "@graph": [elements]} with a shared
//     CreationInfo blank node "_:creationinfo" (specVersion/created/createdBy) and
//     absolute-IRI spdxIds; vocab values serialize as bare entry names.
//   - non-Core property names carry the profile prefix (software_*, ai_*, dataset_*);
//     Core properties (name, releaseTime, suppliedBy, comment, verifiedUsing) are bare.
//   - AI profile restrictions make releaseTime, suppliedBy, software_downloadLocation,
//     software_packageVersion and software_primaryPurpose REQUIRED on ai_AIPackage;
//     dataset_datasetType (1..*), builtTime, originatedBy, releaseTime,
//     software_downloadLocation and software_primaryPurpose are REQUIRED on
//     dataset_DatasetPackage.
//   - relationships are standalone Relationship elements; trainedOn = "the `from`
//     Element has been trained on the `to` Element(s)".
//
// HONESTY (docs/SECURITY-HARDENING.md): required fields the inventory does not record are filled with
// the spec's own no-assertion vocabulary entries where one exists (datasetType
// "noAssertion"), an explicitly named "NOASSERTION" agent, or a urn:olivares:…:
// not-recorded IRI — each disclosed in the element's comment — NEVER with invented
// values. Registry registration time stands in for releaseTime/builtTime and says so.
// MINIMAL-DATA: metadata/refs/digests only, never weights or dataset contents.

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const (
	// spdxSpecVersion pins the export to SPDX 3.0.1 (2024-12-17, the current release
	// as of 2026-06-10; 3.1 is at RC1 and will change the context URL pattern — keep
	// the version in one place).
	spdxSpecVersion = "3.0.1"
	spdxContextURL  = "https://spdx.org/rdf/3.0.1/spdx-context.jsonld"

	spdxDisclaimer = "SPDX " + spdxSpecVersion + " AI Profile export of the governed model inventory: identity, versions, lineage datasets, content digests and signed-admission verdicts. Required fields the inventory does not record are filled with the spec's no-assertion forms and disclosed per element — never invented. Read-only serialization; the sealed, ledger-anchored artifact is the CycloneDX AIBOM. NOT a certification (docs/08 §9)."

	spdxCreationInfoRef = "_:creationinfo"
	spdxNotRecorded     = "registry registration time stands in for the publisher timestamp (not recorded); "
)

// spdxIRI builds an absolute IRI for an element from its registry identity. The
// variable segment is percent-encoded: record IDs are UUIDv7 (already IRI-safe) but
// provider refs are free text, and a space would make the IRI RFC 3987-invalid.
func spdxIRI(kind, id string) string {
	return "urn:olivares:spdx:" + kind + ":" + url.PathEscape(id)
}

// spdxTime normalizes a stored timestamp to the strict SPDX DateTime form
// (YYYY-MM-DDTHH:MM:SSZ, UTC, no fractional seconds); ok=false when unparseable.
func spdxTime(s string) (string, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05Z"), true
		}
	}
	return "", false
}

// spdxDownloadLocation returns the recorded artifact/source ref when it is already a
// URI, else an explicit not-recorded URN (the field is REQUIRED on both package
// classes; an honest placeholder beats an invented URL).
func spdxDownloadLocation(ref, kind, id string) (string, bool) {
	r := strings.TrimSpace(ref)
	if r != "" && (strings.Contains(r, "://") || strings.HasPrefix(strings.ToLower(r), "urn:")) {
		return r, true
	}
	return "urn:olivares:not-recorded:download-location:" + kind + ":" + id, false
}

// spdxConfidentiality maps the registry's dataset classification onto the SPDX
// dataset_ConfidentialityLevelType TLP vocabulary (red/amber/green/clear); ok=false
// for classes with no honest TLP equivalent. The mapping never LOOSENS the recorded
// boundary: org-internal data maps to amber (org + need-to-know), not green
// (community-wide sharing) — an honesty-first export must not over-disclose.
func spdxConfidentiality(classification string) (string, bool) {
	switch classification {
	case "restricted":
		return "red", true
	case "confidential", "pii", "internal":
		return "amber", true
	case "public":
		return "clear", true
	default:
		return "", false
	}
}

// spdxHash returns a verifiedUsing Hash node for a sha256 hex digest, or ok=false
// when the digest is not a 64-char hex string (omitted rather than invalid).
func spdxHash(alg, digest string) (map[string]any, bool) {
	a := strings.ToLower(strings.TrimSpace(alg))
	if a == "" || a == "sha-256" {
		a = "sha256"
	}
	if a != "sha256" {
		return nil, false
	}
	d := strings.ToLower(strings.TrimSpace(digest))
	if !isHexDigest(d) || len(d) != 64 {
		return nil, false
	}
	return map[string]any{"type": "Hash", "algorithm": "sha256", "hashValue": d}, true
}

// buildSPDXAIDocument renders the SPDX 3.0.1 AI Profile JSON-LD document from the
// shared inventory read (the same data the CycloneDX AIBOM and the model card render).
func buildSPDXAIDocument(inv modelInventory, generatedAt string) map[string]any {
	created, ok := spdxTime(generatedAt)
	if !ok {
		created = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}

	toolIRI := spdxIRI("agent", "olivares-models")
	graph := []any{
		map[string]any{
			"type": "CreationInfo", "@id": spdxCreationInfoRef,
			"specVersion": spdxSpecVersion, "created": created, "createdBy": []any{toolIRI},
		},
		map[string]any{
			"type": "SoftwareAgent", "spdxId": toolIRI, "creationInfo": spdxCreationInfoRef,
			"name": "olivares.models (Olivares AI control plane)",
		},
	}

	// NOASSERTION agent: the honest stand-in for every agent slot the inventory does
	// not record — the model supplier when no provider is on file, and ALWAYS the
	// dataset originator (the registry records who ATTESTED a dataset's provenance,
	// never who originated it; asserting the model's provider as the dataset
	// originator would be fabricated provenance, docs/SECURITY-HARDENING.md).
	noassertIRI := spdxIRI("agent", "noassertion:"+inv.Owned.ID)
	graph = append(graph, map[string]any{
		"type": "Agent", "spdxId": noassertIRI, "creationInfo": spdxCreationInfoRef,
		"name": "NOASSERTION", "comment": "honest no-assertion agent: the governed inventory does not record this party (docs/08 §9)",
	})
	supplierIRI := noassertIRI
	if inv.Owned.ProviderRef != "" {
		supplierIRI = spdxIRI("agent", "provider:"+inv.Owned.ProviderRef)
		graph = append(graph, map[string]any{
			"type": "Organization", "spdxId": supplierIRI, "creationInfo": spdxCreationInfoRef,
			"name": inv.Owned.ProviderRef,
		})
	}

	elementIRIs := []any{}

	// Lineage datasets → dataset_DatasetPackage elements.
	var datasetIRIs []any
	for _, d := range inv.Datasets {
		iri := spdxIRI("dataset", d.ID)
		datasetIRIs = append(datasetIRIs, iri)
		when, whenOK := spdxTime(inv.DatasetCreatedAt[d.ID])
		timeNote := ""
		if !whenOK {
			when = created
			timeNote = "; stored timestamp unparseable — export generation time used"
		}
		dl, dlRecorded := spdxDownloadLocation(d.SourceRef, "dataset", d.ID)
		comment := spdxNotRecorded + fmt.Sprintf("provenance_verified=%t (operator attestation); dataset originator not recorded (NOASSERTION)", d.Verified) + timeNote
		if d.Governance != "" {
			comment += "; governance: " + d.Governance
		}
		el := map[string]any{
			"type": "dataset_DatasetPackage", "spdxId": iri, "creationInfo": spdxCreationInfoRef,
			"name": d.Name,
			// Modality is not recorded in the registry — the vocabulary's own
			// noAssertion entry is the honest required value.
			"dataset_datasetType":       []any{"noAssertion"},
			"software_primaryPurpose":   "data",
			"software_downloadLocation": dl,
			"releaseTime":               when,
			"builtTime":                 when,
			// originatedBy is Core 0..* (array in the JSON Schema) restricted to
			// exactly one by the Dataset profile — one honest NOASSERTION entry.
			"originatedBy": []any{noassertIRI},
			"comment":      comment,
		}
		if !dlRecorded {
			el["comment"] = comment + "; download location not recorded (URN placeholder)"
		}
		if lvl, ok := spdxConfidentiality(d.Classification); ok {
			el["dataset_confidentialityLevel"] = lvl
		}
		if d.Classification == "pii" {
			el["dataset_hasSensitivePersonalInformation"] = "yes"
		}
		if h, ok := spdxHash(d.ContentAlg, d.ContentHash); ok {
			el["verifiedUsing"] = []any{h}
		}
		graph = append(graph, el)
		elementIRIs = append(elementIRIs, iri)
	}

	// Model versions → ai_AIPackage elements (+ one lineage relationship each).
	rootIRIs := []any{}
	for _, ver := range inv.Versions {
		iri := spdxIRI("model-version", ver.ID)
		rootIRIs = append(rootIRIs, iri)
		when, whenOK := spdxTime(inv.VersionCreatedAt[ver.ID])
		timeNote := ""
		if !whenOK {
			when = created
			timeNote = "; stored timestamp unparseable — export generation time used"
		}
		dl, dlRecorded := spdxDownloadLocation(ver.ArtifactRef, "model-version", ver.ID)
		comment := spdxNotRecorded + "governed registry record" + timeNote
		if inv.Owned.Kind != "" {
			// The registry kind (hosted/fine_tuned/imported) is CUSTODY, not the SPDX
			// ai_typeOfModel semantics (model class, e.g. LLM) — it rides in the
			// comment, never in a field that means something else.
			comment += "; registry custody kind: " + inv.Owned.Kind
		}
		var limitations []string
		if adm, ok := inv.AdmissionByVersion[ver.ID]; ok {
			comment += fmt.Sprintf("; signed-admission: signature_verified=%t artifact_verified=%t method=%s",
				adm.SignatureVerified, adm.ArtifactVerified, adm.Method)
			if adm.CoverageNote != "" {
				limitations = append(limitations, "admission coverage: "+adm.CoverageNote)
			}
			if !adm.SignatureVerified && adm.Reason != "" {
				limitations = append(limitations, "admission not verified: "+adm.Reason)
			}
		} else {
			comment += "; signed-admission: not_recorded"
		}
		if !dlRecorded {
			comment += "; download location not recorded (URN placeholder)"
		}
		el := map[string]any{
			"type": "ai_AIPackage", "spdxId": iri, "creationInfo": spdxCreationInfoRef,
			"name":                      inv.Owned.Name,
			"software_packageVersion":   ver.Version,
			"software_primaryPurpose":   "model",
			"software_downloadLocation": dl,
			"releaseTime":               when,
			"suppliedBy":                supplierIRI,
			"comment":                   comment,
		}
		// ai_limitation has maxCount 1 — a single joined string.
		if len(limitations) > 0 {
			el["ai_limitation"] = strings.Join(limitations, "; ")
		}
		if adm, ok := inv.AdmissionByVersion[ver.ID]; ok {
			if h, hok := spdxHash("sha256", adm.SubjectDigest); hok {
				el["verifiedUsing"] = []any{h}
			}
		}
		graph = append(graph, el)
		elementIRIs = append(elementIRIs, iri)

		if len(datasetIRIs) > 0 {
			// The registry records lineage datasets WITHOUT a train-vs-evaluate role
			// (models.dataset has no role column), so the export asserts the
			// role-neutral hasDataFile — claiming trainedOn for a possibly
			// evaluation-only dataset would be a machine-readable over-claim. When a
			// role is recorded some day, trainedOn/testedOn are the refinements.
			relIRI := spdxIRI("relationship", "hasdatafile:"+ver.ID)
			graph = append(graph, map[string]any{
				"type": "Relationship", "spdxId": relIRI, "creationInfo": spdxCreationInfoRef,
				"from": iri, "relationshipType": "hasDataFile", "to": datasetIRIs,
				"comment": "governed lineage: datasets recorded as the model's training/evaluation inputs without a recorded role — hasDataFile is the honest role-neutral assertion; completeness is not asserted",
			})
			elementIRIs = append(elementIRIs, relIRI)
		}
	}

	docIRI := spdxIRI("document", inv.Owned.ID)
	doc := map[string]any{
		"type": "SpdxDocument", "spdxId": docIRI, "creationInfo": spdxCreationInfoRef,
		"name":               "SPDX " + spdxSpecVersion + " AI Profile — " + inv.Owned.Name,
		"profileConformance": []any{"core", "software", "ai", "dataset"},
		"comment":            spdxDisclaimer,
	}
	// rootElement/element are 0..* — OMITTED when empty (a version-less model), never
	// serialized as JSON null (the official JSON Schema rejects null arrays).
	if len(rootIRIs) > 0 {
		doc["rootElement"] = rootIRIs
	}
	if len(elementIRIs) > 0 {
		doc["element"] = elementIRIs
	}
	graph = append(graph, doc)

	return map[string]any{
		"@context": spdxContextURL,
		"@graph":   graph,
	}
}

// captureCreatedAt maps record id → base created_at — the registry registration
// instants the SPDX export uses (disclosed) in place of unrecorded publisher
// timestamps.
func captureCreatedAt(recs []model.Record) map[string]string {
	out := make(map[string]string, len(recs))
	for _, rec := range recs {
		out[rec.String(model.ColID)] = rec.String(model.ColCreatedAt)
	}
	return out
}
