// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package services is the appliance's Services module: the unit class table, the closed
// document of the olivares-portal-units helper, the helper's calls to the service manager
// (org.freedesktop.systemd1), the bounded logs read, the host operation descriptors and the
// Services page.
//
// A unit is named, never a path: the name must have systemd's unit-name syntax, be in this
// host's inventory and be admitted by the class table for the verb. No surface sends unit
// content, an ExecStart line or a drop-in. Every act (start, stop, restart, reload, enable,
// disable and logs) is a host operation with its own operation id; list and status change
// nothing and are the helper's read-only set. logs is not in that set: the Appliance Console's
// account cannot read the system journal, so reading it is an authorized act with no host
// effect.
package services

// Module is the host operation module of this package, and its act verb.
const Module = "service"

// HelperName is the helper's socket name: /run/olivares-helpers/units.sock.
const HelperName = "units"

// Audience is the act audience of the service verb.
const Audience = "appliance-helper:olivares-portal-units"

// Closed codes this module answers besides the helper seam's.
const (
	// CodeUnitProtected refuses a verb the class table does not admit for the unit.
	CodeUnitProtected = "unit_protected"
	// CodeInputRefused refuses a document or a unit outside the closed schema or the inventory.
	CodeInputRefused = "input_refused"
	// CodeActNotAdopted refuses an act while the product's act authorization for the service
	// verb is not composed with the helper.
	CodeActNotAdopted = "act_not_adopted"
)
