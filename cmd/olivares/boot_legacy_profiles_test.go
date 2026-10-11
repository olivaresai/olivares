// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
)

type legacyAdopterProbe struct {
	calls  []model.TenantID
	errFor map[model.TenantID]error
}

func (p *legacyAdopterProbe) AdoptUnnamedProfiles(_ context.Context, tenant model.TenantID) (int, error) {
	p.calls = append(p.calls, tenant)
	return 1, p.errFor[tenant]
}

func legacyNamingTestLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func alwaysLeader() bool { return true }

// Naming the legacy profiles covers every tenant this region serves, skips the
// system tenant and foreign regions, and goes on after a tenant it could not
// finish: a profile that stays unnamed is a profile, and the next promotion
// tries again.
func TestLegacyProfileNamingCoversLocalTenantsAndGoesOnAfterAFailure(t *testing.T) {
	local, broken, foreign := model.TenantID(model.NewID()), model.TenantID(model.NewID()), model.TenantID(model.NewID())
	reg, err := residency.NewRegistry("us-east", []string{"eu-west"})
	if err != nil {
		t.Fatal(err)
	}
	probe := &legacyAdopterProbe{errFor: map[model.TenantID]error{broken: errors.New("one profile refused")}}
	<-startLegacyProfileAdoption(
		context.Background(),
		func(context.Context) ([]model.Org, error) {
			return []model.Org{
				recoveryOrg(model.ID(model.SystemTenantID), "", model.StatusActive),
				recoveryOrg(model.ID(broken), "us-east", model.StatusActive),
				recoveryOrg(model.ID(local), "us-east", model.StatusActive),
				recoveryOrg(model.ID(foreign), "eu-west", model.StatusActive),
			}, nil
		},
		reg, probe, alwaysLeader, legacyNamingTestLog(),
	)
	if !slices.Equal(probe.calls, []model.TenantID{broken, local}) {
		t.Fatalf("tenants named = %v, want the two local ones, the broken one not stopping the next", probe.calls)
	}

	probe.calls = nil
	<-startLegacyProfileAdoption(
		context.Background(),
		func(context.Context) ([]model.Org, error) { return nil, errors.New("inventory unavailable") },
		nil, probe, alwaysLeader, legacyNamingTestLog(),
	)
	if len(probe.calls) != 0 {
		t.Fatalf("an unreadable tenant inventory still named profiles: %v", probe.calls)
	}
}

// blockingAdopter holds the naming until released, so a test can tell whether the
// caller waited for it.
type blockingAdopter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingAdopter) AdoptUnnamedProfiles(ctx context.Context, _ model.TenantID) (int, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return 0, nil
}

// Promotion must not wait for the naming: the call returns at once even when the
// naming is stuck, the naming starts only after leadership is visible (a write
// before that fails closed and would never be retried), and it ends with its context.
func TestPromotionDoesNotWaitForLegacyProfileNaming(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	orgs := func(context.Context) ([]model.Org, error) {
		return []model.Org{recoveryOrg(model.ID(tenant), "", model.StatusActive)}, nil
	}
	adopter := &blockingAdopter{started: make(chan struct{}), release: make(chan struct{})}
	var leader atomic.Bool
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	returned := make(chan (<-chan struct{}), 1)
	go func() {
		returned <- startLegacyProfileAdoption(ctx, orgs, nil, adopter, leader.Load, legacyNamingTestLog())
	}()
	var done <-chan struct{}
	select {
	case done = <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("promotion waited for the legacy profile naming")
	}
	select {
	case <-adopter.started:
		t.Fatal("profiles were named before this node was visibly the writer")
	case <-time.After(300 * time.Millisecond):
	}
	leader.Store(true)
	select {
	case <-adopter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the naming never started once the node was the writer")
	}
	stop() // the engine closes while the naming is stuck
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the naming outlived the engine's context")
	}
}

// A node that never becomes the writer never names anything, and the wait ends
// with the context.
func TestLegacyProfileNamingGivesUpWhenNeverTheWriter(t *testing.T) {
	probe := &legacyAdopterProbe{}
	ctx, stop := context.WithCancel(context.Background())
	done := startLegacyProfileAdoption(ctx, func(context.Context) ([]model.Org, error) {
		return []model.Org{recoveryOrg(model.ID(model.NewID()), "", model.StatusActive)}, nil
	}, nil, probe, func() bool { return false }, legacyNamingTestLog())
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait for leadership outlived its context")
	}
	if len(probe.calls) != 0 {
		t.Fatalf("a node that is not the writer named %v", probe.calls)
	}
}
