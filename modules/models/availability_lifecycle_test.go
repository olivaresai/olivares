// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type blockingAvailabilitySource struct {
	calls   atomic.Int32
	started chan struct{}
}

func (s *blockingAvailabilitySource) Sources(context.Context, model.TenantID) ([]AvailabilitySource, error) {
	return []AvailabilitySource{{Ref: "login:codex", ProviderKind: "openai", Driver: "codex", Revision: "v1"}}, nil
}
func (s *blockingAvailabilitySource) Discover(ctx context.Context, _ model.TenantID, _ AvailabilitySource) ([]string, error) {
	s.calls.Add(1)
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAvailableModelsStartAndStopOwnDiscovery(t *testing.T) {
	m, _, tenant := newMod(t)
	source := &blockingAvailabilitySource{started: make(chan struct{})}
	m.UseAvailabilitySource(source, func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil })
	ctx := context.Background()
	m.WakeAvailability(tenant)
	if err := m.RefreshAvailability(ctx, tenant); err != nil || source.calls.Load() != 0 {
		t.Fatal("discovery ran before module start")
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	select {
	case <-source.started:
	case <-time.After(2 * time.Second):
		t.Fatal("module start did not discover configured models")
	}
	// A slow vendor must not hold the read endpoint or prevent module shutdown.
	rows, err := m.AvailableModels(ctx, tenant)
	if err != nil || len(rows) != 1 || rows[0].State != "stale" {
		t.Fatalf("read during discovery: %+v %v", rows, err)
	}
	stopped := make(chan struct{})
	go func() { _ = m.Stop(ctx); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("module stop did not cancel and join discovery")
	}
	m.WakeAvailability(tenant)
	if err := m.RefreshAvailability(ctx, tenant); err != nil || source.calls.Load() != 1 {
		t.Fatal("discovery ran after module stop")
	}
}
