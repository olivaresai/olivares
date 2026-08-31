// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"context"
	"sync"

	"github.com/olivaresai/olivares/sdk/model"
)

// fakeSink collects every emitted observation for assertion. It is concurrency-safe so a
// ctx-cancel test can race against an in-flight Emit under -race.
type fakeSink struct {
	mu      sync.Mutex
	obs     []model.Observation
	emitErr error // when non-nil, returned by Emit to exercise the fatal-error path
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

func (f *fakeSink) costs() []model.CostSample {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.CostSample
	for _, o := range f.obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
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

// postureFindings filters to the safety_posture findings (drops health findings).
func postureFindings(fs []model.FindingReport) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range fs {
		if f.Kind == safetyPostureKind {
			out = append(out, f)
		}
	}
	return out
}

func findingBySubject(fs []model.FindingReport, subjectKind string) (model.FindingReport, bool) {
	for _, f := range fs {
		if f.SubjectKind == subjectKind {
			return f, true
		}
	}
	return model.FindingReport{}, false
}

// testCreds are dummy SigV4 credentials for httptest-backed tests (the test server never
// verifies the signature). Obviously not real, so a leak into an emitted ref/sample would
// be caught by the redaction assertions.
var testCreds = map[string]string{
	cfgAccessKeyID:     "AKIDEXAMPLE",
	cfgSecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}
