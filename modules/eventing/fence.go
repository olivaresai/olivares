// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A subscription's owner is a reference that lets an account act in the tenant,
// so the create is a fenced write: the owner's standing is read through the
// request's standing port and pinned with the tenant's directory fact as the
// transaction's first lock-bearing call, ahead of the egress writer proof and the
// row (auth.FencedWrite). No other writer changes the owner of a stored
// subscription.

// ownerSubjects returns the accounts a subscription created by p names: the
// account its owner actor ref names, read through the owner declaration's
// counted view, and the account p acts for, which a token's actor ref spells as
// the credential rather than the account.
func ownerSubjects(p auth.Principal) ([]model.ID, error) {
	ids, err := pdeclSubscriptionOwner.CountedUserIDs(model.Record{colSubOwnerActor: p.Actor()}, colSubOwnerActor)
	if err != nil {
		return nil, err
	}
	if !p.UserID.IsZero() {
		ids = append(ids, p.UserID)
	}
	return ids, nil
}

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
