// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type testClock struct {
	now  time.Duration
	boot string
}

func (c *testClock) Now() (string, time.Duration, error) { return c.boot, c.now, nil }

type testLock struct{ held bool }

func (l *testLock) Acquire() (func(), error) {
	if l.held {
		return nil, errors.New("held")
	}
	l.held = true
	return func() { l.held = false }, nil
}

type testProbe struct {
	ok    bool
	clock *testClock
}

func (p testProbe) Check(context.Context, Window, Observation, Confirmation) error {
	if !p.ok {
		return errors.New("device_probe_required")
	}
	return nil
}

type testNM struct {
	profile, applied, disk Profile
	baseline               Profile
	calls                  []Mutation
	clock                  *testClock
	secrets, permissions   bool
	delay                  Method
	replies                chan Reply
	serial                 uint32
	dead                   Death
}

func (n *testNM) Observe(context.Context, string) (Observation, error) {
	// The device keeps a SLAAC address and its connected route for the auto IPv6 family.
	runtime := RuntimeState{IPv6: RuntimeFamily{Addresses: []string{"2001:db8::10/64"}, Routes: []Route{{Destination: "2001:db8::/64"}}}}
	return Observation{Profile: n.profile, Applied: n.applied, Managed: true, SecretsComplete: n.secrets, RuntimeUsable: true, ManagementUnchanged: true, Runtime: runtime}, nil
}
func (n *testNM) Permissions(context.Context) error {
	if !n.permissions {
		return errors.New("network_permissions_missing")
	}
	return nil
}
func (n *testNM) OriginalDeath(ProcessIdentity) Death { return n.dead }
func (n *testNM) Send(m Mutation, before func(CallRecord) error) (<-chan Reply, error) {
	n.serial++
	c := pendingCall()
	c.Serial = n.serial
	c.Method = m.Method
	c.Object = m.Profile.Connection
	if err := before(c); err != nil {
		return nil, err
	}
	n.calls = append(n.calls, m)
	reply := Reply{BusID: c.BusID, ConnectionGeneration: c.ConnectionGeneration, Sender: c.Target.Unique, ReplySerial: c.Serial, Completed: true, Success: true}
	switch m.Method {
	case CheckpointCreate:
		n.baseline = n.profile
		reply.Body = []any{"/checkpoint/1"}
	case UpdateInMemory:
		n.profile = m.Profile
		n.profile.Flags = 1
		n.profile.Filename = "/run/NetworkManager/system-connections/shadow.nmconnection"
		n.profile.ProfileVersion++
	case Reapply:
		n.applied = n.profile
		n.applied.AppliedVersion++
	case UpdateToDisk:
		n.profile.Flags = 0
		n.profile.Filename = "/etc/NetworkManager/system-connections/test.nmconnection"
		n.disk = n.profile
	case CheckpointRollback:
		n.profile = n.baseline
		n.applied = n.baseline
		n.disk = n.baseline
		reply.Body = []any{map[string]uint32{n.profile.Device: 0}}
	}
	ch := make(chan Reply, 1)
	if m.Method == n.delay {
		n.replies = ch
	} else {
		ch <- reply
	}
	return ch, nil
}
func newTestEngine(t *testing.T) (*Engine, *testNM, *testClock, Change) {
	t.Helper()
	clock := &testClock{now: time.Second, boot: "boot-a"}
	p := Profile{UUID: "11111111-1111-1111-1111-111111111111", Interface: "ens4", Device: "/device/1", Connection: "/connection/1", Filename: "/etc/NetworkManager/system-connections/test.nmconnection", ProfileVersion: 1, AppliedVersion: 1, IPv4: IPSettings{Method: "manual", Addresses: []string{"192.0.2.10/24"}, NeverDefault: true}, IPv6: IPSettings{Method: "auto", NeverDefault: true}}
	nm := &testNM{profile: p, applied: p, disk: p, clock: clock, secrets: true, permissions: true, dead: DeathAlive}
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "acts"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(Config{Bus: nm, Clock: clock, Journal: journal, Lock: &testLock{}, Probe: testProbe{ok: true, clock: clock}, CallWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	desired := p.IPv4
	desired.Addresses = []string{"192.0.2.20/24"}
	return engine, nm, clock, Change{ProfileUUID: p.UUID, OperationID: "0123456789abcdef0123456789abcdef", Interface: "ens4", IPv4: &desired, WindowSeconds: 60}
}
func TestCheckpoint_UnconfirmedChangeRevertsAtTimeout(t *testing.T) {
	e, n, c, change := newTestEngine(t)
	_, _, err := e.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	c.now += 61 * time.Second
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Status(change.OperationID)
	if err != nil || s.State != StateRolledBack || !n.applied.Matches(n.baseline) {
		t.Fatal(s, err)
	}
}
func TestCheckpoint_ConfirmedChangePersists(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	_, token, err := e.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := change.Digest()
	if err := e.Confirm(context.Background(), Confirmation{OperationID: change.OperationID, Digest: digest, Token: token, Class: NonManagement}); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(change.OperationID)
	if s.State != StateConfirmed || !n.disk.Matches(n.profile) || !n.profile.Persistent() {
		t.Fatal(s)
	}
}
func TestCheckpoint_FlagsArePinnedAndNeverDestroyAll(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	if n.calls[0].Flags != 6 || n.calls[0].TimeoutSeconds != 90 {
		t.Fatal(n.calls[0])
	}
}
func TestApply_WritesInMemoryOnlyBeforeConfirm(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	old := n.disk
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	if !old.Matches(n.disk) {
		t.Fatal("disk changed before confirmation")
	}
	for _, call := range n.calls {
		if call.Method == UpdateToDisk {
			t.Fatal("early save")
		}
	}
}
func TestApply_KeepsEverySecretOrRefuses(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	n.secrets = false
	if _, _, err := e.Apply(context.Background(), change); err == nil || len(n.calls) != 0 {
		t.Fatal("missing secret was accepted", err)
	}
}
func TestApply_UsesVersionIDAndTheNetworkLock(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	for _, call := range n.calls {
		if (call.Method == UpdateInMemory || call.Method == Reapply) && call.Version == 0 {
			t.Fatal("zero version")
		}
	}
	if _, _, err := e.Apply(context.Background(), Change{ProfileUUID: change.ProfileUUID, OperationID: "fedcba9876543210fedcba9876543210", Interface: change.Interface, IPv4: change.IPv4, WindowSeconds: 60}); err == nil {
		t.Fatal("second window admitted")
	}
}
func TestConfirm_RequiresTheOneUseToken(t *testing.T) {
	e, _, _, change := newTestEngine(t)
	_, token, err := e.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := change.Digest()
	proof := Confirmation{OperationID: change.OperationID, Digest: digest, Token: "wrong", Class: NonManagement}
	if err := e.Confirm(context.Background(), proof); err == nil {
		t.Fatal("wrong token accepted")
	}
	proof.Token = token
	if err := e.Confirm(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	if err := e.Confirm(context.Background(), proof); err == nil {
		t.Fatal("reused token accepted")
	}
}
func TestConfirm_NonManagementNeedsChangedDeviceProbe(t *testing.T) {
	e, _, _, change := newTestEngine(t)
	e.config.Probe = testProbe{ok: false}
	_, token, err := e.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := change.Digest()
	if err := e.Confirm(context.Background(), Confirmation{OperationID: change.OperationID, Digest: digest, Token: token, Class: NonManagement}); err == nil {
		t.Fatal("missing probe accepted")
	}
}
func TestConfirm_DeadlineAndGuardHaveOnlyOneWinningTransition(t *testing.T) {
	e, _, clock, change := newTestEngine(t)
	_, token, err := e.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	clock.now += 60 * time.Second
	digest, _ := change.Digest()
	if err := e.Confirm(context.Background(), Confirmation{OperationID: change.OperationID, Digest: digest, Token: token, Class: NonManagement}); err == nil {
		t.Fatal("deadline confirmation accepted")
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(change.OperationID)
	if s.State != StateRolledBack {
		t.Fatal(s)
	}
}
func TestConfirm_SavesWithEmptyUpdate2AndMeasuresDBusFacts(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	_, token, err := e.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := change.Digest()
	if err := e.Confirm(context.Background(), Confirmation{OperationID: change.OperationID, Digest: digest, Token: token, Class: NonManagement}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range n.calls {
		if call.Method == UpdateToDisk {
			found = true
			if !call.EmptySettings || call.Flags != 1 || call.Version == 0 {
				t.Fatal(call)
			}
		}
	}
	if !found {
		t.Fatal("no persistence call")
	}
}
func TestNoConfirm_PlaneCallsCheckpointRollbackAndRecordsPerDeviceResults(t *testing.T) {
	e, _, clock, change := newTestEngine(t)
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	clock.now += 61 * time.Second
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, err := e.config.Journal.Load(change.OperationID)
	if err != nil || len(w.RollbackResults) != 1 {
		t.Fatal(w, err)
	}
}
func TestSameOperationIDNeverAppliesTwice(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	count := len(n.calls)
	if _, token, err := e.Apply(context.Background(), change); err != nil || token != "" || len(n.calls) != count {
		t.Fatal("repeated apply", token, err)
	}
	change.WindowSeconds++
	if _, _, err := e.Apply(context.Background(), change); err == nil {
		t.Fatal("changed digest accepted")
	}
}
func TestGuard_RefusesWithoutEveryNeededPermission(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	n.permissions = false
	if _, _, err := e.Apply(context.Background(), change); err == nil || len(n.calls) != 0 {
		t.Fatal("missing permissions accepted", err)
	}
}
func TestPendingUpdate_DeadlineAndCorrectObservationNeverPublishTerminal(t *testing.T) {
	e, n, clock, change := newTestEngine(t)
	n.delay = UpdateInMemory
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	clock.now += 120 * time.Second
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(change.OperationID)
	if s.FinalKnownAt != 0 || s.PendingCalls != 1 || s.State == StateRolledBack {
		t.Fatal(s)
	}
	if len(n.calls) != 2 {
		t.Fatal("recovery overtook pending Update2")
	}
}

// CheckRestoration reports a device-bound observation from the baseline interface's
// SLAAC address, made now in the current boot.
func (p testProbe) CheckRestoration(_ context.Context, w Window, _ Observation) ([]Reachability, error) {
	if !p.ok {
		return nil, errors.New("restoration_probe_required")
	}
	boot, now, _ := p.clock.Now()
	return []Reachability{{Interface: w.Baseline.Interface, Source: "2001:db8::10", Target: "2001:db8::53", BootID: boot, At: now}}, nil
}
