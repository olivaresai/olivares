// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import "slices"

// Class is what a helper's socket and admission promise. Each helper of the seam has exactly one.
type Class int

// The helper classes.
const (
	// ClassRoot is a root helper (Helpers): its socket is root's alone (root:root, 0600), shipped
	// from the seam's own unit directory or, for a module's program such as HelperFirewallLocal, from
	// that module's, its instances run as the account Account names, and its own rules decide who it
	// admits.
	ClassRoot Class = iota + 1
	// ClassModule is a module helper (ModuleHelpers): its module ships its socket and template, the
	// socket is root's with the Appliance Console's group (root:olivares-portal, 0660) so the
	// console can connect, and the seam admits the Appliance Console alone (ModuleInvokers),
	// whatever its module's rules name.
	ClassModule
)

// The module helpers, by the name of their socket, /run/olivares-helpers/<name>.sock. Each is its
// module's: the module states its document and its rules and ships its units.
const (
	// HelperUnits is the services module's units helper.
	HelperUnits = "units"
	// HelperStorage is the storage module's inventory helper.
	HelperStorage = "storage"
	// HelperFirewall is the firewall module's owner helper, olivares-portal-firewall.
	HelperFirewall = "firewall"
)

// ModuleHelpers returns the module helpers, in a fixed order. A module's helper joins this class
// with the composition of its module.
func ModuleHelpers() []string { return []string{HelperUnits, HelperStorage, HelperFirewall} }

// ClassOf returns the class of the helper name, and false for a name that is no helper of the
// seam. A name is compared byte for byte.
func ClassOf(name string) (Class, bool) {
	switch {
	case slices.Contains(Helpers(), name):
		return ClassRoot, true
	case slices.Contains(ModuleHelpers(), name):
		return ClassModule, true
	}
	return 0, false
}

// ModuleInvokers returns the module class's closed invoker set: the Appliance Console alone. The
// repair console on tty1, another unit and a login session are refused by a module helper even
// when its module's rules name them.
func ModuleInvokers() []Invoker { return []Invoker{Portal} }
