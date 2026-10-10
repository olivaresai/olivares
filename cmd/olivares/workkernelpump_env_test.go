// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

// The work-outbox pump's environment contract (#566): the documented `0`
// disables the periodic drain — no scheduler registration, the witness never
// claims one, and the insert nudge keeps draining fresh rows (the
// eventing-dispatch disable posture). A typo or a negative value keeps the
// default rather than silently changing the schedule (the retention-sweep
// posture).

func TestWorkOutboxPumpIntervalEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	pump := func() *workOutboxPump {
		return newWorkOutboxPump(getenv, fakeStoreLeader(true), &sessions.Module{}, discardLog())
	}
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultWorkOutboxInterval},
		{"7m", 7 * time.Minute},
		{" 30s ", 30 * time.Second},
		{"0", 0},
		{"0s", 0},
		{"-0s", 0}, // ParseDuration normalizes every zero spelling to the disable
		{"garbage", defaultWorkOutboxInterval},
		{"-5s", defaultWorkOutboxInterval},
	}
	for _, c := range cases {
		env[workOutboxPumpIntervalEnv] = c.raw
		p := pump()
		if p == nil {
			t.Fatalf("interval %q: pump is nil, want a pump", c.raw)
		}
		if p.interval != c.want {
			t.Fatalf("interval %q = %s, want %s", c.raw, p.interval, c.want)
		}
	}
}

func TestWorkOutboxPumpZeroDisablesPeriodicKeepsInsertNudge(t *testing.T) {
	// The nudge slot is process-global: leave it as found.
	t.Cleanup(func() { sessions.SetWorkOutboxNudge(nil) })

	p := newWorkOutboxPump(func(string) string { return "0" }, fakeStoreLeader(true), &sessions.Module{}, discardLog())
	if p == nil {
		t.Fatal("\"0\" must keep a pump that owns the insert nudge, not remove it")
	}
	calls := make(chan struct{}, 1)
	p.run = func(context.Context) error { calls <- struct{}{}; return nil }

	// The witness must tell the truth about a disabled pump: attached (so the
	// K3 verdict names the zero interval) but never marked registered.
	witness := &communicationPumpWitness{}
	p.useCommunication(witness, nil)

	spy := &spyScheduler{}
	if err := p.register(spy); err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	if spy.calls != 0 {
		t.Fatalf("\"0\" registered the periodic drain %d times, want 0", spy.calls)
	}
	if s := witness.status(); s.Registered || s.Interval != 0 {
		t.Fatalf("disabled pump witness = %s, want registered=false interval=0s", s)
	}
	if ready, err := witness.CommunicationPumpReady(context.Background()); err != nil || ready {
		t.Fatalf("a disabled pump must not authorize K3 claims: ready=%t err=%v", ready, err)
	}
	if !sessions.WorkOutboxNudgeRegistered() {
		t.Fatal("the insert nudge must stay registered while the periodic drain is disabled")
	}

	// Fresh rows still drain: a nudge wakes the pump's owned drain.
	p.nudgeCh <- struct{}{}
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("a nudge did not drain while the periodic drain is disabled")
	}
}
