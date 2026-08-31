// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudflare

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

const (
	testAccount = "acct123"
	testZone    = "zone456"
	// embeddedSecret is an AWS-shaped key planted in a Logpush destination so the
	// redaction test can assert it never survives into an emitted ref.
	embeddedSecret = "AKIAIOSFODNN7EXAMPLE"
)

// fixtureServer is an httptest server that routes Cloudflare REST paths to canned
// fixtures and records every request method+path so a test can assert read-only.
type fixtureServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string
	bodies   map[string]string // path -> JSON body
	statuses map[string]int    // path -> status override
}

func newFixtureServer() *fixtureServer {
	f := &fixtureServer{bodies: map[string]string{}, statuses: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fixtureServer) close() { f.srv.Close() }

func (f *fixtureServer) set(path, body string) { f.bodies[path] = body }

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

// seedHappyPath wires fixtures for every target (account + zone) with two items
// each where ordering matters, so the golden test also proves the sort.
func (f *fixtureServer) seedHappyPath() {
	f.set("/accounts/"+testAccount+"/workers/scripts",
		`{"success":true,"errors":[],"result":[{"id":"zeta-worker"},{"id":"alpha-worker"}]}`)
	f.set("/accounts/"+testAccount+"/r2/buckets",
		`{"success":true,"errors":[],"result":{"buckets":[{"name":"logs-bucket"},{"name":"assets-bucket"}]}}`)
	f.set("/accounts/"+testAccount+"/logpush/jobs",
		`{"success":true,"errors":[],"result":[`+
			`{"id":7,"dataset":"http_requests","destination_conf":"s3://my-bucket/logs?region=eu&access-key-id=`+embeddedSecret+`&secret-access-key=wJalrXUtnFEMI"},`+
			`{"id":3,"dataset":"audit_logs","destination_conf":"https://siem.example.com/ingest?token=topsecret123"}]}`)
	f.set("/zones/"+testZone+"/workers/routes",
		`{"success":true,"errors":[],"result":[`+
			`{"id":"r2","pattern":"*.example.com/api/*","script":"alpha-worker"},`+
			`{"id":"r1","pattern":"app.example.com/*","script":"zeta-worker"}]}`)
	f.set("/zones/"+testZone+"/logpush/jobs",
		`{"success":true,"errors":[],"result":[{"id":11,"dataset":"firewall_events","destination_conf":"r2://zone-logs/fw"}]}`)
}

func newSource(t *testing.T, base, zone string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgAPIToken:  "test-token",
		cfgAccountID: testAccount,
		cfgAPIBase:   base,
		cfgTimeout:   "5s",
	}
	if zone != "" {
		settings[cfgZoneID] = zone
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// edgeKey renders an edge as a stable comparable string for golden assertions.
func edgeKey(e model.EdgeObservation) string {
	return fmt.Sprintf("%s|%s -> %s|%s [tool=%s mode=%s src=%s conf=%s]",
		e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, e.ToolRef, e.Mode, e.Source, e.Confidence)
}

// TestGatherGolden asserts the EXACT set (sorted) of edges emitted across
// workers/routes/r2/logpush from the happy-path fixtures.
func TestGatherGolden(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	s := newSource(t, fs.srv.URL, testZone)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fr := sink.findings(); len(fr) != 0 {
		t.Fatalf("want no findings on happy path, got %v", fr)
	}

	got := make([]string, 0)
	for _, e := range sink.edges() {
		got = append(got, edgeKey(e))
	}
	sort.Strings(got)

	want := []string{
		// account -> workers (sorted by ref)
		"cf.account|acct123 -> cf.worker|alpha-worker [tool= mode=unknown src=cloudflare conf=attributed]",
		"cf.account|acct123 -> cf.worker|zeta-worker [tool= mode=unknown src=cloudflare conf=attributed]",
		// account -> r2 buckets (sorted by ref)
		"cf.account|acct123 -> r2.bucket|assets-bucket [tool= mode=unknown src=cloudflare conf=attributed]",
		"cf.account|acct123 -> r2.bucket|logs-bucket [tool= mode=unknown src=cloudflare conf=attributed]",
		// account -> logpush jobs (sorted by "dataset#id"; destination sanitized)
		"cf.account|acct123 -> cf.logpush_job|audit_logs#3 [tool=https://siem.example.com/ingest mode=unknown src=cloudflare conf=attributed]",
		"cf.account|acct123 -> cf.logpush_job|http_requests#7 [tool=s3://my-bucket/logs mode=unknown src=cloudflare conf=attributed]",
		// zone -> worker routes (sorted by pattern ref; tool=script)
		"cf.zone|zone456 -> cf.worker_route|*.example.com/api/* [tool=alpha-worker mode=unknown src=cloudflare conf=attributed]",
		"cf.zone|zone456 -> cf.worker_route|app.example.com/* [tool=zeta-worker mode=unknown src=cloudflare conf=attributed]",
		// zone -> logpush job
		"cf.zone|zone456 -> cf.logpush_job|firewall_events#11 [tool=r2://zone-logs/fw mode=unknown src=cloudflare conf=attributed]",
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

// TestGatherNoZoneSkipsZoneTargets verifies that without a zone, no zone-scoped
// request is issued and no zone edge is emitted (absent = skipped silently).
func TestGatherNoZoneSkipsZoneTargets(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	s := newSource(t, fs.srv.URL, "") // no zone
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, e := range sink.edges() {
		if e.OriginKind == originZone {
			t.Fatalf("emitted a zone edge with no zone configured: %+v", e)
		}
	}
	for _, m := range fs.methods() {
		if strings.Contains(m, "/zones/") {
			t.Fatalf("issued a zone request with no zone configured: %s", m)
		}
	}
}

// TestGatherHealthFindingOn500 verifies an enabled-but-failing target yields
// exactly one health FindingReport and the pass continues for the other targets.
func TestGatherHealthFindingOn500(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()
	// R2 listing fails with a 500; everything else succeeds.
	fs.fail("/accounts/"+testAccount+"/r2/buckets", http.StatusInternalServerError)

	s := newSource(t, fs.srv.URL, testZone)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	fr := sink.findings()
	if len(fr) != 1 {
		t.Fatalf("want exactly 1 health finding, got %d: %+v", len(fr), fr)
	}
	f := fr[0]
	if f.Kind != "health" {
		t.Errorf("Kind = %q, want health", f.Kind)
	}
	if f.SubjectKind != originAccount || f.SubjectRef != testAccount {
		t.Errorf("subject = %s/%s, want %s/%s", f.SubjectKind, f.SubjectRef, originAccount, testAccount)
	}
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %s, want medium", f.Severity)
	}
	if f.DetailHash == "" || strings.Contains(f.DetailHash, "boom") {
		t.Errorf("DetailHash must be a hash, not raw detail: %q", f.DetailHash)
	}
	// The other targets still produced edges.
	if len(sink.edges()) == 0 {
		t.Fatal("pass should continue and emit edges from the healthy targets")
	}
	for _, e := range sink.edges() {
		if e.ResourceKind == resR2Bucket {
			t.Fatalf("failing R2 target must emit no edges, got %+v", e)
		}
	}
}

// TestGatherAbsentTargetSilent verifies that a target which returns an empty list
// (configured, reachable, but nothing present) produces neither an edge nor a
// finding — absence is not a fault.
func TestGatherAbsentTargetSilent(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.set("/accounts/"+testAccount+"/workers/scripts", `{"success":true,"errors":[],"result":[]}`)
	fs.set("/accounts/"+testAccount+"/r2/buckets", `{"success":true,"errors":[],"result":{"buckets":[]}}`)
	fs.set("/accounts/"+testAccount+"/logpush/jobs", `{"success":true,"errors":[],"result":[]}`)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.edges()) != 0 {
		t.Fatalf("want no edges for empty lists, got %v", sink.edges())
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want no findings for empty lists, got %v", sink.findings())
	}
}

// TestGatherRedaction verifies a secret embedded in a Logpush destination_conf is
// absent from every emitted ref, and that the destination is reduced to its
// non-sensitive host/path (the redaction marker / stripped query proves it).
func TestGatherRedaction(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	s := newSource(t, fs.srv.URL, testZone)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var sawSanitizedDest bool
	for _, e := range sink.edges() {
		for _, ref := range []string{e.OriginRef, e.ResourceRef, e.ToolRef} {
			if strings.Contains(ref, embeddedSecret) {
				t.Fatalf("AWS key leaked into ref %q (edge %+v)", ref, e)
			}
			if strings.Contains(ref, "secret-access-key") || strings.Contains(ref, "wJalrXUtnFEMI") {
				t.Fatalf("secret query leaked into ref %q", ref)
			}
			if strings.Contains(ref, "token=topsecret123") || strings.Contains(ref, "topsecret123") {
				t.Fatalf("token leaked into ref %q", ref)
			}
		}
		// The http_requests job's destination must survive as scheme+host+path only.
		if e.ResourceKind == resLogpushJob && strings.HasPrefix(e.ResourceRef, "http_requests#") {
			if e.ToolRef != "s3://my-bucket/logs" {
				t.Fatalf("destination not reduced to host/path: %q", e.ToolRef)
			}
			sawSanitizedDest = true
		}
	}
	if !sawSanitizedDest {
		t.Fatal("did not observe the sanitized destination edge")
	}
}

// TestGatherRedaction_HostlessDestination covers a Logpush destination_conf whose
// URL has an EMPTY host (an opaque / triple-slash URI). The host-based stripper
// cannot fire on it, so this is exactly the case where a credential in a query
// parameter would survive if SanitizeURL did not also drop the query for hostless
// inputs. The secret uses a key the key=value scrubber does NOT recognize
// ("x-amz-signature"), so only whole-query stripping can remove it.
func TestGatherRedaction_HostlessDestination(t *testing.T) {
	const hostlessSecret = "ZXCV0987SIGNATUREVALUE"
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()
	fs.set("/accounts/"+testAccount+"/logpush/jobs",
		`{"success":true,"errors":[],"result":[`+
			`{"id":42,"dataset":"dns_logs","destination_conf":"s3:///opaque-bucket/dns?x-amz-signature=`+hostlessSecret+`"}]}`)

	s := newSource(t, fs.srv.URL, testZone)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var sawHostless bool
	for _, e := range sink.edges() {
		for _, ref := range []string{e.OriginRef, e.ResourceRef, e.ToolRef} {
			if strings.Contains(ref, hostlessSecret) {
				t.Fatalf("hostless-destination secret leaked into ref %q (edge %+v)", ref, e)
			}
		}
		if e.ResourceKind == resLogpushJob && strings.HasPrefix(e.ResourceRef, "dns_logs#") {
			sawHostless = true
			if strings.Contains(e.ToolRef, "?") {
				t.Fatalf("hostless destination query not stripped: %q", e.ToolRef)
			}
		}
	}
	if !sawHostless {
		t.Fatal("did not observe the hostless logpush destination edge")
	}
}

// TestGatherReadOnly verifies every HTTP request issued during a pass is a GET.
// Cloudflare list/describe endpoints are all GET; the connector never issues a
// create/update/delete/exec call.
func TestGatherReadOnly(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	s := newSource(t, fs.srv.URL, testZone)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	methods := fs.methods()
	if len(methods) == 0 {
		t.Fatal("no requests issued")
	}
	for _, m := range methods {
		if !strings.HasPrefix(m, http.MethodGet+" ") {
			t.Fatalf("non-GET (write) request issued: %q", m)
		}
	}
}

// TestGatherCtxCancel verifies a canceled ctx makes Gather return ctx.Err()
// promptly without emitting a (spurious) health finding for the cancellation.
func TestGatherCtxCancel(t *testing.T) {
	// A handler that blocks until the request's ctx is canceled, so the cancel
	// races into the in-flight GET.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := newSource(t, srv.URL, testZone)
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
	// Cancellation is not a target health fault.
	if len(sink.findings()) != 0 {
		t.Fatalf("cancellation must not emit a health finding, got %v", sink.findings())
	}
}

// TestGatherCtxCancelBeforeStart verifies an already-canceled ctx returns
// immediately with no requests.
func TestGatherCtxCancelBeforeStart(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	s := newSource(t, fs.srv.URL, testZone)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sink := &fakeSink{}
	if err := s.Gather(ctx, sink); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(fs.methods()) != 0 {
		t.Fatalf("canceled ctx must issue no requests, got %v", fs.methods())
	}
}

// TestOpenValidation verifies the required fields are enforced in Open.
func TestOpenValidation(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		wantErr  bool
	}{
		{"missing token", map[string]string{cfgAccountID: "a"}, true},
		{"missing account", map[string]string{cfgAPIToken: "t"}, true},
		{"ok", map[string]string{cfgAPIToken: "t", cfgAccountID: "a"}, false},
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

// TestDescriptor verifies the descriptor declares the api_token as a Secret and
// names the connector correctly.
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
		t.Error("api_token must be declared Secret:true")
	}
	if !tokenField.Required {
		t.Error("api_token must be Required:true")
	}
}

// TestTokenNeverEmitted is a belt-and-braces check that the secret api_token
// never appears in any emitted ref or finding (it travels only in the Authorization
// header, which the test server validates separately).
func TestTokenNeverEmitted(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	const token = "super-secret-cf-token-value"
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgAPIToken: token, cfgAccountID: testAccount, cfgZoneID: testZone, cfgAPIBase: fs.srv.URL, cfgTimeout: "5s",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
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

// TestCloseNoOp verifies Close is safe and returns nil even if Open was never
// called (the SDK requires Close to be safe after a failed Open).
func TestCloseNoOp(t *testing.T) {
	if err := New().Close(context.Background()); err != nil {
		t.Fatalf("Close on un-opened source: %v", err)
	}
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()
	s := newSource(t, fs.srv.URL, "")
	if err := s.Gather(context.Background(), &fakeSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close after Gather: %v", err)
	}
}

// TestGatherEmitErrorFatal verifies an Emit error is fatal to the pass: Gather
// returns it and stops (a closed sink is not something the connector recovers
// from, per the SDK contract).
func TestGatherEmitErrorFatal(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()

	s := newSource(t, fs.srv.URL, testZone)
	want := errors.New("sink closed")
	sink := &fakeSink{emitErr: want}
	err := s.Gather(context.Background(), sink)
	if !errors.Is(err, want) {
		t.Fatalf("want emit error propagated, got %v", err)
	}
}

// TestGatherMalformedResultIsFinding verifies a target whose result rows are
// malformed JSON yields a health finding (not a panic/crash) and the pass
// continues.
func TestGatherMalformedResultIsFinding(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.seedHappyPath()
	// Workers scripts result is a JSON object where an array is expected.
	fs.set("/accounts/"+testAccount+"/workers/scripts",
		`{"success":true,"errors":[],"result":{"not":"an-array"}}`)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fr := sink.findings()
	if len(fr) != 1 || fr[0].SubjectKind != originAccount {
		t.Fatalf("want one account health finding for malformed result, got %+v", fr)
	}
	// R2 + Logpush still succeeded.
	if len(sink.edges()) == 0 {
		t.Fatal("pass should continue past the malformed target")
	}
}

// TestApiFaultError exercises the three rendering branches of *apiFault.Error so
// the message a finding hashes is well-formed in each case.
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

// TestGatherNonJSONErrorBody verifies a 5xx with a non-JSON (HTML) body still
// becomes a health finding rather than a decode crash.
func TestGatherNonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/workers/scripts") {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	defer srv.Close()

	s := newSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 1 {
		t.Fatalf("want one health finding for the HTML 502, got %+v", sink.findings())
	}
}
