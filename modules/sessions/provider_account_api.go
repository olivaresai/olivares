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

// The provider-account surface, under /v1/m/sessions/provider-accounts.
//
// PERMISSION TIERS. read lists and gets accounts; write adopts an existing profile
// as an account; admin is reserved for the acts that cannot be undone on an
// account's home. They are `sessions:account:*` because a module's permission
// namespace IS its API namespace, exactly as the provider-record tiers are.
//
// WHO DECIDES A READ. The list and the get are collection routes decided by the
// engine's own route door, which answers a human caller and a service caller
// alike (the same path the provider-record and provider-profile reads take); the
// rows are then read tenant-pinned through the module's data handle. No row port
// that refuses a human sits on this path.
//
// WHAT A WORKSPACE-CONFINED CALLER SEES: no account. An account belongs to its
// tenant and to an execution environment, never to a workspace, and the profile
// row declares no workspace lineage. The data handle of a request made by a member
// confined to one workspace refuses such a table before it reads a row, so the list
// and a get answer 403 "workspace confined": never the account, never an empty
// page, and the same answer for a well-formed reference that exists and for one
// that does not. A malformed reference answers 404 before the data handle is
// reached, for every caller; that answer depends only on the reference's
// spelling, so it tells nothing about the tenant's accounts.
// Adopt is refused as well, at the route door when scoped grants are wired (a
// confined member holds no tenant-level write) and otherwise by the same data
// handle. The refusal is decided before any row is read, so no complete-read
// decision is needed to answer it.
const (
	permAccountRead  auth.Permission = "sessions:account:read"
	permAccountWrite auth.Permission = "sessions:account:write"
	permAccountAdmin auth.Permission = "sessions:account:admin"
)

func providerAccountPermissions() []auth.Permission {
	return []auth.Permission{permAccountRead, permAccountWrite, permAccountAdmin}
}

// providerAccountRoutes mounts the provider-account surface under /v1/m/sessions/.
func (m *Module) providerAccountRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/provider-accounts", permAccountRead, m.handleListProviderAccounts)
	reg.Handle("GET", "/provider-accounts/{ref}", permAccountRead, m.handleGetProviderAccount)
	reg.Handle("POST", "/provider-accounts/{ref}/adopt", permAccountWrite, m.handleAdoptProviderAccount)
}

// providerAccountDTO is the ONLY shape a reader of an account sees.
//
// It carries no absolute path, for the reason the profile's normal view carries
// none: a home path names a directory on a node, and the configuration read of the
// profile is the one authorized place it is shown. The audit event of each act
// records the path for the ledger.
type providerAccountDTO struct {
	// AccountRef is the profile's own ppf_ reference; an account has no second id.
	AccountRef     string `json:"account_ref"`
	Name           string `json:"name"`
	Driver         string `json:"driver"`
	EnvironmentRef string `json:"environment_ref"`
	State          string `json:"state"`
	HomeMode       string `json:"home_mode"`
	HomeGeneration int64  `json:"home_generation"`
	// HomeRelative is the home under the accounts root. An adopted home lives
	// wherever the operator keeps it, so it has none and this is empty.
	HomeRelative string `json:"home_relative"`
	// IsolationLevel is always stated: shared or dedicated, never inferred.
	IsolationLevel    string `json:"isolation_level"`
	OSUser            string `json:"os_user,omitempty"`
	ReleaseRef        string `json:"release_ref,omitempty"`
	PendingRelease    string `json:"pending_release,omitempty"`
	AuthSource        string `json:"auth_source"`
	ProviderRecordRef string `json:"provider_record_ref,omitempty"`
	// Identity is who the vendor says is signed in, and IdentitySource says where
	// that came from. Nothing asks the vendor yet, so the source is "none" and the
	// identity is empty rather than guessed.
	Identity       string `json:"identity"`
	IdentitySource string `json:"identity_source"`
	LastLoginAt    string `json:"last_login_at,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// accountIdentityNone is the identity source of an account whose vendor was not
// asked who is signed in.
const accountIdentityNone = "none"

// adoptProviderAccountRequest names the account. An empty or null name asks the
// server to generate one; the isolation level is not a field, because an adopted
// home is shared by what it is, not by what a request says.
type adoptProviderAccountRequest struct {
	Name string `json:"name"`
}

func toProviderAccountDTO(a ProviderAccount) providerAccountDTO {
	return providerAccountDTO{
		AccountRef: a.Ref, Name: a.Name, Driver: a.Driver, EnvironmentRef: a.EnvironmentRef,
		State: a.State, HomeMode: a.HomeMode, HomeGeneration: a.HomeGeneration,
		IsolationLevel: a.IsolationLevel, OSUser: a.OSUser,
		ReleaseRef: a.ReleaseRef, PendingRelease: a.PendingRelease,
		AuthSource: a.AuthSource, ProviderRecordRef: a.ProviderRecordRef,
		IdentitySource: accountIdentityNone, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

// handleListProviderAccounts lists the tenant's named provider accounts, optionally narrowed by environment, driver and state; a profile nobody has named is never listed.
func (m *Module) handleListProviderAccounts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	query := r.URL.Query()
	accounts, page, err := m.ListProviderAccounts(r.Context(), mc.Tenant, ProviderAccountFilter{
		EnvironmentRef: strings.TrimSpace(query.Get("environment")),
		Driver:         strings.TrimSpace(query.Get("driver")),
		State:          strings.TrimSpace(query.Get("state")),
	}, listQuery(r))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out := listResponse[providerAccountDTO]{Items: make([]providerAccountDTO, 0, len(accounts))}
	for _, a := range accounts {
		out.Items = append(out.Items, toProviderAccountDTO(a))
	}
	out.Cursor, out.HasMore = page.Cursor, page.HasMore
	writeJSON(w, http.StatusOK, out)
}

// handleGetProviderAccount returns one provider account by its reference, without its paths; a profile nobody has named is not an account and answers not found.
func (m *Module) handleGetProviderAccount(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	account, err := m.GetProviderAccount(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProviderAccountDTO(account))
}

// handleAdoptProviderAccount names an existing provider profile as an account, under the given name or a generated one; the database decides whether the name is free, and the home is recorded as shared isolation.
func (m *Module) handleAdoptProviderAccount(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body adoptProviderAccountRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	account, err := m.AdoptProviderAccount(r.Context(), mc.Principal, mc.Tenant, chi.URLParam(r, "ref"), body.Name)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProviderAccountDTO(account))
}
