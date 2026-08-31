// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fixtureDoer multiplexes by request path to the recorded Cursor fixtures. It records
// every request (so a test can assert the connector is read-only and authenticates), and
// can be told to return 403 for a given path (the plan-gated degrade test).
type fixtureDoer struct {
	t           *testing.T
	reqs        []*http.Request
	unavailable map[string]bool // path -> return 403
	fail        map[string]int  // path -> return this status (e.g. 500) with a body
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	p := req.URL.Path
	if d.unavailable[p] {
		return resp(403, `{"error":"forbidden"}`), nil
	}
	if st := d.fail[p]; st != 0 {
		return resp(st, `{"error":"boom"}`), nil
	}
	var file string
	switch p {
	case membersPath:
		file = "members.json"
	case usagePath:
		file = "filtered-usage-events.json"
	case spendPath:
		file = "spend.json"
	case auditPath:
		file = "audit-logs.json"
	default:
		d.t.Fatalf("unexpected (possibly mutating) request path %q %q", req.Method, p)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return resp(200, string(body)), nil
}

func resp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range s.obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

func (s *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func newSource(t *testing.T, fd *fixtureDoer, cfg map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = fd
	s.now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
	if cfg == nil {
		cfg = map[string]string{}
	}
	if _, ok := cfg["api_key"]; !ok {
		cfg["api_key"] = "key-secret-xyz"
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGatherBilledCostSamples(t *testing.T) {
	fd := &fixtureDoer{t: t}
	s := newSource(t, fd, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	costs := sink.costs()
	if len(costs) != 3 { // 3 chargeable events (one non-chargeable is skipped)
		t.Fatalf("got %d cost samples, want 3", len(costs))
	}
	byActor := map[string]model.CostSample{}
	for _, c := range costs {
		byActor[c.Actor] = c
		if c.ProviderRef != modelprovider.ProviderCursor {
			t.Errorf("ProviderRef = %q, want cursor", c.ProviderRef)
		}
		if c.Provenance != model.ProvenanceBilled {
			t.Errorf("Provenance = %q, want billed", c.Provenance)
		}
		if c.CostType != costTypeCursor {
			t.Errorf("CostType = %q, want cursor", c.CostType)
		}
	}

	// Alice: chargedCents 20.18232 -> 201823 micro-USD (cents * 10_000, rounded).
	alice, ok := byActor["user_alice"] // attributed to the stable member id, not email
	if !ok {
		t.Fatalf("no cost sample attributed to user_alice; actors=%v", keys(byActor))
	}
	if alice.CostMicroUSD != 201823 {
		t.Errorf("alice CostMicroUSD = %d, want 201823", alice.CostMicroUSD)
	}
	if alice.ModelRef != "claude-3.5-sonnet" {
		t.Errorf("alice ModelRef = %q", alice.ModelRef)
	}
	// InputTokens is the documented TOTAL input volume (uncached 12000 + cache-write 4000
	// + cache-read 30000 = 46000); the cache-read split is carried separately.
	if alice.InputTokens != 46000 || alice.OutputTokens != 800 || alice.CacheReadTokens != 30000 {
		t.Errorf("alice token mapping wrong: in=%d out=%d cacheRead=%d", alice.InputTokens, alice.OutputTokens, alice.CacheReadTokens)
	}

	// Bob: a flat (non-token) chargeable event, 4.0 cents -> 40000 micro-USD, no tokens.
	bob := byActor["user_bob"]
	if bob.CostMicroUSD != 40000 || bob.InputTokens != 0 {
		t.Errorf("bob sample wrong: micro=%d in=%d", bob.CostMicroUSD, bob.InputTokens)
	}

	// Service account: attributed to a non-PII svc ref, never an email.
	if _, ok := byActor["svc:svc_ci_runner"]; !ok {
		t.Errorf("service-account event not attributed to svc ref; actors=%v", keys(byActor))
	}
}

func TestGatherMemberInventory(t *testing.T) {
	fd := &fixtureDoer{t: t}
	// Disable everything but members to isolate inventory.
	s := newSource(t, fd, map[string]string{"usage": "false", "spend": "false", "audit": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var inv int
	for _, f := range sink.findings() {
		if f.Kind == "inventory" && f.SubjectKind == subjectMember {
			inv++
			if strings.Contains(f.Title, "@") {
				t.Errorf("member finding title leaks email: %q", f.Title)
			}
		}
	}
	if inv != 3 {
		t.Fatalf("got %d member inventory findings, want 3", inv)
	}
}

func TestGatherBudgetPosture(t *testing.T) {
	fd := &fixtureDoer{t: t}
	s := newSource(t, fd, map[string]string{"usage": "false", "members": "false", "audit": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	byRef := map[string]model.FindingReport{}
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectBudget {
			byRef[f.SubjectRef] = f
		}
	}
	// alice 95% -> Medium (approaching); bob 120% -> High (over); dave no limit -> none;
	// erin 10% -> none.
	if len(byRef) != 2 {
		t.Fatalf("got %d budget findings, want 2 (alice+bob); refs=%v", len(byRef), keys(byRef))
	}
	if byRef["user_alice"].Severity != model.SeverityMedium {
		t.Errorf("alice budget severity = %q, want medium", byRef["user_alice"].Severity)
	}
	if byRef["user_bob"].Severity != model.SeverityHigh {
		t.Errorf("bob budget severity = %q, want high", byRef["user_bob"].Severity)
	}
}

func TestGatherAuditEvidence(t *testing.T) {
	fd := &fixtureDoer{t: t}
	s := newSource(t, fd, map[string]string{"usage": "false", "members": "false", "spend": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var n int
	for _, f := range sink.findings() {
		if f.Kind == findingKindActivity && f.SubjectKind == subjectAudit {
			n++
			// The audit detail (email/ip/payload) must be hashed, never in the title.
			if strings.Contains(f.Title, "@") || strings.Contains(f.Title, "203.0.113") {
				t.Errorf("audit finding leaks PII in title: %q", f.Title)
			}
		}
	}
	if n != 2 {
		t.Fatalf("got %d audit findings, want 2", n)
	}
}

func TestReadOnlyAndAuthenticated(t *testing.T) {
	fd := &fixtureDoer{t: t}
	s := newSource(t, fd, nil)
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(fd.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range fd.reqs {
		// Read-only: only GET, or POST to the documented query-with-body READ endpoints.
		if r.Method == http.MethodPost && r.URL.Path != usagePath && r.URL.Path != spendPath {
			t.Errorf("POST to a non-read path %q", r.URL.Path)
		}
		// Authenticated as Basic with the key as username + empty password.
		u, p, ok := r.BasicAuth()
		if !ok || u != "key-secret-xyz" || p != "" {
			t.Errorf("request to %q not Basic-authed key-as-username/empty-password (ok=%v u=%q p=%q)", r.URL.Path, ok, u, p)
		}
	}
}

func TestPlanGatedDegrades(t *testing.T) {
	fd := &fixtureDoer{t: t, unavailable: map[string]bool{usagePath: true}}
	s := newSource(t, fd, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must not abort on a plan-gated 403: %v", err)
	}
	var posture bool
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectSurface && strings.Contains(f.Title, "Usage Events") {
			posture = true
			if f.Severity != model.SeverityMedium {
				t.Errorf("degrade severity = %q, want medium", f.Severity)
			}
		}
	}
	if !posture {
		t.Fatal("a 403 on usage events must degrade to an honest posture finding")
	}
	// The rest of the gather (members/audit/spend) still ran.
	if len(sink.costs()) != 0 {
		t.Errorf("no cost samples expected when usage is gated, got %d", len(sink.costs()))
	}
}

func TestOfflineEmitsNothing(t *testing.T) {
	fd := &fixtureDoer{t: t}
	s := New()
	s.doer = fd
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline (no api_key) must emit nothing, got %d", len(sink.obs))
	}
	if len(fd.reqs) != 0 {
		t.Fatalf("offline must make no request, got %d", len(fd.reqs))
	}
}

func TestNoSecretLeakOnError(t *testing.T) {
	fd := &fixtureDoer{t: t, fail: map[string]int{membersPath: 500}}
	s := newSource(t, fd, nil)
	err := s.Gather(context.Background(), &captureSink{})
	if err == nil {
		t.Fatal("a 500 should surface as a retriable error")
	}
	if strings.Contains(err.Error(), "key-secret-xyz") {
		t.Fatalf("SECURITY: the api key leaked into an error: %v", err)
	}
}

// truncDoer always reports another page of usage events, so the gather hits the max_pages
// bound with data still pending (the silent-truncation path).
type truncDoer struct{ t *testing.T }

func (d *truncDoer) Do(req *http.Request) (*http.Response, error) {
	switch req.URL.Path {
	case membersPath:
		body, _ := os.ReadFile(filepath.Join("testdata", "members.json"))
		return resp(200, string(body)), nil
	case usagePath:
		return resp(200, `{"usageEvents":[{"timestamp":"1717200000000","userEmail":"alice@example.com","model":"m","kind":"agent","isChargeable":true,"isTokenBasedCall":false,"chargedCents":1.0}],"pagination":{"hasNextPage":true}}`), nil
	default:
		d.t.Fatalf("unexpected path %q", req.URL.Path)
		return nil, nil
	}
}

func TestPaginationTruncationEmitsCoverage(t *testing.T) {
	fd := &truncDoer{t: t}
	s := New()
	s.doer = fd
	s.now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"api_key": "k", "audit": "false", "spend": "false", "max_pages": "1",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var cov int
	for _, f := range sink.findings() {
		if f.Kind == "health" && strings.Contains(f.Title, "Usage Events") && strings.Contains(f.Title, "max_pages") {
			cov++
			if f.Severity != model.SeverityLow {
				t.Errorf("coverage severity = %q, want low", f.Severity)
			}
		}
	}
	if cov != 1 {
		t.Fatalf("truncated pagination must emit exactly one coverage finding, got %d", cov)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
