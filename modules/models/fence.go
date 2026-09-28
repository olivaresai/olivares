// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A model-access rule whose subject is a user names the account it lets use, or
// keeps from using, a model, so its writers take the directory barrier: they read
// the named account's standing through the request's standing port and open
// their transaction by pinning that account's authority version with the
// tenant's directory epoch. An allow grants the account something, so it is a
// fenced write (auth.FencedWrite): an account being removed from the tenant, or
// erased, is refused with 409. A forbid only takes something away, so it is a
// restricting write (auth.RestrictingWrite): it takes the same barrier and is
// never refused for the account's standing, and the retirement keeps it.

// pdeclModelAccessSubject declares a model-access rule's subject reference: an
// account id when the subject kind is user. The write seam and the retirement
// step read this one declaration.
var pdeclModelAccessSubject = model.KindRef(colMASubjectKind, model.ClassAuthority)

// accessSubjects returns the account a validated rule names: the subject of a
// user-subject rule, when it is a canonical id. The fence skips an id that names
// no account.
func accessSubjects(d modelAccessDTO) []model.ID {
	if d.SubjectKind != subjectUser {
		return nil
	}
	id, err := model.ParseID(d.SubjectRef)
	if err != nil || id.IsZero() {
		return nil
	}
	return []model.ID{id}
}

// accessRestricts reports whether a validated rule only restricts its subject:
// a forbid.
func accessRestricts(d modelAccessDTO) bool { return normalizeEffect(d.Effect) == effectForbid }

// fencedWrite runs write in the request's transaction over the rule d names: the
// named account's authority version is pinned first, none when no subject names
// an account, and only an allow is refused for the account's standing.
func fencedWrite(r *http.Request, mc api.ModuleContext, d modelAccessDTO, write func(store.Scope) error) error {
	mutate := func(fn func(store.Scope) error) error { return mc.Data.Mutate(r.Context(), fn) }
	body := func(sc store.Scope, _ bool) error { return write(sc) }
	if accessRestricts(d) {
		return auth.RestrictingWrite(r.Context(), mc.Standing, mc.Tenant, accessSubjects(d), auth.FenceDirectory, mutate, body)
	}
	return auth.FencedWrite(r.Context(), mc.Standing, mc.Tenant, accessSubjects(d), auth.FenceDirectory, mutate, body)
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
