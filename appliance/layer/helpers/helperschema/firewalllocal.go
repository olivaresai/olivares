// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

// HelperFirewallLocal is the host firewall's local entry point, olivares-portal-firewall-local: a root
// helper over the same firewall owner as the module helper HelperFirewall, whose one admitted invoker
// is the repair console on tty1. It is a socket of its own, root's alone, so the module helper's socket
// keeps its one audience and ModuleInvokers is not widened.
const HelperFirewallLocal = "firewall-local"

// The local entry point's closed set. status reads the owner's state and the target a restoration
// would load; revert loads the confirmed policy again for an unconfirmed window, as the module helper's
// revert does; restore returns the confirmed policy to the one derived from the validated appliance
// answers, and is not a revert.
const (
	FirewallLocalStatus  = "status"
	FirewallLocalRevert  = "revert"
	FirewallLocalRestore = "restore"
)

// FirewallLocalRequest is olivares-portal-firewall-local's document: {"op": "status"} or
// {"op": "revert" | "restore", "operation_id"}. No field is a policy, a rule, a port, an interface or
// a path: restore derives its target on the host from the validated answers and never takes one from
// its caller.
type FirewallLocalRequest struct {
	Op          string `json:"op"`
	OperationID string `json:"operation_id,omitempty"`
}

// Subcommand implements Request.
func (r FirewallLocalRequest) Subcommand() string { return r.Op }

// Operation implements Request.
func (r FirewallLocalRequest) Operation() string { return r.OperationID }

// Validate implements Request. status carries no operation id; revert and restore carry the one the
// console minted when its operator confirmed.
func (r FirewallLocalRequest) Validate() error {
	switch r.Op {
	case FirewallLocalStatus:
		if r.OperationID != "" {
			return refuse("$.operation_id", "status changes nothing, so it carries no operation id")
		}
		return nil
	case FirewallLocalRevert, FirewallLocalRestore:
		return requireOperationID(r.OperationID)
	case "":
		return refuse("$.op", "required: status, revert or restore")
	}
	return refuse("$.op", "expected status, revert or restore")
}

// FirewallLocalRules is olivares-portal-firewall-local's admission: status, revert and restore for the
// repair console on tty1 alone. The Appliance Console reaches the firewall through the module helper,
// never through this socket.
func FirewallLocalRules() []Rule {
	return []Rule{
		{Subcommand: FirewallLocalStatus, Invokers: []Invoker{RepairConsole}},
		{Subcommand: FirewallLocalRevert, Mutating: true, Invokers: []Invoker{RepairConsole}},
		{Subcommand: FirewallLocalRestore, Mutating: true, Invokers: []Invoker{RepairConsole}},
	}
}
