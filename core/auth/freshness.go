// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// ErrRightsDataStale refuses a new sign-in or the administrative step-up because
// the data that grants the account its rights is older than its source allows.
// It is the one stable refusal of the freshness port; nothing already issued is
// touched.
//
// It also matches ErrStepUpRequired, as the policy's own step-up error does, so
// every consumer that classifies a refused floor keeps its answer; the ones that
// can tell them apart test ErrRightsDataStale first.
var ErrRightsDataStale error = rightsStaleError{}

type rightsStaleError struct{}

func (rightsStaleError) Error() string {
	return "auth: the data that grants this account's rights is out of date; ask an administrator to refresh it"
}

func (rightsStaleError) Is(target error) bool { return target == ErrStepUpRequired }

// FreshnessSource is the optional port a source of rights data fills: for a
// user it returns the instant after which that data is too old, or the zero time
// when nothing bounds it (no data of that source, or a source without a limit).
// The composition root installs it; core names no source. It is called outside
// any store transaction and must be cheap. An error refuses (deny closed).
type FreshnessSource func(ctx context.Context, user model.ID) (notAfter time.Time, err error)

// WithFreshnessSource installs the optional port once before serving. Without
// it (the default) no sign-in and no step-up is ever refused for freshness.
func (a *Authenticator) WithFreshnessSource(src FreshnessSource) *Authenticator {
	a.freshness = src
	return a
}

// requireFresh is the one question both uses ask: a new sign-in and the
// administrative step-up. A port that cannot answer refuses like a stale one.
func (a *Authenticator) requireFresh(ctx context.Context, user model.ID) error {
	return a.checkFresh(ctx, user, true)
}

// checkFresh is requireFresh; logFailure says whether a port failure is logged
// (a sign-in or ceremony, rare) or not (every authenticated request, which would
// flood the log while the source is down).
func (a *Authenticator) checkFresh(ctx context.Context, user model.ID, logFailure bool) error {
	if a.freshness == nil {
		return nil
	}
	notAfter, err := a.freshness(ctx, user)
	if err != nil {
		if logFailure {
			a.log.Error("auth: freshness source failed", "err", err)
		}
		return ErrRightsDataStale
	}
	if !notAfter.IsZero() && !a.clock.Now().Time().Before(notAfter) {
		return ErrRightsDataStale
	}
	return nil
}

// withFreshness marks a session principal whose data is past its instant, so
// StepUpSatisfied refuses it. The principal and its session otherwise stay as
// they are. Call it outside a store transaction.
//
// ponytail: one port read per authenticated session request, two on a governed
// route (Authenticate and ResolvePrincipalScope), while a port is installed
// (none in Community); upgrade to a per-instant cache when the source's owner
// measures that read as costly.
func (a *Authenticator) withFreshness(ctx context.Context, p Principal) Principal {
	if a.freshness != nil && p.Kind == KindUser && a.checkFresh(ctx, p.UserID, false) != nil {
		p.rightsStale = true
	}
	return p
}

// staleOr is the refusal of a failed step-up or floor: the stable freshness
// refusal when p is stale, else refusal.
func staleOr(p Principal, refusal error) error {
	if p.rightsStale {
		return ErrRightsDataStale
	}
	return refusal
}

// StepUpRefusal is the error a failed administrative step-up answers with:
// ErrRightsDataStale when p's data is past its instant, else the policy's own
// step-up requirement.
func StepUpRefusal(ctx context.Context, p Principal) error {
	return staleOr(p, StepUpRequiredFor(StepUpPolicyFrom(ctx)))
}

// requireFreshSignIn is requireFresh for a new sign-in: a refusal also lands on
// the ledger as a blocked login, so an account that should no longer sign in
// leaves a trace of each attempt.
func (a *Authenticator) requireFreshSignIn(ctx context.Context, user model.ID, ip string) error {
	err := a.requireFresh(ctx, user)
	if err != nil {
		a.auditLoginBlocked(ctx, "user:"+user.String(), ip, "rights_data_stale")
	}
	return err
}
