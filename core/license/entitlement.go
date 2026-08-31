// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license

import (
	"errors"
	"fmt"
)

// ErrAddonRequiresLicense is the STABLE, wire-visible refusal of a commercial add-on
// operation. It is the open-core half of the AddonGate contract: the CLOSED enterprise
// build decides (it is the only build that consumes an attested entitlement, LICENSING.md
// §ADR-0010), and this sentinel is what lets the open binary's error mappers turn that
// decision into one status code and one code string instead of a generic 500.
//
// THE SEAM, and why it is shaped this way. The enterprise overlay lives in a separate
// repository and cannot export a type the open binary matches on; the open binary must not
// grow a dependency on the overlay either. So the sentinel lives HERE, in the open core,
// exactly as core/auth.ErrMultiIDPRequiresEnterprise does for the second-IdP line — a
// sentinel defined open, returned closed. The enterprise gate wraps this error; every open
// mapper matches it with errors.Is.
//
// IT GATES NOTHING BY ITSELF. No open-core path constructs it: the open build has no add-on
// to refuse, so this file is inert there, in the same way core/auth keeps
// ErrUserCapRequiresEnterprise mapped to its own 403 after B10 made it unreachable. Wiring a
// code is not wiring a control.
var ErrAddonRequiresLicense = errors.New("license: addon_requires_license")

// AddonRequiredError is the refusal WITH its subject: which add-on, and which operation.
// The contract is a stable error PER OPERATION — "denied" without naming what was denied
// sends an operator to the wrong place — so the gate raises this and the mappers render it.
//
// Addon and Operation are identifiers the deployment already knows (a catalog add-on id
// and the operation being attempted). They are safe to put on the wire: they describe the
// caller's own request, and they leak nothing about other tenants or about internals.
type AddonRequiredError struct {
	// Addon is the catalog add-on id the operation needs, e.g. "compliance-packs".
	Addon string
	// Operation is the operation refused, e.g. "compliance.depth.export".
	Operation string
}

func (e *AddonRequiredError) Error() string {
	switch {
	case e.Addon != "" && e.Operation != "":
		return fmt.Sprintf("license: addon_requires_license: %q requires the %q add-on", e.Operation, e.Addon)
	case e.Addon != "":
		return fmt.Sprintf("license: addon_requires_license: requires the %q add-on", e.Addon)
	default:
		return ErrAddonRequiresLicense.Error()
	}
}

// Unwrap makes errors.Is(err, ErrAddonRequiresLicense) true for every refusal, however it
// was constructed, so a mapper never has to know which shape it received.
func (e *AddonRequiredError) Unwrap() error { return ErrAddonRequiresLicense }

// AddonRequired builds the refusal. The enterprise gate is the only intended caller.
func AddonRequired(addon, operation string) error {
	return &AddonRequiredError{Addon: addon, Operation: operation}
}

// AddonRefusal reports whether err is an add-on entitlement refusal and, if so, the add-on
// and operation it named (either may be empty). It is the one accessor the mappers use, so
// the errors.As dance lives in a single place.
func AddonRefusal(err error) (addon, operation string, ok bool) {
	if !errors.Is(err, ErrAddonRequiresLicense) {
		return "", "", false
	}
	var are *AddonRequiredError
	if errors.As(err, &are) {
		return are.Addon, are.Operation, true
	}
	return "", "", true
}
