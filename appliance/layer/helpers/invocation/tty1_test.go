// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package invocation

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestHelperAdmission_Tty1InvokerNeedsItsUnitAndDevTty1(t *testing.T) {
	performed := map[string]int{}
	power := helpers(performed)[0]
	reboot := documents[helperschema.PowerReboot]

	peer, err := Attest(console(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !peer.Attested || peer.Account != "root" || peer.Unit != "olivares-repair-console.service" || peer.TTY != "/dev/tty1" {
		t.Fatalf("the repair console's process on tty1 was attested as %+v", peer)
	}
	if code, response := serveOne(t, power, console(), reboot); code != ExitDone || response.Result != helperschema.ResultPerformed {
		t.Fatalf("control: the repair console on /dev/tty1: exit %d, %+v", code, response)
	}

	for name, stat := range map[string]string{
		"no controlling terminal":            statOnTerminal("0"),
		"the second virtual console":         statOnTerminal("1026"),
		"a pseudo-terminal, as over SSH":     statOnTerminal("34816"),
		"a serial line":                      statOnTerminal("1088"),
		"a command name that forges a field": "4242 (x) S 1 4242 4242 1025) S 1 4242 4242 0 4242 0 0\n",
		"a status line without its fields":   "4242 (olivares-repair) S 1\n",
	} {
		t.Run("refused: the console's unit with "+name, func(t *testing.T) {
			k := console()
			k.stat = stat
			peer, err := Attest(k, 3)
			if err != nil {
				t.Fatal(err)
			}
			if peer.TTY == "/dev/tty1" {
				t.Fatalf("%s was attested as /dev/tty1: %+v", name, peer)
			}
			// An unreadable status line leaves the process unidentified (no_connection_identity);
			// every other terminal is attested and not admitted (not_admitted).
			code, response := serveOne(t, power, k, reboot)
			if code != ExitRefused || response.Result != helperschema.ResultRefused ||
				(response.Code != helperschema.CodeNotAdmitted && response.Code != helperschema.CodeNoConnectionIdentity) {
				t.Fatalf("exit %d, %+v, want a refusal", code, response)
			}
		})
	}

	t.Run("refused: /dev/tty1 in another unit", func(t *testing.T) {
		k := console()
		k.cgroup = "0::/system.slice/getty@tty1.service\n"
		if code, response := serveOne(t, power, k, reboot); code != ExitRefused || response.Code != helperschema.CodeNotAdmitted {
			t.Fatalf("a getty on tty1: exit %d, %+v", code, response)
		}
	})

	if performed["power reboot"] != 1 || len(performed) != 1 {
		t.Fatalf("performed %v, want power reboot exactly once: the control", performed)
	}
}

func TestHelperAdmission_MutationsWaitForTheGuestPidfdProof(t *testing.T) {
	if guestProven {
		t.Fatal("this build claims the guest proof of SO_PEERPIDFD for per-connection instances; it has not been accepted")
	}
	performed := map[string]int{}
	all := helpers(performed)
	power := all[0]

	for _, verb := range []string{helperschema.PowerReboot, helperschema.PowerShutdown} {
		t.Run("the repair console on tty1 asks for "+verb, func(t *testing.T) {
			var out, log bytes.Buffer
			code := Serve(context.Background(), power, console(), nil, 3, strings.NewReader(documents[verb]), &out, &log)
			if code != ExitRefused || !strings.Contains(out.String(), `"code":"`+helperschema.CodePidfdUnproven+`"`) {
				t.Fatalf("an admitted mutation before the guest proof: exit %d, %q", code, out.String())
			}
			if !strings.Contains(log.String(), "operation_id "+operationID) {
				t.Fatalf("the refusal's log line does not name the operation:\n%s", log.String())
			}
		})
	}
	if len(performed) != 0 {
		t.Fatalf("performed %v before the guest proof", performed)
	}

	t.Run("the refusal comes after admission: another unit is still not admitted", func(t *testing.T) {
		k := console()
		k.cgroup = "0::/system.slice/olivares-other.service\n"
		var out, log bytes.Buffer
		if code := Serve(context.Background(), power, k, nil, 3, strings.NewReader(documents[helperschema.PowerReboot]), &out, &log); code != ExitRefused ||
			!strings.Contains(out.String(), helperschema.CodeNotAdmitted) {
			t.Fatalf("exit %d, %q", code, out.String())
		}
	})

	t.Run("control: the same request once the proof is accepted", func(t *testing.T) {
		if code, response := serveProven(t, power, console(), true, documents[helperschema.PowerReboot]); code != ExitDone ||
			response.Result != helperschema.ResultPerformed || performed["power reboot"] != 1 {
			t.Fatalf("exit %d, %+v, performed %v", code, response, performed)
		}
	})
}

func TestHelperAdmission_Tty1InvokerIsUIDZeroAsTheKernelReportsIt(t *testing.T) {
	performed := map[string]int{}
	power := helpers(performed)[0]
	reboot := documents[helperschema.PowerReboot]

	t.Run("a user database that names another uid root does not make its process root", func(t *testing.T) {
		k := console()
		k.uid = 1001
		k.names = map[uint32]string{1001: "root"}
		code, response := serveOne(t, power, k, reboot)
		if code != ExitRefused || response.Code != helperschema.CodeNotAdmitted {
			t.Fatalf("uid 1001 named root in the console's unit on /dev/tty1: exit %d, %+v, want %s", code, response, helperschema.CodeNotAdmitted)
		}
	})

	t.Run("uid 0 under another account's name is not that account", func(t *testing.T) {
		portalOnly := []helperschema.Rule{{Subcommand: helperschema.PowerReboot, Mutating: true, Invokers: []helperschema.Invoker{helperschema.Portal}}}
		peer := helperschema.Peer{UID: 0, Account: helperschema.Portal.Account, Unit: helperschema.Portal.Unit, Attested: true}
		if err := helperschema.Admit(peer, portalOnly, helperschema.PowerReboot); err == nil {
			t.Fatal("uid 0 named olivares-portal was admitted as the Appliance Console")
		}
		for name, p := range map[string]helperschema.Peer{
			"root by name only": {UID: 1001, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1", Attested: true},
		} {
			if err := helperschema.Admit(p, helperschema.PowerRules(), helperschema.PowerReboot); err == nil {
				t.Errorf("%s was admitted as the repair console", name)
			}
		}
	})

	t.Run("control: root as the kernel reports it, in the console's unit on /dev/tty1", func(t *testing.T) {
		if code, response := serveOne(t, power, console(), reboot); code != ExitDone || response.Result != helperschema.ResultPerformed {
			t.Fatalf("exit %d, %+v", code, response)
		}
	})

	if performed["power reboot"] != 1 || len(performed) != 1 {
		t.Fatalf("performed %v, want power reboot exactly once: the control", performed)
	}
}
