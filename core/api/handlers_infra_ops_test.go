// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
)

type infraFixedClock struct{ now time.Time }

func (c infraFixedClock) Now() model.Timestamp { return model.NewTimestamp(c.now) }

type mutableBusStats struct {
	mu    sync.Mutex
	stats eventbus.Stats
}

func (p *mutableBusStats) BusStats() eventbus.Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.stats
	out.Subscribers = append([]eventbus.SubscriberStats(nil), p.stats.Subscribers...)
	return out
}

func (p *mutableBusStats) update(fn func(*eventbus.Stats)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.stats)
}

type bridgeBusStats struct {
	*mutableBusStats
	bridge natsbus.BridgeStats
}

func (p *bridgeBusStats) Bridge() natsbus.BridgeStats { return p.bridge }

func TestConsoleKeyCustodyReturnsOnlyNonSecretMetadata(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.KeyCustody = api.KeyCustodyInfo{Keys: []api.KeyInfo{
			{
				Purpose: "audit", Algorithm: "ed25519", CustodyMode: "cmek",
				KEK: "aws-kms arn:example", Created: "", PublicKey: "cHVibGlj",
				Fingerprint: strings.Repeat("a", 64), PriorCount: 0,
			},
			{Purpose: "license", Algorithm: "ed25519", Origin: "release", Fingerprint: "1234abcd"},
			{Purpose: "eventing", Source: "file", Present: false},
		}}
	})
	admin := h.adminLogin() // AAL1: this secretless read deliberately needs no step-up.

	r := h.do(http.MethodGet, "/v1/console/keys", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/keys = %d %s", r.code, r.raw)
	}
	keys, ok := r.body["keys"].([]any)
	if !ok || len(keys) != 3 {
		t.Fatalf("keys = %#v, want 3 entries", r.body["keys"])
	}
	auditKey := keys[0].(map[string]any)
	if auditKey["custody_mode"] != "cmek" || auditKey["kek"] != "aws-kms arn:example" ||
		auditKey["fingerprint"] != strings.Repeat("a", 64) || auditKey["prior_count"] != float64(0) {
		t.Fatalf("audit custody metadata = %#v", auditKey)
	}
	if created, present := auditKey["created"]; !present || created != "" {
		t.Fatalf("audit created = %#v (present=%v), want explicit unknown empty string", created, present)
	}
	sealer := keys[2].(map[string]any)
	if len(sealer) != 3 || sealer["purpose"] != "eventing" || sealer["source"] != "file" || sealer["present"] != false {
		t.Fatalf("symmetric sealer must expose purpose/source/present only: %#v", sealer)
	}
	for _, forbidden := range []string{"private_key", "secret_material", "symmetric_key"} {
		if strings.Contains(r.raw, forbidden) {
			t.Fatalf("key custody response leaked forbidden field %q: %s", forbidden, r.raw)
		}
	}
	if unauth := h.do(http.MethodGet, "/v1/console/keys", "", nil, nil); unauth.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated key inventory = %d, want 401", unauth.code)
	}
}

func TestConsoleKeyCustodyDefaultsToEmptyList(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	r := h.do(http.MethodGet, "/v1/console/keys", admin, nil, nil)
	keys, ok := r.body["keys"].([]any)
	if r.code != http.StatusOK || !ok || len(keys) != 0 {
		t.Fatalf("default key inventory = %d %s, want {keys:[]}", r.code, r.raw)
	}
}

