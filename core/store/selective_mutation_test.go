// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestTransactionLockPlanPreservesExactCanonicalKeys(t *testing.T) {
	plan, err := store.NewTransactionLockPlan("b", " a", "b", "a ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{" a", "a ", "b"}
	if got := plan.TransactionKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %#v, want exact sorted/deduped %#v", got, want)
	}
	got := plan.TransactionKeys()
	got[0] = "changed"
	if again := plan.TransactionKeys(); !reflect.DeepEqual(again, want) {
		t.Fatalf("caller mutated immutable plan: %#v", again)
	}
	for _, keys := range [][]string{nil, {}, {""}, {" \t\n"}} {
		if _, err := store.NewTransactionLockPlan(keys...); !errors.Is(err, store.ErrSelectiveMutationPlan) {
			t.Fatalf("NewTransactionLockPlan(%#v) = %v, want ErrSelectiveMutationPlan", keys, err)
		}
	}
}

func TestEvidenceOperationPlanPreservesExactIdentity(t *testing.T) {
	plan, err := store.NewEvidenceOperationPlan(" op-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.OperationID(); got != " op-1 " {
		t.Fatalf("operation ID = %q, want exact identity", got)
	}
	for _, id := range []string{"", " \t\n"} {
		if _, err := store.NewEvidenceOperationPlan(id); !errors.Is(err, store.ErrSelectiveMutationPlan) {
			t.Fatalf("NewEvidenceOperationPlan(%q) = %v, want ErrSelectiveMutationPlan", id, err)
		}
	}
}
