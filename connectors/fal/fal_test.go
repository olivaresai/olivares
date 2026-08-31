// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fal

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

// fixtureDoer multiplexes by request path to the recorded fal fixtures. It records
// every request (read-only assertion) and can return 403/404 for the key-management
// path (the sales-gated/UNVERIFIED degrade test).
type fixtureDoer struct {
	t           *testing.T
	reqs        []*http.Request
	keysUnavail bool
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	path := req.URL.Path
	switch {
	case path == defaultKeysPath:
		if d.keysUnavail {
			return resp(404, `{"error":"not_found"}`), nil
		}
		return d.file("keys.json"), nil
	case strings.HasSuffix(path, "/status"):
		switch {
		case strings.Contains(path, "req_done"):
			return d.file("queue_status_completed.json"), nil
		case strings.Contains(path, "req_pending"):
			return d.file("queue_status_pending.json"), nil
		case strings.Contains(path, "req_missing"):
			return resp(404, `{"error":"not_found"}`), nil
		}
	}
	d.t.Fatalf("unexpected request path %q", path)
	return nil, nil
}

func (d *fixtureDoer) file(name string) *http.Response {
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", name, err)
	}
	return resp(200, string(body))
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

func fixedClock() time.Time { return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "kid:secret"}
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

func TestGather_KeyRotationPostureAndCaveat(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil) // manage_keys defaults true
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	// key_old (>90d) -> 1 rotation finding; key_new (1d) -> none; + 1 sales-gated caveat.
	if len(fs) != 2 {
		t.Fatalf("findings = %d, want 2 (rotation + caveat)", len(fs))
	}
	var sawRotation, sawCaveat bool
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectAPIKey:
			sawRotation = true
			if f.SubjectRef != "key_old" || f.Severity != model.SeverityMedium {
				t.Fatalf("rotation finding = %+v", f)
			}
			if !strings.Contains(f.Title, "rotation age") {
				t.Fatalf("rotation title = %q", f.Title)
			}
		case subjectGovernance:
			sawCaveat = true
			if f.Severity != model.SeverityInfo || !strings.Contains(strings.ToLower(f.Title), "sales-gated") {
				t.Fatalf("caveat finding = %+v", f)
			}
		}
	}
	if !sawRotation || !sawCaveat {
		t.Fatalf("missing finding: rotation=%v caveat=%v", sawRotation, sawCaveat)
	}
}

func TestGather_CaveatEmittedWithoutKeyManagement(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_keys": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectGovernance {
		t.Fatalf("want only the sales-gated caveat, got %+v", fs)
	}
}

func TestGather_KeyMgmtUnavailableDegrades(t *testing.T) {
	doer := &fixtureDoer{t: t, keysUnavail: true}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail on a sales-gated key surface; got %v", err)
	}
	fs := sink.findings()
	// key-management unavailable posture + the sales-gated caveat.
	if len(fs) != 2 {
		t.Fatalf("findings = %d, want 2", len(fs))
	}
	var sawUnavail bool
	for _, f := range fs {
		if f.SubjectKind == subjectKeySurface {
			sawUnavail = true
			if f.Severity != model.SeverityMedium || !strings.Contains(f.Title, "unavailable") {
				t.Fatalf("unavailable finding = %+v", f)
			}
		}
	}
	if !sawUnavail {
		t.Fatal("missing key-management unavailable posture finding")
	}
}

func TestGather_MeteredQueueRequests(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{
		"manage_keys": "false",
		"model":       "fal-ai/fast-sdxl",
		"request_ids": "req_done,req_pending,req_missing",
	})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	// Only the COMPLETED request meters; pending + missing are skipped.
	if len(costs) != 1 {
		t.Fatalf("metered cost samples = %d, want 1", len(costs))
	}
	c := costs[0]
	if c.ProviderRef != modelprovider.ProviderFal {
		t.Fatalf("provider = %q, want fal", c.ProviderRef)
	}
	// fast-sdxl is second-billed at $0.005/s; inference_time 2.0s -> $0.01 = 10000 micro.
	if c.CostMicroUSD != 10_000 {
		t.Fatalf("cost = %d micro-USD, want 10000", c.CostMicroUSD)
	}
	if c.CostType != unitSecond {
		t.Fatalf("cost_type = %q, want second", c.CostType)
	}
	if c.Provenance != model.ProvenanceEstimated {
		t.Fatalf("provenance = %q, want estimated", c.Provenance)
	}
	if c.SessionRef != "req_done" {
		t.Fatalf("session ref = %q, want req_done (queue request id)", c.SessionRef)
	}
}

