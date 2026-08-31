// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests in this file cover Mistral Admin API beta wire currency verified against the
// generated docs.mistral.ai reference on 2026-07-04, including the documented auth
// discrepancy and Enterprise-gated audit-log shape.
package mistral

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// adminFixtureDoer multiplexes requests by path to admin API fixtures. It records
// every request so a test can assert read-only behavior, and can be told to return
// 403/404 for given paths (degrade tests).
type adminFixtureDoer struct {
	t            *testing.T
	reqs         []*http.Request
	unavailable  map[string]bool   // path -> return 403/404
	bodyOverride map[string]string // path -> return this 200 body instead of the fixture
}

func (d *adminFixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if d.unavailable[req.URL.Path] {
		return resp(403, `{"error":"forbidden"}`), nil
	}
	if body, ok := d.bodyOverride[req.URL.Path]; ok {
		return resp(200, body), nil
	}
	var file string
	switch req.URL.Path {
	case defaultModelsND:
		file = "models.json"
	case defaultWorkspacesPath:
		file = "workspaces.json"
	case defaultKeysPath:
		file = "api_keys.json"
	case adminAuditLogsPath:
		file = "admin_audit_logs.json"
	case adminUsagePath:
		file = "admin_usage.json"
	case adminUsersPath:
		file = "admin_users.json"
	case adminWorkspacesPath:
		file = "admin_workspaces.json"
	case adminAPIKeysPath:
		file = "admin_api_keys.json"
	case adminSpendLimitPath:
		file = "admin_spend_limit.json"
	case adminRateLimitPath:
		file = "admin_rate_limit.json"
	default:
		d.t.Fatalf("unexpected request path %q", req.URL.Path)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

func newAdminSource(t *testing.T, doer modelprovider.Doer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{
		"api_key":       "sk-mistral-test",
		"admin_api_key": "sk-admin-test",
	}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGatherAuditLogs_Success(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 2 {
		t.Fatalf("want 2 audit findings, got %d: %+v", len(fs), fs)
	}
	// First entry: api_key.created
	if fs[0].Kind != "external_activity" || fs[0].SubjectKind != subjectAuditLog {
		t.Fatalf("finding[0] kind/subject = %q/%q", fs[0].Kind, fs[0].SubjectKind)
	}
	if fs[0].SubjectRef != "log_001" {
		t.Fatalf("finding[0] ref = %q, want log_001", fs[0].SubjectRef)
	}
	if !strings.Contains(fs[0].Title, "api_key.created") {
		t.Fatalf("finding[0] title = %q, want to contain api_key.created", fs[0].Title)
	}
	if fs[0].Severity != model.SeverityInfo {
		t.Fatalf("finding[0] severity = %q, want info", fs[0].Severity)
	}
	// Actor metadata must NOT be in the title (only in DetailHash).
	if strings.Contains(fs[0].Title, "user@example.com") {
		t.Fatal("finding[0] title leaked actor email")
	}
	// Second entry: workspace.updated
	if fs[1].SubjectRef != "log_002" || !strings.Contains(fs[1].Title, "workspace.updated") {
		t.Fatalf("finding[1] = %+v", fs[1])
	}
}

func TestGatherAuditLogs_LiveV2EndToEnd(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "admin_audit_logs_v2.json"))
	if err != nil {
		t.Fatalf("read admin audit v2 fixture: %v", err)
	}
	doer := &adminFixtureDoer{t: t, bodyOverride: map[string]string{adminAuditLogsPath: string(body)}}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var audits []model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectAuditLog {
			audits = append(audits, f)
		}
	}
	if len(audits) != 2 {
		t.Fatalf("live audit findings = %d, want 2 (%+v)", len(audits), sink.findings())
	}
	if audits[0].SubjectRef != "101" || !strings.Contains(audits[0].Title, "user.create") {
		t.Fatalf("first live audit = %+v", audits[0])
	}
	if audits[1].SubjectRef != "102" || !strings.Contains(audits[1].Title, "workspace.update") {
		t.Fatalf("second live audit = %+v", audits[1])
	}
	for _, f := range audits {
		for _, pii := range []string{"admin@example.com", "new.user@example.com", "owner@example.com", "usr_admin", "usr_new"} {
			if strings.Contains(f.Title, pii) {
				t.Fatalf("live audit title leaked metadata %q: %q", pii, f.Title)
			}
		}
		if len(f.DetailHash) != 64 {
			t.Fatalf("live audit DetailHash not sha-256 hex: %q", f.DetailHash)
		}
	}
}

