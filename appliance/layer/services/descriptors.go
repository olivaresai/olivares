// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import "github.com/olivaresai/olivares/appliance/layer/hostops"

// readModes are the modes a read serves: every sign-in state, including none.
var readModes = []string{"product-up", "product-down-repair", "unavailable"}

// effectText holds each effect's title and consequence.
var effectText = map[string][2]string{
	OpStart:   {"Start a unit", "Start the unit. The operation succeeds only when the unit is measured active afterwards."},
	OpStop:    {"Stop a unit", "Stop the unit, and the units that require it. Stopping the SSH server ends remote shell access. The operation succeeds only when the unit is measured inactive afterwards."},
	OpRestart: {"Restart a unit", "Stop and start the unit; its service is interrupted. The operation succeeds only when the unit is measured active afterwards."},
	OpReload:  {"Reload a unit", "Ask the unit to reload its configuration without stopping. The operation succeeds only when the unit is measured active afterwards."},
	OpEnable:  {"Enable a unit", "Enable the unit's file so that it starts at boot. The operation succeeds only when the file is measured enabled afterwards."},
	OpDisable: {"Disable a unit", "Disable the unit's file so that it does not start at boot. Disabling the SSH server removes remote shell access from the next boot. The operation succeeds only when the file is measured disabled afterwards."},
}

// Descriptors returns the Services tasks, one per subcommand, for web, CLI and TUI alike. list
// and status are reads in every sign-in mode. logs is an act of verb service, product-up or on
// the tty1 console. Each effect is an act of verb service, product-up only. Every act is
// confirmed, and its unit is a unit name, never a path.
func Descriptors() []hostops.Descriptor {
	out := make([]hostops.Descriptor, 0, len(Ops()))
	for _, op := range Ops() {
		out = append(out, descriptor(op))
	}
	return out
}

func descriptor(op string) hostops.Descriptor {
	d := hostops.Descriptor{
		ID:       Module + "." + op,
		Module:   Module,
		Verb:     op,
		Category: "Services",
		InputSchema: hostops.InputSchema{
			Type:       "object",
			Properties: map[string]hostops.InputField{"unit": {Type: "string"}},
			Required:   []string{"unit"},
		},
		OutputSchema:      "hostop-v1.schema.json",
		Surfaces:          []string{"web", "cli", "tui"},
		EquivalentCommand: "olivares-appliance service " + op + " <unit>",
	}
	switch op {
	case OpList:
		d.Title = "List units"
		d.InputSchema.Properties = map[string]hostops.InputField{}
		d.InputSchema.Required = []string{}
		d.Preconditions = []string{"Local admission or an authorized web session"}
		d.Consequence = "Read the loaded units and their classes; no unit changes."
		d.Confirmation = "none"
		d.Modes = readModes
		d.EquivalentCommand = "olivares-appliance service list"
	case OpStatus:
		d.Title = "Unit status"
		d.Preconditions = []string{"Local admission or an authorized web session", "The unit is in this host's inventory"}
		d.Consequence = "Read the unit's class, what the class admits, its active and sub state, its enablement and its dependents; no unit changes."
		d.Confirmation = "none"
		d.Modes = readModes
	case OpLogs:
		d.Title = "Unit logs"
		d.InputSchema.Properties["lines"] = hostops.InputField{Type: "integer"}
		d.Preconditions = []string{"An act authorization for the service verb, or the tty1 console", "The unit is in this host's inventory", "From 1 to 500 entries; 100 when not given"}
		d.Consequence = "Read the unit's latest journal entries: time, priority and message only. The journal is sensitive, so this is an authorized act; it changes nothing on the host."
		d.Confirmation = "confirm"
		d.Modes = []string{"product-up", "tty1"}
		d.ActVerb = Module
		d.Audience = Audience
		d.EquivalentCommand = "olivares-appliance service logs <unit> --lines <n>"
	default:
		d.Title = effectText[op][0]
		d.Preconditions = []string{"An act authorization for the service verb", "The unit is in this host's inventory", "The unit's class admits the verb"}
		d.Consequence = effectText[op][1]
		d.Confirmation = "confirm"
		d.Modes = []string{"product-up"}
		d.ActVerb = Module
		d.Audience = Audience
	}
	return d
}
