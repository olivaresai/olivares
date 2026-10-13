// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// createOrganizationFixture seeds tenant isolation tests through the real store.
// Organization management through HTTP is an Enterprise capability.
func createOrganizationFixture(t *testing.T, st store.Store, name, slug string) model.Org {
	t.Helper()
	ctx := t.Context()
	var org model.Org
	if err := st.System(ctx, func(sys store.SystemScope) error {
		var err error
		org, err = sys.CreateOrg(ctx, model.Org{Name: name, Slug: slug, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatalf("create organization fixture %q: %v", slug, err)
	}
	return org
}
