// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The firewall's route from tty1 is a root helper of its own, firewall-local, whose socket is root's
// alone and whose one invoker is the repair console on tty1. The firewall module's helper keeps its
// class, and the module class keeps the Appliance Console alone: no socket gains a second audience.
func TestHelperClass_FirewallLocalIsARootHelperBesideTheModuleHelper(t *testing.T) {
	if HelperFirewallLocal != "firewall-local" {
		t.Fatalf("the local entry point is named %q; its socket is /run/olivares-helpers/firewall-local.sock", HelperFirewallLocal)
	}
	if class, ok := ClassOf(HelperFirewallLocal); !ok || class != ClassRoot {
		t.Fatalf("firewall-local is of class %v (known %v), want the root class", class, ok)
	}
	if slices.Contains(ModuleHelpers(), HelperFirewallLocal) {
		t.Error("firewall-local is a module helper too")
	}
	if got := Account(HelperFirewallLocal); got != "root" {
		t.Errorf("firewall-local runs as %q, want root, as its template states", got)
	}
	if class, ok := ClassOf(HelperFirewall); !ok || class != ClassModule {
		t.Errorf("the firewall module's helper is of class %v (known %v), want the module class, unchanged", class, ok)
	}
	// Negative control: the module class is not widened to reach the firewall from tty1.
	if got := ModuleInvokers(); !slices.Equal(got, []Invoker{Portal}) {
		t.Fatalf("the module class admits %v, want the Appliance Console alone", got)
	}

	rules := FirewallLocalRules()
	var subcommands []string
	for _, rule := range rules {
		subcommands = append(subcommands, rule.Subcommand)
		if !slices.Equal(rule.Invokers, []Invoker{RepairConsole}) {
			t.Errorf("firewall-local %s admits %v, want the repair console on tty1 alone", rule.Subcommand, rule.Invokers)
		}
		if want := rule.Subcommand != FirewallLocalStatus; rule.Mutating != want {
			t.Errorf("firewall-local %s is mutating %v, want %v", rule.Subcommand, rule.Mutating, want)
		}
	}
	if !slices.Equal(subcommands, []string{FirewallLocalStatus, FirewallLocalRevert, FirewallLocalRestore}) {
		t.Fatalf("firewall-local's closed set is %v, want status, revert and restore", subcommands)
	}

	tty1 := Peer{UID: 0, Account: "root", Unit: RepairConsole.Unit, TTY: RepairConsole.TTY, Attested: true}
	for _, subcommand := range subcommands {
		// A root helper is decided by its own rules: the class does not narrow them.
		if err := AdmitHelper(tty1, HelperFirewallLocal, rules, subcommand); err != nil {
			t.Errorf("the repair console on tty1 asking %s: %v", subcommand, err)
		}
		for name, c := range map[string]struct {
			peer Peer
			code string
		}{
			// Negative control: the Appliance Console is never an invoker of this socket.
			"the Appliance Console":             {Peer{UID: 998, Account: Portal.Account, Unit: Portal.Unit, Attested: true}, CodeNotAdmitted},
			"root in an SSH session":            {Peer{UID: 0, Account: "root", Unit: "session-4.scope", TTY: "device 136:1", Attested: true}, CodeNotAdmitted},
			"a getty on tty1":                   {Peer{UID: 0, Account: "root", Unit: "getty@tty1.service", TTY: "/dev/tty1", Attested: true}, CodeNotAdmitted},
			"the console's unit on tty2":        {Peer{UID: 0, Account: "root", Unit: RepairConsole.Unit, TTY: "/dev/tty2", Attested: true}, CodeNotAdmitted},
			"the network guard":                 {Peer{UID: 997, Account: NetGuard.Account, Unit: NetGuard.Unit, Attested: true}, CodeNotAdmitted},
			"an account named root that is not": {Peer{UID: 1001, Account: "root", Unit: RepairConsole.Unit, TTY: RepairConsole.TTY, Attested: true}, CodeNotAdmitted},
		} {
			var refusal *Refusal
			if err := AdmitHelper(c.peer, HelperFirewallLocal, rules, subcommand); !errors.As(err, &refusal) || refusal.Code != c.code {
				t.Errorf("%s asking %s: %v, want %s", name, subcommand, err, c.code)
			}
		}
	}
	unattested := tty1
	unattested.Attested = false
	for _, subcommand := range []string{FirewallLocalRevert, FirewallLocalRestore} {
		var refusal *Refusal
		if err := AdmitHelper(unattested, HelperFirewallLocal, rules, subcommand); !errors.As(err, &refusal) || refusal.Code != CodeNoConnectionIdentity {
			t.Errorf("%s without a proven connection identity: %v, want %s", subcommand, err, CodeNoConnectionIdentity)
		}
	}
	// The module helper still refuses the repair console, whatever its module's rules name.
	moduleRules := []Rule{{Subcommand: "status", Invokers: []Invoker{Portal, RepairConsole}}}
	if err := AdmitHelper(tty1, HelperFirewall, moduleRules, "status"); err == nil {
		t.Error("the firewall module's helper admitted the repair console on tty1")
	}
}

