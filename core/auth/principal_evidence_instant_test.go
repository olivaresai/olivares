// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// A login and the next request can fall in the same millisecond. SQLite's
// transaction clock reads milliseconds, so the session's creation instant can
// read later than the truncated transaction time. That is not a future-dated
// session; an instant a full clock step later is.
func TestSessionInstantWithinTheTransactionClockPrecisionIsEvidence(t *testing.T) {
	now := model.NewTimestamp(time.Date(2026, 9, 29, 20, 6, 27, 123_000_000, time.UTC))
	deadline := now.Time().Add(time.Minute)
	for _, tc := range []struct {
		name  string
		after time.Duration
		ok    bool
	}{
		{"earlier", -time.Second, true},
		{"same millisecond", 999 * time.Microsecond, true},
		{"next millisecond", time.Millisecond, false},
		{"future", time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := principalEvidenceMaterial{session: &model.AuthSession{AAL: AAL1, AMR: []string{"pwd"}}}
			m.session.CreatedAt = model.NewTimestamp(now.Time().Add(tc.after))
			_, _, _, err := m.finalize(now, deadline)
			if tc.ok && err != nil {
				t.Fatalf("finalize: %v", err)
			}
			if !tc.ok && (err == nil || errors.Is(err, ErrUnauthenticated)) {
				t.Fatalf("finalize accepted or misclassified a future instant: %v", err)
			}
		})
	}
}
