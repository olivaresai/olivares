// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type availabilityFixture struct {
	sources []AvailabilitySource
	ids     []string
	err     error
	calls   int
}

func TestAvailableModelsRejectMalformedObservations(t *testing.T) {
	for _, id := range []string{"", "has space", "has\x01control", "has\u00a0space", strings.Repeat("x", 201)} {
		t.Run(id, func(t *testing.T) {
			m, _, tenant := newMod(t)
			f := &availabilityFixture{sources: []AvailabilitySource{{Ref: "prv_fixture", ProviderRef: "prv_fixture", ProviderKind: "ollama", Revision: "v1"}}, ids: []string{"known-good"}}
			now := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
			m.UseAvailabilitySource(f, func(context.Context) ([]model.TenantID, error) { return nil, nil })
			m.availability.now = func() time.Time { return now }
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Stop(context.Background()) })
			if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
				t.Fatal(err)
			}
			f.ids = []string{id}
			now = now.Add(availabilityRefreshInterval)
			if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
				t.Fatal(err)
			}
			failed, err := m.AvailableModels(context.Background(), tenant)
			if err != nil || len(failed) != 1 || failed[0].State != "failed" || len(failed[0].Models) != 1 || failed[0].Models[0].ID != "known-good" {
				t.Fatalf("malformed output replaced the last good catalog: %+v %v", failed, err)
			}
			f.ids = nil
			now = now.Add(availabilityRefreshInterval)
			if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
				t.Fatal(err)
			}
			empty, err := m.AvailableModels(context.Background(), tenant)
			if err != nil || len(empty) != 1 || empty[0].State != "fresh" || len(empty[0].Models) != 0 {
				t.Fatalf("a valid empty catalog was refused: %+v %v", empty, err)
			}
		})
	}
}

func (f *availabilityFixture) Sources(context.Context, model.TenantID) ([]AvailabilitySource, error) {
	return f.sources, nil
}
func (f *availabilityFixture) Discover(context.Context, model.TenantID, AvailabilitySource) ([]string, error) {
	f.calls++
	return f.ids, f.err
}

func TestAvailableModelsRefreshAndIdleWrites(t *testing.T) {
	m, st, tenant := newMod(t)
	auditRows := func() int {
		n := 0
		if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
			return sc.Audit().Walk(context.Background(), 1, func(model.AuditEvent) error { n++; return nil })
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	f := &availabilityFixture{sources: []AvailabilitySource{{Ref: "prv_fixture", ProviderRef: "prv_fixture", ProviderKind: "ollama", Revision: "v1"}}, ids: []string{"qwen3:8b", "qwen3:8b"}}
	now := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	m.UseAvailabilitySource(f, func(context.Context) ([]model.TenantID, error) { return nil, nil })
	m.availability.now = func() time.Time { return now }
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	first, err := m.AvailableModels(context.Background(), tenant)
	if err != nil || len(first) != 1 || first[0].State != "fresh" || first[0].Message != "" || len(first[0].Models) != 1 || first[0].Models[0].ID != "qwen3:8b" {
		t.Fatalf("catalog: %+v %v", first, err)
	}
	if first[0].ProviderRef != "prv_fixture" || first[0].SeenAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("provenance: %+v", first[0])
	}
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("hot loop: %d provider calls", f.calls)
	}
	beforeAudit := auditRows()
	now = now.Add(availabilityRefreshInterval)
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	second, err := m.AvailableModels(context.Background(), tenant)
	if err != nil || second[0].Version != first[0].Version || second[0].SeenAt == first[0].SeenAt || f.calls != 2 {
		t.Fatalf("unchanged refresh wrote or lost observation: %+v %v calls=%d", second, err, f.calls)
	}
	if auditRows() != beforeAudit {
		t.Fatal("unchanged refresh added audit writes")
	}
	f.err = errors.New("SECRET credential echoed by vendor")
	now = now.Add(availabilityRefreshInterval)
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	failed, _ := m.AvailableModels(context.Background(), tenant)
	if failed[0].State != "failed" || failed[0].Message != "Models could not be refreshed. Check the provider connection or sign in again." || len(failed[0].Models) != 1 {
		t.Fatalf("failure: %+v", failed)
	}
	now = now.Add(availabilityRefreshInterval)
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	retry, _ := m.AvailableModels(context.Background(), tenant)
	if retry[0].Version != failed[0].Version {
		t.Fatal("identical failure wrote another row")
	}
	f.err = nil
	f.sources[0].Revision = "v2"
	f.err = errors.New("new account is not signed in")
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	rotated, _ := m.AvailableModels(context.Background(), tenant)
	if rotated[0].State != "failed" || len(rotated[0].Models) != 0 || rotated[0].SeenAt != "" {
		t.Fatalf("old credential observation survived rotation: %+v", rotated)
	}
	f.err = nil
	f.sources[0].Revision = "v3"
	f.ids = []string{"qwen3:14b"}
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	changed, _ := m.AvailableModels(context.Background(), tenant)
	if changed[0].State != "fresh" || changed[0].Message != "" || changed[0].Models[0].ID != "qwen3:14b" {
		t.Fatalf("configuration change: %+v", changed)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.calls
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if calls != f.calls {
		t.Fatal("discovery ran while module was off")
	}
}

func TestAvailableModelsStaleUnsupportedAndRemoved(t *testing.T) {
	m, _, tenant := newMod(t)
	f := &availabilityFixture{sources: []AvailabilitySource{{Ref: "ppf_fixture", AccountRef: "ppf_fixture", ProviderKind: "codex", Driver: "codex", Revision: "v1"}}, ids: []string{"gpt-fixture"}}
	now := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	m.UseAvailabilitySource(f, func(context.Context) ([]model.TenantID, error) { return nil, nil })
	m.availability.now = func() time.Time { return now }
	initial, err := m.AvailableModels(context.Background(), tenant)
	if err != nil || initial[0].State != "stale" || initial[0].Message != "Models have not been refreshed yet." || len(initial[0].Models) != 0 {
		t.Fatalf("unobserved: %+v %v", initial, err)
	}
	_ = m.Start(context.Background())
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * availabilityRefreshInterval)
	stale, err := m.AvailableModels(context.Background(), tenant)
	if err != nil || stale[0].State != "stale" {
		t.Fatalf("stale: %+v %v", stale, err)
	}
	f.err = ErrModelDiscoveryUnsupported
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	unsupported, _ := m.AvailableModels(context.Background(), tenant)
	if unsupported[0].State != "unsupported" {
		t.Fatalf("unsupported: %+v", unsupported)
	}
	f.sources = nil
	removed, err := m.AvailableModels(context.Background(), tenant)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed source offered: %+v %v", removed, err)
	}
}
