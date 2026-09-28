// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// answersFixture is the host a restoration reads its answers on, as a case states it.
type answersFixture struct {
	published    bool
	publishedErr error
	selection    firewall.Selection
	present      bool
	reason       string
	sshPort      int
	sshErr       error
	links        []string
	linksErr     error
}

// validAnswers are the validated answers of an appliance whose console listens on eth0 and eth1:
// first boot published the selection, sshd reports port 22 and the client links are eth0 and eth1.
func validAnswers() *answersFixture {
	return &answersFixture{published: true, present: true, sshPort: 22, links: []string{"eth0", "eth1"},
		selection: firewall.Selection{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth1", "eth0"}}}
}

func (f *answersFixture) answers() firewall.Answers {
	return firewall.Answers{
		Published: func() (bool, error) { return f.published, f.publishedErr },
		Selection: func() (firewall.Selection, bool, string) { return f.selection, f.present, f.reason },
		SSHPort:   func(context.Context) (int, error) { return f.sshPort, f.sshErr },
		Links:     func() ([]string, error) { return slices.Clone(f.links), f.linksErr },
	}
}

// restoration is the record's closed schema, as the design names its members.
type restoration struct {
	OperationID    string `json:"operation_id"`
	BootID         string `json:"boot_id"`
	CreatedAt      string `json:"created_at"`
	TargetDigest   string `json:"target_digest"`
	PreviousDigest string `json:"previous_digest"`
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	MeasuredDigest string `json:"measured_digest,omitempty"`
}

// record reads the restoration record of id with its closed schema, and false when there is none.
func (o *owner) record(t *testing.T, id string) (restoration, bool) {
	t.Helper()
	path := filepath.Join(o.StateDir, "restorations", id+".json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return restoration{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("the restoration record is %v, want a regular file with mode 0600", info.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r restoration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		t.Fatalf("the restoration record does not follow its schema: %v\n%s", err, data)
	}
	if _, err := time.Parse(time.RFC3339, r.CreatedAt); err != nil || r.OperationID != id {
		t.Errorf("the record's time %q or operation id %q", r.CreatedAt, r.OperationID)
	}
	return r, true
}

// records counts the restoration records.
func (o *owner) records(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(o.StateDir, "restorations"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// closedConsole confirms, after install, a policy that closes the management 9443 row and keeps the
// application proxy's row, which that application's own act added and confirmed first.
func closedConsole(t *testing.T, o *owner) policy.Document {
	t.Helper()
	ctx := context.Background()
	install(t, o, managed())
	withProxy := managed()
	withProxy.Apps = []policy.AppRow{{App: "proxy", Port: "9444/tcp", Interfaces: []string{"eth0"}}}
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(14), App: "proxy", Candidate: withProxy}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Confirm(ctx, operationID(14)); err != nil {
		t.Fatal(err)
	}
	closed := withProxy
	closed.Portal.Enabled = false
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: operationID(15), Candidate: closed}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Confirm(ctx, operationID(15)); err != nil {
		t.Fatal(err)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(closed) {
		t.Fatalf("the confirmed policy is %q, want the one that closes 9443", got)
	}
	if m, ok := o.published(t); !ok {
		t.Fatal("no measurement after the confirmation")
	} else if _, open := rowOf(m.Rows, policy.ConsolePort); open {
		t.Fatal("the confirmed policy still admits 9443")
	}
	return closed
}

// localStatus decodes a status answer of the local entry point.
func localStatus(t *testing.T, response helperschema.Response) firewall.LocalStatus {
	t.Helper()
	if response.Result != helperschema.ResultAnswered {
		t.Fatalf("status: %+v", response)
	}
	var status firewall.LocalStatus
	decoder := json.NewDecoder(bytes.NewReader(response.Bundle))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		t.Fatalf("the status answer %s: %v", response.Bundle, err)
	}
	return status
}

func restoreDocument(id string) helperschema.FirewallLocalRequest {
	return helperschema.FirewallLocalRequest{Op: helperschema.FirewallLocalRestore, OperationID: id}
}

func TestRestore_TargetIsDerivedFromTheValidatedAnswers(t *testing.T) {
	ctx := context.Background()
	fixture := validAnswers()

	// The target is the policy first boot confirms from the same answers: SSH on the port sshd
	// reports, the product's ports, the 9443 row on the selected management interfaces, DHCPv6 on the
	// client links, and no application row.
	target, err := fixture.answers().Target(ctx)
	if err != nil {
		t.Fatalf("the target of valid answers: %v", err)
	}
	want := firewall.Initial(fixture.selection, 22, []string{"eth0", "eth1"})
	if policy.Digest(target) == "" || policy.Digest(target) != policy.Digest(want) || len(target.Apps) != 0 {
		t.Fatalf("the target is %+v, want first boot's policy of the same answers, %+v", target, want)
	}
	if r, open := rowOf(policy.Rows(target), policy.ConsolePort); !open || !slices.Equal(r.Interfaces, []string{"eth0", "eth1"}) {
		t.Fatalf("the target's 9443 row is %+v (open %v), want eth0 and eth1", r, open)
	}

	o := newOwner(t)
	closed := closedConsole(t, o)

	// The status read shows the confirmed policy, the target derived now and the application rows the
	// restoration drops; it changes nothing.
	loads := o.kernel.loads
	status := localStatus(t, firewall.LocalAnswer(ctx, o.Owner, fixture.answers(), helperschema.FirewallLocalRequest{Op: helperschema.FirewallLocalStatus}))
	if status.ConfirmedDigest != policy.Digest(closed) || status.TargetDigest != policy.Digest(target) || status.TargetRefusal != "" {
		t.Errorf("the status is %+v, want the confirmed policy and the derived target", status)
	}
	if !slices.EqualFunc(status.TargetRows, policy.Rows(target), func(a, b policy.MeasuredRow) bool {
		return a.Port == b.Port && slices.Equal(a.Interfaces, b.Interfaces)
	}) {
		t.Errorf("the status's target rows are %+v, want %+v", status.TargetRows, policy.Rows(target))
	}
	if len(status.DroppedAppRows) != 1 || status.DroppedAppRows[0].App != "proxy" || len(status.OpenWindows) != 0 {
		t.Errorf("the status shows dropped rows %+v and open windows %v, want the proxy's row and none", status.DroppedAppRows, status.OpenWindows)
	}
	if o.kernel.loads != loads || o.records(t) != 0 {
		t.Fatal("the status read loaded a table or recorded a restoration")
	}

	// restore derives the same target, records it, loads it, measures it and confirms it.
	id := operationID(16)
	response := firewall.LocalAnswer(ctx, o.Owner, fixture.answers(), restoreDocument(id))
	if response.Result != helperschema.ResultPerformed {
		t.Fatalf("restore: %+v", response)
	}
	var answered firewall.Restoration
	if err := json.Unmarshal(response.Bundle, &answered); err != nil || answered.State != firewall.RestorationRestored || answered.OperationID != id {
		t.Fatalf("the restore answer's record is %s (%v)", response.Bundle, err)
	}
	rec, found := o.record(t, id)
	if !found || rec.State != "restored" || rec.TargetDigest != policy.Digest(target) || rec.PreviousDigest != policy.Digest(closed) ||
		rec.MeasuredDigest != policy.Digest(target) || rec.BootID != bootA || rec.Reason != "" {
		t.Fatalf("the restoration record is %+v (found %v)", rec, found)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(target) {
		t.Errorf("the confirmed policy is %q after the restoration, want the target", got)
	}
	if got := o.kernel.digest(t); got != policy.Digest(target) {
		t.Errorf("the kernel holds %q, want the target", got)
	}
	m, ok := o.published(t)
	if !ok || m.PolicyDigest != policy.Digest(target) {
		t.Fatalf("the measurement after the restoration is %+v (published %v)", m, ok)
	}
	if r, open := rowOf(m.Rows, policy.ConsolePort); !open || !slices.Equal(r.Interfaces, []string{"eth0", "eth1"}) {
		t.Errorf("the measured 9443 row is %+v (open %v), want eth0 and eth1", r, open)
	}
	if _, open := rowOf(m.Rows, "9444/tcp"); open {
		t.Error("the application's row survived the restoration; it returns through its own act")
	}

	// The same operation id with the same target is the record, and loads nothing again; with another
	// target it refuses plan_changed.
	loads = o.kernel.loads
	again := firewall.LocalAnswer(ctx, o.Owner, fixture.answers(), restoreDocument(id))
	if again.Result != helperschema.ResultPerformed || o.kernel.loads != loads {
		t.Errorf("a resent restore: %+v, loads %d -> %d", again, loads, o.kernel.loads)
	}
	other := firewall.Initial(firewall.Selection{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}}, 22, nil)
	if _, err := o.Restore(ctx, id, other); code(err) != firewall.CodePlanChanged {
		t.Errorf("the same id with another target: %v, want %s", err, firewall.CodePlanChanged)
	}
	// A target already in force is refused under a new id, and records nothing.
	if _, err := o.Restore(ctx, operationID(17), target); code(err) != firewall.CodeAlreadyInForce {
		t.Errorf("a restoration of the policy in force: %v, want %s", err, firewall.CodeAlreadyInForce)
	}
	if o.kernel.loads != loads || o.records(t) != 1 {
		t.Errorf("a refused restoration loaded (%d -> %d) or recorded (%d records)", loads, o.kernel.loads, o.records(t))
	}

	// The restored policy is the one every boot loads.
	o.reboot()
	if _, err := o.BootLoad(ctx); err != nil {
		t.Fatal(err)
	}
	if got := o.kernel.digest(t); got != policy.Digest(target) {
		t.Errorf("the boot after the restoration loaded %q, want the target", got)
	}
	if rec, _ := o.record(t, id); rec.State != "restored" {
		t.Errorf("after the reboot the record says %q", rec.State)
	}
}

func TestRestore_MissingOrInvalidAnswersRefuseAndLoadNothing(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		change func(*answersFixture)
		code   string
	}{
		"first boot has not published the selection": {func(f *answersFixture) { f.published = false }, firewall.CodeAnswersInvalid},
		"the first-boot record cannot be read":       {func(f *answersFixture) { f.publishedErr = errors.New("unreadable") }, firewall.CodeAnswersInvalid},
		"the selection file is removed":              {func(f *answersFixture) { f.present, f.selection = false, firewall.Selection{} }, firewall.CodeAnswersInvalid},
		"the selection file is corrupted": {func(f *answersFixture) {
			f.selection, f.reason = firewall.Selection{}, "the published selection does not follow its schema"
		}, firewall.CodeAnswersInvalid},
		"the selection admits no policy": {func(f *answersFixture) { f.selection.ManagementInterfaces = []string{"bad name"} }, firewall.CodeAnswersInvalid},
		"sshd reports no port":           {func(f *answersFixture) { f.sshErr = errors.New("sshd reports no port") }, firewall.CodeSSHPortUnmeasured},
		"the links cannot be listed":     {func(f *answersFixture) { f.linksErr = errors.New("unreadable") }, firewall.CodeLinksUnmeasured},
	} {
		t.Run(name, func(t *testing.T) {
			f := validAnswers()
			c.change(f)
			if _, err := f.answers().Target(ctx); code(err) != c.code {
				t.Fatalf("the target: %v, want %s", err, c.code)
			}
			o := newOwner(t)
			closed := closedConsole(t, o)
			loads := o.kernel.loads
			before, err := os.ReadFile(filepath.Join(o.StateDir, "confirmed.json"))
			if err != nil {
				t.Fatal(err)
			}
			response := firewall.LocalAnswer(ctx, o.Owner, f.answers(), restoreDocument(operationID(18)))
			if response.Result != helperschema.ResultRefused || response.Code != c.code {
				t.Errorf("restore: %+v, want refused %s", response, c.code)
			}
			if o.kernel.loads != loads || o.kernel.digest(t) != policy.Digest(closed) {
				t.Error("a restoration without its answers loaded a table")
			}
			if after, err := os.ReadFile(filepath.Join(o.StateDir, "confirmed.json")); err != nil || !bytes.Equal(after, before) {
				t.Errorf("the confirmed policy changed (%v)", err)
			}
			if o.records(t) != 0 {
				t.Error("a restoration without its answers was recorded")
			}
			// The status read names the refusal and shows no target.
			status := localStatus(t, firewall.LocalAnswer(ctx, o.Owner, f.answers(), helperschema.FirewallLocalRequest{Op: helperschema.FirewallLocalStatus}))
			if status.TargetRefusal != c.code || status.TargetDigest != "" || len(status.TargetRows) != 0 {
				t.Errorf("the status shows target %q rows %v refusal %q, want only the refusal %s", status.TargetDigest, status.TargetRows, status.TargetRefusal, c.code)
			}
		})
	}

	t.Run("an answers reader that is not wired refuses", func(t *testing.T) {
		if _, err := (firewall.Answers{}).Target(ctx); code(err) != firewall.CodeAnswersInvalid {
			t.Errorf("no readers: %v, want %s", err, firewall.CodeAnswersInvalid)
		}
	})

	t.Run("the owner refuses a target that is not a derived one", func(t *testing.T) {
		o := newOwner(t)
		closedConsole(t, o)
		loads := o.kernel.loads
		withApp := managed()
		withApp.Apps = []policy.AppRow{{App: "proxy", Port: "9444/tcp", Interfaces: []string{"eth0"}}}
		invalid := managed()
		invalid.SSHPort = 0
		for name, target := range map[string]policy.Document{"a target with an application row": withApp, "a target outside the schema": invalid} {
			if _, err := o.Restore(ctx, operationID(19), target); code(err) != firewall.CodeInputRefused {
				t.Errorf("%s: %v, want %s", name, err, firewall.CodeInputRefused)
			}
		}
		if _, err := o.Restore(ctx, "not-an-operation-id", managed()); code(err) != firewall.CodeInputRefused {
			t.Errorf("a malformed operation id: %v", err)
		}
		if o.kernel.loads != loads || o.records(t) != 0 {
			t.Error("a refused target loaded or recorded")
		}
	})
}

// observingKernel is the fake kernel with a look at the host before each load.
type observingKernel struct {
	*fakeKernel
	before func(ruleset []byte)
}

func (k observingKernel) Load(ctx context.Context, ruleset []byte) error {
	k.before(ruleset)
	return k.fakeKernel.Load(ctx, ruleset)
}

func TestRestore_RecordIsWrittenBeforeTheLoad(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	closed := closedConsole(t, o)
	target, err := validAnswers().answers().Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := operationID(20)
	var seen []restoration
	o.Owner.Kernel = observingKernel{fakeKernel: o.kernel, before: func(ruleset []byte) {
		rec, found := o.record(t, id)
		if !found {
			t.Error("the target was loaded before its restoration was recorded")
			return
		}
		if strings.Contains(string(ruleset), policy.Digest(target)) {
			seen = append(seen, rec)
		}
	}}
	if _, err := o.Restore(ctx, id, target); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(seen) != 1 || seen[0].State != "pending" || seen[0].TargetDigest != policy.Digest(target) ||
		seen[0].PreviousDigest != policy.Digest(closed) || seen[0].BootID != bootA || seen[0].MeasuredDigest != "" {
		t.Fatalf("before the load the record was %+v, want pending with its target and the previous policy", seen)
	}

	t.Run("a record that cannot be created exclusively loads nothing and replaces nothing", func(t *testing.T) {
		o := newOwner(t)
		closedConsole(t, o)
		loads := o.kernel.loads
		dir := filepath.Join(o.StateDir, "restorations")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte("kept\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, operationID(21)+".json")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, operationID(22)+".json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{operationID(21), operationID(22)} {
			if _, err := o.Restore(ctx, id, target); code(err) != firewall.CodeStateUnreadable {
				t.Errorf("a planted record %s: %v, want %s", id[:1], err, firewall.CodeStateUnreadable)
			}
		}
		if data, err := os.ReadFile(outside); err != nil || string(data) != "kept\n" {
			t.Errorf("the link's target was written: %q (%v)", data, err)
		}
		if o.kernel.loads != loads {
			t.Error("a restoration whose record could not be created loaded a table")
		}
	})

	t.Run("a clock that cannot be read records and loads nothing", func(t *testing.T) {
		o := newOwner(t)
		closedConsole(t, o)
		loads := o.kernel.loads
		o.Owner.Clock = brokenClock{}
		if _, err := o.Restore(ctx, operationID(23), target); code(err) != firewall.CodeStateUnreadable {
			t.Errorf("restore without a clock: %v, want %s", err, firewall.CodeStateUnreadable)
		}
		if o.kernel.loads != loads || o.records(t) != 0 {
			t.Error("a restoration without this boot's clock loaded or recorded")
		}
	})
}

// brokenClock cannot read this boot's clock.
type brokenClock struct{}

func (brokenClock) Now() (string, time.Duration, error) {
	return "", 0, errors.New("clock_gettime: EINVAL")
}

// failingKernel is the fake kernel that fails at the target: its load fails, or, with measure, the
// load succeeds and the kernel then reports a foreign table. With again, the confirmed policy does
// not load again either.
type failingKernel struct {
	*fakeKernel
	target  string
	measure bool
	again   bool
}

func (k failingKernel) Load(ctx context.Context, ruleset []byte) error {
	isTarget := strings.Contains(string(ruleset), k.target)
	if (isTarget && !k.measure) || (!isTarget && k.again) {
		return errors.New("netlink: Error: Could not process rule: Operation not permitted")
	}
	return k.fakeKernel.Load(ctx, ruleset)
}

func (k failingKernel) Table(ctx context.Context) (firewall.Table, error) {
	table, err := k.fakeKernel.Table(ctx)
	if err == nil && k.measure && strings.Contains(table.Comment, k.target) {
		table.Comment = policy.TableComment + "sha256:" + strings.Repeat("0", 64)
	}
	return table, err
}

func TestRestore_FailedLoadReloadsTheConfirmedPolicyAndRecordsFailed(t *testing.T) {
	ctx := context.Background()
	fixture := validAnswers()
	target, err := fixture.answers().Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		kernel   func(*fakeKernel) failingKernel
		code     string
		reloaded bool
	}{
		"the target does not load": {func(k *fakeKernel) failingKernel {
			return failingKernel{fakeKernel: k, target: policy.Digest(target)}
		}, firewall.CodeLoadFailed, true},
		"the target loads and does not measure": {func(k *fakeKernel) failingKernel {
			return failingKernel{fakeKernel: k, target: policy.Digest(target), measure: true}
		}, firewall.CodeMeasurementFailed, true},
		"neither the target nor the confirmed policy loads": {func(k *fakeKernel) failingKernel {
			return failingKernel{fakeKernel: k, target: policy.Digest(target), again: true}
		}, firewall.CodeLoadFailed, false},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOwner(t)
			closed := closedConsole(t, o)
			before, err := os.ReadFile(filepath.Join(o.StateDir, "confirmed.json"))
			if err != nil {
				t.Fatal(err)
			}
			o.Owner.Kernel = c.kernel(o.kernel)
			id := operationID(24)
			response := firewall.LocalAnswer(ctx, o.Owner, fixture.answers(), restoreDocument(id))
			// Negative control: a restoration whose load failed is never reported, or recorded, restored.
			if response.Result != helperschema.ResultFailed || response.Code != c.code {
				t.Errorf("restore: %+v, want failed %s", response, c.code)
			}
			rec, found := o.record(t, id)
			if !found || rec.State != "failed" || rec.Reason != c.code || rec.TargetDigest != policy.Digest(target) || rec.PreviousDigest != policy.Digest(closed) {
				t.Fatalf("the record is %+v (found %v), want failed with the reason %s", rec, found, c.code)
			}
			if after, err := os.ReadFile(filepath.Join(o.StateDir, "confirmed.json")); err != nil || !bytes.Equal(after, before) {
				t.Errorf("a failed restoration changed the confirmed policy (%v)", err)
			}
			m, published := o.published(t)
			if c.reloaded {
				if rec.MeasuredDigest != policy.Digest(closed) || o.kernel.digest(t) != policy.Digest(closed) || !published || m.PolicyDigest != policy.Digest(closed) {
					t.Errorf("after the failure the kernel holds %q, measured %q (published %v), recorded %q; want the confirmed policy loaded again",
						o.kernel.digest(t), m.PolicyDigest, published, rec.MeasuredDigest)
				}
			} else if rec.MeasuredDigest != "" || published {
				t.Errorf("an unmeasured reload recorded %q and published %v", rec.MeasuredDigest, published)
			}
			// The same id is the recorded failure, loaded no further, and never restored.
			loads := o.kernel.loads
			again := firewall.LocalAnswer(ctx, o.Owner, fixture.answers(), restoreDocument(id))
			if again.Result == helperschema.ResultPerformed || o.kernel.loads != loads {
				t.Errorf("the same id after a failure: %+v, loads %d -> %d", again, loads, o.kernel.loads)
			}
			if rec, _ := o.record(t, id); rec.State != "failed" {
				t.Errorf("the record now says %q", rec.State)
			}
		})
	}
}

