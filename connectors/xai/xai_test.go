// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests in this file cover xAI management/catalog wire currency re-verified against
// docs.x.ai on 2026-07-04, including optional tpm/qps/qpm quota metadata.
package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fixtureDoer multiplexes by request path to the recorded xAI fixtures (management plane +
// inference plane). It records every request so a test can assert the connector is
// read-only, and can be told to return 404 for any path fragment (the wrong-billing-mode /
// not-entitled degrade tests).
type fixtureDoer struct {
	t            *testing.T
	reqs         []*http.Request
	unavailable  map[string]bool   // path-fragment -> return 404
	bodyOverride map[string]string // path-fragment -> return this 200 body instead of the fixture
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	path := req.URL.Path
	for frag := range d.unavailable {
		if strings.Contains(path, frag) {
			return resp(404, `{"error":"not_found"}`), nil
		}
	}
	for frag, body := range d.bodyOverride {
		if strings.Contains(path, frag) {
			return resp(200, body), nil
		}
	}
	var file string
	switch {
	case path == validationPath:
		file = "validation.json"
	case strings.HasSuffix(path, "/api-keys"):
		file = "api_keys.json"
	case strings.HasSuffix(path, "/invoice/preview"):
		file = "preview.json"
	case strings.HasSuffix(path, "/invoices"):
		file = "invoices.json"
	case strings.HasSuffix(path, "/prepaid/balance"):
		file = "balance.json"
	case strings.HasSuffix(path, "/spending-limits"):
		file = "spending_limits.json"
	case path == languageModelsPath:
		file = "language_models.json"
	case strings.Contains(path, "/audit/teams/") && strings.HasSuffix(path, "/events"):
		file = "audit_events.json"
	default:
		d.t.Fatalf("unexpected request path %q", path)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return resp(200, string(body)), nil
}

func resp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range s.obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

func (s *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

// fixedClock is after key_old (2026-01-01, >90d) and within 90d of key_new (2026-06-10).
func fixedClock() time.Time { return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"management_key": "mgmt-test", "api_key": "inf-test"}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor = %+v", d)
	}
	var sawMgmt, sawInf bool
	for _, f := range d.ConfigFields {
		if f.Key == "management_key" && f.Secret {
			sawMgmt = true
		}
		if f.Key == "api_key" && f.Secret {
			sawInf = true
		}
	}
	if !sawMgmt || !sawInf {
		t.Fatalf("both credentials must be declared secret (mgmt=%v inf=%v)", sawMgmt, sawInf)
	}
}

func TestGather_KeyPosture(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"billing": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	// key_old (>90d, wildcard ACL) -> rotation + broad-ACL; key_new (recent, scoped) -> none;
	// key_disabled (old, wildcard, but disabled) -> skipped entirely.
	var rotation, acl int
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectAPIKey:
			rotation++
			if f.SubjectRef != "key_old" || f.Severity != model.SeverityMedium {
				t.Fatalf("rotation finding = %+v", f)
			}
		case subjectACL:
			acl++
			if f.SubjectRef != "key_old" || f.Severity != model.SeverityLow {
				t.Fatalf("acl finding = %+v", f)
			}
		}
	}
	if rotation != 1 || acl != 1 {
		t.Fatalf("findings = rotation %d, acl %d; want 1/1 (%+v)", rotation, acl, fs)
	}
	for _, f := range fs {
		if strings.Contains(f.Title, "xai-") {
			t.Fatalf("finding leaked a key hint: %q", f.Title)
		}
		if len(f.DetailHash) != 64 {
			t.Fatalf("detail hash not sha-256 hex: %q", f.DetailHash)
		}
	}
}

