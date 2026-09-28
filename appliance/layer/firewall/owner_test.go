// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

// install gives o its first confirmed policy.
func install(t *testing.T, o *owner, d policy.Document) {
	t.Helper()
	if _, err := o.Install(context.Background(), d); err != nil {
		t.Fatalf("the first confirmed policy was not installed: %v", err)
	}
	if got := o.kernel.digest(t); got != policy.Digest(d) {
		t.Fatalf("the kernel holds %q after install, want %q", got, policy.Digest(d))
	}
}

// confirmedDigest is the digest of the confirmed policy on disk.
func confirmedDigest(t *testing.T, o *owner) string {
	t.Helper()
	d, ok, err := o.Confirmed()
	if err != nil || !ok {
		t.Fatalf("no confirmed policy: %v", err)
	}
	return policy.Digest(d)
}

// recorded is the hostops read model's state for the window's observation.
func recorded(t *testing.T, w firewall.Window) string {
	t.Helper()
	catalog, err := hostops.NewCatalog(firewall.Descriptors())
	if err != nil {
		t.Fatalf("the firewall descriptors are refused: %v", err)
	}
	e, err := hostops.OpenWithCatalog(t.TempDir(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	cmd := hostops.Command{OperationID: w.OperationID, TaskID: "firewall-change", Module: firewall.Module, Verb: firewall.VerbApply,
		Target: firewall.Target, PlanDigest: strings.TrimPrefix(w.CandidateDigest, "sha256:"), Mode: "product-up", Surface: "web", Actor: "operator"}
	if _, _, err := e.Submit(cmd, nil); err != nil {
		t.Fatalf("the operation was not recorded: %v", err)
	}
	rec, err := e.Recover(w.OperationID, firewall.Observe(w))
	if err != nil {
		t.Fatalf("the observation was refused: %v", err)
	}
	return rec.State
}

func TestFirewall_RemovingTheManagementRowIsRevertedNotApplied(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	confirmed := managed()
	install(t, o, confirmed)
	before, err := os.ReadFile(filepath.Join(o.StateDir, "confirmed.json"))
	if err != nil {
		t.Fatal(err)
	}

	// The plan shows the removal of the row carrying the operator's session as reverting unless
	// confirmed.
	candidate := managed()
	candidate.Portal.Enabled = false
	changes := policy.Diff(confirmed, candidate)
	if len(changes) != 1 || changes[0].Kind != "removed" || changes[0].Row.Port != "9443/tcp" || changes[0].Note != "reverts unless confirmed" {
		t.Fatalf("the plan is %+v, want the 9443 row removed, reverting unless confirmed", changes)
	}

	id := operationID(1)
	w, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id, Candidate: candidate, RevertAfter: 120 * time.Second})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if w.State != firewall.WindowPending || w.OperationID != id || w.CandidateDigest != policy.Digest(candidate) || w.PreviousDigest != policy.Digest(confirmed) {
		t.Fatalf("the window is %+v", w)
	}
	// The runtime ruleset changed; the confirmed policy did not.
	if got := o.kernel.digest(t); got != policy.Digest(candidate) {
		t.Errorf("the kernel holds %q, want the candidate", got)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(confirmed) {
		t.Errorf("apply changed the confirmed policy to %q", got)
	}
	if m, ok := o.published(t); !ok || m.PolicyDigest != policy.Digest(candidate) {
		t.Errorf("the measurement after apply is %+v (published %v)", m, ok)
	} else if _, open := rowOf(m.Rows, "9443/tcp"); open {
		t.Error("the measurement still shows the removed 9443 row")
	}
	if got := recorded(t, w); got != hostops.StateRunning {
		t.Errorf("a pending window is recorded %q, want running", got)
	}

	// Nobody confirms. Before the deadline the guard leaves the window; at it, the guard reverts.
	o.clock.now += 119 * time.Second
	if closed, err := o.Sweep(ctx); err != nil || len(closed) != 0 {
		t.Fatalf("before the deadline the guard closed %+v (%v)", closed, err)
	}
	o.clock.now += time.Second
	closed, err := o.Sweep(ctx)
	if err != nil || len(closed) != 1 {
		t.Fatalf("at the deadline the guard closed %+v (%v), want the window", closed, err)
	}
	w = closed[0]
	if w.State != firewall.WindowReverted || w.Reason != firewall.ReasonDeadline || len(w.Postconditions) == 0 {
		t.Errorf("the closed window is %+v, want reverted at the deadline with its measured postconditions", w)
	}
	if got := o.kernel.digest(t); got != policy.Digest(confirmed) {
		t.Errorf("after the deadline the kernel holds %q, want the confirmed policy", got)
	}
	m, ok := o.published(t)
	if !ok || m.PolicyDigest != policy.Digest(confirmed) {
		t.Fatalf("the measurement after the revert is %+v (published %v)", m, ok)
	}
	if r, open := rowOf(m.Rows, "9443/tcp"); !open || !slices.Equal(r.Interfaces, []string{"eth0"}) {
		t.Errorf("the 9443 row is not back on eth0: %+v", m.Rows)
	}
	if got := recorded(t, w); got != hostops.StateRolledBack {
		t.Errorf("the reverted window is recorded %q, want rolled_back", got)
	}

	// It was never applied: the confirmed policy is byte for byte the one before, and a late
	// confirmation changes nothing.
	after, err := os.ReadFile(filepath.Join(o.StateDir, "confirmed.json"))
	if err != nil || string(after) != string(before) {
		t.Errorf("the confirmed policy changed (%v)", err)
	}
	if _, err := o.Confirm(ctx, id); code(err) != firewall.CodeWindowExpired {
		t.Errorf("a confirmation after the revert: %v, want %s", err, firewall.CodeWindowExpired)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(confirmed) {
		t.Errorf("the late confirmation changed the confirmed policy to %q", got)
	}

	// The same removal, confirmed in time, is applied; its operation id is then a retrieval.
	id2 := operationID(2)
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id2, Candidate: candidate}); err != nil {
		t.Fatal(err)
	}
	w, err = o.Confirm(ctx, id2)
	if err != nil || w.State != firewall.WindowConfirmed {
		t.Fatalf("confirm in time: %+v %v", w, err)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(candidate) {
		t.Errorf("the confirmed policy is %q after confirmation", got)
	}
	loads := o.kernel.loads
	if again, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id2, Candidate: candidate}); err != nil || again.State != firewall.WindowConfirmed || o.kernel.loads != loads {
		t.Errorf("a resent apply re-ran or failed: %+v %v, loads %d -> %d", again, err, loads, o.kernel.loads)
	}
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id2, Candidate: confirmed}); code(err) != firewall.CodePlanChanged {
		t.Errorf("the same id with another plan: %v, want %s", err, firewall.CodePlanChanged)
	}
}

