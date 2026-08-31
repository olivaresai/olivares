// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// ---------- Invites ----------

func TestGatherInvites_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherInvites(context.Background(), sink); err != nil {
		t.Fatalf("gatherInvites: %v", err)
	}
	fs := govFindings(sink.obs)
	// 2 invite findings + 1 summary finding = 3.
	if len(fs) != 3 {
		t.Fatalf("emitted %d findings, want 3", len(fs))
	}
	// First invite: pending.
	if fs[0].SubjectKind != subjectInvite || fs[0].SubjectRef != "inv_abc123" {
		t.Fatalf("first invite = %+v", fs[0])
	}
	if fs[0].Severity != model.SeverityInfo {
		t.Fatalf("pending invite severity = %q, want info", fs[0].Severity)
	}
	// Second invite: expired → Low.
	if fs[1].Severity != model.SeverityLow {
		t.Fatalf("expired invite severity = %q, want low", fs[1].Severity)
	}
	// Summary finding.
	summary := fs[2]
	if summary.Kind != "posture" || summary.SubjectRef != "organization" {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(summary.Title, "1 pending") || !strings.Contains(summary.Title, "1 expired") {
		t.Fatalf("summary title = %q", summary.Title)
	}
	// Minimal-data: email must NOT appear in any title.
	for _, f := range fs {
		if strings.Contains(f.Title, "@") {
			t.Fatalf("invite title leaked email: %q", f.Title)
		}
	}
}

func TestGatherInvites_403Degrades(t *testing.T) {
	doer := &asstDoer{t: t, unavailable: map[string]bool{"/v1/organization/invites": true}}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherInvites(context.Background(), sink); err != nil {
		t.Fatalf("gatherInvites must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable finding, got %+v", fs)
	}
}

// ---------- Project Users ----------

func TestGatherProjectUsers_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	// Use proj_default (the non-archived project from the fixture).
	if err := s.gatherProjectUsers(context.Background(), sink, "proj_default", "Default Project"); err != nil {
		t.Fatalf("gatherProjectUsers: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 {
		t.Fatalf("emitted %d findings, want 1 (summary)", len(fs))
	}
	f := fs[0]
	if f.SubjectKind != subjectProjectUser || f.SubjectRef != "proj_default" {
		t.Fatalf("project users finding = %+v", f)
	}
	if !strings.Contains(f.Title, "2 user(s)") {
		t.Fatalf("title missing user count: %q", f.Title)
	}
	if !strings.Contains(f.Title, "Default Project") {
		t.Fatalf("title missing project name: %q", f.Title)
	}
	// Minimal-data: no individual emails.
	if strings.Contains(f.Title, "@") {
		t.Fatalf("title leaked PII: %q", f.Title)
	}
}

// ---------- Project Service Accounts ----------

func TestGatherProjectServiceAccounts_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherProjectServiceAccounts(context.Background(), sink, "proj_default", "Default Project"); err != nil {
		t.Fatalf("gatherProjectServiceAccounts: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 {
		t.Fatalf("emitted %d findings, want 1", len(fs))
	}
	if fs[0].SubjectKind != subjectServiceAccount {
		t.Fatalf("subject kind = %q", fs[0].SubjectKind)
	}
	if !strings.Contains(fs[0].Title, "1 service account") {
		t.Fatalf("title = %q", fs[0].Title)
	}
}

// ---------- Project API Keys ----------

func TestGatherProjectAPIKeys_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherProjectAPIKeys(context.Background(), sink, "proj_default", "Default Project"); err != nil {
		t.Fatalf("gatherProjectAPIKeys: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 {
		t.Fatalf("emitted %d findings, want 1", len(fs))
	}
	f := fs[0]
	if f.SubjectKind != subjectProjectAPIKey || f.SubjectRef != "proj_default" {
		t.Fatalf("project keys finding = %+v", f)
	}
	if !strings.Contains(f.Title, "2 API key(s)") {
		t.Fatalf("title = %q", f.Title)
	}
	// key_proj2 has created_at 1704067200 (2024-01-01) — more than 90 days before
	// fixedClock (2026-06-02). So 1 stale key.
	if f.Severity != model.SeverityLow {
		t.Fatalf("stale-key severity = %q, want low", f.Severity)
	}
	if !strings.Contains(f.Title, "older than 90d") {
		t.Fatalf("title missing stale count: %q", f.Title)
	}
}

// ---------- Project Admin integration ----------

func TestGatherProjectAdmin_SkipsArchived(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherProjectAdmin(context.Background(), sink); err != nil {
		t.Fatalf("gatherProjectAdmin: %v", err)
	}
	// The projects fixture has 2 projects: proj_default (active) and proj_old (archived).
	// Only proj_default should be processed. We should get:
	// - 1 project users summary
	// - 1 service accounts finding
	// - 1 project API keys finding
	fs := govFindings(sink.obs)
	if len(fs) != 3 {
		t.Fatalf("emitted %d findings, want 3 (from the one active project)", len(fs))
	}
	// Verify no finding references the archived project.
	for _, f := range fs {
		if f.SubjectRef == "proj_old" {
			t.Fatalf("archived project found in findings: %+v", f)
		}
	}
}

// ---------- Read-only constraint ----------

func TestAdmin_AllGETRequests(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherInvites(context.Background(), sink); err != nil {
		t.Fatalf("gatherInvites: %v", err)
	}
	if err := s.gatherProjectAdmin(context.Background(), sink); err != nil {
		t.Fatalf("gatherProjectAdmin: %v", err)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
	}
}
