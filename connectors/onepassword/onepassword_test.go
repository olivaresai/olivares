// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package onepassword

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const testToken = "eventsapi-supersecret-bearer-token"

// recordedRequest captures what the connector sent so a test can assert auth
// headers, method, URL and the POST body (and that no secret rode the body).
type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Body   string
}

// route is one programmed response for a matching call.
type route struct {
	status int
	body   string
}

// stubDoer routes requests by "METHOD path-substring", returning the next
// queued route for each key and recording every request.
type stubDoer struct {
	t      *testing.T
	routes map[string][]route
	calls  []recordedRequest
}

func newStub(t *testing.T) *stubDoer {
	t.Helper()
	return &stubDoer{t: t, routes: map[string][]route{}}
}

// on queues a 200 JSON response for a method + path-substring key.
func (d *stubDoer) on(method, match, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: 200, body: body})
	return d
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
		_ = req.Body.Close()
	}
	d.calls = append(d.calls, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
		Body:   bodyStr,
	})
	for key, queued := range d.routes {
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method || len(queued) == 0 {
			continue
		}
		if strings.Contains(req.URL.Path, parts[1]) {
			r := queued[0]
			d.routes[key] = queued[1:]
			h := http.Header{}
			h.Set("Content-Type", "application/json")
			return &http.Response{
				StatusCode: r.status,
				Header:     h,
				Body:       io.NopCloser(bytes.NewBufferString(r.body)),
				Request:    req,
			}, nil
		}
	}
	d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
	return nil, fmt.Errorf("unreachable")
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func fixedClock() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) }

func openSource(t *testing.T, d *stubDoer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func edges(t *testing.T, sink *captureSink) []model.EdgeObservation {
	t.Helper()
	var out []model.EdgeObservation
	for _, o := range sink.obs {
		e, ok := o.(model.EdgeObservation)
		if !ok {
			t.Fatalf("non-edge observation emitted: %T", o)
		}
		out = append(out, e)
	}
	return out
}

// ---------------------------------------------------------------------------
// Snapshot (custodian roster)
// ---------------------------------------------------------------------------

func TestSnapshotIntrospectSecretStoreRow(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/v2/auth/introspect", fixture(t, "introspect.json"))
	s := openSource(t, d, map[string]string{"events_token": testToken})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceOnePassword {
		t.Errorf("source = %q, want onepassword", g.Source)
	}
	if !g.CapturedAt.Equal(fixedClock()) {
		t.Errorf("CapturedAt = %v", g.CapturedAt)
	}
	if len(g.Identities) != 1 {
		t.Fatalf("identities = %d, want 1 (the account custodian row)", len(g.Identities))
	}
	id := g.Identities[0]
	if id.Ref != "1password:ACCOUNTUUID00000000000001" {
		t.Errorf("ref = %q", id.Ref)
	}
	if id.Type != identitysource.PrincipalNHI || id.Kind != identitysource.KindSecretStore {
		t.Errorf("type/kind = %q/%q, want nhi/secret_store", id.Type, id.Kind)
	}
	if id.DisplayName != "1Password account" {
		t.Errorf("displayName = %q", id.DisplayName)
	}
	if id.Attributes["features"] != "auditevents,itemusages,signinattempts" {
		t.Errorf("features attr = %q", id.Attributes["features"])
	}
	if len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("custodian snapshot must carry no collections/memberships: %+v", g)
	}
}

func TestSnapshotPrunesEmptyAttributes(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/v2/auth/introspect", `{"uuid":"i","account_uuid":"acc","features":[]}`)
	s := openSource(t, d, map[string]string{"events_token": testToken})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Identities[0].Attributes != nil {
		t.Errorf("empty attributes must prune to nil, got %v", g.Identities[0].Attributes)
	}
}

// ---------------------------------------------------------------------------
// Gather (item-usage feed)
// ---------------------------------------------------------------------------

