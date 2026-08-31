// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the Admin-API GOVERNANCE POSTURE the identity console's `posture` tab reads:
// the External Keys / CMEK inventory (ANT2-04) and the workspace data-residency object
// (ANT2-06). Both were DECLARED by the typed client and mounted by nobody, so the panel
// asked and the engine answered 404 while every mocked unit test stayed green.
//
// WHAT THIS EXPOSES AND WHAT IT DOES NOT. The data is already collected and governed by
// connectors/claude-api (governance.go fetchExternalKeys / fetchWorkspaces, reaching
// /v1/organizations/external_keys and .../workspaces). This serves that inventory; it
// does not gather anything new and it cannot mutate. It is minimal-data by construction:
// an ekey_ REFERENCE, the cloud-KMS provider TYPE, a validation state and timestamps —
// there is no field here that could carry key material, and the connector's own type has
// none either (connectors/modelprovider/catalog.go:405-408).
//
// THREE ANSWERS, NEVER TWO. "No customer-managed keys" and "we could not look" are
// different facts and must not render alike:
//   - available=true  + items      — the inventory, read cleanly (zero items is a real zero)
//   - available=false + reason     — no Admin credential wired, or a transient fetch fault
//   - 403                          — the caller lacks governance:identity:read
//
// It never 500s on a connector fault and never echoes the connector's error, which can
// embed the Admin endpoint and credential: that goes to the log, the browser gets a
// generic reason. This mirrors the sibling read-only inventory the same connector already
// backs — modules/models/ratelimits.go handleRateLimits — deliberately, so the two
// degrade the same way.
//
// SCOPE AXIS, SAID OUT LOUD. These are ANTHROPIC-org objects read through ONE
// deployment-level Admin credential; an Anthropic workspace is not an Olivares workspace,
// so there is no Olivares workspace axis to confine on and inventing one would filter by a
// key that means something else. The tenant axis IS honored the only way it can be: the
// route is tenant-resolved and self-audits under the caller's tenant. The sibling
// /v1/m/models/rate-limits reads the same credential the same way.

// IdentityPostureProvider is the read seam over the claude-api Admin connector's
// governance inventory. It is READ-ONLY by type: there is no method here that can change
// anything at the provider. A nil provider is the honest "not wired" posture — distinct
// from a wired provider returning zero rows, which is a real empty inventory.
type IdentityPostureProvider interface {
	// ExternalKeys lists the org's customer-managed encryption keys as inventory
	// metadata (ekey_ references only, never key material).
	ExternalKeys(ctx context.Context) ([]modelprovider.ExternalKeyRef, error)
	// Workspaces lists the org's workspaces with their governance object (home geo,
	// CMEK reference, compartment, tags, data-residency policy).
	Workspaces(ctx context.Context) ([]modelprovider.WorkspaceRef, error)
}

// WithIdentityPostureProvider wires the read-only Admin-API posture source. Without it
// GET /external-keys and GET /residency answer available=false with a reason — never an
// empty inventory, which would read as "this org has none".
func WithIdentityPostureProvider(p IdentityPostureProvider) IdentityConsoleOption {
	return func(c *IdentityConsole) { c.posture = p }
}

// The reasons the console renders verbatim when a posture could not be read. They name
// what to do about it, because "unavailable" alone leaves an operator guessing.
const (
	reasonExternalKeysUnwired = "the Claude Admin-API connector is not wired; the External Keys (CMEK) inventory cannot be read (provision the read-only Admin credential to enable it)"
	reasonResidencyUnwired    = "the Claude Admin-API connector is not wired; the workspace data-residency inventory cannot be read (provision the read-only Admin credential to enable it)"
	reasonExternalKeysFailed  = "the External Keys (CMEK) inventory is temporarily unavailable"
	reasonResidencyFailed     = "the workspace data-residency inventory is temporarily unavailable"
)

// postureListDTO is the list envelope PLUS the availability pair. It is a superset of the
// engine-wide list shape (items + has_more) so the console's ListResponse<T> still binds,
// with `available`/`reason` carrying the third answer.
type postureListDTO[T any] struct {
	// api.JSONArray, not []T: an empty collection MUST serialize as [] and never as
	// null (core/api/listresponse.go; the repo-wide invariant test enforces it).
	Items     api.JSONArray[T] `json:"items"`
	HasMore   bool             `json:"has_more"`
	Available bool             `json:"available"`
	Reason    string           `json:"reason,omitempty"`
}

// externalKeyDTO mirrors web/src/features/identity/types.ts:351 (ExternalKeyRef) 1:1.
// Timestamps are omitted when zero: the console renders "never" for an absent value and
// would print a year-1 date for a zero one.
type externalKeyDTO struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Name            string `json:"name,omitempty"`
	State           string `json:"state,omitempty"`
	LastValidatedAt string `json:"last_validated_at,omitempty"`
	InUse           bool   `json:"in_use"`
	CreatedAt       string `json:"created_at,omitempty"`
}

