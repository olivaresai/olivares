// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// THE WORK VOCABULARY IS NOT FLATTENED, AND THAT IS THE POINT OF THIS FILE.
//
// writeWorkError answers with a verdict and a stable code alongside the status,
// which is why this module has classifyWorkStoreError instead of a copy of
// writeStoreError. Centralized the STATUS for sentinels that classifier does
// not name — and nothing else. These two cells are the two halves of that claim,
// and the second is the one that would catch an over-eager follow-up: a change
// that gave these four a work code of their own would go red here, on purpose,
// because the console switches on that code (web/src/features/work/work-section.tsx:55).
func TestWorkErrorAnswersAKnownRefusalAsBrokenNotUnknown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"tenant suspended", store.ErrTenantSuspended, http.StatusLocked, "forbidden"},
		{"tenant not in service", store.ErrTenantNotInService, http.StatusLocked, "forbidden"},
		{"residency violation", store.ErrResidencyViolation, http.StatusForbidden, "forbidden"},
		{"cursor with sort", store.ErrCursorWithSort, http.StatusBadRequest, "invalid_cursor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeWorkError(rec, fmt.Errorf("work: %w", tc.err))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (this arm answered 500 before the shared mapping); body = %s",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
			// THE HALF THAT MATTERS MORE THAN THE STATUS. These are KNOWN refusals, so
			// the verdict must be BROKEN: VerdictUnknown means the observation could
			// not be completed, and the console picks its "could not look" screen off
			// exactly that verdict (web/src/features/work/api.ts:97-108). Answering a
			// withdrawn tenant "I could not look" is false — the engine looked.
			//
			// The codes are the work vocabulary's own, not new ones: "forbidden" and
			// "invalid_cursor" are already answered elsewhere in K1. A cell that let
			// internal_error through here would be locking in the incoherence the
			// external contrast caught.
			code, verdict := workEnvelope(t, rec)
			if code != tc.wantCode || verdict != string(VerdictBroken) {
				t.Fatalf("code/verdict = %q/%q, want %q/%q — a known refusal is BROKEN, never UNKNOWN",
					code, verdict, tc.wantCode, VerdictBroken)
			}
		})
	}
}

// The classifier still owns everything it DOES name: same status, same code, same
// verdict as before. Without this cell the one above could be satisfied by a change
// that routed every work error through the shared mapping and threw the work
// vocabulary away.
func TestWorkErrorClassifierStillOwnsWhatItNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound, "not_found"},
		{"conflict is a STATE conflict here", store.ErrConflict, http.StatusConflict, "state_conflict"},
		{"workspace confined", store.ErrWorkspaceConfinement, http.StatusForbidden, "workspace_confined"},
		{"audit spool full is evidence, not a spool", store.ErrAuditSpoolFull, http.StatusServiceUnavailable, "evidence_unavailable"},
		{"not leader is an observation gap", store.ErrNotLeader, http.StatusServiceUnavailable, "observation_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeWorkError(rec, fmt.Errorf("work: %w", tc.err))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code, _ := workEnvelope(t, rec); code != tc.wantCode {
				t.Fatalf("code = %q, want %q — the work vocabulary must survive the shared mapping", code, tc.wantCode)
			}
		})
	}
}

func workEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (code, verdict string) {
	t.Helper()
	var env struct {
		Verdict string `json:"verdict"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q is not the work error envelope: %v", rec.Body.String(), err)
	}
	return env.Code, env.Verdict
}
