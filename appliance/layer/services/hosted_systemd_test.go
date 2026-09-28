// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build hostedsystemd && linux

package services

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHosted_UnitsStartsAndStopsATestUnit runs only in a disposable, privileged systemd
// container, as root, built with the hostedsystemd tag. It installs a runtime test unit and
// drives it through the real service manager bus and journal reader: start, status, logs,
// enable, disable, stop, and a protected unit's refused stop. It removes the unit afterwards.
func TestHosted_UnitsStartsAndStopsATestUnit(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("the hosted job runs this test as root in its systemd container")
	}
	const unit = "olivares-app-hosted-probe.service"
	path := "/run/systemd/system/" + unit
	content := "[Unit]\nDescription=Services module hosted probe\n\n[Service]\nType=simple\nExecStart=/usr/bin/sleep infinity\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	reload := func() {
		if out, err := exec.Command("/usr/bin/systemctl", "daemon-reload").CombinedOutput(); err != nil {
			t.Fatalf("daemon-reload: %v %s", err, out)
		}
	}
	reload()
	t.Cleanup(func() {
		_ = exec.Command("/usr/bin/systemctl", "stop", unit).Run()
		_ = exec.Command("/usr/bin/systemctl", "disable", unit).Run()
		_ = os.Remove(path)
		_ = exec.Command("/usr/bin/systemctl", "daemon-reload").Run()
	})
	bus, err := DialSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	x := Executor{Bus: bus, Journal: ExecJournal, JobWait: time.Minute}
	if got := Classify(unit); got != ClassManaged {
		t.Fatalf("the probe unit is %s", got)
	}
	expect := func(op, want string) Result {
		t.Helper()
		res := x.Apply(ctx, op, unit)
		obs := res.Observation()
		if obs.P1 != "performed" || len(obs.Postconditions) == 0 {
			t.Fatalf("%s answered %+v, observation %+v", op, res, obs)
		}
		if want != "" && res.JobResult != want {
			t.Fatalf("%s job result %q, want %q", op, res.JobResult, want)
		}
		return res
	}
	if res := expect(OpStart, "done"); res.After.ActiveState != "active" {
		t.Fatalf("after start: %+v", res.After)
	}
	status, err := x.Status(ctx, unit)
	if err != nil || status.State.ActiveState != "active" || status.Class != ClassManaged {
		t.Fatalf("status %+v %v", status, err)
	}
	var logs Logs
	for i := 0; i < 20; i++ {
		logs, err = x.Logs(ctx, unit, 10)
		if err == nil && len(logs.Entries) > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil || len(logs.Entries) == 0 || logs.Entries[len(logs.Entries)-1].Timestamp == "" {
		t.Fatalf("logs %+v %v", logs, err)
	}
	if res := expect(OpEnable, ""); res.After.UnitFileState != "enabled" {
		t.Fatalf("after enable: %+v", res.After)
	}
	if res := expect(OpDisable, ""); res.After.UnitFileState != "disabled" {
		t.Fatalf("after disable: %+v", res.After)
	}
	if res := expect(OpStop, "done"); res.After.ActiveState != "inactive" {
		t.Fatalf("after stop: %+v", res.After)
	}
	refused := x.Apply(ctx, OpStop, "systemd-journald.service")
	if refused.Refusal == nil || refused.Refusal.Code != CodeUnitProtected {
		t.Fatalf("a protected unit's stop answered %+v", refused)
	}
	if status, err := x.Status(ctx, "systemd-journald.service"); err != nil || status.State.ActiveState != "active" {
		t.Fatalf("the journal after a refused stop: %+v %v", status, err)
	}
}
