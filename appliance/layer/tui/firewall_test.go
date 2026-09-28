// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

// firewallLocal stands in for olivares-portal-firewall-local on its socket: it keeps each document it
// is sent, answers status with its status and restore with what restore returns for the id.
type firewallLocal struct {
	mu       sync.Mutex
	received []map[string]any
}

// startFirewallLocal listens on dir/firewall-local.sock until the test ends.
func startFirewallLocal(t *testing.T, dir string, status firewall.LocalStatus, restore func(id string) string) *firewallLocal {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, helperschema.HelperFirewallLocal+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	answered, err := json.Marshal(helperschema.Response{Result: helperschema.ResultAnswered, Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	h := &firewallLocal{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var document map[string]any
			data, _ := io.ReadAll(conn)
			_ = json.Unmarshal(data, &document)
			h.mu.Lock()
			h.received = append(h.received, document)
			h.mu.Unlock()
			answer := `{"result": "refused", "code": "input_refused"}` + "\n"
			switch document["op"] {
			case helperschema.FirewallLocalStatus:
				answer = string(answered) + "\n"
			case helperschema.FirewallLocalRestore:
				id, _ := document["operation_id"].(string)
				answer = restore(id)
			}
			_, _ = io.WriteString(conn, answer)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
	})
	return h
}

// ops returns the op of each document received, in order.
func (h *firewallLocal) ops() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ops []string
	for _, document := range h.received {
		op, _ := document["op"].(string)
		ops = append(ops, op)
	}
	return ops
}

// restores returns each restore document received, in order.
func (h *firewallLocal) restores() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var documents []map[string]any
	for _, document := range h.received {
		if document["op"] == helperschema.FirewallLocalRestore {
			documents = append(documents, document)
		}
	}
	return documents
}

var (
	closedDigest = "sha256:" + strings.Repeat("c", 64)
	targetDigest = "sha256:" + strings.Repeat("d", 64)
)

// closedStatus is the local entry point's status on an appliance whose confirmed policy closed the
// management 9443 row: the target derived from its answers admits 9443 on eth0 and eth1 again and
// drops the proxy's row.
func closedStatus() firewall.LocalStatus {
	return firewall.LocalStatus{
		ConfirmedDigest: closedDigest,
		ConfirmedRows:   []policy.MeasuredRow{{Port: "22/tcp", Interfaces: []string{"*"}}, {Port: "9444/tcp", Interfaces: []string{"eth0"}}},
		OpenWindows:     []string{},
		TargetDigest:    targetDigest,
		TargetRows: []policy.MeasuredRow{{Port: "22/tcp", Interfaces: []string{"*"}}, {Port: "546/udp", Interfaces: []string{"eth0", "eth1"}},
			{Port: "9443/tcp", Interfaces: []string{"eth0", "eth1"}}},
		DroppedAppRows: []policy.AppRow{{App: "proxy", Port: "9444/tcp", Interfaces: []string{"eth0"}}},
	}
}

// recorded answers a restore with the owner's record in state, performed only when restored.
func recorded(state, reason string) func(id string) string {
	return func(id string) string {
		record := firewall.Restoration{OperationID: id, BootID: "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e", CreatedAt: "2026-09-28T01:00:00Z",
			TargetDigest: targetDigest, PreviousDigest: closedDigest, State: state, Reason: reason, MeasuredDigest: targetDigest}
		response := helperschema.Response{Result: helperschema.ResultPerformed}
		if state != firewall.RestorationRestored {
			record.MeasuredDigest = closedDigest
			response = helperschema.Response{Result: helperschema.ResultFailed, Code: reason, Detail: "the target did not load"}
		}
		bundle, _ := json.Marshal(record)
		response.Bundle = bundle
		data, _ := json.Marshal(response)
		return string(data) + "\n"
	}
}

// switchable is a sign-in the case qualifies and ends.
type switchable struct{ on bool }

func (s *switchable) Qualified() bool { return s.on }

// minted is a random source from which the console mints the operation ids 11..., 22... and 33....
func minted() io.Reader {
	var raw []byte
	for _, b := range []byte{0x11, 0x22, 0x33} {
		raw = append(raw, bytes.Repeat([]byte{b}, 16)...)
	}
	return bytes.NewReader(raw)
}

