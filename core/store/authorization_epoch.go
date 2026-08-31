// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/olivaresai/olivares/core/model"
)

// ErrAuthorizationEpochUnavailable means the store could not establish or
// advance one exact, canonical authorization generation. Missing, duplicate,
// malformed, stale and exhausted generations all map here. Callers must turn
// it into UNKNOWN/fail-closed evidence, never generation zero or a clean fact.
var ErrAuthorizationEpochUnavailable = errors.New("authorization epoch unavailable")

// AuthorizationEpochReader is an OPTIONAL, payload-free Scope capability. It
// returns only the exact kind/id/version witness for the surrounding tenant and
// transaction; it cannot enumerate tenants or expose an epoch row.
type AuthorizationEpochReader interface {
	ReadAuthorizationEpoch(context.Context) (AuthorizationFactRef, error)
}

// AuthorizationEpochBumper is an OPTIONAL Mutate-scope capability. It advances
// the surrounding tenant's epoch only when expected is its exact current
// witness. The compare-and-swap and the caller's governed write share one
// transaction, so either both commit or both roll back. A View scope refuses it.
type AuthorizationEpochBumper interface {
	BumpAuthorizationEpoch(context.Context, AuthorizationFactRef) (AuthorizationFactRef, error)
}

// AuthorizationEpochStore is the narrow read/write capability used by governed
// authorization writers. It deliberately exposes no repository or row payload.
type AuthorizationEpochStore interface {
	AuthorizationEpochReader
	AuthorizationEpochBumper
}

func validateAuthorizationEpochFact(
	tenant model.TenantID,
	fact AuthorizationFactRef,
) error {
	if fact.Kind != model.AuthorizationEpochKind ||
		fact.ID != model.ID(tenant) || fact.Version < 1 {
		return authorizationEpochUnavailable("fact is not bound to the scope tenant", nil)
	}
	return nil
}

func validateAuthorizationEpochAdvance(
	tenant model.TenantID,
	expected, next AuthorizationFactRef,
) error {
	if err := validateAuthorizationEpochFact(tenant, expected); err != nil {
		return err
	}
	if expected.Version == math.MaxInt64 {
		return authorizationEpochUnavailable("generation is exhausted", nil)
	}
	if err := validateAuthorizationEpochFact(tenant, next); err != nil {
		return err
	}
	if next.ID != expected.ID || next.Version != expected.Version+1 {
		return authorizationEpochUnavailable("advance is not the exact next generation", nil)
	}
	return nil
}

func authorizationEpochUnavailable(what string, err error) error {
	if errors.Is(err, ErrAuthorizationEpochUnavailable) {
		return err
	}
	if err == nil {
		return fmt.Errorf("%w: %s", ErrAuthorizationEpochUnavailable, what)
	}
	return fmt.Errorf("%w: %s: %v", ErrAuthorizationEpochUnavailable, what, err)
}
