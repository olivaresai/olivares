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

func TestResolvePrincipalScopeSealsAuthenticationWithinTransactionClockPrecision(t *testing.T) {
	for _, clock := range []struct {
		name      string
		precision time.Duration
	}{
		{"sqlite_milliseconds", time.Millisecond},
		{"postgres_microseconds", time.Microsecond},
	} {
		t.Run(clock.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := f.sessionRef()
			now := f.now.Truncate(clock.precision)
			f.hooks.now = model.NewTimestamp(now)
			for _, tc := range []struct {
				name  string
				after time.Duration
				ok    bool
			}{
				{"same_clock_step", clock.precision - time.Nanosecond, true},
				// Both readers use the existing coarsest clock ceiling: a full
				// millisecond ahead is refused even for the finer PG witness.
				{"coarsest_clock_boundary", time.Millisecond, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					instant := now.Add(tc.after)
					f.hooks.rewriteSession = func(session model.AuthSession) model.AuthSession {
						session.CreatedAt = model.NewTimestamp(instant)
						return session
					}
					p, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant)
					if !tc.ok {
						if !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
							t.Fatalf("future authentication instant: %v, want unavailable evidence", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("resolve same-clock-step authentication: %v", err)
					}
					evidence, ok := p.AuthenticationEvidence()
					if !ok || !evidence.AuthenticatedAt.Equal(instant) {
						t.Fatalf("sealed authentication instant = %v (valid=%t), want %v", evidence.AuthenticatedAt, ok, instant)
					}
					got := NewAuthorizer(nil).AuthorizeEvidence(f.ctx, principalAuthorityEvidenceRequest(p, f.tenant))
					if got.Outcome != EvidenceAllow {
						t.Fatalf("same-clock-step authority = %+v, want ALLOW", got)
					}
				})
			}
		})
	}
}
