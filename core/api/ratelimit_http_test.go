// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// rlClock is a frozen, wall-clock-only clock for the limiter so a test's token
// budget is exactly the burst (no refill races). It mirrors the production clock,
// which strips monotonic.
type rlClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *rlClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// rlTestConfig is the shared limiter config for the HTTP/gRPC rate-limit tests: a
// tight default tier so a few requests trip it, and a generous system tier so the
// superadmin bootstrap (setup/org/user/grant, all on the system tier) never trips.
func rlTestConfig(now func() time.Time, mode ratelimit.Mode) *ratelimit.Config {
	return &ratelimit.Config{
		Now:         now,
		Mode:        mode,
		DefaultTier: ratelimit.TierDefault,
		Tiers: map[string]ratelimit.TierLimits{
			ratelimit.TierDefault: {
				PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
					ratelimit.ClassRead:  {Rate: 1000, Burst: 4},
					ratelimit.ClassWrite: {Rate: 1000, Burst: 2},
				},
				Total: ratelimit.Limit{Rate: 1000, Burst: 5},
			},
			ratelimit.TierSystem: {
				PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
					ratelimit.ClassRead:  {Rate: 1000, Burst: 10000},
					ratelimit.ClassWrite: {Rate: 1000, Burst: 10000},
				},
				Total: ratelimit.Limit{Rate: 1000, Burst: 10000},
			},
		},
	}
}

// newRLHarness builds a harness whose API server enforces rl (with the given metrics
// registry, so a test can scrape decisions). The limiter uses its OWN frozen clock,
// independent of the server clock — so freezing the bucket math does not freeze the
// access log or SSO TTLs.
func newRLHarness(t *testing.T, mode ratelimit.Mode, reg *metrics.Registry, ingest ...api.ObservationPublisher) *harness {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(context.Background()); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	clk := &rlClock{t: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}
	rl := rlTestConfig(clk.now, mode)
	var ing api.ObservationPublisher
	if len(ingest) > 0 {
		ing = ingest[0]
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Metrics: reg, RateLimit: rl, Ingest: ing,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, authr: authr, signer: signer, setupTok: plaintext}
}

// rawResp adds response headers to resp (the base harness.do drops them, but the
// rate limiter's value is partly in its headers).
type rawResp struct {
	code   int
	header http.Header
	body   map[string]any
	raw    string
}

