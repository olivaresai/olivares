// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// baseTime is a fixed instant so day-bucketing is deterministic.
var baseTime = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

const (
	srcOTLP      = "olivares.claude"
	srcAnalytics = "olivares.claude-api"
)

// newModule opens a real in-memory SQLite store with the adoption schema, provisions a
// tenant, and wires the module's data handle.
func newModule(t *testing.T) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m := New()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, st, tenant
}

// fakeHost is an in-test sdk.Host that captures published events, so the discrepancy
// finding emission path (host.Publish of a FindingReport) can be asserted.
type fakeHost struct {
	mu     sync.Mutex
	events []event.Event
}

func (h *fakeHost) Publish(_ context.Context, e event.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}
func (h *fakeHost) Subscribe([]event.Type, event.Handler) (func(), error) {
	return func() {}, nil
}
func (h *fakeHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (h *fakeHost) Config() sdk.Config { return sdk.Config{} }

func (h *fakeHost) findings() []sdkmodel.FindingReport {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sdkmodel.FindingReport
	for _, e := range h.events {
		if f, ok := event.FindingOf(e); ok {
			out = append(out, f)
		}
	}
	return out
}

// ingest pushes a MetricSample through the ingestion path.
func (m *Module) ingest(t *testing.T, tenant model.TenantID, source string, ms sdkmodel.MetricSample) {
	t.Helper()
	if err := m.onMetric(context.Background(), tenant, source, ms); err != nil {
		t.Fatalf("onMetric: %v", err)
	}
}

// devMetric builds an Analytics-lens (per-developer, snapshot) MetricSample.
func devMetric(name, dev string, value int64, at time.Time, dims map[string]string) sdkmodel.MetricSample {
	return sdkmodel.MetricSample{
		Name: name, Value: value, Additive: false,
		SubjectKind: subjectDeveloper, SubjectRef: dev, OccurredAt: at, Dimensions: dims,
	}
}

// sessMetric builds a telemetry-lens (per-session, additive delta) MetricSample with an
// optional team label.
func sessMetric(name, session string, value int64, at time.Time, dims map[string]string, team string) sdkmodel.MetricSample {
	var labels map[string]string
	if team != "" {
		labels = map[string]string{labelTeam: team}
	}
	return sdkmodel.MetricSample{
		Name: name, Value: value, Additive: true,
		SubjectKind: subjectSession, SubjectRef: session, OccurredAt: at, Dimensions: dims, Labels: labels,
	}
}

func metricByName(metrics []discrepancyMetric, name string) discrepancyMetric {
	for _, metric := range metrics {
		if metric.Name == name {
			return metric
		}
	}
	return discrepancyMetric{}
}

// adoptionRows returns the read-model rows in a tenant.
func adoptionRows(t *testing.T, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(adoptionMetricKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("adoptionRows: %v", err)
	}
	return out
}

// pinnedData adapts the tenant-parameterized ModuleData to the tenant-PINNED ScopedData a
// route handler receives, so handler tests run the real query path.
type pinnedData struct {
	md     api.ModuleData
	tenant model.TenantID
}

func (p pinnedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return p.md.View(ctx, p.tenant, fn)
}

// Export mirrors View: these doubles model a tenant that IS in service, and the
// portability door reaches the same data. Written out rather than panicking so a
// route that legitimately exports keeps working under the double.
// Export is not wired: this double pins an api.ModuleData, the tenant-parameterized
// handle for EVENT-DRIVEN modules, and the portability door is a ROUTE concern —
// no claudeadoption route hands the customer their own data back. It returns an
// explicit error rather than a silent success, so a future route that does export
// fails loudly here instead of quietly exporting through a fake.
func (p pinnedData) Export(context.Context, func(store.ExportScope) error) error {
	return fmt.Errorf("pinnedData: Export is not wired for this module's test double")
}
func (p pinnedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return p.md.Mutate(ctx, p.tenant, fn)
}

// get calls a module GET handler with the given query string and decodes the JSON body
// into out, returning the status code.
func get[T any](t *testing.T, st store.Store, tenant model.TenantID, h api.ModuleHandler, query string, out *T) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x?"+query, nil)
	rec := httptest.NewRecorder()
	mc := api.ModuleContext{Tenant: tenant, Data: pinnedData{md: api.NewModuleData(st), tenant: tenant}}
	h(rec, req, mc)
	res := rec.Result()
	if res.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return res.StatusCode
}
