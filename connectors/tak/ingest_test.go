// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

var fixedAt = time.Date(2021, 6, 1, 12, 0, 0, 0, time.UTC)

func newSource(t *testing.T, settings map[string]string) *Source {
	t.Helper()
	s := New()
	cfg, err := loadConfig(sdk.Config{Settings: settings})
	if err != nil {
		t.Fatalf("loadConfig(%v): %v", settings, err)
	}
	s.cfg = cfg
	return s
}

func mustParse(t *testing.T, raw string) Event {
	t.Helper()
	ev, err := ParseEvent([]byte(raw), Limits{})
	if err != nil {
		t.Fatalf("ParseEvent: %v\ninput: %s", err, raw)
	}
	return ev
}

func splitObs(obs []model.Observation) (edges []model.EdgeObservation, findings []model.FindingReport) {
	for _, o := range obs {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges = append(edges, v)
		case model.FindingReport:
			findings = append(findings, v)
		}
	}
	return
}

// A plain, well-formed event: not drop-track, not unbounded, no detail.
func plainEvent(uid string) string {
	e := with(defEventAttrs(), "uid", uid)
	return render(e, defPointAttrs(), "")
}

func TestObserveEmitsWriteEdgeWithHashedUID(t *testing.T) {
	s := newSource(t, map[string]string{cfgFeedRef: "test-feed"})
	ev := mustParse(t, plainEvent("ANDROID-writer"))

	// The receipt clock must be distinguishable from the emitter's asserted time,
	// otherwise the ObservedAt assertion below cannot tell which one was used.
	receivedAt := fixedAt.Add(90 * time.Second)
	if receivedAt.Equal(ev.Time) {
		t.Fatalf("test setup: receivedAt must differ from ev.Time")
	}

	edges, findings := splitObs(s.observe(ev, transportTCP, receivedAt))
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %d, want 0 for a plain event", len(findings))
	}
	e := edges[0]
	if e.OriginKind != "identity" {
		t.Errorf("OriginKind = %q, want identity", e.OriginKind)
	}
	if e.Mode != model.ModeWrite {
		t.Errorf("Mode = %q, want write", e.Mode)
	}
	if e.Source != model.SignalCoT {
		t.Errorf("Source = %q, want cot", e.Source)
	}
	if e.Confidence != model.ConfidenceApproximate {
		t.Errorf("Confidence = %q, want approximate (base CoT is unauthenticated)", e.Confidence)
	}
	if e.ResourceKind != "tak.cot.feed" {
		t.Errorf("ResourceKind = %q, want tak.cot.feed", e.ResourceKind)
	}
	if e.ResourceRef != "test-feed" {
		t.Errorf("ResourceRef = %q, want test-feed (the feed_ref)", e.ResourceRef)
	}
	if e.ToolRef != transportTCP {
		t.Errorf("ToolRef = %q, want %q (the transport)", e.ToolRef, transportTCP)
	}
	// ObservedAt is the connector's RECEIPT clock, never the emitter's asserted
	// `time`. Base CoT is unauthenticated: sourcing the dedup/ordering key from the
	// event would let any peer that can reach the listener choose it.
	if !e.ObservedAt.Equal(receivedAt) {
		t.Errorf("ObservedAt = %v, want the connector receipt clock %v", e.ObservedAt, receivedAt)
	}
	if e.ObservedAt.Equal(ev.Time) {
		t.Errorf("ObservedAt must not be the emitter-supplied ev.Time (%v)", ev.Time)
	}
	// Default mode hashes: the raw uid must never appear on the access map.
	if e.OriginRef == "ANDROID-writer" {
		t.Errorf("OriginRef leaked the raw uid: %q", e.OriginRef)
	}
	if !strings.HasPrefix(e.OriginRef, "cot-uid:") {
		t.Errorf("OriginRef = %q, want cot-uid: prefix", e.OriginRef)
	}
	if lbl := e.Labels["cot_type"]; lbl != ev.Type {
		t.Errorf("Labels[cot_type] = %q, want %q", lbl, ev.Type)
	}
	if lbl := e.Labels["cot_affiliation"]; lbl != "h" {
		t.Errorf("Labels[cot_affiliation] = %q, want h", lbl)
	}
}

