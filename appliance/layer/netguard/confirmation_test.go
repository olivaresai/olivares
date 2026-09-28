// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package netguard

import (
	"context"
	"errors"
	"testing"
	"time"
)

type observedBus struct {
	*testNM
	management bool
}

func (b observedBus) Observe(ctx context.Context, iface string) (Observation, error) {
	o, e := b.testNM.Observe(ctx, iface)
	o.ManagementDevice = b.management
	return o, e
}

type pathProbe struct{}

func (pathProbe) Check(_ context.Context, w Window, _ Observation, c Confirmation) error {
	if c.Witness != w.Candidate.Interface {
		return errors.New("device_probe_required")
	}
	return nil
}
func TestConfirm_RefusesAConnectionOffTheNewPath(t *testing.T) {
	e, n, _, c := newTestEngine(t)
	e.config.Probe = pathProbe{}
	_, token, err := e.Apply(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := c.Digest()
	if err := e.Confirm(t.Context(), Confirmation{OperationID: c.OperationID, Digest: d, Token: token, Class: NonManagement, Witness: "nic0"}); err == nil {
		t.Fatal("foreign path accepted")
	}
	for _, m := range n.calls {
		if m.Method == UpdateToDisk {
			t.Fatal("persisted without changed device witness")
		}
	}
}
func TestConfirm_CliSelfConnectionNeverProvesRemoteManagement(t *testing.T) {
	e, _, _, c := newTestEngine(t)
	_, token, err := e.Apply(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := c.Digest()
	if err := e.Confirm(t.Context(), Confirmation{OperationID: c.OperationID, Digest: d, Token: token, Class: StaticManagement, Witness: "cli-self-connection"}); err == nil {
		t.Fatal("CLI claimed remote management")
	}
}
func TestConfirm_StaticManagementRequiresObservedExposureDependency(t *testing.T) {
	e, n, _, c := newTestEngine(t)
	e.config.Bus = observedBus{n, true}
	if _, _, err := e.Apply(t.Context(), c); err == nil || len(n.calls) != 0 {
		t.Fatal("management mutation without exposure", err, n.calls)
	}
}
func TestConfirm_DynamicManagementRequiresQualifiedConsole(t *testing.T) {
	e, n, _, c := newTestEngine(t)
	n.profile.IPv4.Method = "auto"
	n.profile.IPv4.Addresses = nil
	n.applied = n.profile
	e.config.Bus = observedBus{n, true}
	c.IPv4 = &IPSettings{Method: "auto", DNS: []string{"192.0.2.53"}, NeverDefault: true}
	if _, _, err := e.Apply(t.Context(), c); err == nil || len(n.calls) != 0 {
		t.Fatal("unqualified dynamic management mutation", err, n.calls)
	}
}
func TestGuard_UnknownCreateNeverAdmitsSettingsOrAnotherAct(t *testing.T) {
	e, n, clock, c := newTestEngine(t)
	n.delay = CheckpointCreate
	if _, _, err := e.Apply(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	clock.now += 61 * time.Second
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(c.OperationID)
	if s.PendingCalls != 1 || s.State == StateRolledBack || len(n.calls) != 1 {
		t.Fatal(s, n.calls)
	}
	next := c
	next.OperationID = "1123456789abcdef0123456789abcdef"
	if _, _, err := e.Apply(t.Context(), next); err == nil || len(n.calls) != 1 {
		t.Fatal("pending create admitted another act", err, n.calls)
	}
}

func TestApply_ProfileIdentityIsBoundBeforeAnyOwnerCall(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	change.ProfileUUID = "22222222-2222-2222-2222-222222222222"
	if _, _, err := e.Apply(t.Context(), change); err == nil || len(n.calls) != 0 {
		t.Fatal("changed profile accepted", err, n.calls)
	}
}
