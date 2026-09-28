// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A binding of the user tree lets the account it names reach its source, so every
// writer of one is fenced: it reads the named account's standing through the
// request's standing port and opens its transaction by pinning that account's
// authority version with the tenant's directory epoch (auth.FencedWrite). An
// account being removed from the tenant, or erased, is refused with 409.

// pdeclBindingScopeRef declares a binding's scope reference: an account id when
// the scope tree is the user tree. The write seam and the retirement step read
// this one declaration.
var pdeclBindingScopeRef = model.KindRef(colScopeTree, model.ClassAuthority)

// bindingSubjects returns the account a binding names: the reference of a
// user-tree binding, when it is a canonical id. The fence skips an id that names
// no account.
func bindingSubjects(b bindingDTO) []model.ID {
	if strings.ToLower(strings.TrimSpace(b.ScopeTree)) != scopeUser {
		return nil
	}
	id, err := model.ParseID(strings.TrimSpace(b.ScopeRef))
	if err != nil || id.IsZero() {
		return nil
	}
	return []model.ID{id}
}

// proposalSubjects returns the account an approval of the posture request id
// would bind: the one its proposed binding names. A stored proposal never
// changes, so it is read before the fenced transaction. A rejection binds
// nothing, and neither does a request that creates or updates no binding.
func proposalSubjects(ctx context.Context, mc api.ModuleContext, id model.ID, approve bool) ([]model.ID, error) {
	if !approve {
		return nil, nil
	}
	var out []model.ID
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(postureRequestKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if op := rec.String(colPROp); op != postureOpCreate && op != postureOpUpdate {
			return nil
		}
		var in bindingDTO
		if json.Unmarshal([]byte(rec.String(colPRProposed)), &in) == nil {
			out = bindingSubjects(in)
		}
		return nil
	})
	return out, err
}

// fencedWrite runs write in the request's transaction as a fenced write over
// subjects: the named accounts' authority versions are pinned first, and none
// is pinned when no subject names an account.
func fencedWrite(r *http.Request, mc api.ModuleContext, subjects []model.ID, write func(store.Scope) error) error {
	mutate := func(fn func(store.Scope) error) error { return mc.Data.Mutate(r.Context(), fn) }
	return auth.FencedWrite(r.Context(), mc.Standing, mc.Tenant, subjects, auth.FenceDirectory, mutate,
		func(sc store.Scope, _ bool) error { return write(sc) })
}

// writeFenceRefusal answers a fence refusal with 409 and its stable code, and
// reports whether err was one.
func writeFenceRefusal(w http.ResponseWriter, err error) bool {
	code, ok := auth.FenceRefusalCode(err)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
	return true
}
