// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/orchestration"
)

// orchestrationAutonomy adapts native declarations, never activity or payloads.
type orchestrationAutonomy struct{ source *orchestration.Module }

var _ compliance.AutonomySource = orchestrationAutonomy{}

func (a orchestrationAutonomy) Autonomy(ctx context.Context, tenant model.TenantID, agentRef string) (compliance.AutonomySignal, error) {
	d, err := a.source.DeclaredAutonomy(ctx, tenant, agentRef)
	if err != nil {
		return compliance.AutonomySignal{}, err
	}
	return compliance.AutonomySignal{State: d.State, Scheduled: d.Scheduled, Autonomous: d.Autonomous}, nil
}
