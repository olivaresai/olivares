// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kongaudit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	kongaudit "github.com/olivaresai/olivares/connectors/kong-audit"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink records every observation the connector emits, split by kind so
// tests can assert the edge/finding contract independently.
type capturingSink struct {
	all      []model.Observation
	edges    []model.EdgeObservation
	findings []model.FindingReport
}

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	c.all = append(c.all, obs)
	switch o := obs.(type) {
	case model.EdgeObservation:
		c.edges = append(c.edges, o)
	case model.FindingReport:
		c.findings = append(c.findings, o)
	}
	return nil
}

func open(t *testing.T) *kongaudit.Source {
	t.Helper()
	s := kongaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata/audit.jsonl"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *kongaudit.Source) *capturingSink {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func TestDescriptorContract(t *testing.T) {
	d := kongaudit.New().Descriptor()
	if d.Name != "olivares.kong-audit" {
		t.Errorf("Name = %q, want olivares.kong-audit", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
	var hasPath, pathRequired bool
	for _, f := range d.ConfigFields {
		if f.Key == "path" {
			hasPath = true
			pathRequired = f.Required
		}
	}
	if !hasPath || !pathRequired {
		t.Errorf("path config field must exist and be required: %+v", d.ConfigFields)
	}
}

func TestOpenRequiresPath(t *testing.T) {
	if err := kongaudit.New().Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open with no path must error")
	}
}

// TestAdminAPIEdges asserts that audit_requests records become admin-api edges with
// the method-derived mode and the right identity/source.
func TestAdminAPIEdges(t *testing.T) {
	sink := gather(t, open(t))

	// 3 audit_requests records (GET, POST, DELETE) -> 3 edges.
	if len(sink.edges) != 3 {
		t.Fatalf("got %d edges, want 3: %+v", len(sink.edges), sink.edges)
	}

	byPath := map[string]model.EdgeObservation{}
	for _, e := range sink.edges {
		if e.Source != kongaudit.SignalKongAudit {
			t.Errorf("edge source = %q, want kong_audit", e.Source)
		}
		if e.OriginKind != "identity" {
			t.Errorf("edge OriginKind = %q, want identity", e.OriginKind)
		}
		if e.ResourceKind != "kong.admin_api" {
			t.Errorf("edge ResourceKind = %q, want kong.admin_api", e.ResourceKind)
		}
		byPath[e.ResourceRef] = e
	}

	// GET /services -> read, attributed to the RBAC user.
	if got := byPath["/services"]; got.Mode != model.ModeRead {
		t.Errorf("GET /services mode = %q, want read", got.Mode)
	} else if got.Confidence != model.ConfidenceAttributed {
		t.Errorf("GET /services confidence = %q, want attributed", got.Confidence)
	} else if got.OriginRef != "2e959b45-0053-41cc-9c2c-5458d0964331" {
		t.Errorf("GET /services origin = %q, want rbac_user_id", got.OriginRef)
	} else if got.ToolRef != "GET" {
		t.Errorf("GET /services toolref = %q, want GET", got.ToolRef)
	}

	// POST /consumers -> readwrite (a body-bearing write that also returns a body).
	if got := byPath["/consumers"]; got.Mode != model.ModeReadWrite {
		t.Errorf("POST /consumers mode = %q, want readwrite", got.Mode)
	}

	// DELETE /routes/r1 -> write; no RBAC user in the record, so it falls back to
	// the client IP at approximate confidence.
	got := byPath["/routes/r1"]
	if got.Mode != model.ModeWrite {
		t.Errorf("DELETE /routes/r1 mode = %q, want write", got.Mode)
	}
	if got.Confidence != model.ConfidenceApproximate {
		t.Errorf("DELETE /routes/r1 confidence = %q, want approximate", got.Confidence)
	}
	if got.OriginRef != "10.0.0.9" {
		t.Errorf("DELETE /routes/r1 origin = %q, want client_ip fallback", got.OriginRef)
	}
}

// TestConfigChangeFindings asserts that audit_objects create/update/delete records
// become gateway_config_change findings with the right subject and severity.
func TestConfigChangeFindings(t *testing.T) {
	sink := gather(t, open(t))

	// 3 audit_objects records (create, update, delete) -> 3 findings.
	if len(sink.findings) != 3 {
		t.Fatalf("got %d findings, want 3: %+v", len(sink.findings), sink.findings)
	}

	bySubject := map[string]model.FindingReport{}
	for _, f := range sink.findings {
		if f.Kind != "gateway_config_change" {
			t.Errorf("finding kind = %q, want gateway_config_change", f.Kind)
		}
		if f.Severity != model.SeverityInfo {
			t.Errorf("finding severity = %q, want info (a config change is ops, not an incident)", f.Severity)
		}
		if f.SubjectKind != "kong.entity" {
			t.Errorf("finding SubjectKind = %q, want kong.entity", f.SubjectKind)
		}
		if f.DetailHash == "" || len(f.DetailHash) != 64 {
			t.Errorf("finding DetailHash = %q, want a sha-256 hex", f.DetailHash)
		}
		bySubject[f.SubjectRef] = f
	}

	for _, tc := range []struct {
		subject   string
		wantTitle string
	}{
		{"consumers:16787ed7-d805-434a-9cec-5e5a3e5c9e4f", "kong create consumers by 2e959b45-0053-41cc-9c2c-5458d0964331"},
		{"plugins:aaaa1111-bbbb-2222-cccc-333344445555", "kong update plugins by 2e959b45-0053-41cc-9c2c-5458d0964331"},
		{"routes:r1", "kong delete routes by 2e959b45-0053-41cc-9c2c-5458d0964331"},
	} {
		f, ok := bySubject[tc.subject]
		if !ok {
			t.Errorf("missing finding for subject %q in %v", tc.subject, bySubject)
			continue
		}
		if f.Title != tc.wantTitle {
			t.Errorf("subject %q title = %q, want %q", tc.subject, f.Title, tc.wantTitle)
		}
		// audit_objects time reconstructed from (expire - ttl).
		if f.OccurredAt.IsZero() {
			t.Errorf("subject %q has zero OccurredAt", tc.subject)
		}
	}
}

// TestUnknownRecordsSkipped asserts the non-Kong line in the fixture produced
// neither an edge nor a finding.
func TestUnknownRecordsSkipped(t *testing.T) {
	sink := gather(t, open(t))
	if len(sink.all) != 6 { // 3 edges + 3 findings; the junk line is dropped
		t.Fatalf("got %d total observations, want 6: %+v", len(sink.all), sink.all)
	}
}

// TestNoPayloadOrSecretLeaks is the minimal-data negative test (docs/SECURITY-HARDENING.md): the
// fixture embeds an AWS access key and a recognizable secret string inside the
// audit "payload"/"entity" bodies. Neither field is read, so neither value may
// ever appear in any emitted observation.
func TestNoPayloadOrSecretLeaks(t *testing.T) {
	sink := gather(t, open(t))
	blob, err := json.Marshal(sink.all)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"AKIAIOSFODNN7EXAMPLE", // embedded in both a request payload and an entity body
		"SUPERSECRETVALUE",     // embedded in an entity body
		"hunter2",              // a password fragment in an entity body
		"rate-limiting",        // a value inside an entity body (config), not a field we read
	} {
		if strings.Contains(string(blob), needle) {
			t.Fatalf("a payload/entity value leaked into the emitted observations (%q): %s", needle, blob)
		}
	}
	if len(sink.all) == 0 {
		t.Fatal("expected observations")
	}
}
