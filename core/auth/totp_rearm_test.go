// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type totpRearmClock struct{ at time.Time }

func (c *totpRearmClock) Now() model.Timestamp { return model.NewTimestamp(c.at) }

func totpRearmFixture(t *testing.T) (*totpFixture, *totpRearmClock) {
	t.Helper()
	f := newTOTPFixture(t)
	res, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatal(err)
	}
	f.enrolFactor(res.Token)
	clock := &totpRearmClock{at: time.Now()}
	f.a = auth.NewAuthenticator(f.st, clock).WithTOTPSeedSealer(f.sealer)
	return f, clock
}

func TestTOTPContinuationRearmKeepsOriginalExpiry(t *testing.T) {
	for _, horizon := range []struct {
		name string
		ttl  time.Duration
	}{
		{"proof-horizon", time.Minute},
		{"pending-ttl", 2 * auth.TOTPPendingTTL},
	} {
		for _, locked := range []bool{false, true} {
			reason := "wrong-code"
			if locked {
				reason = "lockout"
			}
			t.Run(horizon.name+"/"+reason, func(t *testing.T) {
				f, clock := totpRearmFixture(t)
				initial := clock.at
				cont := &fakeContinuation{scope: f.tenantFor(t, "rearm"), horizon: initial.Add(horizon.ttl)}
				out, err := f.a.CompleteExternalLogin(f.ctx, f.user, "127.0.0.1", cont, "", nil)
				if err != nil || !out.RequiresMFA() {
					t.Fatalf("external challenge: requires MFA=%v, err=%v", out.RequiresMFA(), err)
				}
				clock.at = initial.Add(10 * time.Second)
				failures := 1
				if locked {
					failures = 5
				}
				for i := 0; i < failures; i++ {
					if token, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, "not-a-code", "127.0.0.1", nil); token != "" || !errors.Is(err, auth.ErrTOTPVerification) {
						t.Fatalf("wrong code %d: issued=%v, err=%v", i, token != "", err)
					}
				}
				if locked {
					if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, "not-a-code", "127.0.0.1", nil); !errors.Is(err, auth.ErrLockedOut) {
						t.Fatalf("lockout: err=%v", err)
					}
				}
				originalTTL := min(horizon.ttl, auth.TOTPPendingTTL)
				clock.at = initial.Add(originalTTL + time.Second)
				// Enrolment uses the same pending credential without spending a code
				// or waiting for the account's throttle. Expiry must refuse it too.
				if _, err := f.a.BeginTOTPEnrolmentForLogin(f.ctx, out.MFAToken, "test"); !errors.Is(err, auth.ErrTOTPVerification) {
					t.Fatalf("rearmed external challenge survives its original expiry: %v", err)
				}
			})
		}
	}
}

func TestTOTPContinuationRearmPreservesLocalSlidingExpiry(t *testing.T) {
	f, clock := totpRearmFixture(t)
	initial := clock.at
	out, err := f.loginAs(f.user.Email)
	if err != nil || !out.RequiresMFA() {
		t.Fatalf("local challenge: requires MFA=%v, err=%v", out.RequiresMFA(), err)
	}
	clock.at = initial.Add(time.Minute)
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, out.MFAToken, "not-a-code", "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("wrong code: %v", err)
	}
	clock.at = initial.Add(auth.TOTPPendingTTL + time.Second)
	if _, err := f.a.BeginTOTPEnrolmentForLogin(f.ctx, out.MFAToken, "test"); err != nil {
		t.Fatalf("local challenge no longer has its existing sliding expiry: %v", err)
	}
}
