// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

// operationID is a well-formed operation id for documents that need one.
var operationID = strings.Repeat("ab", 16)

func TestUnits_ProtectedUnitRefusesStopDisableAndMask(t *testing.T) {
	protected := []string{
		"dbus.service", "dbus.socket", "dbus-broker.service", "systemd-journald.service", "systemd-timesyncd.service",
		"polkit.service", "NetworkManager.service", "olivares-portal.service", "olivares-portal-local.socket",
		"olivares-repair-console.service", "olivares-helper-power.socket", "olivares-helper-units@3-4-5.service",
		"olivares-firewall.service", "olivares-net-guard.service", "olivares-net-guard.socket",
	}
	ctx := context.Background()
	for _, unit := range protected {
		if got := Classify(unit); got != ClassProtected {
			t.Errorf("%s is in class %q, want protected", unit, got)
		}
		bus := newFakeBus(active(unit))
		x := Executor{Bus: bus, JobWait: 20 * time.Millisecond}
		for _, op := range []string{OpStop, OpRestart, OpReload, OpDisable} {
			res := x.Apply(ctx, op, unit)
			if res.Refusal == nil || res.Refusal.Code != CodeUnitProtected || res.Refusal.Class != ClassProtected {
				t.Errorf("%s %s answered %+v, want a unit_protected refusal naming the protected class", op, unit, res)
			}
		}
		res := x.Apply(ctx, "mask", unit)
		if res.Refusal == nil || res.Refusal.Code != CodeInputRefused {
			t.Errorf("mask %s answered %+v, want input_refused: mask is not a verb of this module", unit, res)
		}
		if calls := bus.Calls(); len(calls) != 0 {
			t.Errorf("%s: a refused verb reached the service manager: %v", unit, calls)
		}
		for _, op := range []string{"mask", "unmask", "kill", "link", "edit"} {
			doc := `{"op":"` + op + `","unit":"` + unit + `","operation_id":"` + operationID + `"}`
			if err := helperschema.Decode(strings.NewReader(doc), &Request{}); err == nil {
				t.Errorf("the helper document accepts op %q", op)
			}
		}
	}
	for _, op := range []string{OpStop, OpRestart, OpReload, OpDisable, "mask"} {
		if ClassProtected.Allows(op) {
			t.Errorf("the protected class admits %s", op)
		}
	}
	bus := reflect.TypeOf((*Bus)(nil)).Elem()
	for i := 0; i < bus.NumMethod(); i++ {
		if strings.Contains(strings.ToLower(bus.Method(i).Name), "mask") {
			t.Errorf("the helper's bus has a masking method: %s", bus.Method(i).Name)
		}
	}
	// Control: the table refuses by class, not everything. A managed unit's stop passes it.
	if refusal := Check(OpStop, "olivares.service"); refusal != nil {
		t.Errorf("stop olivares.service refused by the class table: %+v", refusal)
	}
}