func TestRestore_OpenWindowRefusesTargetLocked(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	closedConsole(t, o)
	target, err := validAnswers().answers().Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := o.Confirmed()
	if err != nil {
		t.Fatal(err)
	}
	candidate.SSHPort = 2222
	window := operationID(25)
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: window, Candidate: candidate}); err != nil {
		t.Fatal(err)
	}
	loads := o.kernel.loads
	_, err = o.Restore(ctx, operationID(26), target)
	var refusal *firewall.Refusal
	if !errors.As(err, &refusal) || refusal.Code != firewall.CodeTargetLocked || refusal.OperationID != window {
		t.Fatalf("a restoration during an open window: %v, want %s naming the window", err, firewall.CodeTargetLocked)
	}
	if o.kernel.loads != loads || o.records(t) != 0 {
		t.Fatal("a restoration during an open window loaded or recorded")
	}
	if w, err := o.Window(window); err != nil || w.State != firewall.WindowPending {
		t.Fatalf("the open window is %+v (%v), want it untouched", w, err)
	}

	// A busy network lock refuses the same way and records nothing.
	o.lock.busy = true
	if _, err := o.Restore(ctx, operationID(26), target); code(err) != firewall.CodeTargetLocked {
		t.Errorf("a busy network lock: %v, want %s", err, firewall.CodeTargetLocked)
	}
	o.lock.busy = false

	// The operator reverts the window first, explicitly; then the restoration proceeds.
	if _, err := o.Revert(ctx, window); err != nil {
		t.Fatal(err)
	}
	if r, err := o.Restore(ctx, operationID(26), target); err != nil || r.State != firewall.RestorationRestored {
		t.Fatalf("the restoration after the revert: %+v %v", r, err)
	}
	if o.lock.held != 0 {
		t.Errorf("the lock is held %d times after the calls returned", o.lock.held)
	}
}

