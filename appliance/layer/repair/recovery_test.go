// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
)

const (
	powerID    = "0123456789abcdef0123456789abcdef"
	networkID  = "fedcba9876543210fedcba9876543210"
	otherID    = "00112233445566778899aabbccddeeff"
	bootID     = "3f1c2a8e-5b7d-4e21-9a0c-6d2f8b4e1a73"
	oldBootID  = "11111111-2222-4333-8444-555555555555"
	generation = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	sessionRef = "5e55105e55105e55105e55105e55105e"
)

// console is the repair console's own process as the kernel describes it.
var console = helperschema.Peer{UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1", Attested: true}

// pendingStatus is the guard's status of the one window whose mutating reply was lost.
func pendingStatus() netguard.Status {
	return netguard.Status{ObservedBootID: bootID, OperationID: networkID, State: netguard.StateAwaitingConfirmation, BootID: bootID,
		WindowGeneration: generation, PendingCalls: 1, DeadlineRemaining: 30 * time.Second}
}

// guard answers status with the windows it holds, in the boot it states.
type guard struct {
	boot    string
	windows []netguard.Status
	code    string
	err     error
}

func (g *guard) Status(context.Context) (netguard.EdgeResponse, error) {
	if g.err != nil {
		return netguard.EdgeResponse{}, g.err
	}
	code := g.code
	if code == "" {
		code = "ok"
	}
	return netguard.EdgeResponse{Code: code, BootID: g.boot, OpenWindows: slices.Clone(g.windows)}, nil
}

func currentBoot() (string, error) { return bootID, nil }

// noHold serializes nothing: the cases that measure a gate alone.
type noHold struct{ held *int }

func (h noHold) Hold(context.Context) (func(), error) {
	if h.held != nil {
		*h.held++
	}
	return func() {}, nil
}

// failing is a gate that refuses.
type failing struct{ name string }

func (f failing) Name() string                                { return f.name }
func (f failing) Check(context.Context, PendingNetwork) error { return errors.New(f.name + " refuses") }

// linkageDir returns a protected linkage directory of this process's own.
func linkageDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "recovery")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// dirWithMode returns a fresh directory of this process's own with mode.
func dirWithMode(t *testing.T, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "recovery")
	if err := os.Mkdir(dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

// store is a linkage store in a protected directory of this process's own.
func store(t *testing.T) LinkageStore {
	t.Helper()
	return LinkageStore{Dir: linkageDir(t), Owner: uint32(os.Getuid())}
}

// planFor is the recovery-reboot plan the console displayed for the pending window.
func planFor(t *testing.T, g *guard) Plan {
	t.Helper()
	response, _ := g.Status(context.Background())
	pending, err := PendingFromStatus(response, bootID)
	if err != nil {
		t.Fatalf("control: the pending window was not identified: %v", err)
	}
	return Plan{PowerOperationID: powerID, Action: ActionReboot, Pending: pending, SessionRef: sessionRef}
}

// admission is an admission with the window gate over g, the given gates, and s.
func admission(g *guard, s Linker, gates ...Gate) Admission {
	return Admission{
		Invoker:    func() (helperschema.Peer, error) { return console, nil },
		Serializer: noHold{},
		Gates:      append([]Gate{WindowGate{Status: g.Status, Boot: currentBoot}}, gates...),
		Store:      s,
		Now:        func() time.Time { return time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC) },
	}
}

// signedIn and signedOut are the console's sign-in as the admission asks it.
func signedIn() bool  { return true }
func signedOut() bool { return false }

// effect counts power effects and fails the case if one runs while forbidden.
type effect struct{ runs int }

func (e *effect) run(context.Context) error {
	e.runs++
	return nil
}

// refusedBy asserts that err is a refusal naming gate, and that nothing ran and nothing was linked.
func refusedBy(t *testing.T, err error, gate string, e *effect, s LinkageStore) {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) || (gate != "" && refusal.Gate != gate) {
		t.Fatalf("got %v, want a refusal by %q", err, gate)
	}
	if e.runs != 0 {
		t.Fatalf("a refused recovery reboot ran its effect %d times", e.runs)
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("a refused recovery reboot left a linkage record: %v", entries)
	}
}

