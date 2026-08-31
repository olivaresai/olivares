// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrUserCapRequiresEnterprise WAS returned when creating another user account
// would exceed the community user-seat cap. Since B10 (2026-07-27) self-hosted
// has NO user cap in ANY tier, so NO code path returns it any more: it is kept
// as an exported symbol — and kept mapped to its distinct 403 code in
// core/api/errors.go — purely for compatibility (an SDK, a console build or the
// closed enterprise overlay that still switches on it keeps compiling and keeps
// behaving). Deleting the seam is NOT the same as removing the limit; only the
// limit was removed.
//
// See an internal design note (not shipped) (`self_hosted.users: unlimited`) and
// an internal design note (not shipped) §B10.
var ErrUserCapRequiresEnterprise = errors.New("auth: user_cap_requires_enterprise: another active user account requires the enterprise build and a commercial license")

// CommunitySeatLimit is the active-user limit of the default (AGPL/community)
// build. It is 0 — which in this package's vocabulary (see SeatPolicy) means
// UNLIMITED. B10 removed the cap of 3 from every self-hosted tier: Community,
// Business, add-ons and Enterprise all admit an unlimited number of accounts, and
// no commercial state (present license, expired license, no license at all) can
// ever change that.
//
// The constant is DELIBERATELY not deleted. The closed enterprise overlay lives in
// a separate repository and reads this symbol as the limit it falls back to when no
// valid license is present; at 0 that fallback becomes "unlimited" too, so the
// lapse-degrades-to-3 behavior disappears from the overlay as well — but ONLY once
// it is REBUILT against this core. A Go constant is baked in at compile time, and an
// enterprise binary compiled before this change carries the old value AND the old
// enforcement, so nothing here retrofits an already-shipped artifact: that needs a
// rebuild and a binary swap (and the credential side — attesting MaxUsers=0 — is
// campaign G-1). Same reasoning for the whole seam below: it is retained as a
// compatibility no-op, never as a runtime gate.
//
// (History: Wired superadmin + 1, the post launch plan raised it to
// superadmin + 2 = 3 active seats, and B10 removed it outright. The pricing model
// is now term-based commercial entitlement, never per-seat: PRICING-CANON.md.)
const CommunitySeatLimit = 0

// SeatPolicy is the retained seat seam. It reports the active-user entitlement a
// build wants to ADVERTISE; it does NOT gate anything. Since B10 no seat figure
// is enforced at runtime by this binary in any edition — enforceSeatCapTx is an
// unconditional no-op — so a policy is display/attestation plumbing only.
//
// The 10-user cap of the Cloud service is NOT this: it belongs to the cloud
// control plane and is applied per tenant there, never by the seat policy of the
// engine binary (PRICING-CANON.md, `modules_hold`: "`seats` NO es payload: es PLAN
// CONTROL").
//
// A nil policy (a library embedder, or a test that constructs the Authenticator
// directly) means UNLIMITED, as does a policy reporting ok=false or a limit <= 0.
type SeatPolicy interface {
	// MaxActiveUsers reports a finite active-user figure with ok=true, or
	// unlimited with ok=false / a limit <= 0. It is never consulted to refuse an
	// account (B10); SeatLimit surfaces it for display only.
	MaxActiveUsers() (limit int, ok bool)
}

// communitySeatPolicy reports UNLIMITED active users (B10). The type is kept so
// the wiring seam in cmd/olivares survives the removal of the cap.
type communitySeatPolicy struct{}

// NewCommunitySeatPolicy returns the default (AGPL) build's seat policy. Since
// B10 it reports UNLIMITED (CommunitySeatLimit = 0): the community build has no
// user cap, and neither has any other self-hosted tier.
func NewCommunitySeatPolicy() SeatPolicy { return communitySeatPolicy{} }

func (communitySeatPolicy) MaxActiveUsers() (int, bool) { return CommunitySeatLimit, false }