func TestConsole_FirewallRestoreNeedsTheQualifiedSignIn(t *testing.T) {
	network := verbPosition(t, auth.VerbNetwork)
	id := strings.Repeat("ab", 16)
	f := newFixture(t, onTty1, e12Record{human: recordedHuman})
	local := startFirewallLocal(t, f.helperDir, closedStatus(), recorded(firewall.RestorationRestored, ""))

	// Physical access alone: the network verb refuses before its question, and firewall-restore typed at
	// the menu selects nothing.
	code, out := session(t, f.console, network, "firewall-restore", id, "q")
	if code != 0 {
		t.Fatalf("the console exited %d:\n%s", code, out)
	}
	if got := local.ops(); len(got) != 0 {
		t.Fatalf("a console without a sign-in asked the firewall's local entry point %q:\n%s", got, out)
	}

	// Neither half asks anything without a qualified sign-in: none, or one that is no longer qualified.
	for name, console := range map[string]Console{"no sign-in": f.console, "an unqualified sign-in": f.console.withSignIn(signInState(false))} {
		if plan, statement := console.PlanFirewallRestore(); plan != nil || !strings.Contains(statement, "needs its qualified sign-in") {
			t.Errorf("%s: the plan is %+v, %q", name, plan, statement)
		}
		if said := console.FirewallRestore(FirewallRestorePlan{OperationID: id, TargetDigest: targetDigest}, id); !strings.Contains(said, "needs its qualified sign-in") {
			t.Errorf("%s: the restore said %q", name, said)
		}
	}
	if got := local.ops(); len(got) != 0 {
		t.Fatalf("an unqualified console asked %q", got)
	}

	// A sign-in that ends between the plan and the typed id asks nothing more.
	ending := &switchable{on: true}
	console := f.console.withSignIn(ending)
	plan, statement := console.PlanFirewallRestore()
	if plan == nil {
		t.Fatalf("control: a qualified sign-in got no plan: %q", statement)
	}
	ending.on = false
	if said := console.FirewallRestore(*plan, plan.OperationID); !strings.Contains(said, "needs its qualified sign-in") {
		t.Errorf("a sign-in that ended before the typed id: %q", said)
	}

	// Fresh invoker evidence: a console whose own process is not the repair console on tty1 asks
	// nothing at the act, whatever its sign-in.
	elsewhere := f.console.WithAuthority(Authority{Terminal: attested{UID: 0, Account: "root", Unit: "getty@tty1.service", TTY: "/dev/tty1", Attested: true}}).
		withSignIn(signInState(true))
	if plan, _ := elsewhere.PlanFirewallRestore(); plan == nil {
		t.Fatal("control: the status read was refused")
	} else if said := elsewhere.FirewallRestore(*plan, plan.OperationID); !strings.Contains(said, "not attested as the repair console on tty1") {
		t.Errorf("a console outside its unit: %q", said)
	}
	if got := local.restores(); len(got) != 0 {
		t.Fatalf("a restore was asked without the qualified sign-in on tty1: %v", got)
	}

	// Control: the qualified sign-in on tty1 asks once.
	signed := f.console.withSignIn(signInState(true))
	plan, _ = signed.PlanFirewallRestore()
	if plan == nil {
		t.Fatal("control: no plan")
	}
	if said := signed.FirewallRestore(*plan, plan.OperationID); !strings.Contains(said, "firewall-restore: restored") {
		t.Errorf("control: the restore said %q", said)
	}
	if got := local.restores(); len(got) != 1 {
		t.Fatalf("the local entry point was asked to restore %d times, want once: the control", len(got))
	}
}

