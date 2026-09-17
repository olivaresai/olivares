// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/auth"
)

// noStoreProbeModule is a three-route module that exists only to measure the
// no-store route capability at its two boundaries: the engine's own refusals,
// which are written before the module handler runs, and a plain sibling route,
// which must be untouched.
type noStoreProbeModule struct{}

func (noStoreProbeModule) APINamespace() string { return "nostoreprobe" }

func (noStoreProbeModule) Permissions() []auth.Permission {
	return []auth.Permission{"nostoreprobe:probe:read", "nostoreprobe:probe:admin"}
}

func (m noStoreProbeModule) APIRoutes(reg api.RouteRegistrar) {
	noStore, ok := reg.(api.NoStoreRouteRegistrar)
	if !ok {
		// The registrar the engine hands a module MUST carry the capability; a
		// silent fallback here would make the whole measurement vacuous.
		panic("api: the mounting registrar does not implement NoStoreRouteRegistrar")
	}
	noStore.HandleNoStore("GET", "/declared", "nostoreprobe:probe:read", m.ok)
	// A route the caller's role does not grant: the 403 is written by the engine
	// BEFORE the handler, which is the case a handler-side header cannot reach.
	noStore.HandleNoStore("GET", "/denied", "nostoreprobe:probe:admin", m.ok)
	// The control: same module, same seam, no declaration.
	reg.Handle("GET", "/plain", "nostoreprobe:probe:read", m.ok)
}

