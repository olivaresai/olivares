// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"sync"

	"github.com/olivaresai/olivares/sdk/model"
)

// --- fakeSink ----------------------------------------------------------------

// fakeSink collects every emitted observation for assertion. It mirrors the
// mcp helpers_test.go shape and is concurrency-safe so a ctx-cancel test can race
// against an in-flight Emit under -race.
type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
	// emitErr, when non-nil, is returned by Emit to exercise the fatal-error path.
	emitErr error
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.emitErr != nil {
		return f.emitErr
	}
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) edges() []model.EdgeObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range f.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) findings() []model.FindingReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.FindingReport
	for _, o := range f.obs {
		if r, ok := o.(model.FindingReport); ok {
			out = append(out, r)
		}
	}
	return out
}

// testCreds are dummy credentials used by every httptest-backed test. They are
// syntactically valid for SigV4 (the test server never verifies the signature)
// but obviously not real, so a leak into an emitted ref would be caught by the
// redaction assertions.
var testCreds = map[string]string{
	cfgAccessKeyID:     "AKIDEXAMPLE",
	cfgSecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

// edgeKey is the natural key a test uses to compare an emitted edge against the
// golden set without depending on per-pass timestamps.
type edgeKey struct {
	originKind string
	originRef  string
	resKind    string
	resRef     string
	mode       model.AccessMode
	source     model.SignalSource
	conf       model.Confidence
	toolRef    string
}

func keyOf(e model.EdgeObservation) edgeKey {
	return edgeKey{
		originKind: e.OriginKind,
		originRef:  e.OriginRef,
		resKind:    e.ResourceKind,
		resRef:     e.ResourceRef,
		mode:       e.Mode,
		source:     e.Source,
		conf:       e.Confidence,
		toolRef:    e.ToolRef,
	}
}