func TestRecoveryPower_ExactPendingWindowOnly(t *testing.T) {
	g := &guard{boot: bootID, windows: []netguard.Status{pendingStatus()}}
	plan := planFor(t, g)

	t.Run("control: the exact pending window is admitted", func(t *testing.T) {
		s, e := store(t), &effect{}
		if err := admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run); err != nil || e.runs != 1 {
			t.Fatalf("the exact pending window was refused (%v) or ran %d times", err, e.runs)
		}
	})

	window := func(change func(*netguard.Status)) []netguard.Status {
		w := pendingStatus()
		change(&w)
		return []netguard.Status{w}
	}
	for name, g := range map[string]*guard{
		"another window":                   {boot: bootID, windows: window(func(w *netguard.Status) { w.OperationID = otherID })},
		"a stale boot in the answer":       {boot: oldBootID, windows: []netguard.Status{pendingStatus()}},
		"a window from an earlier boot":    {boot: bootID, windows: window(func(w *netguard.Status) { w.BootID = oldBootID })},
		"another window generation":        {boot: bootID, windows: window(func(w *netguard.Status) { w.WindowGeneration = otherID })},
		"a confirmed window":               {boot: bootID, windows: window(func(w *netguard.Status) { w.State = netguard.StateConfirmed })},
		"a rolled-back window":             {boot: bootID, windows: window(func(w *netguard.Status) { w.State = netguard.StateRolledBack })},
		"a window with no pending effect":  {boot: bootID, windows: window(func(w *netguard.Status) { w.PendingCalls = 0 })},
		"a window whose facts changed":     {boot: bootID, windows: window(func(w *netguard.Status) { w.RecoveryAttempt = 2 })},
		"the confirmed window and another": {boot: bootID, windows: append([]netguard.Status{pendingStatus()}, window(func(w *netguard.Status) { w.OperationID = otherID })...)},
		"no window":                        {boot: bootID},
		"a status that is not ok":          {boot: bootID, code: "network_status_unknown", windows: []netguard.Status{pendingStatus()}},
		"an unreadable status":             {err: errors.New("network_guard_unavailable")},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			s, e := store(t), &effect{}
			refusedBy(t, admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "pending network window", e, s)
		})
	}

	t.Run("refused: a plan other than the one displayed", func(t *testing.T) {
		for name, change := range map[string]func(*Plan){
			"shutdown instead of reboot":  func(p *Plan) { p.Action = helperschema.PowerShutdown },
			"another power operation":     func(p *Plan) { p.PowerOperationID = otherID },
			"another pending operation":   func(p *Plan) { p.Pending.OperationID = otherID },
			"another session":             func(p *Plan) { p.SessionRef = otherID },
			"another window generation":   func(p *Plan) { p.Pending.WindowGeneration = otherID },
			"a malformed power operation": func(p *Plan) { p.PowerOperationID = "../DO-NOT-PRINT" },
			"a malformed pending boot":    func(p *Plan) { p.Pending.BootID = "boot" },
			"a malformed status digest":   func(p *Plan) { p.Pending.StatusDigest = "digest" },
			"no pending network at all":   func(p *Plan) { p.Pending = PendingNetwork{} },
			"no session reference":        func(p *Plan) { p.SessionRef = "" },
		} {
			s, e := store(t), &effect{}
			changed := plan
			change(&changed)
			// The console confirmed the displayed plan's digest; the applied plan differs from it.
			err := admission(g, s).Recover(context.Background(), changed, plan.Digest(), signedIn, e.run)
			if err == nil || e.runs != 0 {
				t.Errorf("%s: admitted (%v), ran %d times", name, err, e.runs)
			}
			if err != nil && strings.Contains(err.Error(), "DO-NOT-PRINT") {
				t.Errorf("%s: the refusal repeats the value: %v", name, err)
			}
		}
	})

	t.Run("refused: ordinary power while any window is open, pending or not", func(t *testing.T) {
		for _, g := range []*guard{
			{boot: bootID, windows: []netguard.Status{pendingStatus()}},
			{boot: bootID, windows: window(func(w *netguard.Status) { w.PendingCalls = 0 })},
		} {
			s, e := store(t), &effect{}
			refusedBy(t, admission(g, s).Ordinary(context.Background(), signedIn, e.run), "pending network window", e, s)
		}
		s, e := store(t), &effect{}
		if err := admission(&guard{boot: bootID}, s).Ordinary(context.Background(), signedIn, e.run); err != nil || e.runs != 1 {
			t.Fatalf("control: ordinary power with no open window was refused (%v)", err)
		}
		if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
			t.Fatalf("ordinary power wrote a recovery linkage: %v", entries)
		}
	})
}