func TestGatherAuditLogs_403Degrades(t *testing.T) {
	doer := &adminFixtureDoer{t: t, unavailable: map[string]bool{adminAuditLogsPath: true}}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs must NOT fail on 403; got %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("want 1 unavailable finding, got %d: %+v", len(fs), fs)
	}
	if fs[0].SubjectKind != subjectAdminCoverage || fs[0].Severity != model.SeverityMedium {
		t.Fatalf("unavailable finding = %+v", fs[0])
	}
	if !strings.Contains(fs[0].Title, "unavailable") || !strings.Contains(fs[0].Title, "audit logs") {
		t.Fatalf("unavailable title = %q", fs[0].Title)
	}
}

func TestGatherAdminUsage_Success(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAdminUsage(context.Background(), sink); err != nil {
		t.Fatalf("gatherAdminUsage: %v", err)
	}
	costs := sink.costs()
	if len(costs) != 2 {
		t.Fatalf("want 2 cost samples, got %d", len(costs))
	}
	// First model: mistral-large-latest, $0.55 → 550000 micro-USD.
	if costs[0].ModelRef != "mistral-large-latest" {
		t.Fatalf("cost[0] model = %q", costs[0].ModelRef)
	}
	if costs[0].CostMicroUSD != 550_000 {
		t.Fatalf("cost[0] = %d micro-USD, want 550000", costs[0].CostMicroUSD)
	}
	if costs[0].Provenance != model.ProvenanceBilled {
		t.Fatalf("cost[0] provenance = %q, want billed (currency present)", costs[0].Provenance)
	}
	if costs[0].InputTokens != 500_000 || costs[0].OutputTokens != 200_000 {
		t.Fatalf("cost[0] tokens = in %d out %d", costs[0].InputTokens, costs[0].OutputTokens)
	}
	if costs[0].ProviderRef != modelprovider.ProviderMistral {
		t.Fatalf("cost[0] provider = %q", costs[0].ProviderRef)
	}
	// Second model: mistral-small-latest, $0.19 → 190000 micro-USD.
	if costs[1].CostMicroUSD != 190_000 {
		t.Fatalf("cost[1] = %d micro-USD, want 190000", costs[1].CostMicroUSD)
	}

	// Verify query parameters included month and year.
	var usageReqs []*http.Request
	for _, r := range doer.reqs {
		if r.URL.Path == adminUsagePath {
			usageReqs = append(usageReqs, r)
		}
	}
	if len(usageReqs) != 1 {
		t.Fatalf("usage requests = %d, want 1", len(usageReqs))
	}
	q := usageReqs[0].URL.Query()
	if q.Get("month") != "6" || q.Get("year") != "2026" {
		t.Fatalf("usage query = month=%q year=%q, want 6/2026", q.Get("month"), q.Get("year"))
	}
}

func TestGatherSpendPosture_Success(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherSpendPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSpendPosture: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 2 {
		t.Fatalf("want 2 posture findings (spend + rate), got %d: %+v", len(fs), fs)
	}
	// Spend limit finding.
	var spend, rate *model.FindingReport
	for i := range fs {
		if fs[i].SubjectRef == "spend_limit" {
			spend = &fs[i]
		}
		if fs[i].SubjectRef == "rate_limit" {
			rate = &fs[i]
		}
	}
	if spend == nil {
		t.Fatal("missing spend_limit finding")
	}
	if !strings.Contains(spend.Title, "1000.00") || !strings.Contains(spend.Title, "USD") {
		t.Fatalf("spend title = %q", spend.Title)
	}
	if spend.Severity != model.SeverityInfo || spend.SubjectKind != subjectAdminPosture {
		t.Fatalf("spend finding = %+v", spend)
	}
	// Rate limit finding.
	if rate == nil {
		t.Fatal("missing rate_limit finding")
	}
	if !strings.Contains(rate.Title, "60 req/min") || !strings.Contains(rate.Title, "100000 tok/min") {
		t.Fatalf("rate title = %q", rate.Title)
	}
}

