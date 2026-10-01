// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The raw authority read stays in the composition root. Service-withdrawal and
// residency guards remain on this store. The module checks the live grant and
// hands the work callback only its tenant-and-workspace-confined scope.
type sessionOrchestrationWorkScope struct {
	st     store.Store
	module *sessions.Module
}

func (s sessionOrchestrationWorkScope) WithScope(ctx context.Context, principal auth.Principal, tenant model.TenantID, mutate bool, fn func(store.Scope) error) error {
	if s.st == nil || s.module == nil {
		return auth.ErrUnauthenticated
	}
	operation := s.st.View
	if mutate {
		operation = s.st.Mutate
	}
	return operation(ctx, tenant, func(scope store.Scope) error {
		return s.module.WithOrchestrationWorkScope(ctx, scope, principal, tenant, mutate, fn)
	})
}
