// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"errors"
	"slices"
	"testing"
)

func TestHelperClass_RootHelpersKeepTheirContract(t *testing.T) {
	if got := Helpers(); !slices.Equal(got, []string{HelperPower, HelperSupportBundle, HelperCert, HelperFirewallLocal}) {
		t.Fatalf("the root helpers are %v, want exactly power, support-bundle, cert and firewall-local", got)
	}
	for helper, account := range map[string]string{HelperPower: "root", HelperSupportBundle: "olivares-support-bundle", HelperCert: "root",
		HelperFirewallLocal: "root"} {
		if class, ok := ClassOf(helper); !ok || class != ClassRoot {
			t.Errorf("%s is of class %v (known %v), want the root class", helper, class, ok)
		}
		if got := Account(helper); got != account {
			t.Errorf("%s runs as %q, want %s", helper, got, account)
		}
	}
	// A root helper's socket is root's alone, so its rules never name the Appliance Console.
	for helper, rules := range map[string][]Rule{HelperPower: PowerRules(), HelperSupportBundle: SupportBundleRules(), HelperCert: CertRules(),
		HelperFirewallLocal: FirewallLocalRules()} {
		for _, rule := range rules {
			if slices.Contains(rule.Invokers, Portal) {
				t.Errorf("%s %s admits the Appliance Console", helper, rule.Subcommand)
			}
		}
	}
	for _, rule := range PowerRules() {
		if !slices.Equal(rule.Invokers, []Invoker{RepairConsole}) || !rule.Mutating {
			t.Errorf("power %s is %+v, want a mutating subcommand for the repair console on tty1 alone", rule.Subcommand, rule)
		}
	}
	for _, rule := range SupportBundleRules() {
		if len(rule.Invokers) != 0 {
			t.Errorf("support-bundle %s admits %v, want no invoker until its row is admitted", rule.Subcommand, rule.Invokers)
		}
	}
	// The certificate helper's one admitted invoker is the repair console on tty1, for generate; the
	// Appliance Console's begin and install admit no invoker until the certificate audience is adopted.
	for _, rule := range CertRules() {
		want := []Invoker(nil)
		if rule.Subcommand == CertGenerate {
			want = []Invoker{RepairConsole}
		}
		if !rule.Mutating || !slices.Equal(rule.Invokers, want) {
			t.Errorf("cert %s is %+v, want a mutating subcommand for %v", rule.Subcommand, rule, want)
		}
	}
}

func TestHelperClass_ModuleHelpersAreTheirOwnClassForTheApplianceConsoleAlone(t *testing.T) {
	if got := ModuleHelpers(); !slices.Equal(got, []string{HelperUnits, HelperStorage, HelperFirewall}) {
		t.Fatalf("the module helpers are %v, want exactly units, storage and firewall", got)
	}
	for _, helper := range ModuleHelpers() {
		if class, ok := ClassOf(helper); !ok || class != ClassModule {
			t.Errorf("%s is of class %v (known %v), want the module class", helper, class, ok)
		}
		if slices.Contains(Helpers(), helper) {
			t.Errorf("%s is in both classes", helper)
		}
		// Every module template runs as root; the account is the template's.
		if got := Account(helper); got != "root" {
			t.Errorf("%s runs as %q, want root, as its template states", helper, got)
		}
	}
	if got := ModuleInvokers(); !slices.Equal(got, []Invoker{Portal}) {
		t.Errorf("the module class admits %v, want the Appliance Console alone", got)
	}
	for _, name := range []string{"", "../units", "units.sock", "Units", "storage/..", " storage", "power ", "Firewall", "firewall.sock", "nftables"} {
		if class, ok := ClassOf(name); ok {
			t.Errorf("%q is of class %v, but it is no helper of the seam", name, class)
		}
	}
}

// The firewall owner's helper joins the module class with its module. Its rules name the repair
// console on tty1 for status and revert, and the class narrows them to the Appliance Console: the
// seam refuses the repair console for the firewall helper, and the class's invoker set is not
// widened to fit the module's rules. A tty1 route to the firewall is a design decision of its own.
func TestHelperClass_TheFirewallHelperJoinsTheModuleClassAsRoot(t *testing.T) {
	if HelperFirewall != "firewall" {
		t.Fatalf("the firewall helper is named %q; its socket is /run/olivares-helpers/firewall.sock", HelperFirewall)
	}
	if class, ok := ClassOf("firewall"); !ok || class != ClassModule {
		t.Fatalf("firewall is of class %v (known %v), want the module class", class, ok)
	}
	if slices.Contains(Helpers(), "firewall") {
		t.Error("firewall is a root helper too")
	}
	if got := Account("firewall"); got != "root" {
		t.Errorf("firewall runs as %q, want root, as its template states", got)
	}
	if got := ModuleInvokers(); !slices.Equal(got, []Invoker{Portal}) {
		t.Fatalf("the module class admits %v, want the Appliance Console alone", got)
	}

	// Rules shaped as the firewall module states them: status for both consoles, revert a mutating
	// subcommand for both, apply for the Appliance Console alone.
	rules := []Rule{
		{Subcommand: "status", Invokers: []Invoker{Portal, RepairConsole}},
		{Subcommand: "apply", Mutating: true, Invokers: []Invoker{Portal}},
		{Subcommand: "revert", Mutating: true, Invokers: []Invoker{Portal, RepairConsole}},
	}
	tty1 := Peer{UID: 0, Account: "root", Unit: RepairConsole.Unit, TTY: RepairConsole.TTY, Attested: true}
	console := Peer{UID: 998, Account: Portal.Account, Unit: Portal.Unit, Attested: true}
	for _, subcommand := range []string{"status", "revert"} {
		if err := Admit(tty1, rules, subcommand); err != nil {
			t.Fatalf("control: the rules as written admit the repair console for %s: %v", subcommand, err)
		}
		var refusal *Refusal
		if err := AdmitHelper(tty1, HelperFirewall, rules, subcommand); !errors.As(err, &refusal) || refusal.Code != CodeNotAdmitted {
			t.Errorf("the firewall helper's %s for the repair console on tty1: %v, want %s", subcommand, err, CodeNotAdmitted)
		}
		if err := AdmitHelper(console, HelperFirewall, rules, subcommand); err != nil {
			t.Errorf("the firewall helper's %s for the Appliance Console: %v", subcommand, err)
		}
	}
	var refusal *Refusal
	if err := AdmitHelper(tty1, HelperFirewall, rules, "apply"); !errors.As(err, &refusal) || refusal.Code != CodeNotAdmitted {
		t.Errorf("the firewall helper's apply for the repair console on tty1: %v, want %s", err, CodeNotAdmitted)
	}
}
