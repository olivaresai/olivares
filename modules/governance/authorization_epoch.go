// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// policyChangeAdvancesAuthorizationEpoch reports whether one governance Policy
// mutation changes the effective ABAC authorization generation. A nil old policy
// is a create; a nil next policy is a delete. Approval policy authoring is not an
// authorization-epoch input in this cut.
func policyChangeAdvancesAuthorizationEpoch(old, next *model.Policy) (bool, error) {
	switch {
	case old == nil && next == nil:
		return false, policyAuthorizationEpochUnavailable("policy change has no old or new state", nil)
	case old == nil:
		return next.Kind == policyKindABAC && next.Enabled, nil
	case next == nil:
		return old.Kind == policyKindABAC && old.Enabled, nil
	case old.Kind != next.Kind:
		return false, policyAuthorizationEpochUnavailable("policy kind changed while classifying authorization impact", nil)
	case old.Kind != policyKindABAC:
		return false, nil
	case old.Enabled != next.Enabled:
		return true, nil
	case !old.Enabled:
		return false, nil
	default:
		equal, err := canonicalPolicySpecsEqual(old.Spec, next.Spec)
		if err != nil {
			return false, policyAuthorizationEpochUnavailable("compare canonical policy specs", err)
		}
		return !equal, nil
	}
}

// policiesCanonicallyEqual identifies an exact authoring replay. Policy specs
// are compared through encoding/json's deterministic map-key ordering rather
// than Go map iteration or representation identity. Rule slice order remains
// significant because it is part of the persisted canonical spec.
func policiesCanonicallyEqual(old, next model.Policy) (bool, error) {
	if old.Name != next.Name || old.Kind != next.Kind || old.Enabled != next.Enabled {
		return false, nil
	}
	return canonicalPolicySpecsEqual(old.Spec, next.Spec)
}

func canonicalPolicySpecsEqual(left, right map[string]any) (bool, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

// advancePolicyAuthorizationEpoch reads and advances the tenant's exact epoch
// witness inside the caller's Mutate transaction. It remains the compatibility
// wrapper for existing writers that do not need to carry the new witness onward.
// C3 callers lock first and use advancePolicyAuthorizationEpochFrom to bind a
// post-CAS revision/freshness/audit to that exact fact without any second read.
func advancePolicyAuthorizationEpoch(ctx context.Context, sc store.Scope) error {
	_, err := advancePolicyAuthorizationEpochWitness(ctx, sc)
	return err
}

// advancePolicyAuthorizationEpochWitness advances the canonical tenant epoch and
// returns the exact, validated CAS result. The returned fact is the only safe
// generation witness for subsequent authority rows in this Mutate: a post-CAS read
// could be decorated or superseded and must not be silently attributed to this CAS.
func advancePolicyAuthorizationEpochWitness(ctx context.Context, sc store.Scope) (store.AuthorizationFactRef, error) {
	epochs, ok := sc.(store.AuthorizationEpochStore)
	if !ok {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("scope lacks complete authorization epoch capability", nil)
	}

	current, err := epochs.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("read exact authorization epoch witness", err)
	}
	if !validPolicyAuthorizationEpochFact(sc.Tenant(), current) {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("authorization epoch witness is not exact for the tenant", nil)
	}
	return advancePolicyAuthorizationEpochFrom(ctx, sc, current)
}

// advancePolicyAuthorizationEpochFrom advances from a previously validated and locked
// witness without rereading the epoch. C3 uses this immediately after
// lockPolicyAuthorizationEpochWitness: a second Read between lock and CAS could be
// decorated as another valid-looking generation, breaking the lock/CAS chain.
func advancePolicyAuthorizationEpochFrom(
	ctx context.Context,
	sc store.Scope,
	locked store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	epochs, ok := sc.(store.AuthorizationEpochStore)
	if !ok {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("scope lacks complete authorization epoch capability", nil)
	}
	if !validPolicyAuthorizationEpochFact(sc.Tenant(), locked) {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("locked authorization epoch witness is not exact for the tenant", nil)
	}
	if locked.Version == math.MaxInt64 {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("authorization epoch is exhausted", nil)
	}
	next, err := epochs.BumpAuthorizationEpoch(ctx, locked)
	if err != nil {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("compare-and-swap authorization epoch", err)
	}
	if !validPolicyAuthorizationEpochFact(sc.Tenant(), next) ||
		next.ID != locked.ID || next.Version != locked.Version+1 {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("authorization epoch did not advance exactly once", nil)
	}
	return next, nil
}

// lockPolicyAuthorizationEpoch pins the tenant's exact epoch witness to the caller's
// Mutate transaction without advancing it. Managed/authored/adopted Cedar writers use
// this before reading the authority they will recompose: a later epoch-aware writer must
// then wait on the same row, and a writer that committed between the initial read and the
// lock makes LockAuthoritySnapshot fail stale instead of letting a plan cross snapshots.
//
// The combined epoch capability is mandatory even for a mutation whose final projection
// is byte-identical. Accepting a reader plus an unrelated locker would serialize reads but
// provide no proof that this scope can atomically advance the same canonical witness when
// the prospective projection does change. Existing callers only need the lock; C3
// callers that must compare this exact witness to an installed runtime use
// lockPolicyAuthorizationEpochWitness.
func lockPolicyAuthorizationEpoch(ctx context.Context, sc store.Scope) error {
	locked, err := lockPolicyAuthorizationEpochWitness(ctx, sc)
	if err != nil {
		return err
	}
	// Preserve the legacy writer contract: a path that might advance must reject
	// exhaustion while taking its lock. C3 obtains the witness directly so an exact
	// rollback-current can still prove/report the already-selected Max generation
	// without attempting a write; advancePolicyAuthorizationEpochFrom rejects it.
	if locked.Version == math.MaxInt64 {
		return policyAuthorizationEpochUnavailable("authorization epoch is exhausted", nil)
	}
	return nil
}

// lockPolicyAuthorizationEpochWitness reads and locks the exact canonical fact, then
// returns the SAME fact that was locked. It avoids a second, independently decorable
// read when a caller must make a monotonic decision before dependent authority reads.
func lockPolicyAuthorizationEpochWitness(ctx context.Context, sc store.Scope) (store.AuthorizationFactRef, error) {
	epochs, ok := sc.(store.AuthorizationEpochStore)
	if !ok {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("scope lacks complete authorization epoch capability", nil)
	}
	locker, ok := sc.(store.AuthoritySnapshotLocker)
	if !ok {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("scope lacks authorization snapshot lock capability", nil)
	}

	current, err := epochs.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("read exact authorization epoch witness for lock", err)
	}
	if !validPolicyAuthorizationEpochFact(sc.Tenant(), current) {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("authorization epoch lock witness is not exact for the tenant", nil)
	}
	if err := locker.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{current}); err != nil {
		return store.AuthorizationFactRef{}, policyAuthorizationEpochUnavailable("lock exact authorization epoch witness", err)
	}
	return current, nil
}

func validPolicyAuthorizationEpochFact(
	tenant model.TenantID,
	fact store.AuthorizationFactRef,
) bool {
	return !tenant.IsZero() && !tenant.IsSystem() &&
		fact.Kind == model.AuthorizationEpochKind &&
		fact.ID == model.ID(tenant) && fact.Version >= 1
}

func policyAuthorizationEpochUnavailable(reason string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", store.ErrAuthorizationEpochUnavailable, reason)
	}
	return fmt.Errorf("%w: %s: %v", store.ErrAuthorizationEpochUnavailable, reason, err)
}
