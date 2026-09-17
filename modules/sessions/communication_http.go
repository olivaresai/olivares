// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"net/http"
)

// writeCommunicationError maps communication decisions whose wire meaning
// differs from the shared store mapper. Route admission never makes the
// communication kernel ready. In
// particular, a semantic plan precondition is 412 while an ordinary
// store.ErrConflict (including idempotency rebind) continues through
// writeStoreError as 409.
func writeCommunicationError(w http.ResponseWriter, err error) {
	status, code, verdict, ok := communicationHTTPDisposition(err)
	if !ok {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{
		"verdict": verdict,
		"code":    code,
		"error": map[string]string{
			"code": code, "message": code,
		},
	})
}

func communicationHTTPDisposition(
	err error,
) (status int, code string, verdict AssessmentVerdict, ok bool) {
	switch {
	case errors.Is(err, ErrCommunicationPlanChanged):
		return http.StatusPreconditionFailed, "plan_changed", VerdictBroken, true
	case errors.Is(err, errDirectNoticeCursorVersionMismatch):
		return http.StatusPreconditionFailed, "version_mismatch", VerdictBroken, true
	case errors.Is(err, errDirectNoticeCursorVersionRequired),
		errors.Is(err, errDirectNoticeAckVersionRequired),
		errors.Is(err, errHandoffVersionRequired),
		errors.Is(err, errCommunicationChannelVersionRequired):
		return http.StatusPreconditionRequired, "version_required", VerdictBroken, true
	case errors.Is(err, ErrCommunicationNotFound):
		return http.StatusNotFound, "not_found", VerdictBroken, true
	case errors.Is(err, ErrCommunicationForbidden):
		return http.StatusForbidden, "forbidden", VerdictBroken, true
	case errors.Is(err, ErrCommunicationTerminal):
		return http.StatusConflict, "terminal", VerdictBroken, true
	// The administrative sheet's OWN conflict: the Channel's version or ACL
	// revision moved between two pages of one paginated history. It is named
	// separately from `terminal` and from the store conflict the mutations
	// return, because its remedy is specific — discard the pages collected so
	// far and restart — and because reinterpreting every store conflict as this
	// would tell a caller to restart a listing when a write actually lost a CAS.
	case errors.Is(err, ErrCommunicationChannelSnapshotChanged):
		return http.StatusConflict, "channel_snapshot_changed", VerdictBroken, true
	case errors.Is(err, ErrInvalidCommunicationModel),
		errors.Is(err, ErrInvalidCommunicationTransition),
		errors.Is(err, errCommunicationCursorTokenInvalid):
		return http.StatusBadRequest, "invalid_request", VerdictBroken, true
	case errors.Is(err, ErrCommunicationEvidenceUnknown):
		return http.StatusServiceUnavailable, "evidence_unavailable", VerdictUnknown, true
	default:
		return 0, "", "", false
	}
}
