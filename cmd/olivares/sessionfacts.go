// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// sessionFactsSource gives the sessions module the two facts about a session that live
// outside it: the display name of the account it acts for (core accounts, which a
// module never reaches) and how many of its actions a person approved (governance).
type sessionFactsSource struct {
	st        store.Store
	approvals *governance.EngineApprovals
	finops    *finops.Module // nil when the build has no FinOps: no budget limit is shown
}

// UserNames answers only for accounts that are members of the tenant, so a session's
// read can never name an account the tenant does not hold. Email never leaves: an
// account with no display name answers an empty name and the read says why.
func (s sessionFactsSource) UserNames(ctx context.Context, tenant model.TenantID, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	err := s.st.AuthView(ctx, func(sc store.AuthScope) error {
		members, _, err := sc.Memberships().List(ctx, model.Query{Limit: 1000, Filters: []model.Filter{
			{Column: "user_id", Op: model.OpIn, Value: ids},
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		}})
		if err != nil {
			return err
		}
		inTenant := make([]string, 0, len(members))
		for _, m := range members {
			inTenant = append(inTenant, m.UserID.String())
		}
		if len(inTenant) == 0 {
			return nil
		}
		users, _, err := sc.Users().List(ctx, model.Query{Limit: 1000, Filters: []model.Filter{
			{Column: model.ColID, Op: model.OpIn, Value: inTenant},
		}})
		if err != nil {
			return err
		}
		for _, u := range users {
			out[u.ID.String()] = u.DisplayName
		}
		return nil
	})
	return out, err
}

func (s sessionFactsSource) ApprovedActions(ctx context.Context, tenant model.TenantID, sessionIDs []string) (map[string]int64, error) {
	return s.approvals.ApprovedBySession(ctx, tenant, sessionIDs)
}

func (s sessionFactsSource) BudgetLimits(ctx context.Context, tenant model.TenantID, subjects []sessions.SessionBudgetSubject) (map[string]sessions.SessionBudgetLimit, error) {
	out := map[string]sessions.SessionBudgetLimit{}
	if s.finops == nil {
		return out, nil
	}
	asked := make([]finops.SessionBudgetSubject, 0, len(subjects))
	for _, subject := range subjects {
		asked = append(asked, finops.SessionBudgetSubject{Key: subject.Key, SessionRef: subject.SessionRef, AgentRef: subject.AgentRef})
	}
	limits, err := s.finops.SessionBudgets(ctx, tenant, asked)
	for key, limit := range limits {
		out[key] = sessions.SessionBudgetLimit{LimitMicroUSD: limit.LimitMicroUSD, Period: limit.Period}
	}
	return out, err
}