func TestResidencyRegistryAndSetOrgRegion(t *testing.T) {
	reg, err := residency.NewRegistry("eu", []string{"us", "eu"})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessOpts(t, func(o *api.Options) { o.Residency = reg })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	registry := h.do(http.MethodGet, "/v1/system/residency", admin, nil, nil)
	if registry.code != http.StatusOK {
		t.Fatalf("GET residency = %d %s", registry.code, registry.raw)
	}
	regions := registry.body["regions"].([]any)
	if registry.body["home_region"] != "eu" || registry.body["enforces"] != true ||
		len(regions) != 2 || regions[0] != "eu" || regions[1] != "us" {
		t.Fatalf("residency registry = %s", registry.raw)
	}

	// RBAC is evaluated before assurance: a tenant admin remains forbidden even
	// after elevation because the write is system/superadmin-only.
	member := h.mkMember(admin, "tenant-admin@acme.io", "tenantadmin1", auth.RoleAdmin, tenant)
	h.elevate(member)
	if denied := h.do(http.MethodPut, "/v1/system/orgs/"+tenant.String()+"/region", member,
		map[string]any{"data_region": "eu"}, tenantHdr(tenant)); denied.code != http.StatusForbidden ||
		errCode(denied.body) == "step_up_required" {
		t.Fatalf("tenant admin set region = %d %s, want RBAC 403", denied.code, denied.raw)
	}

	if stepUp := h.do(http.MethodPut, "/v1/system/orgs/"+tenant.String()+"/region", admin,
		map[string]any{"data_region": "eu"}, nil); stepUp.code != http.StatusForbidden ||
		errCode(stepUp.body) != "step_up_required" {
		t.Fatalf("AAL1 set region = %d %s, want 403 step_up_required", stepUp.code, stepUp.raw)
	}
	h.elevate(admin)

	system := h.do(http.MethodPut, "/v1/system/orgs/"+model.SystemTenantID.String()+"/region", admin,
		map[string]any{"data_region": "eu"}, nil)
	if system.code != http.StatusBadRequest {
		t.Fatalf("system-tenant set region = %d %s, want 400", system.code, system.raw)
	}

	unknown := h.do(http.MethodPut, "/v1/system/orgs/"+tenant.String()+"/region", admin,
		map[string]any{"data_region": "moon"}, nil)
	if unknown.code != http.StatusBadRequest ||
		!strings.Contains(unknown.raw, `unknown region \"moon\"`) ||
		!strings.Contains(unknown.raw, "known regions: eu, us") {
		t.Fatalf("unknown region = %d %s, want known-region diagnostic", unknown.code, unknown.raw)
	}

	ok := h.do(http.MethodPut, "/v1/system/orgs/"+tenant.String()+"/region", admin,
		map[string]any{"data_region": "EU"}, nil)
	if ok.code != http.StatusOK || ok.body["tenant_id"] != tenant.String() || ok.body["data_region"] != "eu" {
		t.Fatalf("set region = %d %s", ok.code, ok.raw)
	}
}

func TestResidencyRegistryNilIsExplicitlyNonEnforcing(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	r := h.do(http.MethodGet, "/v1/system/residency", admin, nil, nil)
	regions, ok := r.body["regions"].([]any)
	if r.code != http.StatusOK || r.body["home_region"] != "" || r.body["enforces"] != false ||
		!ok || len(regions) != 0 {
		t.Fatalf("nil residency registry = %d %s", r.code, r.raw)
	}
}

func TestConsoleBusSnapshotShapeIncludesBridge(t *testing.T) {
	provider := &bridgeBusStats{
		mutableBusStats: &mutableBusStats{stats: eventbus.Stats{
			Subscribers: []eventbus.SubscriberStats{{
				Name: "notify-dispatch", Class: eventbus.ClassNotify, Depth: 7, Capacity: 32,
			}},
			PublishBlocked: 1, Dropped: 2, DroppedTelemetry: 3, DroppedNotify: 4,
			HandlerErrors: 5, Enqueued: 6, Handled: 7,
		}},
		bridge: natsbus.BridgeStats{
			Connected: true, PendingMsgs: 8, PendingBytes: 9, Dropped: 10,
			PublishErrors: 11, DecodeErrors: 12, GateSkipped: 13, InvalidSubject: 14,
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) { o.BusStats = provider })
	admin := h.adminLogin()

	r := h.do(http.MethodGet, "/v1/console/bus", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/bus = %d %s", r.code, r.raw)
	}
	subscribers := r.body["subscribers"].([]any)
	if len(subscribers) != 1 {
		t.Fatalf("subscribers = %#v", subscribers)
	}
	sub := subscribers[0].(map[string]any)
	if sub["name"] != "notify-dispatch" || sub["class"] != "notify" ||
		sub["depth"] != float64(7) || sub["capacity"] != float64(32) {
		t.Fatalf("subscriber snapshot = %#v", sub)
	}
	for field, want := range map[string]float64{
		"publish_blocked": 1, "dropped": 2, "dropped_telemetry": 3,
		"dropped_notify": 4, "handler_errors": 5, "enqueued": 6, "handled": 7,
	} {
		if r.body[field] != want {
			t.Errorf("%s = %#v, want %v", field, r.body[field], want)
		}
	}
	bridge := r.body["bridge"].(map[string]any)
	if bridge["connected"] != true || bridge["pending_msgs"] != float64(8) ||
		bridge["invalid_subject"] != float64(14) {
		t.Fatalf("bridge snapshot = %#v", bridge)
	}
}

