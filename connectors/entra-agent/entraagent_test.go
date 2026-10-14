// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	testTokenURL    = "https://login.test.local/token"
	testSecret      = "verysecretclientsecret"
	testAccessToken = "eyJ0eXAfake.graph.access.token"
)

// recordedRequest captures what the connector sent so a test can assert auth
// headers, method, URL and (crucially) that no secret was placed on the wire.
type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Prefer string
	Body   string
}

// route is a programmed response: the body (and status) to return for the i-th
// matching call.
type route struct {
	status int
	body   string
}

// stubDoer routes requests by (method, longest matching path-substring),
// returning the next queued route for each key and recording every request.
// Longest-match keeps the Graph cast endpoints deterministic: a request for
// .../microsoft.graph.agentIdentityBlueprintPrincipal also CONTAINS the
// .../microsoft.graph.agentIdentity match string, so the most specific queued
// key must win regardless of map iteration order.
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

// on queues a 200 JSON response for a method + match key.
func (d *stubDoer) on(method, match, body string) *stubDoer {
	return d.onStatus(method, match, 200, body)
}

// onStatus queues a response with an explicit status.
func (d *stubDoer) onStatus(method, match string, status int, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: status, body: body})
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
		Prefer: req.Header.Get("Prefer"),
		Body:   bodyStr,
	})

	// Pick the LONGEST matching key with a remaining queued route.
	bestKey, bestMatch := "", ""
	for key, queued := range d.routes {
		if len(queued) == 0 {
			continue
		}
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method {
			continue
		}
		match := parts[1]
		if !strings.Contains(req.URL.Path, match) && !strings.Contains(req.URL.String(), match) {
			continue
		}
		if len(match) > len(bestMatch) {
			bestKey, bestMatch = key, match
		}
	}
	if bestKey == "" {
		d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		return nil, fmt.Errorf("unreachable")
	}
	r := d.routes[bestKey][0]
	d.routes[bestKey] = d.routes[bestKey][1:]
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: r.status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(r.body)),
		Request:    req,
	}, nil
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

// openSource opens a connector against the stub with the given settings merged
// over a complete online configuration.
func openSource(t *testing.T, d *stubDoer, overrides map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		"tenant_id":       "tenant-abc",
		"client_id":       "client-xyz",
		"client_secret":   testSecret,
		"oauth_token_url": testTokenURL,
	}
	for k, v := range overrides {
		if v == "" {
			delete(settings, k)
			continue
		}
		settings[k] = v
	}
	s := New()
	if d != nil {
		s.doer = d
	}
	s.now = func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// stubOwnershipEmpty queues empty owners/sponsors pages for the given agent ids.
func stubOwnershipEmpty(d *stubDoer, ids ...string) {
	for _, id := range ids {
		d.on(http.MethodGet, "/servicePrincipals/"+id+"/microsoft.graph.agentIdentity/owners", `{"value":[]}`)
		d.on(http.MethodGet, "/servicePrincipals/"+id+"/microsoft.graph.agentIdentity/sponsors", `{"value":[]}`)
	}
}

func stubAgentUsersEmpty(d *stubDoer) {
	d.on(http.MethodGet, "/v1.0/users/microsoft.graph.agentUser", `{"value":[]}`)
}

// collectSink gathers emitted observations.
type collectSink struct{ obs []model.Observation }