// TestObserveNeverLeaksCoordinates is the critical privacy test: a coordinate is a
// person's location and a <detail> is free-form payload; neither may cross the
// connector boundary. It marshals every observation the connector emits for an
// event that is simultaneously drop-track, unbounded and detail-bearing (so all
// three observation shapes are exercised) and asserts the serialized bytes contain
// none of the fixture's coordinates or detail text.
func TestObserveNeverLeaksCoordinates(t *testing.T) {
	s := newSource(t, map[string]string{cfgFeedRef: "test-feed"})

	e := defEventAttrs()
	e = with(e, "uid", "ANDROID-secret")
	e = with(e, "type", "a-h-G-E-V")
	e = with(e, "stale", "2021-06-01T11:00:00Z") // stale before start -> drop track
	p := defPointAttrs()
	p = with(p, "lat", "30.0090")
	p = with(p, "lon", "-85.9080")
	p = with(p, "hae", "-42.6")
	p = with(p, "ce", "9999999") // unbounded
	raw := render(e, p, `<detail><contact callsign="RAVEN-SECRET-CALLSIGN"/><remarks>CLASSIFIED-REMARKS-TEXT</remarks></detail>`)

	ev := mustParse(t, raw)
	if !ev.IsDropTrack() || !ev.Point.CEUnbounded() || !ev.HasDetail {
		t.Fatalf("test fixture is wrong: drop=%v unbounded=%v detail=%v", ev.IsDropTrack(), ev.Point.CEUnbounded(), ev.HasDetail)
	}

	obs := s.observe(ev, transportUDP, fixedAt)
	edges, findings := splitObs(obs)
	if len(edges) != 1 || len(findings) != 2 {
		t.Fatalf("want 1 edge + 2 findings, got %d edges + %d findings", len(edges), len(findings))
	}

	forbidden := []string{
		"30.0090", "30.009", // lat, raw and float forms
		"-85.9080", "-85.908", "85.908", // lon
		"-42.6",                   // hae
		"RAVEN-SECRET-CALLSIGN",   // <detail> callsign
		"CLASSIFIED-REMARKS-TEXT", // <detail> remarks
	}
	for i, o := range obs {
		blob, err := json.Marshal(o)
		if err != nil {
			t.Fatalf("marshal obs[%d]: %v", i, err)
		}
		s := string(blob)
		for _, bad := range forbidden {
			if strings.Contains(s, bad) {
				t.Errorf("obs[%d] (%T) leaked %q: %s", i, o, bad, s)
			}
		}
	}
}

func TestObserveRawUIDMode(t *testing.T) {
	s := newSource(t, map[string]string{cfgCoTUIDMode: uidModeRaw})
	ev := mustParse(t, plainEvent("ANDROID-raw-1"))
	edges, _ := splitObs(s.observe(ev, transportTCP, fixedAt))
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].OriginRef != "ANDROID-raw-1" {
		t.Errorf("raw mode OriginRef = %q, want the raw uid ANDROID-raw-1", edges[0].OriginRef)
	}
}

