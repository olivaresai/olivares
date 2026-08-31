// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

// This file surfaces the declared Anthropic deployment-surface matrix (ANT2-01/17)
// and per-platform model lifecycle (ANT2-03) as a CONSULTABLE reference: the
// data the platforms view previously read from static web files now ships from
// the engine, AsOf-stamped and source-cited. The structs are MODULE-OWNED mirrors of
// the connector's reference shapes, fed through a nil-defaulting provider seam — the
// module keeps the "models does not import claude-api" property
// (the admin-dashboards contract), exactly like the rate-limit inventory
// seam (ratelimits.go); the real adapter lives in the composition root. With no
// provider wired the route degrades to available=false WITH a reason — honest,
// never a 500.

// PlatformAPISupport records which Anthropic API families apply on one surface
// (mirror of the connector's APISupport). A surface without the Admin API cannot
// feed Anthropic-side governance ingest — collapsing the surfaces would be wrong on
// residency, compliance and ingest coverage.
type PlatformAPISupport struct {
	Messages     bool `json:"messages"`
	Admin        bool `json:"admin"`
	Compliance   bool `json:"compliance"`
	Models       bool `json:"models"`
	Batches      bool `json:"batches"`
	MCPConnector bool `json:"mcp_connector"`
}

// PlatformSurface is the full attribute set of one Claude deployment surface
// (mirror of the connector's Surface, 1:1 with the web's Surface type). Every field
// is verbatim from the cited authority or carries an explicit to-confirm marker
// (hipaa_status) — never a fabricated posture.
type PlatformSurface struct {
	Gateway            string             `json:"gateway"`
	DisplayName        string             `json:"display_name"`
	Operator           string             `json:"operator"`
	OperatorDataAccess string             `json:"operator_data_access"`
	BaseURLPattern     string             `json:"base_url_pattern"`
	AuthScheme         string             `json:"auth_scheme"`
	SigV4Service       string             `json:"sigv4_service"`
	WorkspaceHeader    string             `json:"workspace_header"`
	ModelIDForm        string             `json:"model_id_form"`
	APIs               PlatformAPISupport `json:"apis"`
	Billing            string             `json:"billing"`
	HIPAA              string             `json:"hipaa"`
	HIPAAStatus        string             `json:"hipaa_status"`
	ZDR                string             `json:"zdr"`
	Residency          string             `json:"residency"`
	Deprecated         bool               `json:"deprecated"`
	AsOf               string             `json:"as_of"`
	Notes              string             `json:"notes"`
}

// PlatformRetirement is one per-surface retirement row of a lifecycle family.
// Status is the honesty marker, and "confirmed" means the retirement DATE is
// published — so a to-confirm row arises two ways with different fields:
//   - a published-schedule row whose date the authority announced but did not
//     publish (e.g. mythos-preview "after Mythos 5 GA"): retires_on "" and
//     status to-confirm, but it KEEPS the family's published deprecated_on
//     (the deprecation itself is a published fact on those surfaces);
//   - a surface verified to run its OWN unpublished schedule (Bedrock/Vertex
//     rows): retires_on "" and NO deprecated_on — the partner authority
//     published neither, so neither is claimed.
//
// Absence is always rendered as to-confirm, never as "never retires".
// ReplacementRef carries the family's published successor on every row — the
// deprecations page now names one per deprecated family (this supersedes the old
// web claim that it was "empty by design").
type PlatformRetirement struct {
	Surface        string `json:"surface"`
	RetiresOn      string `json:"retires_on"`
	Status         string `json:"status"`
	ReplacementRef string `json:"replacement_ref"`
	DeprecatedOn   string `json:"deprecated_on,omitempty"`
	AsOf           string `json:"as_of"`
}

// PlatformLifecycle is one declared model-lifecycle family: an id (the registry's
// family prefix) and its per-surface deadlines, sorted by surface.
type PlatformLifecycle struct {
	ModelID     string               `json:"model_id"`
	DisplayName string               `json:"display_name"`
	Retirements []PlatformRetirement `json:"retirements"`
}

// PlatformParamDeprecation is the sampling-param deprecation pre-advice (ANT2-03):
// the affected generations reject non-default temperature/top_p/top_k with a 400.
// Anthropic's deprecation, not a product bug — informational.
type PlatformParamDeprecation struct {
	Params     []string `json:"params"`
	Affected   string   `json:"affected"`
	HTTPStatus int      `json:"http_status"`
}

// PlatformsReference is the full declared reference a provider serves: the surface
// matrix and the lifecycle schedule, each with its own AsOf stamp and source path so
// the consumer can weigh staleness and cite provenance.
type PlatformsReference struct {
	Surfaces         []PlatformSurface        `json:"surfaces"`
	SurfacesAsOf     string                   `json:"surfaces_as_of"`
	SurfacesSource   string                   `json:"surfaces_source"`
	Lifecycles       []PlatformLifecycle      `json:"lifecycles"`
	LifecycleAsOf    string                   `json:"lifecycle_as_of"`
	LifecycleSource  string                   `json:"lifecycle_source"`
	ParamDeprecation PlatformParamDeprecation `json:"param_deprecation"`
}

// PlatformsProvider is the read seam over the declared platforms reference. nil
// (the default) means no reference source is wired; the real adapter lives in the
// composition root. The provider is read-only and credential-less by contract: the
// data is declared reference, so an error is a TRANSIENT fault — the route degrades
// to available=false with a reason, never a 500.
type PlatformsProvider interface {
	Platforms(ctx context.Context) (PlatformsReference, error)
}

// platformsResponseDTO is the GET /platforms envelope: the reference flattened next
// to the availability marker, so a degraded answer keeps the full (empty) shape.
type platformsResponseDTO struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	PlatformsReference
}

// handlePlatforms returns the declared platforms reference. It NEVER mutates and
// NEVER 500s: with no provider wired, or on a transient provider error, it returns
// available=false with a reason and empty (not null) collections.
func (m *Module) handlePlatforms(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	out := platformsResponseDTO{PlatformsReference: PlatformsReference{
		Surfaces:         []PlatformSurface{},
		Lifecycles:       []PlatformLifecycle{},
		ParamDeprecation: PlatformParamDeprecation{Params: []string{}},
	}}
	if m.platforms == nil {
		out.Reason = "no platforms reference provider is wired; the deployment-surface and lifecycle reference is unavailable"
		writeJSON(w, http.StatusOK, out)
		return
	}
	ref, err := m.platforms.Platforms(r.Context())
	if err != nil {
		// Degrade honestly, never a 500, and never leak whatever the error embeds
		// (log it server-side instead) — same contract as the rate-limit inventory.
		if m.log != nil {
			m.log.Warn("models: platforms reference fetch failed; degrading to unavailable-with-reason", "err", err)
		}
		out.Reason = "the platforms reference is temporarily unavailable"
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Available = true
	out.PlatformsReference = ref
	// A stable JSON shape: collections are [] (never null) even if a provider
	// returns nil slices.
	if out.Surfaces == nil {
		out.Surfaces = []PlatformSurface{}
	}
	if out.Lifecycles == nil {
		out.Lifecycles = []PlatformLifecycle{}
	}
	if out.ParamDeprecation.Params == nil {
		out.ParamDeprecation.Params = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}
