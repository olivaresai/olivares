// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// AuditReader opens existing evidence without migrating or publishing a Store.
// ViewAudit pins one tenant and one read-only snapshot, including for suspended
// tenants. Append and other optional writing capabilities refuse ErrReadOnly.
type AuditReader interface {
	ViewAudit(context.Context, model.TenantID, func(AuditLog) error) error
	Close() error
}