func TestUnits_ProtectedUnitRefusesEveryEffectIncludingStartAndEnable(t *testing.T) {
	// Starting systemd-reboot or systemd-poweroff is a power act, which the power helper admits
	// from the tty1 console alone; starting or enabling systemd-networkd, systemd-resolved or
	// systemd-sysupdate would make a second owner of the network or update plane.
	protected := []string{
		"systemd-reboot.service", "systemd-poweroff.service", "systemd-halt.service", "systemd-kexec.service",
		"systemd-soft-reboot.service", "systemd-networkd.service", "systemd-resolved.service", "systemd-sysupdate.service",
		"systemd-sysupdate.timer", "systemd-timesyncd.service", "NetworkManager.service", "dbus.service", "polkit.service",
		"olivares-portal.service", "olivares-helper-support-bundle.socket", "olivares-net-guard.service", "olivares-firewall.service",
	}
	ctx := context.Background()
	for _, unit := range protected {
		if got := Classify(unit); got != ClassProtected {
			t.Errorf("%s is in class %q, want protected", unit, got)
		}
		bus := newFakeBus(inactive(unit))
		bus.files[unit] = "disabled"
		x := Executor{Bus: bus, JobWait: 20 * time.Millisecond}
		for _, op := range []string{OpStart, OpStop, OpRestart, OpReload, OpEnable, OpDisable} {
			res := x.Apply(ctx, op, unit)
			if res.Refusal == nil || res.Refusal.Code != CodeUnitProtected || res.Refusal.Class != ClassProtected {
				t.Errorf("%s %s answered %+v, want unit_protected naming the protected class", op, unit, res.Refusal)
			}
			if refusal := Check(op, unit); refusal == nil || refusal.Code != CodeUnitProtected {
				t.Errorf("the class table admits %s %s", op, unit)
			}
		}
		if calls := bus.Calls(); len(calls) != 0 {
			t.Errorf("%s: a refused effect reached the service manager: %v", unit, calls)
		}
	}
	for _, op := range Ops() {
		if want := op == OpStatus || op == OpLogs; ClassProtected.Allows(op) != want {
			t.Errorf("the protected class admits %s: %v, want %v", op, !want, want)
		}
	}
	if strings.Contains(ClassProtected.Consequence(), "start") {
		t.Errorf("the protected consequence offers a start: %q", ClassProtected.Consequence())
	}
}

func TestUnits_ManagedTimersAreNamedNotMatchedByPrefix(t *testing.T) {
	// No timer is managed by the shape of its name: a timer is managed only when the class
	// table names it.
	for _, unit := range []string{
		"olivares-anything.timer", "olivares-firewall-guard.timer", "olivares-backup.timer", "olivares-.timer",
		"olivares-app-x.timer", "dnf5-automatic.timer", "apt-daily-upgrade.timer",
	} {
		if got := Classify(unit); got == ClassManaged {
			t.Errorf("%s is managed by its prefix", unit)
		}
		if refusal := Check(OpStart, unit); refusal == nil {
			t.Errorf("start %s passes the class table", unit)
		}
	}
	// The product unit, the named services and an optional app's unit stay managed; an app
	// unit's slug is a lowercase name.
	for _, unit := range []string{"olivares.service", "unattended-upgrades.service", "cron.service", "crond.service", "olivares-app-passbolt.service", "olivares-app-nginx-proxy-manager.service"} {
		if got := Classify(unit); got != ClassManaged {
			t.Errorf("%s is in class %q, want managed", unit, got)
		}
	}
	for _, unit := range []string{"olivares-app-.service", "olivares-app-Passbolt.service", "olivares-app-x@1.service", "olivares-app-a.b.service", "olivares-app--x.service"} {
		if got := Classify(unit); got == ClassManaged {
			t.Errorf("%s is managed, but its slug is not an app slug", unit)
		}
	}
}

func TestUnits_StorageOwnedMountUnitIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, unit := range []string{"srv-olivares-mnt-host-data.mount", "srv-olivares-mnt-host-7f3a.mount"} {
		if got := Classify(unit); got != ClassStorageOwned {
			t.Errorf("%s is in class %q, want storage-owned", unit, got)
		}
		bus := newFakeBus(Unit{Name: unit, LoadState: "loaded", ActiveState: "active", SubState: "mounted"})
		x := Executor{Bus: bus, JobWait: 20 * time.Millisecond}
		for _, op := range []string{OpStart, OpStop, OpRestart, OpReload, OpEnable, OpDisable} {
			res := x.Apply(ctx, op, unit)
			if res.Refusal == nil || res.Refusal.Code != CodeUnitProtected || res.Refusal.Class != ClassStorageOwned || res.Refusal.Owner != "olivares-portal-storage" {
				t.Errorf("%s %s answered %+v, want unit_protected naming the storage-owned class and olivares-portal-storage", op, unit, res.Refusal)
			}
		}
		if calls := bus.Calls(); len(calls) != 0 {
			t.Errorf("%s: a refused verb reached the service manager: %v", unit, calls)
		}
	}
	for _, op := range []string{OpStatus, OpLogs} {
		if !ClassStorageOwned.Allows(op) {
			t.Errorf("the storage-owned class refuses the read %s", op)
		}
	}
	// Only the Storage module's mount units are storage-owned: another mount unit, a service
	// named like one and a mount unit with an empty name after the prefix are not.
	for _, unit := range []string{"srv-data.mount", "srv-olivares-mnt-host-data.service", "srv-olivares-mnt-.mount", "srv-olivares-mnt-host-data.automount"} {
		if Classify(unit) == ClassStorageOwned {
			t.Errorf("%s is storage-owned", unit)
		}
	}
}

