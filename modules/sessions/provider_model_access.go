// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// ModelAccess answers whether the authenticated caller may run a model. An empty model is
// the question "may this caller run whatever its tool picks": a caller confined to named
// models may not. An error is a refusal, never an open answer.
type ModelAccess interface {
	MayRunModel(ctx context.Context, tenant model.TenantID, principal auth.Principal, modelRef string) (bool, error)
}

// ErrModelNotAssigned is the refusal of a launch or resume on a model the caller's person,
// role and groups were not assigned.
var ErrModelNotAssigned = forbiddenErr("this model is not assigned to you; choose one assigned to you or request access from a department admin or owner")

// errModelNotNamed is the refusal of a launch that left the model to its tool, for a caller
// confined to named models: nobody can tell whether the tool's own choice is one of them.
var errModelNotNamed = forbiddenErr("your models are assigned; choose a model for this session")

// askModelAccess is the caller's model question, bound to the authenticated principal.
func (m *Module) askModelAccess(ctx context.Context, tenant model.TenantID, principal auth.Principal) func(chosen string) error {
	return func(chosen string) error {
		if m.rt.ModelAccess == nil {
			return nil
		}
		ok, err := m.rt.ModelAccess.MayRunModel(ctx, tenant, principal, modelGrantRef(chosen))
		if err != nil {
			m.warnf("sessions: model assignment could not be read", "err", redactErr(err))
			return &runErr{http.StatusServiceUnavailable, "model assignment could not be read"}
		}
		switch {
		case ok:
			return nil
		case chosen == "":
			return errModelNotNamed
		default:
			return ErrModelNotAssigned
		}
	}
}

// modelGrantRef is the model a grant names: a launch's extended-context variant of a model
// ("claude-sonnet-5-5[1m]") runs that model.
func modelGrantRef(chosen string) string {
	if n := len(chosen) - len("[1m]"); n > 0 && strings.EqualFold(chosen[n:], "[1m]") {
		return chosen[:n]
	}
	return chosen
}
