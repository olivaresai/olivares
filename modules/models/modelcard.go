// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// standalone MODEL CARDS generated from the inventory (the substrate the
// Trust/procurement package consumes). The card follows the canonical model-card
// structure (Mitchell et al., "Model Cards for Model Reporting", FAT* 2019,
// arXiv:1810.03993; Hugging Face model-card layout, huggingface.co/docs/hub/model-cards)
// rendered from what the control plane actually records: identity, versions, lineage
// datasets, signed-admission verdicts and supplier GPAI posture.
//
// HONESTY (docs/SECURITY-HARDENING.md): a section the platform has no recorded evidence for is emitted
// as the explicit marker "not_recorded" — never invented. The control plane governs
// and inventories models; it does not train or benchmark them, so evaluation metrics,
// bias analyses and environmental figures are structurally not_recorded here (the
// CycloneDX AIBOM and the eval surfaces carry what little the plane can
// evidence). MINIMAL-DATA: metadata/refs/digests only.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	modelCardSchema     = "olivares:model-card:v1"
	modelCardDisclaimer = "Model card generated from the control plane's governed inventory: identity, versions, lineage datasets, signed-admission verdicts and supplier GPAI posture. Sections without recorded evidence are marked not_recorded — never fabricated. NOT a certification, NOT a performance report and NOT legal advice (docs/08 §9)."
	notRecorded         = "not_recorded"
)

// modelCardReferences pins the card structure to its canonical sources (verified
// 2026-06-10).
var modelCardReferences = []map[string]string{
	{
		"title": "Model Cards for Model Reporting (Mitchell et al., FAT* 2019)",
		"url":   "https://arxiv.org/abs/1810.03993",
	},
	{
		"title": "Hugging Face Hub — Model Cards",
		"url":   "https://huggingface.co/docs/hub/model-cards",
	},
}

// modelCardDoc is the generated card. Field order mirrors the Hugging Face annotated
// model-card layout: details → intended use → factors/limitations → training data →
// evaluation → ethical considerations (Mitchell et al. §4 differs slightly:
// evaluation data precedes training data there).
type modelCardDoc struct {
	Schema       string              `json:"schema"`
	GeneratedAt  string              `json:"generated_at"`
	ModelDetails modelCardDetails    `json:"model_details"`
	IntendedUse  any                 `json:"intended_use"`
	Limitations  []string            `json:"limitations"`
	TrainingData any                 `json:"training_data"`
	Evaluation   any                 `json:"evaluation"`
	Ethical      any                 `json:"ethical_considerations"`
	Provenance   modelCardProvenance `json:"provenance_and_admission"`
	References   []map[string]string `json:"format_references"`
	Disclaimer   string              `json:"disclaimer"`
}

type modelCardDetails struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// BaseRef is the registry's recorded base-model reference (HF convention:
	// base_model) — NOT an architecture-family assertion the registry never makes.
	BaseRef     string             `json:"base_ref,omitempty"`
	ProviderRef string             `json:"provider_ref,omitempty"`
	Status      string             `json:"status"`
	OwnerRef    string             `json:"owner_ref,omitempty"`
	Versions    []modelCardVersion `json:"versions"`
}

type modelCardVersion struct {
	Version           string `json:"version"`
	Status            string `json:"status"`
	ArtifactRef       string `json:"artifact_ref,omitempty"`
	SourceRef         string `json:"source_ref,omitempty"`
	ParentRef         string `json:"parent_ref,omitempty"`
	AdmissionRecorded bool   `json:"admission_recorded"`
	SignatureVerified bool   `json:"signature_verified"`
	ArtifactVerified  bool   `json:"artifact_verified"`
	SubjectDigest     string `json:"subject_digest,omitempty"`
}

type modelCardDataset struct {
	Name               string `json:"name"`
	Classification     string `json:"classification,omitempty"`
	Governance         string `json:"governance,omitempty"`
	SourceRef          string `json:"source_ref,omitempty"`
	ProvenanceVerified bool   `json:"provenance_verified"`
	ContentHash        string `json:"content_hash,omitempty"`
}

type modelCardProvenance struct {
	SignedAdmissions    int    `json:"signed_admissions_recorded"`
	AdmissionsVerified  int    `json:"signed_admissions_verified"`
	GPAIPostureRecorded bool   `json:"supplier_gpai_posture_recorded"`
	GPAIPostureVerified bool   `json:"supplier_gpai_posture_verified"`
	Note                string `json:"note"`
}

