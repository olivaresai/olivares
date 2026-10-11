// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"strings"
	"unicode/utf8"
)

// AutonomyDeclarations contains only active schedule intent for one exact agent
// reference. It says nothing about fired work, safety or external orchestration.
type AutonomyDeclarations struct {
	State      string
	Scheduled  bool
	Autonomous bool
}

// DeclaredAutonomy reads one bounded, tenant-pinned page of native declarations.
// none_declared means no active declaration in this scope, never non-autonomy.
// partial means the native page did not cover all matching declarations. Positive
// flags remain declaration evidence; absence in that page proves nothing.
func (m *Module) DeclaredAutonomy(ctx context.Context, tenant model.TenantID, agentRef string) (AutonomyDeclarations, error) {
	result := AutonomyDeclarations{State: "unknown"}
	if m == nil || m.data == nil {
		return result, errors.New("orchestration autonomy source unavailable")
	}
	ref := strings.TrimSpace(agentRef)
	if tenant.IsZero() || ref == "" || utf8.RuneCountInString(ref) > maxRefLen {
		return result, nil
	}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		records, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSubjectKind, "agent"), eq(colSubjectRef, ref), eq(colDesiredStat, "active")}, Limit: listCap})
		if err != nil {
			return err
		}
		result.State = "none_declared"
		for _, rec := range records {
			result.State = "declared"
			result.Scheduled = true
			switch rec.String(colTriggerKind) {
			case "cron", "event":
				result.Autonomous = true
			}
		}
		if page.HasMore {
			result.State = "partial"
		}
		return nil
	})
	if err != nil {
		return AutonomyDeclarations{State: "unknown"}, err
	}
	return result, nil
}
