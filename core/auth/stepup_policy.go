// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Administrative step-up policy. Every action that used to demand a fresh
// hardware ceremony (AAL3) asks StepUpSatisfied instead, and the deployment
// administrator decides what that means:
//
//   - StepUpNone (the default): the administrator acts at the strength they
//     signed in with (password, plus TOTP when enrolled). Authentication, RBAC,
//     tenant isolation and the audit ledger are unchanged; only the extra
//     ceremony is gone.
//   - StepUpTOTP: the session must have been signed in with a TOTP code, or
//     carry a fresh passkey/PIV step-up.
//   - StepUpPasskey: the session must carry a fresh passkey/PIV step-up (AAL3),
//     the behavior before this policy existed.
//
// API tokens never satisfy it: a token carries no human assurance, so every
// route that refused a token still refuses it under every policy.
const (
	StepUpNone    = "none"
	StepUpTOTP    = "totp"
	StepUpPasskey = "passkey"
)

// ErrStepUpPolicyInvalid names a value outside StepUpNone/TOTP/Passkey.
var ErrStepUpPolicyInvalid = errors.New("auth: admin_step_up must be none, totp or passkey")

// ErrStepUpPasskeyUnproven refuses turning on the passkey policy from a session
// that has not just used a passkey: the console must prove a passkey works at
// this address before the policy can demand one (no lockout).
var ErrStepUpPasskeyUnproven = errors.New("auth: sign in with a passkey on this address before requiring passkeys")

// ErrStepUpPasskeyMissing refuses the passkey policy when the caller has no
// passkey enrolled.
var ErrStepUpPasskeyMissing = errors.New("auth: add a passkey to your account before requiring passkeys")

// ErrStepUpTOTPUnproven refuses the TOTP policy from a session that was not
// signed in with a TOTP code (no lockout: the caller proves the factor works).
var ErrStepUpTOTPUnproven = errors.New("auth: sign in with your authenticator code before requiring it")

// stepUpError is ErrStepUpRequired with the remedy the deployment's policy asks
// for; errors.Is(err, ErrStepUpRequired) holds, so it maps to step_up_required.
type stepUpError struct{ msg string }

func (e *stepUpError) Error() string        { return e.msg }
func (e *stepUpError) Is(target error) bool { return target == ErrStepUpRequired }

// StepUpRequiredFor is the step-up refusal for policy: an authenticator code for
// totp, a passkey or PIV step-up otherwise.
func StepUpRequiredFor(policy string) error {
	if policy == StepUpTOTP {
		return &stepUpError{"auth: step-up required: this deployment asks for an authenticator code before administrative changes; sign in again with your code"}
	}
	return ErrStepUpRequired
}

// ValidStepUpPolicy reports whether policy is one of the three known values.
func ValidStepUpPolicy(policy string) bool {
	return policy == StepUpNone || policy == StepUpTOTP || policy == StepUpPasskey
}

// StepUpSource answers the deployment's administrative step-up policy.
type StepUpSource func(context.Context) (string, error)

type stepUpSourceKey struct{}

// WithStepUpSource returns ctx carrying src. The answer is memoized for the
// request and re-read once it is older than the cache TTL, so a request that
// runs longer than the TTL sees a policy change before its effect commits. A
// re-read waits at most stepUpReadTimeout and fails strict: a gate evaluated
// inside a store transaction never hangs on it. The authenticate middleware
// attaches the engine's policy to every request; a context without one fails
// closed (StepUpSatisfied demands AAL3).
func WithStepUpSource(ctx context.Context, src StepUpSource) context.Context {
	var (
		mu     sync.Mutex
		read   bool
		at     time.Time
		policy string
		err    error
	)
	memo := StepUpSource(func(c context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if !read || time.Since(at) >= stepUpCacheTTL {
			rc, cancel := context.WithTimeout(c, stepUpReadTimeout)
			policy, err = src(rc)
			cancel()
			read, at = true, time.Now()
		}
		return policy, err
	})
	return context.WithValue(ctx, stepUpSourceKey{}, memo)
}

