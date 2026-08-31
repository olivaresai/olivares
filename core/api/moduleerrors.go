// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/store"
)

// StoreErrorStatus is the ONE place a product module decides what a store or
// license sentinel means on the wire. Every module's writeStoreError delegates
// its default arm here, so the answer for a sentinel is decided once instead of
// thirty-six times.
//
// WHY THIS EXISTS, measured on 2026-08-12 over the whole modules tree. The family
// is thirty-six functions with the signature func(http.ResponseWriter, error) —
// twenty-six named writeStoreError plus ten siblings — each enumerating by hand
// the sentinels its author happened to know about. Nothing watched them, and they
// had drifted in a direction nobody was looking at: not in the six holes anyone
// had noticed, but in the sentinels core/api added LATER and no copy ever
// received.
//
//	store.ErrTenantSuspended        core/api answers 423   0 of 36 copies had it
//	store.ErrTenantNotInService     core/api answers 423   0 of 36 copies had it
//	store.ErrNotLeader              core/api answers 503   2 of 36 copies had it
//	store.ErrResidencyViolation     core/api answers 403   1 of 36 copies had it
//
// All four are REACHABLE from an ordinary module route, and that is the point:
// suspension.Guard and residency.Guard decorate the very store a module reaches
// through mc.Data (cmd/olivares/boot.go:758,771 — the suspension guard is armed
// unconditionally and checks View AND Mutate), and sqlStore.Mutate answers
// ErrNotLeader on any standby (core/internal/store/sqlstore/store.go:1381) while
// the middleware that would intercept it is opt-in (middleware.go:176). So a
// suspended tenant was answered 423 by /v1/orgs and 500 "internal error" by every
// /v1/m/ route in the product, for the same refusal, on the same request.
//
// A 500 is not merely the wrong number. It blames the server for a decision the
// server made on purpose, it is indistinguishable from a real fault in the logs,
// and — for ErrNotLeader — it is not retried, where the 503 it should have been
// is. That is the same reasoning the addon_requires_license arm of statusFor was
// written for, one layer out.
//
// THE STATUS IS NOT RE-DECIDED HERE. It comes from statusFor, so core/api and the
// modules cannot answer differently for the same error however either changes; a
// sentinel added to statusFor tomorrow reaches all thirty-six copies without
// touching one of them. What this function adds is the module envelope's MESSAGE,
// because that envelope carries no code field ({"error":{"message":…}}) where
// core/api's carries one.
//
// The returned message is safe to hand a client by construction: it is keyed on
// the curated code statusFor produced, never on err.Error(), so it cannot echo a
// wrapped error's internals. ok reports whether the error was a sentinel this
// family answers for; when it is false the status and message are the 500
// "internal error" fallback, so a caller that ignores ok stays correct and one
// that reads it can keep its own default arm.
func StoreErrorStatus(err error) (status int, message string, ok bool) {
	if err == nil {
		// A nil error is not this function's business — every mapper in the family
		// answers it 200 with an empty body in its own first arm, which is a writing
		// decision and not a mapping one. Reaching here with nil is a caller bug, so
		// it fails closed rather than manufacturing a success.
		return http.StatusInternalServerError, "internal error", false
	}
	if errors.Is(err, store.ErrCursorWithSort) || errors.Is(err, store.ErrUnknownEntity) {
		// ⚠ A MEASURED DIVERGENCE THAT IS DELIBERATELY *NOT* RESOLVED HERE, named so
		// the next session finds it instead of rediscovering it.
		//
		// ErrCursorWithSort agrees: statusFor answers 400 and so does the family.
		// ErrUnknownEntity does NOT — statusFor folds it in with ErrNotFound and
		// answers 404, while twenty-three module copies answer 400 "invalid query",
		// modules/sessions/api.go:190 answers 404 (it moved deliberately, with its
		// reason written beside it) and modules/claudeadoption/dto.go:188 answers 503
		// "adoption store not ready". Three readings exist because the sentinel has
		// two producers with opposite blame: an unknown SORT COLUMN is the client's
		// (400), an unregistered entity KIND is the deployment's (503, or 500).
		//
		// Routing it through statusFor would silently move twenty-three modules from
		// 400 to 404 in a change nobody asked for and only five tests in the whole
		// modules tree would notice. Picking the right answer is a semantic decision
		// about what the sentinel MEANS, and it belongs to whoever splits the two
		// producers apart — not to the session that centralized the mapping.
		return http.StatusBadRequest, "invalid query", true
	}
	if addon, operation, isRefusal := license.AddonRefusal(err); isRefusal {
		return http.StatusForbidden, AddonRefusalMessage(addon, operation), true
	}
	status, code := statusFor(err)
	msg, known := moduleErrorMessage[code]
	if !known {
		// Deliberately closed: a code with no sentence here is one the module family
		// has never been able to receive, or one nobody has decided a wording for.
		// Inventing a humanized form of the code (which writeError does for its
		// honest-seam band) would put an unreviewed sentence in front of a customer.
		return http.StatusInternalServerError, "internal error", false
	}
	return status, msg, true
}

