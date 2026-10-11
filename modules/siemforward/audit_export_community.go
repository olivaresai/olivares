// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package siemforward

import (
	"context"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
)

func (*Module) Forward(context.Context, model.AuditEvent) error { return audit.ErrBusinessAudit }
func (*Module) ForwardDue(context.Context, model.TenantID) (int, error) {
	return 0, audit.ErrBusinessAudit
}
