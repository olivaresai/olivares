// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mistral

import (
	"context"
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

// fixtureDoer multiplexes by request path to the recorded Mistral fixtures. It records
// every request so a test can assert the connector is read-only, and can be told to
// return 403/404 for a given path (the UNVERIFIED-OFFLINE degrade test).
type fixtureDoer struct {
	t           *testing.T
	reqs        []*http.Request
	unavailable map[string]bool // path -> return 404
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if d.unavailable[req.URL.Path] {
		return resp(404, `{"error":"not_found"}`), nil
	}
	var file string
	switch req.URL.Path {
	case defaultModelsND:
		file = "models.json"
	case defaultWorkspacesPath:
		file = "workspaces.json"
	case defaultKeysPath:
		file = "api_keys.json"
	default:
		d.t.Fatalf("unexpected request path %q", req.URL.Path)
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

// fixedClock is after key_old (2026-01-01, >90d) and within 90d of key_new (2026-06-01).
func fixedClock() time.Time { return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "sk-mistral-test"}
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
	var sawSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == "api_key" && f.Secret {
			sawSecret = true
		}
	}
	if !sawSecret {
		t.Fatal("api_key must be declared as a secret config field")
	}
}

func TestSnapshot_LiveCatalog(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderMistral || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 4 {
		t.Fatalf("models = %d, want 4 (incl. the fine-tuned card)", len(cat.Models))
	}

	large, ok := cat.FindModel("mistral-large-latest")
	if !ok || large.Pricing == nil || large.Pricing.InputPerMTokUSD != 0.5 || large.Pricing.OutputPerMTokUSD != 1.5 {
		t.Fatalf("mistral-large not enriched: %+v", large)
	}
	if large.Pricing.Source != modelprovider.PricingList {
		t.Fatalf("pricing source = %q, want list", large.Pricing.Source)
	}
	if large.CapabilitySource != "live" {
		t.Fatalf("capability source = %q, want live", large.CapabilitySource)
	}
	if large.ContextWindow != 131072 {
		t.Fatalf("context window = %d, want 131072 (live max_context_length)", large.ContextWindow)
	}
	// Live booleans → vision + tool_use; declared family folds in structured_outputs + batch.
	for _, want := range []modelprovider.Capability{
		modelprovider.CapVision, modelprovider.CapToolUse, modelprovider.CapStreaming,
		modelprovider.CapStructuredOutputs, modelprovider.CapBatch,
	} {
		if !large.HasCapability(want) {
			t.Fatalf("mistral-large missing capability %q (caps=%v)", want, large.Capabilities)
		}
	}

	// Codestral: 256K context from the live API, FIM model.
	cod, _ := cat.FindModel("codestral-latest")
	if cod.ContextWindow != 262144 || cod.Pricing == nil || cod.Pricing.InputPerMTokUSD != 0.3 {
		t.Fatalf("codestral = %+v", cod)
	}
	// The codestral fixture has vision:false and its declared family has no vision, so the
	// live signal must SUPPRESS CapVision — proving liveCapabilities gates on the boolean
	// rather than hard-coding the flag (mistral-large has it only because vision:true).
	if cod.HasCapability(modelprovider.CapVision) {
		t.Fatalf("codestral has live vision=false; CapVision must be suppressed (caps=%v)", cod.Capabilities)
	}

	// The deprecated 7B model carries the Deprecated flag from the live deprecation date.
	dep, _ := cat.FindModel("open-mistral-7b")
	if !dep.Deprecated {
		t.Fatal("open-mistral-7b should be marked Deprecated (live deprecation date present)")
	}

	// Fine-tuned card is included with no declared family → no pricing, but still live caps.
	ftID := "ft:open-mistral-7b:my-org:custom:abc123"
	ft, ok := cat.FindModel(ftID)
	if !ok {
		t.Fatalf("fine-tuned card %q not in catalog", ftID)
	}
	if ft.Pricing != nil {
		t.Fatalf("fine-tuned card must not carry a guessed price: %+v", ft.Pricing)
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
			t.Fatalf("offline model %s capability source = %q, want declared", m.Ref, m.CapabilitySource)
		}
	}
	// A declared chat model is priced from the list table.
	large, ok := cat.FindModel("mistral-large-latest")
	if !ok || large.Pricing == nil || large.Pricing.InputPerMTokUSD != 0.5 {
		t.Fatalf("offline mistral-large not priced: %+v", large)
	}
}

