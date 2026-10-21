// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/model"

	"github.com/olivaresai/olivares/core/auth"
)

// Runtime secret-store endpoints: the console/CLI authoring surface for the
// sealed secret store an operator references from connector configs as
// `store:<name>`. V1 governs a single deployment-wide (global) scope, so the
// endpoints are SUPERADMIN-gated — a deployment-wide credential must not be
// editable by a single tenant's admin — and writes additionally require an AAL3
// step-up (secret-bearing, privilege-shaped). The secret VALUE is never returned;
// a read exposes only a non-secret fingerprint hint. The one exception is reveal:
// a person reads back an item of their own vault/<user-id>/ namespace (see
// handleRevealSecret).

// errSecretStoreUnavailable is returned when no secret store is wired (an
// embedder/test that did not opt in). Mapped to 501 (honest seam), like SSO.
var errSecretStoreUnavailable = errors.New("api: secret store unavailable")

// secretDTO is the read shape: the name, a non-secret hint and metadata. NEVER the
// value.
type secretDTO struct {
	Name        string `json:"name"`
	Hint        string `json:"hint,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// secretsListDTO is the list response, plus whether the sealer is wired (so the
// console can show an honest "secret writes disabled" banner instead of failing
// each write).
type secretsListDTO struct {
	Secrets         []secretDTO `json:"secrets"`
	SealerAvailable bool        `json:"sealer_available"`
}

// secretInput is the PUT/DELETE payload. On PUT a blank value keeps the stored
// sealed value (so editing the description never forces re-entering the secret).
type secretInput struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

func toSecretDTO(v auth.SecretView) secretDTO {
	d := secretDTO{Name: v.Name, Hint: v.Hint, Description: v.Description}
	if !v.CreatedAt.IsZero() {
		d.CreatedAt = v.CreatedAt.String()
	}
	if !v.UpdatedAt.IsZero() {
		d.UpdatedAt = v.UpdatedAt.String()
	}
	return d
}

// secretSvc returns the secret store, or writes 501 when it is not wired.
func (s *Server) secretSvc(w http.ResponseWriter, r *http.Request) (*auth.SecretStore, bool) {
	if s.secretStore == nil {
		s.writeError(w, r, errSecretStoreUnavailable)
		return nil, false
	}
	return s.secretStore, true
}

// The explicit tenant scope serves MCP credential handles (mcp/), the secrets
// sessions receive as environment variables (env/, modules/sessions
// session_secret_env.go) and every member's personal vault (vault/<user-id>/). It
// cannot inventory or replace provider, federation, or deployment-wide secrets.
//
// Any member of the tenant is admitted to it, because every member keeps a vault; the
// mcp/ and env/ names stay with the tenant's admins (secretNameInScope, handleListSecrets).
func (s *Server) secretScope(w http.ResponseWriter, r *http.Request) (auth.Principal, model.TenantID, bool) {
	values, present := r.URL.Query()["scope"]
	if !present {
		p, ok := s.authzSystem(w, r, "system:admin")
		return p, auth.GlobalSecretScope, ok
	}
	if len(values) != 1 || values[0] != "tenant" {
		s.badRequest(w, r, "scope must be tenant or omitted for the global store")
		return auth.Principal{}, model.TenantID(""), false
	}
	p, tenant, ok := s.authzTenant(w, r, "tenant:read")
	if ok {
		if _, confined := p.ConfinedWorkspaceIn(tenant); confined {
			s.writeError(w, r, errForbidden)
			ok = false
		}
	}
	return p, tenant, ok
}

// A person's own namespace in the tenant scope is auth.PersonalVaultPrefix
// (vault/<user-id>/<item>): only that person may write, list or reveal it.

// tenantSecretName is the tenant scope's namespace: MCP credential handles,
// session environment secrets and personal vault items.
func tenantSecretName(name string) bool {
	return strings.HasPrefix(name, "mcp/") || strings.HasPrefix(name, "env/") || strings.HasPrefix(name, auth.PersonalVaultPrefix)
}

// ownsVaultItem reports whether name is vault/<user-id>/<item> in p's own
// namespace. Only a signed-in person owns one: a token carries its owner's id but
// is never that person, neither is a superadmin acting for someone else, and
// neither is an agent session, which is minted with its launcher's id.
func ownsVaultItem(p auth.Principal, name string) bool {
	// The store trims a name before it writes, so a name with edges would be stored as
	// another one than the one checked here: only the exact name is an owner's.
	rest, ok := strings.CutPrefix(name, auth.PersonalVaultPrefix)
	if !ok || name != strings.TrimSpace(name) || p.Kind != auth.KindUser || p.UserID.IsZero() || p.AgentIdentity != "" || p.SessionIdentity != "" {
		return false
	}
	owner, item, ok := strings.Cut(rest, "/")
	return ok && item != "" && owner == p.UserID.String()
}

// tenantSecretVisible reports whether p may see name in a tenant scope: a vault item only
// its owner, an mcp/ or env/ name only the tenant's admins.
func tenantSecretVisible(p auth.Principal, name string, tenantAdmin bool) bool {
	if strings.HasPrefix(name, auth.PersonalVaultPrefix) {
		return ownsVaultItem(p, name)
	}
	return tenantSecretName(name) && tenantAdmin
}

// isTenantAdmin reports whether p administers tenant's own mcp/ and env/ secrets. The
// scope admits every member; this is the second question, asked only for those names.
// A refusal is false; a failure to decide (the policy engine, the store) is an error,
// never a silent "not an admin" that would hide the organization's secrets from its admin.
func (s *Server) isTenantAdmin(r *http.Request, p auth.Principal, tenant model.TenantID) (bool, error) {
	_, err := s.admit(r.Context(), auth.Request{
		Principal: p, Permission: "tenant:admin", Tenant: tenant, Resource: auth.ResourceFor("tenant:admin"),
	}, errForbidden, ungovernedRoute)
	if err == nil {
		return true, nil
	}
	if status, _ := statusFor(err); status == http.StatusForbidden {
		return false, nil
	}
	return false, err
}

func (s *Server) secretNameInScope(w http.ResponseWriter, r *http.Request, p auth.Principal, scope model.TenantID, name string) bool {
	if scope == auth.GlobalSecretScope {
		return true
	}
	if !tenantSecretName(name) {
		s.badRequest(w, r, "tenant secret names must begin with mcp/, env/ or vault/")
		return false
	}
	// A vault name is decided by its owner alone; the admin question is for the rest.
	admin := strings.HasPrefix(name, auth.PersonalVaultPrefix)
	if !admin {
		var err error
		if admin, err = s.isTenantAdmin(r, p, scope); err != nil {
			s.writeError(w, r, err)
			return false
		}
	}
	if !tenantSecretVisible(p, name, admin) {
		s.writeError(w, r, errForbidden)
		return false
	}
	return true
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	scope := mc.Tenant
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	views, err := svc.List(r.Context(), scope)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := secretsListDTO{Secrets: make([]secretDTO, 0, len(views)), SealerAvailable: svc.SealerWired()}
	// Whether the caller administers the tenant matters only to its mcp/ and env/ names,
	// so it is asked when the first one shows up.
	var tenantAdmin, adminKnown bool
	for _, v := range views {
		if scope != auth.GlobalSecretScope {
			if !adminKnown && !strings.HasPrefix(v.Name, auth.PersonalVaultPrefix) {
				var aerr error
				if tenantAdmin, aerr = s.isTenantAdmin(r, mc.Principal, scope); aerr != nil {
					s.writeError(w, r, aerr)
					return
				}
				adminKnown = true
			}
			if !tenantSecretVisible(mc.Principal, v.Name, tenantAdmin) {
				continue
			}
		}
		out.Secrets = append(out.Secrets, toSecretDTO(v))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutSecret(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	scope := mc.Tenant
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	var in secretInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if !s.secretNameInScope(w, r, p, scope, in.Name) {
		return
	}
	// Sessions read env/ secrets only in the tenant scope. Keep legacy global
	// values readable and deletable, but refuse misleading writes here.
	if scope == auth.GlobalSecretScope && strings.HasPrefix(strings.TrimSpace(in.Name), "env/") {
		s.badRequest(w, r, "session secrets belong in the tenant store; use New session > More options > Manage session secrets, or scope=tenant")
		return
	}
	var view auth.SecretView
	var err error
	vault := scope != auth.GlobalSecretScope && strings.HasPrefix(in.Name, auth.PersonalVaultPrefix)
	if vault && in.Value != "" {
		// A personal vault item is a document, validated before it is sealed; its kind
		// becomes the entry's hint.
		item, perr := auth.ParseVaultItem(in.Value)
		if perr != nil {
			s.writeError(w, r, perr)
			return
		}
		view, err = svc.PutVault(r.Context(), p, scope, in.Name, item, in.Description)
	} else {
		// An edit with no value keeps the sealed item, and its description is still bounded.
		if vault {
			if derr := auth.ValidateVaultDescription(in.Description); derr != nil {
				s.writeError(w, r, derr)
				return
			}
		}
		view, err = svc.Put(r.Context(), p, scope, in.Name, in.Value, in.Description)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecretDTO(view))
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	scope := mc.Tenant
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	var in secretInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if !s.secretNameInScope(w, r, p, scope, in.Name) {
		return
	}
	if err := svc.Delete(r.Context(), p, scope, in.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Reveal budget per person: a burst of 20, refilled at 20 a minute. A person reads a
// handful of items; a stolen session scraping the vault does not get further.
// Per node: a shared store would make it global (ponytail: per-node, upgrade with HA).
const revealTier = "reveal"

var revealLimit = ratelimit.Limit{Rate: 20.0 / 60, Burst: 20}

// newRevealLimiter builds the limiter handleRevealSecret meters each person with.
func newRevealLimiter() *ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{
		Tiers: map[string]ratelimit.TierLimits{
			revealTier: {PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{ratelimit.ClassRead: revealLimit}, Total: revealLimit},
		},
		DefaultTier: revealTier,
	}, nil)
}

// handleRevealSecret returns the plaintext of one personal vault item to its owner:
// the single place the sealed store hands a value to a person. It answers only a
// signed-in person (a token never passes the step-up), only for an item in their
// own vault/<user-id>/ namespace of the tenant scope: global, mcp/ and env/ names,
// and everyone else's items, stay write-only. Each success is one ledger entry
// without the value (SecretStore.Reveal), and the response is never cached.
func (s *Server) handleRevealSecret(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	w.Header().Set("Cache-Control", "no-store")
	p := mc.Principal
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	var in secretInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if mc.Tenant == auth.GlobalSecretScope || !ownsVaultItem(p, in.Name) {
		s.writeError(w, r, errForbidden)
		return
	}
	if dec := s.revealRL.Allow(r.Context(), p.UserID.String(), revealTier, ratelimit.ClassRead); !dec.OK {
		w.Header().Set("Retry-After", strconv.Itoa(dec.RetryAfter))
		s.writeError(w, r, errRateLimited)
		return
	}
	value, err := svc.Reveal(r.Context(), p, mc.Tenant, in.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": strings.TrimSpace(in.Name), "value": string(value)})
}

// handleVaultTOTP returns the current authenticator code of one of the caller's own vault
// items and the seconds it has left. It is guarded exactly like reveal (a signed-in person,
// the deployment's step-up, the owner of the item, the same per-person budget), and it is
// one ledger entry (secret.totp) without the code. The seed stays in the store: neither this
// answer nor a list carries it.
func (s *Server) handleVaultTOTP(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	w.Header().Set("Cache-Control", "no-store")
	p := mc.Principal
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	var in secretInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if mc.Tenant == auth.GlobalSecretScope || !ownsVaultItem(p, in.Name) {
		s.writeError(w, r, errForbidden)
		return
	}
	if dec := s.revealRL.Allow(r.Context(), p.UserID.String(), revealTier, ratelimit.ClassRead); !dec.OK {
		w.Header().Set("Retry-After", strconv.Itoa(dec.RetryAfter))
		s.writeError(w, r, errRateLimited)
		return
	}
	code, err := svc.VaultTOTP(r.Context(), p, mc.Tenant, in.Name, time.Now())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, code)
}

// handleGeneratePassword returns a random password for the signed-in member. Nothing is
// stored and nothing is recorded: it is the same generator the CLI runs locally. Options
// the body omits keep their defaults (20 characters of all four kinds).
func (s *Server) handleGeneratePassword(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	w.Header().Set("Cache-Control", "no-store")
	opts := auth.DefaultPasswordOptions()
	if err := decodeJSON(w, r, &opts); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	password, err := auth.GeneratePassword(opts)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"password": password})
}