// StepUpPolicyFrom reads the policy attached to ctx. Without a source, or when
// it cannot be read, it reports StepUpPasskey: the strict answer.
func StepUpPolicyFrom(ctx context.Context) string {
	src, _ := ctx.Value(stepUpSourceKey{}).(StepUpSource)
	if src == nil {
		return StepUpPasskey
	}
	policy, err := src(ctx)
	if err != nil || !ValidStepUpPolicy(policy) {
		return StepUpPasskey
	}
	return policy
}

// StepUpSatisfied is the one predicate behind every administrative step-up
// gate (core handlers, sealed route floors, core/auth credential changes and
// module gates). A token or delegated handle (AAL 0) never satisfies it.
func StepUpSatisfied(ctx context.Context, p Principal) bool {
	if p.Kind != KindUser || p.AAL < AAL1 {
		return false
	}
	if p.AAL >= AAL3 {
		return true
	}
	switch StepUpPolicyFrom(ctx) {
	case StepUpNone:
		return true
	case StepUpTOTP:
		return slices.Contains(p.AMR, "totp") ||
			(p.AAL == AAL2 && slices.Contains(p.AMR, "sso") && slices.Contains(p.AMR, "mfa"))
	default:
		return false
	}
}

// stepUpSatisfied is StepUpSatisfied for the authenticator's own credential
// changes. A caller whose context carries no policy (a direct service call)
// gets the stored one, never a silently strict or lenient default. Call it
// outside a store transaction: the policy read is its own view.
func (a *Authenticator) stepUpSatisfied(ctx context.Context, actor Principal) bool {
	if _, ok := ctx.Value(stepUpSourceKey{}).(StepUpSource); !ok {
		ctx = WithStepUpSource(ctx, a.CurrentStepUp)
	}
	return StepUpSatisfied(ctx, actor)
}

// stepUpCacheTTL bounds how stale another node's view of a policy change can be:
// a change takes effect within a few seconds on every node, and at once on the
// node that made it (SetAdminStepUp refreshes its cache). A variable only so
// tests can shorten it (SetStepUpCacheTTLForTest).
var stepUpCacheTTL = 5 * time.Second

// stepUpReadTimeout bounds a policy re-read made while a request is running.
const stepUpReadTimeout = 2 * time.Second

// SetStepUpCacheTTLForTest shortens the policy cache for a test and returns the
// function that restores it.
func SetStepUpCacheTTLForTest(d time.Duration) (restore func()) {
	old := stepUpCacheTTL
	stepUpCacheTTL = d
	return func() { stepUpCacheTTL = old }
}

// stepUpRank orders the policies from least to most demanding.
func stepUpRank(policy string) int {
	switch policy {
	case StepUpTOTP:
		return 1
	case StepUpPasskey:
		return 2
	}
	return 0
}

type stepUpCache struct {
	mu     sync.Mutex
	policy string
	at     time.Time
}

// CurrentStepUp answers the policy for a request: from a cache at most
// stepUpCacheTTL old, read from the store otherwise. Callers resolve it before
// opening any store transaction (the authenticate middleware does), because the
// read is its own view and SQLite serializes it behind an open writer.
func (a *Authenticator) CurrentStepUp(ctx context.Context) (string, error) {
	a.stepUp.mu.Lock()
	defer a.stepUp.mu.Unlock()
	if a.stepUp.policy != "" && time.Since(a.stepUp.at) < stepUpCacheTTL {
		return a.stepUp.policy, nil
	}
	policy, err := a.AdminStepUp(ctx)
	if err != nil {
		return "", err
	}
	a.stepUp.policy, a.stepUp.at = policy, time.Now()
	return policy, nil
}

// ReloadStepUp drops the cached policy so the next request reads the store, for
// a change written outside SetAdminStepUp (a restore, a direct store write).
func (a *Authenticator) ReloadStepUp() {
	a.stepUp.mu.Lock()
	a.stepUp.policy = ""
	a.stepUp.mu.Unlock()
}

