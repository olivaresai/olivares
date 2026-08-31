// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"crypto/sha256"
	"encoding/hex"
)

// This file renders a sealed evidence package as NIST OSCAL (FIN-10) — the format a
// client's GRC tool ingests. It emits THREE OSCAL models the export needs: a
// component-definition (the control plane's capabilities as implemented-requirements),
// assessment-results (per-control status + evidence anchored to the tamper-evident
// ledger), and a control-mapping (the framework→capability crosswalk as the
// OSCAL Control Mapping model). It REFERENCES the ledger via props (manifest_hash +
// ledger seq/hash + integrity), it never copies it (docs/SECURITY-HARDENING.md).
//
// Target: NIST OSCAL v1.2.2 (the current 1.2.x release, 2026-04-30; pages.nist.gov/OSCAL,
// usnistgov/OSCAL @ v1.2.2). v1.2.0 (2025-12-12) added the Control Mapping model used
// below; v1.2.1/v1.2.2 are patch releases (datatype/dependency fixes) — the
// component-definition and assessment-results shapes this file emits are unchanged from
// 1.1.x in every field we use, and our custom-namespace props are unaffected by the
// 1.2.0 reserved-prop-type renames. Field names, nesting and the assessment status enum
// are verified against the v1.2.2 metaschema. Three honesty-relevant constraints are
// encoded here:
//   - OSCAL's finding status enum is ONLY {satisfied, not-satisfied} — there is no
//     "partial"/"by_design". So a control that is by_design/partial/gap/unmapped maps
//     to OSCAL `not-satisfied`, and the REAL product status rides in status.reason +
//     a prop, so the export never launders a by_design control into "satisfied"
//     (docs/SECURITY-HARDENING.md). ONLY StatusSatisfied → OSCAL satisfied.
//   - The control-mapping NEVER asserts conformance: every map uses relationship
//     "intersects-with" (the capabilities address part of a control), never
//     "equivalent-to"/satisfied — the only satisfaction assertion lives in
//     assessment-results, gated on live operational evidence (docs/SECURITY-HARDENING.md).
//   - Custom props (manifest_hash, ledger_hash, control_status) use our OWN namespace,
//     not NIST's reserved default ns, so we do not squat on csrc.nist.gov/ns/oscal.

const (
	oscalVersion = "1.2.2"
	oscalPropNS  = "https://olivares.ai/ns/oscal"
)

// oscalStatusState maps a product control status to the OSCAL finding status enum.
// ONLY a satisfied control (operational evidence backs it) is OSCAL `satisfied`; every
// honest non-satisfied state (by_design/partial/gap/unmapped) is `not-satisfied`, with
// the precise product status preserved in the reason and a prop.
func oscalStatusState(s string) string {
	if s == string(StatusSatisfied) {
		return "satisfied"
	}
	return "not-satisfied"
}