func TestRestore_IsNotRevert(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	closed := closedConsole(t, o)

	// Reverting the confirmed window that closed 9443 is refused: a revert is for an unconfirmed
	// window, and this one was confirmed.
	if _, err := o.Revert(ctx, operationID(15)); code(err) != firewall.CodeWindowConfirmed {
		t.Fatalf("a revert of the confirmed window: %v, want %s", err, firewall.CodeWindowConfirmed)
	}
	if got := confirmedDigest(t, o); got != policy.Digest(closed) {
		t.Fatalf("the refused revert changed the confirmed policy to %q", got)
	}
	// So is the local entry point's revert, which is the owner's.
	revert := firewall.LocalAnswer(ctx, o.Owner, validAnswers().answers(),
		helperschema.FirewallLocalRequest{Op: helperschema.FirewallLocalRevert, OperationID: operationID(15)})
	if revert.Result != helperschema.ResultRefused || revert.Code != firewall.CodeWindowConfirmed {
		t.Errorf("the local revert of the confirmed window: %+v, want refused %s", revert, firewall.CodeWindowConfirmed)
	}

	// The restoration is its own explicit act: it opens no window and leaves the confirmed window as
	// it was.
	id := operationID(27)
	response := firewall.LocalAnswer(ctx, o.Owner, validAnswers().answers(), restoreDocument(id))
	if response.Result != helperschema.ResultPerformed {
		t.Fatalf("restore: %+v", response)
	}
	if _, err := o.Window(id); code(err) != firewall.CodeNoSuchWindow {
		t.Errorf("the restoration's id names a window: %v", err)
	}
	if _, err := o.Revert(ctx, id); code(err) != firewall.CodeNoSuchWindow {
		t.Errorf("a revert of the restoration: %v, want %s", err, firewall.CodeNoSuchWindow)
	}
	if w, err := o.Window(operationID(15)); err != nil || w.State != firewall.WindowConfirmed {
		t.Errorf("the confirmed window is %+v (%v) after the restoration", w, err)
	}
	windows, err := o.Windows()
	if err != nil || slices.ContainsFunc(windows, func(w firewall.Window) bool { return w.State == firewall.WindowPending }) {
		t.Errorf("a window is open after the restoration: %+v (%v)", windows, err)
	}
}
