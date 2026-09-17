// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"time"
)

// LoginCapabilityPort is the transaction's view of the durable global/default
// login-enforcement capability observation (R5, ROOT-CONSTRUCTION-R5-1 §4).
//
// An observation means only that an artifact declared the login enforcement component
// available during writer promotion. It is not proof of later startup, of any evaluated
// login, or of historical enforcement. An absent row is unknown history.
//
// The state belongs to the enclosing transaction: every port obtained from the same
// AuthScope shares it, the first failure is sticky, and a Mutate whose callback
// discarded a failure refuses to commit.
type LoginCapabilityPort interface {
	// Lock takes the store-owned transaction lock that serializes capability and
	// posture writers with absent-component session creators, then reads the
	// observation. It must precede every directory, user-authority and audit lock in
	// the transaction and serializes even when no row exists. It is idempotent after a
	// successful acquisition. Inside AuthView it returns ErrReadOnly.
	Lock(context.Context) (LoginCapabilityObservation, error)
	// Observe records one observation for the given nonempty artifact version using
	// database time. It requires a successful Lock, writes at most once per
	// transaction, preserves the first observation time, and returns the cached
	// result on repeated calls. Inside AuthView it returns ErrReadOnly.
	Observe(context.Context, string) (LoginCapabilityObservation, error)
	// Read returns the stored observation without locking, writing or repairing.
	Read(context.Context) (LoginCapabilityObservation, error)
}

// LoginCapabilityObservation is the stored observation. Timestamps are informational;
// only Present carries meaning, and the artifact version is never an entitlement.
type LoginCapabilityObservation struct {
	Present             bool
	FirstObservedAt     time.Time
	LastObservedAt      time.Time
	LastArtifactVersion string
	ObservationCount    int64
}

var (
	// ErrLoginCapabilityLockOrder reports a capability lock requested after this
	// transaction already held or noted a directory, user-authority or audit lock.
	ErrLoginCapabilityLockOrder = errors.New("store: the login capability lock must precede directory, user-authority and audit locks")
	// ErrLoginCapabilityNotLocked reports an observation without the capability lock.
	ErrLoginCapabilityNotLocked = errors.New("store: login capability observation requires the capability lock in this transaction")
	// ErrLoginCapabilityArtifactVersion reports an empty artifact version.
	ErrLoginCapabilityArtifactVersion = errors.New("store: login capability observation requires a nonempty artifact version")
	// ErrLoginCapabilityMalformed reports a stored observation that violates its contract.
	ErrLoginCapabilityMalformed = errors.New("store: stored login capability observation is malformed")
)