// workspaceResidencyDTO mirrors web/src/features/identity/types.ts:365 (WorkspaceResidency).
type workspaceResidencyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Geo           string            `json:"geo,omitempty"`
	ExternalKeyID string            `json:"external_key_id,omitempty"`
	CompartmentID string            `json:"compartment_id,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	DataResidency *dataResidencyDTO `json:"data_residency,omitempty"`
}

type dataResidencyDTO struct {
	AllowedInferenceGeos []string `json:"allowed_inference_geos,omitempty"`
	DefaultInferenceGeo  string   `json:"default_inference_geo,omitempty"`
}

// rfc3339OrEmpty renders a timestamp for the wire, or "" for the zero time so the field
// is omitted rather than serialized as year 1.
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// handleExternalKeys serves the read-only CMEK inventory (ANT2-04).
func (c *IdentityConsole) handleExternalKeys(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := postureListDTO[externalKeyDTO]{Items: api.JSONArray[externalKeyDTO]{}}
	if c.posture == nil {
		out.Reason = reasonExternalKeysUnwired
		c.writePosture(w, r, mc, "governance.identity.external_keys.read", model.Kind("anthropic.external_key"), out)
		return
	}
	keys, err := c.posture.ExternalKeys(r.Context())
	if err != nil {
		// The connector error can embed the Admin endpoint and credential — log it,
		// never return it.
		if c.log != nil {
			c.log.Warn("identity: External Keys inventory fetch failed; degrading to unavailable-with-reason", "err", err)
		}
		out.Reason = reasonExternalKeysFailed
		c.writePosture(w, r, mc, "governance.identity.external_keys.read", model.Kind("anthropic.external_key"), out)
		return
	}
	out.Available = true
	for _, k := range keys {
		out.Items = append(out.Items, externalKeyDTO{
			ID:              k.ID,
			Provider:        k.Provider,
			Name:            k.Name,
			State:           k.State,
			LastValidatedAt: rfc3339OrEmpty(k.LastValidatedAt),
			InUse:           k.InUse,
			CreatedAt:       rfc3339OrEmpty(k.CreatedAt),
		})
	}
	c.writePosture(w, r, mc, "governance.identity.external_keys.read", model.Kind("anthropic.external_key"), out)
}

// handleResidency serves the read-only workspace governance object (ANT2-06). ARCHIVED
// workspaces are omitted: an archived workspace takes no inference, so listing its
// missing CMEK would report a residency gap nobody can act on or exploit.
func (c *IdentityConsole) handleResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := postureListDTO[workspaceResidencyDTO]{Items: api.JSONArray[workspaceResidencyDTO]{}}
	if c.posture == nil {
		out.Reason = reasonResidencyUnwired
		c.writePosture(w, r, mc, "governance.identity.residency.read", model.Kind("anthropic.workspace"), out)
		return
	}
	workspaces, err := c.posture.Workspaces(r.Context())
	if err != nil {
		if c.log != nil {
			c.log.Warn("identity: workspace residency inventory fetch failed; degrading to unavailable-with-reason", "err", err)
		}
		out.Reason = reasonResidencyFailed
		c.writePosture(w, r, mc, "governance.identity.residency.read", model.Kind("anthropic.workspace"), out)
		return
	}
	out.Available = true
	for _, ws := range workspaces {
		if ws.Archived {
			continue
		}
		item := workspaceResidencyDTO{
			ID:            ws.ID,
			Name:          ws.Name,
			Geo:           ws.Geo,
			ExternalKeyID: ws.ExternalKeyID,
			CompartmentID: ws.CompartmentID,
			Tags:          ws.Tags,
		}
		if ws.Residency != nil {
			item.DataResidency = &dataResidencyDTO{
				AllowedInferenceGeos: ws.Residency.AllowedInferenceGeos,
				DefaultInferenceGeo:  ws.Residency.DefaultInferenceGeo,
			}
		}
		out.Items = append(out.Items, item)
	}
	c.writePosture(w, r, mc, "governance.identity.residency.read", model.Kind("anthropic.workspace"), out)
}

// writePosture self-audits the read in a committed transaction BEFORE answering, then
// writes the payload. Reading which workspaces hold no customer-managed key is
// recon-relevant (docs/SECURITY-HARDENING.md, §4) — it names where data is provider-encrypted — so it is
// audited on the same deny-closed terms as the WIF graph next door: if the audit write
// fails, the posture is NOT served. The UNAVAILABLE answers are audited too; an operator
// asking and being told "not wired" is still an access attempt worth a ledger row.
func (c *IdentityConsole) writePosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, action string, kind model.Kind, body any) {
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, action, kind, "", nil)
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}