func TestGather_Billing(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_keys": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	// invoices: 2 finalized (PAID) lines billed; the PENDING invoice is skipped.
	// preview: 1 estimated line. -> 3 cost samples total.
	if len(costs) != 3 {
		t.Fatalf("cost samples = %d, want 3 (2 billed + 1 estimated)", len(costs))
	}
	var billed, estimated int64
	var sawBilled, sawEstimated bool
	for _, c := range costs {
		if c.ProviderRef != modelprovider.ProviderXAI || c.CostType != costTypeXAI || c.WorkspaceRef != "team_default" {
			t.Fatalf("cost attribution = %+v", c)
		}
		switch c.Provenance {
		case model.ProvenanceBilled:
			billed += c.CostMicroUSD
			sawBilled = true
			if c.SessionRef != "2026-05" {
				t.Fatalf("billed session ref = %q, want invoice number 2026-05", c.SessionRef)
			}
		case model.ProvenanceEstimated:
			estimated += c.CostMicroUSD
			sawEstimated = true
			if c.SessionRef != "preview" {
				t.Fatalf("estimated session ref = %q, want preview", c.SessionRef)
			}
		}
	}
	if !sawBilled || !sawEstimated {
		t.Fatalf("missing provenance: billed=%v estimated=%v", sawBilled, sawEstimated)
	}
	if billed != 14_750_000 { // 10.50 + 4.25
		t.Fatalf("billed total = %d micro-USD, want 14750000", billed)
	}
	if estimated != 3_750_000 { // 3.75
		t.Fatalf("estimated total = %d micro-USD, want 3750000", estimated)
	}

	fs := sink.findings()
	// balance info inventory + no-spending-limit Low posture.
	var balance, spendcap int
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectBalance:
			balance++
			if f.Severity != model.SeverityInfo {
				t.Fatalf("balance finding = %+v", f)
			}
		case subjectSpendCap:
			spendcap++
			if f.Severity != model.SeverityLow || !strings.Contains(f.Title, "no effective monthly spending limit") {
				t.Fatalf("spend-cap finding = %+v", f)
			}
		}
	}
	if balance != 1 || spendcap != 1 {
		t.Fatalf("findings = balance %d, spendcap %d; want 1/1 (%+v)", balance, spendcap, fs)
	}
}

func TestGather_PrepaidModeSkipsPostpaidSurfaces(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{"/invoice/preview": true, "/spending-limits": true}}
	s := newSource(t, doer, map[string]string{"manage_keys": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail when postpaid sub-surfaces 404; got %v", err)
	}
	costs := sink.costs()
	// Only the 2 billed invoice lines remain (preview 404s).
	if len(costs) != 2 {
		t.Fatalf("cost samples = %d, want 2 (preview skipped)", len(costs))
	}
	for _, c := range costs {
		if c.Provenance != model.ProvenanceBilled {
			t.Fatalf("unexpected non-billed sample: %+v", c)
		}
	}
	// Balance still emits; the spending-limit posture does not (404).
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectSpendCap {
			t.Fatalf("spending-limit finding should be skipped on a 404: %+v", f)
		}
	}
}

func TestGather_KeysUnavailableDegrades(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{"/api-keys": true}}
	s := newSource(t, doer, map[string]string{"billing": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail on a 403/404 key surface; got %v", err)
	}
	var keyFindings []model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectAPIKey {
			keyFindings = append(keyFindings, f)
		}
	}
	if len(keyFindings) != 1 || keyFindings[0].Severity != model.SeverityMedium {
		t.Fatalf("want 1 key-unavailable Medium posture, got %+v", keyFindings)
	}
	if !strings.Contains(keyFindings[0].Title, "unavailable") {
		t.Fatalf("unavailable finding title = %q", keyFindings[0].Title)
	}
}

func TestGather_TeamUnresolvedDegrades(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{validationPath: true}}
	s := newSource(t, doer, nil) // no team_id -> must discover via validation, which 404s
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail when the team cannot be resolved; got %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectTeam {
		t.Fatalf("want 1 team-unresolved posture, got %+v", fs)
	}
}

func TestGather_OfflineNoManagementKeyEmitsNothing(t *testing.T) {
	s := New()
	s.doer = &fixtureDoer{t: t}
	// An inference key alone drives only the catalog (Snapshot); Gather is a no-op.
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "inf-test"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline gather emitted %d observations, want 0", len(sink.obs))
	}
}