func TestConsole_FirewallRestoreAsksOnlyForTheTypedID(t *testing.T) {
	network := verbPosition(t, auth.VerbNetwork)
	f := newFixture(t, onTty1, e12Record{human: recordedHuman})
	f.console.random = minted()
	local := startFirewallLocal(t, f.helperDir, closedStatus(), recorded(firewall.RestorationRestored, ""))
	first, second, third := strings.Repeat("11", 16), strings.Repeat("22", 16), strings.Repeat("33", 16)

	// Signed in, the operator selects the network verb and types firewall-restore three times: once
	// answering something else, once with the previous plan's id, and once with the id this plan shows.
	code, out := session(t, f.console, "s", "olivares",
		network, "firewall-restore", "yes",
		network, "firewall-restore", first,
		network, "firewall-restore", third,
		"q")
	if code != 0 {
		t.Fatalf("the console exited %d:\n%s", code, out)
	}
	if got := local.ops(); strings.Join(got, " ") != "status status status restore" {
		t.Fatalf("the local entry point was asked %q, want three status reads and one restore:\n%s", got, out)
	}
	restores := local.restores()
	if len(restores) != 1 || len(restores[0]) != 2 || restores[0]["operation_id"] != third {
		t.Fatalf("the restore document is %v, want the op and the third plan's id alone", restores)
	}
	// Each plan shows the status, the derived target and its own minted id, and asks for that id.
	for _, want := range []string{first, second, third, closedDigest, targetDigest, "9443/tcp on eth0,eth1", "546/udp on eth0,eth1",
		"proxy 9444/tcp on eth0", firewallRestoreConfirm} {
		if !strings.Contains(out, want) {
			t.Errorf("the console did not state %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "Nothing was asked of the firewall's local entry point.") != 2 {
		t.Errorf("an answer that is not the plan's id did not ask nothing:\n%s", out)
	}
	if !strings.Contains(out, "firewall-restore: restored") || !strings.Contains(out, third) {
		t.Errorf("the recorded result is not stated:\n%s", out)
	}

	t.Run("a status that allows no restoration asks for no id", func(t *testing.T) {
		open := closedStatus()
		open.OpenWindows = []string{strings.Repeat("ef", 16)}
		refused := closedStatus()
		refused.TargetDigest, refused.TargetRows, refused.DroppedAppRows, refused.TargetRefusal = "", []policy.MeasuredRow{}, []policy.AppRow{}, firewall.CodeAnswersInvalid
		inForce := closedStatus()
		inForce.ConfirmedDigest = targetDigest
		for name, c := range map[string]struct {
			status firewall.LocalStatus
			says   string
		}{
			"an open window":           {open, strings.Repeat("ef", 16)},
			"answers that are invalid": {refused, firewall.CodeAnswersInvalid},
			"the target in force":      {inForce, "already"},
		} {
			dir := socketDir(t)
			local := startFirewallLocal(t, dir, c.status, recorded(firewall.RestorationRestored, ""))
			console := NewConsole(auth.State{Mode: auth.Unavailable}).WithHelper(helperclient.Client{Dir: dir, Owner: uint32(os.Getuid())}).
				WithAuthority(Authority{Terminal: onTty1}).withSignIn(signInState(true))
			plan, statement := console.PlanFirewallRestore()
			if plan != nil || !strings.Contains(statement, c.says) || !strings.Contains(statement, "nothing was asked") {
				t.Errorf("%s: the plan is %+v, %q", name, plan, statement)
			}
			if got := local.ops(); strings.Join(got, " ") != "status" {
				t.Errorf("%s: the local entry point was asked %q, want the status read alone", name, got)
			}
		}
	})

	t.Run("the recorded result is stated as it came, and a failure is never restored", func(t *testing.T) {
		dir := socketDir(t)
		local := startFirewallLocal(t, dir, closedStatus(), recorded(firewall.RestorationFailed, firewall.CodeLoadFailed))
		console := NewConsole(auth.State{Mode: auth.Unavailable}).WithHelper(helperclient.Client{Dir: dir, Owner: uint32(os.Getuid())}).
			WithAuthority(Authority{Terminal: onTty1}).withSignIn(signInState(true))
		plan, statement := console.PlanFirewallRestore()
		if plan == nil {
			t.Fatalf("control: no plan: %q", statement)
		}
		said := console.FirewallRestore(*plan, plan.OperationID)
		if strings.Contains(said, "firewall-restore: restored") || !strings.Contains(said, firewall.CodeLoadFailed) || !strings.Contains(said, "recorded failed") {
			t.Errorf("a failed restoration was stated %q", said)
		}
		if len(local.restores()) != 1 {
			t.Errorf("the restore was asked %d times", len(local.restores()))
		}
		// An absent helper asks nothing and claims nothing.
		absent := NewConsole(auth.State{Mode: auth.Unavailable}).WithHelper(helperclient.Client{Dir: socketDir(t), Owner: uint32(os.Getuid())}).
			WithAuthority(Authority{Terminal: onTty1}).withSignIn(signInState(true))
		if said := absent.FirewallRestore(*plan, plan.OperationID); !strings.Contains(said, "unavailable") || strings.Contains(said, "firewall-restore: restored") {
			t.Errorf("an absent helper: %q", said)
		}
	})
}
