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
	"github.com/olivaresai/olivares/core/model"
)

// Permission tiers for the provider-profile plane (B1), declared through the
// module catalog like every other tier here. read: list/get profiles and
// bindings; write: create profiles, rename/disable/enable them, create bindings;
// admin: retire a profile, read its configuration (the paths) and revoke a
// binding. Creating a binding ADDITIONALLY requires whatever the composition port
// demands to administer the source — for the global roster, the deployment-wide
// admin the console's source CRUD already requires. A client-side selector or a
// path in a request is never authorization.
const (
	permProfileRead  auth.Permission = "sessions:profile:read"
	permProfileWrite auth.Permission = "sessions:profile:write"
	permProfileAdmin auth.Permission = "sessions:profile:admin"

	permProfileBindingRead  auth.Permission = "sessions:profile-binding:read"
	permProfileBindingWrite auth.Permission = "sessions:profile-binding:write"
	permProfileBindingAdmin auth.Permission = "sessions:profile-binding:admin"
)

func providerProfilePermissions() []auth.Permission {
	out := []auth.Permission{
		permProfileRead, permProfileWrite, permProfileAdmin,
		permProfileBindingRead, permProfileBindingWrite, permProfileBindingAdmin,
	}
	// D19: the provider-RECORD tiers travel with the profile tiers because they are
	// declared as one plane, and a role that can administer profiles is not thereby
	// allowed to register credentials — the tiers stay independent.
	return append(out, providerRecordPermissions()...)
}

// providerProfileRoutes mounts the B1 administration surface under /v1/m/sessions/.
func (m *Module) providerProfileRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/provider-profiles", permProfileRead, m.handleListProfiles)
	reg.Handle("POST", "/provider-profiles", permProfileWrite, m.handleCreateProfile)
	reg.Handle("GET", "/provider-profiles/{ref}", permProfileRead, m.handleGetProfile)
	reg.Handle("PATCH", "/provider-profiles/{ref}", permProfileWrite, m.handlePatchProfile)
	reg.Handle("POST", "/provider-profiles/{ref}/retire", permProfileAdmin, m.handleRetireProfile)
	reg.Handle("GET", "/provider-profiles/{ref}/configuration", permProfileAdmin, m.handleProfileConfiguration)
	// The requirements read of ONE profile, under the profile READ permission: it
	// explains the same profile the read above returns and grants nothing the read
	// does not. It is deliberately not under profile:admin — it exposes no path —
	// and deliberately not under run:write — it creates nothing.
	reg.Handle("GET", "/provider-profiles/{ref}/launch-readiness", permProfileRead, m.handleProfileLaunchReadiness)
	// HC1: the official CLI candidates observed for the same profile, under the same
	// read permission. Advisory codes only (host_tools.go).
	reg.Handle("GET", "/provider-profiles/{ref}/host-tools", permProfileRead, m.handleProfileHostTools)
	reg.Handle("GET", "/provider-source-bindings", permProfileBindingRead, m.handleListBindings)
	reg.Handle("POST", "/provider-source-bindings", permProfileBindingWrite, m.handleCreateBinding)
	reg.Handle("GET", "/provider-source-bindings/{ref}", permProfileBindingRead, m.handleGetBinding)
	reg.Handle("POST", "/provider-source-bindings/{ref}/revoke", permProfileBindingAdmin, m.handleRevokeBinding)
	m.providerRecordRoutes(reg)
}

