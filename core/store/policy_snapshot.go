// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// PolicySnapshot is a policy row whose nullable Spec holds its exact stored
// text. Snapshot reads do not interpret or rewrite that text.
type PolicySnapshot struct {
	model.BaseFields
	Name    string
	Kind    string
	Enabled bool
	Spec    *string
}

// PolicySnapshotRepository is the optional raw-recovery read capability of a
// policy repository. LockPolicySnapshot requires a Mutate scope.
type PolicySnapshotRepository interface {
	GetPolicySnapshot(context.Context, model.ID) (PolicySnapshot, error)
	ListPolicySnapshots(context.Context, model.Query) ([]PolicySnapshot, model.Page, error)
	LockPolicySnapshot(context.Context, model.ID) (PolicySnapshot, error)
}
