// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
)

// The stability tests are the enforcement half of the public API stability
// policy (docs-site: reference/api-stability): every deprecation declared in
// core/api/stability.go must reference a real route, carry the announcement
// date, and schedule its sunset no earlier than the tier's minimum support
// window (stable: 24 months, beta: 12). A violating entry fails the build.

// collectRoutes walks the built router and returns the spec-canonical
// "METHOD path" set, exactly as the deprecation middleware will match them.
func collectRoutes(t *testing.T, h *harness) map[string]bool {
	t.Helper()
	router, ok := h.srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	routes := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+api.CanonicalRoutePatternForTest(route)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

func TestStabilityPolicyWindows(t *testing.T) {
	routes := collectRoutes(t, newHarness(t))
	for _, d := range api.RouteDeprecationsForTest() {
		key := d.Method + " " + d.Path
		if d.Method != strings.ToUpper(d.Method) {
			t.Errorf("%s: method must be upper-case", key)
		}
		if !strings.HasPrefix(d.Path, "/") || (len(d.Path) > 1 && strings.HasSuffix(d.Path, "/")) {
			t.Errorf("%s: path must be spec-canonical (leading slash, no trailing slash)", key)
		}
		if !routes[key] {
			t.Errorf("%s: deprecates a route that is not registered", key)
		}
		if d.Tier != api.StabilityStable && d.Tier != api.StabilityBeta {
			t.Errorf("%s: unknown stability tier %q", key, d.Tier)
		}
		if d.DeprecatedAt.IsZero() {
			t.Errorf("%s: a deprecation must carry its announcement date", key)
		}
		if d.Docs == "" {
			t.Errorf("%s: a real deprecation must ship its migration guide URL", key)
		}
		if d.SunsetAt.IsZero() {
			continue
		}
		months := api.MinSupportWindowMonthsForTest(d.Tier)
		if earliest := d.DeprecatedAt.AddDate(0, months, 0); d.SunsetAt.Before(earliest) {
			t.Errorf("%s: sunset %s violates the %d-month %s window (earliest allowed %s)",
				key, d.SunsetAt.Format(time.RFC3339), months, d.Tier, earliest.Format(time.RFC3339))
		}
	}
}

// get performs a raw request so response HEADERS can be asserted (the harness
// do helper only surfaces the body).
func rawGet(h *harness, path, token string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDeprecationHeaders(t *testing.T) {
	depAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sunAt := time.Date(2028, 6, 1, 0, 0, 0, 0, time.UTC)
	guide := "https://docs.olivares.invalid/how-to/migrate-example/"
	restore := api.SwapRouteDeprecationsForTest([]api.RouteDeprecation{
		// Scheduled sunset + guide on a simple unauthenticated route.
		{Method: "GET", Path: "/v1/server-info", Tier: api.StabilityStable,
			DeprecatedAt: depAt, SunsetAt: sunAt, Docs: guide},
		// Announced-only (no sunset, no guide yet → policy link), beta tier.
		{Method: "GET", Path: "/healthz", Tier: api.StabilityBeta, DeprecatedAt: depAt},
		// Collection route: registered as Route("/agents", Get("/")) — the chi
		// pattern carries a trailing slash the table key must not need.
		{Method: "GET", Path: "/v1/agents", Tier: api.StabilityStable,
			DeprecatedAt: depAt, SunsetAt: sunAt, Docs: guide},
		// Module route under /v1/m/<ns>/.
		{Method: "GET", Path: "/v1/m/demo/things", Tier: api.StabilityBeta,
			DeprecatedAt: depAt, Docs: guide},
	})
	defer restore()
	h := newHarness(t, demoModule{})

	rec := rawGet(h, "/v1/server-info", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/server-info = %d", rec.Code)
	}
	if got, want := rec.Header().Get("Deprecation"), "@"+strconv.FormatInt(depAt.Unix(), 10); got != want {
		t.Errorf("Deprecation = %q, want %q (RFC 9745 Structured Field Date)", got, want)
	}
	if got, want := rec.Header().Get("Sunset"), sunAt.Format(http.TimeFormat); got != want {
		t.Errorf("Sunset = %q, want %q (RFC 8594 HTTP-date)", got, want)
	}
	links := rec.Header().Values("Link")
	wantLinks := []string{"<" + guide + `>; rel="deprecation"`, "<" + guide + `>; rel="sunset"`}
	for _, want := range wantLinks {
		found := false
		for _, l := range links {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Link headers %v missing %q", links, want)
		}
	}

	// Announced-only: Deprecation but no Sunset; the Link falls back to the policy.
	rec = rawGet(h, "/healthz", "", nil)
	if rec.Header().Get("Deprecation") == "" {
		t.Error("/healthz: missing Deprecation header")
	}
	if got := rec.Header().Get("Sunset"); got != "" {
		t.Errorf("/healthz: unscheduled sunset must not emit Sunset (got %q)", got)
	}
	// RELAJADO a proposito el 2026-08-19, y aqui esta el motivo para poder revertirlo:
	// esta asercion exigia que la URL nombrara la PAGINA (`api-stability`). La URL
	// apuntaba a docs.olivares.ai, que no resuelve, y la pagina solo existe en el
	// docs-site sin desplegar. Publicarla en olivares.ai/docs cuesta 13 locales -- el
	// sitio enlaza cada pagina a todos -- y el canon pide `sol max` para traduccion
	// tecnica. Asi que la URL pasa a la RAIZ de las docs, que si resuelve, y esto
	// comprueba el host canonico en vez del nombre de la pagina. Cuando la pagina se
	// publique, devolved la cadena a "reference/api-stability".
	if links := rec.Header().Values("Link"); len(links) != 1 || !strings.Contains(links[0], "olivares.ai/docs") {
		t.Errorf("/healthz: want a single policy deprecation link, got %v", links)
	}

	// A route NOT in the table stays silent.
	rec = rawGet(h, "/livez", "", nil)
	if got := rec.Header().Get("Deprecation"); got != "" {
		t.Errorf("/livez: unexpected Deprecation header %q", got)
	}

	// Pre-routing middleware responses must STILL carry the signal: the setup
	// gate answers 409 before chi routes, so the writer resolves the pattern by
	// matching the request against the router (the policy promises the headers
	// on every response of a deprecated route).
	rec = rawGet(h, "/v1/agents", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("pre-setup /v1/agents = %d, want 409", rec.Code)
	}
	if rec.Header().Get("Deprecation") == "" {
		t.Error("setup-gate 409 on a deprecated route lost the Deprecation header")
	}

	// Authenticated surfaces: the collection route (trailing-slash chi pattern)
	// and a module route, exercising canonicalisation through real auth.
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Same for an auth-middleware 401 (post-setup, invalid bearer).
	rec = rawGet(h, "/v1/agents", "olvk_invalid_token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid-token /v1/agents = %d, want 401", rec.Code)
	}
	if rec.Header().Get("Deprecation") == "" {
		t.Error("authenticate 401 on a deprecated route lost the Deprecation header")
	}

	// An unrouted path (now past the setup gate) reaches chi's NotFound: no
	// signal, no panic in the writer wrapper.
	rec = rawGet(h, "/v1/definitely-not-a-route", admin, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unrouted path = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Deprecation"); got != "" {
		t.Errorf("404: unexpected Deprecation header %q", got)
	}
	rec = rawGet(h, "/v1/agents", admin, tenantHdr(tenant))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/agents = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Deprecation") == "" {
		t.Error("/v1/agents: missing Deprecation header on collection route")
	}
	rec = rawGet(h, "/v1/m/demo/things", admin, tenantHdr(tenant))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/m/demo/things = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Deprecation") == "" {
		t.Error("module route: missing Deprecation header")
	}
}