func TestFirewallGuard_RebootLoadsTheLastConfirmedPolicy(t *testing.T) {
	ctx := context.Background()

	// A boot before the first confirmed policy loads nothing and publishes nothing.
	empty := newOwner(t)
	if _, err := empty.BootLoad(ctx); code(err) != firewall.CodeNoConfirmedPolicy {
		t.Errorf("a boot without a confirmed policy: %v, want %s", err, firewall.CodeNoConfirmedPolicy)
	}
	if empty.kernel.loads != 0 {
		t.Error("a boot without a confirmed policy loaded a table")
	}
	if _, ok := empty.published(t); ok {
		t.Error("a boot without a confirmed policy published a measurement")
	}

	o := newOwner(t)
	confirmed := managed()
	install(t, o, confirmed)
	candidate := managed()
	candidate.Portal.ManagementInterfaces = []string{"eth1"}
	id := operationID(3)
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id, Candidate: candidate, RevertAfter: 600 * time.Second}); err != nil {
		t.Fatal(err)
	}

	// The host restarts inside the window: the boot loads the confirmed policy, never the pending one.
	o.reboot()
	m, err := o.BootLoad(ctx)
	if err != nil {
		t.Fatalf("boot load: %v", err)
	}
	if got := o.kernel.digest(t); got != policy.Digest(confirmed) || m.PolicyDigest != policy.Digest(confirmed) || m.BootID != bootB {
		t.Errorf("the boot loaded %q and measured %+v, want the confirmed policy in the new boot", got, m)
	}
	published, ok := o.published(t)
	if !ok || published.BootID != bootB || published.PolicyDigest != policy.Digest(confirmed) {
		t.Errorf("the boot's measurement is %+v (published %v)", published, ok)
	}
	w, err := o.Window(id)
	if err != nil || w.State != firewall.WindowReverted || w.Reason != firewall.ReasonReboot {
		t.Fatalf("the window after the reboot is %+v (%v), want reverted by the reboot", w, err)
	}
	if got := recorded(t, w); got != hostops.StateRolledBack {
		t.Errorf("the window closed by the reboot is recorded %q, want rolled_back", got)
	}
	if _, err := o.Confirm(ctx, id); code(err) != firewall.CodeWindowExpired {
		t.Errorf("a confirmation after the reboot: %v, want %s", err, firewall.CodeWindowExpired)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(confirmed) {
		t.Errorf("the confirmed policy is %q after the reboot", got)
	}

	// A confirmed change is what the next boot loads.
	id2 := operationID(4)
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id2, Candidate: candidate}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Confirm(ctx, id2); err != nil {
		t.Fatal(err)
	}
	o.reboot()
	if _, err := o.BootLoad(ctx); err != nil {
		t.Fatal(err)
	}
	if got := o.kernel.digest(t); got != policy.Digest(candidate) {
		t.Errorf("the boot after a confirmation loaded %q, want the confirmed candidate", got)
	}
	// The guard holds the network lock for its sweep; a busy lock is left for the next tick.
	o.lock.busy = true
	if closed, err := o.Sweep(ctx); err != nil || len(closed) != 0 {
		t.Errorf("a sweep under a busy lock: %+v %v", closed, err)
	}
	if o.lock.held != 0 {
		t.Errorf("the lock is held %d times after the calls returned", o.lock.held)
	}
}