// buildModelCard renders the card from the shared inventory read.
func buildModelCard(inv modelInventory, generatedAt string) modelCardDoc {
	card := modelCardDoc{
		Schema:      modelCardSchema,
		GeneratedAt: generatedAt,
		ModelDetails: modelCardDetails{
			Name: inv.Owned.Name, Kind: inv.Owned.Kind, BaseRef: inv.Owned.BaseRef,
			ProviderRef: inv.Owned.ProviderRef, Status: inv.Owned.Status, OwnerRef: inv.Owned.OwnerRef,
			Versions: []modelCardVersion{},
		},
		Evaluation: notRecorded,
		Ethical:    notRecorded,
		References: modelCardReferences,
		Disclaimer: modelCardDisclaimer,
	}

	// Intended use: the operator's recorded note is the only honest source; the plane
	// never infers a use case.
	if n := strings.TrimSpace(inv.Owned.Note); n != "" {
		card.IntendedUse = n
	} else {
		card.IntendedUse = notRecorded
	}

	verified, recorded := 0, 0
	for _, ver := range inv.Versions {
		v := modelCardVersion{
			Version: ver.Version, Status: ver.Status, ArtifactRef: ver.ArtifactRef,
			SourceRef: ver.SourceRef, ParentRef: ver.ParentRef,
		}
		if adm, ok := inv.AdmissionByVersion[ver.ID]; ok {
			recorded++
			v.AdmissionRecorded = true
			v.SignatureVerified = adm.SignatureVerified
			v.ArtifactVerified = adm.ArtifactVerified
			v.SubjectDigest = adm.SubjectDigest
			if adm.SignatureVerified {
				verified++
			}
			if adm.CoverageNote != "" {
				card.Limitations = append(card.Limitations, "admission coverage ("+ver.Version+"): "+adm.CoverageNote)
			}
		}
		card.ModelDetails.Versions = append(card.ModelDetails.Versions, v)
	}

	if len(inv.Datasets) > 0 {
		ds := make([]modelCardDataset, 0, len(inv.Datasets))
		for _, d := range inv.Datasets {
			ds = append(ds, modelCardDataset{
				Name: d.Name, Classification: d.Classification, Governance: d.Governance,
				SourceRef: d.SourceRef, ProvenanceVerified: d.Verified, ContentHash: d.ContentHash,
			})
		}
		card.TrainingData = ds
	} else {
		card.TrainingData = notRecorded
	}

	card.Limitations = append(card.Limitations,
		"The control plane inventories and governs this model; it does not train or benchmark it — performance, bias and environmental metrics are not recorded here.")

	card.Provenance = modelCardProvenance{
		SignedAdmissions: recorded, AdmissionsVerified: verified,
		GPAIPostureRecorded: inv.GPAIPostureRecorded, GPAIPostureVerified: inv.GPAIPostureVerified,
		Note: "Signed-model admission covers self-hosted/third-party artifacts (OpenSSF Model Signing); closed-weight brokered models are evidenced via the supplier GPAI posture instead.",
	}
	return card
}

// modelCardMarkdown renders the card as a human-readable Markdown document (the
// shape a procurement reviewer expects), from the same data — no extra claims.
func modelCardMarkdown(c modelCardDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Model Card — %s\n\n", c.ModelDetails.Name)
	fmt.Fprintf(&b, "> %s\n\n", c.Disclaimer)
	b.WriteString("## Model details\n\n")
	fmt.Fprintf(&b, "- **Kind:** %s\n- **Status:** %s\n", c.ModelDetails.Kind, c.ModelDetails.Status)
	if c.ModelDetails.BaseRef != "" {
		fmt.Fprintf(&b, "- **Base model:** %s\n", c.ModelDetails.BaseRef)
	}
	if c.ModelDetails.ProviderRef != "" {
		fmt.Fprintf(&b, "- **Provider:** %s\n", c.ModelDetails.ProviderRef)
	}
	b.WriteString("\n### Versions\n\n")
	b.WriteString("| Version | Status | Admission | Signature verified | Digest |\n|---|---|---|---|---|\n")
	for _, v := range c.ModelDetails.Versions {
		adm := notRecorded
		if v.AdmissionRecorded {
			adm = "recorded"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %t | %s |\n", v.Version, v.Status, adm, v.SignatureVerified, v.SubjectDigest)
	}
	b.WriteString("\n## Intended use\n\n")
	fmt.Fprintf(&b, "%v\n", c.IntendedUse)
	b.WriteString("\n## Limitations\n\n")
	for _, l := range c.Limitations {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	b.WriteString("\n## Training data (governed lineage datasets)\n\n")
	if ds, ok := c.TrainingData.([]modelCardDataset); ok {
		b.WriteString("| Dataset | Classification | Provenance verified |\n|---|---|---|\n")
		for _, d := range ds {
			fmt.Fprintf(&b, "| %s | %s | %t |\n", d.Name, d.Classification, d.ProvenanceVerified)
		}
	} else {
		b.WriteString(notRecorded + "\n")
	}
	fmt.Fprintf(&b, "\n## Evaluation\n\n%v\n", c.Evaluation)
	fmt.Fprintf(&b, "\n## Ethical considerations\n\n%v\n", c.Ethical)
	b.WriteString("\n## Provenance and admission\n\n")
	fmt.Fprintf(&b, "- Signed admissions recorded/verified: %d/%d\n- Supplier GPAI posture recorded: %t (verified: %t)\n- %s\n",
		c.Provenance.SignedAdmissions, c.Provenance.AdmissionsVerified,
		c.Provenance.GPAIPostureRecorded, c.Provenance.GPAIPostureVerified, c.Provenance.Note)
	return b.String()
}

// handleModelCard returns the generated model card for an owned model (read-only
// export from the live inventory; read-tier, not audited — observer effect, exactly
// like the AIBOM generate route). ?format=md renders Markdown; JSON is the default.
func (m *Module) handleModelCard(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var card modelCardDoc
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		inv, err := readModelInventory(r, sc, id)
		if err != nil {
			return err
		}
		card = buildModelCard(inv, time.Now().UTC().Format(time.RFC3339))
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "md") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(modelCardMarkdown(card)))
		return
	}
	writeJSON(w, http.StatusOK, card)
}