func TestGatherAdmin_NoAdminKey(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	// Only api_key, no admin_api_key.
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "sk-mistral-test"}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.adminClient != nil {
		t.Fatal("adminClient must be nil when admin_api_key is empty")
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Only the coverage caveat; no admin surfaces.
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectCoverage {
		t.Fatalf("want only coverage caveat, got %+v", fs)
	}
	// No admin API requests issued.
	for _, r := range doer.reqs {
		if strings.HasPrefix(r.URL.Path, "/api/admin/") {
			t.Fatalf("admin request issued without admin key: %s", r.URL.Path)
		}
	}
}

func TestSnapshot_WithAdminKey(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)

	// First gather to populate the admin inventory cache.
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Now snapshot should include admin workspaces and keys.
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2 (from admin API)", len(cat.Workspaces))
	}
	if cat.Workspaces[0].ID != "ws_prod" || cat.Workspaces[0].Name != "Production" {
		t.Fatalf("workspace[0] = %+v", cat.Workspaces[0])
	}
	if cat.Workspaces[1].ID != "ws_staging" || cat.Workspaces[1].Name != "Staging" {
		t.Fatalf("workspace[1] = %+v", cat.Workspaces[1])
	}
	if len(cat.Keys) != 2 {
		t.Fatalf("keys = %d, want 2 (from admin API)", len(cat.Keys))
	}
	if cat.Keys[0].ID != "akey_01" || cat.Keys[0].Name != "production-service" {
		t.Fatalf("key[0] = %+v", cat.Keys[0])
	}
	if cat.Keys[0].WorkspaceRef != "ws_prod" {
		t.Fatalf("key[0] workspace = %q, want ws_prod", cat.Keys[0].WorkspaceRef)
	}
	if cat.Keys[0].CreatedAt.IsZero() {
		t.Fatal("key[0] created_at must be parsed (not zero)")
	}
	// Models should still be populated from the live catalog.
	if len(cat.Models) == 0 {
		t.Fatal("models must not be empty")
	}
}

func TestGatherAdminUsers_Success(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAdminUsers(context.Background(), sink); err != nil {
		t.Fatalf("gatherAdminUsers: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("want 1 user inventory finding, got %d: %+v", len(fs), fs)
	}
	if fs[0].SubjectKind != subjectAdminUser || fs[0].Kind != "inventory" {
		t.Fatalf("user finding = %+v", fs[0])
	}
	if !strings.Contains(fs[0].Title, "3 users") {
		t.Fatalf("user finding title = %q, want to contain '3 users'", fs[0].Title)
	}
	// Emails must NOT be in the title.
	if strings.Contains(fs[0].Title, "alice@") || strings.Contains(fs[0].Title, "bob@") {
		t.Fatal("user finding title leaked email")
	}
}

func TestGatherAdmin_ReadOnlyBearerAuth(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
		// Admin paths use the admin key; non-admin paths use the regular key.
		if strings.HasPrefix(r.URL.Path, "/api/admin/") {
			if r.Header.Get("Authorization") != "Bearer sk-admin-test" {
				t.Fatalf("admin path %s used wrong credential: %q", r.URL.Path, r.Header.Get("Authorization"))
			}
			if r.Header.Get("x-api-key") != "sk-admin-test" {
				t.Fatalf("admin path %s missing x-api-key credential: %q", r.URL.Path, r.Header.Get("x-api-key"))
			}
		}
	}
}

func TestGatherAdmin_AdminBaseURLAndDualAuth(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, map[string]string{"admin_base_url": "https://console.mistral.ai/api/admin"})
	sink := &captureSink{}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(doer.reqs))
	}
	r := doer.reqs[0]
	if r.URL.Host != "console.mistral.ai" || r.URL.Path != adminAuditLogsPath {
		t.Fatalf("admin URL = %s, want https://console.mistral.ai%s", r.URL.String(), adminAuditLogsPath)
	}
	if r.Header.Get("Authorization") != "Bearer sk-admin-test" || r.Header.Get("x-api-key") != "sk-admin-test" {
		t.Fatalf("admin auth headers = Authorization %q x-api-key %q", r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
	}
}

