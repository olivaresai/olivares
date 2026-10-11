// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package eventing

import (
	"context"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
)

func (*Module) IngestAudit(context.Context, model.TenantID, AuditIntake) (int, error) {
	return 0, audit.ErrBusinessAudit
}