func TestGatherCursorPagination(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, "/api/v2/itemusages", fixture(t, "itemusages_page1.json"))
	d.on(http.MethodPost, "/api/v2/itemusages", fixture(t, "itemusages_page2.json"))
	s := openSource(t, d, map[string]string{"events_token": testToken, "lookback": "24h", "limit": "100"})

	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Exactly two POST calls: ResetCursor then cursor; has_more=false stopped it.
	if len(d.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (ResetCursor + cursor, stop on has_more=false)", len(d.calls))
	}

	// Call 1: ResetCursor form — limit + start_time (= now-lookback), NO cursor.
	var first map[string]any
	if err := json.Unmarshal([]byte(d.calls[0].Body), &first); err != nil {
		t.Fatalf("first body not JSON: %v", err)
	}
	if first["limit"] != float64(100) {
		t.Errorf("first body limit = %v, want 100", first["limit"])
	}
	wantStart := fixedClock().Add(-24 * time.Hour).Format(time.RFC3339)
	if first["start_time"] != wantStart {
		t.Errorf("first body start_time = %v, want %s", first["start_time"], wantStart)
	}
	if _, ok := first["cursor"]; ok {
		t.Error("first body must not carry a cursor (ResetCursor form)")
	}

	// Call 2: cursor form — the cursor from page 1, NO limit/start_time.
	var second map[string]any
	if err := json.Unmarshal([]byte(d.calls[1].Body), &second); err != nil {
		t.Fatalf("second body not JSON: %v", err)
	}
	if second["cursor"] != "cursor-page-2" {
		t.Errorf("second body cursor = %v, want cursor-page-2", second["cursor"])
	}
	if _, ok := second["limit"]; ok {
		t.Error("cursor body must not carry limit")
	}
	if _, ok := second["start_time"]; ok {
		t.Error("cursor body must not carry start_time")
	}

	// 3 edges: page1 has 3 items but one lacks user.uuid; page2 has 2 items but
	// one lacks item_uuid. 2 + 1 = 3.
	es := edges(t, sink)
	if len(es) != 3 {
		t.Fatalf("edges = %d, want 3 (skip missing user uuid + missing item uuid)", len(es))
	}

	e := es[0]
	if e.OriginKind != "identity" || e.OriginRef != "USERUUID00000000000000001" {
		t.Errorf("edge origin = %q/%q", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != "onepassword.item" || e.ResourceRef != "VAULTUUID0000000000000001/ITEMUUID00000000000000001" {
		t.Errorf("edge resource = %q/%q", e.ResourceKind, e.ResourceRef)
	}
	if e.Mode != model.ModeRead {
		t.Errorf("mode = %q, want read (every item usage is a secret read)", e.Mode)
	}
	if e.ToolRef != "reveal" {
		t.Errorf("toolRef = %q, want reveal", e.ToolRef)
	}
	if e.Source != SignalOnePassword {
		t.Errorf("source = %q, want onepassword", e.Source)
	}
	if e.Confidence != model.ConfidenceAttributed {
		t.Errorf("confidence = %q, want attributed", e.Confidence)
	}
	if want := time.Date(2026, 6, 11, 8, 15, 0, 0, time.UTC); !e.ObservedAt.Equal(want) {
		t.Errorf("observedAt = %v, want %v", e.ObservedAt, want)
	}

	// Page-2 event made it through the cursor.
	last := es[2]
	if last.ToolRef != "export" || last.ResourceRef != "VAULTUUID0000000000000002/ITEMUUID00000000000000004" {
		t.Errorf("page-2 edge = %+v", last)
	}
	// Every action shape is a read.
	for _, e := range es {
		if e.Mode != model.ModeRead {
			t.Errorf("action %q mode = %q, want read", e.ToolRef, e.Mode)
		}
	}
}

func TestGatherStopsAtMaxPages(t *testing.T) {
	d := newStub(t)
	// Both pages claim has_more=true; max_pages=2 must bound the walk.
	page := `{"cursor":"c-next","has_more":true,"items":[]}`
	d.on(http.MethodPost, "/api/v2/itemusages", page)
	d.on(http.MethodPost, "/api/v2/itemusages", page)
	s := openSource(t, d, map[string]string{"events_token": testToken, "max_pages": "2"})

	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(d.calls) != 2 {
		t.Fatalf("calls = %d, want exactly max_pages=2", len(d.calls))
	}
}

func TestBearerHeaderOnEveryCall(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/v2/auth/introspect", fixture(t, "introspect.json"))
	d.on(http.MethodPost, "/api/v2/itemusages", `{"cursor":"","has_more":false,"items":[]}`)
	s := openSource(t, d, map[string]string{"events_token": testToken})

	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(d.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(d.calls))
	}
	for _, c := range d.calls {
		if c.Auth != "Bearer "+testToken {
			t.Errorf("%s %s auth = %q, want exact Bearer scheme", c.Method, c.URL, c.Auth)
		}
		if strings.Contains(c.Body, testToken) {
			t.Errorf("token leaked into a request body: %q", c.Body)
		}
	}
}