// WithSeatPolicy sets the user-seat policy and returns the Authenticator for
// chaining. It is called once at boot, before serving, so it is race-free. Since
// B10 the policy only feeds the SeatLimit display accessor; account creation is
// UNLIMITED whatever is wired (including nil).
func (a *Authenticator) WithSeatPolicy(p SeatPolicy) *Authenticator {
	a.seatPolicy = p
	return a
}

// SeatLimit reports the active-user figure the wired seat policy ADVERTISES, for
// display only (the console edition panel). ok=false (and limit<=0) means
// unlimited, which since B10 is what every policy this repository ships reports —
// a nil policy (a library/test embedder) and the community policy alike.
//
// It is NOT an enforcement read: nothing refuses an account on this number any
// more (enforceSeatCapTx is an unconditional no-op). The console panel therefore
// renders active-user USAGE, never a quota.
//
// A non-positive figure is NORMALIZED to (0,false) whatever the policy said. A
// policy returning (0,true) — "a limit of zero applies" — would otherwise be
// published on the wire as seat_limited=true/seat_limit=0, which a client would
// honestly read as "no account may exist": the exact opposite of the truth. Only a
// STRICTLY POSITIVE figure is passed through, and even that is advisory.
func (a *Authenticator) SeatLimit() (limit int, ok bool) {
	if a.seatPolicy == nil {
		return 0, false // unlimited (library/test embedder)
	}
	limit, ok = a.seatPolicy.MaxActiveUsers()
	if !ok || limit <= 0 {
		return 0, false // unlimited, in one canonical spelling
	}
	return limit, true
}

// ActiveUserCount counts ACTIVE user accounts up to max and reports whether it was
// CAPPED at max (more active accounts may exist), for the console's active-user
// USAGE display. A max<=0 uses a sane display bound. It is read-only (AuthView) and
// counts only model.StatusActive accounts, so disabling an account (including the
// internal superadmin, see SetSuperadminActive) lowers the figure.
//
// Cost, stated honestly: the query bounds the rows RETURNED at max+1, not the rows
// EXAMINED. There is no index on users.status (see the descriptor in
// core/internal/store/sqlstore/authcatalog.go), so an estate with many INACTIVE
// accounts can still make the engine walk a long way to find max+1 active ones. The
// read is superadmin-only and infrequent, which is why that is acceptable rather
// than fixed here; it is a display bound, never an entitlement.
//
// It is pure telemetry: since B10 nothing compares this count against a limit.
func (a *Authenticator) ActiveUserCount(ctx context.Context, max int) (count int, capped bool, err error) {
	if max <= 0 {
		max = 100000 // a generous display bound; "capped" tells the UI to show "N+"
	}
	err = a.st.AuthView(ctx, func(as store.AuthScope) error {
		// Read one past the bound so we can tell "exactly max" from "more than max".
		users, _, lerr := as.Users().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "status", Op: model.OpEq, Value: string(model.StatusActive)}},
			Limit:   max + 1,
		})
		if lerr != nil {
			return lerr
		}
		if len(users) > max {
			count, capped = max, true
		} else {
			count = len(users)
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return count, capped, nil
}

// enforceSeatCapTx is the retained account-creation seam, and since B10 it is an
// UNCONDITIONAL NO-OP: it always returns nil, reads nothing, and can refuse
// nothing. Self-hosted user accounts are unlimited in every tier and under every
// commercial state — no license, a valid license, an expired one — so the binary
// must never count seats to decide whether an account may exist
// (an internal design note (not shipped): "el seam `seats` queda como no-op de compatibilidad;
// MaxUsers=0; JAMÁS gate runtime").
//
// Why keep it rather than delete it and its call sites (CreateUser,
// OnboardMember)? Because the removal is of the LIMIT, not of the seam: the
// single choke point stays wired inside the caller's AuthMutate transaction, so
// the invariant "no seat figure gates account creation" is enforced in ONE place
// and any future policy that tried to re-cap through core would have to pass
// through this function — and this function never says no. A deleted seam would
// scatter that decision back into the call sites.
//
// Callers keep invoking it before creating an account; the parameters are
// retained (and ignored) so the signature does not churn.
func (a *Authenticator) enforceSeatCapTx(_ context.Context, _ store.AuthScope) error {
	return nil
}