func TestGather_ReadOnlyBearerAuthPerPlane(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
		// Gather only touches the management plane; it must carry the management key.
		if r.URL.Host == "management-api.x.ai" && r.Header.Get("Authorization") != "Bearer mgmt-test" {
			t.Fatalf("management credential not sent on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
	}
}

func TestSnapshot_LiveCatalog(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderXAI || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 2 {
		t.Fatalf("live models = %d, want 2", len(cat.Models))
	}

	g43, ok := cat.FindModel("grok-4.3")
	if !ok || g43.Pricing == nil {
		t.Fatalf("grok-4.3 not present/priced: %+v", g43)
	}
	// 12500 cents/1e8 tok -> $1.25/M; 25000 -> $2.50/M; 2000 cached -> $0.20/M.
	if g43.Pricing.InputPerMTokUSD != 1.25 || g43.Pricing.OutputPerMTokUSD != 2.50 || g43.Pricing.CacheReadPerMTokUSD != 0.20 {
		t.Fatalf("grok-4.3 pricing = %+v", g43.Pricing)
	}
	if g43.CapabilitySource != "live" {
		t.Fatalf("capability source = %q, want live", g43.CapabilitySource)
	}
	for _, want := range []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapVision, modelprovider.CapToolUse, modelprovider.CapStructuredOutputs,
	} {
		if !g43.HasCapability(want) {
			t.Fatalf("grok-4.3 missing capability %q (caps=%v)", want, g43.Capabilities)
		}
	}
	// prompt_caching is a pricing feature, NOT a per-model capability tag (per the docs).
	if g43.HasCapability(modelprovider.CapPromptCaching) {
		t.Fatal("grok-4.3 must NOT tag CapPromptCaching (it is a pricing field, not a page capability)")
	}

	// The reasoning variant carries extended thinking from its declared family.
	reason, _ := cat.FindModel("grok-4.20-0309-reasoning")
	if !reason.HasCapability(modelprovider.CapExtendedThinking) {
		t.Fatalf("grok-4.20 reasoning missing extended_thinking (caps=%v)", reason.Capabilities)
	}
	// It is text-only (input_modalities ["text"]) and its declared family has no vision, so
	// the live modality signal must SUPPRESS CapVision — proving liveCapabilities gates the
	// flag on the modality, not hard-codes it.
	if reason.HasCapability(modelprovider.CapVision) {
		t.Fatalf("grok-4.20 reasoning is text-only; CapVision must be suppressed (caps=%v)", reason.Capabilities)
	}

	// Key inventory (masked hint only, never a secret) + the team as a workspace.
	if len(cat.Keys) != 3 {
		t.Fatalf("keys = %d, want 3", len(cat.Keys))
	}
	for _, k := range cat.Keys {
		if k.Hint == "" || !strings.Contains(k.Hint, "**") {
			t.Fatalf("key hint looks unmasked: %q", k.Hint)
		}
	}
	if len(cat.Workspaces) != 1 || cat.Workspaces[0].ID != "team_default" {
		t.Fatalf("workspaces = %+v, want [team_default]", cat.Workspaces)
	}
	// The disabled key is inventoried with status disabled (not dropped).
	var sawDisabled bool
	for _, k := range cat.Keys {
		if k.ID == "key_disabled" && k.Status == "disabled" {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatal("key_disabled should be inventoried with status=disabled")
	}
}

func TestSnapshot_OfflineCatalog(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Models) != len(declaredModelIDs) {
		t.Fatalf("offline models = %d, want %d", len(cat.Models), len(declaredModelIDs))
	}
	for _, m := range cat.Models {
		if m.CapabilitySource != "declared" {
			t.Fatalf("offline model %s capability source = %q", m.Ref, m.CapabilitySource)
		}
	}
	g43, ok := cat.FindModel("grok-4.3")
	if !ok || g43.Pricing == nil || g43.Pricing.InputPerMTokUSD != 1.25 || g43.ContextWindow != 1_000_000 {
		t.Fatalf("offline grok-4.3 = %+v", g43)
	}
	if len(cat.Keys) != 0 || len(cat.Workspaces) != 0 {
		t.Fatal("offline catalog must not contain key/workspace inventory")
	}
}

