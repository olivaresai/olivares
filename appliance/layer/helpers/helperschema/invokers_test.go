// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestHelperAdmission_InvokersComeFromTheClosedTable(t *testing.T) {
	table := Invokers()
	for _, want := range []Invoker{RepairConsole, Portal, NetGuard} {
		if !slices.Contains(table, want) {
			t.Errorf("the closed invoker table lacks %s", want.Name)
		}
	}
	if RepairConsole.Account != "root" || RepairConsole.Unit != "olivares-repair-console.service" || RepairConsole.TTY != "/dev/tty1" {
		t.Errorf("the repair console is %+v, not root in its unit on /dev/tty1", RepairConsole)
	}
	for _, invoker := range table {
		if invoker.Name == "" || invoker.Account == "" || invoker.Unit == "" {
			t.Errorf("an invoker of the table is not named by its account and its unit: %+v", invoker)
		}
	}

	rules := map[string][]Rule{HelperPower: PowerRules(), HelperSupportBundle: SupportBundleRules(), HelperCert: CertRules(),
		HelperFirewallLocal: FirewallLocalRules()}
	if len(rules) != len(Helpers()) {
		t.Fatalf("the census reads %d helpers, the seam has %d", len(rules), len(Helpers()))
	}
	for helper, helperRules := range rules {
		if Account(helper) == "" {
			t.Errorf("%s names no account for its instances", helper)
		}
		for _, rule := range helperRules {
			for _, invoker := range rule.Invokers {
				if !slices.Contains(table, invoker) {
					t.Errorf("%s %s admits %+v, which is not in the closed invoker table", helper, rule.Subcommand, invoker)
				}
				if invoker == NetGuard && helper != "netrestore" {
					t.Errorf("%s %s admits the network guard, which may invoke the network restore helper alone", helper, rule.Subcommand)
				}
			}
		}
	}
	if Account("../power") != "" || Account("") != "" {
		t.Error("an account was named for a helper the seam does not have")
	}

	t.Run("an invoker is its account, never a uid that happens to match", func(t *testing.T) {
		portalOnly := []Rule{{Subcommand: "status", Mutating: true, Invokers: []Invoker{Portal}}}
		portal := Peer{UID: 998, Account: "olivares-portal", Unit: Portal.Unit, Attested: true}
		if err := Admit(portal, portalOnly, "status"); err != nil {
			t.Fatalf("control: the portal's own process was refused: %v", err)
		}
		for name, peer := range map[string]Peer{
			"root in the portal's unit":            {UID: 0, Account: "root", Unit: Portal.Unit, Attested: true},
			"an account the database did not name": {UID: 998, Unit: Portal.Unit, Attested: true},
			"the portal's account on a terminal":   {UID: 998, Account: "olivares-portal", Unit: Portal.Unit, TTY: "/dev/tty1", Attested: true},
		} {
			var refusal *Refusal
			if err := Admit(peer, portalOnly, "status"); !errors.As(err, &refusal) || refusal.Code != CodeNotAdmitted {
				t.Errorf("%s: %v, want %s", name, err, CodeNotAdmitted)
			}
		}
	})
}

