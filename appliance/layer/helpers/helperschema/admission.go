// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import "slices"

// Peer is the invoker of one connection, as the kernel reports it: the uid the socket's peer
// credentials carry (SO_PEERCRED) and the account the system's user database names for it; and,
// for the process the peer's pidfd names (SO_PEERPIDFD), the unit whose cgroup holds it and its
// controlling terminal. Attested is true only when the pidfd was available and its process was
// still alive after its cgroup and terminal were read, so Unit and TTY are that process's. A
// peer is never taken from the request document, an argument or the environment.
type Peer struct {
	UID     uint32
	Account string
	Unit    string
	// TTY is the process's controlling terminal: /dev/ttyN for a virtual console, "" for none,
	// and a device that names no virtual console otherwise.
	TTY      string
	Attested bool
}

// Invoker is who may ask for a subcommand: the account its process runs as, the one unit its
// process must run in and the controlling terminal it must have ("" for none).
type Invoker struct {
	// Name says who this is, for a refusal and for the audit record.
	Name    string
	Account string
	Unit    string
	TTY     string
}

// The closed invoker table. A helper's rules admit only these, each attested by its account,
// its unit and its terminal, never by a name, mode or flag it supplies.
var (
	// RepairConsole is the Appliance Repair Console on tty1: root, in its own unit, on the
	// first virtual console.
	RepairConsole = Invoker{Name: "the repair console on tty1", Account: "root",
		Unit: "olivares-repair-console.service", TTY: "/dev/tty1"}
	// Portal is the Appliance Console daemon on 9443.
	Portal = Invoker{Name: "the Appliance Console", Account: "olivares-portal", Unit: "olivares-portal.service"}
	// NetGuard is the NetworkManager plane's guard, which runs as its own static account in its
	// own unit, never as the Appliance Console's. It may invoke the network restore helper alone.
	NetGuard = Invoker{Name: "the network guard", Account: "olivares-net-guard", Unit: "olivares-net-guard.service"}
)

// Invokers returns the closed invoker table. The update timer joins it with the slice that
// names its unit and account.
func Invokers() []Invoker { return []Invoker{RepairConsole, Portal, NetGuard} }

// Account returns the account a helper's instances run as, which its service template states
// with User=: root where the effect's owner answers root alone, the helper's own account
// otherwise. The module helpers run as root, as their modules' templates state. An unknown helper
// has none.
func Account(helper string) string {
	switch helper {
	case HelperPower, HelperUnits, HelperStorage, HelperFirewall, HelperCert, HelperFirewallLocal:
		return "root"
	case HelperSupportBundle:
		return "olivares-support-bundle"
	}
	return ""
}

// Rule admits one subcommand for its invokers. A mutating subcommand changes the host or a
// spool; every other one only answers.
type Rule struct {
	Subcommand string
	Mutating   bool
	// Invokers is closed; empty means no invoker is admitted yet and the subcommand is
	// unavailable.
	Invokers []Invoker
}

// PowerRules is olivares-portal-power's admission: reboot and shut down, for the repair
// console on tty1 alone. The console's product-up and repair routes arrive with the product's
// act authorization and are not admitted here.
func PowerRules() []Rule {
	return []Rule{
		{Subcommand: PowerReboot, Mutating: true, Invokers: []Invoker{RepairConsole}},
		{Subcommand: PowerShutdown, Mutating: true, Invokers: []Invoker{RepairConsole}},
	}
}

// SupportBundleRules is the support-bundle helper's admission: no invoker yet. The verb stays
// unavailable on every surface until its consumer, peer, spool owner and path are admitted
// together as one row.
func SupportBundleRules() []Rule {
	return []Rule{
		{Subcommand: SupportBundleProduce, Mutating: true},
		{Subcommand: SupportBundleFetch, Mutating: false},
	}
}

// Refusal is an admission refusal.
type Refusal struct {
	Code   string
	Reason string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Reason }

// Admit decides whether p may ask for subcommand under rules. A mutating subcommand is refused
// to a peer whose process the kernel could not identify, whoever it claims or seems to be:
// without SO_PEERPIDFD there is no proven connection identity, and a pid read later could name
// another process. Then the invoker must match one of the rule's invokers by the account, the
// unit and the controlling terminal the kernel reported. Root is uid 0 as the kernel reports it,
// whatever the user database names: an account named root matches only uid 0, and no other
// account matches uid 0.
func Admit(p Peer, rules []Rule, subcommand string) error {
	i := slices.IndexFunc(rules, func(r Rule) bool { return r.Subcommand == subcommand })
	if i < 0 {
		return &Refusal{Code: CodeInputRefused, Reason: "no such subcommand"}
	}
	rule := rules[i]
	if rule.Mutating && !p.Attested {
		return &Refusal{Code: CodeNoConnectionIdentity,
			Reason: "the connection's process could not be identified (no SO_PEERPIDFD), so no mutating subcommand is admitted"}
	}
	if len(rule.Invokers) == 0 {
		return &Refusal{Code: CodeVerbUnavailable, Reason: "no invoker is admitted for this subcommand yet"}
	}
	for _, invoker := range rule.Invokers {
		if p.Attested && p.Account != "" && p.Account == invoker.Account && (p.UID == 0) == (invoker.Account == "root") &&
			p.Unit == invoker.Unit && p.TTY == invoker.TTY {
			return nil
		}
	}
	return &Refusal{Code: CodeNotAdmitted, Reason: "this invoker is not admitted for this subcommand"}
}

// AdmitHelper decides as Admit does for the helper name under its class. A module helper's rules
// keep only the invokers of ModuleInvokers, so whatever its module's rules name, a module helper
// admits the Appliance Console alone. A root helper, and a helper the registry does not name, is
// decided by its own rules.
func AdmitHelper(p Peer, helper string, rules []Rule, subcommand string) error {
	if class, ok := ClassOf(helper); ok && class == ClassModule {
		narrowed := make([]Rule, 0, len(rules))
		for _, rule := range rules {
			kept := Rule{Subcommand: rule.Subcommand, Mutating: rule.Mutating}
			for _, invoker := range rule.Invokers {
				if slices.Contains(ModuleInvokers(), invoker) {
					kept.Invokers = append(kept.Invokers, invoker)
				}
			}
			narrowed = append(narrowed, kept)
		}
		rules = narrowed
	}
	return Admit(p, rules, subcommand)
}
