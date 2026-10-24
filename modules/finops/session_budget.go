// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errSessionBudgetsTruncated = errors.New("finops: budget set truncated at scan cap; the session limit is unknown")

// SessionBudgetSubject names one session whose budget limit a console wants to show:
// Key is the caller's own handle for the answer, SessionRef the session's canonical
// id and AgentRef the agent identity that drove it.
type SessionBudgetSubject struct {
	Key        string
	SessionRef string
	AgentRef   string
}

// SessionBudget is the limit of the most restrictive enabled budget scoped to one
// session or to its agent identity.
type SessionBudget struct {
	LimitMicroUSD int64
	Period        string
}

// SessionBudgets answers, in one read of the tenant's budgets, which limit applies to
// each subject. Only budgets scoped to the session itself or to its agent identity
// count: a company-wide or per-model budget limits many sessions and is not "this
// session's limit". A subject no budget scopes is absent from the answer.
func (m *Module) SessionBudgets(ctx context.Context, tenant model.TenantID, subjects []SessionBudgetSubject) (map[string]SessionBudget, error) {
	out := map[string]SessionBudget{}
	if m.data == nil || len(subjects) == 0 {
		return out, nil
	}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		budgets, truncated, err := listAllBudgets(ctx, sc)
		if err != nil {
			return err
		}
		if truncated {
			// Past the scan cap the most restrictive limit is unknown: say so, as
			// enforcement does, rather than show no limit.
			return errSessionBudgetsTruncated
		}
		for _, p := range budgets {
			if !p.Enabled {
				continue
			}
			spec := parseBudgetSpec(p.Spec)
			spec.fillDefaults()
			if spec.Dimension != "session" && spec.Dimension != "agent" {
				continue
			}
			for _, s := range subjects {
				if !spec.matches(attribution{SessionRef: s.SessionRef, AgentRef: s.AgentRef}) {
					continue
				}
				if prior, ok := out[s.Key]; !ok || spec.LimitMicroUSD < prior.LimitMicroUSD {
					out[s.Key] = SessionBudget{LimitMicroUSD: spec.LimitMicroUSD, Period: spec.Period}
				}
			}
		}
		return nil
	})
	return out, err
}
