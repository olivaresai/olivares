// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// testTime is the fixed observation clock used across tests (deterministic findings/edges).
var testTime = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// fakeSink captures emitted observations. failAt>0 makes the (failAt+1)-th Emit fail, to
// exercise the connector's backpressure/abort handling.
type fakeSink struct {
	mu     sync.Mutex
	obs    []model.Observation
	failAt int
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAt > 0 && len(f.obs) >= f.failAt {
		return errors.New("sink unavailable")
	}
	f.obs = append(f.obs, o)
	return nil
}

var _ sdk.Sink = (*fakeSink)(nil)

func (f *fakeSink) all() []model.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Observation(nil), f.obs...)
}

func (f *fakeSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range f.all() {
		if fr, ok := o.(model.FindingReport); ok {
			out = append(out, fr)
		}
	}
	return out
}

func (f *fakeSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range f.all() {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range f.all() {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

// findingByKind returns the first finding whose SubjectKind matches, or false.
func (f *fakeSink) findingBySubjectKind(kind string) (model.FindingReport, bool) {
	for _, fr := range f.findings() {
		if fr.SubjectKind == kind {
			return fr, true
		}
	}
	return model.FindingReport{}, false
}

// openTestSource builds a Source from a settings map with an injected fixed clock and opens
// it. base sets the API base_url (e.g. an httptest server). It fails the test on a config
// error.
func openTestSource(t *testing.T, settings map[string]string) *Source {
	t.Helper()
	s := &Source{now: fixedClock(testTime)}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}
