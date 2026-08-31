// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"net/http"
)

// writeCommunicationError is a pre-activation handler seam; it does not mount a
// route or make the communication kernel ready. It names only communication
// decisions whose wire meaning differs from the shared store mapper. In
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
	case errors.Is(err, ErrCommunicationEvidenceUnknown):
		return http.StatusServiceUnavailable, "evidence_unavailable", VerdictUnknown, true
	default:
		return 0, "", "", false
	}
}
