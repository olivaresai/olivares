// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

func TestAPIRoutesRegistersReportEndpoints(t *testing.T) {
	var reg recordingRoutes
	New().APIRoutes(&reg)
	// The two built-in on-demand routes must be registered first.
	want := []recordedRoute{
		{method: http.MethodGet, pattern: "/reports", perm: permReportRead},
		{method: http.MethodGet, pattern: "/reports/{type}", perm: permReportRead},
	}
	for i, w := range want {
		if reg.routes[i].method != w.method || reg.routes[i].pattern != w.pattern || reg.routes[i].perm != w.perm {
			t.Fatalf("route %d = %+v, want %+v", i, reg.routes[i], w)
		}
	}
	// The enterprise surface must also be registered (served, answering 501
	// until its seams are wired — see TestEnterpriseRoutes501WithoutSeams).
	mustRoute := func(method, pattern string) {
		for _, r := range reg.routes {
			if r.method == method && r.pattern == pattern {
				return
			}
		}
		t.Fatalf("missing route %s %s among %+v", method, pattern, reg.routes)
	}
	mustRoute(http.MethodGet, "/enterprise/posture")
	mustRoute(http.MethodGet, "/enterprise/risk")
	mustRoute(http.MethodGet, "/enterprise/bundle")
	mustRoute(http.MethodPost, "/schedules")
	mustRoute(http.MethodPut, "/branding")
	mustRoute(http.MethodPut, "/templates/{type}")
}

// TestEnterpriseRoutes501WithoutSeams proves the community parity: with no
// enterprise seam wired (the default build), every route answers 501 —
// honest not-implemented, never a fabricated empty report.
func TestEnterpriseRoutes501WithoutSeams(t *testing.T) {
	m := New() // no enterprise seams, no providers
	m.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	mc := api.ModuleContext{Tenant: model.TenantID("t1"), Data: servedData{}}

	cases := []struct {
		method, path string
		handler      func(http.ResponseWriter, *http.Request, api.ModuleContext)
	}{
		{http.MethodGet, "/enterprise/posture", m.handleEnterprisePosture},
		{http.MethodGet, "/enterprise/risk", m.handleEnterpriseRisk},
		{http.MethodGet, "/enterprise/bundle", m.handleEnterpriseBundle},
		{http.MethodGet, "/schedules", m.handleListSchedules},
		{http.MethodGet, "/branding", m.handleGetBranding},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.handler(rec, httptest.NewRequest(c.method, c.path, nil), mc)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s without a seam = %d, want 501: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestReportHandlersListGenerateAndCacheHTML(t *testing.T) {
	src := &fakeComplianceSource{}
	m := newReportingTestModule(t, WithComplianceSource(src))
	mc := api.ModuleContext{Tenant: model.TenantID("tenant"), Data: servedData{}}

	list := httptest.NewRecorder()
	m.handleListReports(list, httptest.NewRequest(http.MethodGet, "/reports", nil), mc)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), string(ReportComplianceEvidence)) {
		t.Fatalf("list reports = %d %s", list.Code, list.Body.String())
	}

	req := reportRequest("/reports/compliance-evidence?format=html&locale=es&framework=iso_27001&from=2026-01-01&to=2026-01-31", ReportComplianceEvidence)
	first := httptest.NewRecorder()
	m.handleGenerateReport(first, req, mc)
	if first.Code != http.StatusOK {
		t.Fatalf("generate report = %d %s", first.Code, first.Body.String())
	}
	if ct := first.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want html", ct)
	}
	body := first.Body.String()
	for _, want := range []string{"Informe de evidencia de cumplimiento", "ISO/IEC 27001", "A.5.1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated report missing %q in %s", want, body)
		}
	}

	second := httptest.NewRecorder()
	m.handleGenerateReport(second, req, mc)
	if second.Code != http.StatusOK {
		t.Fatalf("cached report = %d %s", second.Code, second.Body.String())
	}
	if src.calls != 1 {
		t.Fatalf("compliance source calls = %d, want 1 due cache hit", src.calls)
	}
}