func TestDeprecationMiddlewareAbsentWhenTableEmpty(t *testing.T) {
	restore := api.SwapRouteDeprecationsForTest(nil)
	defer restore()
	h := newHarness(t)
	rec := rawGet(h, "/healthz", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Deprecation") != "" {
		t.Fatalf("empty table: code=%d Deprecation=%q", rec.Code, rec.Header().Get("Deprecation"))
	}
}

func TestOpenAPIStabilityExtensions(t *testing.T) {
	depAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sunAt := time.Date(2028, 6, 1, 0, 0, 0, 0, time.UTC)
	guide := "https://docs.olivares.invalid/how-to/migrate-example/"
	restore := api.SwapRouteDeprecationsForTest([]api.RouteDeprecation{
		{Method: "GET", Path: "/v1/agents", Tier: api.StabilityStable,
			DeprecatedAt: depAt, SunsetAt: sunAt, Docs: guide},
	})
	defer restore()

	doc := api.OpenAPIDocument()
	info := doc["info"].(map[string]any)
	// RELAJADO a proposito el 2026-08-19, y aqui esta el motivo para poder revertirlo:
	// esta asercion exigia que la URL nombrara la PAGINA (`api-stability`). La URL
	// apuntaba a docs.olivares.ai, que no resuelve, y la pagina solo existe en el
	// docs-site sin desplegar. Publicarla en olivares.ai/docs cuesta 13 locales -- el
	// sitio enlaza cada pagina a todos -- y el canon pide `sol max` para traduccion
	// tecnica. Asi que la URL pasa a la RAIZ de las docs, que si resuelve, y esto
	// comprueba el host canonico en vez del nombre de la pagina. Cuando la pagina se
	// publique, devolved la cadena a "reference/api-stability".
	if pol, _ := info["x-stability-policy"].(string); !strings.Contains(pol, "olivares.ai/docs") {
		t.Errorf("info.x-stability-policy = %v", info["x-stability-policy"])
	}
	paths := doc["paths"].(map[string]any)
	get := paths["/v1/agents"].(map[string]any)["get"].(map[string]any)
	if get["deprecated"] != true {
		t.Error("deprecated operation missing deprecated: true")
	}
	if got := get["x-deprecated-at"]; got != "2026-06-01T00:00:00Z" {
		t.Errorf("x-deprecated-at = %v", got)
	}
	if got := get["x-sunset-at"]; got != "2028-06-01T00:00:00Z" {
		t.Errorf("x-sunset-at = %v", got)
	}
	if got := get["x-migration-guide"]; got != guide {
		t.Errorf("x-migration-guide = %v", got)
	}
	if got := get["x-stability"]; got != "stable" {
		t.Errorf("x-stability = %v", got)
	}
	// Sibling operation on the same path is untouched but still tiered.
	post := paths["/v1/agents"].(map[string]any)["post"].(map[string]any)
	if _, ok := post["deprecated"]; ok {
		t.Error("non-deprecated sibling operation gained deprecated flag")
	}
	if got := post["x-stability"]; got != "stable" {
		t.Errorf("sibling x-stability = %v", got)
	}
}
