// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// THE LOCAL ARM MUST STILL WIN. Centralizing the mapping is not license to
// flatten a module that answers differently on purpose: this module reads
// ErrUnknownEntity as "the adoption read model is not registered on this
// deployment" and answers 503, where the shared mapping answers 400
// "invalid query". Delegation put the shared call in the DEFAULT arm precisely so
// the local one is consulted first, and this cell is what proves the order.
func TestAdoptionKeepsItsOwnUnknownEntityAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStoreError(rec, fmt.Errorf("ext: %w", store.ErrUnknownEntity))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}
	if got := adoptionMessage(t, rec); got != "adoption store not ready" {
		t.Fatalf("message = %q, want %q", got, "adoption store not ready")
	}
}

// …AND EVERYTHING ELSE NOW COMES FROM THE SHARED MAPPING. Before 2026-08-12 this
// module carried three arms and answered 500 for the rest.
//
// FOUR OF THE ROWS BELOW ARE REACHABLE FROM ITS FIVE GET ROUTES, and by a mechanism
// worth naming because it is the one a per-module sweep misses: they are injected by
// the STORE DECORATORS, not produced by anything in this package. suspension.Guard
// and residency.Guard wrap the very store mc.Data reaches and run on View as well as
// Mutate, so a read-only module with no writes at all still receives them.
//
// store.ErrNotFound is the sharpest of the four and the one this session first got
// wrong. Measuring inside the module said "unreachable": five GET routes, no
// repo.Get, no findOne, so nothing here can produce a not-found. But
// core/suspension/store.go:262 returns store.ErrNotFound WRAPPED for a tenant whose
// org row is absent — a typo'd id, or one hard-deleted after its grace period — on
// every View. So a request naming a tenant that is not there was answered 500
// "internal error" by this module, which is exactly what the brief that opened
// said and what this session briefly refuted. The producer was one decorator out.
func TestAdoptionDelegatesEverythingElse(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"not found, as the suspension guard wraps it for an absent org", store.ErrNotFound, http.StatusNotFound, "not found"},
		{"tenant suspended", store.ErrTenantSuspended, http.StatusLocked, "tenant suspended"},
		{"tenant not in service", store.ErrTenantNotInService, http.StatusLocked, "tenant not in service"},
		{"residency violation", store.ErrResidencyViolation, http.StatusForbidden, "residency violation"},
		{"workspace confined", store.ErrWorkspaceConfinement, http.StatusForbidden, "workspace confined"},
		{"audit spool full", store.ErrAuditSpoolFull, http.StatusServiceUnavailable, "audit spool full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeStoreError(rec, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := adoptionMessage(t, rec); got != tc.wantMsg {
				t.Fatalf("message = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

// The non-firing direction: a real fault is still a fault, and nil is still a 200.
func TestAdoptionStillCallsARealFaultAFault(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStoreError(rec, fmt.Errorf("the disk caught fire"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	rec = httptest.NewRecorder()
	writeStoreError(rec, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("nil answered %d with body %q, want 200 and an empty body", rec.Code, rec.Body.String())
	}
}

func adoptionMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
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