func TestGatherAdmin_FullGather_EmitsAllSurfaces(t *testing.T) {
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Count observations by type.
	var auditEvents, costs, userInv, wsInv, keyInv, posture, coverage int
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.FindingReport:
			switch v.SubjectKind {
			case subjectCoverage:
				coverage++
			case subjectAuditLog:
				auditEvents++
			case subjectAdminUser:
				userInv++
			case subjectWorkspace:
				wsInv++
			case subjectAPIKey:
				keyInv++
			case subjectAdminPosture:
				posture++
			}
		case model.CostSample:
			costs++
		}
	}

	if coverage != 1 {
		t.Fatalf("coverage caveats = %d, want 1", coverage)
	}
	if auditEvents != 2 {
		t.Fatalf("audit events = %d, want 2", auditEvents)
	}
	if costs != 2 {
		t.Fatalf("cost samples = %d, want 2", costs)
	}
	if userInv != 1 {
		t.Fatalf("user inventory = %d, want 1 (aggregate)", userInv)
	}
	if wsInv != 2 {
		t.Fatalf("workspace inventory = %d, want 2", wsInv)
	}
	if keyInv != 2 {
		t.Fatalf("key inventory = %d, want 2", keyInv)
	}
	if posture != 2 {
		t.Fatalf("posture findings = %d, want 2 (spend + rate)", posture)
	}
}

func TestGatherAdminWorkspaces_LiveV2UUIDAndSpendLimit(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "admin_workspaces_v2.json"))
	if err != nil {
		t.Fatalf("read admin workspaces v2 fixture: %v", err)
	}
	doer := &adminFixtureDoer{t: t, bodyOverride: map[string]string{adminWorkspacesPath: string(body)}}
	s := newAdminSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAdminWorkspaces(context.Background(), sink); err != nil {
		t.Fatalf("gatherAdminWorkspaces: %v", err)
	}
	var workspaces, spend []model.FindingReport
	for _, f := range sink.findings() {
		switch f.SubjectKind {
		case subjectWorkspace:
			workspaces = append(workspaces, f)
		case subjectAdminPosture:
			spend = append(spend, f)
		}
	}
	if len(workspaces) != 2 {
		t.Fatalf("workspace findings = %d, want 2 (%+v)", len(workspaces), sink.findings())
	}
	if workspaces[0].SubjectRef != "ws_live_prod" {
		t.Fatalf("workspace[0] ref = %q, want uuid ws_live_prod", workspaces[0].SubjectRef)
	}
	if len(spend) != 1 {
		t.Fatalf("workspace spend-limit findings = %d, want 1 (%+v)", len(spend), sink.findings())
	}
	if spend[0].SubjectRef != "workspace_spend_limit:ws_live_prod" ||
		!strings.Contains(spend[0].Title, "250.00") ||
		!strings.Contains(spend[0].Title, "USD") {
		t.Fatalf("workspace spend-limit posture = %+v", spend[0])
	}
}

func TestDescriptor_AdminAPIKeyField(t *testing.T) {
	d := New().Descriptor()
	var sawAdminSecret, sawAdminBase bool
	for _, f := range d.ConfigFields {
		if f.Key == "admin_api_key" && f.Secret {
			sawAdminSecret = true
		}
		if f.Key == "admin_base_url" && f.Default == defaultAdminBaseURL {
			sawAdminBase = true
		}
	}
	if !sawAdminSecret {
		t.Fatal("admin_api_key must be declared as a secret config field")
	}
	if !sawAdminBase {
		t.Fatal("admin_base_url must be declared with the current default admin base")
	}
}

func TestGatherAdmin_ManageInventoryFallback(t *testing.T) {
	// When manage_inventory=true but no admin key, the UNVERIFIED-OFFLINE seam runs.
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_inventory": "true"})
	if s.adminClient != nil {
		t.Fatal("adminClient must be nil without admin_api_key")
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Should have coverage caveat + UNVERIFIED-OFFLINE inventory (2 ws + 1 rotation).
	fs := sink.findings()
	var wsCount int
	for _, f := range fs {
		if f.SubjectKind == subjectWorkspace {
			wsCount++
		}
	}
	if wsCount != 2 {
		t.Fatalf("UNVERIFIED-OFFLINE workspace findings = %d, want 2", wsCount)
	}
}

func TestGatherAdmin_AdminKeySupersedes_ManageInventory(t *testing.T) {
	// When admin_api_key is set, manage_inventory=true should NOT trigger the
	// UNVERIFIED-OFFLINE seam (admin API supersedes it).
	doer := &adminFixtureDoer{t: t}
	s := newAdminSource(t, doer, map[string]string{"manage_inventory": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// No requests to the UNVERIFIED-OFFLINE paths.
	for _, r := range doer.reqs {
		if r.URL.Path == defaultWorkspacesPath || r.URL.Path == defaultKeysPath {
			t.Fatalf("UNVERIFIED-OFFLINE path called when admin key is set: %s", r.URL.Path)
		}
	}
}