func TestRecoveryPower_LinkageDurableBeforeEffect(t *testing.T) {
	g := &guard{boot: bootID, windows: []netguard.Status{pendingStatus()}}
	plan := planFor(t, g)
	s := store(t)
	name, err := LinkageName(powerID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(powerID))
	if name != hex.EncodeToString(sum[:])+".json" {
		t.Fatalf("the linkage is named %q, want the SHA-256 of the power operation id", name)
	}

	var seen Linkage
	ran := 0
	err = admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, func(context.Context) error {
		ran++
		info, err := os.Lstat(filepath.Join(s.Dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > MaxLinkageBytes {
			t.Fatalf("at the effect the linkage is %v (%v), want a complete regular file mode 0600", info, err)
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if seen, err = DecodeLinkage(data); err != nil {
			t.Fatalf("at the effect the linkage does not decode: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		keys := func(m map[string]any) []string {
			var out []string
			for k := range m {
				out = append(out, k)
			}
			slices.Sort(out)
			return out
		}
		if got := keys(raw); !slices.Equal(got, []string{"action", "confirmed_plan_digest", "created_at_utc", "pending_network",
			"power_operation_id", "schema", "sign_in_session_ref"}) {
			t.Fatalf("the linkage has the fields %v", got)
		}
		nested, _ := raw["pending_network"].(map[string]any)
		if got := keys(nested); !slices.Equal(got, []string{"boot_id", "operation_id", "status_digest", "window_generation"}) {
			t.Fatalf("pending_network has the fields %v", got)
		}
		return nil
	})
	if err != nil || ran != 1 {
		t.Fatalf("the recovery reboot was refused (%v) or ran %d times", err, ran)
	}
	want := Linkage{Schema: LinkageSchema, PowerOperationID: powerID, Action: ActionReboot, ConfirmedPlanDigest: plan.Digest(),
		SignInSessionRef: sessionRef, CreatedAtUTC: "2026-09-27T20:00:00Z", PendingNetwork: plan.Pending}
	if seen != want {
		t.Fatalf("the linkage is %+v, want %+v", seen, want)
	}
	if read, err := s.Read(powerID); err != nil || read != want {
		t.Fatalf("the linkage reads back as %+v (%v)", read, err)
	}

	t.Run("a linkage that cannot be committed leaves zero effect", func(t *testing.T) {
		for name, linker := range map[string]Linker{
			"the store fails":            failingLinker{},
			"an unprotected directory":   LinkageStore{Dir: dirWithMode(t, 0o755), Owner: uint32(os.Getuid())},
			"an absent directory":        LinkageStore{Dir: filepath.Join(t.TempDir(), "absent"), Owner: uint32(os.Getuid())},
			"a directory of another uid": LinkageStore{Dir: linkageDir(t), Owner: uint32(os.Getuid()) + 1},
			"no store at all":            nil,
		} {
			e := &effect{}
			err := admission(g, linker).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Gate != "linkage" || e.runs != 0 {
				t.Errorf("%s: %v, ran %d times, want a linkage refusal and no effect", name, err, e.runs)
			}
		}
	})

	t.Run("the linkage holds no credential and nothing the caller named as a path", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(s.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, never := range []string{"password", "token", "secret", "/"} {
			if strings.Contains(strings.ToLower(strings.ReplaceAll(string(data), LinkageSchema, "")), never) {
				t.Errorf("the linkage carries %q: %s", never, data)
			}
		}
	})
}

// failingLinker is a store whose commit fails.
type failingLinker struct{}

func (failingLinker) Commit(Linkage) error { return errors.New("fsync failed") }

func TestRecoveryPower_UnreadableOrReplayedLinkageRefuses(t *testing.T) {
	g := &guard{boot: bootID, windows: []netguard.Status{pendingStatus()}}
	plan := planFor(t, g)
	name, err := LinkageName(powerID)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the same power operation id is never a second reboot", func(t *testing.T) {
		s, e := store(t), &effect{}
		if err := admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run); err != nil || e.runs != 1 {
			t.Fatalf("control: %v, %d", err, e.runs)
		}
		err := admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Gate != "linkage" || e.runs != 1 {
			t.Fatalf("a replayed power operation id: %v, ran %d times", err, e.runs)
		}
		if !errors.Is(s.Commit(Linkage{}), ErrLinkageInvalid) {
			t.Fatal("an invalid linkage was not refused before the store")
		}
	})

	for kind, plant := range map[string]func(t *testing.T, dir string){
		"a partial record": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"schema": "`+LinkageSchema+`", "power_operation_id"`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a link at the record's name": func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.json"), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		},
		"a directory at the record's name": func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run("refused: "+kind, func(t *testing.T) {
			s, e := store(t), &effect{}
			plant(t, s.Dir)
			err := admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Gate != "linkage" || e.runs != 0 {
				t.Fatalf("%v, ran %d times", err, e.runs)
			}
			if _, err := s.Read(powerID); err == nil {
				t.Fatal("an unreadable record was read as a linkage")
			}
		})
	}

	t.Run("refused: a linkage directory that is a link", func(t *testing.T) {
		target := linkageDir(t)
		link := filepath.Join(t.TempDir(), "recovery")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		s, e := LinkageStore{Dir: link, Owner: uint32(os.Getuid())}, &effect{}
		err := admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run)
		if err == nil || e.runs != 0 {
			t.Fatalf("a linked directory: %v, ran %d times", err, e.runs)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Fatalf("a record was written through the link: %v", entries)
		}
	})

	t.Run("the schema is closed", func(t *testing.T) {
		valid := Linkage{Schema: LinkageSchema, PowerOperationID: powerID, Action: ActionReboot, ConfirmedPlanDigest: plan.Digest(),
			SignInSessionRef: sessionRef, CreatedAtUTC: "2026-09-27T20:00:00Z", PendingNetwork: plan.Pending}
		data, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeLinkage(data); err != nil {
			t.Fatalf("control: a valid linkage was refused: %v", err)
		}
		text := string(data)
		for name, document := range map[string]string{
			"an unknown field":              strings.Replace(text, `{"schema"`, `{"extra": 1, "schema"`, 1),
			"an unknown nested field":       strings.Replace(text, `"operation_id":"`+networkID, `"extra": 1, "operation_id":"`+networkID, 1),
			"a missing field":               strings.Replace(text, `"action":"reboot",`, ``, 1),
			"a null":                        strings.Replace(text, `"action":"reboot"`, `"action":null`, 1),
			"a repeated field":              strings.Replace(text, `"action":"reboot"`, `"action":"reboot","action":"reboot"`, 1),
			"another schema":                strings.Replace(text, LinkageSchema, "olivares.ai/network-recovery-power-link/v2", 1),
			"another action":                strings.Replace(text, `"action":"reboot"`, `"action":"shutdown"`, 1),
			"a time that is not UTC":        strings.Replace(text, "2026-09-27T20:00:00Z", "2026-09-27T22:00:00+02:00", 1),
			"an uppercase digest":           strings.Replace(text, plan.Digest(), strings.ToUpper(plan.Digest()), 1),
			"a second document":             text + text,
			"more than 4096 bytes":          text + strings.Repeat(" ", MaxLinkageBytes),
			"not an object":                 "[" + text + "]",
			"a pending network that is not": strings.Replace(text, `"pending_network":{`, `"pending_network":{"x":{},`, 1),
		} {
			if _, err := DecodeLinkage([]byte(document)); err == nil {
				t.Errorf("%s was decoded", name)
			}
		}
	})
}

func TestRecoveryPower_OnlyTheAdmittedInvokerAndSignIn(t *testing.T) {
	// The console-level case is TestRecoveryPower_OnlySignedInRepairConsole; this one measures the
	// admission's own refusal of every other invoker and of a sign-in that is no longer qualified.
	g := &guard{boot: bootID, windows: []netguard.Status{pendingStatus()}}
	plan := planFor(t, g)
	for name, peer := range map[string]helperschema.Peer{
		"the Appliance Console":            {UID: 998, Account: "olivares-portal", Unit: helperschema.Portal.Unit, Attested: true},
		"root in a timer's unit":           {UID: 0, Account: "root", Unit: "apt-daily-upgrade.service", Attested: true},
		"root on the second console":       {UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty2", Attested: true},
		"root over SSH":                    {UID: 0, Account: "root", Unit: "sshd.service", TTY: "device 136:0", Attested: true},
		"the console's unit, unattested":   {UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1"},
		"another account in the console's": {UID: 1000, Account: "olivares", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1", Attested: true},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			s, e := store(t), &effect{}
			a := admission(g, s)
			a.Invoker = func() (helperschema.Peer, error) { return peer, nil }
			refusedBy(t, a.Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "invoker", e, s)
			refusedBy(t, a.Ordinary(context.Background(), signedIn, e.run), "invoker", e, s)
		})
	}
	s, e := store(t), &effect{}
	refusedBy(t, admission(g, s).Recover(context.Background(), plan, plan.Digest(), signedOut, e.run), "sign-in", e, s)
	refusedBy(t, admission(g, s).Ordinary(context.Background(), signedOut, e.run), "sign-in", e, s)
	// A sign-in that ends while the gates are read refuses before the linkage.
	calls := 0
	ending := func() bool { calls++; return calls == 1 }
	refusedBy(t, admission(g, s).Recover(context.Background(), plan, plan.Digest(), ending, e.run), "sign-in", e, s)
}

// newRecords opens an HM-04 operation store and an initialized lifecycle lock in fresh directories.
func newRecords(t *testing.T) (*hostops.Engine, string, string) {
	t.Helper()
	root := t.TempDir()
	records, lifecycle := filepath.Join(root, "operations"), filepath.Join(root, "lifecycle")
	engine, err := hostops.Open(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := hostops.InitLifecycle(lifecycle); err != nil {
		t.Fatal(err)
	}
	return engine, records, lifecycle
}

// submit records a running operation on target in the engine, as a module's act would.
func submit(t *testing.T, engine *hostops.Engine, id, target string) {
	t.Helper()
	cmd := hostops.Command{OperationID: id, TaskID: "host.status", Module: "host", Verb: "status", Target: target,
		PlanDigest: strings.Repeat("ab", 32), Mode: "tty1", Surface: "tty1", Actor: "root"}
	if _, _, err := engine.Submit(cmd, nil); err != nil {
		t.Fatalf("control: the engine refused %s: %v", target, err)
	}
}

func TestRecoveryPower_ConflictingOperationCannotRaceAdmission(t *testing.T) {
	g := &guard{boot: bootID, windows: []netguard.Status{pendingStatus()}}
	plan := planFor(t, g)
	gated := func(records, lifecycle string, s LinkageStore) Admission {
		a := admission(g, s, RecordsGate{Dir: records})
		a.Serializer = HostHold{LifecycleDir: lifecycle, RecordsDir: records}
		return a
	}

	t.Run("control: only the pending network operation is open, and it alone is exempt", func(t *testing.T) {
		engine, records, lifecycle := newRecords(t)
		submit(t, engine, networkID, "network")
		s, e := store(t), &effect{}
		if err := gated(records, lifecycle, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run); err != nil || e.runs != 1 {
			t.Fatalf("the pending network operation alone was refused (%v)", err)
		}
		// Ordinary power has no exemption: the same open network operation refuses it.
		ordinary, e := store(t), &effect{}
		refusedBy(t, gated(records, lifecycle, ordinary).Ordinary(context.Background(), signedIn, e.run), "", e, ordinary)
	})

	for name, target := range map[string]string{
		"a package operation":            "packages",
		"a storage operation":            "drive:" + strings.Repeat("cd", 32),
		"a system operation":             "system",
		"a network operation of another": "network",
	} {
		t.Run("refused: "+name+" already admitted", func(t *testing.T) {
			engine, records, lifecycle := newRecords(t)
			submit(t, engine, otherID, target)
			s, e := store(t), &effect{}
			refusedBy(t, gated(records, lifecycle, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "host operations", e, s)
		})
	}

	t.Run("an operation admitted during the handoff waits for it and runs no effect before it", func(t *testing.T) {
		engine, records, lifecycle := newRecords(t)
		s := store(t)
		var admitted atomic.Bool
		var submitted error
		finished := make(chan struct{})
		racing := hostops.Command{OperationID: otherID, TaskID: "host.status", Module: "host", Verb: "status", Target: "packages",
			PlanDigest: strings.Repeat("ab", 32), Mode: "tty1", Surface: "tty1", Actor: "root"}
		err := gated(records, lifecycle, s).Recover(context.Background(), plan, plan.Digest(), signedIn, func(context.Context) error {
			go func() {
				defer close(finished)
				_, _, submitted = engine.Submit(racing, nil)
				admitted.Store(true)
			}()
			time.Sleep(300 * time.Millisecond)
			if admitted.Load() {
				t.Error("a package operation was admitted while the recovery reboot held the admission")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("control: %v", err)
		}
		<-finished
		if !admitted.Load() || submitted != nil {
			t.Fatalf("the racing operation was not admitted after the hold was released: %v", submitted)
		}
	})

	t.Run("refused: a lifecycle transition holds the lifecycle lock", func(t *testing.T) {
		_, records, lifecycle := newRecords(t)
		transition, err := hostops.OpenLifecycle(lifecycle, hostops.RoleInitializer)
		if err != nil {
			t.Fatal(err)
		}
		defer transition.Close()
		if err := transition.Exclusive(); err != nil {
			t.Fatal(err)
		}
		s, e := store(t), &effect{}
		refusedBy(t, gated(records, lifecycle, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "serialization", e, s)
	})

	t.Run("refused: an unreadable operation store", func(t *testing.T) {
		_, records, lifecycle := newRecords(t)
		if err := os.WriteFile(filepath.Join(records, otherID+".json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, e := store(t), &effect{}
		refusedBy(t, gated(records, lifecycle, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "host operations", e, s)
		missing := filepath.Join(t.TempDir(), "absent")
		s, e = store(t), &effect{}
		refusedBy(t, gated(missing, lifecycle, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "", e, s)
	})
}