func TestHelperAdmission_TheNetworkGuardIsItsOwnAccountForNetworkRestoreAlone(t *testing.T) {
	guardRow := Invoker{Name: "the network guard", Account: "olivares-net-guard", Unit: "olivares-net-guard.service"}
	if NetGuard != guardRow {
		t.Errorf("the network guard is %+v, want its own account in its own unit: %+v", NetGuard, guardRow)
	}
	// The Appliance Console's row does not change with the guard's.
	if portalRow := (Invoker{Name: "the Appliance Console", Account: "olivares-portal", Unit: "olivares-portal.service"}); Portal != portalRow {
		t.Errorf("the Appliance Console is %+v, want %+v", Portal, portalRow)
	}

	// restore is a rule as the network restore helper states it: one mutating subcommand, for
	// the guard. The guard's process is attested like every other invoker's.
	restore := []Rule{{Subcommand: "restore", Mutating: true, Invokers: []Invoker{NetGuard}}}
	guard := Peer{UID: 997, Account: "olivares-net-guard", Unit: "olivares-net-guard.service", Attested: true}
	if err := Admit(guard, restore, "restore"); err != nil {
		t.Errorf("the network restore helper refused the guard's own process: %v", err)
	}

	t.Run("the guard's rule admits its account in its unit and no one else", func(t *testing.T) {
		for name, c := range map[string]struct {
			peer Peer
			code string
		}{
			"the Appliance Console's account in the guard's unit": {Peer{UID: 998, Account: "olivares-portal", Unit: "olivares-net-guard.service", Attested: true}, CodeNotAdmitted},
			"the Appliance Console in its own unit":               {Peer{UID: 998, Account: "olivares-portal", Unit: "olivares-portal.service", Attested: true}, CodeNotAdmitted},
			"the guard's account in the Appliance Console's unit": {Peer{UID: 997, Account: "olivares-net-guard", Unit: "olivares-portal.service", Attested: true}, CodeNotAdmitted},
			"the guard's account on a terminal":                   {Peer{UID: 997, Account: "olivares-net-guard", Unit: "olivares-net-guard.service", TTY: "/dev/tty1", Attested: true}, CodeNotAdmitted},
			"root in the guard's unit":                            {Peer{UID: 0, Account: "root", Unit: "olivares-net-guard.service", Attested: true}, CodeNotAdmitted},
			"uid 0 named as the guard's account":                  {Peer{UID: 0, Account: "olivares-net-guard", Unit: "olivares-net-guard.service", Attested: true}, CodeNotAdmitted},
			"the guard's account and unit without a pidfd":        {Peer{UID: 997, Account: "olivares-net-guard", Unit: "olivares-net-guard.service"}, CodeNoConnectionIdentity},
		} {
			var refusal *Refusal
			if err := Admit(c.peer, restore, "restore"); !errors.As(err, &refusal) || refusal.Code != c.code {
				t.Errorf("%s: %v, want %s", name, err, c.code)
			}
		}
	})

	t.Run("every other helper refuses the guard", func(t *testing.T) {
		rules := map[string][]Rule{HelperPower: PowerRules(), HelperSupportBundle: SupportBundleRules(), HelperCert: CertRules(),
			HelperFirewallLocal: FirewallLocalRules()}
		if len(rules) != len(Helpers()) {
			t.Fatalf("the census reads %d helpers, the seam has %d", len(rules), len(Helpers()))
		}
		for helper, helperRules := range rules {
			for _, rule := range helperRules {
				var refusal *Refusal
				if err := Admit(guard, helperRules, rule.Subcommand); !errors.As(err, &refusal) {
					t.Errorf("%s %s admitted the network guard (%v), which may invoke the network restore helper alone", helper, rule.Subcommand, err)
				}
			}
		}
	})
}

func TestHelperAdmission_TheReadmeNamesTheNetworkGuardByItsOwnAccount(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(data)), " ")
	named := regexp.MustCompile("\\(`([^`]+)`, `olivares-net-guard\\.service`\\)").FindAllStringSubmatch(text, -1)
	if len(named) == 0 {
		t.Error("README.md does not name the network guard's account and unit")
	}
	for _, pair := range named {
		if pair[1] != "olivares-net-guard" || pair[1] != NetGuard.Account {
			t.Errorf("README.md names %s in the network guard's unit; the guard runs as olivares-net-guard, and the table names %s", pair[1], NetGuard.Account)
		}
	}
	if !strings.Contains(text, "the Appliance Console (`olivares-portal`, `olivares-portal.service`)") {
		t.Error("README.md does not name the Appliance Console by its account and unit")
	}
}
