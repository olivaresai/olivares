// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The provider-record administration surface, under /v1/m/sessions/providers.
//
// PERMISSION TIERS. read lists and gets (never a value); write registers, renames,
// re-endpoints, ROTATES and tests; admin revokes, which is irreversible for that
// id. Testing is a write and not a read on purpose: it spends the credential's
// reputation with the provider, it can be rate-limited by them, and it changes the
// row it reports on.
//
// THE NAMESPACE. These are `sessions:provider:*` and not `providers:*` because a
// module's permission namespace IS its API namespace — the catalog validator splits
// on ':' and drops anything whose first component is not the module's own. A
// top-level `providers:*` namespace would require a top-level `providers` module,
// which §4 A1 of the design rejects with its blast radius measured.
const (
	permProviderRead  auth.Permission = "sessions:provider:read"
	permProviderWrite auth.Permission = "sessions:provider:write"
	permProviderAdmin auth.Permission = "sessions:provider:admin"
)

func providerRecordPermissions() []auth.Permission {
	return []auth.Permission{permProviderRead, permProviderWrite, permProviderAdmin}
}

// providerRecordRoutes mounts the provider-record administration surface under /v1/m/sessions/.
func (m *Module) providerRecordRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/providers", permProviderRead, m.handleListProviderRecords)
	reg.Handle("POST", "/providers", permProviderWrite, m.handleCreateProviderRecord)
	reg.Handle("GET", "/providers/{ref}", permProviderRead, m.handleGetProviderRecord)
	reg.Handle("PATCH", "/providers/{ref}", permProviderWrite, m.handlePatchProviderRecord)
	reg.Handle("POST", "/providers/{ref}/test", permProviderWrite, m.handleTestProviderRecord)
	reg.Handle("POST", "/providers/{ref}/revoke", permProviderAdmin, m.handleRevokeProviderRecord)
}

// providerRecordDTO is the ONLY shape a reader ever sees.
//
// It has no field for the credential and no field for the vault locator, and both
// omissions are deliberate. The value is obvious. The LOCATOR is subtler: it is not
// secret, but it is the name a caller would need to ask the vault with, and there
// is no reason for a browser to hold it.
type providerRecordDTO struct {
	ProviderRef string `json:"provider_ref"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url,omitempty"`
	// KeyHint is four characters. It exists to tell two credentials apart in a
	// picker and it is useless for anything else.
	KeyHint string `json:"key_hint,omitempty"`
	State   string `json:"state"`
	// Models are what the LAST SUCCESSFUL probe reported. They are an observation
	// with a timestamp, not a catalog: a provider can add or retire a model without
	// this list moving, which is why probed_at travels beside it.
	Models []string `json:"models,omitempty"`
	// ProbeState is "" (never tested), ok, refused or unreachable. It is three
	// values and not a boolean because "the provider rejected this credential" and
	// "I could not reach the provider" send an operator to different places.
	ProbeState     string `json:"probe_state,omitempty"`
	ProbeDetail    string `json:"probe_detail,omitempty"`
	ProbeLatencyMS int64  `json:"probe_latency_ms,omitempty"`
	ProbedAt       string `json:"probed_at,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	RevokedAt      string `json:"revoked_at,omitempty"`
}

// createProviderRecordRequest registers one provider. APIKey is the only field that
// carries a credential; it appears in no response and in no log.
type createProviderRecordRequest struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
}

// patchProviderRecordRequest renames, re-endpoints and/or rotates. Kind is absent
// because a record's kind decides what is injected into a child, so changing it
// under an existing binding would redefine every launch that binding authorizes.
type patchProviderRecordRequest struct {
	DisplayName *string `json:"display_name"`
	BaseURL     *string `json:"base_url"`
	APIKey      *string `json:"api_key"`
}

func toProviderRecordDTO(r ProviderRecord) providerRecordDTO {
	return providerRecordDTO{
		ProviderRef: r.Ref, Kind: r.Kind, DisplayName: r.DisplayName, BaseURL: r.BaseURL,
		KeyHint: r.KeyHint, State: r.State, Models: r.Models,
		ProbeState: r.ProbeState, ProbeDetail: r.ProbeDetail, ProbeLatencyMS: r.ProbeMillis,
		ProbedAt: r.ProbedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, RevokedAt: r.RevokedAt,
	}
}

// handleListProviderRecords lists the tenant's registered providers as kinds, names and hints, never a credential.
func (m *Module) handleListProviderRecords(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	records, page, err := m.ListProviderRecords(r.Context(), mc.Tenant, state, kind, listQuery(r))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out := listResponse[providerRecordDTO]{Items: make([]providerRecordDTO, 0, len(records))}
	for _, rec := range records {
		out.Items = append(out.Items, toProviderRecordDTO(rec))
	}
	out.Cursor, out.HasMore = page.Cursor, page.HasMore
	writeJSON(w, http.StatusOK, out)
}

// handleCreateProviderRecord registers one provider credential; the engine seals the value and returns only a four-character hint.
func (m *Module) handleCreateProviderRecord(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body createProviderRecordRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	rec, err := m.CreateProviderRecord(r.Context(), mc.Tenant, CreateProviderRecordInput{
		Kind: body.Kind, DisplayName: body.DisplayName, BaseURL: body.BaseURL, APIKey: body.APIKey,
		Actor: mc.Principal,
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toProviderRecordDTO(rec))
}

// handleGetProviderRecord returns one registered provider by its reference, without its credential.
func (m *Module) handleGetProviderRecord(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	rec, err := m.GetProviderRecord(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProviderRecordDTO(rec))
}

// handlePatchProviderRecord renames a provider, changes its endpoint and/or rotates its credential in place.
func (m *Module) handlePatchProviderRecord(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body patchProviderRecordRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.DisplayName == nil && body.BaseURL == nil && body.APIKey == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("nothing to change: provide display_name, base_url and/or api_key"))
		return
	}
	rec, err := m.PatchProviderRecord(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), ProviderRecordPatch{
		DisplayName: body.DisplayName, BaseURL: body.BaseURL, APIKey: body.APIKey,
		Actor: mc.Principal,
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProviderRecordDTO(rec))
}

// handleTestProviderRecord asks the provider which models it serves with the registered credential; it sends no completion and spends nothing.
func (m *Module) handleTestProviderRecord(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	rec, err := m.TestProviderRecord(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	// A probe that REACHED the provider and was refused is a successful test with a
	// negative verdict, not a failed request: the row it returns carries the verdict,
	// and answering 4xx here would make the console show a transport error for a
	// credential question it did get an answer to.
	writeJSON(w, http.StatusOK, toProviderRecordDTO(rec))
}

// handleRevokeProviderRecord withdraws a provider credential for good; the record keeps its id so the sessions it authorized still read truthfully.
func (m *Module) handleRevokeProviderRecord(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	rec, err := m.RevokeProviderRecord(r.Context(), mc.Principal, mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProviderRecordDTO(rec))
}