func TestPublicStatusIngestUsesBusCounterDeltasWithoutNames(t *testing.T) {
	const privateSubscriberName = "governance.internal-rule-projector"
	provider := &mutableBusStats{stats: eventbus.Stats{
		Subscribers:    []eventbus.SubscriberStats{{Name: privateSubscriberName, Depth: 0, Capacity: 10}},
		PublishBlocked: 4,
	}}
	h := newHarnessOpts(t, func(o *api.Options) { o.BusStats = provider })

	first := h.do(http.MethodGet, "/status", "", nil, nil)
	if got := componentStatus(t, first, "ingest"); got != "operational" {
		t.Fatalf("initial non-zero cumulative counter ingest = %q, want operational", got)
	}
	provider.update(func(stats *eventbus.Stats) { stats.PublishBlocked++ })
	second := h.do(http.MethodGet, "/status", "", nil, nil)
	if got := componentStatus(t, second, "ingest"); got != "degraded" {
		t.Fatalf("increased publish_blocked ingest = %q, want degraded", got)
	}
	if strings.Contains(first.raw, privateSubscriberName) || strings.Contains(second.raw, privateSubscriberName) {
		t.Fatalf("public /status leaked subscriber/module name: %s", second.raw)
	}

	// Cumulative history alone is not a permanent incident: with no new delta,
	// the following observation returns to operational.
	third := h.do(http.MethodGet, "/status", "", nil, nil)
	if got := componentStatus(t, third, "ingest"); got != "operational" {
		t.Fatalf("unchanged cumulative counter ingest = %q, want operational", got)
	}
}

func TestPublicStatusIngestDegradesAtEightyPercentSaturation(t *testing.T) {
	const privateSubscriberName = "catalog.hidden-indexer"
	provider := &mutableBusStats{stats: eventbus.Stats{
		Subscribers: []eventbus.SubscriberStats{{
			Name: privateSubscriberName, Class: eventbus.ClassState, Depth: 8, Capacity: 10,
		}},
	}}
	h := newHarnessOpts(t, func(o *api.Options) { o.BusStats = provider })

	r := h.do(http.MethodGet, "/status", "", nil, nil)
	if got := componentStatus(t, r, "ingest"); got != "degraded" {
		t.Fatalf("80%% saturated ingest = %q, want degraded", got)
	}
	if strings.Contains(r.raw, privateSubscriberName) {
		t.Fatalf("public /status leaked saturated subscriber name: %s", r.raw)
	}
}

func TestPublicStatusIngestNilProviderMirrorsStore(t *testing.T) {
	h := newHarness(t)
	if err := h.st.Close(); err != nil {
		t.Fatalf("close store for outage fixture: %v", err)
	}
	r := h.do(http.MethodGet, "/status", "", nil, nil)
	storeStatus := componentStatus(t, r, "store")
	ingestStatus := componentStatus(t, r, "ingest")
	if storeStatus != "outage" || ingestStatus != storeStatus {
		t.Fatalf("nil-provider fallback: store=%q ingest=%q, want matching outage", storeStatus, ingestStatus)
	}
}

func TestHealthSummaryIncludesLiveTLSExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	notAfter := now.Add(49 * time.Hour)
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Clock = infraFixedClock{now: now}
		o.TLSCertNotAfter = func() (time.Time, bool) { return notAfter, true }
	})
	admin := h.adminLogin()

	r := h.do(http.MethodGet, "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK || r.body["tls_not_after"] != notAfter.Format(time.RFC3339) ||
		r.body["tls_days_left"] != float64(2) {
		t.Fatalf("TLS health summary = %d %s", r.code, r.raw)
	}
}

func TestHealthSummaryOmitsUnknownTLSExpiry(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.TLSCertNotAfter = func() (time.Time, bool) { return time.Time{}, false }
	})
	admin := h.adminLogin()
	r := h.do(http.MethodGet, "/v1/console/health-summary", admin, nil, nil)
	if _, present := r.body["tls_not_after"]; present {
		t.Fatalf("unknown TLS expiry must omit tls_not_after: %s", r.raw)
	}
	if _, present := r.body["tls_days_left"]; present {
		t.Fatalf("unknown TLS expiry must omit tls_days_left: %s", r.raw)
	}
}

func componentStatus(t *testing.T, r resp, name string) string {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", r.code, r.raw)
	}
	for _, raw := range r.body["components"].([]any) {
		component := raw.(map[string]any)
		if component["name"] == name {
			status, _ := component["status"].(string)
			return status
		}
	}
	t.Fatalf("status response has no %q component: %s", name, r.raw)
	return ""
}
