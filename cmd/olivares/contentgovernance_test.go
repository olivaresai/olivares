// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	claudecompliancePkg "github.com/olivaresai/olivares/connectors/claude-compliance"
	sdkPkg "github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestContentInventoryMinimalData proves the content governance contract:
// provider-side content is enumerated and classified as governance evidence
// WITHOUT any body, PII, or secret appearing in the emitted findings. The
// inventory carries only structural counts (per kind, active vs soft-deleted)
// and age distribution — enough for the compliance module's retention policies
// and hold-gate, never a content preview.
func TestContentInventoryMinimalData(t *testing.T) {
	doer := &contentInvDoer{t: t}
	src := claudecompliancePkg.New()
	src.SetTestTransport(doer)
	cfg := sdkPkg.Config{Settings: map[string]string{
		"compliance_access_key": "sk-ant-api01-cak",
		"org_ref":               "acme",
	}}
	if err := src.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	var findings []sdkmodel.FindingReport
	sink := &e2eCaptureSink{t: t, findings: &findings}
	if err := src.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	// Find the content inventory finding.
	var contentInventory *sdkmodel.FindingReport
	for i, f := range findings {
		if f.SubjectKind == "claude_compliance_content" {
			contentInventory = &findings[i]
			break
		}
	}
	if contentInventory == nil {
		t.Fatal("content inventory finding must be emitted when CAK is configured")
	}

	// The inventory must carry structural counts, not content.
	if !strings.Contains(contentInventory.Title, "project(s) enumerated") {
		t.Errorf("content inventory title must show project count: %q", contentInventory.Title)
	}
	if !strings.Contains(contentInventory.Title, "2 active") {
		t.Errorf("content inventory must count active projects: %q", contentInventory.Title)
	}
	if !strings.Contains(contentInventory.Title, "1 soft-deleted") {
		t.Errorf("content inventory must count soft-deleted projects: %q", contentInventory.Title)
	}
	if contentInventory.DetailHash == "" {
		t.Error("content inventory must carry a DetailHash (tamper-evidence)")
	}

	// HEADLINE: no PII, no content, no project name/description in the finding.
	piiSecrets := []string{
		"Secret Project", "alice@corp.example",
		"confidential-instructions", "sk-ant-api01-cak",
	}
	for _, f := range findings {
		blob := f.Title + "|" + f.SubjectRef + "|" + f.DetailHash
		for _, secret := range piiSecrets {
			if strings.Contains(blob, secret) {
				t.Fatalf("PII/content %q leaked into finding — minimal-data violated", secret)
			}
		}
	}
}

// TestContentGovernanceClassifierHoldCheck exercises the hold-gate bridge for
// provider-side content: a nil classifier (unconfigured) returns unheld, and the
// kind mapping produces the correct subject vocabulary.
func TestContentGovernanceClassifierHoldCheck(t *testing.T) {
	// Nil classifier (module not wired): unheld, no error.
	var c *contentGovernanceClassifier
	held, err := c.classifyContentForHold(context.Background(), "tenant-1",
		claudecompliancePkg.ContentRef{ID: "chat-1", Kind: "chat"})
	if err != nil || held {
		t.Fatalf("nil classifier must return unheld, got held=%v err=%v", held, err)
	}

	// Kind mapping.
	if got := contentRefToSubjectKind("chat"); got != "chat" {
		t.Errorf("chat → %q, want chat", got)
	}
	if got := contentRefToSubjectKind("project"); got != "project" {
		t.Errorf("project → %q, want project", got)
	}
	if got := contentRefToSubjectKind("unknown"); got != "document" {
		t.Errorf("unknown → %q, want document (conservative)", got)
	}
}

// contentInvDoer answers the directory + content endpoints for the inventory test.
type contentInvDoer struct{ t *testing.T }

func (d *contentInvDoer) Do(req *http.Request) (*http.Response, error) {
	d.t.Helper()
	if req.Method != http.MethodGet {
		d.t.Errorf("non-GET request %s — content inventory must be read-only", req.Method)
	}
	p := req.URL.Path
	switch {
	case p == "/v1/compliance/organizations":
		return jsonResp(200, `{"data":[]}`), nil
	case p == "/v1/compliance/groups":
		return jsonResp(200, `{"data":[],"has_more":false}`), nil
	case strings.HasSuffix(p, "/settings"):
		return jsonResp(404, `{"error":"not found"}`), nil
	case strings.HasSuffix(p, "/roles"):
		return jsonResp(200, `{"data":[],"has_more":false}`), nil
	case strings.HasSuffix(p, "/users"):
		return jsonResp(200, `{"data":[],"has_more":false}`), nil
	case p == "/v1/compliance/apps/projects":
		// Return 3 projects: 2 active, 1 soft-deleted. The SENSITIVE "name" field
		// is present in the fixture but must NOT appear in the finding.
		return jsonResp(200, `{"data":[
			{"id":"proj_1","name":"Secret Project","organization_uuid":"org_a","deleted_at":null,"created_at":"2026-06-01T00:00:00Z"},
			{"id":"proj_2","name":"Also Secret","organization_uuid":"org_a","deleted_at":null,"created_at":"2026-05-15T00:00:00Z"},
			{"id":"proj_3","name":"Deleted One","organization_uuid":"org_a","deleted_at":"2026-06-10T00:00:00Z","created_at":"2026-04-01T00:00:00Z"}
		],"has_more":false}`), nil
	default:
		d.t.Fatalf("unexpected path %q", p)
		return nil, nil
	}
}

func jsonResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