func TestUnits_JobResultIsReadFromJobRemovedAfterSubscribe(t *testing.T) {
	ctx := context.Background()
	unit := "olivares.service"
	for _, c := range []struct {
		op, method string
		before     Unit
		after      func(string) Unit
	}{
		{OpStart, "StartUnit", inactive(unit), active},
		{OpStop, "StopUnit", active(unit), inactive},
		{OpRestart, "RestartUnit", active(unit), active},
		{OpReload, "ReloadUnit", active(unit), active},
	} {
		bus := newFakeBus(c.before)
		// Before its reply, the manager reports another job's end and then this job's: only a
		// caller that subscribed before queueing sees them, and only its own job counts.
		bus.finish = func(_, u, job string) ([]JobRemoved, Unit) {
			return []JobRemoved{
				{ID: 1, Job: "/org/freedesktop/systemd1/job/1", Unit: "other.service", Result: "failed"},
				{ID: 2, Job: job, Unit: u, Result: "done"},
			}, c.after(u)
		}
		res := Executor{Bus: bus, JobWait: time.Second}.Apply(ctx, c.op, unit)
		if got := effects(bus.Calls()); !slices.Equal(got, []string{"Subscribe", c.method}) {
			t.Errorf("%s called %v, want Subscribe before %s and nothing else", c.op, got, c.method)
		}
		if !slices.Equal(bus.modes, []string{"fail"}) {
			t.Errorf("%s queued with modes %v, want fail", c.op, bus.modes)
		}
		if res.Refusal != nil || res.Failure != "" || res.Job == "" || res.JobResult != "done" {
			t.Errorf("%s answered %+v, want its own job's result, done", c.op, res)
		}
		if res.Before.ActiveState != c.before.ActiveState || res.After.ActiveState != c.after(unit).ActiveState || !res.After.Measured {
			t.Errorf("%s measured %+v before and %+v after", c.op, res.Before, res.After)
		}
	}
	for _, result := range []string{"canceled", "timeout", "failed", "dependency", "skipped"} {
		bus := newFakeBus(inactive(unit))
		bus.finish = finishWith(result, inactive)
		if res := (Executor{Bus: bus, JobWait: time.Second}).Apply(ctx, OpStart, unit); res.JobResult != result {
			t.Errorf("the job reported %s and the effect recorded %q", result, res.JobResult)
		}
	}
	// A job that never reports leaves no result: its operation stays running, outcome unknown.
	bus := newFakeBus(inactive(unit))
	bus.finish = func(_, u, _ string) ([]JobRemoved, Unit) { return nil, inactive(u) }
	if res := (Executor{Bus: bus, JobWait: 20 * time.Millisecond}).Apply(ctx, OpStart, unit); res.Job == "" || res.JobResult != "" || res.Failure != "" {
		t.Errorf("an unreported job answered %+v", res)
	}
	// Without the subscription no job is queued.
	bus = newFakeBus(inactive(unit))
	bus.subErr = errors.New("org.freedesktop.DBus.Error.AccessDenied")
	if res := (Executor{Bus: bus, JobWait: time.Second}).Apply(ctx, OpStart, unit); slices.Contains(bus.Calls(), "StartUnit") || res.Failure == "" {
		t.Errorf("a job was queued without its subscription: %v %+v", bus.Calls(), res)
	}
	// A call the manager refuses queues no job and is a failure.
	bus = newFakeBus(inactive(unit))
	bus.queueErr = errors.New("org.freedesktop.systemd1.TransactionIsDestructive")
	if res := (Executor{Bus: bus, JobWait: time.Second}).Apply(ctx, OpStart, unit); res.Failure == "" || res.Job != "" {
		t.Errorf("a refused call answered %+v", res)
	}
	// A unit outside the inventory is refused before the subscription.
	bus = newFakeBus()
	if res := (Executor{Bus: bus, JobWait: time.Second}).Apply(ctx, OpStart, "olivares-app-absent.service"); res.Refusal == nil || res.Refusal.Code != CodeInputRefused || len(effects(bus.Calls())) != 0 {
		t.Errorf("a unit outside the inventory answered %+v after %v", res, bus.Calls())
	}
	// enable and disable queue no job: they change the unit file and measure its state.
	bus = newFakeBus(inactive(unit))
	bus.files[unit] = "disabled"
	bus.fileAfter[unit] = "enabled"
	res := Executor{Bus: bus, JobWait: time.Second}.Apply(ctx, OpEnable, unit)
	if got := effects(bus.Calls()); !slices.Equal(got, []string{"EnableUnitFiles"}) || res.Changes != 1 || res.Before.UnitFileState != "disabled" || res.After.UnitFileState != "enabled" {
		t.Errorf("enable called %v and answered %+v", got, res)
	}
}