func TestOriginRefHashIsStableAndDomainSeparated(t *testing.T) {
	const uid = "ANDROID-stable-uid"
	s1 := newSource(t, nil)
	s2 := newSource(t, nil)

	r1 := s1.originRef(uid)
	r2 := s2.originRef(uid)
	if r1 != r2 {
		t.Fatalf("hashed originRef is not stable across sources: %q vs %q", r1, r2)
	}
	if !strings.HasPrefix(r1, "cot-uid:") {
		t.Fatalf("originRef = %q, want cot-uid: prefix", r1)
	}

	// The domain-separated hex must NOT equal a bare sha256(uid) hex prefix; if it
	// did, the uid would be rainbow-tableable against digests taken elsewhere.
	bare := sha256.Sum256([]byte(uid))
	bareHex := hex.EncodeToString(bare[:])
	got := strings.TrimPrefix(r1, "cot-uid:")
	if got == bareHex[:len(got)] {
		t.Errorf("originRef hex %q equals bare sha256(uid) prefix — domain prefix not applied", got)
	}
}

func TestObserveDropTrackEmitsFinding(t *testing.T) {
	s := newSource(t, map[string]string{cfgFeedRef: "test-feed"})
	ev := mustParse(t, string(readFixtureBytes(t, "droptrack.xml")))
	if !ev.IsDropTrack() {
		t.Fatal("fixture is not a drop track")
	}
	_, findings := splitObs(s.observe(ev, transportTCP, fixedAt))
	f := findByKind(t, findings, findingCoTDropTrack)
	if f.Severity != model.SeverityInfo {
		t.Errorf("drop-track Severity = %q, want info", f.Severity)
	}
	if f.SubjectKind != subjectKindFeed {
		t.Errorf("SubjectKind = %q, want %q", f.SubjectKind, subjectKindFeed)
	}
	if f.SubjectRef != "test-feed" {
		t.Errorf("SubjectRef = %q, want test-feed", f.SubjectRef)
	}
	if f.OccurredAt != fixedAt {
		t.Errorf("OccurredAt = %v, want %v (connector clock, not emitter clock)", f.OccurredAt, fixedAt)
	}
}

func TestObserveUnboundedErrorEmitsFinding(t *testing.T) {
	s := newSource(t, map[string]string{cfgFeedRef: "test-feed"})
	p := with(defPointAttrs(), "ce", "9999999")
	ev := mustParse(t, render(defEventAttrs(), p, ""))
	if !ev.Point.CEUnbounded() {
		t.Fatal("fixture is not unbounded")
	}
	if ev.IsDropTrack() {
		t.Fatal("fixture must not also be a drop track for this test")
	}
	_, findings := splitObs(s.observe(ev, transportTCP, fixedAt))
	f := findByKind(t, findings, findingCoTUnboundedError)
	if f.Severity != model.SeverityLow {
		t.Errorf("unbounded Severity = %q, want low", f.Severity)
	}
}

func TestRejectionFindingClassifies(t *testing.T) {
	s := newSource(t, map[string]string{cfgFeedRef: "test-feed"})
	cases := []struct {
		reason   string
		wantKind string
		wantSev  model.Severity
	}{
		{reasonRateLimited, findingCoTRateLimited, model.SeverityMedium},
		{reasonOversize, findingCoTRejected, model.SeverityLow},
		{reasonMalformed, findingCoTRejected, model.SeverityLow},
		{reasonConnLimit, findingCoTRejected, model.SeverityLow},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			f := s.rejectionFinding(tc.reason, transportUDP, 7, fixedAt)
			if f.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", f.Kind, tc.wantKind)
			}
			if f.Severity != tc.wantSev {
				t.Errorf("Severity = %q, want %q", f.Severity, tc.wantSev)
			}
			if f.SubjectRef != "test-feed" {
				t.Errorf("SubjectRef = %q, want test-feed", f.SubjectRef)
			}
			if f.DetailHash == "" {
				t.Error("DetailHash empty")
			}
		})
	}
}

// --- small helpers ----------------------------------------------------------

func readFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	return readFixture(t, name)
}

func findByKind(t *testing.T, findings []model.FindingReport, kind string) model.FindingReport {
	t.Helper()
	for _, f := range findings {
		if f.Kind == kind {
			return f
		}
	}
	t.Fatalf("no finding of kind %q among %d findings", kind, len(findings))
	return model.FindingReport{}
}