// detUUID derives a deterministic, format-valid UUID (v5-style: version nibble 5,
// RFC 4122 variant) from a seed, so the same sealed package always renders byte-stable
// OSCAL (good for re-verification and golden tests) without a random source.
func detUUID(seed string) string {
	h := sha256.Sum256([]byte("oscal|" + seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	s := hex.EncodeToString(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// prop builds one OSCAL property under our namespace.
func prop(name, value string) map[string]any {
	return map[string]any{"name": name, "ns": oscalPropNS, "value": value}
}

// ledgerAnchorProps are the tamper-evidence props attached to the component and the
// assessment result so the OSCAL document re-verifies against the ledger offline.
func ledgerAnchorProps(dto evidencePackageDTO) []any {
	props := []any{
		prop("manifest_hash", dto.ManifestHash),
		prop("ledger_seq", itoa(dto.LedgerSeq)),
		prop("integrity_ok", boolStr(dto.IntegrityOK)),
		prop("evidence_package_id", dto.ID),
	}
	if dto.LedgerHash != "" {
		props = append(props, prop("ledger_hash", dto.LedgerHash))
	}
	return props
}

// oscalDocument renders the sealed package + control results as the OSCAL bundle
// (component-definition + assessment-results + control-mapping) with the ledger anchor
// in props.
//
// when ref is non-nil (an operator registered an OSCAL profile/SSP for this
// framework), the caller has already filtered results to the resolved selection, so the
// component-definition, the assessment-results findings AND the control-mapping are all
// scoped consistently; this function additionally (a) switches the assessment-results
// reviewed-controls from `include-all` to `include-controls` with the selected ids, and
// (b) stamps the profile back-references (profile/ssp uuid, import-profile/source href,
// document SHA-256) as props under our namespace. When ref is nil the output is
// BYTE-IDENTICAL to the pre export (include-all, no profile props) — no rug-pull.
func oscalDocument(dto evidencePackageDTO, results []controlResultDTO, fwName string, ref *ProfileRef) map[string]any {
	meta := func(title string) map[string]any {
		return map[string]any{
			"title":         title,
			"last-modified": dto.GeneratedAt,
			"version":       dto.FrameworkVersion,
			"oscal-version": oscalVersion,
		}
	}
	source := oscalSourcePrefix + dto.Framework

	// component-definition: one component (the control plane) implementing each control
	// of the framework. The implemented-requirement carries the control id, the capability
	// keys that evidence it, and the real status as a prop.
	implemented := make([]any, 0, len(results))
	for _, c := range results {
		irProps := []any{prop("control_status", c.Status)}
		for _, ev := range c.Capabilities {
			irProps = append(irProps, prop("capability", string(ev.Key)+":"+string(ev.State)))
		}
		implemented = append(implemented, map[string]any{
			"uuid":        detUUID(dto.ID + "|ir|" + c.ControlID),
			"control-id":  c.ControlID,
			"description": nonEmpty(c.Summary, c.Title),
			"props":       irProps,
		})
	}
	component := map[string]any{
		"uuid":        detUUID(dto.ID + "|component"),
		"type":        "service",
		"title":       "Olivares.AI Control Plane",
		"description": "Enterprise AI control plane (guardrails, access-map, governance, audit ledger, compliance). Capabilities implementing " + fwName + ".",
		"props":       ledgerAnchorProps(dto),
		"control-implementations": []any{
			map[string]any{
				"uuid":                     detUUID(dto.ID + "|ci"),
				"source":                   source,
				"description":              "Control-plane capability implementation of " + fwName + ", assessed from a sealed, ledger-anchored evidence package.",
				"implemented-requirements": implemented,
			},
		},
	}
	componentDefinition := map[string]any{
		"uuid":       detUUID(dto.ID + "|component-definition"),
		"metadata":   meta("Olivares.AI Control Plane — " + fwName + " component definition"),
		"components": []any{component},
	}

	// assessment-results: one result with a finding per control. Control status lives
	// under findings[].target.status.state (satisfied|not-satisfied); the real product
	// status is preserved in reason + a prop so by_design is never laundered to satisfied.
	findings := make([]any, 0, len(results))
	for _, c := range results {
		findings = append(findings, map[string]any{
			"uuid":        detUUID(dto.ID + "|finding|" + c.ControlID),
			"title":       c.ControlID + " — " + c.Status,
			"description": nonEmpty(c.Summary, c.Title),
			"target": map[string]any{
				"type":      "objective-id",
				"target-id": c.ControlID + "_obj",
				"status": map[string]any{
					"state":   oscalStatusState(c.Status),
					"reason":  c.Status, // the precise product status (satisfied|by_design|partial|gap|unmapped)
					"remarks": nonEmpty(c.Summary, c.Title),
				},
			},
			"props": []any{prop("control_status", c.Status)},
		})
	}
	// reviewed-controls declares WHICH controls this assessment covered. Default is
	// include-all; an operator profile/SSP scopes it to include-controls with the
	// resolved ids (the OSCAL-correct way to express the assessment scope).
	reviewedSelections := []any{map[string]any{"include-all": map[string]any{}}}
	resultProps := ledgerAnchorProps(dto)
	if ref != nil {
		reviewedSelections = []any{map[string]any{"include-controls": oscalIncludeControls(ref.SelectedIDs)}}
		resultProps = append(resultProps, profileRefProps(ref)...)
	}
	result := map[string]any{
		"uuid":        detUUID(dto.ID + "|result"),
		"title":       fwName + " control assessment",
		"description": "Automated control-status assessment derived from a sealed, hash-chain-verified evidence package.",
		"start":       dto.GeneratedAt,
		"reviewed-controls": map[string]any{
			"control-selections": reviewedSelections,
		},
		"props":    resultProps,
		"findings": findings,
	}
	assessmentResults := map[string]any{
		"uuid":     detUUID(dto.ID + "|assessment-results"),
		"metadata": meta("Olivares.AI Control Plane — " + fwName + " assessment results"),
		// import-ap is required; we have no separate Assessment Plan, so we point at a
		// synthetic, stable reference and disclose it (the assessment is generated from
		// the sealed evidence package, not a pre-authored plan).
		"import-ap": map[string]any{"href": oscalAssessmentPlanPrefix + dto.Framework},
		"results":   []any{result},
	}

	// control-mapping (OSCAL 1.2.0 Control Mapping model): the framework→capability
	// crosswalk the module already produces. The Olivares capability reference model is the
	// shared pivot — EVERY framework maps to it, so two controls (in different frameworks)
	// mapped to the same capability are crosswalked through it. relationship is
	// "intersects-with" (the capabilities address part of a control); it NEVER asserts
	// conformance — OSCAL satisfaction lives ONLY in assessment-results, gated on live
	// operational evidence (docs/SECURITY-HARDENING.md). Unmapped controls have no crosswalk and are omitted
	// here; they still surface as unmapped in assessment-results.
	maps := make([]any, 0, len(results))
	for _, c := range results {
		if len(c.Capabilities) == 0 {
			continue
		}
		targets := make([]any, 0, len(c.Capabilities))
		for _, ev := range c.Capabilities {
			targets = append(targets, map[string]any{
				"type":   "control",
				"id-ref": string(ev.Key),
				"props":  []any{prop("capability_state", string(ev.State))},
			})
		}
		maps = append(maps, map[string]any{
			"uuid":         detUUID(dto.ID + "|map|" + c.ControlID),
			"relationship": "intersects-with",
			"sources":      []any{map[string]any{"type": "control", "id-ref": c.ControlID}},
			"targets":      targets,
			"props":        []any{prop("control_status", c.Status)},
			"remarks":      "Control-plane capabilities that evidence " + c.ControlID + " (live status: " + c.Status + "). intersects-with: the capabilities address this control; OSCAL satisfaction is asserted ONLY in assessment-results and ONLY for live operational evidence.",
		})
	}
	controlMapping := map[string]any{
		"uuid":     detUUID(dto.ID + "|mapping-collection"),
		"metadata": meta("Olivares.AI Control Plane — " + fwName + " → capability control mapping"),
		"mappings": []any{
			map[string]any{
				"uuid": detUUID(dto.ID + "|mapping"),
				"source-resource": map[string]any{
					"type":    "catalog",
					"href":    source,
					"remarks": fwName + " control set.",
				},
				"target-resource": map[string]any{
					"type":    "catalog",
					"href":    oscalCapabilitiesURL,
					"remarks": "Olivares.AI control-plane capability reference model — the shared pivot across every mapped framework (capabilities.go).",
				},
				"maps":  maps,
				"props": ledgerAnchorProps(dto),
			},
		},
	}

	disclaimer := reportDisclaimer + " OSCAL v" + oscalVersion + " export; satisfied is asserted ONLY for controls with live operational evidence (by_design/partial/gap map to OSCAL not-satisfied; the control-mapping uses intersects-with and never asserts conformance)."
	if ref != nil {
		// Honest scoping note: the assessment-results are scoped to the operator's OSCAL
		// profile/SSP selection, while the ledger/manifest anchor still references the
		// FULL sealed package — the scope rides reviewed-controls + the profile_* props,
		// never a re-sealed subset.
		disclaimer += " Scoped to the operator-registered OSCAL " + ref.DocKind + " selection (assessment-results reviewed-controls = include-controls; see the profile_* props); the manifest/ledger anchor references the full sealed evidence package."
	}
	return map[string]any{
		"oscal_version":        oscalVersion,
		"component-definition": componentDefinition,
		"assessment-results":   assessmentResults,
		"control-mapping":      controlMapping,
		"manifest":             evidenceManifest(dto),
		"disclaimer":           disclaimer,
	}
}

// oscalIncludeControls renders the assessment-results reviewed-controls include-controls
// array: ONE object per selected control id ({"control-id": id}). This is the
// assessment-results model's select-control-by-id shape (verified against the OSCAL
// v1.2.2 assessment-results metaschema: include-controls is an array of objects each with
// a REQUIRED control-id token). It is deliberately DIFFERENT from the profile model's
// include-controls (which uses with-ids / matching / with-child-controls) — emitting the
// profile's with-ids shape here would make the assessment-results document invalid, which
// the FedRAMP/agency consumer that schema-validates the output would reject.
func oscalIncludeControls(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = map[string]any{"control-id": id}
	}
	return out
}

// profileRefProps stamps the operator OSCAL profile/SSP back-references as props under
// our namespace, so the assessment-results document points back at the exact profile/SSP
// that scoped it (the closed GRC loop). doc_kind + doc_sha256 are always present; the
// uuid/href fields appear only when the ingested document carried them.
func profileRefProps(ref *ProfileRef) []any {
	props := []any{
		prop("profile_doc_kind", ref.DocKind),
		prop("profile_doc_sha256", ref.DocSHA256),
	}
	if ref.ProfileUUID != "" {
		props = append(props, prop("profile_uuid", ref.ProfileUUID))
	}
	if ref.SSPUUID != "" {
		props = append(props, prop("ssp_uuid", ref.SSPUUID))
	}
	if ref.ImportProfileHref != "" {
		props = append(props, prop("import_profile_href", ref.ImportProfileHref))
	}
	if ref.SourceHref != "" {
		props = append(props, prop("profile_source_href", ref.SourceHref))
	}
	if ref.OscalVersion != "" {
		props = append(props, prop("profile_oscal_version", ref.OscalVersion))
	}
	return props
}

// boolStr / nonEmpty are tiny local helpers (itoa lives in helpers.go).
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
