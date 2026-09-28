// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package invocation

import (
	"context"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// portalUID is the Appliance Console's static account in these descriptions.
const portalUID = 998

// portalService is the Appliance Console's own process: its account in its unit, with no
// controlling terminal, with a pidfd.
func portalService() *fakeKernel {
	return &fakeKernel{pid: 5151, pidfdPid: 5151, uid: portalUID, names: map[uint32]string{portalUID: "olivares-portal", 997: "olivares-net-guard"},
		cgroup: "0::/system.slice/olivares-portal.service\n", stat: statOnTerminal("0")}
}

// moduleHelpers are the module helpers with their modules' production rules and documents; each
// Perform counts its calls and performs nothing.
func moduleHelpers(performed map[string]int) []Helper {
	perform := func(name string) func(context.Context, helperschema.Peer, helperschema.Request) helperschema.Response {
		return func(_ context.Context, _ helperschema.Peer, r helperschema.Request) helperschema.Response {
			performed[name+" "+r.Subcommand()]++
			return helperschema.Response{Result: helperschema.ResultAnswered}
		}
	}
	return []Helper{
		{Name: helperschema.HelperUnits, Rules: services.Rules(),
			NewRequest: func() helperschema.Request { return &services.Request{} },
			Perform:    perform(helperschema.HelperUnits)},
		{Name: helperschema.HelperStorage, Rules: storage.HelperRules(),
			NewRequest: func() helperschema.Request { return &storage.InventoryRequest{} },
			Perform:    perform(helperschema.HelperStorage)},
		{Name: helperschema.HelperFirewall, Rules: firewall.Rules(),
			NewRequest: func() helperschema.Request { return &firewall.Request{} },
			Perform:    perform(helperschema.HelperFirewall)},
	}
}

// moduleReads are one read document per module helper, and the subcommand it reads.
var moduleReads = map[string]string{
	helperschema.HelperUnits:    `{"op": "list"}`,
	helperschema.HelperStorage:  `{"op": "inventory"}`,
	helperschema.HelperFirewall: `{"op": "status"}`,
}

// moduleReadSubcommands are the subcommands of moduleReads.
var moduleReadSubcommands = map[string]string{
	helperschema.HelperUnits:    "list",
	helperschema.HelperStorage:  "inventory",
	helperschema.HelperFirewall: "status",
}

func TestHelperAdmission_AModuleHelperAdmitsTheApplianceConsoleServiceAlone(t *testing.T) {
	if len(moduleHelpers(map[string]int{})) != len(helperschema.ModuleHelpers()) {
		t.Fatalf("the census serves %d module helpers, the seam has %d", len(moduleHelpers(map[string]int{})), len(helperschema.ModuleHelpers()))
	}
	for _, h := range moduleHelpers(map[string]int{}) {
		t.Run(h.Name, func(t *testing.T) {
			performed := map[string]int{}
			var helper Helper
			for _, candidate := range moduleHelpers(performed) {
				if candidate.Name == h.Name {
					helper = candidate
				}
			}
			read := moduleReads[h.Name]
			if code, response := serveOne(t, helper, portalService(), read); code != ExitDone || response.Result != helperschema.ResultAnswered {
				t.Fatalf("control: the Appliance Console's process was refused: exit %d, %+v", code, response)
			}
			for name, shape := range map[string]func() *fakeKernel{
				// The module's own rules name the repair console; the class does not admit it.
				"the repair console on tty1": console,
				"an SSH session of an operator": func() *fakeKernel {
					return &fakeKernel{pid: 6161, pidfdPid: 6161, uid: 1000,
						cgroup: "0::/user.slice/user-1000.slice/session-3.scope\n", stat: statOnTerminal("34816")}
				},
				"root in an SSH session": func() *fakeKernel {
					return &fakeKernel{pid: 6162, pidfdPid: 6162, uid: 0,
						cgroup: "0::/user.slice/user-0.slice/session-4.scope\n", stat: statOnTerminal("34817")}
				},
				"root in another system unit": func() *fakeKernel {
					k := console()
					k.cgroup, k.stat = "0::/system.slice/olivares-other.service\n", statOnTerminal("0")
					return k
				},
				"the console's account in another unit": func() *fakeKernel {
					k := portalService()
					k.cgroup = "0::/system.slice/olivares-net-guard.service\n"
					return k
				},
				"the network guard in its own unit": func() *fakeKernel {
					k := portalService()
					k.uid, k.cgroup = 997, "0::/system.slice/olivares-net-guard.service\n"
					return k
				},
				"the console's account and unit on a terminal": func() *fakeKernel {
					k := portalService()
					k.stat = statOnTerminal("1025")
					return k
				},
				"the console's account and unit without a pidfd": func() *fakeKernel {
					k := portalService()
					k.noPidfd = true
					return k
				},
			} {
				code, response := serveOne(t, helper, shape(), read)
				if code != ExitRefused || response.Result != helperschema.ResultRefused || response.Code != helperschema.CodeNotAdmitted {
					t.Errorf("%s: exit %d, %+v, want refused %s", name, code, response, helperschema.CodeNotAdmitted)
				}
			}
			if len(performed) != 1 || performed[h.Name+" "+moduleReadSubcommands[h.Name]] != 1 {
				t.Fatalf("performed %v, want the control's read exactly once", performed)
			}
		})
	}

	t.Run("control: a root helper keeps its own invokers", func(t *testing.T) {
		counted := map[string]int{}
		power := helpers(counted)[0]
		if code, response := serveOne(t, power, console(), documents[helperschema.PowerReboot]); code != ExitDone || response.Result != helperschema.ResultPerformed {
			t.Fatalf("the repair console on tty1 asking power: exit %d, %+v", code, response)
		}
		if code, response := serveOne(t, power, portalService(), documents[helperschema.PowerReboot]); code != ExitRefused || response.Code != helperschema.CodeNotAdmitted {
			t.Fatalf("the Appliance Console asking power: exit %d, %+v", code, response)
		}
		if counted["power reboot"] != 1 || len(counted) != 1 {
			t.Fatalf("performed %v, want power reboot once", counted)
		}
	})
}

// The firewall module's rules name the repair console on tty1 for status and revert. The module
// class admits the Appliance Console alone, so the seam refuses the repair console for both, on
// either side of the guest proof, and serves the same documents to the console's process. The tty1
// console reaches the firewall through no helper route until one is designed for it.
func TestHelperAdmission_TheFirewallHelperRefusesTheRepairConsoleItsRulesName(t *testing.T) {
	if firewall.HelperName != helperschema.HelperFirewall {
		t.Fatalf("the firewall program serves %q, the seam names %q", firewall.HelperName, helperschema.HelperFirewall)
	}
	var named []string
	for _, rule := range firewall.Rules() {
		if slices.Contains(rule.Invokers, helperschema.RepairConsole) {
			named = append(named, rule.Subcommand)
		}
	}
	if !slices.Equal(named, []string{firewall.OpStatus, firewall.OpRevert}) {
		t.Fatalf("the module's rules name the repair console for %v; this test reads status and revert", named)
	}
	performed := map[string]int{}
	var helper Helper
	for _, candidate := range moduleHelpers(performed) {
		if candidate.Name == helperschema.HelperFirewall {
			helper = candidate
		}
	}
	if helper.Name == "" {
		t.Fatal("the firewall helper is not served as a module helper")
	}
	documents := map[string]string{
		firewall.OpStatus: `{"op": "status"}`,
		firewall.OpRevert: `{"op": "revert", "operation_id": "` + operationID + `"}`,
	}
	for op, document := range documents {
		for _, proven := range []bool{true, false} {
			code, response := serveProven(t, helper, console(), proven, document)
			if code != ExitRefused || response.Result != helperschema.ResultRefused || response.Code != helperschema.CodeNotAdmitted {
				t.Errorf("%s from the repair console on tty1 (proven %v): exit %d, %+v, want refused %s", op, proven, code, response, helperschema.CodeNotAdmitted)
			}
		}
		if code, response := serveOne(t, helper, portalService(), document); code != ExitDone || response.Result != helperschema.ResultAnswered {
			t.Errorf("%s from the Appliance Console's process: exit %d, %+v", op, code, response)
		}
	}
	if len(performed) != 2 || performed["firewall status"] != 1 || performed["firewall revert"] != 1 {
		t.Fatalf("performed %v, want the Appliance Console's status and revert once each", performed)
	}
}
