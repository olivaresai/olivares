// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type brokenExpiryPageRepo struct {
	store.GenericRepo
}

func (brokenExpiryPageRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return []model.Record{{model.ColID: "one"}}, model.Page{HasMore: true}, nil
}

type brokenExpiryScope struct {
	store.Scope
	repo store.GenericRepo
}

func (s brokenExpiryScope) Ext(model.Kind) (store.GenericRepo, error) {
	return s.repo, nil
}

func TestAllExtRefusesBrokenPagination(t *testing.T) {
	_, err := allExt(context.Background(), brokenExpiryScope{repo: brokenExpiryPageRepo{}}, bindingKind)
	if !errors.Is(err, store.ErrPageContinuation) {
		t.Fatalf("allExt() error = %v, want ErrPageContinuation", err)
	}
}
