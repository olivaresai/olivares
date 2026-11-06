// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// PolicyResolver selects the acting person's policies within the supplied tenant
// transaction. userID is a stable human ID, empty for system actors and session-filed
// approvals. An empty ID selects the tenant default. Implementations
// return selected=false to retain tenant-wide policy (including when a licence
// does not cover selection), or selected=true for a replacement, even an empty one.
// Errors always fail closed. A nil resolver preserves tenant-wide policy.
// Return only enabled approval policies, in precedence order. DLP rows use
// class and action; an empty list disables DLP just as no tenant rules do.
// The composition root supplies the same resolver to governance and knowledge.
type PolicyResolver interface {
	ApprovalPolicies(context.Context, Scope, string) ([]model.Policy, bool, error)
	DLPRules(context.Context, Scope, string) ([]model.Record, bool, error)
}
