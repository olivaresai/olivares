//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

func (a *Authenticator) ConfigureCAEPSet(context.Context, Principal, model.TenantID, CAEPSetConfig) error {
	return ErrCAEPRequiresBusiness
}

func (a *Authenticator) CAEPReceiveEvent(context.Context, Principal, model.TenantID, CAEPEventEnvelope) (CAEPEventResult, error) {
	return CAEPEventResult{}, ErrCAEPRequiresBusiness
}
