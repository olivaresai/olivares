// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SeedStoredAuthorizationForTest represents an installation predating the edition
// move. This export exists only in the test binary, never in a product build.
func SeedStoredAuthorizationForTest(ctx context.Context, st store.Store, tenant model.TenantID, records map[model.Kind][]model.Record) error {
	var subjects []model.ID
	for _, record := range records[scopedGrantKind] {
		if record.String(colSGSubjectKind) == subjectUser {
			subjects = append(subjects, model.ID(record.String(colSGSubjectRef)))
		}
	}
	refs, err := auth.FenceSubjects(ctx, auth.NewAuthenticator(st, nil), tenant, subjects)
	if err != nil {
		return err
	}
	return st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := pinPolicyAuthorizationEpochWitness(ctx, sc, refs); err != nil {
			return err
		}
		if err := advancePolicyAuthorizationEpoch(ctx, sc); err != nil {
			return err
		}
		for _, kind := range []model.Kind{permGroupKind, customRoleKind, scopedGrantKind} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			for _, record := range records[kind] {
				if _, err := repo.Create(ctx, record); err != nil {
					return err
				}
			}
		}
		state, err := loadManagedProjectionState(ctx, sc)
		if err != nil {
			return err
		}
		_, _, err = appendRevision(ctx, sc, surfaceCedarManaged, projectManagedCedar(state.grants, state.roles, state.groups), "upgrade-fixture", true, true, "stored before upgrade")
		return err
	})
}
