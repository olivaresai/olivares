// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package olivares

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient builds a Client against a fake server with an instant,
// recording sleep (so retry waits are asserted, not slept).
func newTestClient(t *testing.T, h http.Handler, opts ...Option) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "olvk_test_secret", opts...)
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return c, &slept
}

func TestRequestShape(t *testing.T) {
	var got *http.Request
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{"items":[],"has_more":false}`))
	}), WithTenant("t-default"))

	if _, err := c.GetV1Agents(context.Background(), Query("limit", "5"), Tenant("t-override")); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/v1/agents" || got.URL.Query().Get("limit") != "5" {
		t.Errorf("request = %s %s", got.URL.Path, got.URL.RawQuery)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer olvk_test_secret" {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.Header.Get("X-Olivares-Tenant"); h != "t-override" {
		t.Errorf("tenant override lost: %q", h)
	}
	if ua := got.Header.Get("User-Agent"); !strings.HasPrefix(ua, "olivares-client-go/"+Version) ||
		!strings.Contains(ua, "api "+APIVersion) {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestRawRequestPreservesDeclaredContentType(t *testing.T) {
	var got []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Content-Type"))
		_, _ = w.Write([]byte(`{}`))
	}))

	ctx := context.Background()
	if _, err := c.doReqRawWithType(
		ctx, http.MethodPost, "/memory/import", "/memory/import",
		[]byte("{}\n"), "application/x-ndjson",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := c.doReqRaw(
		ctx, http.MethodPut, "/files/raw", "/files/raw", []byte("raw"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := c.doReqRawWithType(
		ctx, http.MethodPost, "/memory/import", "/memory/import", nil,
		"application/x-ndjson",
	); err != nil {
		t.Fatal(err)
	}

	want := []string{"application/x-ndjson", "application/octet-stream", ""}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Content-Type headers = %q, want %q", got, want)
	}
}

func TestRequiredJSONNullIsPresentWhileOptionalNilIsAbsent(t *testing.T) {
	type request struct {
		body        string
		contentType string
	}
	var got []request
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		got = append(got, request{body: string(body), contentType: r.Header.Get("Content-Type")})
		_, _ = w.Write([]byte(`{}`))
	}))

	ctx := context.Background()
	if _, err := c.doJSONRequired(ctx, http.MethodPost, "/required", "/required", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.do(ctx, http.MethodPost, "/optional", "/optional", nil); err != nil {
		t.Fatal(err)
	}

	want := []request{
		{body: "null", contentType: "application/json"},
		{body: "", contentType: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestPathEscapingThroughGeneratedOp(t *testing.T) {
	var gotPath string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{}`))
	}))
	if _, err := c.GetV1AgentsByID(context.Background(), "a/b c"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/agents/a%2Fb%20c" {
		t.Errorf("escaped path = %q", gotPath)
	}
}

func TestErrorEnvelope(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-42")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such agent"}}`))
	}))
	_, err := c.GetV1AgentsByID(context.Background(), "missing")
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.Status != 404 || ae.Code != "not_found" || ae.Message != "no such agent" || ae.RequestID != "req-42" {
		t.Errorf("APIError = %+v", ae)
	}
}

func TestRetry429HonoursRetryAfter(t *testing.T) {
	var calls atomic.Int32
	c, slept := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	out, err := c.PostV1Agents(context.Background(), map[string]any{"name": "a"})
	if err != nil || out["ok"] != true {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls.Load())
	}
	if len(*slept) != 1 || (*slept)[0] != 3*time.Second {
		t.Errorf("slept = %v, want [3s] (Retry-After is the lower bound)", *slept)
	}
}

func TestRetryBudgetExhausts(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"still"}}`))
	}), WithMaxRetries(2))
	_, err := c.GetV1Agents(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "rate_limited" {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (initial + 2 retries)", calls.Load())
	}
}

func Test503RetriesOnlyGET(t *testing.T) {
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"not_leader","message":"handoff"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})

	c, _ := newTestClient(t, handler)
	if _, err := c.GetV1Agents(context.Background()); err != nil {
		t.Fatalf("GET should retry the 503 handoff: %v", err)
	}

	calls.Store(0)
	c2, _ := newTestClient(t, handler)
	if _, err := c2.PostV1Agents(context.Background(), map[string]any{}); err == nil {
		t.Fatal("POST must NOT retry a 503 (not known idempotent)")
	} else if calls.Load() != 1 {
		t.Errorf("POST calls = %d, want 1", calls.Load())
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"nope"}}`))
	}))
	if _, err := c.GetV1Agents(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (4xx is never retried)", calls.Load())
	}
}

func TestDeprecationNoticeOncePerEndpoint(t *testing.T) {
	var notices []DeprecationNotice
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "@1780272000")
		w.Header().Set("Sunset", "Thu, 01 Jun 2028 00:00:00 GMT")
		w.Header().Add("Link", `<https://docs.olivares.invalid/how-to/migrate-example/>; rel="deprecation"`)
		w.Header().Add("Link", `<https://docs.olivares.invalid/how-to/migrate-example/>; rel="sunset"`)
		_, _ = w.Write([]byte(`{}`))
	}), WithDeprecationHandler(func(n DeprecationNotice) { notices = append(notices, n) }))

	ctx := context.Background()
	for range 3 {
		if _, err := c.GetV1ServerInfo(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.GetHealthz(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want 2 (deduped per endpoint)", len(notices))
	}
	n := notices[0]
	if n.Method != "GET" || n.Path != "/v1/server-info" ||
		n.Deprecation != "@1780272000" || n.Sunset != "Thu, 01 Jun 2028 00:00:00 GMT" ||
		n.Link != "https://docs.olivares.invalid/how-to/migrate-example/" {
		t.Errorf("notice = %+v", n)
	}
}

func TestListPages(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items":  []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
				"cursor": "c1", "has_more": true,
			})
		case "c1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{"id": "c"}}, "has_more": false,
			})
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	}))
	var ids []string
	for item, err := range c.ListPages(context.Background(), "/v1/agents") {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item["id"].(string))
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("ids = %v", ids)
	}
}

