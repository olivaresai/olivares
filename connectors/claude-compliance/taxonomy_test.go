// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestClassifyActivity_RealEventTypes verifies the taxonomy classifies the real
// Compliance API activity type names into the right category, flags the
// security-relevant ones, and is honest (CategoryOther) about an unknown type.
func TestClassifyActivity_RealEventTypes(t *testing.T) {
	cases := []struct {
		typ      string
		cat      ActivityCategory
		security bool
	}{
		{"claude_chat_created", CategoryChat, false},
		{"claude_chat_deleted", CategoryData, true}, // a deletion is data/eDiscovery-relevant
		{"claude_project_created", CategoryProject, false},
		{"user_signed_in", CategoryAuthn, true},
		{"user_signed_out", CategoryAuthn, false},
		{"workspace_member_role_changed", CategoryMembership, true},
		{"user_invited", CategoryMembership, true},
		{"api_key_created", CategoryCredential, true},
		{"compliance_api_accessed", CategoryAudit, true},
		{"data_exported", CategoryData, true},
		{"organization_settings_updated", CategoryAdmin, true},
		{"workspace_created", CategoryWorkspace, false},
		{"some_future_event_we_have_not_seen", CategoryOther, false},
		{"", CategoryOther, false},
	}
	for _, c := range cases {
		got := ClassifyActivity(c.typ)
		if got.Category != c.cat {
			t.Errorf("ClassifyActivity(%q).Category = %q, want %q", c.typ, got.Category, c.cat)
		}
		if got.SecurityRelevant != c.security {
			t.Errorf("ClassifyActivity(%q).SecurityRelevant = %v, want %v", c.typ, got.SecurityRelevant, c.security)
		}
	}
}

// TestComplianceGaps_DocumentedHonestly verifies the gap inventory names the gaps the
// task requires (ZDR, Cowork, EU-routing, Enterprise-gating, content-DELETE) and routes
// each to an owner — so the product states what the feed does NOT cover.
func TestComplianceGaps_DocumentedHonestly(t *testing.T) {
	gaps := ComplianceGaps()
	if len(gaps) == 0 {
		t.Fatal("gaps must be documented, not empty")
	}
	blob := strings.ToLower(func() string {
		var b strings.Builder
		for _, g := range gaps {
			b.WriteString(g.ID + " " + g.Title + " " + g.Detail + " " + g.Owner + "\n")
			if g.Owner == "" {
				t.Errorf("gap %q has no owner — a gap must be tracked, not hand-waved", g.ID)
			}
		}
		return b.String()
	}())
	for _, want := range []string{"zdr", "cowork", "eu", "enterprise", "delete"} {
		if !strings.Contains(blob, want) {
			t.Errorf("gap inventory must mention %q honestly:\n%s", want, blob)
		}
	}
	// The two reassigned depths must hand off honestly to their owning COMPONENT
	// (named area, not an internal session number).
	for _, want := range []string{"observability", "content.go"} {
		if !strings.Contains(blob, want) {
			t.Errorf("gap inventory must hand off depth to %q", want)
		}
	}
	// Returned slice is a copy: mutating it must not affect package state.
	gaps[0].ID = "tampered"
	if ComplianceGaps()[0].ID == "tampered" {
		t.Fatal("ComplianceGaps must return a copy, not the package slice")
	}
}

// TestCoverageFindingShapeAndMinimalData verifies the coverage finding is a posture
// Info finding referencing the org, carrying a hashed (non-sensitive) detail.
func TestCoverageFindingShapeAndMinimalData(t *testing.T) {
	s := New()
	s.now = fixedClock
	s.orgRef = "acme"
	f := s.coverageFinding(fixedClock())
	if f.Kind != findingKindCoverage || f.Severity != model.SeverityInfo {
		t.Fatalf("coverage finding must be posture/info: %+v", f)
	}
	if f.SubjectRef != "acme" || f.SubjectKind != "claude_compliance" {
		t.Errorf("coverage subject = %s/%s", f.SubjectKind, f.SubjectRef)
	}
	if f.DetailHash == "" {
		t.Error("coverage finding must carry a hashed detail")
	}
	if !strings.Contains(f.Title, "gaps documented") {
		t.Errorf("coverage title should state gaps are documented: %q", f.Title)
	}
}

// TestOfflineEmitsNoCoverage verifies the Enterprise-gated honest-absence posture
// extends to the coverage finding: with no key the connector emits NOTHING at all
// (not even the gaps doc), so an unconfigured connector stays fully silent.
func TestOfflineEmitsNoCoverage(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline connector emitted %d findings, want 0 (Enterprise-gated honest absence)", len(sink.obs))
	}
}
