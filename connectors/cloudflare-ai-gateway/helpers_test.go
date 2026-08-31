// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfaigateway

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
