// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestParseUSDToMicros(t *testing.T) {
	cases := map[string]int64{
		"12.4831200000": 12_483_120,
		"0.8200000000":  820_000,
		"31.0050000000": 31_005_000,
		"1":             1_000_000,
		"0":             0,
		"2.5":           2_500_000,
		"0.0000005":     1, // round half-up on the 7th fractional digit
		"0.0000004":     0,
		"0.000001":      1,
		"-2.500000":     -2_500_000, // credit/refund (negative UnblendedCost)
		"-0.000001":     -1,
	}
	for in, want := range cases {
		got, err := parseUSDToMicros(in)
		if err != nil {
			t.Errorf("parseUSDToMicros(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseUSDToMicros(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseUSDToMicros("not-a-number"); err == nil {
		t.Error("parseUSDToMicros(non-numeric) should error, not silently coerce")
	}
	if _, err := parseUSDToMicros(""); err == nil {
		t.Error("parseUSDToMicros(empty) should error")
	}
	// A pathologically large integer part must error, not silently overflow int64.
	if _, err := parseUSDToMicros("100000000000000.0"); err == nil {
		t.Error("parseUSDToMicros(out-of-range) should error, not wrap")
	}
}

// ceFixture serves GetCostAndUsage pages. When loop is true it ALWAYS returns a page
// with a NextPageToken (to exercise the ceMaxPages bound).
type ceFixture struct {
	pages   []string
	loop    bool
	calls   int
	methods []string
	targets []string
	auths   []string
}

func (f *ceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.methods = append(f.methods, r.Method)
	f.targets = append(f.targets, r.Header.Get("X-Amz-Target"))
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	i := f.calls
	f.calls++
	w.Header().Set("Content-Type", contentTypeAWSJSON)
	if f.loop {
		_, _ = w.Write([]byte(`{"ResultsByTime":[{"TimePeriod":{"Start":"2026-05-01","End":"2026-05-02"},"Groups":[{"Keys":["USE1-X-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"1.000000","Unit":"USD"}}}]}],"NextPageToken":"always-more"}`))
		return
	}
	if i < len(f.pages) {
		_, _ = w.Write([]byte(f.pages[i]))
		return
	}
	_, _ = w.Write([]byte(`{"ResultsByTime":[]}`))
}

func newCostSource(t *testing.T, srvURL string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgEnableCost:   "true",
		cfgCostEndpoint: srvURL,
		cfgRegion:       "eu-west-1", // not us-east-1: proves CE is still signed/called us-east-1
		cfgAccountID:    "123456789012",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestCost_BilledSamples proves the connector emits billed CostSamples per (day,
// usage-type) from Cost Explorer — cost parsed from the decimal string, Provenance=billed,
// tokens=0, ModelRef=usage-type — and SKIPS a zero-cost row (never a fabricated billed $0).
func TestCost_BilledSamples(t *testing.T) {
	page := `{
		"GroupDefinitions":[{"Key":"USAGE_TYPE","Type":"DIMENSION"}],
		"ResultsByTime":[
			{"Estimated":false,"TimePeriod":{"Start":"2026-05-01","End":"2026-05-02"},"Total":{},"Groups":[
				{"Keys":["USE1-Claude4.6Sonnet-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"12.4831200000","Unit":"USD"}}},
				{"Keys":["USE1-Claude4.6Sonnet-output-tokens"],"Metrics":{"UnblendedCost":{"Amount":"31.0050000000","Unit":"USD"}}},
				{"Keys":["USE1-Nova2.0Lite-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"0.0000000000","Unit":"USD"}}}
			]}
		]
	}`
	fx := &ceFixture{pages: []string{page}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	cs := sink.costs()
	if len(cs) != 2 {
		t.Fatalf("emitted %d billed samples, want 2 (input + output; zero-cost row skipped): %+v", len(cs), cs)
	}
	for _, c := range cs {
		if c.Provenance != model.ProvenanceBilled {
			t.Errorf("provenance = %q, want billed", c.Provenance)
		}
		if c.ProviderRef != ProviderBedrock {
			t.Errorf("provider = %q, want bedrock", c.ProviderRef)
		}
		if c.CostMicroUSD <= 0 {
			t.Errorf("billed cost must be > 0, got %d", c.CostMicroUSD)
		}
		if c.InputTokens != 0 || c.OutputTokens != 0 {
			t.Errorf("billed cost sample must carry 0 tokens (tokens come from the usage stream), got %d/%d", c.InputTokens, c.OutputTokens)
		}
		if c.OccurredAt.Year() != 2026 || c.OccurredAt.Month() != 5 || c.OccurredAt.Day() != 1 {
			t.Errorf("bucket day = %v, want 2026-05-01", c.OccurredAt)
		}
	}
	byModel := costByModel(cs)
	if in, ok := byModel["USE1-Claude4.6Sonnet-input-tokens"]; !ok || in.CostMicroUSD != 12_483_120 {
		t.Errorf("input-tokens sample = %+v ok=%v, want cost 12483120", in, ok)
	}

	// Read-only AWS-JSON POST with the right target.
	for _, m := range fx.methods {
		if m != http.MethodPost {
			t.Fatalf("non-POST Cost Explorer request: %s", m)
		}
	}
	if fx.targets[0] != ceTarget {
		t.Fatalf("X-Amz-Target = %q, want %q", fx.targets[0], ceTarget)
	}
	// Cost Explorer is GLOBAL: it must be signed for us-east-1 even though the connector's
	// operating region is eu-west-1 (newCostSource). Pin that invariant on the wire.
	if !strings.Contains(fx.auths[0], "/us-east-1/ce/aws4_request") {
		t.Fatalf("CE must be signed for us-east-1/ce, got Authorization scope %q", fx.auths[0])
	}
}

// TestCost_EstimatedProvenance proves a not-yet-finalized period (AWS Estimated=true) is
// emitted as Provenance=estimated (preliminary, not reconcilable), while a finalized
// period is billed — the connector never labels a mutable preliminary figure as billed.
func TestCost_EstimatedProvenance(t *testing.T) {
	page := `{"ResultsByTime":[
		{"Estimated":false,"TimePeriod":{"Start":"2026-05-01","End":"2026-05-02"},"Groups":[
			{"Keys":["USE1-A-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"10.000000","Unit":"USD"}}}]},
		{"Estimated":true,"TimePeriod":{"Start":"2026-05-31","End":"2026-06-01"},"Groups":[
			{"Keys":["USE1-A-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"3.000000","Unit":"USD"}}}]}
	]}`
	fx := &ceFixture{pages: []string{page}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	cs := sink.costs()
	if len(cs) != 2 {
		t.Fatalf("want 2 samples (finalized + preliminary), got %d", len(cs))
	}
	var billed, estimated int
	for _, c := range cs {
		switch c.Provenance {
		case model.ProvenanceBilled:
			billed++
		case model.ProvenanceEstimated:
			estimated++
		default:
			t.Fatalf("unexpected provenance %q", c.Provenance)
		}
	}
	if billed != 1 || estimated != 1 {
		t.Fatalf("want 1 billed + 1 estimated, got %d billed %d estimated", billed, estimated)
	}
}

// TestCost_NegativeCreditKept proves a negative UnblendedCost (a credit/refund — a real
// billed line) is emitted with a negative CostMicroUSD so net spend reconciles, while an
// exactly-zero line is still skipped.
func TestCost_NegativeCreditKept(t *testing.T) {
	page := `{"ResultsByTime":[{"Estimated":false,"TimePeriod":{"Start":"2026-05-01","End":"2026-05-02"},"Groups":[
		{"Keys":["USE1-A-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"10.000000","Unit":"USD"}}},
		{"Keys":["Credit"],"Metrics":{"UnblendedCost":{"Amount":"-2.500000","Unit":"USD"}}},
		{"Keys":["USE1-Z-zero"],"Metrics":{"UnblendedCost":{"Amount":"0.000000","Unit":"USD"}}}
	]}]}`
	fx := &ceFixture{pages: []string{page}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	cs := sink.costs()
	if len(cs) != 2 {
		t.Fatalf("want 2 samples (charge + credit; zero skipped), got %d: %+v", len(cs), cs)
	}
	byModel := costByModel(cs)
	if credit, ok := byModel["Credit"]; !ok || credit.CostMicroUSD != -2_500_000 {
		t.Fatalf("credit sample = %+v ok=%v, want -2500000", credit, ok)
	}
}

// TestCost_MaxPagesPartial proves that exhausting ceMaxPages with a pending cursor emits
// an honest Low partial-coverage finding (no silent caps) and respects the page bound.
func TestCost_MaxPagesPartial(t *testing.T) {
	fx := &ceFixture{loop: true} // every page carries a NextPageToken
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fx.calls != ceMaxPages {
		t.Fatalf("made %d calls, want ceMaxPages=%d (bound respected)", fx.calls, ceMaxPages)
	}
	partial := false
	for _, f := range postureFindings(sink.findings()) {
		if f.Severity == model.SeverityLow && f.SubjectKind == subjectCost && strings.Contains(f.Title, "PARTIAL") {
			partial = true
		}
	}
	if !partial {
		t.Fatalf("expected a Low partial-coverage cost finding at the ceMaxPages bound, got %+v", sink.findings())
	}
}

// TestCost_NoBilledRow proves an empty Cost Explorer result emits NO sample (cost is
// never fabricated; absence ≠ zero).
func TestCost_NoBilledRow(t *testing.T) {
	fx := &ceFixture{pages: []string{`{"ResultsByTime":[{"Estimated":false,"TimePeriod":{"Start":"2026-05-01","End":"2026-05-02"},"Total":{},"Groups":[]}]}`}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatalf("no billed row must emit no cost sample, got %+v", sink.costs())
	}
}

// TestCost_Pagination proves NextPageToken drives a second call and both pages' samples
// are emitted.
func TestCost_Pagination(t *testing.T) {
	fx := &ceFixture{pages: []string{
		`{"ResultsByTime":[{"TimePeriod":{"Start":"2026-05-01","End":"2026-05-02"},"Groups":[{"Keys":["USE1-A-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"1.000000","Unit":"USD"}}}]}],"NextPageToken":"p2"}`,
		`{"ResultsByTime":[{"TimePeriod":{"Start":"2026-05-02","End":"2026-05-03"},"Groups":[{"Keys":["USE1-B-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"2.000000","Unit":"USD"}}}]}]}`,
	}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fx.calls != 2 {
		t.Fatalf("made %d GetCostAndUsage calls, want 2 (NextPageToken)", fx.calls)
	}
	if len(sink.costs()) != 2 {
		t.Fatalf("emitted %d samples across 2 pages, want 2: %+v", len(sink.costs()), sink.costs())
	}
}

// TestCost_ReadFailure proves a Cost Explorer failure yields one health finding, never a
// fabricated cost.
func TestCost_ReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	s := newCostSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatal("a read failure must not fabricate a cost sample")
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].Kind != "health" || fs[0].SubjectKind != subjectCost {
		t.Fatalf("expected one cost health finding, got %+v", fs)
	}
}