func TestMeasurement_PublishedAfterEveryLoad(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	confirmed := managed()
	last := ""
	expect := func(step string, d policy.Document) {
		t.Helper()
		m, ok := o.published(t)
		if !ok {
			t.Fatalf("%s: no measurement was published", step)
		}
		if m.PolicyDigest != policy.Digest(d) || m.BootID != o.clock.boot || m.InputPolicy != "drop" {
			t.Errorf("%s: the measurement is %+v, want digest %s in boot %s with input drop", step, m, policy.Digest(d), o.clock.boot)
		}
		if want := policy.Rows(d); !slices.EqualFunc(m.Rows, want, func(a, b policy.MeasuredRow) bool {
			return a.Port == b.Port && slices.Equal(a.Interfaces, b.Interfaces)
		}) {
			t.Errorf("%s: the measured rows are %+v, want %+v", step, m.Rows, want)
		}
		if m.MeasuredAt == last {
			t.Errorf("%s: the measurement was not published again (measured_at %s)", step, m.MeasuredAt)
		}
		last = m.MeasuredAt
	}

	install(t, o, confirmed)
	expect("install", confirmed)
	candidate := managed()
	candidate.SSHPort = 2222
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(5), Candidate: candidate}); err != nil {
		t.Fatal(err)
	}
	expect("apply", candidate)
	if _, err := o.Revert(ctx, operationID(5)); err != nil {
		t.Fatal(err)
	}
	expect("revert", confirmed)
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(6), Candidate: candidate, RevertAfter: 60 * time.Second}); err != nil {
		t.Fatal(err)
	}
	expect("second apply", candidate)
	o.clock.now += 60 * time.Second
	if _, err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	expect("the guard's revert", confirmed)
	o.reboot()
	if _, err := o.BootLoad(ctx); err != nil {
		t.Fatal(err)
	}
	expect("boot", confirmed)

	// A load that fails leaves no measurement behind: unmeasured, never the previous one.
	o.kernel.failLoad = true
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(7), Candidate: candidate}); code(err) != firewall.CodeLoadFailed {
		t.Errorf("a failed load: %v, want %s", err, firewall.CodeLoadFailed)
	}
	if m, ok := o.published(t); ok {
		t.Errorf("a failed load left the measurement %+v", m)
	}
	// A table that does not carry the loaded policy's digest is not measured either.
	o.kernel.failLoad = false
	o.kernel.comment = policy.TableComment + "sha256:" + strings.Repeat("0", 64)
	if _, err := o.BootLoad(ctx); code(err) != firewall.CodeMeasurementFailed {
		t.Errorf("a foreign table: %v, want %s", err, firewall.CodeMeasurementFailed)
	}
	if m, ok := o.published(t); ok {
		t.Errorf("a foreign table was published as measured: %+v", m)
	}
}

