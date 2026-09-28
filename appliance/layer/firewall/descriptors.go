// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import "github.com/olivaresai/olivares/appliance/layer/hostops"

// The module, its verbs, its lock target and its act.
const (
	// Module is the host operation module of this package.
	Module = "firewall"
	// VerbShow reads the measured policy.
	VerbShow = "show"
	// VerbApply loads a candidate derived from the published selection under confirm-or-revert.
	VerbApply = "apply"
	// VerbConfirm makes a pending window's candidate the confirmed policy.
	VerbConfirm = "confirm"
	// VerbRevert loads the confirmed policy again for a pending window.
	VerbRevert = "revert"
	// VerbAppRow adds or removes one application's rows, as that application's own act.
	VerbAppRow = "app-row"
	// Target is the lock key every firewall operation shares with the NetworkManager plane.
	Target = "network"
	// ActVerb is the act verb of every firewall effect.
	ActVerb = "network"
	// Audience is the act audience of the firewall's helper.
	Audience = "appliance-helper:olivares-portal-firewall"
)

// Verbs returns the module's verbs in a fixed order.
func Verbs() []string { return []string{VerbShow, VerbApply, VerbConfirm, VerbRevert, VerbAppRow} }

// Descriptors returns the Firewall tasks for web, CLI and TUI alike. show is a read in every
// sign-in mode. apply, confirm and app-row are acts of verb network, product-up only; revert is
// that act product-up and a repair verb on the tty1 console. Every act is confirmed; none takes
// a rule, a port list or a path, only closed choices and the operation it names.
func Descriptors() []hostops.Descriptor {
	operation := map[string]hostops.InputField{"operation": {Type: "string"}}
	window := hostops.InputField{Type: "integer"}
	base := func(verb string) hostops.Descriptor {
		return hostops.Descriptor{
			ID: Module + "." + verb, Module: Module, Verb: verb, Category: "Network",
			InputSchema:  hostops.InputSchema{Type: "object", Properties: map[string]hostops.InputField{}, Required: []string{}},
			Confirmation: "confirm", Modes: []string{"product-up"}, ActVerb: ActVerb, Audience: Audience,
			OutputSchema: "hostop-v1.schema.json", Surfaces: []string{"web", "cli", "tui"},
		}
	}
	show := base(VerbShow)
	show.Title = "Firewall policy"
	show.Preconditions = []string{"Local admission or an authorized web session"}
	show.Consequence = "Read the measured policy: each port, the interfaces it answers on, this boot's measurement and any open window; no rule changes."
	show.Confirmation, show.Modes, show.ActVerb, show.Audience = "none", []string{"product-up", "product-down-repair", "unavailable"}, "", ""
	show.EquivalentCommand = "olivares-appliance firewall show"

	apply := base(VerbApply)
	apply.Title = "Apply the firewall policy"
	apply.InputSchema.Properties = map[string]hostops.InputField{"revert_after_s": window}
	apply.Preconditions = []string{"An act authorization for the network verb", "A confirmed policy", "No other open network or firewall window", "A revert window from 60 to 600 seconds; 120 when not given"}
	apply.Consequence = "Load the policy derived from the published console selection now. The confirmed policy changes only on confirmation; without it the owner reverts at the deadline and a reboot loads the confirmed policy. Removing the row that carries this session reverts unless confirmed."
	apply.EquivalentCommand = "olivares-appliance firewall apply --revert-after <s>"

	confirm := base(VerbConfirm)
	confirm.Title = "Confirm the firewall change"
	confirm.InputSchema.Properties, confirm.InputSchema.Required = operation, []string{"operation"}
	confirm.Preconditions = []string{"An act authorization for the network verb", "The window is open, in this boot and before its deadline"}
	confirm.Consequence = "Make the loaded candidate the confirmed policy that every boot loads, after reading the table back from the kernel."
	confirm.EquivalentCommand = "olivares-appliance firewall confirm <operation>"

	revert := base(VerbRevert)
	revert.Title = "Revert the firewall change"
	revert.InputSchema.Properties, revert.InputSchema.Required = operation, []string{"operation"}
	revert.Modes = []string{"product-up", "tty1"}
	revert.Preconditions = []string{"An act authorization for the network verb, or the tty1 console", "The window is open"}
	revert.Consequence = "Load the confirmed policy again now and close the window as reverted."
	revert.EquivalentCommand = "olivares-appliance firewall revert <operation>"

	appRow := base(VerbAppRow)
	appRow.Title = "Add or remove an application's firewall rows"
	appRow.InputSchema.Properties = map[string]hostops.InputField{"app": {Type: "string"}, "change": {Type: "string", Enum: []string{"add", "remove"}}, "revert_after_s": window}
	appRow.InputSchema.Required = []string{"app", "change"}
	appRow.Preconditions = []string{"An act authorization for the network verb", "The application is installed; its rows come from its descriptor", "No other open network or firewall window"}
	appRow.Consequence = "The application's own act: add or remove its port rows and nothing else, under confirm-or-revert. Installing the application does not open a port; if this act fails the application stays installed and unexposed."
	appRow.EquivalentCommand = "olivares-appliance firewall app-row <app> add|remove"

	return []hostops.Descriptor{show, apply, confirm, revert, appRow}
}
