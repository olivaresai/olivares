// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestWriteCommunicationErrorMapsTypedPlanChangeOnlyToPreconditionFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantVerdict AssessmentVerdict
	}{
		{
			name: "wrapped semantic plan change",
			err: fmt.Errorf("locked apply: %w", communicationError(
				ErrCommunicationPlanChanged, "stale expected plan",
			)),
			wantStatus: http.StatusPreconditionFailed, wantCode: "plan_changed",
			wantVerdict: VerdictBroken,
		},
		{
			name:       "handoff offered by a sender that is not the owner",
			err:        fmt.Errorf("offer: %w", errHandoffSenderNotOwner),
			wantStatus: http.StatusForbidden, wantCode: "not_owner",
			wantVerdict: VerdictBroken,
		},
		{
			name:       "unknown evidence remains retryable",
			err:        fmt.Errorf("publish: %w", ErrCommunicationEvidenceUnknown),
			wantStatus: http.StatusServiceUnavailable, wantCode: "evidence_unavailable",
			wantVerdict: VerdictUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeCommunicationError(recorder, test.err)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s",
					recorder.Code, test.wantStatus, recorder.Body.String())
			}
			var body struct {
				Verdict AssessmentVerdict `json:"verdict"`
				Code    string            `json:"code"`
				Error   struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != test.wantCode || body.Error.Code != test.wantCode ||
				body.Verdict != test.wantVerdict {
				t.Fatalf("body = %#v, want code=%q verdict=%q",
					body, test.wantCode, test.wantVerdict)
			}
		})
	}
}

func TestWriteCommunicationErrorKeepsIdempotencyConflictAtConflict(t *testing.T) {
	t.Parallel()

	idempotencyRebind := fmt.Errorf("%w: idempotency_key_reused", store.ErrConflict)
	if errors.Is(idempotencyRebind, ErrCommunicationPlanChanged) {
		t.Fatal("idempotency conflict aliases the semantic plan sentinel")
	}
	recorder := httptest.NewRecorder()
	writeCommunicationError(recorder, idempotencyRebind)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status = %d, want 409: %s",
			recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "plan_changed") {
		t.Fatalf("idempotency conflict was mislabeled as plan_changed: %s", recorder.Body.String())
	}
}

// The owner refusal and the stale offer leave one if-statement apart, and only
// the first is permanent: the owner refusal is 403 not_owner and says the rule,
// every other stale-offer cause stays the 409 store conflict.
func TestWriteCommunicationErrorSplitsHandoffOwnerRefusalFromStaleOffer(t *testing.T) {
	t.Parallel()

	stale := httptest.NewRecorder()
	writeCommunicationError(stale, fmt.Errorf("offer: %w", errHandoffStaleOffer))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"code":"conflict"`) {
		t.Fatalf("stale offer = %d %s, want 409 conflict", stale.Code, stale.Body.String())
	}

	refused := httptest.NewRecorder()
	writeCommunicationError(refused, fmt.Errorf("offer: %w", errHandoffSenderNotOwner))
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode owner refusal: %v", err)
	}
	const rule = "Only the current owner of this work item can offer it for handoff."
	if refused.Code != http.StatusForbidden || body.Error.Code != "not_owner" || body.Error.Message != rule {
		t.Fatalf("owner refusal = %d %+v, want 403 not_owner %q", refused.Code, body, rule)
	}
}