func TestAppRowIsItsOwnAct(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	confirmed := managed()
	install(t, o, confirmed)
	proxy := policy.AppRow{App: "proxy", Port: "9444/tcp", Interfaces: []string{"eth0"}}
	withProxy := managed()
	withProxy.Apps = []policy.AppRow{proxy}

	loads := o.kernel.loads
	for name, c := range map[string]struct {
		app       string
		candidate func() policy.Document
	}{
		"a general change that adds an application row": {"", func() policy.Document { return withProxy }},
		"an application's act that also changes the console": {"proxy", func() policy.Document {
			d := withProxy
			d.Portal.Enabled = false
			return d
		}},
		"an application's act that adds another application's row": {"proxy", func() policy.Document {
			d := managed()
			d.Apps = []policy.AppRow{{App: "passbolt", Port: "8080/tcp", Interfaces: []string{"*"}}}
			return d
		}},
		"an application's act that changes nothing": {"proxy", managed},
	} {
		if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(8), App: c.app, Candidate: c.candidate()}); code(err) != firewall.CodeAppRowNotOwnAct {
			t.Errorf("%s: %v, want %s", name, err, firewall.CodeAppRowNotOwnAct)
		}
	}
	if o.kernel.loads != loads {
		t.Fatal("a refused act loaded a table")
	}
	if _, err := o.Window(operationID(8)); err == nil {
		t.Error("a refused act left a window")
	}

	// The application's own act adds exactly its rows under confirm-or-revert.
	w, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(9), App: "proxy", Candidate: withProxy})
	if err != nil || w.State != firewall.WindowPending || w.App != "proxy" {
		t.Fatalf("the application's act: %+v %v", w, err)
	}
	if _, err := o.Confirm(ctx, operationID(9)); err != nil {
		t.Fatal(err)
	}
	m, ok := o.published(t)
	if r, found := rowOf(m.Rows, "9444/tcp"); !ok || !found || !slices.Equal(r.Interfaces, []string{"eth0"}) {
		t.Errorf("the application's row is not measured on eth0: %+v", m.Rows)
	}
	// A general change keeps the application's rows; the application's own act removes them.
	general := withProxy
	general.SSHPort = 2222
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(10), Candidate: general}); err != nil {
		t.Errorf("a general change that keeps the application's rows: %v", err)
	}
	if _, err := o.Revert(ctx, operationID(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(11), App: "proxy", Candidate: managed()}); err != nil {
		t.Errorf("the application's act that removes its rows: %v", err)
	}

	// One task, two operations, one effect each: the application's install and its firewall row.
	d, found := describe(firewall.Descriptors(), firewall.VerbAppRow)
	if !found || d.ActVerb != "network" || d.Audience != "appliance-helper:olivares-portal-firewall" || d.Confirmation == "none" {
		t.Fatalf("the application row task is %+v (found %v), want its own confirmed network act", d, found)
	}
	appInstall := hostops.Descriptor{ID: "app.install", Module: "app", Verb: "install", Title: "Install an application", Category: "Apps",
		InputSchema:   hostops.InputSchema{Type: "object", Properties: map[string]hostops.InputField{"app": {Type: "string"}}, Required: []string{"app"}},
		Preconditions: []string{"An act authorization for the app verb"}, Consequence: "Install the application with its listeners bound and no firewall row.",
		Confirmation: "confirm", Modes: []string{"product-up"}, ActVerb: "app", Audience: "appliance-helper:olivares-portal-apps",
		EquivalentCommand: "olivares-appliance app install <app>", OutputSchema: "hostop-v1.schema.json", Surfaces: []string{"web", "cli", "tui"}}
	catalog, err := hostops.NewCatalog(append(firewall.Descriptors(), appInstall))
	if err != nil {
		t.Fatal(err)
	}
	e, err := hostops.OpenWithCatalog(t.TempDir(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	task := "install-proxy"
	appOp := hostops.Command{OperationID: operationID(12), TaskID: task, Module: "app", Verb: "install", Target: "app:proxy",
		PlanDigest: strings.Repeat("1", 64), Mode: "product-up", Surface: "web", Actor: "operator"}
	rowOp := hostops.Command{OperationID: w.OperationID, TaskID: task, Module: firewall.Module, Verb: firewall.VerbAppRow, Target: firewall.Target,
		PlanDigest: strings.TrimPrefix(w.CandidateDigest, "sha256:"), Mode: "product-up", Surface: "web", Actor: "operator"}
	for _, c := range []hostops.Command{appOp, rowOp} {
		if _, _, err := e.Submit(c, nil); err != nil {
			t.Fatalf("%s.%s was not recorded: %v", c.Module, c.Verb, err)
		}
	}
	grouped, err := e.Task(task)
	if err != nil || len(grouped.OperationIDs) != 2 || grouped.OperationIDs[0] == grouped.OperationIDs[1] {
		t.Errorf("the task groups %+v (%v), want two operations", grouped, err)
	}
}

// describe returns the descriptor of verb, or false.
func describe(ds []hostops.Descriptor, verb string) (hostops.Descriptor, bool) {
	for _, d := range ds {
		if d.Verb == verb {
			return d, true
		}
	}
	return hostops.Descriptor{}, false
}

// The guard shares the network lock with the NetworkManager plane, whose owner takes it without
// waiting: a tick with no window due reads the windows and leaves the lock alone.
func TestFirewallGuard_TakesTheNetworkLockOnlyWhenAWindowIsDue(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	install(t, o, managed())
	taken := o.lock.taken
	for i := 0; i < 3; i++ {
		if closed, err := o.Sweep(ctx); err != nil || len(closed) != 0 {
			t.Fatalf("a sweep with no window: %+v %v", closed, err)
		}
	}
	candidate := managed()
	candidate.SSHPort = 2222
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(13), Candidate: candidate, RevertAfter: 60 * time.Second}); err != nil {
		t.Fatal(err)
	}
	taken++
	if _, err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if o.lock.taken != taken {
		t.Errorf("the guard took the network lock %d times with no window due", o.lock.taken-taken)
	}
	o.clock.now += 60 * time.Second
	if closed, err := o.Sweep(ctx); err != nil || len(closed) != 1 || o.lock.taken != taken+1 {
		t.Errorf("a due window: closed %+v (%v), lock taken %d more times, want once", closed, err, o.lock.taken-taken)
	}
}