// AdminStepUp reads the deployment's policy. An absent row, or a row that never
// set it, is StepUpNone.
func (a *Authenticator) AdminStepUp(ctx context.Context) (string, error) {
	policy := StepUpNone
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.AuthPolicy().List(ctx, model.Query{Filters: []model.Filter{}})
		if err != nil {
			return err
		}
		if len(rows) > 0 && ValidStepUpPolicy(rows[0].AdminStepUp) {
			policy = rows[0].AdminStepUp
		}
		return nil
	})
	return policy, err
}

// SetAdminStepUp changes the deployment's policy and ledgers the change. Turning
// it off, or lowering it, always works at the caller's current strength.
// Raising it refuses unless the calling session already meets the new level, so
// an administrator cannot lock themselves out: TOTP needs this session to have
// been signed in with a TOTP code, and a passkey needs an enrolled passkey and a
// fresh passkey/PIV step-up on this session (which also proves this console
// address can use passkeys). Raise or lower is decided against the stored level
// inside the write, so a concurrent change cannot turn a raise into a "lower".
func (a *Authenticator) SetAdminStepUp(ctx context.Context, actor Principal, policy string) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() || actor.UserID.IsZero() {
		return ErrUnauthenticated
	}
	if !ValidStepUpPolicy(policy) {
		return ErrStepUpPolicyInvalid
	}
	// The passkey enrolment read is its own view, so it is taken before the write.
	enrolled := false
	if policy == StepUpPasskey && actor.AAL < AAL3 {
		var err error
		if enrolled, err = a.hasPasskey(ctx, actor.UserID); err != nil {
			return err
		}
	}
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.AuthPolicy().List(ctx, model.Query{Filters: []model.Filter{}})
		if err != nil {
			return err
		}
		previous := StepUpNone
		if len(rows) > 0 && ValidStepUpPolicy(rows[0].AdminStepUp) {
			previous = rows[0].AdminStepUp
		}
		if stepUpRank(policy) > stepUpRank(previous) {
			if policy == StepUpTOTP {
				factors, _, err := as.TOTPCredentials().List(ctx, model.Query{
					Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: actor.UserID.String()}},
				})
				if err != nil {
					return err
				}
				// The factor this session was signed in with must still be enrolled:
				// a removed factor proves nothing about the next sign-in.
				if len(factors) == 0 {
					return ErrStepUpTOTPUnproven
				}
			}
			switch {
			case policy == StepUpTOTP && !slices.Contains(actor.AMR, "totp"):
				return ErrStepUpTOTPUnproven
			case policy == StepUpPasskey && actor.AAL < AAL3 && !enrolled:
				return ErrStepUpPasskeyMissing
			case policy == StepUpPasskey && actor.AAL < AAL3:
				return ErrStepUpPasskeyUnproven
			}
		}
		if len(rows) == 0 {
			if _, err := as.AuthPolicy().Create(ctx, model.AuthPolicy{AdminStepUp: policy}); err != nil {
				return err
			}
		} else {
			row := rows[0]
			row.AdminStepUp = policy
			if _, err := as.AuthPolicy().Update(ctx, row); err != nil {
				return err
			}
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.stepup.policy", TargetKind: "core.auth_policy",
			Meta: map[string]any{"admin_step_up": policy, "previous": previous},
		})
		return err
	})
	if err == nil {
		a.stepUp.mu.Lock()
		a.stepUp.policy, a.stepUp.at = policy, time.Now()
		a.stepUp.mu.Unlock()
	}
	return err
}

// hasPasskey reports whether the user holds at least one WebAuthn credential.
func (a *Authenticator) hasPasskey(ctx context.Context, userID model.ID) (bool, error) {
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.WebAuthnCredentials().List(ctx, byEq("user_id", userID.String(), 1))
		if err != nil {
			return err
		}
		found = len(rows) > 0
		return nil
	})
	return found, err
}