// restore derives its target on the host; its caller sends an operation id and nothing else. A
// policy, a rule, nft text, a path or any other member is refused, never ignored.
func TestHelperInput_FirewallLocalTakesNoPolicyNoRuleAndNoPath(t *testing.T) {
	for _, document := range []string{
		`{"op": "status"}`,
		`{"op": "revert", "operation_id": "` + testOperationID + `"}`,
		`{"op": "restore", "operation_id": "` + testOperationID + `"}`,
	} {
		request := &FirewallLocalRequest{}
		if err := Decode(strings.NewReader(document), request); err != nil {
			t.Fatalf("control: %s was refused: %v", document, err)
		}
	}
	restore := `{"op": "restore", "operation_id": "` + testOperationID + `"`
	for name, document := range map[string]string{
		"a caller-supplied policy":    restore + `, "candidate": {"schema_version": "olivares-firewall-policy/v1", "ssh_port": 22}}`,
		"a caller-supplied target":    restore + `, "target": "sha256:DO-NOT-PRINT"}`,
		"nft text":                    restore + `, "nft": "flush ruleset DO-NOT-PRINT"}`,
		"a ruleset":                   restore + `, "ruleset": "table inet olivares { DO-NOT-PRINT }"}`,
		"a rule list":                 restore + `, "rules": ["tcp dport 9443 accept"]}`,
		"a path":                      restore + `, "path": "/etc/DO-NOT-PRINT"}`,
		"an invoker":                  restore + `, "invoker": "the repair console on tty1"}`,
		"restore without its id":      `{"op": "restore"}`,
		"revert without its id":       `{"op": "revert"}`,
		"status with an id":           `{"op": "status", "operation_id": "` + testOperationID + `"}`,
		"an uppercase id":             `{"op": "restore", "operation_id": "` + strings.ToUpper(testOperationID) + `"}`,
		"the module helper's apply":   `{"op": "apply", "operation_id": "` + testOperationID + `"}`,
		"the module helper's confirm": `{"op": "confirm", "operation_id": "` + testOperationID + `"}`,
		"a disable":                   `{"op": "disable", "operation_id": "` + testOperationID + `"}`,
		"a shell":                     `{"op": "shell"}`,
		"a folded key":                `{"OP": "restore", "operation_id": "` + testOperationID + `"}`,
		"no op":                       `{"operation_id": "` + testOperationID + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			refused(t, Decode(strings.NewReader(document), &FirewallLocalRequest{}), "DO-NOT-PRINT")
		})
	}
	typ := reflect.TypeOf(FirewallLocalRequest{})
	var tags []string
	for i := 0; i < typ.NumField(); i++ {
		tags = append(tags, typ.Field(i).Tag.Get("json"))
		if typ.Field(i).Type.Kind() != reflect.String {
			t.Errorf("FirewallLocalRequest.%s is %s; every field is a string from a closed shape", typ.Field(i).Name, typ.Field(i).Type.Kind())
		}
	}
	if !slices.Equal(tags, []string{"op", "operation_id,omitempty"}) {
		t.Errorf("the document's members are %q, want op and operation_id alone", tags)
	}
}
