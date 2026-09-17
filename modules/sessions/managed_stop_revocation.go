// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"time"
)

// managed_stop_revocation.go (P2 / W2) — the bounded revocation phase.
//
// Credential revocation deliberately runs under context.WithoutCancel: a caller
// that walked away must not leave a live credential behind, so the revocation
// outlives the request. That is right for the legacy Stop and StopForWork paths
// and this file does not change them.
//
// It is wrong for a managed Stop. A managed Stop holds the run token and owns a
// journal row it must settle; a revocation that waits forever on a blocked writer
// or an unanswering issuer does not merely delay itself — it holds the token and
// leaves the operation claimed with nothing able to settle it. So a managed Stop
// runs its revocations inside ONE phase whose deadline is created after the
// process was told to stop, and every blocking revocation site inherits it.
//
// ⛔ THE MARKER IS THE WHOLE MECHANISM, AND ITS ABSENCE IS THE OLD BEHAVIOR.
// Legacy Stop and StopForWork never set it, so each of the four sites still
// evaluates context.WithoutCancel(ctx) exactly as before. Bounding them instead
// would trade a documented wait for an undocumented orphaned credential.
//
// No numerical teardown total is claimed here: whether the stores observe the
// deadline while waiting for writer or lock admission is measured, not assumed.

// boundedRevocationKey is the private context key of the marker. A caller outside
// this package cannot set it, so the bound cannot be acquired by an unrelated
// path that merely passes a context through.
type boundedRevocationKey struct{}

// withBoundedRevocation marks ctx as a bounded revocation phase. The DEADLINE is
// the caller's to attach: the marker only says "do not detach from it".
func withBoundedRevocation(ctx context.Context) context.Context {
	return context.WithValue(ctx, boundedRevocationKey{}, struct{}{})
}

// revocationContext is the context one blocking revocation site runs under.
//
// Unmarked it is byte-for-byte the previous behavior, context.WithoutCancel. A
// marked phase context is returned AS IS, so its single deadline reaches every
// site — including both handle-clear attempts, which therefore share one bound
// rather than each receiving a fresh one.
func revocationContext(ctx context.Context) context.Context {
	if _, bounded := ctx.Value(boundedRevocationKey{}).(struct{}); bounded {
		return ctx
	}
	return context.WithoutCancel(ctx)
}

// revocationPhase creates the context a stop's credential revocation runs under.
// It is invoked only after Process.Stop has returned, so a bounded phase measures
// the revocation and not the stop before it.
type revocationPhase func() (context.Context, context.CancelFunc)

// legacyRevocationPhase is the unchanged operator path: the caller's own context,
// unmarked, so every site detaches from it exactly as it always did.
func legacyRevocationPhase(ctx context.Context) revocationPhase {
	return func() (context.Context, context.CancelFunc) { return ctx, func() {} }
}

// managedRevocationPhase is the managed Stop's phase: detached from the caller's
// cancellation like the legacy path, marked, and bounded by the admission window
// counted from the moment the phase starts.
func managedRevocationPhase(ctx context.Context, bound time.Duration) revocationPhase {
	return func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(withBoundedRevocation(context.WithoutCancel(ctx)), bound)
	}
}