// doRaw is do but also returns the response headers (Retry-After, RateLimit-*).
func (h *harness) doRaw(method, path, token string, body any, hdr map[string]string) rawResp {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := rawResp{code: rec.Code, header: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal([]byte(out.raw), &out.body)
	return out
}

// errCode safely extracts error.code from a JSON error envelope body.
func errCode(body map[string]any) string {
	e, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := e["code"].(string)
	return c
}

// tenantToken provisions an editor member in tenant and returns its tenant-bound
// session token (metered against that tenant's bucket, not the superadmin's).
func (h *harness) tenantToken(admin string, tenant model.TenantID, email string) string {
	return h.tenantTokenRole(admin, tenant, email, auth.RoleEditor)
}

// tenantTokenRole is tenantToken with an explicit role (e.g. RoleAdmin to obtain
// ingest:write).
func (h *harness) tenantTokenRole(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if cr.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); g.code != http.StatusCreated {
		h.t.Fatalf("grant %s = %d %s", email, g.code, g.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	return lr.body["token"].(string)
}

// tenantTokenMulti provisions a user with memberships in several tenants and returns
// its session token (so p.Tenants() has >1 entry — the multi-membership keying path).
func (h *harness) tenantTokenMulti(admin, email string, tenants ...model.TenantID) string {
	h.t.Helper()
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if cr.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	for _, tn := range tenants {
		if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tn.String(), "role": auth.RoleEditor}, nil); g.code != http.StatusCreated {
			h.t.Fatalf("grant %s in %s = %d %s", email, tn, g.code, g.raw)
		}
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	return lr.body["token"].(string)
}

// TestRateLimitSuperadminNotMeteredIntoTenantBucket: a superadmin is keyed per-cred at
// the system tier, NOT into any tenant's bucket — so an operator's work cannot be
// blocked by (nor starve) a tenant whose bucket is exhausted.
func TestRateLimitSuperadminNotMeteredIntoTenantBucket(t *testing.T) {
	h := newRLHarness(t, ratelimit.ModeEnforce, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.tenantToken(admin, tenant, "a@acme.io")

	// Exhaust tenant A's write bucket via its own member.
	var drained bool
	for i := 0; i < 6; i++ {
		if h.do("POST", "/v1/agents", tok, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenant)).code == http.StatusTooManyRequests {
			drained = true
		}
	}
	if !drained {
		t.Fatal("tenant A's write bucket should be exhausted")
	}
	// The superadmin writes to the SAME tenant and still succeeds — it is not metered
	// into A's (exhausted) bucket.
	if r := h.do("POST", "/v1/agents", admin, map[string]any{"name": "sa", "kind": "k"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("superadmin write must not be blocked by the tenant's exhausted bucket, got %d %s", r.code, r.raw)
	}
}

// TestRateLimitNonMemberHeaderUsesCredBucket: a multi-membership user that names a
// tenant it does NOT belong to is metered against its own credential bucket, never the
// victim tenant's — so it cannot drain a tenant it cannot act in. Proven by draining
// the victim's bucket and showing the spoofing caller still reaches authz (403), not 429.
func TestRateLimitNonMemberHeaderUsesCredBucket(t *testing.T) {
	h := newRLHarness(t, ratelimit.ModeEnforce, nil)
	admin := h.adminLogin()
	a := h.createOrg(admin, "acme")
	b := h.createOrg(admin, "globex")
	c := h.createOrg(admin, "initech")
	tokB := h.tenantToken(admin, b, "b@globex.io")        // member of B (the victim)
	tokU := h.tenantTokenMulti(admin, "u@multi.io", a, c) // member of A and C, NOT B

	// Drain tenant B's write bucket (burst 2).
	for i := 0; i < 4; i++ {
		h.do("POST", "/v1/agents", tokB, map[string]any{"name": "x", "kind": "k"}, tenantHdr(b))
	}
	// U targets B. If the limiter keyed U into B's (drained) bucket it would 429 before
	// authz; instead U is keyed to its own cred bucket, the limiter admits, and authz
	// rejects with 403 — proving a non-member header cannot borrow/drain a victim's bucket.
	r := h.do("POST", "/v1/agents", tokU, map[string]any{"name": "x", "kind": "k"}, tenantHdr(b))
	if r.code != http.StatusForbidden {
		t.Fatalf("non-member targeting a drained tenant must 403 (own cred bucket, not the victim's), got %d %s", r.code, r.raw)
	}
}

// TestRateLimitNoisyNeighborIsolation is the headline OPS-5 proof: tenant A's write
// flood is throttled (429) while tenant B is untouched (separate per-tenant bucket).
func TestRateLimitNoisyNeighborIsolation(t *testing.T) {
	h := newRLHarness(t, ratelimit.ModeEnforce, nil)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	tokA := h.tenantToken(admin, tenantA, "a@acme.io")
	tokB := h.tenantToken(admin, tenantB, "b@globex.io")

	// Tenant A floods writes. Write burst is 2, so the first two admit, the rest 429.
	var got429 bool
	for i := 0; i < 6; i++ {
		r := h.do("POST", "/v1/agents", tokA, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenantA))
		if r.code == http.StatusTooManyRequests {
			got429 = true
			// Honest, observable 429: code rate_limited, and the body must not leak the
			// tenant id, a tier name, or a bucket-key prefix (minimal data).
			if code := errCode(r.body); code != "rate_limited" {
				t.Fatalf("429 body code = %q, want rate_limited; body=%s", code, r.raw)
			}
			for _, leak := range []string{tenantA.String(), "tn:", "su:", "cr:", "enterprise", "system"} {
				if strings.Contains(r.raw, leak) {
					t.Fatalf("429 body leaks %q: %s", leak, r.raw)
				}
			}
		}
	}
	if !got429 {
		t.Fatal("tenant A's write flood was never throttled")
	}

	// Tenant B, a different tenant, is unaffected: its own bucket is full.
	if r := h.do("POST", "/v1/agents", tokB, map[string]any{"name": "y", "kind": "k"}, tenantHdr(tenantB)); r.code != http.StatusCreated {
		t.Fatalf("tenant B throttled by tenant A's flood (noisy-neighbor leak): %d %s", r.code, r.raw)
	}
}

// TestRateLimitHeadersAndRetryAfter verifies the advisory headers on an admitted
// response and the normative Retry-After on a denial.
func TestRateLimitHeadersAndRetryAfter(t *testing.T) {
	h := newRLHarness(t, ratelimit.ModeEnforce, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.tenantToken(admin, tenant, "a@acme.io")

	// First write admits and carries the advisory RateLimit-* headers.
	r := h.doRaw("POST", "/v1/agents", tok, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("first write = %d", r.code)
	}
	if r.header.Get("RateLimit-Limit") == "" || r.header.Get("RateLimit-Remaining") == "" {
		t.Fatalf("admitted response missing RateLimit-* headers: %v", r.header)
	}

	// Exhaust the write burst (2) then assert the 429 carries Retry-After >= 1.
	h.doRaw("POST", "/v1/agents", tok, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenant)) // 2nd admits
	denied := h.doRaw("POST", "/v1/agents", tok, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenant))
	if denied.code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst, got %d", denied.code)
	}
	if ra := denied.header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("429 must carry a positive Retry-After, got %q", ra)
	}
}