func TestGenerateReportRejectsUnknownTypeAndUnavailablePDF(t *testing.T) {
	m := newReportingTestModule(t, WithComplianceSource(&fakeComplianceSource{}))
	mc := api.ModuleContext{Tenant: model.TenantID("tenant"), Data: servedData{}}

	unknown := httptest.NewRecorder()
	m.handleGenerateReport(unknown, reportRequest("/reports/nope", ReportType("nope")), mc)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown report = %d, want 404", unknown.Code)
	}

	t.Setenv("PATH", t.TempDir())
	pdf := httptest.NewRecorder()
	m.handleGenerateReport(pdf, reportRequest("/reports/compliance-evidence?format=pdf", ReportComplianceEvidence), mc)
	if pdf.Code != http.StatusNotImplemented {
		t.Fatalf("pdf unavailable = %d %s, want 501", pdf.Code, pdf.Body.String())
	}
}

func newReportingTestModule(t *testing.T, opts ...Option) *Module {
	t.Helper()
	t.Setenv("OLIVARES_REPORT_CACHE_DIR", t.TempDir())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(opts...)
	m.log = log
	m.engine = NewEngine(log)
	m.cache = NewCache(log)
	t.Cleanup(m.cache.Close)
	return m
}

func reportRequest(target string, reportType ReportType) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("type", string(reportType))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

type recordedRoute struct {
	method  string
	pattern string
	perm    auth.Permission
}

type recordingRoutes struct {
	routes []recordedRoute
}

func (r *recordingRoutes) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}

func (r *recordingRoutes) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	r.routes = append(r.routes, recordedRoute{method: method, pattern: pattern, perm: perm})
}

type fakeComplianceSource struct {
	calls int
}

func (f *fakeComplianceSource) GatherComplianceData(context.Context, model.TenantID, string) (ComplianceData, error) {
	f.calls++
	return ComplianceData{
		Generated:  time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
		Disclaimer: "Test disclaimer.",
		Frameworks: []FrameworkReport{{
			ID:        "iso_27001",
			Name:      "ISO/IEC 27001",
			Version:   "2022",
			Authority: "ISO",
			Summary:   AssessmentSummary{Total: 2, Satisfied: 1, Gap: 1},
			Controls: []ControlReport{{
				ID:          "A.5.1",
				Title:       "Policies",
				Status:      "satisfied",
				Requirement: "Approved policy.",
			}},
		}},
	}, nil
}

// servedData is a ScopedData for a tenant that IS in service. These tests exercise
// the report cache, not the service-state door — but handleGenerateReport now
// re-enters the store before answering from a warm cache (a cached report must not
// outlive the tenant's service), and it fails CLOSED when it has no handle to ask.
// A nil Data is therefore the wrong fixture, not a lighter one.
type servedData struct{}

func (servedData) View(context.Context, func(store.Scope) error) error   { return nil }
func (servedData) Mutate(context.Context, func(store.Scope) error) error { return nil }

// Export succeeds for a tenant in service, like View.
func (servedData) Export(context.Context, func(store.ExportScope) error) error { return nil }

// withdrawnData is a ScopedData for a tenant whose service has been withdrawn —
// exactly what core/suspension's guard returns.
type withdrawnData struct{}

func (withdrawnData) View(context.Context, func(store.Scope) error) error {
	return fmt.Errorf("%w: tenant is not in service", store.ErrTenantSuspended)
}
func (withdrawnData) Mutate(context.Context, func(store.Scope) error) error {
	return fmt.Errorf("%w: tenant is not in service", store.ErrTenantSuspended)
}

// Export SUCCEEDS even for a withdrawn tenant — that is the decided line, and a
// double that refused it would model a product that does not exist.
func (withdrawnData) Export(context.Context, func(store.ExportScope) error) error { return nil }

