// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

// fakeSso is the read seam over the engine's federation posture.
type fakeSso struct {
	protocol string
	err      error
	calls    int
}

func (f *fakeSso) SsoPosture(context.Context) (string, error) {
	f.calls++
	return f.protocol, f.err
}

func ssoHarness(t *testing.T, p governance.SsoPostureProvider) (*harness, string) {
	t.Helper()
	var opts []governance.IdentityConsoleOption
	if p != nil {
		opts = append(opts, governance.WithSsoPostureProvider(p))
	}
	h := newHarnessWith(t, harnessOpts{identityOpts: opts})
	tenant, admin := h.tenantAdmin()
	return h, admin + "\x00" + tenant.String()
}

// ssoGet issues the request; the tenant travels with the token through the packed pair
// so the table-style tests below stay one-liners.
func (h *harness) ssoGet(packed string) resp {
	h.t.Helper()
	parts := strings.SplitN(packed, "\x00", 2)
	return h.do("GET", "/v1/m/identity/sso", parts[0], nil, map[string]string{"X-Olivares-Tenant": parts[1]})
}

// THE ROUTE MUST EXIST. This is the ONE 404 the browser walk actually reproduced
// (scripts/console-walk.mjs against a live engine): the federation tab is the identity
// screen's DEFAULT tab, so /sso is the only identity call a default page load makes.
func TestSsoStatusRouteIsRegistered(t *testing.T) {
	h, p := ssoHarness(t, &fakeSso{})
	if r := h.ssoGet(p); r.code == http.StatusNotFound {
		t.Fatal("GET /v1/m/identity/sso = 404: the console's default tab calls it on every load")
	}
}

// UNCONFIGURED IS A STATE, NOT AN ABSENCE. The engine says so at boot ("env SSO not
// configured; SSO defers to managed config or answers 501"); the console must be able to
// render that as the explicit "not configured" panel, not as a pending seam.
func TestSsoStatusUnconfiguredIsAnExplicitState(t *testing.T) {
	h, p := ssoHarness(t, &fakeSso{protocol: ""})
	r := h.ssoGet(p)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", r.code, r.raw)
	}
	if configured, _ := r.body["configured"].(bool); configured {
		t.Error("configured = true with no provider")
	}
	if proto, _ := r.body["protocol"].(string); proto != "" {
		t.Errorf("protocol = %q, want empty", proto)
	}
}

func TestSsoStatusReportsTheActiveProtocol(t *testing.T) {
	for _, proto := range []string{"oidc", "saml"} {
		h, p := ssoHarness(t, &fakeSso{protocol: proto})
		r := h.ssoGet(p)
		if r.code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", proto, r.code, r.raw)
		}
		if configured, _ := r.body["configured"].(bool); !configured {
			t.Errorf("%s configured = false though a provider is active", proto)
		}
		if got, _ := r.body["protocol"].(string); got != proto {
			t.Errorf("protocol = %q, want %q", got, proto)
		}
	}
}

// PKCE IS REFLECTED, NEVER TOGGLED. Core is always S256 (never plain); the panel renders
// what the server reports, so the server must report it.
func TestSsoStatusReflectsS256PKCE(t *testing.T) {
	h, p := ssoHarness(t, &fakeSso{protocol: "oidc"})
	r := h.ssoGet(p)
	if got, _ := r.body["pkce_method"].(string); got != "S256" {
		t.Errorf("pkce_method = %q, want S256", got)
	}
}

// THE REDIRECT URI IS SERVER-DERIVED and must be the EXACT callback the provider
// registers (RFC 9700 exact match) — the same rule the login leg uses, not a copy that
// can drift. The panel computes one client-side only until the backend lands; this is it.
func TestSsoStatusServesTheExactCallbackURI(t *testing.T) {
	h, p := ssoHarness(t, &fakeSso{protocol: "oidc"})
	r := h.ssoGet(p)
	got, _ := r.body["redirect_uri"].(string)
	if !strings.HasSuffix(got, "/v1/auth/federation/callback") {
		t.Errorf("redirect_uri = %q, want the federation callback path", got)
	}
	if !strings.HasPrefix(got, "http://") && !strings.HasPrefix(got, "https://") {
		t.Errorf("redirect_uri = %q, want an absolute URL", got)
	}
}

// A POSTURE READ FAULT DEGRADES HONESTLY — never a 500, never a fabricated
// "unconfigured" (which would tell an operator SSO is off when we simply could not look).
func TestSsoStatusReadFailureIsNotReportedAsUnconfigured(t *testing.T) {
	h, p := ssoHarness(t, &fakeSso{err: errors.New("store unavailable")})
	r := h.ssoGet(p)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", r.code, r.raw)
	}
	if reason, _ := r.body["reason"].(string); strings.TrimSpace(reason) == "" {
		t.Error("a failed posture read must carry a reason, not a silent configured=false")
	}
	if strings.Contains(r.raw, "store unavailable") {
		t.Errorf("response echoed the internal error: %s", r.raw)
	}
}

// UNWIRED PROVIDER: the route still answers, with a reason.
func TestSsoStatusWithNoProviderAnswersWithAReason(t *testing.T) {
	h, p := ssoHarness(t, nil)
	r := h.ssoGet(p)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", r.code, r.raw)
	}
	if reason, _ := r.body["reason"].(string); strings.TrimSpace(reason) == "" {
		t.Error("no provider wired and no reason given")
	}
}

// THE DECLARED PERMISSION GATES IT, proved by a deny on exactly that resource+verb.
func TestSsoStatusSitsBehindTheDeclaredPermission(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{identityOpts: []governance.IdentityConsoleOption{
		governance.WithSsoPostureProvider(&fakeSso{protocol: "oidc"}),
	}})
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	packed := tok + "\x00" + tenant.String()
	if r := h.ssoGet(packed); r.code != http.StatusOK {
		t.Fatalf("control = %d, want 200 before any deny: %s", r.code, r.raw)
	}
	h.authorPolicy(root, tenant, "no-posture-reads", map[string]any{"rules": []any{
		map[string]any{"deny": true, "resource": "idposture", "verb": "read"},
	}})
	if r := h.ssoGet(packed); r.code != http.StatusForbidden {
		t.Errorf("under a deny on governance:idposture:read = %d, want 403: %s", r.code, r.raw)
	}
}
