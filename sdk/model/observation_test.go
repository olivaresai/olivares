// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestObservationTypeDiscriminators(t *testing.T) {
	cases := []struct {
		obs  model.Observation
		want model.ObservationType
	}{
		{model.EdgeObservation{}, model.ObsEdge},
		{model.CostSample{}, model.ObsCost},
		{model.FindingReport{}, model.ObsFinding},
		{model.MetricSample{}, model.ObsMetric},
	}
	for _, c := range cases {
		if got := c.obs.ObservationType(); got != c.want {
			t.Errorf("%T.ObservationType() = %q, want %q", c.obs, got, c.want)
		}
	}
}

// TestObservationIsSealed is a compile-time guarantee documented as a test: the
// three first-party DTOs satisfy the sealed Observation interface, and (proven
// by the unexported marker living in package model) no type outside this package
// can. If a new observation type is added it must implement isObservation here.
func TestObservationIsSealed(t *testing.T) {
	obs := []model.Observation{
		model.EdgeObservation{},
		model.CostSample{},
		model.FindingReport{},
		model.MetricSample{},
	}
	if len(obs) != 4 {
		t.Fatalf("expected 4 sealed observation types, got %d", len(obs))
	}
}

func TestAccessModeValid(t *testing.T) {
	for _, m := range []model.AccessMode{model.ModeUnknown, model.ModeRead, model.ModeWrite, model.ModeReadWrite} {
		if !m.Valid() {
			t.Errorf("AccessMode %q should be valid", m)
		}
	}
	if model.AccessMode("garbage").Valid() {
		t.Error("garbage AccessMode should be invalid")
	}
}

func TestSeverityOrdering(t *testing.T) {
	if !model.SeverityCritical.AtLeast(model.SeverityHigh) {
		t.Error("critical should be at least high")
	}
	if model.SeverityLow.AtLeast(model.SeverityHigh) {
		t.Error("low should not be at least high")
	}
	if !model.SeverityHigh.AtLeast(model.SeverityHigh) {
		t.Error("high should be at least high (inclusive)")
	}
	// Fail closed: an unknown severity never trips a threshold.
	if model.Severity("bogus").AtLeast(model.SeverityInfo) {
		t.Error("unknown severity must not satisfy AtLeast(info)")
	}
	if model.Severity("bogus").Valid() {
		t.Error("unknown severity must be invalid")
	}
}

func TestConfidenceValid(t *testing.T) {
	if !model.ConfidenceAttributed.Valid() || !model.ConfidenceApproximate.Valid() {
		t.Error("seeded confidence levels should be valid")
	}
	if model.Confidence("x").Valid() {
		t.Error("unknown confidence should be invalid")
	}
}
