// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// directoryHandler answers the directory endpoints with fixtures (one SCIM group, one
// direct group; two orgs each with one role + two users).
func directoryHandler(t *testing.T) func(*http.Request) (int, string) {
	return func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET request %s — directory ingest must be read-only", req.Method)
		}
		if k := req.Header.Get("x-api-key"); k != "sk-ant-api01-cak" {
			t.Errorf("auth = %q, want the DISTINCT Compliance Access Key", k)
		}
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/settings"):
			// The effective-settings endpoint is enabled per parent org separately; this
			// directory fixture leaves it disabled, so gatherSettings degrades to a single
			// honest note (the full attestation path is exercised in settings_test.go).
			return http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error","message":"not found"}}`
		case strings.HasSuffix(p, "/roles"):
			return http.StatusOK, `{"data":[{"id":"rbac_role_1","name":"admin"}],"has_more":false}`
		case strings.HasSuffix(p, "/users"):
			return http.StatusOK, `{"data":[{"id":"u1"},{"id":"u2"}],"has_more":false}`
		case p == groupsPath:
			return http.StatusOK, `{"data":[{"id":"rbac_group_1","name":"eng","source_type":"scim"},{"id":"rbac_group_2","name":"adhoc","source_type":"direct"}],"has_more":false}`
		case p == orgsPath:
			return http.StatusOK, `{"data":[{"uuid":"org_a","name":"A"},{"uuid":"org_b","name":"B"}]}`
		case p == "/v1/compliance/apps/projects":
			return http.StatusOK, `{"data":[{"id":"proj_1","organization_uuid":"org_a","created_at":"2026-06-01T00:00:00Z"}],"has_more":false}`
		default:
			t.Fatalf("unexpected directory path %q", p)
			return http.StatusInternalServerError, ""
		}
	}
}

func newDirectorySource(t *testing.T, doer *routeDoer) *Source {
	t.Helper()
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"compliance_access_key": "sk-ant-api01-cak", "org_ref": "acme"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDirectory_DenyClosedWithoutKey proves no Compliance Access Key ⇒ no directory call
// and no emission (Gather only runs the activity feed if its own key is set).
func TestDirectory_DenyClosedWithoutKey(t *testing.T) {
	doer := &routeDoer{handler: func(*http.Request) (int, string) {
		t.Fatal("no compliance_access_key must make NO directory call")
		return 0, ""
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"org_ref": "acme"}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline (no keys) must emit nothing, got %d", len(sink.obs))
	}
}

// TestDirectory_EmitsTopologyRolesAndSCIMSignal proves the directory ingest emits the org
// topology, per-org role/user counts, and — the governance prize — the SCIM-provisioning
// signal that resolves the Admin-API blind spot. Read-only; uses the distinct CAK.
func TestDirectory_EmitsTopologyRolesAndSCIMSignal(t *testing.T) {
	doer := &routeDoer{handler: directoryHandler(t)}
	s := newDirectorySource(t, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	var topo, groups, orgs, settingsNote, contentInv int
	var scimSignal bool
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("emitted %T, want FindingReport", o)
		}
		// The effective-settings endpoint is 404 in this directory fixture, so
		// gatherSettings degrades to exactly one honest posture note (settings.go).
		if f.Kind == findingKindSettings && f.SubjectKind == subjectKindSettingsNote {
			settingsNote++
			if !strings.Contains(f.Title, "not enabled") {
				t.Errorf("settings note should explain the 404 (not enabled): %q", f.Title)
			}
			continue
		}
		if f.Kind != findingKindDirectory {
			t.Errorf("directory finding Kind = %q", f.Kind)
		}
		switch f.SubjectKind {
		case "claude_compliance_directory":
			topo++
		case "claude_compliance_groups":
			groups++
			if strings.Contains(f.Title, "SCIM provisioning ACTIVE") && strings.Contains(f.Title, "1 of 2") {
				scimSignal = true
			}
		case "claude_compliance_org":
			orgs++
			if !strings.Contains(f.Title, "1 role(s)") || !strings.Contains(f.Title, "2 user(s)") {
				t.Errorf("org finding title %q missing role/user counts", f.Title)
			}
		case "claude_compliance_content":
			contentInv++
		default:
			t.Errorf("unexpected directory subject %q", f.SubjectKind)
		}
		if f.DetailHash == "" {
			t.Error("directory finding must carry a DetailHash (minimal-data)")
		}
	}
	if topo != 1 {
		t.Errorf("want 1 topology finding, got %d", topo)
	}
	if groups != 1 || !scimSignal {
		t.Errorf("want 1 groups finding with the SCIM signal (scim=1 of 2), got groups=%d scimSignal=%v", groups, scimSignal)
	}
	if orgs != 2 {
		t.Errorf("want 2 per-org findings (org_a, org_b), got %d", orgs)
	}
	if settingsNote != 1 {
		t.Errorf("want exactly 1 settings-unavailable note (endpoint 404, emitted once), got %d", settingsNote)
	}
	if contentInv != 1 {
		t.Errorf("want 1 content inventory finding, got %d", contentInv)
	}
}
