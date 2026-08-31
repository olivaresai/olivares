// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/store"
)

// ONE CELL PER SENTINEL, so a mutant that drops a single arm kills exactly one.
// Grouping them in a loop over a shared assertion would let one deleted map entry
// be masked by the others passing.
//
// The four marked NEW ON 2026-08-12 are the ones this function was written for:
// core/api had mapped them for a long time and at most two of the thirty-six
// module copies carried them, so the same refusal answered 423/503/403 on a core
// route and 500 "internal error" on every module route.
func TestStoreErrorStatusMapsTheCanonicalSet(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound, "not found"},
		{"conflict", store.ErrConflict, http.StatusConflict, "conflict"},
		{"cursor with sort", store.ErrCursorWithSort, http.StatusBadRequest, "invalid query"},
		{"unknown entity", store.ErrUnknownEntity, http.StatusBadRequest, "invalid query"},
		{"audit spool full", store.ErrAuditSpoolFull, http.StatusServiceUnavailable, "audit spool full"},
		{"workspace confinement", store.ErrWorkspaceConfinement, http.StatusForbidden, "workspace confined"},
		{"workspace lineage required", store.ErrWorkspaceLineageRequired, http.StatusForbidden, "workspace confined"},

		// NEW ON 2026-08-12 — the drift this function exists to end.
		{"not leader", store.ErrNotLeader, http.StatusServiceUnavailable, "not leader"},
		{"residency violation", store.ErrResidencyViolation, http.StatusForbidden, "residency violation"},
		{"tenant suspended", store.ErrTenantSuspended, http.StatusLocked, "tenant suspended"},
		{"tenant not in service", store.ErrTenantNotInService, http.StatusLocked, "tenant not in service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg, ok := StoreErrorStatus(tc.err)
			if !ok {
				t.Fatalf("StoreErrorStatus(%v) reported ok=false; the family must answer this sentinel", tc.err)
			}
			if status != tc.wantStatus || msg != tc.wantMsg {
				t.Fatalf("StoreErrorStatus(%v) = (%d, %q), want (%d, %q)", tc.err, status, msg, tc.wantStatus, tc.wantMsg)
			}
			// A sentinel arrives wrapped far more often than bare: the store wraps on
			// the way out (generic.go mapWriteErr) and handlers wrap for context. An
			// arm keyed on == instead of errors.Is passes the row above and fails here.
			wrapped := fmt.Errorf("handler: %w", tc.err)
			wstatus, wmsg, wok := StoreErrorStatus(wrapped)
			if !wok || wstatus != tc.wantStatus || wmsg != tc.wantMsg {
				t.Fatalf("wrapped %v = (%d, %q, %v), want (%d, %q, true)", tc.err, wstatus, wmsg, wok, tc.wantStatus, tc.wantMsg)
			}
		})
	}
}

// THE NON-FIRING DIRECTION. A mapper that answered something for everything would
// pass every row above; these rows are what make those rows mean anything.
func TestStoreErrorStatusRefusesToAnswerForWhatItDoesNotMap(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a genuine internal fault", fmt.Errorf("the disk caught fire")},
		// Internal invariant violations. statusFor answers 500 "internal" for these on
		// purpose — they are never the client's fault — and this function must not
		// dress them as something the caller can act on.
		{"no tenant bound", store.ErrNoTenant},
		{"scope is read-only", store.ErrReadOnly},
		{"append-only table", store.ErrAppendOnly},
		{"tenant scope violation", store.ErrTenantViolation},
		// NOT A WIDENING. statusFor folds auth.ErrInvalidRole into the bad_request
		// code, which the module family words as "invalid query". Answering that for a
		// bad role would be both a status change nobody asked for and a false sentence,
		// which is why moduleErrorMessage deliberately has no bad_request entry.
		{"an invalid role is not a bad query", auth.ErrInvalidRole},
		{"a step-up demand is not the module family's to answer", auth.ErrStepUpRequired},
		// nil is a caller bug, and it fails closed rather than manufacturing a 200.
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg, ok := StoreErrorStatus(tc.err)
			if ok {
				t.Fatalf("StoreErrorStatus(%v) claimed the family answers it: (%d, %q)", tc.err, status, msg)
			}
			if status != http.StatusInternalServerError || msg != "internal error" {
				t.Fatalf("StoreErrorStatus(%v) fallback = (%d, %q), want (500, %q)",
					tc.err, status, msg, "internal error")
			}
		})
	}
}

// THE STATUS IS STATUSFOR'S, NOT A SECOND COPY OF IT. This is the property that
// makes the whole design work: a sentinel added to statusFor reaches all thirty-six
// module mappers without anyone editing them. A future refactor that inlines a
// status here — the obvious "simplification" — makes this red.
func TestStoreErrorStatusNeverDisagreesWithStatusFor(t *testing.T) {
	for _, err := range []error{
		store.ErrNotFound, store.ErrConflict, store.ErrAuditSpoolFull,
		store.ErrWorkspaceConfinement, store.ErrWorkspaceLineageRequired,
		store.ErrNotLeader, store.ErrResidencyViolation,
		store.ErrTenantSuspended, store.ErrTenantNotInService,
		license.ErrAddonRequiresLicense,
	} {
		want, _ := statusFor(err)
		got, _, ok := StoreErrorStatus(err)
		if !ok {
			t.Fatalf("StoreErrorStatus(%v) reported ok=false", err)
		}
		if got != want {
			t.Errorf("StoreErrorStatus(%v) = %d but statusFor says %d — core and the modules now disagree", err, got, want)
		}
	}
}

// THE LICENSE CASE, DRIVEN DIRECTLY. It cannot be reached end to end in this build:
// license.AddonRequired (core/license/entitlement.go:61) has no caller outside its
// own file and tests, because the addonGate that constructs it lives in the closed
// enterprise overlay. So the mapper is exercised with the constructor, and the
// declaration is part of the test rather than a comment somewhere else.
func TestStoreErrorStatusAnswersTheAddonRefusalTheOverlayProduces(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantSubstr string
	}{
		{"names the add-on and the operation", license.AddonRequired("compliance-packs", "compliance.depth.export"), `the "compliance-packs" add-on is required for compliance.depth.export`},
		{"add-on only", license.AddonRequired("regulated", ""), `the "regulated" add-on is required for this operation`},
		{"bare sentinel", license.ErrAddonRequiresLicense, "a commercial add-on is required for this operation"},
		{"wrapped by a handler", fmt.Errorf("depth pack: %w", license.AddonRequired("identity-scale", "op")), `the "identity-scale" add-on is required for op`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg, ok := StoreErrorStatus(tc.err)
			if !ok || status != http.StatusForbidden {
				t.Fatalf("StoreErrorStatus(%v) = (%d, %q, %v), want (403, …, true)", tc.err, status, msg, ok)
			}
			if msg[:len(tc.wantSubstr)] != tc.wantSubstr {
				t.Fatalf("message %q does not start with %q", msg, tc.wantSubstr)
			}
			// The reassurance is the half an operator acts on: it separates "your
			// license lapsed" from "you may not read your own data".
			const unaffected = "; reading, verifying and exporting your data are unaffected"
			if msg[len(msg)-len(unaffected):] != unaffected {
				t.Fatalf("message %q does not end with the unaffected-reads clause", msg)
			}
		})
	}
}
