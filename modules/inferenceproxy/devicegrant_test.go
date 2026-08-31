// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type deviceGrantClock struct{ t time.Time }

func (c *deviceGrantClock) Now() model.Timestamp { return model.NewTimestamp(c.t) }

func newDeviceGrantHarness(t *testing.T, now time.Time) (*Module, model.TenantID, *deviceGrantClock) {
	t.Helper()
	clk := &deviceGrantClock{t: now}
	m := New(WithClock(clk))
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, tenant, clk
}

func TestDeviceGrantLifecyclePendingApprovedConsumed(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	m, tenant, clk := newDeviceGrantHarness(t, now)
	ctx := context.Background()
	grant, err := m.CreateDeviceGrant(ctx, tenant, "device-1", "BCDF-GHJK", now, 10*time.Minute)
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if grant.State != DeviceGrantPending {
		t.Fatalf("state = %q, want pending", grant.State)
	}
	if _, status, err := m.PollDeviceGrant(ctx, tenant, "device-1", now, 5*time.Second); err != nil || status != DeviceGrantPollPending {
		t.Fatalf("first poll status=%q err=%v, want pending", status, err)
	}
	if _, status, err := m.PollDeviceGrant(ctx, tenant, "device-1", now.Add(time.Second), 5*time.Second); err != nil || status != DeviceGrantPollSlowDown {
		t.Fatalf("fast poll status=%q err=%v, want slow_down", status, err)
	}
	clk.t = now.Add(2 * time.Second)
	approved, err := m.ApproveDeviceGrant(ctx, tenant, "user:u1", "user", "BCDF-GHJK", false)
	if err != nil {
		t.Fatalf("approve grant: %v", err)
	}
	if approved.State != DeviceGrantApproved || approved.Tenant != tenant || approved.ApprovedBy != "user:u1" {
		t.Fatalf("approved grant = %+v", approved)
	}
	consumed, err := m.ConsumeDeviceGrant(ctx, tenant, approved.ID, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("consume grant: %v", err)
	}
	if consumed.State != DeviceGrantConsumed {
		t.Fatalf("state = %q, want consumed", consumed.State)
	}
	if _, status, err := m.PollDeviceGrant(ctx, tenant, "device-1", now.Add(4*time.Second), 5*time.Second); err != nil || status != DeviceGrantPollConsumed {
		t.Fatalf("post-consume poll status=%q err=%v, want consumed", status, err)
	}
}

func TestDeviceGrantExpiry(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	m, tenant, clk := newDeviceGrantHarness(t, now)
	ctx := context.Background()
	if _, err := m.CreateDeviceGrant(ctx, tenant, "device-exp", "LMNP-QRST", now, time.Second); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	later := now.Add(2 * time.Second)
	grant, status, err := m.PollDeviceGrant(ctx, tenant, "device-exp", later, 5*time.Second)
	if err != nil || status != DeviceGrantPollExpired || grant.State != DeviceGrantExpired {
		t.Fatalf("expired poll grant=%+v status=%q err=%v", grant, status, err)
	}
	clk.t = later
	if _, err := m.ApproveDeviceGrant(ctx, tenant, "user:u1", "user", "LMNP-QRST", false); !errors.Is(err, ErrDeviceGrantExpired) {
		t.Fatalf("approve expired err=%v, want ErrDeviceGrantExpired", err)
	}
}

func TestDeviceGrantDenyAndUnknown(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	m, tenant, _ := newDeviceGrantHarness(t, now)
	ctx := context.Background()
	if _, err := m.CreateDeviceGrant(ctx, tenant, "device-deny", "VWXZ-BCDF", now, 10*time.Minute); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	denied, err := m.ApproveDeviceGrant(ctx, tenant, "user:u1", "user", "VWXZ-BCDF", true)
	if err != nil {
		t.Fatalf("deny grant: %v", err)
	}
	if denied.State != DeviceGrantDenied {
		t.Fatalf("state = %q, want denied", denied.State)
	}
	if _, status, err := m.PollDeviceGrant(ctx, tenant, "device-deny", now, 5*time.Second); err != nil || status != DeviceGrantPollDenied {
		t.Fatalf("denied poll status=%q err=%v, want denied", status, err)
	}
	if _, err := m.ApproveDeviceGrant(ctx, tenant, "user:u1", "user", "NOPE-NOPE", false); !errors.Is(err, ErrDeviceGrantNotFound) {
		t.Fatalf("approve unknown err=%v, want ErrDeviceGrantNotFound", err)
	}
}
