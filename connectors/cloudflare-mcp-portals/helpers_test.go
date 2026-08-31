// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"context"
	"sync"

	"github.com/olivaresai/olivares/sdk/model"
)

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
