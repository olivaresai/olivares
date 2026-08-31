// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func fixedClock() time.Time { return time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC) }

// routeDoer answers GETs from a handler keyed by request, recording every request so a
// test can assert the connector is GET-only and paginates with after_id.
type routeDoer struct {
	reqs    []*http.Request
	handler func(*http.Request) (int, string)
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st, body := d.handler(req)
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

// page1 carries the secret PII the connector must NEVER surface in a finding, plus a
// has_more=true cursor to exercise pagination; page2 closes the feed.
const (
	secretIP    = "203.0.113.77"
	secretEmail = "alice@corp.example"
	secretUA    = "Mozilla/5.0 (SecretAgent)"
)

func page1() string {
	return `{"data":[
		{"id":"act_1","created_at":"2026-06-04T10:00:00Z","organization_id":"org_x","type":"claude_chat_created",
		 "actor":{"type":"user_actor","ip_address":"` + secretIP + `","user_agent":"` + secretUA + `","email_address":"` + secretEmail + `","user_id":"u_1"},
		 "claude_chat_id":"chat_1","claude_project_id":"proj_1"}
	],"has_more":true,"first_id":"act_1","last_id":"act_1"}`
}

func page2() string {
	return `{"data":[
		{"id":"act_2","created_at":"2026-06-04T11:00:00Z","organization_id":"org_x","type":"user_signed_in",
		 "actor":{"type":"user_actor","ip_address":"198.51.100.9","user_agent":"curl/8","email_address":"bob@corp.example","user_id":"u_2"}}
	],"has_more":false,"first_id":"act_2","last_id":"act_2"}`
}

// TestGatherEmitsActivityEvidence verifies CLA-06: the connector paginates the Activity
// Feed (after_id cursor), emits one external_activity FindingReport per record at Info
// severity, references the activity id, and — minimal-data — leaks NO actor ip/email/UA.
func TestGatherEmitsActivityEvidence(t *testing.T) {
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET request %s — connector must be read-only", req.Method)
		}
		if req.URL.Query().Get("after_id") == "act_1" {
			return http.StatusOK, page2()
		}
		return http.StatusOK, page1()
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"api_key": "sk-ant-admin01-test", "org_ref": "acme"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	// The connector emits posture findings (documented gaps + the CAK-absent honest
	// degradation note in feed-only mode) plus one activity-evidence finding per record.
	var activities []model.FindingReport
	var posture []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("emitted %T, want FindingReport", o)
		}
		switch f.Kind {
		case findingKindActivity:
			activities = append(activities, f)
		case findingKindCoverage:
			posture = append(posture, f)
		default:
			t.Errorf("unexpected finding Kind %q", f.Kind)
		}
	}
	if len(activities) != 2 {
		t.Fatalf("emitted %d activity findings, want 2 (one per activity across two pages)", len(activities))
	}
	// Feed-only mode (api_key, no CAK): the coverage-gaps finding + the CAK-absent
	// honest-degradation finding = 2 posture findings.
	if len(posture) != 2 {
		t.Fatalf("emitted %d posture findings, want 2 (gaps + CAK-absent note in feed-only mode)", len(posture))
	}
	for _, f := range activities {
		if f.Severity != model.SeverityInfo {
			t.Errorf("Severity = %q, want info (evidence, not an alert)", f.Severity)
		}
		if f.SubjectKind != "claude_activity" || f.SubjectRef == "" {
			t.Errorf("subject = %s/%s", f.SubjectKind, f.SubjectRef)
		}
		if f.DetailHash == "" {
			t.Error("missing DetailHash — evidence must be a hash reference")
		}
	}

	// Minimal-data: the raw actor PII must appear NOWHERE in the emitted findings.
	for _, o := range sink.obs {
		f := o.(model.FindingReport)
		blob := f.Title + "|" + f.SubjectRef + "|" + f.DetailHash + "|" + f.Kind
		for _, secret := range []string{secretIP, secretEmail, secretUA} {
			if strings.Contains(blob, secret) {
				t.Fatalf("raw actor PII %q leaked into a finding — minimal-data violated", secret)
			}
		}
	}

	// The first activity's id and type are the non-sensitive handle + title, and the
	// title carries the classified category ([chat] for claude_chat_created).
	first := activities[0]
	if first.SubjectRef != "act_1" || !strings.Contains(first.Title, "claude_chat_created") {
		t.Errorf("first finding = ref %q title %q", first.SubjectRef, first.Title)
	}
	if !strings.Contains(first.Title, "[chat]") {
		t.Errorf("first finding title must carry the [chat] category: %q", first.Title)
	}
}

