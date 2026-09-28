// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

// peer is one connection's peer as the kernel reports it: its uid, the cgroup of the pidfd's
// process and its controlling terminal's tty_nr, with or without a pidfd.
type peer struct {
	uid     uint32
	cgroup  string
	ttyNr   string
	noPidfd bool
}

func (p peer) PeerCred(int) (int, uint32, error) { return 4242, p.uid, nil }

func (p peer) PeerPidfd(int) (int, error) {
	if p.noPidfd {
		return 0, syscall.ENOPROTOOPT
	}
	return 77, nil
}

func (p peer) PidOf(int) (int, error) { return 4242, nil }

func (p peer) Cgroup(int) (string, error) { return p.cgroup, nil }

func (p peer) Stat(int) (string, error) {
	return "4242 (olivares-repair) S 1 4242 4242 " + p.ttyNr + " 4242 4194560 0 0 0 0 0 0 0 0 20 0 1 0 1 0 0\n", nil
}

func (p peer) AccountOf(uid uint32) (string, error) {
	switch uid {
	case 0:
		return "root", nil
	case 998:
		return "olivares-portal", nil
	case 997:
		return "olivares-net-guard", nil
	}
	return "", errors.New("unknown uid")
}

func (peer) Alive(int) error { return nil }

func (peer) Close(int) {}

// tty1 is the repair console's process: root, in its unit, on /dev/tty1 (major 4, minor 1).
var tty1 = peer{uid: 0, cgroup: "0::/system.slice/olivares-repair-console.service\n", ttyNr: "1025"}

// serve runs one connection through h as the helper ships, and returns its exit code and answer.
func serve(t *testing.T, h invocation.Helper, k invocation.Kernel, args []string, document string) (int, helperschema.Response) {
	t.Helper()
	var out, log bytes.Buffer
	code := invocation.Serve(context.Background(), h, k, args, 3, strings.NewReader(document), &out, &log)
	var response helperschema.Response
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("the answer is not one document: %q", out.String())
		}
	}
	return code, response
}

func TestFirewallLocal_AdmitsOnlyTheRepairConsole(t *testing.T) {
	opened := 0
	open := func() (*firewall.Owner, error) { opened++; return nil, errors.New("no host in this test") }
	h := helper(open, firewall.Answers{})
	if h.Name != helperschema.HelperFirewallLocal || !reflect.DeepEqual(h.Rules, helperschema.FirewallLocalRules()) || h.NewRequest == nil || h.Perform == nil {
		t.Fatalf("the helper is %+v, want firewall-local with its registry rules", h)
	}
	if class, ok := helperschema.ClassOf(h.Name); !ok || class != helperschema.ClassRoot {
		t.Fatalf("the helper serves %q, of class %v (known %v), want a root helper", h.Name, class, ok)
	}
	if _, ok := h.NewRequest().(*helperschema.FirewallLocalRequest); !ok {
		t.Fatal("the helper's document is not the local entry point's")
	}
	id := strings.Repeat("ab", 16)
	documents := map[string]string{
		helperschema.FirewallLocalStatus:  `{"op": "status"}`,
		helperschema.FirewallLocalRevert:  `{"op": "revert", "operation_id": "` + id + `"}`,
		helperschema.FirewallLocalRestore: `{"op": "restore", "operation_id": "` + id + `"}`,
	}

	// The repair console on tty1: its read reaches the owner, which this test does not open; its acts
	// are admitted and then wait for the guest proof of the peer's pidfd, as every per-connection
	// helper's acts do.
	if code, response := serve(t, h, tty1, nil, documents[helperschema.FirewallLocalStatus]); response.Result != helperschema.ResultFailed || opened != 1 || code != invocation.ExitFailure {
		t.Fatalf("control: status from the repair console on tty1: exit %d, %+v, opened %d", code, response, opened)
	}
	for _, op := range []string{helperschema.FirewallLocalRevert, helperschema.FirewallLocalRestore} {
		if code, response := serve(t, h, tty1, nil, documents[op]); code != invocation.ExitRefused || response.Code != helperschema.CodePidfdUnproven {
			t.Errorf("%s from the repair console on tty1: exit %d, %+v, want admitted and refused %s", op, code, response, helperschema.CodePidfdUnproven)
		}
	}

	// Everyone else is refused at admission, before the owner is opened.
	for name, k := range map[string]peer{
		// Negative control: the Appliance Console is never this helper's invoker.
		"the Appliance Console":               {uid: 998, cgroup: "0::/system.slice/olivares-portal.service\n", ttyNr: "0"},
		"root in an SSH session":              {uid: 0, cgroup: "0::/user.slice/user-0.slice/session-4.scope\n", ttyNr: "34817"},
		"root in a getty on tty1":             {uid: 0, cgroup: "0::/system.slice/getty@tty1.service\n", ttyNr: "1025"},
		"the console's unit on tty2":          {uid: 0, cgroup: tty1.cgroup, ttyNr: "1026"},
		"the network guard":                   {uid: 997, cgroup: "0::/system.slice/olivares-net-guard.service\n", ttyNr: "0"},
		"the console on tty1 without a pidfd": {uid: 0, cgroup: tty1.cgroup, ttyNr: "1025", noPidfd: true},
	} {
		for op, document := range documents {
			code, response := serve(t, h, k, nil, document)
			if code != invocation.ExitRefused || response.Result != helperschema.ResultRefused ||
				(response.Code != helperschema.CodeNotAdmitted && response.Code != helperschema.CodeNoConnectionIdentity) {
				t.Errorf("%s asking %s: exit %d, %+v, want refused at admission", name, op, code, response)
			}
		}
	}

	// A caller-supplied policy or nft text is refused before admission, from the repair console too;
	// so is a command line.
	for name, document := range map[string]string{
		"a caller-supplied policy": `{"op": "restore", "operation_id": "` + id + `", "candidate": {"ssh_port": 22}}`,
		"nft text":                 `{"op": "restore", "operation_id": "` + id + `", "nft": "flush ruleset"}`,
		"a disable":                `{"op": "disable", "operation_id": "` + id + `"}`,
	} {
		if code, response := serve(t, h, tty1, nil, document); code != invocation.ExitRefused || response.Code != helperschema.CodeInputRefused {
			t.Errorf("%s: exit %d, %+v, want refused %s", name, code, response, helperschema.CodeInputRefused)
		}
	}
	if code, _ := serve(t, h, tty1, []string{"--restore"}, documents[helperschema.FirewallLocalStatus]); code != invocation.ExitFailure {
		t.Errorf("a command line: exit %d, want %d", code, invocation.ExitFailure)
	}

	// Perform answers only the local entry point's document.
	if response := h.Perform(context.Background(), helperschema.Peer{}, &helperschema.PowerRequest{Verb: "reboot", OperationID: id}); response.Result != helperschema.ResultRefused {
		t.Errorf("another helper's document: %+v", response)
	}
	if opened != 1 {
		t.Fatalf("the owner was opened %d times, want once, for the repair console's status read", opened)
	}
}
