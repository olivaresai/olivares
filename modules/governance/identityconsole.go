// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"log/slog"
	"net/http"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// IdentityConsole is the E read-only WIF-graph backend the identity console
// calls at GET /v1/m/identity/wif. It serves the federation object graph
// (fdis_/fdrl_/svac_ + the static-key footgun) the in-browser WIF linter needs: the
// LIVE config reconciled against the operator-declared rules (declared|live|both
// provenance + drift) when the claude-wif source carries an org:admin OAuth token, else
// the declared graph. It is READ-ONLY — there is NO create/edit/delete affordance (WIF
// objects are created/edited in the Anthropic Console; this lists and lints them). MINIMAL
// DATA (docs/SECURITY-HARDENING.md): it emits `ca_cert_configured` as a presence boolean ONLY — never a
// ca_cert_pem, an sk-ant- key, an org:admin token, or a JWT-SVID.
type IdentityConsole struct {
	log  *slog.Logger
	data api.ModuleData
	wif  WifGraphProvider
	// sso is the federation-posture seam behind GET /sso. Nil ⇒ the route answers
	// with a reason. See identitysso.go.
	sso SsoPostureProvider
	// posture is the Admin-API governance seam (External Keys / workspace
	// residency). Nil ⇒ those routes answer available=false with a reason; they are
	// mounted either way, because a mounted route that says "not wired" is an answer
	// and a 404 is a defect. See identityposture.go.
	posture IdentityPostureProvider
}

var (
	_ sdk.Module       = (*IdentityConsole)(nil)
	_ api.Module       = (*IdentityConsole)(nil)
	_ api.DataConsumer = (*IdentityConsole)(nil)
)

// IdentityConsoleNamespace mounts the console at /v1/m/identity/.
const IdentityConsoleNamespace = "identity"

// WifGraphProvider supplies the operator-declared WIF object graph for a tenant (from
// the claude-wif connector's declared federation rules + footgun). nil/ok=false ⇒ no
// federation is declared (an honest empty graph, never a fabricated one).
type WifGraphProvider interface {
	WifGraph(ctx context.Context, tenant model.TenantID) (claudewif.WIFGraph, bool)
}

// IdentityConsoleOption configures an IdentityConsole.
type IdentityConsoleOption func(*IdentityConsole)

// WithWifGraphProvider wires the declared WIF graph source.
func WithWifGraphProvider(p WifGraphProvider) IdentityConsoleOption {
	return func(c *IdentityConsole) { c.wif = p }
}

// NewIdentityConsole constructs the WIF-graph console.
func NewIdentityConsole(opts ...IdentityConsoleOption) *IdentityConsole {
	c := &IdentityConsole{}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *IdentityConsole) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name: "olivares.identity-console", Version: "0.1.0", APIVersion: sdk.APIVersion,
		Type: sdk.TypeModule, Title: "Identity & WIF graph (read-only)",
		Description: "Serves the operator-declared Workload Identity Federation object graph (issuers/rules/service-accounts + static-key footgun) the console lints. Read-only; no key material over the wire.",
	}
}

func (c *IdentityConsole) UseData(d api.ModuleData) { c.data = d }
func (c *IdentityConsole) Init(_ context.Context, host sdk.Host) error {
	c.log = host.Logger()
	return nil
}
func (c *IdentityConsole) Start(context.Context) error { return nil }
func (c *IdentityConsole) Stop(context.Context) error  { return nil }
func (c *IdentityConsole) APINamespace() string        { return IdentityConsoleNamespace }

// Permissions declares only what this console's own routes require.
//
// permIdentityRead was declared here too until the permission inventory could
// run again and named it: not one route below requires it, and the roster routes
// that do (GET /identities, /groups, /groups/{ref}/members) belong to the governance
// module, which declares it at its own Permissions(). A second module declaring a
// permission it does not enforce is the exact shape was written against, only
// inverted — there a guard had no declaration, here a declaration had no guard — and
// it is worse than harmless: Permissions() feeds the scope-grantable catalog, so it
// offered an operator a permission this console never checks. Removing it loses
// nothing, because governance still declares AND mounts it.
func (c *IdentityConsole) Permissions() []auth.Permission {
	return []auth.Permission{permIdentityPostureRead}
}

// APIRoutes mounts the console's read-only routes. All FOUR take
// governance:idposture:read — the permission this console DECLARES in Permissions()
// above, so the guard and the registration are the same fact (a guard whose
// permission the module never registers stops guarding without saying so).
//
// This sentence read "all three take governance:identity:read" until and it was
// wrong in both halves while every test stayed green: there are four routes and none
// of them takes that permission. A comment naming the wrong permission on an
// authorization seam is not a typo — it is the same claim-versus-code defect the
// permission inventory exists to make impossible to hold.
//
// STILL NOT MOUNTED, and named rather than left silent: /tls and /crypto-inventory
// (web/src/features/identity/api.ts:174,176). The engine knows its own serving
// certificate and its own signing algorithms, but the console labels those sections
// cert-manager TLS and a PQC key inventory (verified ABSENT) — serving the
// engine's self-signed cert under a cert-manager label would fabricate MEANING even
// though the bytes are real. They are declared in the console-route seam register
// (scripts/console-route-seams.json) with that reason, so the class test fails
// the day a route is added without one.
func (c *IdentityConsole) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/wif", permIdentityPostureRead, c.handleWifGraph)
	reg.Handle("GET", "/sso", permIdentityPostureRead, c.handleSsoStatus)
	reg.Handle("GET", "/external-keys", permIdentityPostureRead, c.handleExternalKeys)
	reg.Handle("GET", "/residency", permIdentityPostureRead, c.handleResidency)
}

// handleWifGraph serves the declared WIF object graph. Viewing the federation graph is
// a privileged, recon-relevant read, so it self-audits in a committed
// transaction BEFORE returning — deny-closed: if the audit write fails, the graph is
// NOT served. The graph carries `ca_cert_configured` as a boolean only; no PEM/key/SVID.
func (c *IdentityConsole) handleWifGraph(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	graph := claudewif.WIFGraph{
		Issuers:         []claudewif.WIFIssuer{},
		Rules:           []claudewif.WIFRule{},
		ServiceAccounts: []claudewif.WIFServiceAccount{},
	}
	if c.wif != nil {
		if g, ok := c.wif.WifGraph(r.Context(), mc.Tenant); ok {
			graph = g
		}
	}
	// Self-audit the privileged read (committed); deny-closed on an audit-write failure.
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "governance.identity.wif.read", model.Kind("anthropic.federation"), "", nil)
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}
