// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"context"
	"sync"

	"github.com/olivaresai/olivares/sdk/model"
)

// fakeSink collects every emitted observation for assertion. It is concurrency-safe
// so a ctx-cancel test can race an in-flight Emit under -race.
type fakeSink struct {
	mu      sync.Mutex
	obs     []model.Observation
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

// testCreds are dummy AWS credentials used by every httptest-backed AWS test. They
// are syntactically valid for SigV4 (the test server never verifies the signature)
// but obviously not real, so a leak into an emitted ref is caught by the redaction
// assertions. The secret string also serves as the "must never appear" probe.
const testSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"

var awsTestCreds = map[string]string{
	cfgAccessKeyID:     "AKIDEXAMPLE",
	cfgSecretAccessKey: testSecret,
}

const gcpTestToken = "ya29.SECRET-GCP-ACCESS-TOKEN-VALUE-do-not-leak"
