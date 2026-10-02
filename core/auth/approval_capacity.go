// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ApprovalCapacity counts distinct active people able to administer this tenant.
// Global administrators and tenant administrators are deduplicated by user id;
// tokens and workspace-confined memberships cannot add people to the quorum.
func (a *Authenticator) ApprovalCapacity(ctx context.Context, tenant model.TenantID) (int64, error) {
	people := map[model.ID]bool{}
	err := a.st.AuthView(ctx, func(sc store.AuthScope) error {
		q := model.Query{Limit: 200}
		for pages := 0; ; pages++ {
			if pages >= 10000 {
				return errors.New("approval capacity user inventory exceeds limit")
			}
			users, page, err := sc.Users().List(ctx, q)
			if err != nil {
				return err
			}
			for _, u := range users {
				if u.Status == model.StatusActive && u.DeletedAt == nil && u.IsSuperadmin {
					standing, err := loadStanding(ctx, sc, u.ID, "")
					if err != nil {
						return err
					}
					if _, excluded := standing.excluded[tenant]; !excluded {
						people[u.ID] = true
					}
				}
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			if q.Cursor == page.Cursor {
				return errors.New("approval capacity user cursor did not advance")
			}
			q.Cursor = page.Cursor
		}
		q = model.Query{Limit: 200, Filters: []model.Filter{{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()}}}
		for pages := 0; ; pages++ {
			if pages >= 10000 {
				return errors.New("approval capacity membership inventory exceeds limit")
			}
			memberships, page, err := sc.Memberships().List(ctx, q)
			if err != nil {
				return err
			}
			for _, membership := range memberships {
				if !membership.WorkspaceID.IsZero() || people[membership.UserID] {
					continue
				}
				u, err := sc.Users().Get(ctx, membership.UserID)
				if err != nil {
					return err
				}
				if u.Status == model.StatusActive && u.DeletedAt == nil {
					grants, _, confined, err := loadGrants(ctx, sc, u.ID, u.IsSuperadmin)
					if err != nil {
						return err
					}
					if RoleRank(grants[tenant]) < RoleRank(RoleAdmin) || !confined[tenant].IsZero() {
						continue
					}
					standing, err := loadStanding(ctx, sc, u.ID, "")
					if err != nil {
						return err
					}
					if _, excluded := standing.excluded[tenant]; !excluded {
						people[u.ID] = true
					}
				}
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			if q.Cursor == page.Cursor {
				return errors.New("approval capacity membership cursor did not advance")
			}
			q.Cursor = page.Cursor
		}
		return nil
	})
	return int64(len(people)), err
}
