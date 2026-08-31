// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestProtocolVersionIsCurrentStable(t *testing.T) {
	// currentRevision must be the 2026-07-28 frozen-RC revision (AIP-01).
	// protocolVersion is intentionally kept at 2025-11-25: the 2026-07-28 revision
	// removes Initialize; protocolVersion is only sent by the legacy (non-stateless)
	// path and must NOT be bumped.
	if currentRevision != "2026-07-28" {
		t.Fatalf("currentRevision must be the 2026-07-28 frozen-RC revision, got %q", currentRevision)
	}
	if protocolVersion != "2025-11-25" {
		t.Fatalf("protocolVersion (legacy Initialize) must remain 2025-11-25, got %q", protocolVersion)
	}
	if revisionTimeline[len(revisionTimeline)-1] != currentRevision {
		t.Errorf("currentRevision must be the newest entry on the timeline")
	}
}

func TestClassifyRevision(t *testing.T) {
	cases := []struct {
		rev  string
		want revisionStatus
	}{
		{"2026-07-28", revisionCurrent}, // current baseline
		{"2025-11-25", revisionStale},   // known-but-older (legacy Initialize path)
		{"2025-06-18", revisionStale},
		{"2025-03-26", revisionStale},
		{"2024-11-05", revisionStale},
		{"garbage", revisionUnknown},
		{"", revisionUnknown}, // spec requires the field; absence is a signal
	}
	for _, c := range cases {
		if got := classifyRevision(c.rev); got != c.want {
			t.Errorf("classifyRevision(%q) = %q, want %q", c.rev, got, c.want)
		}
	}
}

func TestRevisionFindingSeverityAndTitle(t *testing.T) {
	at := fixedTime()
	current := revisionFinding("srv", currentRevision, at)
	if current.Severity != model.SeverityInfo || current.Kind != findingRevision {
		t.Errorf("current revision should be an Info mcp_revision finding: %+v", current)
	}
	stale := revisionFinding("srv", revision20250618, at)
	if stale.Severity != model.SeverityLow {
		t.Errorf("stale revision should be Low, got %q", stale.Severity)
	}
	missing := revisionFinding("srv", "", at)
	if missing.Severity != model.SeverityInfo {
		t.Errorf("absent revision should be Info, got %q", missing.Severity)
	}
	if len(stale.DetailHash) != 64 {
		t.Errorf("DetailHash must be a SHA-256 hex, got %q", stale.DetailHash)
	}
}