// moduleErrorMessage is the module envelope's sentence for each statusFor code a
// product module can actually receive from the store. It is keyed on the CODE and
// not on the sentinel so it cannot drift from statusFor's grouping: fold two
// sentinels into one code there and both arrive here with one wording, as
// ErrWorkspaceConfinement and ErrWorkspaceLineageRequired already do.
//
// Every sentence below is the wording twenty or more of the thirty-six copies
// already emitted on 2026-08-12, so centralizing them changed no bytes on the
// wire for those. The three module-local wordings that differ (catalog's longer
// conflict sentence, notify's "not the active writer; retry against the leader",
// finops/models' "version conflict") keep their own arm ahead of the delegation
// and are unchanged.
//
// tenant_suspended and tenant_not_in_service are the two that had no wording at
// all, because no copy carried them. They are short on purpose: core/api answers
// these two with err.Error(), which names the tenant and its raw org status, and
// that is an operator sentence rather than a client one.
// There is deliberately NO "bad_request" entry, and no "addon_requires_license"
// one. Both codes are already answered above the statusFor call — the query
// sentinels by the divergence arm, the refusal by AddonRefusalMessage — so an
// entry here would be dead. Worse than dead for bad_request: statusFor also folds
// auth.ErrInvalidRole and auth.ErrInvalidToken into that code, and a module that
// reaches this function with one of those would be moved from its current 500 to
// a 400 reading "invalid query", which is neither asked for nor true.
var moduleErrorMessage = map[string]string{
	"not_found":             "not found",
	"conflict":              "conflict",
	"workspace_confined":    "workspace confined",
	"residency_violation":   "residency violation",
	"tenant_suspended":      "tenant suspended",
	"tenant_not_in_service": "tenant not in service",
	"not_leader":            "not leader",
	"audit_spool_full":      "audit spool full",
}

// AddonRefusalMessage is the client-facing sentence for an add-on entitlement
// refusal. It moved here from modules/compliance/helpers.go so all thirty-six
// mappers say the same thing: compliance was the ONLY one of them that answered
// this refusal at all, and the other thirty-five sent it to their default arm and
// answered 500 "internal error" — telling an operator their server is broken when
// in fact their license lapsed.
//
// The reassurance is not decoration. The refusal gates a commercial ADD-ON
// operation, never a read of your own data, and an operator who reads "forbidden"
// without that clause has no way to tell the two apart at the moment they are
// least able to check.
//
// INERT IN THIS BUILD, and that is not a reason to leave it out. Nothing in the
// open tree constructs the error: license.AddonRequired (core/license/entitlement.go:61)
// has no caller outside its own file and tests, because the addonGate that builds
// it lives in the closed enterprise overlay. The arm is here for the same reason
// statusFor keeps user_cap_requires_enterprise mapped after B10 made it
// unreachable — the wire meaning of a refusal does not depend on which build is
// currently able to produce it.
func AddonRefusalMessage(addon, operation string) string {
	const unaffected = "; reading, verifying and exporting your data are unaffected"
	switch {
	case addon != "" && operation != "":
		return "the \"" + addon + "\" add-on is required for " + operation + unaffected
	case addon != "":
		return "the \"" + addon + "\" add-on is required for this operation" + unaffected
	default:
		return "a commercial add-on is required for this operation" + unaffected
	}
}
