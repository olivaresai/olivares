// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// commitOutcomeFakeModuleData is the data seam the communication kernel receives,
// returning one prepared error without invoking the callback.
type commitOutcomeFakeModuleData struct{ err error }

func (f commitOutcomeFakeModuleData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return f.err
}

func (f commitOutcomeFakeModuleData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return f.err
}

// TestCommunicationDataPreservesCommitOutcome is HS-W for the communication
// kernel's own data seam, the last wrapper before the module decides a status.
//
// Class: P-GREEN at every cut. Its mutant is a wrapper that re-wraps with %v.
func TestCommunicationDataPreservesCommitOutcome(t *testing.T) {
	t.Parallel()

	cause := errors.New("commit acknowledgement never read")
	tenant := model.NewTenantID()
	data := tenantCommunicationData{
		data:   commitOutcomeFakeModuleData{err: fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, cause)},
		tenant: tenant,
	}

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "Mutate", run: func() error {
			return data.Mutate(context.Background(), func(store.Scope) error { return nil })
		}},
		{name: "View", run: func() error {
			return data.View(context.Background(), func(store.Scope) error { return nil })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.run()
			if !errors.Is(got, store.ErrCommitOutcomeUnknown) {
				t.Errorf("the communication data seam dropped the commit-outcome sentinel: %v", got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("the communication data seam dropped the original cause: %v", got)
			}
		})
	}

	// The existing deny-closed answer for an absent data handle is untouched: a
	// missing store is an availability refusal, not an undetermined commit.
	t.Run("absent_data_is_still_unavailable", func(t *testing.T) {
		t.Parallel()
		absent := tenantCommunicationData{tenant: tenant}
		err := absent.Mutate(context.Background(), func(store.Scope) error { return nil })
		if !errors.Is(err, store.ErrStoreUnavailable) {
			t.Errorf("an absent data handle answered %v, want store.ErrStoreUnavailable", err)
		}
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			t.Error("a unit of work that never started was reported as an undetermined commit")
		}
	})
}