func TestServices_StatusAndListAreReadsThatNameTheClass(t *testing.T) {
	ctx := context.Background()
	bus := newFakeBus(active("olivares.service"), active("sshd.service"), active("dbus.service"), Unit{Name: "srv-olivares-mnt-host-data.mount", LoadState: "loaded", ActiveState: "active", SubState: "mounted"})
	bus.dependents["dbus.service"] = []string{"polkit.service", "NetworkManager.service"}
	x := Executor{Bus: bus}
	status, err := x.Status(ctx, "dbus.service")
	if err != nil || status.Class != ClassProtected || status.State.ActiveState != "active" || !slices.Equal(status.Allowed, []string{OpStatus, OpLogs}) || len(status.State.Dependents) != 2 || status.Consequence == "" {
		t.Errorf("status answered %+v %v", status, err)
	}
	if _, err := x.Status(ctx, "/etc/passwd"); err == nil {
		t.Error("status accepted a path")
	}
	if _, err := x.Status(ctx, "absent.service"); err == nil {
		t.Error("status answered a unit outside the inventory")
	}
	inventory, err := x.List(ctx)
	if err != nil || len(inventory.Units) != 4 || inventory.Truncated {
		t.Fatalf("list answered %+v %v", inventory, err)
	}
	classes := map[string]Class{}
	for _, u := range inventory.Units {
		classes[u.Name] = u.Class
	}
	if classes["sshd.service"] != ClassLockoutRisk || classes["srv-olivares-mnt-host-data.mount"] != ClassStorageOwned || classes["olivares.service"] != ClassManaged {
		t.Errorf("list classes %v", classes)
	}
	if got := effects(bus.Calls()); len(got) != 0 {
		t.Errorf("a read asked the manager for %v", got)
	}
}

