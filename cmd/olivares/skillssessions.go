// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/skills"
)

// The runtime has already admitted this stored run. Its inherited skill pins
// include the tenant catalog, which confined HTTP handlers cannot administer.
// This bridge exposes only its immutable selection and verified files.
type sessionSkillsSource struct {
	st       store.Store
	sessions *sessions.Module
	catalog  *skills.Module
}

func (s sessionSkillsSource) ResolveSessionSkills(ctx context.Context, tenant model.TenantID, runRef string) (skills.Selection, error) {
	var selected skills.Selection
	err := s.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		selected, err = s.sessions.PinSessionSkillsSelection(ctx, sc, runRef, s.catalog)
		return err
	})
	return selected, err
}

func (s sessionSkillsSource) ReadSessionSkillFiles(ctx context.Context, tenant model.TenantID, selected skills.Selection) ([]skills.DeliveryFile, error) {
	var files []skills.DeliveryFile
	err := s.st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		files, err = s.catalog.ReadSelectionFiles(ctx, sc, selected)
		return err
	})
	return files, err
}
