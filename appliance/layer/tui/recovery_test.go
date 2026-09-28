// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"github.com/olivaresai/olivares/appliance/layer/repair"
)

const (
	pendingBoot       = "3f1c2a8e-5b7d-4e21-9a0c-6d2f8b4e1a73"
	pendingOperation  = "fedcba9876543210fedcba9876543210"
	pendingGeneration = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

// pendingGuard is the network guard with one window whose mutating reply was lost, in this boot.
type pendingGuard struct{ requests []netguard.EdgeRequest }

func (g *pendingGuard) Call(_ context.Context, r netguard.EdgeRequest) (netguard.EdgeResponse, error) {
	g.requests = append(g.requests, r)
	return netguard.EdgeResponse{Code: "ok", BootID: pendingBoot, OpenWindows: []netguard.Status{{ObservedBootID: pendingBoot,
		OperationID: pendingOperation, State: netguard.StateAwaitingConfirmation, BootID: pendingBoot,
		WindowGeneration: pendingGeneration, PendingCalls: 1}}}, nil
}

// holdNothing is a serialization point that holds nothing: the cases that measure the console.
type holdNothing struct{}

func (holdNothing) Hold(context.Context) (func(), error) { return func() {}, nil }

// openAdmission admits every power act of the repair console's own process on tty1: no gate refuses.
func openAdmission() repair.Admission {
	return repair.Admission{Invoker: func() (helperschema.Peer, error) { return helperschema.Peer(onTty1), nil },
		Serializer: holdNothing{}, Now: time.Now}
}

// recoveryConsole is a fixture's console with the pending guard and an admission whose linkage store
// is dir, with the window gate over the same guard.
func recoveryConsole(t *testing.T, f *fixture, g *pendingGuard, dir string) Console {
	t.Helper()
	status := func(ctx context.Context) (netguard.EdgeResponse, error) {
		return g.Call(ctx, netguard.EdgeRequest{Action: "status"})
	}
	boot := func() (string, error) { return pendingBoot, nil }
	admission := repair.Admission{
		Invoker:    func() (helperschema.Peer, error) { return helperschema.Peer(onTty1), nil },
		Serializer: holdNothing{},
		Gates:      []repair.Gate{repair.WindowGate{Status: status, Boot: boot}},
		Store:      repair.LinkageStore{Dir: dir, Owner: uint32(os.Getuid())},
		Now:        time.Now,
	}
	return f.console.WithNetwork(g).WithPowerAdmission(admission).WithBootID(boot)
}

// protectedDir is a fresh directory mode 0700 of this process's own.
func protectedDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "recovery")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRecoveryPower_OnlySignedInRepairConsole(t *testing.T) {
	power := powerPosition(t)

	t.Run("the signed-in repair console confirms the exact window, links it and asks the power helper once", func(t *testing.T) {
		f, g, dir := newFixture(t, onTty1, e12Record{human: recordedHuman}), &pendingGuard{}, protectedDir(t)
		code, out := session(t, recoveryConsole(t, f, g, dir), "s", "olivares", power, "recovery-reboot", pendingOperation, "q")
		if code != 0 {
			t.Fatalf("the console exited %d:\n%s", code, out)
		}
		for _, want := range []string{pendingOperation, pendingBoot, pendingGeneration, "firewall", "package", "exposure"} {
			if !strings.Contains(out, want) {
				t.Errorf("the confirmation does not show %q:\n%s", want, out)
			}
		}
		documents := f.power.documents()
		if len(documents) != 1 || documents[0]["verb"] != helperschema.PowerReboot {
			t.Fatalf("the power helper received %v, want one reboot", documents)
		}
		powerOperation, _ := documents[0]["operation_id"].(string)
		if !strings.Contains(out, powerOperation) {
			t.Fatalf("the power operation asked for, %s, is not the one the console displayed:\n%s", powerOperation, out)
		}
		linkage, err := (repair.LinkageStore{Dir: dir, Owner: uint32(os.Getuid())}).Read(powerOperation)
		if err != nil {
			t.Fatalf("no linkage for the power operation the helper was asked for: %v", err)
		}
		if linkage.PendingNetwork.OperationID != pendingOperation || linkage.PendingNetwork.BootID != pendingBoot ||
			linkage.PendingNetwork.WindowGeneration != pendingGeneration || linkage.Action != repair.ActionReboot {
			t.Fatalf("the linkage names %+v", linkage)
		}
	})

	for name, lines := range map[string][]string{
		"no sign-in":                    {power, "recovery-reboot", pendingOperation},
		"a confirmation naming nothing": {"s", "olivares", power, "recovery-reboot", "yes"},
		"a confirmation of another id":  {"s", "olivares", power, "recovery-reboot", strings.Repeat("0", 32)},
		"leaving at the prompt":         {"s", "olivares", power, "recovery-reboot"},
		"an ordinary reboot":            {"s", "olivares", power, "reboot"},
		"an ordinary shutdown":          {"s", "olivares", power, "shutdown"},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			f, g, dir := newFixture(t, onTty1, e12Record{human: recordedHuman}), &pendingGuard{}, protectedDir(t)
			_, out := session(t, recoveryConsole(t, f, g, dir), append(lines, "q")...)
			if got := f.power.verbs(); len(got) != 0 {
				t.Fatalf("the power helper was asked %q:\n%s", got, out)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("a linkage was written: %v\n%s", entries, out)
			}
		})
	}

	t.Run("refused: a sign-in that ends between the display and the confirmation", func(t *testing.T) {
		f, g, dir := newFixture(t, onTty1, e12Record{human: recordedHuman}), &pendingGuard{}, protectedDir(t)
		start := f.clock.now
		input := &stepped{lines: []string{"s", "olivares", power, "recovery-reboot", pendingOperation, "q"},
			before: map[int]func(){4: func() { f.clock.now = start.Add(IdleLimit + time.Minute) }}}
		var out, diagnostics strings.Builder
		recoveryConsole(t, f, g, dir).Run(input, &out, &diagnostics)
		if got := f.power.verbs(); len(got) != 0 || !strings.Contains(out.String(), "needs its qualified sign-in") {
			t.Fatalf("an idle sign-in confirmed a recovery reboot, helper asked %q:\n%s", got, out.String())
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("a linkage was written: %v", entries)
		}
	})

	t.Run("an ordinary reboot while the window is pending names the recovery route instead", func(t *testing.T) {
		f, g, dir := newFixture(t, onTty1, e12Record{human: recordedHuman}), &pendingGuard{}, protectedDir(t)
		_, out := session(t, recoveryConsole(t, f, g, dir), "s", "olivares", power, "reboot", "q")
		if !strings.Contains(out, "recovery-reboot") || !strings.Contains(out, "refused") {
			t.Fatalf("an ordinary reboot with a pending window read as:\n%s", out)
		}
	})
}
