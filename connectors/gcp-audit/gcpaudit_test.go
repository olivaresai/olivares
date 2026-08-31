// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gcpaudit "github.com/olivaresai/olivares/connectors/gcp-audit"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	signalGCP   = model.SignalSource("gcp")
	signalAudit = model.SignalSource("gcp_audit")
	testOrg     = "123456789012"
)

// capturingSink collects emitted observations (race-safe).
type capturingSink struct {
	mu       sync.Mutex
	edges    []model.EdgeObservation
	findings []model.FindingReport
}

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch o := obs.(type) {
	case model.EdgeObservation:
		c.edges = append(c.edges, o)
	case model.FindingReport:
		c.findings = append(c.findings, o)
	}
	return nil
}

func (c *capturingSink) edgeSnapshot() []model.EdgeObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.EdgeObservation(nil), c.edges...)
}

func (c *capturingSink) findingSnapshot() []model.FindingReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.FindingReport(nil), c.findings...)
}

// fixture is an httptest server standing in for Resource Manager, IAM and Cloud
// Logging. It records every request's method+path so a test can assert read-only
// verbs and that a Bearer token was presented.
type fixture struct {
	mu       sync.Mutex
	reqs     []string // "METHOD path"
	authSeen bool
	failPath string // when set, this path returns 500 (health-finding test)
	entries  string // entries:list response JSON body
}

func newFixture() *fixture {
	return &fixture{entries: defaultEntriesJSON}
}

func (f *fixture) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		f.authSeen = true
	}
}