func TestGather_OfflineNoCredentialEmitsNothing(t *testing.T) {
	s := New()
	s.doer = &fixtureDoer{t: t}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
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

func TestGather_ReadOnlyFalKeyAuth(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"model": "fal-ai/fast-sdxl", "request_ids": "req_done"})
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
		if r.Header.Get("Authorization") != "Key kid:secret" {
			t.Fatalf("fal Key credential not sent on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
	}
}

func TestSnapshot_Catalog(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderFal || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != len(falModels) {
		t.Fatalf("models = %d, want %d", len(cat.Models), len(falModels))
	}
	for _, m := range cat.Models {
		if m.Pricing != nil {
			t.Fatalf("fal model %s carries token pricing (should be nil — fal is pay-per-output)", m.Ref)
		}
		if m.CapabilitySource != "declared" {
			t.Fatalf("model %s capability source = %q", m.Ref, m.CapabilitySource)
		}
	}
	// Key inventory: masked partial only, never a secret.
	if len(cat.Keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(cat.Keys))
	}
	for _, k := range cat.Keys {
		if k.Hint == "" || !strings.Contains(k.Hint, "...") {
			t.Fatalf("key hint looks unmasked: %q", k.Hint)
		}
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
	if len(cat.Models) != len(falModels) {
		t.Fatalf("offline models = %d, want %d", len(cat.Models), len(falModels))
	}
	if len(cat.Keys) != 0 {
		t.Fatal("offline catalog must not contain key inventory")
	}
}

func TestMeter_UnitLogic(t *testing.T) {
	s := New()
	s.now = fixedClock
	at := fixedClock()

	// Second-billed model: cost = inference_time * rate.
	cs, ok := s.Meter("fal-ai/fast-sdxl", 4.0, 0, at)
	if !ok || cs.CostMicroUSD != 20_000 || cs.CostType != unitSecond { // 4*0.005=$0.02
		t.Fatalf("second-billed meter = %+v ok=%v", cs, ok)
	}
	// Per-image model: cost = outputs * rate (inference_time ignored).
	cs, ok = s.Meter("fal-ai/flux/schnell", 9.9, 3, at)
	if !ok || cs.CostMicroUSD != 9_000 || cs.CostType != unitImage { // 3*0.003=$0.009
		t.Fatalf("per-image meter = %+v ok=%v", cs, ok)
	}
	// Unknown model, no fallback: cost 0, not priced.
	cs, ok = s.Meter("fal-ai/unknown", 5.0, 2, at)
	if ok || cs.CostMicroUSD != 0 {
		t.Fatalf("unknown model must not be priced: %+v ok=%v", cs, ok)
	}
	// Unknown model WITH operator fallback: meters compute-seconds.
	s.fallbackPerSec = 0.01
	cs, ok = s.Meter("fal-ai/unknown", 5.0, 2, at)
	if !ok || cs.CostMicroUSD != 50_000 { // 5*0.01=$0.05
		t.Fatalf("fallback meter = %+v ok=%v", cs, ok)
	}
}

func TestFalPricingFor_LongestPrefix(t *testing.T) {
	cases := []struct {
		id        string
		wantUnit  string
		wantMatch bool
	}{
		{"fal-ai/flux/schnell", unitImage, true},
		{"fal-ai/flux/dev", unitMegapixel, true},
		{"fal-ai/fast-sdxl", unitSecond, true},
		{"fal-ai/whisper", unitAudioMin, true},
		{"fal-ai/does-not-exist", "", false},
	}
	for _, c := range cases {
		unit, _, ok := falPricingFor(c.id)
		if ok != c.wantMatch {
			t.Fatalf("%s: match = %v, want %v", c.id, ok, c.wantMatch)
		}
		if ok && unit != c.wantUnit {
			t.Fatalf("%s: unit = %q, want %q", c.id, unit, c.wantUnit)
		}
	}
}
