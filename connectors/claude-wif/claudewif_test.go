// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// routeDoer answers Admin API GETs from canned JSON keyed by request path, and
// records every request so a test can assert the connector is read-only (GET only).
type routeDoer struct {
	t      *testing.T
	bodies map[string]string
	reqs   []*http.Request
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	body, ok := d.bodies[req.URL.Path]
	if !ok {
		d.t.Fatalf("unexpected request path %q", req.URL.Path)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range s.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
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

func fixedClock() time.Time { return time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC) }

// adminFixtures is the canned Admin API roster used across roster/edge tests.
func adminFixtures() map[string]string {
	return map[string]string{
		"/v1/organizations/users":                       `{"data":[{"id":"user_1","email":"alice@x.com","name":"Alice","role":"admin","added_at":"2026-01-01T00:00:00Z","type":"user"}],"has_more":false,"last_id":"user_1"}`,
		"/v1/organizations/invites":                     `{"data":[{"id":"invite_1","email":"bob@x.com","role":"developer","status":"pending","invited_at":"2026-02-01T00:00:00Z","expires_at":"2026-02-08T00:00:00Z","type":"invite"}],"has_more":false,"last_id":"invite_1"}`,
		"/v1/organizations/api_keys":                    `{"data":[{"id":"apikey_1","name":"prod-key","workspace_id":"wrkspc_1","status":"active","partial_key_hint":"sk-ant-***xyz","created_at":"2026-01-02T00:00:00Z"},{"id":"apikey_2","name":"old-key","workspace_id":"wrkspc_1","status":"inactive","partial_key_hint":"sk-ant-***abc","created_at":"2025-12-01T00:00:00Z"}],"has_more":false,"last_id":"apikey_2"}`,
		"/v1/organizations/workspaces":                  `{"data":[{"id":"wrkspc_1","name":"Production","archived_at":"","created_at":"2025-11-01T00:00:00Z"}],"has_more":false,"last_id":"wrkspc_1"}`,
		"/v1/organizations/workspaces/wrkspc_1/members": `{"data":[{"type":"workspace_member","user_id":"user_1","workspace_id":"wrkspc_1","workspace_role":"workspace_admin"}],"has_more":false,"last_id":"user_1"}`,
	}
}

func newLive(t *testing.T, federation string) (*Source, *routeDoer) {
	t.Helper()
	doer := &routeDoer{t: t, bodies: adminFixtures()}
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"admin_key": "sk-ant-admin-test", "organization_id": "11111111-1111-1111-1111-111111111111"}
	if federation != "" {
		settings["federation"] = federation
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, doer
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor = %+v", d)
	}
	var sawSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == "admin_key" && f.Secret {
			sawSecret = true
		}
	}
	if !sawSecret {
		t.Fatal("admin_key must be declared as a secret config field")
	}
}

func TestGraphProviderContract(t *testing.T) {
	// Compile-time assertions live in claudewif.go; this verifies offline Snapshot
	// is a no-network no-op beyond declared federation.
	s := New()
	if err := s.Open(context.Background(), sdk.Config{}); err != nil {
		t.Fatalf("Open offline: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot offline: %v", err)
	}
	if g.Source != identitysource.SourceAnthropic {
		t.Fatalf("source = %q", g.Source)
	}
	if len(g.Identities) != 0 {
		t.Fatalf("offline snapshot should be empty, got %d identities", len(g.Identities))
	}
}

func TestParseFederation(t *testing.T) {
	good := `[{"rule_id":"fdrl_1","service_account_id":"svac_1","issuer_id":"fdis_1","oauth_scope":"workspace:developer","workspace_id":"wrkspc_1"}]`
	rules, err := parseFederation(good)
	if err != nil {
		t.Fatalf("parse good: %v", err)
	}
	if len(rules) != 1 || rules[0].RuleID != "fdrl_1" {
		t.Fatalf("rules = %+v", rules)
	}
	if rules[0].scope() != scopeWorkspaceDeveloper {
		t.Fatalf("scope = %q", rules[0].scope())
	}

	// empty oauth_scope defaults to developer
	dflt, _ := parseFederation(`[{"rule_id":"fdrl_1","service_account_id":"svac_1"}]`)
	if dflt[0].scope() != scopeWorkspaceDeveloper {
		t.Fatalf("default scope = %q", dflt[0].scope())
	}

	for _, bad := range []string{
		`[{"rule_id":"bad_1","service_account_id":"svac_1"}]`,                       // wrong rule prefix
		`[{"rule_id":"fdrl_1","service_account_id":"bad_1"}]`,                       // wrong sa prefix
		`[{"rule_id":"fdrl_1","service_account_id":"svac_1","workspace_id":"bad"}]`, // wrong workspace
		`not json`,
	} {
		if _, err := parseFederation(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}

	if rules, err := parseFederation(""); err != nil || rules != nil {
		t.Fatalf("empty federation = %v, %v", rules, err)
	}
}
