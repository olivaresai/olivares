// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

// The workstation tool inventory: the AI tools a workstation reports on the fleet
// check-in, the version of each and whether it is signed in. It is Business.
// Community owns the schema (lint:schema-parity) and the published contract and
// never writes a row: the check-in and the read route answer 501.
//
// A tool entry is a driver, a version and a state. The state set is the one the
// provider snapshot and `olivares tool ls` already publish. Nothing that names the
// account (email, plan, method, config directory) and nothing that could be a
// secret is part of the contract.
const (
	// workstationToolsMax bounds the tools one check-in reports.
	workstationToolsMax = 32
	// workstationDriverPattern is a provider driver key: lowercase letters, digits and - _ . only.
	workstationDriverPattern = `^[a-z0-9][a-z0-9._-]{0,63}$`
	// workstationVersionPattern is a version string of at most 64 letters, digits and . + _ ~ - only.
	workstationVersionPattern = `^[0-9A-Za-z][0-9A-Za-z.+_~-]{0,63}$`
)

// workstationToolStates are the sign-in states of a reported tool.
var workstationToolStates = []string{"ready", "not_signed_in", "not_installed", "unknown", "error"}