// TestRateLimitExemptsOperationalProbes: a flood of probe/scrape requests is never
// throttled (k8s/Prometheus must always reach them).
func TestRateLimitExemptsOperationalProbes(t *testing.T) {
	h := newRLHarness(t, ratelimit.ModeEnforce, nil)
	h.adminLogin()
	for _, path := range []string{"/healthz", "/livez", "/readyz", "/metrics"} {
		for i := 0; i < 50; i++ {
			if r := h.do("GET", path, "", nil, nil); r.code == http.StatusTooManyRequests {
				t.Fatalf("operational probe %s was throttled (must be exempt) on iter %d", path, i)
			}
		}
	}
}

// TestRateLimitReportOnlyAllowsButCounts: report-only never denies but counts the
// would-be denials, so an operator can size quotas before enforcing.
func TestRateLimitReportOnlyAllowsButCounts(t *testing.T) {
	reg := metrics.New("test", time.Now())
	h := newRLHarness(t, ratelimit.ModeReportOnly, reg)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.tenantToken(admin, tenant, "a@acme.io")

	for i := 0; i < 8; i++ {
		if r := h.do("POST", "/v1/agents", tok, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenant)); r.code == http.StatusTooManyRequests {
			t.Fatalf("report-only must never 429, got one on iter %d", i)
		}
	}
	// Assert the counter VALUE: the write burst is 2, so the other 6 writes are would-be
	// denials counted as report_only. A substring check would pass even at zero (the
	// series is pre-created), so it must be >= 1.
	if v := metricsValue(t, reg, `olivares_http_ratelimit_decisions_total{class="write",decision="report_only"}`); v < 1 {
		t.Fatalf("report-only over-limit writes must be counted; report_only=%v, want >=1", v)
	}
}

func metricsValue(t *testing.T, reg *metrics.Registry, prefix string) float64 {
	t.Helper()
	var b strings.Builder
	reg.WritePrometheus(&b)
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, prefix+" ") {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return v
		}
	}
	t.Fatalf("series %q not found", prefix)
	return 0
}
