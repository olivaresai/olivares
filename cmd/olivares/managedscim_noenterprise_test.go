// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import "testing"

func TestNewManagedSCIMIsNil(t *testing.T) {
	t.Parallel()
	if got := newManagedSCIM(); got != nil {
		t.Fatalf("community managed-SCIM seam must stay nil, got %T", got)
	}
}