func (f *fixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v3/folders", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.failPath == "/v3/folders" {
			w.WriteHeader(500)
			return
		}
		switch r.URL.Query().Get("parent") {
		case "organizations/" + testOrg:
			writeJSON(w, `{"folders":[{"name":"folders/111","parent":"organizations/`+testOrg+`","state":"ACTIVE","displayName":"eng"}]}`)
		default:
			writeJSON(w, `{"folders":[]}`)
		}
	})

	mux.HandleFunc("/v3/projects", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		switch r.URL.Query().Get("parent") {
		case "organizations/" + testOrg:
			writeJSON(w, `{"projects":[{"projectId":"proj-root","parent":"organizations/`+testOrg+`","state":"ACTIVE"}]}`)
		case "folders/111":
			writeJSON(w, `{"projects":[{"projectId":"proj-eng","parent":"folders/111","state":"ACTIVE"},{"projectId":"proj-gone","parent":"folders/111","state":"DELETE_REQUESTED"}]}`)
		default:
			writeJSON(w, `{"projects":[]}`)
		}
	})

	mux.HandleFunc("/v1/projects/proj-root/serviceAccounts", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSON(w, `{"accounts":[{"email":"sa-root@proj-root.iam.gserviceaccount.com","disabled":false}]}`)
	})
	mux.HandleFunc("/v1/projects/proj-eng/serviceAccounts", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSON(w, `{"accounts":[{"email":"sa-eng@proj-eng.iam.gserviceaccount.com","disabled":false}]}`)
	})

	mux.HandleFunc("/v2/entries:list", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.failPath == "/v2/entries:list" {
			w.WriteHeader(500)
			return
		}
		writeJSON(w, f.entries)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// defaultEntriesJSON is the entries:list fixture. It mixes Admin Activity (a
// write by definition), Data Access read/write/unknown-verb entries, a Policy
// Denied and a System Event entry (both must be skipped), and an entry with no
// principal (skipped). Each entry deliberately carries request/resourceName
// fields the connector must NOT read (leak test).
const defaultEntriesJSON = `{"entries":[
{"logName":"organizations/123456789012/logs/cloudaudit.googleapis.com%2Factivity","timestamp":"2026-06-12T09:00:01Z","protoPayload":{"serviceName":"iam.googleapis.com","methodName":"google.iam.admin.v1.CreateServiceAccount","resourceName":"projects/proj-root/serviceAccounts/secret-sa-xyz","authenticationInfo":{"principalEmail":"admin@example.com"},"request":{"account_id":"super-secret-99"},"requestMetadata":{"callerIp":"203.0.113.7"}}},
{"logName":"projects/proj-eng/logs/cloudaudit.googleapis.com%2Fdata_access","timestamp":"2026-06-12T09:00:02Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"v1.compute.instances.get","resourceName":"projects/proj-eng/zones/us/instances/db-prod-7","authenticationInfo":{"principalEmail":"sa-eng@proj-eng.iam.gserviceaccount.com"}}},
{"logName":"projects/proj-eng/logs/cloudaudit.googleapis.com%2Fdata_access","timestamp":"2026-06-12T09:00:03Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"v1.compute.instances.insert","resourceName":"projects/proj-eng/zones/us/instances/new-1","authenticationInfo":{"principalEmail":"deployer@example.com"}}},
{"logName":"projects/proj-eng/logs/cloudaudit.googleapis.com%2Fdata_access","timestamp":"2026-06-12T09:00:04Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"v1.compute.instances.simulateMaintenanceEvent","resourceName":"projects/proj-eng/zones/us/instances/x","authenticationInfo":{"principalEmail":"ops@example.com"}}},
{"logName":"organizations/123456789012/logs/cloudaudit.googleapis.com%2Fpolicy","timestamp":"2026-06-12T09:00:05Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"v1.compute.instances.delete","authenticationInfo":{"principalEmail":"intruder@evil.example"}}},
{"logName":"organizations/123456789012/logs/cloudaudit.googleapis.com%2Fsystem_event","timestamp":"2026-06-12T09:00:06Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"compute.instances.guestTerminate","authenticationInfo":{"principalEmail":"system@google.com"}}},
{"logName":"projects/proj-eng/logs/cloudaudit.googleapis.com%2Fdata_access","timestamp":"2026-06-12T09:00:07Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"v1.compute.instances.list","authenticationInfo":{"principalEmail":""}}}
]}`

func openSource(t *testing.T, base string, extra map[string]string) *gcpaudit.Source {
	t.Helper()
	settings := map[string]string{
		"access_token":     "test-token",
		"organization_id":  testOrg,
		"crm_endpoint":     base,
		"iam_endpoint":     base,
		"logging_endpoint": base,
	}
	for k, v := range extra {
		settings[k] = v
	}
	s := gcpaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *gcpaudit.Source) *capturingSink {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

type edgeKey struct {
	originKind, originRef, resKind, resRef string
	mode                                   model.AccessMode
	source                                 model.SignalSource
	conf                                   model.Confidence
	tool                                   string
}

func keyOf(e model.EdgeObservation) edgeKey {
	return edgeKey{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, e.Mode, e.Source, e.Confidence, e.ToolRef}
}

func TestGatherInventoryGolden(t *testing.T) {
	f := newFixture()
	srv := f.server(t)
	s := openSource(t, srv.URL, map[string]string{"enable_audit": "false"})
	sink := gather(t, s)

	want := []edgeKey{
		{"gcp.organization", "organizations/" + testOrg, "gcp.folder", "folders/111", model.ModeUnknown, signalGCP, model.ConfidenceAttributed, ""},
		{"gcp.folder", "folders/111", "gcp.project", "proj-eng", model.ModeUnknown, signalGCP, model.ConfidenceAttributed, ""},
		{"gcp.organization", "organizations/" + testOrg, "gcp.project", "proj-root", model.ModeUnknown, signalGCP, model.ConfidenceAttributed, ""},
		{"gcp.project", "proj-eng", "gcp.service_account", "sa-eng@proj-eng.iam.gserviceaccount.com", model.ModeUnknown, signalGCP, model.ConfidenceAttributed, ""},
		{"gcp.project", "proj-root", "gcp.service_account", "sa-root@proj-root.iam.gserviceaccount.com", model.ModeUnknown, signalGCP, model.ConfidenceAttributed, ""},
	}
	got := sink.edgeSnapshot()
	if len(got) != len(want) {
		t.Fatalf("got %d inventory edges, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if k := keyOf(got[i]); k != want[i] {
			t.Errorf("edge[%d]\n got=%+v\nwant=%+v", i, k, want[i])
		}
		if got[i].ObservedAt.IsZero() {
			t.Errorf("edge[%d] ObservedAt is zero (inventory edges carry the per-pass timestamp)", i)
		}
	}
	if !f.authSeen {
		t.Error("no Bearer token presented to the fixture")
	}
	// proj-gone (DELETE_REQUESTED) must not appear.
	for _, e := range got {
		if e.ResourceRef == "proj-gone" {
			t.Error("a DELETE_REQUESTED project was emitted")
		}
	}
}

func TestInventoryReadOnly(t *testing.T) {
	f := newFixture()
	srv := f.server(t)
	gather(t, openSource(t, srv.URL, map[string]string{"enable_audit": "false"}))
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("inventory issued a non-GET request: %q (must be read-only)", r)
		}
	}
}

