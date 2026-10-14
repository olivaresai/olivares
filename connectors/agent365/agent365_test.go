// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agent365

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// recordedRequest captures what the connector sent so a test can assert the
// auth header, method, URL and that no secret was placed on the wire.
type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Body   string
}

// route is one programmed response for a match key (consumed in order).
type route struct {
	status int
	body   string
}

// stubDoer routes requests by (method, path-substring), returning the next
// queued route for each key and recording every request.
type stubDoer struct {
	t       *testing.T
	routes  map[string][]route // key: METHOD + " " + pathOrURLSubstring
	calls   []recordedRequest
	fixture func(name string) string
}

func newStub(t *testing.T) *stubDoer {
	t.Helper()
	return &stubDoer{
		t:      t,
		routes: map[string][]route{},
		fixture: func(name string) string {
			b, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("read fixture %s: %v", name, err)
			}
			return string(b)
		},
	}
}

// on queues a 200 JSON response for a method + match key. match is compared as
// a substring of the request URL path (or the full URL).
func (d *stubDoer) on(method, match, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: 200, body: body})
	return d
}

func (d *stubDoer) onStatus(method, match string, status int, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: status, body: body})
	return d
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			d.t.Fatalf("read request body: %v", err)
		}
		body = string(b)
	}
	d.calls = append(d.calls, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
		Body:   body,
	})
	bestKey := ""
	bestMatchLen := -1
	for key, queued := range d.routes {
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method || len(queued) == 0 {
			continue
		}
		if strings.Contains(req.URL.Path, parts[1]) || strings.Contains(req.URL.String(), parts[1]) {
			if len(parts[1]) > bestMatchLen {
				bestKey = key
				bestMatchLen = len(parts[1])
			}
		}
	}
	if bestKey != "" {
		queued := d.routes[bestKey]
		r := queued[0]
		d.routes[bestKey] = queued[1:]
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: r.status,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString(r.body)),
			Request:    req,
		}, nil
	}
	d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
	return nil, fmt.Errorf("unreachable")
}

const testToken = "eyJdelegated.supersecret.token"

const (
	testTokenURL     = "https://login.test.local/tenant/oauth2/v2.0/token"
	testClientSecret = "client-secret-supersecret"
	mintedToken      = "minted-agent365-token"
)

func openSource(t *testing.T, d *stubDoer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// sawPath reports whether any recorded request URL contained sub.
func sawPath(d *stubDoer, sub string) bool {
	for _, c := range d.calls {
		if strings.Contains(c.URL, sub) {
			return true
		}
	}
	return false
}

// assertNoSecret walks every string field of the Graph and fails if any secret
// appears, proving the connector carries only registry metadata.
func assertNoSecret(t *testing.T, g identitysource.Graph, secrets ...string) {
	t.Helper()
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
	for _, f := range fields {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(f, secret) {
				t.Errorf("secret leaked into Graph field %q", f)
			}
		}
	}
}

func TestDescriptorShape(t *testing.T) {
	desc := New().Descriptor()
	if desc.Name != Name || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion || desc.Version != "0.2.0" {
		t.Errorf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret["access_token"] {
		t.Error("access_token must be Secret")
	}
	if !secret["client_secret"] {
		t.Error("client_secret must be Secret")
	}
}

func TestOpenDefaults(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"max_pages": "-3"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.baseURL != defaultBaseURL {
		t.Errorf("base_url = %q, want default %q", s.baseURL, defaultBaseURL)
	}
	if s.maxPages != defaultMaxPages {
		t.Errorf("max_pages = %d, want default %d (non-positive rejected)", s.maxPages, defaultMaxPages)
	}
}

func TestGatherPostureFindings(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	findings := runPostureGather(t, fixed)
	if got := len(findings); got != 2 {
		t.Fatalf("findings = %d, want 2: %+v", got, findings)
	}

	first := findings[0]
	if first.Kind != findingBlockedDeployed || first.Severity != model.SeverityMedium {
		t.Fatalf("first finding kind/severity = %s/%s", first.Kind, first.Severity)
	}
	if first.SubjectKind != "identity" || first.SubjectRef != "P_blocked_deployed" {
		t.Errorf("first subject = %s/%s", first.SubjectKind, first.SubjectRef)
	}
	if first.Title != "blocked package still deployed: Blocked Deployed Agent" {
		t.Errorf("first title = %q", first.Title)
	}
	if want := redact.Hash("agent365_blocked_deployed|agent365|P_blocked_deployed|some"); first.DetailHash != want {
		t.Errorf("first DetailHash = %q, want %q", first.DetailHash, want)
	}
	if !first.OccurredAt.Equal(fixed) {
		t.Errorf("first OccurredAt = %v, want %v", first.OccurredAt, fixed)
	}

	second := findings[1]
	if second.Kind != findingExternalBroadDeployment || second.Severity != model.SeverityLow {
		t.Fatalf("second finding kind/severity = %s/%s", second.Kind, second.Severity)
	}
	if second.SubjectKind != "identity" || second.SubjectRef != "P_external_all" {
		t.Errorf("second subject = %s/%s", second.SubjectKind, second.SubjectRef)
	}
	if second.Title != "external package deployed to all users: External Everywhere Agent" {
		t.Errorf("second title = %q", second.Title)
	}
	if want := redact.Hash("agent365_external_broad_deployment|agent365|P_external_all"); second.DetailHash != want {
		t.Errorf("second DetailHash = %q, want %q", second.DetailHash, want)
	}
}

func TestGatherPostureDeterministic(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	a := summarizeFindings(runPostureGather(t, fixed))
	b := summarizeFindings(runPostureGather(t, fixed))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("finding order/content changed between runs:\n%v\n%v", a, b)
	}
}

func runPostureGather(t *testing.T, fixed time.Time) []model.FindingReport {
	t.Helper()
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("posture_packages.json"))
	s := openSource(t, d, map[string]string{"access_token": testToken})
	s.now = func() time.Time { return fixed }
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings(t)
}

type findingSummary struct {
	Kind       string
	Severity   model.Severity
	SubjectRef string
	Title      string
	DetailHash string
}

func summarizeFindings(findings []model.FindingReport) []findingSummary {
	out := make([]findingSummary, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingSummary{
			Kind:       f.Kind,
			Severity:   f.Severity,
			SubjectRef: f.SubjectRef,
			Title:      f.Title,
			DetailHash: f.DetailHash,
		})
	}
	return out
}

type collectSink struct {
	obs []model.Observation
}

func (s *collectSink) Emit(_ context.Context, obs model.Observation) error {
	s.obs = append(s.obs, obs)
	return nil
}

func (s *collectSink) findings(t *testing.T) []model.FindingReport {
	t.Helper()
	out := make([]model.FindingReport, 0, len(s.obs))
	for _, obs := range s.obs {
		f, ok := obs.(model.FindingReport)
		if !ok {
			t.Fatalf("observation %T, want FindingReport", obs)
		}
		out = append(out, f)
	}
	return out
}

// sinkFunc adapts a func to sdk.Sink for no-emit assertions.
type sinkFunc func() error

func (f sinkFunc) Emit(context.Context, model.Observation) error { return f() }
