// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Authenticator assurance levels. The numbers follow the
// NIST SP 800-63B-4 vocabulary the panel shares (1 = single factor, 3 =
// hardware-backed, phishing-resistant with user verification). They are TARGET
// standards: the engine claims no NIST/FIPS conformance anywhere (docs/SECURITY-HARDENING.md).
const (
	// AAL1 is the base assurance of every fresh session (password or a
	// federated login this engine cannot vouch beyond).
	AAL1 = 1
	// AAL3 is the assurance granted ONLY by a ceremony this engine verified:
	// a WebAuthn assertion with user verification, or a validated PIV/CAC
	// client certificate.
	AAL3 = 3
)

// StepUpTTL bounds how long an elevated assurance stays effective. Past it the
// session degrades back to AAL1 and the operator must re-run the ceremony — the
// 800-63B-4 AAL3 reauthentication target (15 min of inactivity); the engine
// applies it as a hard window, which is strictly tighter.
const StepUpTTL = 15 * time.Minute

// ErrStepUpRequired means the action demands a higher authenticator assurance
// than the calling session carries: the operator must complete a
// phishing-resistant step-up ceremony (WebAuthn / PIV) first. Mapped to 403
// "step_up_required" so the panel can route to the step-up flow.
var ErrStepUpRequired = errors.New("auth: step-up required: this action requires a verified hardware authenticator (AAL3)")

// effectiveAAL computes the assurance a session row carries RIGHT NOW. A legacy
// row (zero AAL) and an elevated row whose freshness window has passed both read
// as AAL1 — assurance is degraded, never inflated (fail-closed).
func effectiveAAL(s model.AuthSession, now model.Timestamp) int {
	aal := s.AAL
	if aal <= AAL1 {
		return AAL1
	}
	if s.AALExpiresAt == nil || s.AALExpiresAt.Before(now) {
		return AAL1
	}
	return aal
}

// ElevateSession raises the calling session's assurance to aal after the caller
// VERIFIED a ceremony for method ("webauthn" or "piv"). It refuses non-session
// principals and dead sessions, appends method to the session's AMR (once),
// stamps the step-up freshness window, and records the step-up on the ledger in
// the same transaction. It never lowers an assurance and never touches the
// session credential or grants.
func (a *Authenticator) ElevateSession(ctx context.Context, actor Principal, method string, aal int) (model.AuthSession, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return model.AuthSession{}, ErrUnauthenticated
	}
	now := a.clock.Now()
	var sess model.AuthSession
	if err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		s, err := as.Sessions().Get(ctx, actor.CredID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrUnauthenticated
			}
			return err
		}
		if s.Revoked || s.ExpiresAt.Before(now) {
			return ErrUnauthenticated
		}
		if aal > s.AAL {
			s.AAL = aal
		}
		if !slices.Contains(s.AMR, method) {
			s.AMR = append(s.AMR, method)
		}
		exp := model.NewTimestamp(now.Time().Add(StepUpTTL))
		s.AALExpiresAt = &exp
		updated, err := as.Sessions().Update(ctx, s)
		if err != nil {
			return err
		}
		sess = updated
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.stepup", TargetKind: "core.auth_session", TargetID: s.ID,
			Meta: map[string]any{"method": method, "aal": aal},
		})
		return err
	}); err != nil {
		return model.AuthSession{}, err
	}
	return sess, nil
}

// auditStepUpFailure records a DENIED step-up attempt on the ledger (docs/SECURITY-HARDENING.md
// self-audit). reason is a coarse, non-sensitive label ("verification",
// "clone_warning", "user_verification") — never the underlying error detail.
func (a *Authenticator) auditStepUpFailure(ctx context.Context, actor Principal, method, reason string) {
	if err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.stepup.failed", TargetKind: "core.auth_session", TargetID: actor.CredID,
			Meta: map[string]any{"method": method, "reason": reason},
		})
		return err
	}); err != nil {
		a.log.Error("auth: recording failed step-up", "err", err)
	}
}