func TestAPIErrorCarriesStatusNeverToken(t *testing.T) {
	d := newStub(t)
	d.routes["POST /api/v2/itemusages"] = []route{{status: 401, body: `{"Error":{"Message":"Unauthorized"}}`}}
	s := openSource(t, d, map[string]string{"events_token": testToken})

	err := s.Gather(context.Background(), &captureSink{})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry the status: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error must never carry the credential: %v", err)
	}

	d2 := newStub(t)
	d2.routes["GET /api/v2/auth/introspect"] = []route{{status: 403, body: `{"Error":{"Message":"forbidden"}}`}}
	s2 := openSource(t, d2, map[string]string{"events_token": testToken})
	_, err = s2.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), testToken) {
		t.Errorf("introspect error wrong: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Minimal data: no secret, no email, no user name in anything emitted.
// ---------------------------------------------------------------------------

func TestNoSecretOrEmailLeaksIntoEmissions(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/v2/auth/introspect", fixture(t, "introspect.json"))
	d.on(http.MethodPost, "/api/v2/itemusages", fixture(t, "itemusages_page1.json"))
	d.on(http.MethodPost, "/api/v2/itemusages", fixture(t, "itemusages_page2.json"))
	s := openSource(t, d, map[string]string{"events_token": testToken})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// The fixture's user PII and the token must appear in NOTHING emitted.
	forbidden := []string{testToken, "alice@corp.example", "bob@corp.example", "Alice Smith", "Bob Jones"}

	var fields []string
	for _, id := range g.Identities {
		fields = append(fields, id.Ref, id.Kind, id.DisplayName, string(id.Type))
		for k, v := range id.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, c := range g.Collections {
		fields = append(fields, c.Ref, c.DisplayName)
		for k, v := range c.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, m := range g.Memberships {
		fields = append(fields, m.MemberRef, m.CollectionRef)
	}
	for _, e := range edges(t, sink) {
		fields = append(fields, e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef)
		for k, v := range e.Labels {
			fields = append(fields, k, v)
		}
	}
	for _, f := range fields {
		for _, bad := range forbidden {
			if strings.Contains(f, bad) {
				t.Errorf("forbidden value %q leaked into emitted field %q", bad, f)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Offline + descriptor
// ---------------------------------------------------------------------------

func TestOfflineSnapshotEmptyGraphNilError(t *testing.T) {
	s := New() // no doer: any network call would nil-panic — offline must not call
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open without credential must not fail: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot must not error: %v", err)
	}
	if g.Source != identitysource.SourceOnePassword {
		t.Errorf("offline source = %q", g.Source)
	}
	if g.CapturedAt.IsZero() {
		t.Error("offline CapturedAt must be set")
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("offline graph must be empty: %+v", g)
	}
}

func TestOfflineGatherEmitsNothing(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("offline Gather must return nil: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Errorf("offline Gather emitted %d observations", len(sink.obs))
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion || d.Version != "0.1.0" {
		t.Errorf("descriptor header wrong: %+v", d)
	}
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret["events_token"] {
		t.Error("events_token must be declared Secret")
	}
	for _, key := range []string{"base_url", "lookback", "limit", "max_pages", "timeout"} {
		if _, ok := secret[key]; !ok {
			t.Errorf("config field %q missing", key)
		}
	}
}

func TestOpenDefaults(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"events_token": testToken,
		"base_url":     "https://events.1password.eu/",
		"limit":        "-5",  // malformed-ish: non-positive falls back to default
		"max_pages":    "0",   // same
		"lookback":     "-1h", // same
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.baseURL != "https://events.1password.eu" {
		t.Errorf("base_url = %q (trailing slash must be trimmed)", s.baseURL)
	}
	if s.limit != defaultLimit || s.maxPages != defaultMaxPages || s.lookback != defaultLookback {
		t.Errorf("defaults not applied: limit=%d maxPages=%d lookback=%v", s.limit, s.maxPages, s.lookback)
	}
}