func TestGatherAuditGolden(t *testing.T) {
	f := newFixture()
	srv := f.server(t)
	s := openSource(t, srv.URL, map[string]string{
		"enable_inventory": "false",
		"shared_accounts":  "sa-eng@proj-eng.iam.gserviceaccount.com",
		"lookback":         "720h", // wide window so the fixture timestamps fall inside.
	})
	sink := gather(t, s)

	want := []edgeKey{
		{"identity", "sa-eng@proj-eng.iam.gserviceaccount.com", "gcp.api", "compute.googleapis.com:v1.compute.instances.get", model.ModeRead, signalAudit, model.ConfidenceApproximate, "compute.googleapis.com"},
		{"identity", "deployer@example.com", "gcp.api", "compute.googleapis.com:v1.compute.instances.insert", model.ModeWrite, signalAudit, model.ConfidenceAttributed, "compute.googleapis.com"},
		{"identity", "ops@example.com", "gcp.api", "compute.googleapis.com:v1.compute.instances.simulateMaintenanceEvent", model.ModeUnknown, signalAudit, model.ConfidenceAttributed, "compute.googleapis.com"},
		{"identity", "admin@example.com", "gcp.api", "iam.googleapis.com:google.iam.admin.v1.CreateServiceAccount", model.ModeWrite, signalAudit, model.ConfidenceAttributed, "iam.googleapis.com"},
	}
	got := sink.edgeSnapshot()
	if len(got) != len(want) {
		t.Fatalf("got %d audit edges, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if k := keyOf(got[i]); k != want[i] {
			t.Errorf("edge[%d]\n got=%+v\nwant=%+v", i, k, want[i])
		}
	}
	// The Admin Activity edge must carry the entry's own timestamp, not now().
	for _, e := range got {
		if strings.Contains(e.ResourceRef, "CreateServiceAccount") {
			wantTS, _ := time.Parse(time.RFC3339, "2026-06-12T09:00:01Z")
			if !e.ObservedAt.Equal(wantTS) {
				t.Errorf("admin-activity ObservedAt = %v, want %v", e.ObservedAt, wantTS)
			}
		}
	}
}

// TestNoRawLeak proves no request/resourceName/payload fragment from the audit
// fixture reaches an emitted edge field (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawLeak(t *testing.T) {
	f := newFixture()
	srv := f.server(t)
	sink := gather(t, openSource(t, srv.URL, map[string]string{"enable_inventory": "false", "lookback": "720h"}))
	forbidden := []string{
		"secret-sa-xyz", "super-secret-99", "203.0.113.7", "db-prod-7",
		"zones/us/instances", "callerIp", "requestMetadata", "intruder@evil.example",
		"system@google.com", // system_event principal must never be emitted
	}
	for _, e := range sink.edgeSnapshot() {
		fields := []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef}
		for _, fld := range fields {
			for _, frag := range forbidden {
				if strings.Contains(fld, frag) {
					t.Errorf("edge field %q leaked forbidden fragment %q", fld, frag)
				}
			}
		}
	}
}

// TestSkippedCategories proves policy-denied, system-event and no-principal
// entries are not emitted.
func TestSkippedCategories(t *testing.T) {
	f := newFixture()
	srv := f.server(t)
	sink := gather(t, openSource(t, srv.URL, map[string]string{"enable_inventory": "false", "lookback": "720h"}))
	if n := len(sink.edgeSnapshot()); n != 4 {
		t.Fatalf("got %d audit edges, want 4 (policy/system/no-principal skipped)", n)
	}
}

func TestOfflineNoOp(t *testing.T) {
	// No credential of any kind ⇒ Open succeeds, Gather emits nothing.
	s := gcpaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"organization_id": testOrg}}); err != nil {
		t.Fatalf("offline Open should succeed: %v", err)
	}
	sink := gather(t, s)
	if n := len(sink.edgeSnapshot()) + len(sink.findingSnapshot()); n != 0 {
		t.Errorf("offline Gather emitted %d observations, want 0", n)
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	// Credential present but no scope ⇒ error.
	err := gcpaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"access_token": "t"}})
	if err == nil {
		t.Error("a credential with no org/projects scope should error")
	}
	// Malformed inline credentials ⇒ error.
	err = gcpaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"credentials_json": "{not json", "organization_id": testOrg}})
	if err == nil {
		t.Error("malformed credentials_json should error")
	}
	// Scope present with projects only (no org) and a token ⇒ ok.
	if err := gcpaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"access_token": "t", "projects": "p1,p2"}}); err != nil {
		t.Errorf("project-scoped config should open: %v", err)
	}
}

