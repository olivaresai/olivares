// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Steering a session (input, resume, peers) is Community for the person who started it.
// Steering a session another person started is Business: a Community engine answers
// 501 business_required, as every Business capability does, and the Business edition sets
// Dependencies.SteerOthers, which it asks on each call: without a licence that covers
// steering, the colleague hears 403 addon_requires_license. The role check
// (sessions:run:write) runs first and is the same in both editions.

// refuseForeignSteering writes the edition's refusal (Community's 501, or a Business
// engine's licence refusal) and reports true when the caller is steering a session another
// person started. It also reports true, with the run error written, when the run cannot
// be read: the gate never opens on an error. The route already answered an unknown or
// concealed run with its own 404.
func (m *Module) refuseForeignSteering(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) bool {
	var unlicensed error
	if m.rt.SteerOthers != nil {
		if unlicensed = m.rt.SteerOthers(); unlicensed == nil {
			return false
		}
	}
	rec, err := m.loadRun(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return true
	}
	if !foreignSteering(runLauncher(rec), mc.Principal) {
		return false
	}
	if unlicensed != nil {
		status, body, _ := api.StoreErrorBody(unlicensed)
		writeJSON(w, status, body)
		return true
	}
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("business_required",
		"Steering a session another person started requires Olivares Business."))
	return true
}

// runLauncher is who started the run: the account stamped when the row was created, which
// holds for a token launch too, and for a row written before that column the account
// inside its claim holder when that is a login's "user:<id>".
func runLauncher(rec model.Record) auth.PersonRef {
	ref := auth.PersonRefFromActor(rec.String(colClaimHolder))
	if user := rec.String(colRunLauncherUserID); user != "" {
		ref.User = user
	}
	return ref
}

// foreignSteering reports whether launcher is provably a different account than the caller.
// It asks auth.TwoDistinctPeople to refuse when the verdict is undetermined, so only two
// attributable accounts that differ count: the launcher's own API token is the launcher, and
// a run launched by an agent or a work lease, or one written before the account was stamped
// under a token, carries no account to compare and is not another person's.
func foreignSteering(launcher auth.PersonRef, caller auth.Principal) bool {
	distinct, _ := auth.TwoDistinctPeople(launcher, auth.PersonRefOf(caller), auth.RefuseWhenUndetermined)
	return distinct
}