// engineFor opens an operation model whose catalog admits the service task op.
func engineFor(t *testing.T, op string) *hostops.Engine {
	t.Helper()
	d := hostops.StatusDescriptor()
	d.ID, d.Module, d.Verb = "service."+op, Module, op
	catalog, err := hostops.NewCatalog([]hostops.Descriptor{d})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := hostops.OpenWithCatalog(t.TempDir(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

// record submits op on unit as one operation and records res's observation for it.
func record(t *testing.T, op, unit string, obs hostops.Observation) hostops.Record {
	t.Helper()
	engine := engineFor(t, op)
	cmd := NewCommand(operationID, "task.services", op, unit, 0, "product-up", "web", "user-1")
	if _, status, err := engine.Submit(cmd, nil); err != nil || status != hostops.StatusRunning {
		t.Fatalf("submit %s %s: %d %v", op, unit, status, err)
	}
	rec, err := engine.Recover(operationID, obs)
	if err != nil {
		t.Fatalf("recover %s %s: %v", op, unit, err)
	}
	return rec
}

func TestUnits_DoneWithAnotherActiveStateIsPartial(t *testing.T) {
	ctx := context.Background()
	unit := "olivares.service"
	for _, c := range []struct {
		name, op, result, want string
		before                 Unit
		after                  func(string) Unit
	}{
		{"start done and active", OpStart, "done", hostops.StateSucceeded, inactive(unit), active},
		{"start done but failed", OpStart, "done", hostops.StatePartial, inactive(unit), failed},
		{"start done but inactive", OpStart, "done", hostops.StatePartial, inactive(unit), inactive},
		{"restart done but failed", OpRestart, "done", hostops.StatePartial, active(unit), failed},
		{"stop done but active again", OpStop, "done", hostops.StatePartial, active(unit), active},
		{"stop done and inactive", OpStop, "done", hostops.StateSucceeded, active(unit), inactive},
		{"start failed with no change", OpStart, "failed", hostops.StateFailed, inactive(unit), inactive},
		{"start failed after a change", OpStart, "failed", hostops.StatePartial, inactive(unit), failed},
		{"start never reported", OpStart, "", hostops.StateRunning, inactive(unit), inactive},
	} {
		t.Run(c.name, func(t *testing.T) {
			bus := newFakeBus(c.before)
			bus.finish = func(_, u, job string) ([]JobRemoved, Unit) {
				if c.result == "" {
					return nil, c.after(u)
				}
				return []JobRemoved{{ID: 3, Job: job, Unit: u, Result: c.result}}, c.after(u)
			}
			res := Executor{Bus: bus, JobWait: 50 * time.Millisecond}.Apply(ctx, c.op, unit)
			rec := record(t, c.op, unit, res.Observation())
			if rec.State != c.want {
				t.Fatalf("recorded %q (%+v) for %+v, want %q", rec.State, rec, res, c.want)
			}
			after := "ActiveState=" + c.after(unit).ActiveState
			if c.want != hostops.StateRunning && !slices.Contains(rec.Postconditions, after) {
				t.Errorf("postconditions %v do not name the measured %s", rec.Postconditions, after)
			}
			if c.want == hostops.StateSucceeded && !slices.Contains(rec.Postconditions, "job_result=done") {
				t.Errorf("postconditions %v do not name the job's result", rec.Postconditions)
			}
		})
	}
	// enable and disable: the measured unit-file state decides.
	for _, c := range []struct {
		name, op, before, after, want string
	}{
		{"enable reaches enabled", OpEnable, "disabled", "enabled", hostops.StateSucceeded},
		{"disable reaches disabled", OpDisable, "enabled", "disabled", hostops.StateSucceeded},
		{"enable changes nothing", OpEnable, "static", "", hostops.StateFailed},
		{"enable reaches another state", OpEnable, "disabled", "indirect", hostops.StatePartial},
	} {
		t.Run(c.name, func(t *testing.T) {
			bus := newFakeBus(active(unit))
			bus.files[unit] = c.before
			if c.after != "" {
				bus.fileAfter[unit] = c.after
			}
			res := Executor{Bus: bus}.Apply(ctx, c.op, unit)
			if rec := record(t, c.op, unit, res.Observation()); rec.State != c.want {
				t.Fatalf("recorded %q for %+v, want %q", rec.State, res, c.want)
			}
		})
	}
	// A refused effect performed nothing.
	res := Executor{Bus: newFakeBus(active("dbus.service"))}.Apply(ctx, OpStop, "dbus.service")
	if rec := record(t, OpStop, "dbus.service", res.Observation()); rec.State != hostops.StateFailed || len(rec.Postconditions) != 0 {
		t.Fatalf("a refused stop recorded %+v", rec)
	}
}

func TestUnits_LogsIsAnAuthorizedReadNotAReadOnlySubcommand(t *testing.T) {
	rules := Rules()
	if len(rules) != len(Ops()) {
		t.Fatalf("the admission table has %d rules for %d subcommands", len(rules), len(Ops()))
	}
	for _, rule := range rules {
		readOnly := rule.Subcommand == OpList || rule.Subcommand == OpStatus
		if rule.Mutating == readOnly {
			t.Errorf("%s: Mutating=%v; only list and status are the read-only set", rule.Subcommand, rule.Mutating)
		}
	}
	portal := helperschema.Peer{UID: 990, Account: "olivares-portal", Unit: "olivares-portal.service", Attested: true}
	console := helperschema.Peer{UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: helperschema.RepairConsole.TTY, Attested: true}
	for _, c := range []struct {
		peer helperschema.Peer
		op   string
		ok   bool
	}{
		{portal, OpList, true}, {portal, OpLogs, true}, {portal, OpStop, true},
		{console, OpStatus, true}, {console, OpLogs, true}, {console, OpStop, false},
	} {
		if err := helperschema.Admit(c.peer, rules, c.op); (err == nil) != c.ok {
			t.Errorf("%s asking %s: %v, want admitted=%v", c.peer.Unit, c.op, err, c.ok)
		}
	}
	// Without a proven connection identity the logs read is refused like an effect.
	unproven := portal
	unproven.Attested = false
	var refusal *helperschema.Refusal
	if err := helperschema.Admit(unproven, rules, OpLogs); !errors.As(err, &refusal) || refusal.Code != helperschema.CodeNoConnectionIdentity {
		t.Errorf("an unproven peer asking logs: %v", err)
	}
	// A logs document carries the operation id of its act; a read carries none.
	for doc, ok := range map[string]bool{
		`{"op":"logs","unit":"olivares.service"}`:                                        false,
		`{"op":"logs","unit":"olivares.service","operation_id":"` + operationID + `"}`:   true,
		`{"op":"status","unit":"olivares.service","operation_id":"` + operationID + `"}`: false,
		`{"op":"status","unit":"olivares.service"}`:                                      true,
	} {
		if err := helperschema.Decode(strings.NewReader(doc), &Request{}); (err == nil) != ok {
			t.Errorf("%s: %v, want accepted=%v", doc, err, ok)
		}
	}
	// The logs task is an act of verb service for the units helper's audience, product-up or on
	// the tty1 console, with a confirmation; list and status are reads with no act.
	catalog, err := hostops.NewCatalog(Descriptors())
	if err != nil {
		t.Fatalf("the Services descriptors are refused: %v", err)
	}
	if got := len(catalog.Descriptors()); got != len(Ops()) {
		t.Fatalf("%d Services tasks for %d subcommands", got, len(Ops()))
	}
	logs, err := catalog.Describe(Module, OpLogs, "tui")
	if err != nil || logs.ActVerb != Module || logs.Audience != Audience || !slices.Equal(logs.Modes, []string{"product-up", "tty1"}) || logs.Confirmation != "confirm" || logs.Category != "Services" {
		t.Errorf("logs task %+v %v", logs, err)
	}
	for _, op := range []string{OpList, OpStatus} {
		d, err := catalog.Describe(Module, op, "web")
		if err != nil || d.ActVerb != "" || d.Audience != "" || d.Confirmation != "none" || !slices.Contains(d.Modes, "unavailable") {
			t.Errorf("%s task %+v %v", op, d, err)
		}
	}
	for _, op := range []string{OpStart, OpStop, OpRestart, OpReload, OpEnable, OpDisable} {
		d, err := catalog.Describe(Module, op, "cli")
		if err != nil || d.ActVerb != Module || d.Audience != Audience || !slices.Equal(d.Modes, []string{"product-up"}) || d.Confirmation != "confirm" {
			t.Errorf("%s task %+v %v", op, d, err)
		}
	}
	// A logs act is receipted performed with no host effect.
	read := Logs{Unit: "olivares.service", Lines: 10, Entries: []Entry{{Timestamp: "2026-09-27T12:00:00Z", Priority: "6", Message: "started"}}}
	rec := record(t, OpLogs, "olivares.service", read.Observation())
	if rec.State != hostops.StateSucceeded || !slices.Contains(rec.Postconditions, "no host effect") {
		t.Errorf("a logs act recorded %+v", rec)
	}
}

// logsDoc is a logs document for olivares.service with extra members.
func logsDoc(extra string) string {
	doc := `{"op":"logs","unit":"olivares.service","operation_id":"` + operationID + `"`
	if extra != "" {
		doc += "," + extra
	}
	return doc + "}"
}

// journalLine is one line of the journal reader's JSON output.
func journalLine(micros int64, priority, message string, extra map[string]any) string {
	fields := map[string]any{"__REALTIME_TIMESTAMP": strconv.FormatInt(micros, 10), "PRIORITY": priority, "MESSAGE": message}
	for k, v := range extra {
		fields[k] = v
	}
	data, _ := json.Marshal(fields)
	return string(data)
}

func TestUnits_LogsAreBoundedAndTakeNoPath(t *testing.T) {
	ctx := context.Background()
	// The document has four members, none of them a path, and lines is from 1 to 500 and only
	// for logs.
	names := map[string]bool{}
	fields := reflect.TypeOf(Request{})
	for i := 0; i < fields.NumField(); i++ {
		name, _, _ := strings.Cut(fields.Field(i).Tag.Get("json"), ",")
		names[name] = true
	}
	if len(names) != 4 || !names["op"] || !names["unit"] || !names["lines"] || !names["operation_id"] {
		t.Fatalf("the helper document's members are %v", names)
	}
	for doc, ok := range map[string]bool{
		logsDoc(""): true, logsDoc(`"lines":1`): true, logsDoc(`"lines":500`): true,
		logsDoc(`"lines":0`): false, logsDoc(`"lines":-1`): false, logsDoc(`"lines":501`): false, logsDoc(`"lines":"10"`): false,
		logsDoc(`"path":"/var/log/journal"`): false, logsDoc(`"directory":"/run/log"`): false, logsDoc(`"output":"cat"`): false,
		`{"op":"status","unit":"olivares.service","lines":10}`: false,
	} {
		if err := helperschema.Decode(strings.NewReader(doc), &Request{}); (err == nil) != ok {
			t.Errorf("%s: %v, want accepted=%v", doc, err, ok)
		}
	}
	var ran [][]string
	var limits []int64
	var output []byte
	var readerErr error
	journal := func(_ context.Context, argv []string, limit int64) ([]byte, error) {
		ran = append(ran, slices.Clone(argv))
		limits = append(limits, limit)
		return output, readerErr
	}
	bus := newFakeBus(active("olivares.service"))
	x := Executor{Bus: bus, Journal: journal}
	for _, unit := range []string{
		"/etc/passwd", "../olivares.service", "olivares.service/..", "/var/log/journal", "olivares*.service", "olivares?.service",
		"[a].service", "a b.service", "--output=cat", "--unit=a.service", "olivares.service --since=1970", `olivares\x2dx.service`,
		"olivares", "", strings.Repeat("a", 256) + ".service", "a\nb.service", "-.mount", "@.service", "a@.service",
	} {
		doc := `{"op":"logs","unit":` + strconv.Quote(unit) + `,"operation_id":"` + operationID + `"}`
		if err := helperschema.Decode(strings.NewReader(doc), &Request{}); err == nil {
			t.Errorf("the document accepts the unit %q", unit)
		}
		if _, err := x.Logs(ctx, unit, 10); err == nil {
			t.Errorf("logs read the unit %q", unit)
		}
	}
	for _, lines := range []int{0, -1, MaxLogLines + 1} {
		if _, err := x.Logs(ctx, "olivares.service", lines); err == nil {
			t.Errorf("logs read %d lines", lines)
		}
	}
	if _, err := x.Logs(ctx, "absent.service", 10); err == nil {
		t.Error("logs read a unit outside the inventory")
	}
	if len(ran) != 0 {
		t.Fatalf("the journal reader ran for a refused read: %v", ran)
	}
	// The one argument vector: the reader by absolute path, the unit from the inventory, JSON,
	// the bounded count and no pager.
	if got := LogArgv("sshd.service", 20); !slices.Equal(got, []string{"/usr/bin/journalctl", "--unit=sshd.service", "--output=json", "--lines=20", "--no-pager"}) {
		t.Fatalf("argv %q", got)
	}
	long := strings.Repeat("a", 2000) + "\x1b[31m"
	output = []byte(strings.Join([]string{
		journalLine(1790000000000000, "6", "started", map[string]any{"_CMDLINE": "/usr/bin/tool --token=hunter2", "_PID": "42"}),
		`{"__REALTIME_TIMESTAMP":"1790000001000000","PRIORITY":"3","MESSAGE":[104,105,255],"SECRET_FIELD":"hunter2"}`,
		`{"__REALTIME_TIMESTAMP":"1790000002000000","PRIORITY":"4","MESSAGE":null}`,
		journalLine(1790000003000000, "5", long, nil),
		"-- No entries --",
		"",
	}, "\n"))
	logs, err := x.Logs(ctx, "olivares.service", 10)
	if err != nil || len(ran) != 1 || !slices.Equal(ran[0], LogArgv("olivares.service", 10)) || limits[0] != MaxJournalBytes {
		t.Fatalf("logs %+v %v after %q with limit %v", logs, err, ran, limits)
	}
	if len(logs.Entries) != 4 || logs.Entries[0].Message != "started" || logs.Entries[0].Priority != "6" ||
		logs.Entries[0].Timestamp != time.UnixMicro(1790000000000000).UTC().Format(time.RFC3339Nano) || !strings.HasPrefix(logs.Entries[1].Message, "hi") {
		t.Fatalf("entries %+v", logs.Entries)
	}
	for _, e := range logs.Entries {
		data, _ := json.Marshal(e)
		var members map[string]any
		if json.Unmarshal(data, &members) != nil || len(members) != 3 {
			t.Errorf("an entry has members %v: time, priority and message only", members)
		}
		if len(e.Message) > MaxMessageBytes || !utf8.ValidString(e.Message) || strings.ContainsAny(e.Message, "\x1b\n\r") {
			t.Errorf("an unbounded or unsafe message: %q", e.Message)
		}
	}
	if answer, _ := json.Marshal(logs); strings.Contains(string(answer), "hunter2") || strings.Contains(string(answer), "_PID") {
		t.Errorf("the answer carries a field other than time, priority and message: %s", answer)
	}
	if got := effects(bus.Calls()); len(got) != 0 {
		t.Errorf("a logs read asked the manager for %v", got)
	}
	// More entries than asked keep the newest; the answer stays within its bound, newest kept.
	var many []string
	for i := int64(0); i < 600; i++ {
		many = append(many, journalLine(1790000000000000+i, "6", strings.Repeat("m", 2000), nil))
	}
	output = []byte(strings.Join(many, "\n"))
	logs, err = x.Logs(ctx, "olivares.service", MaxLogLines)
	answer, _ := json.Marshal(logs)
	if err != nil || !logs.Truncated || len(answer) > MaxAnswerBytes || len(logs.Entries) == 0 || len(logs.Entries) > MaxLogLines ||
		logs.Entries[len(logs.Entries)-1].Timestamp != time.UnixMicro(1790000000000599).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("a large read answered %d entries in %d bytes, truncated=%v, %v", len(logs.Entries), len(answer), logs.Truncated, err)
	}
	// A reader over its byte bound keeps what it read and says so.
	output = []byte(journalLine(1790000000000000, "6", "first", nil) + "\n" + `{"__REALTIME_TIMESTAMP":"17900`)
	readerErr = ErrJournalLimit
	logs, err = x.Logs(ctx, "olivares.service", 10)
	if err != nil || !logs.Truncated || len(logs.Entries) != 1 {
		t.Fatalf("a reader over its bound answered %+v %v", logs, err)
	}
}
