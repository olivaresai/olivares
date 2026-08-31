// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func signInEdges(t *testing.T, sink *collectSink) []model.EdgeObservation {
	t.Helper()
	out := make([]model.EdgeObservation, 0, len(sink.obs))
	for _, o := range sink.obs {
		e, ok := o.(model.EdgeObservation)
		if !ok {
			t.Fatalf("observation %T, want EdgeObservation", o)
		}
		out = append(out, e)
	}
	return out
}

func signInRequests(d *stubDoer) []recordedRequest {
	var out []recordedRequest
	for _, c := range d.calls {
		if strings.Contains(c.URL, "/beta/auditLogs/signIns") {
			out = append(out, c)
		}
	}
	return out
}

func runSignInGather(t *testing.T, fixture string, overrides map[string]string) (*stubDoer, []model.EdgeObservation) {
	t.Helper()
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.on(http.MethodGet, "/beta/auditLogs/signIns", d.fixture(fixture))

	settings := map[string]string{
		"ca_posture":         "false",
		"risk_posture":       "false",
		"governance_posture": "false",
		"ingest_signins":     "true",
	}
	for k, v := range overrides {
		settings[k] = v
	}
	s := openSource(t, d, settings)
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return d, signInEdges(t, sink)
}

func TestGatherSignInsDefaultOff(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)

	s := openSource(t, d, map[string]string{
		"ca_posture":         "false",
		"risk_posture":       "false",
		"governance_posture": "false",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if sawPath(d, "/beta/auditLogs/signIns") {
		t.Fatal("signIns must not be read unless ingest_signins=true")
	}
	if len(sink.obs) != 0 {
		t.Fatalf("observations = %d, want none", len(sink.obs))
	}
}

func TestGatherSignInsEdgesFilterAndHeaders(t *testing.T) {
	d, edges := runSignInGather(t, "signins_mixed.json", nil)
	if got := len(edges); got != 2 {
		t.Fatalf("edges = %d, want 2 successful attributable rows", got)
	}

	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "aid-1111",
			ResourceKind: resEntraApp, ResourceRef: "res-app-1",
			Mode: model.ModeUnknown, Source: signalEntraAgentSignIn,
			Confidence: model.ConfidenceAttributed,
			ObservedAt: time.Date(2026, 6, 11, 11, 30, 0, 0, time.UTC),
		},
		{
			OriginKind: "identity", OriginRef: "bpp-1",
			ResourceKind: resEntraApp, ResourceRef: "res-app-2",
			Mode: model.ModeUnknown, Source: signalEntraAgentSignIn,
			Confidence: model.ConfidenceApproximate,
			ObservedAt: time.Date(2026, 6, 11, 11, 31, 0, 0, time.UTC),
		},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges mismatch:\ngot:  %+v\nwant: %+v", edges, want)
	}

	reqs := signInRequests(d)
	if len(reqs) != 1 {
		t.Fatalf("signIns requests = %d, want 1", len(reqs))
	}
	if reqs[0].Prefer != "include-unknown-enum-members" {
		t.Fatalf("signIns Prefer = %q, want include-unknown-enum-members", reqs[0].Prefer)
	}
	u, err := url.Parse(reqs[0].URL)
	if err != nil {
		t.Fatalf("parse signIns URL: %v", err)
	}
	filter := u.Query().Get("$filter")
	if !strings.Contains(filter, defaultSignInFilter) {
		t.Fatalf("$filter = %q, want default literal %q", filter, defaultSignInFilter)
	}
	if !strings.Contains(filter, "createdDateTime ge 2026-06-10T12:00:00Z") {
		t.Fatalf("$filter = %q, want fake-clock lookback clause", filter)
	}
	for _, c := range d.calls {
		if !strings.Contains(c.URL, "/beta/auditLogs/signIns") && c.Prefer != "" {
			t.Fatalf("non-signIns request carried Prefer: %s %s", c.Method, c.URL)
		}
	}
}

func TestGatherSignInsPagingBounded(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.on(http.MethodGet, "/beta/auditLogs/signIns", d.fixture("signins_paged_1.json"))
	d.on(http.MethodGet, "/beta/auditLogs/signIns", d.fixture("signins_paged_2.json"))
	d.on(http.MethodGet, "/beta/auditLogs/signIns", `{"value":[{"id":"unread"}]}`)

	s := openSource(t, d, map[string]string{
		"ca_posture":         "false",
		"risk_posture":       "false",
		"governance_posture": "false",
		"ingest_signins":     "true",
		"max_pages":          "2",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got := len(signInEdges(t, sink)); got != 2 {
		t.Fatalf("edges = %d, want the first two pages only", got)
	}
	if got := len(signInRequests(d)); got != 2 {
		t.Fatalf("signIns pages fetched = %d, want max_pages=2", got)
	}
	if !sawPath(d, "$skiptoken=SIGNIN2") {
		t.Fatal("signIns nextLink page was not followed")
	}
	if sawPath(d, "$skiptoken=SIGNIN3") {
		t.Fatal("signIns pagination exceeded max_pages=2")
	}
}

func TestGatherSignIns403Tolerated(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.onStatus(http.MethodGet, "/beta/auditLogs/signIns", http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied"}}`)

	s := openSource(t, d, map[string]string{
		"ca_posture":         "false",
		"risk_posture":       "false",
		"governance_posture": "false",
		"ingest_signins":     "true",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must tolerate signIns 403: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("observations = %d, want none", len(sink.obs))
	}
}

func TestGatherSignInsDeterministic(t *testing.T) {
	first := func(t *testing.T) []model.EdgeObservation {
		t.Helper()
		_, edges := runSignInGather(t, "signins_mixed.json", nil)
		return edges
	}(t)
	second := func(t *testing.T) []model.EdgeObservation {
		t.Helper()
		_, edges := runSignInGather(t, "signins_mixed.json", nil)
		return edges
	}(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("sign-in edges are not deterministic:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}