func TestDeprecationDedupPerRouteTemplate(t *testing.T) {
	var notices []DeprecationNotice
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "@1780272000")
		_, _ = w.Write([]byte(`{}`))
	}), WithDeprecationHandler(func(n DeprecationNotice) { notices = append(notices, n) }))

	ctx := context.Background()
	for _, id := range []string{"agt_001", "agt_002", "agt_003"} {
		if _, err := c.GetV1AgentsByID(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %d, want 1 (deduped per route template, not per resource)", len(notices))
	}
	if notices[0].Path != "/v1/agents/agt_001" {
		t.Errorf("notice path = %q (the concrete path of the first hit)", notices[0].Path)
	}
}

func TestDeprecationLinkCoalescedHeader(t *testing.T) {
	var notices []DeprecationNotice
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "@1780272000")
		// A proxy may coalesce repeated Link headers into one comma-joined value.
		w.Header().Set("Link", `<https://cdn.example/x>; rel="preload", <https://docs.olivares.invalid/how-to/migrate-example/>; rel="deprecation"`)
		_, _ = w.Write([]byte(`{}`))
	}), WithDeprecationHandler(func(n DeprecationNotice) { notices = append(notices, n) }))
	if _, err := c.GetHealthz(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Link != "https://docs.olivares.invalid/how-to/migrate-example/" {
		t.Fatalf("notices = %+v, want the rel=deprecation target from the coalesced value", notices)
	}
}

func TestRawOperationMetrics(t *testing.T) {
	exposition := "# HELP olivares_requests_total Requests.\nolivares_requests_total 42\n"
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if accept := r.Header.Get("Accept"); accept == "application/json" {
			t.Errorf("raw operation must not demand JSON (Accept=%q)", accept)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(exposition))
	}))
	got, err := c.GetMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != exposition {
		t.Errorf("GetMetrics = %q", got)
	}
}

func TestNewRejectsBadEndpoint(t *testing.T) {
	for _, bad := range []string{"", "not-a-url", "/relative"} {
		if _, err := New(bad, ""); err == nil {
			t.Errorf("New(%q) accepted a non-absolute endpoint", bad)
		}
	}
}

// statementExportCSV is a faithful answer from GET /v1/m/finops/statements/{id}/export:
// the handler's nine columns and one line. The 9007199254740993 is 2^53+1 — it is the
// oracle, not filler. Any consumer that routed this value through a JSON number and an
// IEEE double would hand back 9007199254740992.
const statementExportCSV = "cost_center_code,cost_center_name,model,provider,agent,input_tokens,output_tokens,cost_micro_usd,sample_count\n" +
	"ENG-01,Engineering,claude-opus-5,anthropic,,100,50,9007199254740993,1\n"

// TestStatementExportOperationConsumesCSV drives the GENERATED operation against a
// server that answers what the real handler answers.
//
// ⛔ THE DEFECT THIS CLOSES. While the document declared this 200 an application/json
// object, the generator emitted `map[string]any` + c.do, so this exact response died in
// json.Unmarshal with bad_response — a valid 200 that no generated Go client could read.
// The test is on the OPERATION, not on doRaw: the transport seam was always correct, and
// exercising it directly would have stayed green through the whole defect.
func TestStatementExportOperationConsumesCSV(t *testing.T) {
	var got *http.Request
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="chargeback_ENG-01_2026-06-01.csv"`)
		_, _ = io.WriteString(w, statementExportCSV)
	}))

	body, err := c.GetV1MFinopsStatementsByIDExport(context.Background(), "01a084d5-988c-7e46-b396-5cff04bf2793")
	if err != nil {
		t.Fatalf("generated statement export operation failed on a valid CSV 200: %v", err)
	}
	if string(body) != statementExportCSV {
		t.Errorf("body = %q, want the CSV byte-for-byte", body)
	}
	if !strings.Contains(string(body), "9007199254740993") {
		t.Errorf("the exact micro-USD integer did not survive: %q", body)
	}
	if got.URL.Path != "/v1/m/finops/statements/01a084d5-988c-7e46-b396-5cff04bf2793/export" {
		t.Errorf("path = %q, want the id interpolated into the export route", got.URL.Path)
	}
	// A CSV-only route must not demand JSON: the server does not negotiate.
	if accept := got.Header.Get("Accept"); accept == "application/json" {
		t.Errorf("Accept = %q, want no JSON demand on a raw operation", accept)
	}
}
