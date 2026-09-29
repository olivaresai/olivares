// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"testing"
	"time"
)

func pendingCall() CallRecord {
	return CallRecord{BusID: "bus-a", ConnectionGeneration: "generation-a", Caller: ":1.20", Serial: 37, Target: ProcessIdentity{Unique: ":1.8", BootID: "boot-a", PID: 42, StartTime: 123}, Method: UpdateInMemory, SentAt: 10 * time.Second}
}
func TestCall_OnlyExactReplyOrOriginalDeathSettles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply Reply
		death Death
		want  bool
	}{
		{"matching", Reply{BusID: "bus-a", ConnectionGeneration: "generation-a", Sender: ":1.8", ReplySerial: 37, Completed: true}, DeathUnknown, true},
		{"new bus same names and serial", Reply{BusID: "bus-b", ConnectionGeneration: "generation-a", Sender: ":1.8", ReplySerial: 37, Completed: true}, DeathUnknown, false},
		{"reconnection", Reply{BusID: "bus-a", ConnectionGeneration: "generation-b", Sender: ":1.8", ReplySerial: 37, Completed: true}, DeathUnknown, false},
		{"wrong sender", Reply{BusID: "bus-a", ConnectionGeneration: "generation-a", Sender: ":1.9", ReplySerial: 37, Completed: true}, DeathUnknown, false},
		{"wrong serial", Reply{BusID: "bus-a", ConnectionGeneration: "generation-a", Sender: ":1.8", ReplySerial: 38, Completed: true}, DeathUnknown, false},
		{"bus NoReply", Reply{BusID: "bus-a", ConnectionGeneration: "generation-a", Sender: "org.freedesktop.DBus", ReplySerial: 37, Completed: true}, DeathUnknown, false},
		{"same daemon alive", Reply{}, DeathAlive, false},
		{"hidden proc", Reply{}, DeathUnknown, false},
		{"original dead", Reply{}, DeathProven, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := pendingCall()
			if got := c.SettledBy(tc.reply, tc.death); got != tc.want {
				t.Fatalf("settled %t want %t", got, tc.want)
			}
		})
	}
}

func TestCall_NotificationDeadlineNeverCancelsOrSettles(t *testing.T) {
	c := pendingCall()
	if !c.NotificationDue(21*time.Second) || c.SettledBy(Reply{}, DeathAlive) {
		t.Fatal("notification became settlement")
	}
	if c.NotificationDue(19 * time.Second) {
		t.Fatal("early notification")
	}
}

func TestCall_PerMethodFences(t *testing.T) {
	for _, method := range []Method{UpdateInMemory, UpdateToDisk, CheckpointDestroy, CheckpointRollback} {
		c := pendingCall()
		c.Method = method
		if CanOvertake(c, 9, 10) {
			t.Fatalf("overtook %s", method)
		}
	}
	c := pendingCall()
	c.Method = Reapply
	if CanOvertake(c, 0, 1) || CanOvertake(c, 9, 9) || !CanOvertake(c, 9, 10) {
		t.Fatal("Reapply fence is not a nonzero superseding applied version")
	}
}

func TestProfileVersion_InstalledInterfaceAndTypeArePinned(t *testing.T) {
	if ProfileInterface != "org.freedesktop.NetworkManager.Settings.Connection" {
		t.Fatal("wrong version interface")
	}
	for _, v := range []any{nil, uint32(1), uint64(0), "1"} {
		if _, err := ProfileVersion(v); err == nil {
			t.Fatalf("accepted %T", v)
		}
	}
	if n, err := ProfileVersion(uint64(9)); err != nil || n != 9 {
		t.Fatal(n, err)
	}
}

func TestProfileVersion_ConcurrentMutationRefusesUpdate(t *testing.T) {
	if err := VersionBracket(7, 8); err == nil {
		t.Fatal("concurrent profile mutation accepted")
	}
	if err := VersionBracket(7, 7); err != nil {
		t.Fatal(err)
	}
}

func TestProcessIdentity_RefusesAbsentWrongThreadDeadAndHiddenProcessFD(t *testing.T) {
	good := ProcessFacts{FromCredentials: true, WholeProcess: true, Alive: true, Visible: true, PID: 42, TGID: 42, StartTime: 123, BootID: "boot-a", Unique: ":1.8"}
	if _, err := IdentifyProcess(good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ProcessFacts){func(p *ProcessFacts) { p.FromCredentials = false }, func(p *ProcessFacts) { p.WholeProcess = false }, func(p *ProcessFacts) { p.Alive = false }, func(p *ProcessFacts) { p.Visible = false }, func(p *ProcessFacts) { p.TGID = 43 }, func(p *ProcessFacts) { p.StartTime = 0 }} {
		bad := good
		change(&bad)
		if _, err := IdentifyProcess(bad); err == nil {
			t.Fatal("unproven original process accepted")
		}
	}
}

func TestRecovery_ObservationAndHelperExitNeverSettleCalls(t *testing.T) {
	c := pendingCall()
	for _, event := range []string{"correct-state", "checkpoint-visible", "checkpoint-gone", "helper-exit", "well-known-name-lost", "timeout", "cancel"} {
		if c.SettledBy(Reply{Event: event}, DeathUnknown) {
			t.Fatalf("%s settled", event)
		}
	}
}