// TestWarmCacheIsNotServedToAWithdrawnTenant is the NEGATIVE of the cache tests
// above, and without it they only prove the cache works — never that it stops.
//
// The entry lives an hour and is invalidated by TTL, error or shutdown, never by a
// change of service state; a hit is answered from memory and never touches the
// store, which is where withdrawal is enforced. So the tenant kept receiving
// compliance reports after its service was withdrawn, and a report IS the product.
//
// The first call warms the cache while the tenant is in service; the second asks
// as a withdrawn tenant and must be refused. Asserting only the refusal would be
// weaker: it would also pass if the report had never been cached at all.
func TestWarmCacheIsNotServedToAWithdrawnTenant(t *testing.T) {
	src := &fakeComplianceSource{}
	m := newReportingTestModule(t, WithComplianceSource(src))
	req := reportRequest("/reports/compliance-evidence?format=html&locale=es&framework=iso_27001&from=2026-01-01&to=2026-01-31", ReportComplianceEvidence)

	warm := httptest.NewRecorder()
	m.handleGenerateReport(warm, req, api.ModuleContext{Tenant: model.TenantID("tenant"), Data: servedData{}})
	if warm.Code != http.StatusOK {
		t.Fatalf("precondition: the report must be generated and cached first, got %d %s", warm.Code, warm.Body.String())
	}
	if src.calls != 1 {
		t.Fatalf("precondition: source calls = %d, want 1", src.calls)
	}

	withdrawn := httptest.NewRecorder()
	m.handleGenerateReport(withdrawn, req, api.ModuleContext{Tenant: model.TenantID("tenant"), Data: withdrawnData{}})
	if withdrawn.Code != http.StatusLocked {
		t.Fatalf("a warm cache outlived the tenant's service: got %d %s, want 423 Locked",
			withdrawn.Code, withdrawn.Body.String())
	}
}

// guardedData pins a REAL store to one tenant, exactly as core/api's production
// scopedData does. It is not a double of the guard — the guard under it is the
// real core/suspension.Guard.
type guardedData struct {
	st     store.Store
	tenant model.TenantID
}

func (d guardedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.View(ctx, d.tenant, fn)
}
func (d guardedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, fn)
}

func (d guardedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

// TestWarmCacheRefusalIsWiredToTheRealGuard is an ALARM, not a feature test, and
// it exists because the test above cannot do this job.
//
// handleGenerateReport decides whether to answer from a warm cache by asking the
// store for a read. That coupling is invisible: it depends on the SERVICE GATE
// still denying READS. Has escalated exactly that question to — freeze
// reads (what ships today) or freeze only WRITES — and it is decided by one line
// in core/suspension/store.go's View, whether it wraps with g.wrap or not.
//
// MEASURED, not predicted: with that wrapping removed, core/suspension fails in
// five cells (correct — they pin the semantics), and modules/reporting stays
// GREEN. The cache check silently stops doing anything, while
// TestWarmCacheIsNotServedToAWithdrawnTenant keeps passing, because its double
// returns ErrTenantSuspended from a View that production would no longer refuse.
// That is a test staying green because the double can do what production cannot.
//
// So this cell drives the REAL guard over a REAL store. If the semantics change,
// it goes red HERE, in the package that depends on it, and whoever changes the
// line learns that the report cache needs its own answer rather than finding out
// from a customer served a compliance report after their service was withdrawn.
//
// It asserts NOTHING about which semantics are right. That is a commercial and
// legal decision, it is , and it is not made by a test.
func TestWarmCacheRefusalIsWiredToTheRealGuard(t *testing.T) {
	ctx := context.Background()
	inner, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	if err := inner.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	var tenant model.TenantID
	if err := inner.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = o.TenantID
		return e
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	guarded := suspension.Guard(inner, nil)

	src := &fakeComplianceSource{}
	m := newReportingTestModule(t, WithComplianceSource(src))
	req := reportRequest("/reports/compliance-evidence?format=html&locale=es&framework=iso_27001&from=2026-01-01&to=2026-01-31", ReportComplianceEvidence)
	mc := api.ModuleContext{Tenant: tenant, Data: guardedData{st: guarded, tenant: tenant}}

	// In service: the report is generated and cached.
	warm := httptest.NewRecorder()
	m.handleGenerateReport(warm, req, mc)
	if warm.Code != http.StatusOK {
		t.Fatalf("precondition: an in-service tenant must get its report, got %d %s", warm.Code, warm.Body.String())
	}

	// Withdraw service through the real store, then ask again.
	if err := inner.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(ctx, tenant, model.StatusSuspended)
		return e
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	after := httptest.NewRecorder()
	m.handleGenerateReport(after, req, mc)
	if after.Code == http.StatusOK {
		t.Fatalf("the warm report cache is no longer wired to the service gate: a withdrawn tenant was "+
			"served its cached report (HTTP %d). If the service guard's View stopped gating READS "+
			"(a change to what withdrawing service MEANS), this cache check has gone INERT and "+
			"needs its own service-state answer — it can no longer borrow the read gate's", after.Code)
	}
	if after.Code != http.StatusLocked {
		t.Fatalf("withdrawn tenant got %d %s, want 423 Locked", after.Code, after.Body.String())
	}
}