// TestCommunicationHTTPDispositionCommitOutcome is HD-1: the wire meaning of an
// undetermined commit on the communication family.
//
// 503 with the existing UNKNOWN verdict is the whole answer. It is not 500,
// because 500 says "this request failed" to a caller whose write may be durable;
// and it is not a new verdict literal, because the vocabulary already has the
// one that means "I could not look".
//
// Class: B-RED at C0 (nothing projects the sentinel yet, so ok is false and the
// caller still gets the shared 500) and M-RED for M5.
func TestCommunicationHTTPDispositionCommitOutcome(t *testing.T) {
	t.Parallel()

	cause := errors.New("commit acknowledgement never read")
	sentinel := fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, cause)

	t.Run("sentinel", func(t *testing.T) {
		t.Parallel()
		status, code, verdict, ok := communicationHTTPDisposition(sentinel)
		if !ok {
			t.Fatal("the communication family did not claim an undetermined commit, so it falls through to the shared 500 mapper — which tells a caller the write failed when it may be durable")
		}
		if status != http.StatusServiceUnavailable || code != "commit_outcome_unknown" || verdict != VerdictUnknown {
			t.Fatalf("disposition = (%d, %q, %q), want (503, \"commit_outcome_unknown\", %q)",
				status, code, verdict, VerdictUnknown)
		}
	})

	// SYNTHETIC ARM ORDER, and labeled as such. The communication evidence
	// sentinel returns before Commit is ever reached (store.go:2169-2171), so this
	// join cannot occur in production. The row exists only to pin that the new arm
	// is FIRST: an undetermined commit must not be reported as an evidence read
	// that could not be completed, because the two have different remedies.
	t.Run("joined_sentinel_wins_synthetic_arm_order", func(t *testing.T) {
		t.Parallel()
		joined := errors.Join(sentinel, ErrCommunicationEvidenceUnknown)
		status, code, verdict, ok := communicationHTTPDisposition(joined)
		if !ok || status != http.StatusServiceUnavailable ||
			code != "commit_outcome_unknown" || verdict != VerdictUnknown {
			t.Fatalf("joined disposition = (%d, %q, %q, %t), want the commit-outcome arm first",
				status, code, verdict, ok)
		}
	})

	t.Run("existing_arms_unchanged", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name    string
			err     error
			status  int
			code    string
			verdict AssessmentVerdict
			ok      bool
		}{
			{
				name: "evidence_unavailable_alone", err: ErrCommunicationEvidenceUnknown,
				status: http.StatusServiceUnavailable, code: "evidence_unavailable",
				verdict: VerdictUnknown, ok: true,
			},
			{
				name: "not_found", err: ErrCommunicationNotFound,
				status: http.StatusNotFound, code: "not_found",
				verdict: VerdictBroken, ok: true,
			},
			{
				name: "epilogue_error", err: errors.New("lineage epilogue refused"),
				ok: false,
			},
			{
				name: "store_unavailable_without_the_sentinel",
				err:  fmt.Errorf("%w: %w", store.ErrStoreUnavailable, errors.New("dial tcp: refused")),
				ok:   false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				status, code, verdict, ok := communicationHTTPDisposition(tc.err)
				if ok != tc.ok {
					t.Fatalf("claimed = %t, want %t: this arm is not C32's to change", ok, tc.ok)
				}
				if !tc.ok {
					return
				}
				if status != tc.status || code != tc.code || verdict != tc.verdict {
					t.Fatalf("disposition = (%d, %q, %q), want (%d, %q, %q)",
						status, code, verdict, tc.status, tc.code, tc.verdict)
				}
			})
		}
	})

	// The served envelope is the one every SDK and console fixture is written
	// against, so it is asserted here rather than described: 503, the JSON
	// content type, NO Retry-After (there is nothing to advise a caller to retry),
	// and the code in both the top-level field and the nested error object.
	t.Run("served_envelope", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		writeCommunicationError(rec, sentinel)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := rec.Header().Get("Retry-After"); got != "" {
			t.Errorf("Retry-After = %q: an undetermined commit has no safe retry interval to advertise", got)
		}
		var body struct {
			Verdict string `json:"verdict"`
			Code    string `json:"code"`
			Error   struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the served body %q: %v", rec.Body.String(), err)
		}
		if body.Code != "commit_outcome_unknown" || body.Error.Code != "commit_outcome_unknown" {
			t.Errorf("body codes = %q / %q, want commit_outcome_unknown twice", body.Code, body.Error.Code)
		}
		if body.Verdict != string(VerdictUnknown) {
			t.Errorf("verdict = %q, want %q", body.Verdict, VerdictUnknown)
		}
	})
}

// TestWorkStoreErrorCommitOutcomePreserved is HP-9, and it is a PRESERVATION
// control for a seam C32 deliberately does not repair.
//
// The work plane classifies store errors on its own (classifyWorkStoreError) and
// knows nothing about the commit-outcome sentinel. That means an undetermined
// commit reached through a work route keeps today's two answers: a canceled
// commit falls to the shared 500, because context.Canceled never acquires
// ErrStoreUnavailable, while a deadline commit does acquire it and reaches the
// 503 observation_unavailable arm.
//
// That split is a known residual (E9). Pinning it here is what stops C32 from
// changing it by accident — and what makes the decision NOT to change it visible
// to whoever owns the work vocabulary.
//
// Class: P-GREEN at every cut. Its mutant is a commit-outcome arm added to
// classifyWorkStoreError.
func TestWorkStoreErrorCommitOutcomePreserved(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		verdict string
	}{
		{
			name:   "canceled_commit_stays_internal_error",
			err:    fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, context.Canceled),
			status: http.StatusInternalServerError, code: "internal_error",
			verdict: string(VerdictUnknown),
		},
		{
			name: "deadline_commit_stays_observation_unavailable",
			err: fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown,
				fmt.Errorf("%w: %w", store.ErrStoreUnavailable, context.DeadlineExceeded)),
			status: http.StatusServiceUnavailable, code: "observation_unavailable",
			verdict: string(VerdictUnknown),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			writeWorkError(rec, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: C32 does not change the work seam", rec.Code, tc.status)
			}
			var body struct {
				Verdict string `json:"verdict"`
				Code    string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode the served body %q: %v", rec.Body.String(), err)
			}
			if body.Code != tc.code || body.Verdict != tc.verdict {
				t.Fatalf("body = (%q, %q), want (%q, %q)", body.Code, body.Verdict, tc.code, tc.verdict)
			}
		})
	}
}