func (noStoreProbeModule) ok(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// TestNoStoreRouteCapabilityCoversRefusalsAndOnlyItsOwnRoutes proves everything
// the capability claims, INCLUDING the two refusals it used to concede.
//
//  1. A declared route's SUCCESS carries `Cache-Control: no-store`.
//  2. A declared route's ENGINE REFUSAL carries it too: the authorization denial
//     is written before the module handler exists, so a `w.Header().Set` inside
//     the handler can never reach it, and a 403 or a concealed 404 is precisely
//     the answer a private cache must not replay.
//  3. A declared route's PRE-ROUTING refusals carry it as well — the 401 an
//     INVALID SUPPLIED credential earns from `authenticate` and the 429 a
//     throttled caller earns from `rateLimit`, both written before chi routes the
//     request at all. An independent HTTP witness measured both of these ABSENT
//     on the first correction, which is why they are asserted here by driving the
//     real middlewares rather than by reading the chain.
//  4. A route that did NOT declare it is untouched — on the success path AND on
//     both pre-routing refusals. That is the term that keeps this a route-specific
//     declaration instead of a global cache policy, and it is the one an
//     implementation that simply stamped every response would break.
//
// ⚠ 401 HAS TWO PATHS AND THEY ARE NOT THE SAME TEST. An ABSENT credential reaches
// the route anonymously and is refused inside it; an INVALID one is refused by the
// middleware before routing. The first correction covered only the first and
// reported "401 is covered", which is the confusion this test now separates by
// name.
func TestNoStoreRouteCapabilityCoversRefusalsAndOnlyItsOwnRoutes(t *testing.T) {
	clock := &rlClock{t: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.RateLimit = rlTestConfig(clock.now, ratelimit.ModeEnforce)
	}, noStoreProbeModule{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "nostore")
	token := h.tenantToken(admin, tenant, "probe@nostore.io")

	success := h.do("GET", "/v1/m/nostoreprobe/declared", token, nil, tenantHdr(tenant))
	if success.code != http.StatusOK {
		t.Fatalf("declared route = %d %s", success.code, success.raw)
	}
	if got := success.hdr.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("declared route success Cache-Control = %q, want no-store", got)
	}

	denied := h.do("GET", "/v1/m/nostoreprobe/denied", token, nil, tenantHdr(tenant))
	if denied.code != http.StatusForbidden {
		t.Fatalf("denied route = %d %s, want 403", denied.code, denied.raw)
	}
	if got := denied.hdr.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("ENGINE REFUSAL Cache-Control = %q, want no-store — a denial written "+
			"before the module handler is exactly what a handler-side header misses", got)
	}

	plain := h.do("GET", "/v1/m/nostoreprobe/plain", token, nil, tenantHdr(tenant))
	if plain.code != http.StatusOK {
		t.Fatalf("plain route = %d %s", plain.code, plain.raw)
	}
	if got := plain.hdr.Get("Cache-Control"); got != "" {
		t.Fatalf("plain sibling route Cache-Control = %q, want empty — the capability "+
			"must not become a global transport rule", got)
	}

	// ── (3a) The INVALID-CREDENTIAL 401, refused by authenticate before routing ──
	invalid := h.do("GET", "/v1/m/nostoreprobe/declared", "not-a-real-bearer-token",
		nil, tenantHdr(tenant))
	if invalid.code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer = %d %s, want 401", invalid.code, invalid.raw)
	}
	invalidCache := invalid.hdr.Get("Cache-Control")
	t.Logf("K3_NOSTORE_PREROUTING|probe=invalid_bearer_401|status=%d|cache_control=%q",
		invalid.code, invalidCache)
	if invalidCache != "no-store" {
		t.Fatalf("invalid-credential 401 Cache-Control = %q, want no-store. This refusal "+
			"is written by `authenticate` BEFORE chi routes the request, and it is one of "+
			"the two an independent witness measured missing", invalidCache)
	}
	// And the SIBLING under the same refusal is still untouched: this is what
	// separates a route-specific resolution from a global policy.
	invalidPlain := h.do("GET", "/v1/m/nostoreprobe/plain", "not-a-real-bearer-token",
		nil, tenantHdr(tenant))
	if invalidPlain.code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer on the plain sibling = %d %s, want 401",
			invalidPlain.code, invalidPlain.raw)
	}
	if got := invalidPlain.hdr.Get("Cache-Control"); got != "" {
		t.Fatalf("the plain sibling's pre-routing 401 carries Cache-Control=%q; the "+
			"resolution has become a global policy rather than a route's own", got)
	}
	// An UNROUTABLE path must also stay untouched: nothing is declared there, and a
	// resolver that fell back to "some declared route" would be a leak.
	unknown := h.do("GET", "/v1/m/nostoreprobe/no-such-route", "not-a-real-bearer-token",
		nil, tenantHdr(tenant))
	if got := unknown.hdr.Get("Cache-Control"); got != "" {
		t.Fatalf("an unrouted path's refusal carries Cache-Control=%q", got)
	}

	// ── (3b) The LIMITER's 429, written before routing too ──
	// The limiter's frozen clock means the read burst does not refill, so a bounded
	// loop reaches the denial deterministically.
	var limited resp
	for attempt := 0; attempt < 12 && limited.code != http.StatusTooManyRequests; attempt++ {
		limited = h.do("GET", "/v1/m/nostoreprobe/declared", token, nil, tenantHdr(tenant))
	}
	if limited.code != http.StatusTooManyRequests {
		t.Fatalf("the tight read bucket never produced a 429; last = %d %s", limited.code, limited.raw)
	}
	cacheControl := limited.hdr.Get("Cache-Control")
	t.Logf("K3_NOSTORE_PREROUTING|probe=authenticated_limit_429|status=429|cache_control=%q",
		cacheControl)
	if cacheControl != "no-store" {
		t.Fatalf("the rate limiter's 429 on a DECLARED route carries Cache-Control=%q, "+
			"want no-store. The published contract now declares the header on 429 for "+
			"these routes, so an omission here is a promise the server does not keep",
			cacheControl)
	}
	// The same throttled caller on the PLAIN sibling: still no directive. The
	// limiter is shared, so this is the sharpest control the boundary has.
	var limitedPlain resp
	for attempt := 0; attempt < 12 && limitedPlain.code != http.StatusTooManyRequests; attempt++ {
		limitedPlain = h.do("GET", "/v1/m/nostoreprobe/plain", token, nil, tenantHdr(tenant))
	}
	if limitedPlain.code != http.StatusTooManyRequests {
		t.Fatalf("the plain sibling never reached the shared limit; last = %d %s",
			limitedPlain.code, limitedPlain.raw)
	}
	t.Logf("K3_NOSTORE_PREROUTING|probe=plain_sibling_limit_429|status=429|cache_control=%q",
		limitedPlain.hdr.Get("Cache-Control"))
	if got := limitedPlain.hdr.Get("Cache-Control"); got != "" {
		t.Fatalf("the plain sibling's 429 carries Cache-Control=%q; the global limiter's "+
			"response has been given a cache policy it was never asked for", got)
	}
}