func TestHealthFindingOnFailure(t *testing.T) {
	f := newFixture()
	f.failPath = "/v2/entries:list"
	srv := f.server(t)
	// Inventory succeeds; audit fails ⇒ exactly one health finding, inventory edges still emitted.
	sink := gather(t, openSource(t, srv.URL, nil))
	findings := sink.findingSnapshot()
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}
	if findings[0].Kind != "health" || findings[0].SubjectKind != "gcp.audit" {
		t.Errorf("finding = %+v, want health/gcp.audit", findings[0])
	}
	if findings[0].DetailHash == "" || strings.Contains(findings[0].Title, "500") {
		t.Errorf("finding must hash the detail, not embed it: %+v", findings[0])
	}
	if len(sink.edgeSnapshot()) == 0 {
		t.Error("inventory edges should still be emitted when only audit fails")
	}
}

// TestAuditTruncationFinding proves that when the audit feed stops at max_pages
// with a nextPageToken still pending, the connector emits a low-severity coverage
// finding (honest partial coverage, never a silent cap) AND still emits the edges
// it did collect.
func TestAuditTruncationFinding(t *testing.T) {
	f := newFixture()
	// One entry + a nextPageToken so page 1 (the only allowed page) leaves more data.
	f.entries = `{"nextPageToken":"more","entries":[{"logName":"organizations/123456789012/logs/cloudaudit.googleapis.com%2Factivity","timestamp":"2026-06-12T09:00:01Z","protoPayload":{"serviceName":"iam.googleapis.com","methodName":"google.iam.admin.v1.CreateServiceAccount","authenticationInfo":{"principalEmail":"admin@example.com"}}}]}`
	srv := f.server(t)
	sink := gather(t, openSource(t, srv.URL, map[string]string{
		"enable_inventory": "false", "lookback": "720h", "max_pages": "1",
	}))
	if n := len(sink.edgeSnapshot()); n != 1 {
		t.Fatalf("got %d edges, want 1 (the collected entry)", n)
	}
	findings := sink.findingSnapshot()
	if len(findings) != 1 || findings[0].SubjectKind != "gcp.audit" || findings[0].Severity != model.SeverityLow {
		t.Fatalf("want one low-severity gcp.audit coverage finding, got %+v", findings)
	}
	if !strings.Contains(findings[0].Title, "max_pages") {
		t.Errorf("coverage finding should name max_pages: %q", findings[0].Title)
	}
}

// TestNonAuditLogNameNotForcedWrite proves the categoryFromLogName guard: a
// logName that is not a Cloud Audit Log (ends in "/activity" but lacks the
// cloudaudit.googleapis.com marker) is NOT forced to ModeWrite — it falls to
// verb classification, so a "get" stays read.
func TestNonAuditLogNameNotForcedWrite(t *testing.T) {
	f := newFixture()
	f.entries = `{"entries":[{"logName":"projects/p/logs/activity","timestamp":"2026-06-12T09:00:01Z","protoPayload":{"serviceName":"compute.googleapis.com","methodName":"v1.compute.instances.get","authenticationInfo":{"principalEmail":"x@example.com"}}}]}`
	srv := f.server(t)
	got := gather(t, openSource(t, srv.URL, map[string]string{"enable_inventory": "false", "lookback": "720h"})).edgeSnapshot()
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].Mode != model.ModeRead {
		t.Errorf("non-audit logName with a get verb = %q, want read (not forced write by category)", got[0].Mode)
	}
}

func TestDescriptor(t *testing.T) {
	d := gcpaudit.New().Descriptor()
	if d.Name != "olivares.gcp-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
	// Secret fields must be declared Secret.
	for _, fld := range d.ConfigFields {
		if (fld.Key == "credentials_json" || fld.Key == "access_token") && !fld.Secret {
			t.Errorf("config field %q must be Secret", fld.Key)
		}
	}
}
