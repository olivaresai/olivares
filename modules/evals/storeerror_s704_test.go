// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/store"
)

// evals is the plain shape of the mapper family: no local arms, everything
// delegated. A green in core/api proves the mapping; this proves a module actually
// REACHES it, which is a different claim and the one the delegation is for.
//
// It is deliberately this module. The census that opened used
// evals/writeStoreError as its named NEGATIVE control — the copy that did NOT
// carry the license arm — so if any probe or fix was going to be fooled about a
// module, it was this one.
func TestEvalsMapperAnswersTheWholeCanonicalSet(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound, "not found"},
		{"conflict", store.ErrConflict, http.StatusConflict, "conflict"},
		{"invalid query", store.ErrCursorWithSort, http.StatusBadRequest, "invalid query"},
		{"audit spool full", store.ErrAuditSpoolFull, http.StatusServiceUnavailable, "audit spool full"},
		{"workspace confined", store.ErrWorkspaceConfinement, http.StatusForbidden, "workspace confined"},

		// THE FOUR THIS MODULE ANSWERED 500 FOR UNTIL 2026-08-12. Each is reachable
		// from an ordinary route: the suspension guard decorates the store this module
		// reaches through mc.Data and checks View as well as Mutate, the residency
		// guard the same on a region-scoped instance, and sqlStore.Mutate answers
		// ErrNotLeader on any standby.
		{"tenant suspended", store.ErrTenantSuspended, http.StatusLocked, "tenant suspended"},
		{"tenant not in service", store.ErrTenantNotInService, http.StatusLocked, "tenant not in service"},
		{"not leader", store.ErrNotLeader, http.StatusServiceUnavailable, "not leader"},
		{"residency violation", store.ErrResidencyViolation, http.StatusForbidden, "residency violation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeStoreError(rec, fmt.Errorf("read: %w", tc.err))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := messageOf(t, rec); got != tc.wantMsg {
				t.Fatalf("message = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

// The license refusal, on a module that never had the arm. It cannot be produced
// end to end in this build — nothing open constructs it (core/license/entitlement.go:61
// has no caller outside its own file and tests; the addonGate lives in the closed
// enterprise overlay) — so it is driven with the constructor, and THAT is the
// declaration, not a sentence in a commit message.
func TestEvalsMapperAnswersTheAddonRefusalItNeverCarried(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStoreError(rec, license.AddonRequired("evals-pro", "evals.ab.run"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	msg := messageOf(t, rec)
	for _, want := range []string{"evals-pro", "evals.ab.run", "exporting your data are unaffected"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// THE NON-FIRING DIRECTION, on the module and not only on the shared function. A
// mapper that had been widened to answer everything would pass every row above.
func TestEvalsMapperStillCallsARealFaultAFault(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStoreError(rec, fmt.Errorf("the disk caught fire"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := messageOf(t, rec); got != "internal error" {
		t.Fatalf("message = %q, want %q", got, "internal error")
	}
	// And nil still answers 200 with no body: that arm stayed in the module.
	rec = httptest.NewRecorder()
	writeStoreError(rec, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("nil answered %d with body %q, want 200 and an empty body", rec.Code, rec.Body.String())
	}
}

func messageOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q is not the module error envelope: %v", rec.Body.String(), err)
	}
	return env.Error.Message
}
