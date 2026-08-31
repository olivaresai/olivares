// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package event_test

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestTypeForObservation(t *testing.T) {
	cases := []struct {
		obs  model.Observation
		want event.Type
	}{
		{model.EdgeObservation{}, event.TypeEdgeObserved},
		{model.CostSample{}, event.TypeCostSampled},
		{model.FindingReport{}, event.TypeFindingReported},
		{model.MetricSample{}, event.TypeMetricSampled},
	}
	for _, c := range cases {
		if got := event.TypeForObservation(c.obs); got != c.want {
			t.Errorf("TypeForObservation(%T) = %q, want %q", c.obs, got, c.want)
		}
	}
}

func TestFromObservationStampsTimeAndType(t *testing.T) {
	when := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	e := event.FromObservation("tenant-1", "src", model.EdgeObservation{
		OriginRef: "role-x", ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: when,
	})
	if e.Type != event.TypeEdgeObserved {
		t.Errorf("type = %q, want edge.observed", e.Type)
	}
	if e.Tenant != "tenant-1" || e.Source != "src" {
		t.Errorf("tenant/source not stamped: %+v", e)
	}
	if !e.Time.Equal(when) {
		t.Errorf("time = %v, want %v (taken from ObservedAt)", e.Time, when)
	}
}

func TestTypedHelpers(t *testing.T) {
	edge := model.EdgeObservation{ResourceRef: "public.customers", Mode: model.ModeWrite}
	e := event.FromObservation("t", "s", edge)

	got, ok := event.EdgeOf(e)
	if !ok || got.ResourceRef != "public.customers" || got.Mode != model.ModeWrite {
		t.Fatalf("EdgeOf failed: ok=%v got=%+v", ok, got)
	}
	// Wrong-type helpers return false rather than a zero-but-true.
	if _, ok := event.CostOf(e); ok {
		t.Error("CostOf on an edge event should be false")
	}
	if _, ok := event.FindingOf(e); ok {
		t.Error("FindingOf on an edge event should be false")
	}

	// A type/payload mismatch (hand-built malformed event) is rejected, not
	// returned as a zero value with ok=true.
	bad := event.Event{Type: event.TypeEdgeObserved, Payload: model.CostSample{}}
	if _, ok := event.EdgeOf(bad); ok {
		t.Error("EdgeOf must reject a mismatched payload")
	}
}

func TestMetricOf(t *testing.T) {
	when := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	in := model.MetricSample{
		Name: "claude_code.lines_of_code.count", Value: 42, Unit: "lines",
		SubjectKind: "developer", SubjectRef: "dev@example.com", OccurredAt: when,
		Dimensions: map[string]string{"type": "added", "model": "claude-opus-4-8"},
		Labels:     map[string]string{"team": "payments"},
	}
	e := event.FromObservation("t", "claude-api", in)
	if e.Type != event.TypeMetricSampled {
		t.Fatalf("type = %q, want metric.sampled", e.Type)
	}
	if !e.Time.Equal(when) {
		t.Errorf("time = %v, want %v (taken from OccurredAt)", e.Time, when)
	}
	got, ok := event.MetricOf(e)
	if !ok || got.Name != in.Name || got.Value != 42 || got.SubjectRef != "dev@example.com" {
		t.Fatalf("MetricOf failed: ok=%v got=%+v", ok, got)
	}
	if got.Dimensions["type"] != "added" || got.Labels["team"] != "payments" {
		t.Errorf("dimensions/labels lost: %+v", got)
	}
	// Wrong-type helpers return false on a metric event.
	if _, ok := event.CostOf(e); ok {
		t.Error("CostOf on a metric event should be false")
	}
	// A pointer payload normalizes (time + value survive).
	var o model.Observation = &model.MetricSample{Name: "claude_code.commit.count", Value: 3, OccurredAt: when}
	pe := event.FromObservation("t", "s", o)
	if g, ok := event.MetricOf(pe); !ok || g.Value != 3 || !pe.Time.Equal(when) {
		t.Errorf("MetricOf must normalize a pointer payload: ok=%v got=%+v time=%v", ok, g, pe.Time)
	}
}

// TestPointerObservationNormalized guards the regression where a *DTO (idiomatic
// Go for a large struct, and a valid Observation since the DTOs use value
// receivers) was silently dropped: time lost, EdgeOf returning false.
func TestPointerObservationNormalized(t *testing.T) {
	when := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	var o model.Observation = &model.EdgeObservation{ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: when}

	e := event.FromObservation("t", "s", o)
	if e.Type != event.TypeEdgeObserved {
		t.Errorf("type = %q, want edge.observed", e.Type)
	}
	if !e.Time.Equal(when) {
		t.Errorf("time = %v, want %v (must survive a pointer payload)", e.Time, when)
	}
	got, ok := event.EdgeOf(e)
	if !ok || got.ResourceRef != "public.t" || got.Mode != model.ModeRead {
		t.Errorf("EdgeOf failed for a normalized pointer payload: ok=%v got=%+v", ok, got)
	}

	// EdgeOf on a directly built event carrying a pointer payload also works.
	direct := event.Event{Type: event.TypeEdgeObserved, Payload: &model.EdgeObservation{ResourceRef: "x"}}
	if g, ok := event.EdgeOf(direct); !ok || g.ResourceRef != "x" {
		t.Errorf("EdgeOf must accept a pointer payload: ok=%v got=%+v", ok, g)
	}
}