// providerProfileDTO is the NORMAL view of a profile: references and labels.
// It deliberately carries no path — those belong to the authorized configuration
// read — and no credential value ever.
type providerProfileDTO struct {
	ProfileRef     string `json:"profile_ref"`
	Driver         string `json:"driver"`
	EnvironmentRef string `json:"environment_ref"`
	DisplayName    string `json:"display_name,omitempty"`
	State          string `json:"state"`
	// LocalEnvironment reports whether the profile belongs to THIS node's
	// execution environment; a foreign profile is shown as such and never
	// launched here.
	LocalEnvironment bool `json:"local_environment"`
	// Operable is the LEGACY enablement flag, preserved with its exact meaning and
	// its exact computation: local, active, a driver this runtime has REGISTERED,
	// and — for a driver that needs one — an authorized authentication source. A
	// profile whose driver has no registered runner here is honestly false.
	//
	// ⛔ IT IS NOT A LAUNCH GUARANTEE, AND THIS COMMENT USED TO SAY IT WAS
	// ("whether this node could launch the profile right now"). Measured on a clean
	// engine: a local, active Claude profile with real homes and NO wired inference
	// credential source reports operable=true, and its launch is refused. The four
	// terms above are all it checks — it does not look at the Runner (driverOperable
	// answers true for the historical Claude path without one), at the executable,
	// at the homes still resolving, at the credential wiring, at the transport or at
	// the isolation. A console that renders it as "Launchable" is asserting a
	// verdict nobody computed, which is exactly the defect the launch-readiness read
	// (launch_readiness.go) exists to correct.
	//
	// It is KEPT rather than redefined on purpose: narrowing it to "driver AND a
	// global credential source" would report false for every account-home profile
	// that needs no injected key, and would mix a stream-json requirement into a
	// remote-control selection. Retiring the field is its own contract change.
	Operable bool `json:"operable"`
	// AuthSource is the AUTHORIZED authentication source ("" = none authorized,
	// which refuses every launch whose driver requires one). It is an
	// authorization, never a credential and never a path.
	AuthSource string `json:"auth_source,omitempty"`
	// ProviderRecordRef names the registered provider this profile's managed
	// launches use ("" = none, and the host-wide credential decides, exactly as it
	// did before D19). It is a reference: no key, no hint, no endpoint.
	ProviderRecordRef string `json:"provider_record_ref,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
	RetiredAt         string `json:"retired_at,omitempty"`
}

// providerProfileConfigurationDTO is the authorized configuration read: the
// canonical homes as stored. Still no credential value.
type providerProfileConfigurationDTO struct {
	ProfileRef     string `json:"profile_ref"`
	Driver         string `json:"driver"`
	EnvironmentRef string `json:"environment_ref"`
	ConfigHome     string `json:"config_home"`
	UserHome       string `json:"user_home"`
	State          string `json:"state"`
	AuthSource     string `json:"auth_source,omitempty"`
}

type createProfileRequest struct {
	Driver         string `json:"driver"`
	ConfigHome     string `json:"config_home"`
	UserHome       string `json:"user_home"`
	DisplayName    string `json:"display_name"`
	EnvironmentRef string `json:"environment_ref"`
	// AuthSource authorizes HOW this profile's child obtains its provider
	// identity: provider_account_home or managed_injection. Empty authorizes
	// neither, which is a refusal for every driver that needs one.
	AuthSource string `json:"auth_source"`
	// ProviderRecordRef optionally binds the new profile to a registered provider
	// in the same authorized call, so deploying an agent is one step.
	ProviderRecordRef string `json:"provider_record_ref"`
}

// patchProfileRequest is the only post-creation mutation: a label and/or an
// active↔disabled transition. Retirement has its own admin route; driver,
// environment and homes are not here because they are not mutable.
type patchProfileRequest struct {
	DisplayName *string `json:"display_name"`
	State       *string `json:"state"`
	// AuthSource re-authorizes (or withdraws, with "") the authentication source.
	// It is not identity, so it does not create a new profile id; a LIVE child
	// keeps the source its own launch was authorized under.
	AuthSource *string `json:"auth_source"`
	// ProviderRecordRef binds (or unbinds, with "") the registered provider this
	// profile's managed launches use. A LIVE child keeps the one its own launch
	// resolved.
	ProviderRecordRef *string `json:"provider_record_ref"`
}

type providerBindingDTO struct {
	BindingRef     string `json:"binding_ref"`
	SourceID       string `json:"source_id"`
	SourceRevision int64  `json:"source_revision"`
	SourceName     string `json:"source_name,omitempty"`
	EnvironmentRef string `json:"environment_ref"`
	SelectorKey    string `json:"selector_key"`
	ProfileRef     string `json:"profile_ref"`
	Driver         string `json:"driver"`
	State          string `json:"state"`
	BoundAt        string `json:"bound_at"`
	RevokedAt      string `json:"revoked_at,omitempty"`
}

type createBindingRequest struct {
	SourceID       string `json:"source_id"`
	SourceRevision int64  `json:"source_revision"`
	ProfileRef     string `json:"profile_ref"`
}

func (m *Module) toProfileDTO(p ProviderProfile) providerProfileDTO {
	local := m.rt.environmentRef != "" && p.EnvironmentRef == m.rt.environmentRef
	return providerProfileDTO{
		ProfileRef: p.Ref, Driver: p.Driver, EnvironmentRef: p.EnvironmentRef,
		DisplayName: p.DisplayName, State: p.State, AuthSource: p.AuthSource,
		ProviderRecordRef: p.ProviderRecordRef,
		LocalEnvironment:  local,
		Operable: local && p.State == ProfileActive && m.driverOperable(p.Driver) &&
			requireAuthSourceForDriver(p.Driver, p.AuthSource) == nil,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, RetiredAt: p.RetiredAt,
	}
}

func toBindingDTO(b ProviderSourceBinding) providerBindingDTO {
	return providerBindingDTO{
		BindingRef: b.Ref, SourceID: b.SourceID.String(), SourceRevision: b.SourceRevision,
		SourceName: b.SourceName, EnvironmentRef: b.EnvironmentRef, SelectorKey: b.SelectorKey,
		ProfileRef: b.ProfileRef, Driver: b.Driver, State: b.State,
		BoundAt: b.BoundAt, RevokedAt: b.RevokedAt,
	}
}

// handleListProfiles lists the tenant's provider profiles as references and labels, never paths.
func (m *Module) handleListProfiles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	profiles, page, err := m.ListProfiles(r.Context(), mc.Tenant, state, listQuery(r))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out := listResponse[providerProfileDTO]{Items: make([]providerProfileDTO, 0, len(profiles))}
	for _, p := range profiles {
		out.Items = append(out.Items, m.toProfileDTO(p))
	}
	out.Cursor, out.HasMore = page.Cursor, page.HasMore
	writeJSON(w, http.StatusOK, out)
}

// handleCreateProfile registers a provider profile for a configuration home on this node's execution environment; the homes are validated on the server and must already exist.
func (m *Module) handleCreateProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body createProfileRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	prof, err := m.CreateProfile(r.Context(), mc.Tenant, CreateProfileInput{
		Driver: body.Driver, ConfigHome: body.ConfigHome, UserHome: body.UserHome,
		DisplayName: body.DisplayName, EnvironmentRef: body.EnvironmentRef,
		AuthSource: body.AuthSource, ProviderRecordRef: body.ProviderRecordRef,
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m.toProfileDTO(prof))
}

// handleGetProfile returns one provider profile by its reference, without its paths.
func (m *Module) handleGetProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	prof, err := m.GetProfile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m.toProfileDTO(prof))
}

// handlePatchProfile renames a provider profile and/or moves it between active and disabled; driver, environment and homes are immutable and retirement has its own route.
func (m *Module) handlePatchProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body patchProfileRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.DisplayName == nil && body.State == nil && body.AuthSource == nil && body.ProviderRecordRef == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("nothing to change: provide display_name, state, auth_source and/or provider_record_ref"))
		return
	}
	if body.State != nil {
		switch *body.State {
		case ProfileActive, ProfileDisabled:
		case ProfileRetired:
			writeJSON(w, http.StatusBadRequest, errorBody("retiring is irreversible and uses POST /provider-profiles/{ref}/retire"))
			return
		default:
			writeJSON(w, http.StatusBadRequest, errorBody("state must be active or disabled"))
			return
		}
	}
	prof, err := m.PatchProfile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), ProfilePatch{
		DisplayName: body.DisplayName, State: body.State, AuthSource: body.AuthSource,
		ProviderRecordRef: body.ProviderRecordRef,
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m.toProfileDTO(prof))
}

// handleRetireProfile retires a provider profile for good: the id stays, its history stays, and the home becomes free for a new profile id.
func (m *Module) handleRetireProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	retired := ProfileRetired
	prof, err := m.PatchProfile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), ProfilePatch{State: &retired})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m.toProfileDTO(prof))
}

// handleProfileConfiguration returns the stored canonical homes of one provider profile; this is the only read that exposes paths, and it never exposes a credential value.
func (m *Module) handleProfileConfiguration(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	prof, err := m.GetProfile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, providerProfileConfigurationDTO{
		ProfileRef: prof.Ref, Driver: prof.Driver, EnvironmentRef: prof.EnvironmentRef,
		ConfigHome: prof.ConfigHome, UserHome: prof.UserHome, State: prof.State,
		AuthSource: prof.AuthSource,
	})
}

// handleListBindings lists the source-to-profile bindings of the tenant, optionally narrowed to one profile.
func (m *Module) handleListBindings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	profile := strings.TrimSpace(r.URL.Query().Get("profile_ref"))
	bindings, page, err := m.ListBindings(r.Context(), mc.Tenant, profile, listQuery(r))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out := listResponse[providerBindingDTO]{Items: make([]providerBindingDTO, 0, len(bindings))}
	for _, b := range bindings {
		out.Items = append(out.Items, toBindingDTO(b))
	}
	out.Cursor, out.HasMore = page.Cursor, page.HasMore
	writeJSON(w, http.StatusOK, out)
}

// handleCreateBinding dedicates one configured source, at the exact revision this node has applied, to a provider profile; administering the source is checked through the composition port.
func (m *Module) handleCreateBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body createBindingRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	sourceID, err := model.ParseID(body.SourceID)
	if err != nil || sourceID.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("source_id must be the source's persistent id"))
		return
	}
	b, err := m.CreateBinding(r.Context(), mc.Tenant, mc.Principal, CreateBindingInput{
		SourceID: sourceID, SourceRevision: body.SourceRevision, ProfileRef: body.ProfileRef,
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBindingDTO(b))
}

// handleGetBinding returns one source-to-profile binding by its reference.
func (m *Module) handleGetBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	b, err := m.GetBinding(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBindingDTO(b))
}

// handleRevokeBinding revokes a source-to-profile binding; events already attributed under it keep their provenance and later observations through that source are no longer attributed to the profile.
func (m *Module) handleRevokeBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	b, err := m.RevokeBinding(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBindingDTO(b))
}
