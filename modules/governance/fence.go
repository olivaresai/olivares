// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Governance's fenced writers store references that let an account act in the
// tenant: a user-subject grant, a Cedar permit naming an account or one of its
// credentials, a managed document naming an account, an agent's human sponsor or
// owner. Each reads the
// named accounts' standing through the request's standing port and pins them with
// the tenant's fact before it writes (auth.FencedWrite, auth.PinFence).

// policySubjects returns the accounts a policy document names, for its fenced
// writer: every User or Principal literal, any other canonical id, and every
// email address in it, each resolved through the standing port to the account it
// names (a credential id to the credential's account). Values that name no
// account are left out, so they never count against the fence's bound. A Cedar
// permit and a managed document are both untyped here: the retirement step
// matches the same aliases, so what the writer fences and what the step finds
// are one set.
func policySubjects(ctx context.Context, r auth.StandingReader, content string) ([]model.ID, error) {
	return auth.ResolveContentAccounts(ctx, r, content)
}

var _ auth.StandingConsumer = (*Module)(nil)

// UseStanding implements auth.StandingConsumer: it binds the standing port of
// the writers that run outside a request, the background roster sync.
func (m *Module) UseStanding(r auth.StandingReader) { m.standing = r }

// writeFenceRefusal answers a fence refusal with 409 and its stable code, and
// reports whether err was one.
func writeFenceRefusal(w http.ResponseWriter, err error) bool {
	code, ok := auth.FenceRefusalCode(err)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusConflict, errorBodyCode(code, err.Error()))
	return true
}
