// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// ErrSelectiveMutationPlan identifies an invalid or violated narrow mutation
// plan. A scope implementation must poison the surrounding transaction when a
// callback attempts an operation outside its plan, even if the callback
// discards the returned error.
var ErrSelectiveMutationPlan = errors.New("selective mutation plan invalid or violated")

// SelectiveMutator is an OPTIONAL Store capability for the two transaction
// classes that do not participate in tenant lineage authority. It is separate
// from Store so existing stores and test doubles remain source-compatible, and
// decorators must expose it if and only if their wrapped Store does.
//
// Both methods are business-tenant only. Callers that accept the reserved
// SYSTEM tenant must retain Store.Mutate as their compatibility path.
type SelectiveMutator interface {
	// MutateCoordination takes every key in plan before invoking fn. Matching
	// plans serialize; disjoint plans may enter concurrently on PostgreSQL.
	MutateCoordination(
		ctx context.Context,
		tenant model.TenantID,
		plan TransactionLockPlan,
		fn func(CoordinationMutationScope) error,
	) error
	// MutateEvidenceOperation exposes exactly one evidence operation and its
	// repository-owned audit append. The plan's operation ID is the only ID the
	// callback may read, claim or settle.
	MutateEvidenceOperation(
		ctx context.Context,
		tenant model.TenantID,
		plan EvidenceOperationPlan,
		fn func(EvidenceOperationMutationScope) error,
	) error
}

// NeutralMutationScope is the common read-only width of selective callbacks.
// Org exists so residency and suspension decorators can apply their real policy
// inside this transaction. It does not confer any organization write ability.
type NeutralMutationScope interface {
	Tenant() model.TenantID
	Org(context.Context) (model.Org, error)
}

// CoordinationMutationScope is deliberately narrower than Scope. The planned
// keys are already held before the callback, so it does not expose a dynamic
// TransactionLocker that could extend or invert their order.
type CoordinationMutationScope interface {
	NeutralMutationScope
	TransactionClock
}

// EvidenceOperationMutationScope exposes one plan-confined evidence journal.
// It has no raw Audit, Ext, transaction locker or authority capability.
type EvidenceOperationMutationScope interface {
	NeutralMutationScope
	EvidenceOperations() EvidenceOperationRepo
}

// TransactionLockPlan is an immutable canonical set of exact transaction lock
// keys. Its fields are private so a zero value is the only plan a caller can
// construct without validation.
type TransactionLockPlan struct {
	keys []string
}

// NewTransactionLockPlan validates, sorts and deduplicates keys. Sorting changes
// acquisition order only: key bytes, including surrounding whitespace, retain
// their exact identity.
func NewTransactionLockPlan(keys ...string) (TransactionLockPlan, error) {
	if len(keys) == 0 {
		return TransactionLockPlan{}, fmt.Errorf("%w: transaction key set is empty", ErrSelectiveMutationPlan)
	}
	canonical := append([]string(nil), keys...)
	for _, key := range canonical {
		if strings.TrimSpace(key) == "" {
			return TransactionLockPlan{}, fmt.Errorf("%w: transaction key is empty", ErrSelectiveMutationPlan)
		}
	}
	sort.Strings(canonical)
	deduped := canonical[:0]
	for _, key := range canonical {
		if len(deduped) == 0 || deduped[len(deduped)-1] != key {
			deduped = append(deduped, key)
		}
	}
	return TransactionLockPlan{keys: deduped}, nil
}

// TransactionKeys returns a copy of the canonical exact keys for a Store
// implementation. Mutating the result cannot change the plan.
func (p TransactionLockPlan) TransactionKeys() []string {
	return append([]string(nil), p.keys...)
}

// EvidenceOperationPlan is an immutable declaration of the one journal identity
// reachable in a selective evidence transaction.
type EvidenceOperationPlan struct {
	operationID string
}

// NewEvidenceOperationPlan validates operationID while preserving its exact
// identity. Whitespace-only IDs are invalid under the evidence binding contract.
func NewEvidenceOperationPlan(operationID string) (EvidenceOperationPlan, error) {
	if strings.TrimSpace(operationID) == "" {
		return EvidenceOperationPlan{}, fmt.Errorf("%w: evidence operation ID is empty", ErrSelectiveMutationPlan)
	}
	return EvidenceOperationPlan{operationID: operationID}, nil
}

// OperationID returns the plan's exact journal identity.
func (p EvidenceOperationPlan) OperationID() string { return p.operationID }
