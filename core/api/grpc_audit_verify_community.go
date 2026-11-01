// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package api

import (
	"context"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/audit"
)

func (g *grpcService) VerifyAudit(ctx context.Context, req *apiv1.VerifyAuditRequest) (*apiv1.VerifyAuditResponse, error) {
	if _, _, err := g.s.grpcAuthorize(ctx, "audit:read", req.GetTenant()); err != nil {
		return nil, grpcError(err)
	}
	return nil, grpcError(audit.ErrBusinessAudit)
}
