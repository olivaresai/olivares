// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestGatherAssistants_DeprecationFindings(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}

	fs := govFindings(sink.obs)
	deprecations := findingsByKind(fs, "deprecation_risk")
	if len(deprecations) != 3 {
		t.Fatalf("emitted %d deprecation findings, want 3", len(deprecations))
	}
	for _, f := range deprecations {
		if f.SubjectKind != subjectAssistant {
			t.Fatalf("deprecation subject kind = %q, want %s", f.SubjectKind, subjectAssistant)
		}
		if f.Severity != model.SeverityMedium {
			t.Fatalf("deprecation severity = %q, want medium", f.Severity)
		}
		if !strings.Contains(f.Title, "85") {
			t.Fatalf("deprecation title missing days left: %q", f.Title)
		}
	}

	summaries := findingsBySubjectRef(fs, "assistants_deprecation")
	if len(summaries) != 1 {
		t.Fatalf("emitted %d deprecation summaries, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.Kind != "posture" || summary.SubjectKind != subjectSurface {
		t.Fatalf("summary finding = %+v", summary)
	}
	if summary.Severity != model.SeverityMedium {
		t.Fatalf("summary severity = %q, want medium", summary.Severity)
	}
	if !strings.Contains(summary.Title, "3 assistant(s)") {
		t.Fatalf("summary title missing assistant count: %q", summary.Title)
	}
}

func TestGatherAssistants_DeprecationSeverityThresholds(t *testing.T) {
	tests := []struct {
		name       string
		now        time.Time
		want       model.Severity
		wantPassed bool
	}{
		{
			name: "low before ninety days",
			now:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			want: model.SeverityLow,
		},
		{
			name: "high inside thirty days",
			now:  time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
			want: model.SeverityHigh,
		},
		{
			name:       "high after sunset while api still answers",
			now:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			want:       model.SeverityHigh,
			wantPassed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doer := &asstDoer{t: t}
			s := newAsstSource(t, doer, nil)
			now := tc.now
			s.now = func() time.Time { return now }
			sink := &captureSink{}
			if err := s.gatherAssistants(context.Background(), sink); err != nil {
				t.Fatalf("gatherAssistants: %v", err)
			}

			deprecations := findingsByKind(govFindings(sink.obs), "deprecation_risk")
			if len(deprecations) != 3 {
				t.Fatalf("emitted %d deprecation findings, want 3", len(deprecations))
			}
			for _, f := range deprecations {
				if f.Severity != tc.want {
					t.Fatalf("deprecation severity = %q, want %q", f.Severity, tc.want)
				}
				if tc.wantPassed && !strings.Contains(f.Title, "has passed") {
					t.Fatalf("post-sunset title did not state sunset passed: %q", f.Title)
				}
			}
		})
	}
}

func TestGatherAssistants_PostSunset410DegradesInfo(t *testing.T) {
	doer := &asstDoer{t: t, statuses: map[string]int{"/v1/assistants": 410}}
	s := newAsstSource(t, doer, nil)
	s.now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants must not fail on post-sunset 410; got %v", err)
	}

	fs := govFindings(sink.obs)
	if len(fs) != 1 {
		t.Fatalf("emitted %d findings, want 1", len(fs))
	}
	f := fs[0]
	if f.Kind != "posture" || f.SubjectKind != subjectSurface || f.SubjectRef != "assistants" {
		t.Fatalf("post-sunset degradation finding = %+v", f)
	}
	if f.Severity != model.SeverityInfo {
		t.Fatalf("post-sunset severity = %q, want info", f.Severity)
	}
	if strings.Contains(f.Title, "permission or entitlement") {
		t.Fatalf("post-sunset title used unavailable posture: %q", f.Title)
	}
	if len(findingsByKind(fs, "deprecation_risk")) != 0 {
		t.Fatalf("post-sunset 410 emitted deprecation findings: %+v", fs)
	}
}

func TestGatherAssistants_PreSunset410DegradesUnavailable(t *testing.T) {
	doer := &asstDoer{t: t, statuses: map[string]int{"/v1/assistants": 410}}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants must not fail on pre-sunset 410; got %v", err)
	}

	fs := govFindings(sink.obs)
	if len(fs) != 1 {
		t.Fatalf("emitted %d findings, want 1", len(fs))
	}
	f := fs[0]
	if f.Kind != "posture" || f.SubjectKind != subjectSurface || f.SubjectRef != "assistants" {
		t.Fatalf("pre-sunset degradation finding = %+v", f)
	}
	if f.Severity != model.SeverityMedium {
		t.Fatalf("pre-sunset severity = %q, want medium", f.Severity)
	}
	if !strings.Contains(f.Title, "unavailable") {
		t.Fatalf("pre-sunset title = %q, want unavailable posture", f.Title)
	}
}

func findingsByKind(fs []model.FindingReport, kind string) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range fs {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func findingsBySubjectRef(fs []model.FindingReport, ref string) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range fs {
		if f.SubjectRef == ref {
			out = append(out, f)
		}
	}
	return out
}