// TestActivityFindingHashesNewActorFields proves newly documented actor identifiers are
// included only in the DetailHash preimage, never surfaced in cleartext.
func TestActivityFindingHashesNewActorFields(t *testing.T) {
	s := New()
	s.now = fixedClock
	base := activity{
		ID:             "act_actor",
		CreatedAt:      "2026-07-03T10:00:00Z",
		OrganizationID: "org_x",
		Type:           "compliance_api_accessed",
		Actor: actor{
			Type: "scim_directory_sync_actor",
		},
	}
	baseFinding := s.activityFinding(base)
	cases := []struct {
		name  string
		value string
		mut   func(*activity, string)
	}{
		{"admin api key", "admin_key_secret", func(a *activity, v string) { a.Actor.AdminAPIKeyID = v }},
		{"unauthenticated email", "anon@corp.example", func(a *activity, v string) { a.Actor.UnauthenticatedEmailAddress = v }},
		{"workos event", "workos_evt_secret", func(a *activity, v string) { a.Actor.WorkOSEventID = v }},
		{"directory id", "directory_secret", func(a *activity, v string) { a.Actor.DirectoryID = v }},
		{"idp connection type", "OktaSCIMV2", func(a *activity, v string) { a.Actor.IDPConnectionType = v }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := base
			c.mut(&a, c.value)
			f := s.activityFinding(a)
			if f.DetailHash == "" || f.DetailHash == baseFinding.DetailHash {
				t.Fatalf("%s must affect DetailHash", c.name)
			}
			blob := f.Title + "|" + f.SubjectRef + "|" + f.DetailHash
			if strings.Contains(blob, c.value) {
				t.Fatalf("%s leaked in cleartext: %q", c.name, blob)
			}
		})
	}
}

// TestOfflineEmitsNothing verifies the honest-absence posture: with no key the
// connector emits no activity evidence (so the external_activity capability stays an
// honest gap), and never performs network I/O.
func TestOfflineEmitsNothing(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{}); err != nil { // no api_key => offline
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline connector emitted %d findings, want 0 (honest absence)", len(sink.obs))
	}
}

// TestFeedOnlyModeDeclaresCAKAbsence proves the headline honesty contract: when the
// connector runs with ONLY the Admin API key (no Compliance Access Key), it emits
// activity evidence from the feed AND an explicit posture finding declaring that
// directory, effective-settings, content enumeration, and RTBF erase are UNAVAILABLE —
// never silent omission, never fabricated content. This is the "feed-only" mode.
func TestFeedOnlyModeDeclaresCAKAbsence(t *testing.T) {
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		return http.StatusOK, `{"data":[],"has_more":false}`
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{
		"api_key": "sk-ant-admin01-test",
		"org_ref": "acme",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	var cakAbsent *model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		if f.SubjectKind == "claude_compliance" && strings.Contains(f.Title, "Compliance Access Key not configured") {
			cakAbsent = &f
		}
	}
	if cakAbsent == nil {
		t.Fatal("feed-only mode must emit a posture finding declaring CAK absence — got none")
	}
	low := strings.ToLower(cakAbsent.Title)
	for _, want := range []string{"directory", "settings", "content", "unavailable"} {
		if !strings.Contains(low, want) {
			t.Errorf("CAK-absent finding must mention %q: %q", want, cakAbsent.Title)
		}
	}
	if cakAbsent.Severity != model.SeverityLow {
		t.Errorf("CAK-absent finding severity = %q, want low (a posture gap, not a critical alert)", cakAbsent.Severity)
	}
	if cakAbsent.DetailHash == "" {
		t.Error("CAK-absent finding must carry a DetailHash (tamper-evidence)")
	}
}