func (c *collectSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

// ---------------------------------------------------------------------------
// Gather: nhi_longlived_credential drift over blueprint passwordCredentials.
// ---------------------------------------------------------------------------

func TestGatherDriftFindings(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", d.fixture("drift_blueprints.json"))

	s := openSource(t, d, map[string]string{
		"ca_posture":         "false",
		"risk_posture":       "false",
		"governance_posture": "false",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// bp-app-1 (2 expiring secrets) => medium; bp-app-2 (1 secret without
	// endDateTime) => high; bp-app-3 (certificates only) => NO finding —
	// cert-based auth is the recommended replacement, never flagged.
	if got := len(sink.obs); got != 2 {
		t.Fatalf("findings = %d, want 2", got)
	}
	byRef := map[string]model.FindingReport{}
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("observation %T, want FindingReport", o)
		}
		byRef[f.SubjectRef] = f
	}

	medium := byRef["bp-app-1"]
	if medium.Kind != identitysource.FindingLongLivedCredential {
		t.Errorf("kind = %q", medium.Kind)
	}
	if medium.Severity != model.SeverityMedium {
		t.Errorf("bp-app-1 severity = %q, want medium (all secrets expire)", medium.Severity)
	}
	if medium.SubjectKind != "identity" {
		t.Errorf("subject kind = %q", medium.SubjectKind)
	}
	if medium.Title != "agent blueprint holds 2 static client secret(s)" {
		t.Errorf("title = %q", medium.Title)
	}
	wantHash := redact.Hash("nhi_longlived_credential|entra-agent|bp-app-1|password_credentials=2")
	if medium.DetailHash != wantHash {
		t.Errorf("DetailHash = %q, want %q", medium.DetailHash, wantHash)
	}
	if !medium.OccurredAt.Equal(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("OccurredAt = %v, want injected clock", medium.OccurredAt)
	}

	high := byRef["bp-app-2"]
	if high.Severity != model.SeverityHigh {
		t.Errorf("bp-app-2 severity = %q, want high (a secret without endDateTime never expires)", high.Severity)
	}
	if high.Title != "agent blueprint holds 1 static client secret(s)" {
		t.Errorf("title = %q", high.Title)
	}

	if _, ok := byRef["bp-app-3"]; ok {
		t.Error("certificate-only blueprint must not be flagged")
	}

	// No emitted string carries a credential or credential metadata the fixture
	// planted (hints, keyIds): the finding is count + expiry presence only.
	for _, f := range byRef {
		for _, field := range []string{f.Kind, f.SubjectKind, f.SubjectRef, f.Title, f.DetailHash} {
			for _, leak := range []string{testSecret, testAccessToken, "Qx3", "Zp9", "Aa1", "k1", "k2", "k3"} {
				if strings.Contains(field, leak) {
					t.Errorf("finding field %q leaks %q", field, leak)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Security invariant: no secret/token ever appears in any emitted Graph field.
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Descriptor + defaults.
// ---------------------------------------------------------------------------

func TestDescriptorShape(t *testing.T) {
	desc := New().Descriptor()
	if desc.Name != Name || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion || desc.Version != "0.2.0" {
		t.Errorf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	keys := map[string]bool{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
		keys[f.Key] = true
	}
	if !secret["client_secret"] {
		t.Error("client_secret must be Secret")
	}
	for _, k := range []string{"tenant_id", "client_id", "base_url", "oauth_token_url", "max_pages", "include_deleted", "expand_ownership", "ca_posture", "risk_posture", "governance_posture", "ingest_signins", "signin_filter", "signin_lookback", "timeout"} {
		if !keys[k] {
			t.Errorf("config field %q missing", k)
		}
	}
}

func TestOpenDefaults(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open with empty config must not fail (offline, not malformed): %v", err)
	}
	if s.baseURL != defaultBaseURL {
		t.Errorf("base_url = %q, want %q", s.baseURL, defaultBaseURL)
	}
	if s.maxPages != defaultMaxPages || s.includeDeleted || !s.expandOwners || !s.caPosture || !s.riskPosture || !s.govPosture || s.ingestSignIns || s.signInFilter != defaultSignInFilter || s.signInLookback != defaultSignInLookback || s.timeout != defaultTimeout {
		t.Errorf("defaults wrong: maxPages=%d includeDeleted=%v expandOwners=%v caPosture=%v riskPosture=%v govPosture=%v ingestSignIns=%v signInFilter=%q signInLookback=%v timeout=%v",
			s.maxPages, s.includeDeleted, s.expandOwners, s.caPosture, s.riskPosture, s.govPosture, s.ingestSignIns, s.signInFilter, s.signInLookback, s.timeout)
	}
	if !s.offline() {
		t.Error("unconfigured connector must be offline")
	}
}
