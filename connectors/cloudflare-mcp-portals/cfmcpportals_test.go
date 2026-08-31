// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const testAccount = "acct-test-456"

type fixtureServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string
	bodies   map[string]string
	statuses map[string]int
}

func newFixtureServer() *fixtureServer {
	f := &fixtureServer{bodies: map[string]string{}, statuses: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fixtureServer) close()                       { f.srv.Close() }
func (f *fixtureServer) set(path, body string)        { f.bodies[path] = body }
func (f *fixtureServer) fail(path string, status int) { f.statuses[path] = status }

func (f *fixtureServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":0,"message":"method not allowed"}]}`))
		return
	}
	if st, ok := f.statuses[r.URL.Path]; ok {
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"boom"}]}`))
		return
	}
	body, ok := f.bodies[r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":404,"message":"no fixture"}]}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (f *fixtureServer) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func seedServers(f *fixtureServer) {
	f.set("/accounts/"+testAccount+"/access/ai-controls/mcp/servers",
		`{"success":true,"errors":[],"result":[`+
			`{"id":"srv1","name":"docs-mcp","http_url":"https://docs.mcp.example.com/mcp","status":"ready"},`+
			`{"id":"srv2","name":"api-mcp","http_url":"https://api.mcp.example.com/mcp?token=secret123","status":"ready"},`+
			`{"id":"srv3","name":"shadow-unmanaged","http_url":"https://rogue.example.com/mcp","status":"ready"}`+
			`]}`)
}

func seedPortals(f *fixtureServer) {
	f.set("/accounts/"+testAccount+"/access/ai-controls/mcp/portals",
		`{"success":true,"errors":[],"result":[`+
			`{"id":"portal1","name":"Engineering Portal","hostname":"mcp.example.com"},`+
			`{"id":"portal2","name":"Design Portal","hostname":"design-mcp.example.com"}`+
			`]}`)
}

func newSource(t *testing.T, base string, approvedServers string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgAPIToken:  "test-token-secret",
		cfgAccountID: testAccount,
		cfgAPIBase:   base,
		cfgTimeout:   "5s",
	}
	if approvedServers != "" {
		settings[cfgApprovedServers] = approvedServers
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) }
	return s
}

func edgeKey(e model.EdgeObservation) string {
	return fmt.Sprintf("%s|%s -> %s|%s [tool=%s mode=%s src=%s conf=%s]",
		e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, e.ToolRef, e.Mode, e.Source, e.Confidence)
}

func TestGatherGolden(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want no findings (no shadow detection enabled), got %v", sink.findings())
	}

	edges := sink.edges()
	if len(edges) != 5 {
		t.Fatalf("want 5 edges (3 servers + 2 portals), got %d", len(edges))
	}

	got := make([]string, 0, len(edges))
	for _, e := range edges {
		got = append(got, edgeKey(e))
	}
	sort.Strings(got)

	want := []string{
		"cf.account|acct-test-456 -> cf.mcp_portal|Design Portal [tool=design-mcp.example.com mode=unknown src=cloudflare_mcp_portals conf=attributed]",
		"cf.account|acct-test-456 -> cf.mcp_portal|Engineering Portal [tool=mcp.example.com mode=unknown src=cloudflare_mcp_portals conf=attributed]",
		"cf.account|acct-test-456 -> cf.mcp_server|api-mcp [tool=https://api.mcp.example.com/mcp mode=unknown src=cloudflare_mcp_portals conf=attributed]",
		"cf.account|acct-test-456 -> cf.mcp_server|docs-mcp [tool=https://docs.mcp.example.com/mcp mode=unknown src=cloudflare_mcp_portals conf=attributed]",
		"cf.account|acct-test-456 -> cf.mcp_server|shadow-unmanaged [tool=https://rogue.example.com/mcp mode=unknown src=cloudflare_mcp_portals conf=attributed]",
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("edge count: got %d want %d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("edge[%d]:\n got=%q\nwant=%q", i, got[i], want[i])
		}
	}
}

func TestShadowDetection(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, `["docs-mcp","api-mcp"]`)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	fr := sink.findings()
	if len(fr) != 1 {
		t.Fatalf("want 1 shadow finding, got %d: %+v", len(fr), fr)
	}
	f := fr[0]
	if f.Kind != "shadow_mcp" {
		t.Errorf("Kind = %q, want shadow_mcp", f.Kind)
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("Severity = %s, want high", f.Severity)
	}
	if f.SubjectKind != resMCPServer || f.SubjectRef != "shadow-unmanaged" {
		t.Errorf("subject = %s/%s", f.SubjectKind, f.SubjectRef)
	}
	if !strings.Contains(f.Title, "shadow-unmanaged") {
		t.Errorf("Title = %q", f.Title)
	}
	if f.DetailHash == "" || strings.Contains(f.DetailHash, "rogue") {
		t.Errorf("DetailHash must be hashed, not raw: %q", f.DetailHash)
	}
}

func TestShadowDetectionAllApproved(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, `["docs-mcp","api-mcp","shadow-unmanaged"]`)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want 0 shadow findings when all approved, got %d", len(sink.findings()))
	}
}

func TestShadowDetectionDisabledWithoutConfig(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want no findings when shadow detection not configured, got %d", len(sink.findings()))
	}
}

func TestGatherServerListFailure(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.fail("/accounts/"+testAccount+"/access/ai-controls/mcp/servers", http.StatusForbidden)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fr := sink.findings()
	if len(fr) != 1 || fr[0].Kind != "health" {
		t.Fatalf("want 1 health finding for server list, got %+v", fr)
	}
	if len(sink.edges()) != 2 {
		t.Fatalf("portals should still emit edges: got %d", len(sink.edges()))
	}
}

func TestGatherPortalListFailure(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	fs.fail("/accounts/"+testAccount+"/access/ai-controls/mcp/portals", http.StatusInternalServerError)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fr := sink.findings()
	if len(fr) != 1 || fr[0].Kind != "health" {
		t.Fatalf("want 1 health finding for portal list, got %+v", fr)
	}
	if len(sink.edges()) != 3 {
		t.Fatalf("servers should still emit edges: got %d", len(sink.edges()))
	}
}

func TestGatherReadOnly(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, `["docs-mcp"]`)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, m := range fs.methods() {
		if !strings.HasPrefix(m, http.MethodGet+" ") {
			t.Fatalf("non-GET request issued: %q", m)
		}
	}
}

func TestGatherRedaction(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, e := range sink.edges() {
		if strings.Contains(e.ToolRef, "token=secret123") || strings.Contains(e.ToolRef, "secret123") {
			t.Fatalf("secret query leaked into ToolRef: %q", e.ToolRef)
		}
	}
}

func TestGatherCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := newSource(t, srv.URL, "")
	sink := &fakeSink{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Gather did not return promptly after cancel")
	}
}

func TestOpenValidation(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		wantErr  bool
	}{
		{"missing token", map[string]string{cfgAccountID: "a"}, true},
		{"missing account", map[string]string{cfgAPIToken: "t"}, true},
		{"bad approved_servers", map[string]string{cfgAPIToken: "t", cfgAccountID: "a", cfgApprovedServers: "not-json"}, true},
		{"ok", map[string]string{cfgAPIToken: "t", cfgAccountID: "a"}, false},
		{"ok with approved", map[string]string{cfgAPIToken: "t", cfgAccountID: "a", cfgApprovedServers: `["a","b"]`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := New().Open(context.Background(), sdk.Config{Settings: tc.settings})
			if tc.wantErr != (err != nil) {
				t.Fatalf("Open err = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Version != version || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor identity wrong: %+v", d)
	}
	var tokenField *sdk.ConfigField
	for i := range d.ConfigFields {
		if d.ConfigFields[i].Key == cfgAPIToken {
			tokenField = &d.ConfigFields[i]
		}
	}
	if tokenField == nil {
		t.Fatal("api_token field missing from descriptor")
	}
	if !tokenField.Secret {
		t.Error("api_token must be Secret:true")
	}
	if !tokenField.Required {
		t.Error("api_token must be Required:true")
	}
}

func TestTokenNeverEmitted(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	const token = "super-secret-cf-zt-token"
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgAPIToken: token, cfgAccountID: testAccount, cfgAPIBase: fs.srv.URL, cfgTimeout: "5s",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) }
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, e := range sink.edges() {
		if strings.Contains(edgeKey(e), token) {
			t.Fatalf("token leaked into edge: %+v", e)
		}
	}
	for _, f := range sink.findings() {
		if strings.Contains(f.Title+f.SubjectRef+f.DetailHash, token) {
			t.Fatalf("token leaked into finding: %+v", f)
		}
	}
}

func TestGatherEmitErrorFatal(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedServers(fs)
	seedPortals(fs)

	s := newSource(t, fs.srv.URL, "")
	want := errors.New("sink closed")
	sink := &fakeSink{emitErr: want}
	err := s.Gather(context.Background(), sink)
	if !errors.Is(err, want) {
		t.Fatalf("want emit error propagated, got %v", err)
	}
}

func TestCloseNoOp(t *testing.T) {
	if err := New().Close(context.Background()); err != nil {
		t.Fatalf("Close on un-opened source: %v", err)
	}
}

func TestGatherEmptyServersAndPortals(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.set("/accounts/"+testAccount+"/access/ai-controls/mcp/servers", `{"success":true,"errors":[],"result":[]}`)
	fs.set("/accounts/"+testAccount+"/access/ai-controls/mcp/portals", `{"success":true,"errors":[],"result":[]}`)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.edges()) != 0 {
		t.Fatalf("want 0 edges for empty lists, got %d", len(sink.edges()))
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want 0 findings for empty lists, got %d", len(sink.findings()))
	}
}

func TestApiFaultError(t *testing.T) {
	withErrs := (&apiFault{status: 403, errs: []apiError{{Code: 10000, Message: "auth"}}}).Error()
	if !strings.Contains(withErrs, "10000") || !strings.Contains(withErrs, "auth") {
		t.Errorf("errs branch: %q", withErrs)
	}
	withMsg := (&apiFault{status: 502, msg: "non-JSON error body"}).Error()
	if !strings.Contains(withMsg, "502") || !strings.Contains(withMsg, "non-JSON") {
		t.Errorf("msg branch: %q", withMsg)
	}
	bare := (&apiFault{status: 500}).Error()
	if !strings.Contains(bare, "500") {
		t.Errorf("bare branch: %q", bare)
	}
}