// TestThreeKeyModes exercises the three honest states the connector exposes:
//   - No keys at all → fully silent (honest absence, no fabrication)
//   - Admin API key only → activity evidence + explicit CAK-absent posture gap
//   - Both keys → activity evidence + directory/settings/content evidence, NO CAK-absent finding
func TestThreeKeyModes(t *testing.T) {
	handler := func(req *http.Request) (int, string) {
		p := req.URL.Path
		switch {
		case p == activitiesPath:
			return http.StatusOK, `{"data":[],"has_more":false}`
		case p == "/v1/compliance/organizations":
			return http.StatusOK, `{"data":[]}`
		case p == "/v1/compliance/groups":
			return http.StatusOK, `{"data":[],"has_more":false}`
		default:
			return http.StatusOK, `{"data":[],"has_more":false}`
		}
	}
	modes := []struct {
		name          string
		apiKey        string
		cak           string
		wantFindings  bool
		wantCAKAbsent bool
		wantDirectory bool
	}{
		{"no-keys", "", "", false, false, false},
		{"feed-only", "sk-ant-admin01-test", "", true, true, false},
		{"both-keys", "sk-ant-admin01-test", "sk-ant-api01-cak", true, false, true},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			doer := &routeDoer{handler: handler}
			s := New()
			s.doer = doer
			s.now = fixedClock
			cfg := sdk.Config{Settings: map[string]string{
				"api_key":               m.apiKey,
				"compliance_access_key": m.cak,
				"org_ref":               "acme",
			}}
			if err := s.Open(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			sink := &captureSink{}
			if err := s.Gather(context.Background(), sink); err != nil {
				t.Fatal(err)
			}

			hasFindings := len(sink.obs) > 0
			if hasFindings != m.wantFindings {
				t.Errorf("findings emitted = %v, want %v (got %d)", hasFindings, m.wantFindings, len(sink.obs))
			}

			var hasCAKAbsent, hasDirectory bool
			for _, o := range sink.obs {
				f, ok := o.(model.FindingReport)
				if !ok {
					continue
				}
				if strings.Contains(f.Title, "Compliance Access Key not configured") {
					hasCAKAbsent = true
				}
				if f.Kind == findingKindDirectory {
					hasDirectory = true
				}
			}
			if hasCAKAbsent != m.wantCAKAbsent {
				t.Errorf("CAK-absent finding = %v, want %v", hasCAKAbsent, m.wantCAKAbsent)
			}
			if hasDirectory != m.wantDirectory {
				t.Errorf("directory finding = %v, want %v", hasDirectory, m.wantDirectory)
			}
		})
	}
}

// TestDescriptorDeclaresReadOnlyAndDeletePosture pins the honesty posture in the
// connector's self-description: it is read-only and content DELETE is out of scope.
func TestDescriptorDeclaresReadOnlyAndDeletePosture(t *testing.T) {
	d := New().Descriptor()
	low := strings.ToLower(d.Description)
	if !strings.Contains(low, "read-only") {
		t.Error("descriptor must state read-only")
	}
	if !strings.Contains(low, "delete") || !strings.Contains(low, "out of scope") {
		t.Errorf("descriptor must state content DELETE is out of scope (HITL-gated): %q", d.Description)
	}
}

// TestDescriptorDocumentsLimits pins the 6-year retention + 600 req/min documentation
// in the Descriptor — a buyer must see these limits before configuring.
func TestDescriptorDocumentsLimits(t *testing.T) {
	d := New().Descriptor()
	low := strings.ToLower(d.Description)
	if !strings.Contains(low, "6 years") {
		t.Error("descriptor must document the 6-year activity retention window")
	}
	if strings.Contains(low, "retains activity records for 180 days") {
		t.Error("descriptor must not document the retired 180-day activity retention window")
	}
	if !strings.Contains(low, "600") {
		t.Error("descriptor must document the 600 req/min rate limit")
	}
	if !strings.Contains(low, "two-key") || !strings.Contains(low, "honest degradation") {
		t.Error("descriptor must document the two-key model with honest degradation")
	}
}
