// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// jsonDoer returns a canned JSON body per request path (no live network).
type jsonDoer struct {
	t      *testing.T
	byPath map[string]string
	reqs   []*http.Request
}

func (d *jsonDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	body, ok := d.byPath[req.URL.Path]
	if !ok {
		// default-empty list for any path the test did not pin, so a connector that
		// pulls more than the test pinned does not crash (it just sees no data).
		body = `{"data":[],"next_page":null}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// rateLimitPageDoer returns two rate-limit pages keyed by the documented page cursor.
type rateLimitPageDoer struct {
	t    *testing.T
	reqs []*http.Request
}

func (d *rateLimitPageDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	var body string
	switch req.URL.Query().Get("page") {
	case "":
		body = `{"data":[{"type":"rate_limit","group_type":"batch","models":null,"limits":[{"type":"enqueued_batch_requests","value":500000}]}],"next_page":"cursor_2"}`
	case "cursor_2":
		body = `{"data":[{"type":"rate_limit","group_type":"web_search","models":null,"limits":[{"type":"requests_per_minute","value":5000}]}],"next_page":null}`
	default:
		d.t.Fatalf("unexpected rate-limit page cursor %q", req.URL.Query().Get("page"))
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// TestSnapshot_GovernanceInventory proves the Admin-API governance inventory
// (ANT2-04/05/06/16) is read as metadata/refs only — never key material — and that
// the workspace governance object (residency/CMEK/compartment/tags) flows through.
func TestSnapshot_GovernanceInventory(t *testing.T) {
	s, _ := newLive(t)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// ANT2-04: External Keys are inventory refs (ekey_), with the KMS provider TYPE —
	// never key material. The type cannot even hold a secret (compile-time), so we
	// assert the ref shape and provider are present.
	if len(cat.ExternalKeys) != 1 {
		t.Fatalf("external keys = %d, want 1", len(cat.ExternalKeys))
	}
	ek := cat.ExternalKeys[0]
	if !strings.HasPrefix(ek.ID, "ekey_") {
		t.Errorf("external key id = %q, want an ekey_ reference", ek.ID)
	}
	if ek.Provider != "aws_kms" || !ek.InUse {
		t.Errorf("external key = %+v, want provider aws_kms in_use", ek)
	}

	// ANT2-05: read-only rate-limit group inventory (org groups + workspace overrides).
	if len(cat.RateLimits) != 3 {
		t.Fatalf("rate-limit groups = %d, want 3", len(cat.RateLimits))
	}
	if cat.RateLimits[0].GroupType != "model_group" || len(cat.RateLimits[0].Models) != 2 || len(cat.RateLimits[0].Limits) != 3 {
		t.Fatalf("org model-group rate limit = %+v, want models plus three limiter values", cat.RateLimits[0])
	}
	var wsOverride modelprovider.RateLimitRef
	for _, rl := range cat.RateLimits {
		if rl.WorkspaceRef == "wrkspc_01" {
			wsOverride = rl
		}
	}
	if wsOverride.WorkspaceRef == "" || len(wsOverride.Limits) != 2 || wsOverride.Limits[0].OrgLimit != 4000 {
		t.Fatalf("workspace override = %+v, want wrkspc_01 with org_limit echo", wsOverride)
	}

	// ANT2-06: the workspace governance object on wrkspc_01.
	var prod modelprovider.WorkspaceRef
	for _, w := range cat.Workspaces {
		if w.ID == "wrkspc_01" {
			prod = w
		}
	}
	if prod.ExternalKeyID != "ekey_prod_kms" {
		t.Errorf("wrkspc_01 external_key_id = %q, want ekey_prod_kms", prod.ExternalKeyID)
	}
	if prod.CompartmentID != "cmpt_prod" || prod.Geo != "us" {
		t.Errorf("wrkspc_01 compartment/geo = %q/%q, want cmpt_prod/us", prod.CompartmentID, prod.Geo)
	}
	if prod.Residency == nil || prod.Residency.DefaultInferenceGeo != "us" {
		t.Errorf("wrkspc_01 residency = %+v, want default us", prod.Residency)
	}
	if prod.Tags["cost_center"] != "platform" {
		t.Errorf("wrkspc_01 tags = %v, want cost_center=platform", prod.Tags)
	}

	// ANT2-16: beta-header inventory present on the source-of-truth surface.
	if len(cat.BetaHeaders) == 0 {
		t.Error("beta-header inventory missing on direct surface")
	}
}

func TestFetchRateLimitsAtUsesNextPagePagination(t *testing.T) {
	doer := &rateLimitPageDoer{t: t}
	s := New()
	s.doer = doer
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	limits, err := s.fetchRateLimitsAt(context.Background(), rateLimitsPath, "")
	if err != nil {
		t.Fatalf("fetchRateLimitsAt: %v", err)
	}
	if len(limits) != 2 || limits[0].GroupType != "batch" || limits[1].GroupType != "web_search" {
		t.Fatalf("limits = %+v, want batch then web_search from two pages", limits)
	}
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(doer.reqs))
	}
	if doer.reqs[0].URL.Query().Get("page") != "" || doer.reqs[1].URL.Query().Get("page") != "cursor_2" {
		t.Fatalf("page cursors = %q/%q, want empty/cursor_2", doer.reqs[0].URL.RawQuery, doer.reqs[1].URL.RawQuery)
	}
	for _, req := range doer.reqs {
		if req.URL.Query().Get("after_id") != "" || req.URL.Query().Get("limit") != "" {
			t.Fatalf("rate-limit pagination sent old cursor params: %q", req.URL.RawQuery)
		}
	}
}

// TestSnapshot_LiveCapabilitiesPreferred proves the live Models API capabilities
// (ANT2-16) supersede the hardcoded catalog when present, and that a model the live
// API did not enrich falls back to the declared stack — each labeled honestly.
func TestSnapshot_LiveCapabilitiesPreferred(t *testing.T) {
	s, _ := newLive(t)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	opus, ok := cat.FindModel("claude-opus-4-8")
	if !ok {
		t.Fatal("opus model missing")
	}
	if opus.CapabilitySource != "live" {
		t.Errorf("opus CapabilitySource = %q, want live", opus.CapabilitySource)
	}
	if opus.APICapabilities == nil {
		t.Fatal("opus missing live APICapabilities")
	}
	if !containsAll(opus.APICapabilities.EffortLevels, "low", "medium", "high", "xhigh") {
		t.Errorf("opus effort levels = %v, want low..xhigh", opus.APICapabilities.EffortLevels)
	}
	if opus.MaxInputTokens != 1000000 || opus.MaxOutputTokens != 64000 {
		t.Errorf("opus limits = in %d out %d, want 1000000/64000 (live)", opus.MaxInputTokens, opus.MaxOutputTokens)
	}
	// A model the live API did not enrich with a capabilities object keeps the declared
	// stack, labeled declared.
	sonnet, _ := cat.FindModel("claude-sonnet-4-6")
	if sonnet.CapabilitySource != "declared" || sonnet.APICapabilities != nil {
		t.Errorf("sonnet caps source = %q (apiCaps=%v), want declared/nil", sonnet.CapabilitySource, sonnet.APICapabilities)
	}
}

// TestRetirementPerSurface proves model lifecycle is PER-PLATFORM (ANT2-03): Sonnet 4
// retires on a different date first-party than on Vertex, and no date is fabricated
// for a surface the authority did not publish (Bedrock absent, not invented).
func TestRetirementPerSurface(t *testing.T) {
	rs := RetirementsFor("claude-sonnet-4")
	bySurface := map[model.Gateway]string{}
	for _, r := range rs {
		bySurface[r.Surface] = r.RetiresOn
		if r.AsOf == "" {
			t.Errorf("retirement for %q missing AsOf", r.Surface)
		}
	}
	if bySurface[model.GatewayDirect] != "2026-06-15" {
		t.Errorf("first-party retirement = %q, want 2026-06-15", bySurface[model.GatewayDirect])
	}
	if bySurface[model.GatewayVertex] != "2026-09-14" {
		t.Errorf("Vertex retirement = %q, want 2026-09-14 (divergent)", bySurface[model.GatewayVertex])
	}
	if _, fabricated := bySurface[model.GatewayBedrockMantle]; fabricated {
		t.Error("Bedrock retirement date was fabricated; authority did not publish it")
	}
	// The current successor does not report its own sunset.
	if RetirementsFor("claude-sonnet-4-6") != nil {
		t.Error("successor model claude-sonnet-4-6 must not carry a retirement schedule")
	}
}

// TestRejectsSamplingParams pins the Opus 4.7+ param-deprecation detection (ANT2-03).
func TestRejectsSamplingParams(t *testing.T) {
	cases := map[string]bool{
		"claude-opus-4-8":   true,
		"claude-opus-4-7":   true,
		"claude-opus-4-6":   false,
		"claude-opus-4-1":   false,
		"claude-opus-5-0":   true,
		"claude-sonnet-4-6": false,
		"claude-haiku-4-5":  false,
	}
	for id, want := range cases {
		if got := RejectsSamplingParams(id); got != want {
			t.Errorf("RejectsSamplingParams(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestGather_CostCarriesConfiguredSurface proves the ANT2-01 fix: when the connector
// is configured for an Anthropic-operated surface that DOES have the Admin API
// (claude-platform-aws), the derived AND billed cost samples carry THAT surface — not
// a hardcoded direct (which would mis-attribute FinOps for the deployment).
func TestGather_CostCarriesConfiguredSurface(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "gateway": "claude-platform-aws"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var costN int
	for _, o := range sink.obs {
		cs, ok := o.(model.CostSample)
		if !ok {
			continue
		}
		costN++
		if cs.Gateway != model.GatewayClaudePlatformAWS {
			t.Errorf("cost sample (%s/%s) Gateway = %q, want claude-platform-aws", cs.ModelRef, cs.Provenance, cs.Gateway)
		}
	}
	if costN == 0 {
		t.Fatal("no cost samples emitted on claude-platform-aws surface")
	}
}

// TestGather_DegradedOnNonAdminSurface proves ANT2-01 honest degradation: on a surface
// without the Admin API (Bedrock Mantle), the connector emits a posture finding and
// does NOT poll an Admin endpoint that does not exist (no fabricated empty inventory).
func TestGather_DegradedOnNonAdminSurface(t *testing.T) {
	doer := &jsonDoer{t: t, byPath: map[string]string{}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "gateway": "bedrock-mantle"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// No HTTP call was made to any Admin endpoint (the surface has none).
	if len(doer.reqs) != 0 {
		t.Fatalf("degraded surface made %d HTTP requests, want 0", len(doer.reqs))
	}
	var sawDegraded bool
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("degraded surface emitted a non-finding %T", o)
		}
		if f.SubjectKind == subjectSurface && strings.Contains(f.Title, "unavailable") {
			sawDegraded = true
		}
	}
	if !sawDegraded {
		t.Error("expected an 'Admin-API governance ingest unavailable' posture finding")
	}
}

// TestGather_WorkspaceWithoutCMEK proves the ANT2-06 posture finding fires for an
// active workspace with no customer-managed key, and not for one that has one.
func TestGather_WorkspaceWithoutCMEK(t *testing.T) {
	doer := &jsonDoer{t: t, byPath: map[string]string{
		"/v1/organizations/workspaces": `{"data":[
			{"id":"wrkspc_nocmek","name":"NoCMEK","created_at":"2026-01-01T00:00:00Z"},
			{"id":"wrkspc_haskey","name":"HasKey","external_key_id":"ekey_x","created_at":"2026-01-01T00:00:00Z"}
		],"has_more":false}`,
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	s.costReport = false // isolate to governance findings
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherGovernance(context.Background(), sink); err != nil {
		t.Fatalf("gatherGovernance: %v", err)
	}
	var cmekSubjects []string
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.SubjectKind == subjectWorkspace && strings.Contains(f.Title, "customer-managed encryption key") {
			cmekSubjects = append(cmekSubjects, f.SubjectRef)
		}
	}
	if len(cmekSubjects) != 1 || cmekSubjects[0] != "wrkspc_nocmek" {
		t.Fatalf("CMEK findings = %v, want exactly [wrkspc_nocmek]", cmekSubjects)
	}
}

// TestGather_WorkspaceWithoutRateLimitOverrides proves workspace-level Rate Limits
// responses are override-only: an active workspace with no rows inherits the org pool
// (NOT unlimited), and the connector emits an Info visibility note.
func TestGather_WorkspaceWithoutRateLimitOverrides(t *testing.T) {
	doer := &jsonDoer{t: t, byPath: map[string]string{
		"/v1/organizations/workspaces": `{"data":[
			{"id":"wrkspc_override","name":"Override","external_key_id":"ekey_x","created_at":"2026-01-01T00:00:00Z"},
			{"id":"wrkspc_inherit","name":"Inherit","external_key_id":"ekey_y","created_at":"2026-01-01T00:00:00Z"},
			{"id":"wrkspc_archived","name":"Archived","archived_at":"2026-01-02T00:00:00Z","created_at":"2026-01-01T00:00:00Z"}
		],"has_more":false}`,
		"/v1/organizations/rate_limits": `{"data":[
			{"type":"rate_limit","group_type":"batch","models":null,"limits":[{"type":"enqueued_batch_requests","value":500000}]}
		],"next_page":null}`,
		"/v1/organizations/workspaces/wrkspc_override/rate_limits": `{"data":[
			{"type":"workspace_rate_limit","group_type":"model_group","models":["claude-opus-4-8"],"limits":[{"type":"requests_per_minute","value":1000,"org_limit":4000}]}
		],"next_page":null}`,
		"/v1/organizations/workspaces/wrkspc_inherit/rate_limits": `{"data":[],"next_page":null}`,
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	s.costReport = false // isolate to governance findings
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherGovernance(context.Background(), sink); err != nil {
		t.Fatalf("gatherGovernance: %v", err)
	}
	var inherited []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if ok && f.SubjectKind == subjectWorkspace && strings.Contains(f.Title, "no rate-limit overrides") {
			inherited = append(inherited, f)
		}
	}
	if len(inherited) != 1 {
		t.Fatalf("no-override findings = %+v, want exactly one", inherited)
	}
	if inherited[0].SubjectRef != "wrkspc_inherit" || inherited[0].Severity != model.SeverityInfo {
		t.Fatalf("no-override finding = %+v, want Info on wrkspc_inherit", inherited[0])
	}
}

// TestGather_SpendLimitGapPostureFinding proves the honesty finding fires on an
// admin surface: the Admin API has no set/clear workspace spend-limit endpoint, so the
// connector records the gap (Info) instead of pretending an API spend cap exists.
func TestGather_SpendLimitGapPostureFinding(t *testing.T) {
	doer := &jsonDoer{t: t, byPath: map[string]string{}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	s.costReport = false // isolate to governance findings
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherGovernance(context.Background(), sink); err != nil {
		t.Fatalf("gatherGovernance: %v", err)
	}
	var found *model.FindingReport
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.SubjectKind == subjectSpendLimit {
			ff := f
			found = &ff
		}
	}
	if found == nil {
		t.Fatal("expected a spend-limit posture finding (honesty gap)")
	}
	if found.Severity != model.SeverityInfo || found.SubjectRef != "organization" {
		t.Errorf("spend-limit finding = %+v, want Info/organization", *found)
	}
	if !strings.Contains(found.Title, "spend-limit") {
		t.Errorf("spend-limit finding title = %q, want it to mention spend-limit", found.Title)
	}
}