func TestGather_CoverageCaveatOnly(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil) // manage_inventory defaults false
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatal("Mistral Gather must emit no cost samples (no usage API; cost is via Meter)")
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectCoverage {
		t.Fatalf("want only the coverage caveat, got %+v", fs)
	}
	if fs[0].Severity != model.SeverityInfo || !strings.Contains(strings.ToLower(fs[0].Title), "dashboard-only") {
		t.Fatalf("coverage caveat = %+v", fs[0])
	}
	// The doer is never called when only the caveat is emitted (no inventory pull).
	if len(doer.reqs) != 0 {
		t.Fatalf("caveat-only gather issued %d requests, want 0", len(doer.reqs))
	}
}

func TestGather_InventoryPostureOptIn(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_inventory": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	// coverage caveat + 2 workspace inventory + 1 key rotation (key_old; key_new is < 90d).
	var caveat, ws, rotation int
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectCoverage:
			caveat++
		case subjectWorkspace:
			ws++
		case subjectAPIKey:
			rotation++
			if f.SubjectRef != "key_old" || f.Severity != model.SeverityMedium {
				t.Fatalf("rotation finding = %+v", f)
			}
			if !strings.Contains(f.Title, "rotation age") {
				t.Fatalf("rotation title = %q", f.Title)
			}
		}
	}
	if caveat != 1 || ws != 2 || rotation != 1 {
		t.Fatalf("findings = caveat %d, workspace %d, rotation %d; want 1/2/1 (%+v)", caveat, ws, rotation, fs)
	}
	// No finding leaks a key value (only the masked hint is ever held, and it stays hashed).
	for _, f := range fs {
		if strings.Contains(f.Title, "sk-") {
			t.Fatalf("finding title leaked a key hint: %q", f.Title)
		}
	}
}

func TestGather_InventoryUnavailableDegrades(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{defaultWorkspacesPath: true}}
	s := newSource(t, doer, map[string]string{"manage_inventory": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail on an unavailable inventory surface; got %v", err)
	}
	fs := sink.findings()
	var sawUnavail bool
	for _, f := range fs {
		if f.SubjectKind == subjectInventory {
			sawUnavail = true
			if f.Severity != model.SeverityMedium || !strings.Contains(f.Title, "unavailable") {
				t.Fatalf("unavailable finding = %+v", f)
			}
		}
	}
	if !sawUnavail {
		t.Fatalf("missing inventory-unavailable posture finding; got %+v", fs)
	}
}