func TestPricingFromAPI_CentsConversion(t *testing.T) {
	lm := languageModel{
		ID:                       "grok-4.3",
		PromptTextTokenPrice:     12500,
		CachedPromptTextPrice:    2000,
		CompletionTextTokenPrice: 25000,
	}
	p, ok := pricingFromAPI(lm)
	if !ok || p.InputPerMTokUSD != 1.25 || p.OutputPerMTokUSD != 2.50 || p.CacheReadPerMTokUSD != 0.20 {
		t.Fatalf("pricing = %+v ok=%v", p, ok)
	}
	// A non-priced (e.g. embedding) entry yields ok=false rather than a $0 price.
	if _, ok := pricingFromAPI(languageModel{ID: "x", PromptTextTokenPrice: 0}); ok {
		t.Fatal("a zero-price model must not be priced")
	}
}

func TestFamilyFor_LongestPrefix(t *testing.T) {
	reason, ok := familyFor("grok-4.20-0309-reasoning")
	if !ok || !hasCap(reason.capabilities, modelprovider.CapExtendedThinking) {
		t.Fatalf("grok-4.20-0309-reasoning should resolve to the reasoning family: %+v ok=%v", reason, ok)
	}
	nonReason, ok := familyFor("grok-4.20-0309-non-reasoning")
	if !ok || hasCap(nonReason.capabilities, modelprovider.CapExtendedThinking) {
		t.Fatalf("grok-4.20-0309-non-reasoning must NOT have extended_thinking: %+v", nonReason)
	}
	build, ok := familyFor("grok-build-0.1")
	if !ok || build.pricing.InputPerMTokUSD != 1.00 {
		t.Fatalf("grok-build-0.1 = %+v ok=%v", build, ok)
	}
	if _, ok := familyFor("gpt-4o"); ok {
		t.Fatal("a non-Grok id must not match a family")
	}
}

func TestFlexBool_StringOrBool(t *testing.T) {
	cases := map[string]bool{`true`: true, `false`: false, `"true"`: true, `"false"`: false, `null`: false, `"garbage"`: false}
	for in, want := range cases {
		var b flexBool
		if err := json.Unmarshal([]byte(in), &b); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if bool(b) != want {
			t.Fatalf("flexBool(%q) = %v, want %v", in, bool(b), want)
		}
	}
}

func TestFlexInt_StringOrNumber(t *testing.T) {
	cases := map[string]int{`123`: 123, `"456"`: 456, `null`: 0, `""`: 0, `"garbage"`: 0}
	for in, want := range cases {
		var n flexInt
		if err := json.Unmarshal([]byte(in), &n); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if int(n) != want {
			t.Fatalf("flexInt(%q) = %d, want %d", in, int(n), want)
		}
	}
}

func TestXAIAPIKeyQuotaFields(t *testing.T) {
	var resp apiKeysResponse
	body, err := os.ReadFile(filepath.Join("testdata", "api_keys.json"))
	if err != nil {
		t.Fatalf("read api_keys fixture: %v", err)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal api keys: %v", err)
	}
	if len(resp.APIKeys) == 0 {
		t.Fatal("api key fixture empty")
	}
	k := resp.APIKeys[0]
	if int(k.TPM) != 100000 || int(k.QPS) != 12 || int(k.QPM) != 720 {
		t.Fatalf("quota fields = tpm %d qps %d qpm %d, want 100000/12/720", int(k.TPM), int(k.QPS), int(k.QPM))
	}
}

