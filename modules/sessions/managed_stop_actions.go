// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "github.com/olivaresai/olivares/core/auth"

// managed_stop_actions.go (P2 / W2) — the two Cedar actions a managed Stop asks
// about, and the module's declaration of them.
//
// They exist as ACTIONS rather than as permissions because the permission parser
// is a grammar whose last segment must be read, write or admin. "run:stop" and
// "lease:stop" cannot be spelled inside it without breaking the tier ordering for
// every other module, so the questions name their action here and keep their
// ordinary permission (sessions:run:write, sessions:lease:admin) as the RBAC term.
//
// Declaring them is what makes them usable: the engine refuses a route whose
// action its own module never declared, so an action nobody registered is
// deny-closed rather than free.

const (
	// actionRunStop is the managed Stop of one run, asked on the run itself.
	actionRunStop auth.CedarAction = "run:stop"
	// actionLeaseStop is the work-lease authority a work-BOUND run additionally
	// requires. It is the same barrier as takeover, revoke and clock-rebase: a
	// caller who may stop a run is not thereby entitled to end the work it holds.
	actionLeaseStop auth.CedarAction = "lease:stop"
)

// managedRunStopMetadata is question 2: may this principal stop THIS run?
//
// AAL3 is not decoration. A stop is terminal and irreversible, so the assurance
// floor is the one the engine itself verified through a ceremony, never one a
// token asserts about itself.
var managedRunStopMetadata = auth.RouteMetadata{
	CedarAction:     string(actionRunStop),
	RBACMinimumRole: auth.RoleEditor,
	MinimumAAL:      auth.AAL3,
}

// managedStopLeaseMetadata is question 3, asked ONLY for a work-bound run: may
// this principal end the authority the work lease holds?
//
// The route floor is the ratified editor (correction 2 §3.3), and it is NOT the
// barrier. The question is asked with sessions:lease:admin, the same permission as
// takeover, revoke and clock-rebase, and that permission's own admin tier is what
// refuses an editor: a principal who may stop the run is not thereby entitled to
// end the work it holds.
var managedStopLeaseMetadata = auth.RouteMetadata{
	CedarAction:     string(actionLeaseStop),
	RBACMinimumRole: auth.RoleEditor,
	MinimumAAL:      auth.AAL3,
}

// Actions declares the Cedar Action IDs this module's governed routes may name,
// implementing api.ActionDeclarer. The composition root mounts it through
// auth.RegisterModuleActions; a module that declared nothing reaches no action.
func (m *Module) Actions() []auth.CedarAction {
	return []auth.CedarAction{actionRunStop, actionLeaseStop}
}