func TestGather_OfflineNoCredentialEmitsNothing(t *testing.T) {
	s := New()
	s.doer = &fixtureDoer{t: t}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"manage_inventory": "true"}}); err != nil {
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

func TestGather_ReadOnlyBearerAuth(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_inventory": "true"})
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
		if r.Header.Get("Authorization") != "Bearer sk-mistral-test" {
			t.Fatalf("bearer credential not sent on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
	}
}

func TestMeter_PricingAndProvenance(t *testing.T) {
	s := New()
	s.now = fixedClock
	at := fixedClock()

	// mistral-large: input 2M * $0.5/M + output 1M * $1.5/M = $1.0 + $1.5 = $2.5.
	cs, ok := s.Meter("mistral-large-latest", 2_000_000, 1_000_000, at)
	if !ok || cs.CostMicroUSD != 2_500_000 {
		t.Fatalf("large meter = %d ok=%v, want 2500000 true", cs.CostMicroUSD, ok)
	}
	if cs.ProviderRef != modelprovider.ProviderMistral || cs.CostType != costTypeMistral {
		t.Fatalf("meter attribution = %+v", cs)
	}
	if cs.Provenance != model.ProvenanceEstimated {
		t.Fatalf("provenance = %q, want estimated (no billing API)", cs.Provenance)
	}
	if cs.InputTokens != 2_000_000 || cs.OutputTokens != 1_000_000 {
		t.Fatalf("tokens = in %d out %d", cs.InputTokens, cs.OutputTokens)
	}

	// Uncataloged model: recorded with cost 0, not priced (never a guessed price).
	cs, ok = s.Meter("some-unknown-model", 1_000_000, 1_000_000, at)
	if ok || cs.CostMicroUSD != 0 {
		t.Fatalf("unknown model must not be priced: %d ok=%v", cs.CostMicroUSD, ok)
	}
}

func TestFamilyFor_LongestPrefix(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"mistral-large-latest", 0.5, true},
		{"mistral-large-3-25-12", 0.5, true},
		{"ministral-8b-latest", 0.15, true},
		{"ministral-3b-latest", 0.1, true},
		{"codestral-latest", 0.3, true},
		{"codestral-embed", 0.15, true}, // more specific than "codestral"
		{"gpt-4o", 0, false},            // not a mistral model
	}
	for _, c := range cases {
		f, ok := familyFor(c.id)
		if ok != c.wantMatch {
			t.Fatalf("%s: match = %v, want %v", c.id, ok, c.wantMatch)
		}
		if ok && f.pricing.InputPerMTokUSD != c.wantIn {
			t.Fatalf("%s: input price = %v, want %v", c.id, f.pricing.InputPerMTokUSD, c.wantIn)
		}
	}
}

// pagingDoer synthesizes a cursor-paginated workspace list: page 1 carries has_more=true +
// a last_id, page 2 closes it. When infinite, every page returns has_more=true with a fresh
// last_id (to exercise the maxPages safety bound).
type pagingDoer struct {
	t        *testing.T
	reqs     []*http.Request
	infinite bool
}

func (d *pagingDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if req.URL.Path != defaultWorkspacesPath {
		d.t.Fatalf("unexpected path %q", req.URL.Path)
	}
	after := req.URL.Query().Get("after_id")
	if d.infinite {
		return resp(200, `{"data":[{"id":"w","name":"W","created_at":"2026-01-01T00:00:00Z"}],"has_more":true,"last_id":"w"}`), nil
	}
	if after == "" {
		return resp(200, `{"data":[{"id":"w1","name":"W1","created_at":"2026-01-01T00:00:00Z"}],"has_more":true,"last_id":"w2"}`), nil
	}
	return resp(200, `{"data":[{"id":"w2","name":"W2","created_at":"2026-02-01T00:00:00Z"}],"has_more":false,"last_id":"w2"}`), nil
}

func openWithDoer(t *testing.T, doer modelprovider.Doer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "sk-mistral-test"}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestListWorkspaces_PaginatesAcrossPages(t *testing.T) {
	doer := &pagingDoer{t: t}
	s := openWithDoer(t, doer, nil)
	ws, err := s.listWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("listWorkspaces: %v", err)
	}
	if len(ws) != 2 || ws[0].ID != "w1" || ws[1].ID != "w2" {
		t.Fatalf("paginated workspaces = %+v, want [w1 w2] from both pages", ws)
	}
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (one per page)", len(doer.reqs))
	}
	if got := doer.reqs[1].URL.Query().Get("after_id"); got != "w2" {
		t.Fatalf("page-2 cursor = %q, want w2", got)
	}
}

func TestListWorkspaces_MaxPagesBoundsRunawayCursor(t *testing.T) {
	doer := &pagingDoer{t: t, infinite: true}
	s := openWithDoer(t, doer, map[string]string{"max_pages": "2"})
	ws, err := s.listWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("listWorkspaces: %v", err)
	}
	// has_more never turns false; the loop must stop at max_pages (2), not run away.
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want exactly max_pages=2", len(doer.reqs))
	}
	if len(ws) != 2 {
		t.Fatalf("workspaces = %d, want 2 (one per bounded page)", len(ws))
	}
}