func TestACLs_MergeAcrossSpellings(t *testing.T) {
	k := xaiAPIKey{
		ACLSnake: []string{"api-key:endpoint:*", "dup"},
		ACLCamel: []string{"api-key:model:grok-4.3"},
		ACLPlain: []string{"dup", "api-key:endpoint:chat"},
	}
	got := k.acls()
	want := []string{"api-key:endpoint:*", "dup", "api-key:model:grok-4.3", "api-key:endpoint:chat"}
	if len(got) != len(want) {
		t.Fatalf("merged acls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged acls[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGather_Billing_LowBalancePosture(t *testing.T) {
	// Balance fixture reports 12.34. Threshold 20 -> below -> Medium posture.
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_keys": "false", "low_balance_usd": "20"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var sawLow bool
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectBalance {
			sawLow = true
			if f.Severity != model.SeverityMedium || f.Kind != "posture" || !strings.Contains(f.Title, "below threshold") {
				t.Fatalf("low-balance finding = %+v", f)
			}
		}
	}
	if !sawLow {
		t.Fatal("missing low-balance posture finding")
	}

	// Threshold 10 -> 12.34 is above -> stays Info inventory (the guard boundary).
	doer2 := &fixtureDoer{t: t}
	s2 := newSource(t, doer2, map[string]string{"manage_keys": "false", "low_balance_usd": "10"})
	sink2 := &captureSink{}
	if err := s2.Gather(context.Background(), sink2); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range sink2.findings() {
		if f.SubjectKind == subjectBalance && (f.Severity != model.SeverityInfo || f.Kind != "inventory") {
			t.Fatalf("balance above threshold must stay Info/inventory: %+v", f)
		}
	}
}

func TestGather_SpendingLimitConfigured(t *testing.T) {
	// A populated spending-limits body must take the Info "configured" branch, not the
	// Low "no limit" posture.
	doer := &fixtureDoer{t: t, bodyOverride: map[string]string{
		"/spending-limits": `{"spendingLimits":{"effectiveSl":{"val":"500.00"},"effectiveHardSl":{"val":"1000.00"}}}`,
	}}
	s := newSource(t, doer, map[string]string{"manage_keys": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var sawConfigured int
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectSpendCap {
			sawConfigured++
			if f.Severity != model.SeverityInfo || f.Kind != "inventory" || !strings.Contains(f.Title, "configured") {
				t.Fatalf("configured-limit finding = %+v", f)
			}
		}
	}
	if sawConfigured != 1 {
		t.Fatalf("want 1 configured-limit inventory finding, got %d", sawConfigured)
	}
}

func TestGather_BilledOccurredTimeFallbackAndZeroLineSkip(t *testing.T) {
	// A finalized invoice with a valid createTime + a zero line + a malformed-createTime
	// invoice: the zero line emits no sample; the malformed createTime falls back to the
	// connector clock; the valid one keeps its date.
	doer := &fixtureDoer{t: t, bodyOverride: map[string]string{
		"/invoices": `{"invoices":[
			{"invoiceNumber":"2026-05","invoiceStatus":"PAID","createTime":"2026-05-01T00:00:00Z","lines":[
				{"description":"input","amount":"5.00"},
				{"description":"credit","amount":"0.00"}
			]},
			{"invoiceNumber":"2026-04","invoiceStatus":"PAID","createTime":"","lines":[
				{"description":"input","amount":"2.00"}
			]}
		]}`,
	}}
	s := newSource(t, doer, map[string]string{"manage_keys": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	billed := []model.CostSample{}
	for _, c := range sink.costs() {
		if c.Provenance == model.ProvenanceBilled {
			billed = append(billed, c)
		}
	}
	// 2 billed (5.00, 2.00); the 0.00 credit line is skipped.
	if len(billed) != 2 {
		t.Fatalf("billed samples = %d, want 2 (zero line skipped)", len(billed))
	}
	for _, c := range billed {
		switch c.SessionRef {
		case "2026-05":
			if !c.OccurredAt.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("valid-createTime OccurredAt = %v, want 2026-05-01", c.OccurredAt)
			}
		case "2026-04":
			if !c.OccurredAt.Equal(fixedClock()) {
				t.Fatalf("malformed-createTime OccurredAt = %v, want clock fallback %v", c.OccurredAt, fixedClock())
			}
		default:
			t.Fatalf("unexpected billed sample session ref %q", c.SessionRef)
		}
	}
}

func TestDecimalStringToMicroUSD(t *testing.T) {
	cases := map[string]int64{
		"10.50":     10_500_000, // exact
		"0.0000005": 1,          // rounds UP via math.Round (sub-micro)
		"0.0000004": 0,          // rounds DOWN
		" 1.25 ":    1_250_000,  // trimmed
		"-1":        0,          // negative -> 0
		"":          0,          // empty -> 0
		"abc":       0,          // unparseable -> 0
		"0.00":      0,          // zero -> 0
	}
	for in, want := range cases {
		if got := decimalStringToMicroUSD(in); got != want {
			t.Fatalf("decimalStringToMicroUSD(%q) = %d, want %d", in, got, want)
		}
	}
}

// pagingKeysDoer synthesizes a cursor-paginated /api-keys response: page 1 carries a
// non-empty paginationToken, page 2 closes it. When infinite, every page returns a fresh
// token (to exercise the maxPages safety bound).
type pagingKeysDoer struct {
	t        *testing.T
	reqs     []*http.Request
	infinite bool
}

func (d *pagingKeysDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if !strings.HasSuffix(req.URL.Path, "/api-keys") {
		d.t.Fatalf("unexpected path %q", req.URL.Path)
	}
	token := req.URL.Query().Get("paginationToken")
	if d.infinite {
		return resp(200, `{"apiKeys":[{"apiKeyId":"k","redactedApiKey":"xai-**","createTime":"2026-06-10T00:00:00Z"}],"paginationToken":"more"}`), nil
	}
	if token == "" {
		return resp(200, `{"apiKeys":[{"apiKeyId":"k1","redactedApiKey":"xai-**1","createTime":"2026-06-10T00:00:00Z"}],"paginationToken":"p2"}`), nil
	}
	return resp(200, `{"apiKeys":[{"apiKeyId":"k2","redactedApiKey":"xai-**2","createTime":"2026-06-10T00:00:00Z"}],"paginationToken":""}`), nil
}

func openWithDoer(t *testing.T, doer modelprovider.Doer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"management_key": "mgmt-test"}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestListKeys_PaginatesAcrossPages(t *testing.T) {
	doer := &pagingKeysDoer{t: t}
	s := openWithDoer(t, doer, nil)
	keys, err := s.listKeys(context.Background(), "team_default")
	if err != nil {
		t.Fatalf("listKeys: %v", err)
	}
	if len(keys) != 2 || keys[0].APIKeyID != "k1" || keys[1].APIKeyID != "k2" {
		t.Fatalf("paginated keys = %+v, want [k1 k2] from both pages", keys)
	}
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (one per page)", len(doer.reqs))
	}
	if got := doer.reqs[1].URL.Query().Get("paginationToken"); got != "p2" {
		t.Fatalf("page-2 cursor = %q, want p2", got)
	}
}

func TestListKeys_MaxPagesBoundsRunawayCursor(t *testing.T) {
	doer := &pagingKeysDoer{t: t, infinite: true}
	s := openWithDoer(t, doer, map[string]string{"max_pages": "2"})
	keys, err := s.listKeys(context.Background(), "team_default")
	if err != nil {
		t.Fatalf("listKeys: %v", err)
	}
	// The cursor never closes; the loop must stop at max_pages (2), not run away.
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want exactly max_pages=2", len(doer.reqs))
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2 (one per bounded page)", len(keys))
	}
}

func TestInvoicesSinceWindow_MonthEndNoOverflow(t *testing.T) {
	// Clock at a month-end (Mar 31) with lookback 1: the `since` window must be February,
	// not March — the day-overflow bug would skip a whole month of billed invoices.
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"management_key": "mgmt-test", "manage_keys": "false", "lookback_months": "1",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var since *http.Request
	for _, r := range doer.reqs {
		if strings.HasSuffix(r.URL.Path, "/invoices") {
			since = r
		}
	}
	if since == nil {
		t.Fatal("no /invoices request issued")
	}
	if y, m := since.URL.Query().Get("since.year"), since.URL.Query().Get("since.month"); y != "2026" || m != "2" {
		t.Fatalf("since window = %s-%s, want 2026-2 (February, no day-overflow)", y, m)
	}
}

// hasCap is a test helper.
func hasCap(caps []modelprovider.Capability, c modelprovider.Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}
