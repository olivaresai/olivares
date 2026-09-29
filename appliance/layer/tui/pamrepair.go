// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import "github.com/olivaresai/olivares/appliance/layer/repair"

// PortalPAMRepair restores the portal's sign-in stack from the package's pristine copy and measures
// it as the mode selector measures it. repair.PortalPAM is the installed one.
type PortalPAMRepair interface {
	Restore() (repair.PAMResult, error)
}

// WithPortalPAM returns the console restoring the portal's stack through r.
func (c Console) WithPortalPAM(r PortalPAMRepair) Console {
	c.portalPAM = r
	return c
}

// pamConfirm is the question Run asks after repair-portal-pam is selected.
const pamConfirm = "Type repair to restore the sign-in stack of the console on 9443 from this package's pristine " +
	"copy, or anything else to change nothing:"

// RepairPortalPAM restores the portal's stack when answer is repair, and reports the restore and the
// measurement. It changes nothing else, and nothing at all without a qualified sign-in.
func (c Console) RepairPortalPAM(answer string) string {
	if !c.qualified() {
		return "repair-portal-pam: " + signInRequired
	}
	if answer != "repair" {
		return "Nothing was changed."
	}
	if c.portalPAM == nil {
		return "repair-portal-pam: refused: the restore is not wired to this console, so nothing was changed."
	}
	result, err := c.portalPAM.Restore()
	if err != nil || !result.Restored {
		return "repair-portal-pam: refused: the pristine copy could not be restored, so the stack is unchanged."
	}
	if !result.Usable {
		return "repair-portal-pam: the pristine copy was restored, and the stack is not usable: " + result.Reason
	}
	return "repair-portal-pam: the pristine copy was restored, and the stack measures usable: the file is the " +
		"package's own and the olivares-admins group exists."
}
