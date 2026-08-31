// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GET /v1/m/identity/sso, the SSO connection-state summary the identity console's
// DEFAULT tab requests on every page load. It is the one 404 the browser walk actually
// reproduced (the other declared routes live behind tabs that never mount), and it had no
// backend at all: the console's own type says so in writing — "the panel also computes
// this client-side from the origin so it can be shown BEFORE THE BACKEND LANDS"
// (web/src/features/identity/types.ts:171-172). This is the backend landing.
//
// IT REPORTS, IT NEVER CONFIGURES. SSO configuration is authored elsewhere
// (/v1/console/sso, superadmin-only) and read once at boot from the environment or the
// managed config. This route is a read-only mirror of the resulting posture.
//
// THREE ANSWERS, NEVER TWO:
//   - configured=true  + protocol      — an active IdP backs SSO
//   - configured=false + no reason     — SSO is genuinely OFF (a real, known state the
//     console renders explicitly, distinct from a pending seam)
//   - configured=false + reason        — we could NOT determine it (no provider wired, or
//     the posture read failed). Reporting this as "off" would tell an operator SSO is
//     disabled when the truth is that we did not look.

// SsoPostureProvider reads the deployment's federation posture. It returns the protocol
// the ACTIVE provider speaks ("oidc"/"saml"), or "" when SSO is not configured. It is
// read-only: there is no method here that can change a federation setting.
type SsoPostureProvider interface {
	SsoPosture(ctx context.Context) (protocol string, err error)
}

// UseSsoPosture late-binds the federation posture source. The federation service is
// built AFTER the module set (it needs the store and the config sealer), so boot() wires
// this once it exists — the same late-bind pattern the knowledge guard and the deploy
// binder already use. Until then, and in a build with no federation service, GET /sso
// answers with a reason rather than a fabricated "not configured".
func (c *IdentityConsole) UseSsoPosture(p SsoPostureProvider) { c.sso = p }

// WithSsoPostureProvider wires the federation posture source. Without it GET /sso still
// answers — with a reason — because a mounted route that says "I cannot tell you" is an
// answer and a 404 is a defect.
func WithSsoPostureProvider(p SsoPostureProvider) IdentityConsoleOption {
	return func(c *IdentityConsole) { c.sso = p }
}

const (
	reasonSsoUnwired = "the engine's federation posture is not readable from this build; the SSO connection state cannot be reported"
	reasonSsoFailed  = "the SSO connection state is temporarily unavailable"
	// pkceS256 is the ONLY PKCE method core speaks. The console reflects it; it is not
	// a toggle, so it is reported as a constant rather than read from anywhere.
	pkceS256 = "S256"
)

// ssoStatusDTO mirrors web/src/features/identity/types.ts:167 (SsoStatus), plus the
// `reason` that carries the third answer.
type ssoStatusDTO struct {
	Protocol    string `json:"protocol"`
	Configured  bool   `json:"configured"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	PKCEMethod  string `json:"pkce_method,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// handleSsoStatus serves the SSO connection state. The redirect_uri is derived through
// core's OWN rule (api.FederationCallbackURL) rather than a copy, so what the panel shows
// an operator to paste into their IdP is exactly what the login leg will send — including
// under a trusted reverse proxy.
func (c *IdentityConsole) handleSsoStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := ssoStatusDTO{
		RedirectURI: api.FederationCallbackURL(r),
		PKCEMethod:  pkceS256,
	}
	switch {
	case c.sso == nil:
		out.Reason = reasonSsoUnwired
	default:
		protocol, err := c.sso.SsoPosture(r.Context())
		if err != nil {
			// The posture read can surface store/config internals — log it, never return it.
			if c.log != nil {
				c.log.Warn("identity: SSO posture read failed; reporting unavailable rather than 'not configured'", "err", err)
			}
			out.Reason = reasonSsoFailed
			break
		}
		out.Protocol = protocol
		out.Configured = protocol != ""
	}
	// Reading whether and how an estate federates is recon-relevant, on the same terms
	// as the WIF graph and the CMEK posture next door: audited deny-closed before the
	// answer is served.
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "governance.identity.sso.read", model.Kind("anthropic.federation"), "", nil)
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
