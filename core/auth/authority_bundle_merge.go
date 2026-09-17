// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// maxMergedUserAuthorities is the User reference budget of each supplied bundle
// and of the merged result. It is separate from the 1..64 tenant fact budget.
const maxMergedUserAuthorities = 64

// MergeAuthoritySnapshotBundles (AM1) combines two current-authority bundles into
// one that loses no evidence from either.
//
// ⛔ IT IS A PURE VALUE OPERATION AND NOTHING MORE. It reads no store, clock,
// context, credential or principal seal; it is not an ALLOW decision, it does not
// establish freshness, and it does not pin anything. Every input must still come
// from AuthorizeRouteMutation/AuthorityFor, be rechecked for freshness, and the
// merged result must still be locked through the proper barrier on the mutation's
// own transaction.
//
// ⛔ IT DEDUPLICATES ONLY IDENTICAL EVIDENCE AND REFUSES EVERYTHING ELSE. A tenant
// fact's identity is its complete (Kind, ID) pair; two observations of it collapse
// only when their Version and complete lease witness (subject, fence and canonical
// deadline) agree. A leased and an unleased observation of one fact conflict, and
// so do two leases differing in any witness field even when the row versions agree.
// A User reference collapses only when its version agrees. Nothing is stripped,
// rebuilt from partial data, upgraded to a newer version or taken from one input in
// preference to the other, so the result does not depend on argument order.
//
// Each input must supply 1..64 tenant facts and 0..64 User references; the result
// holds at most 64 distinct facts and 64 distinct Users. An oversized input is
// refused before any element is read. Every refusal is ErrRouteUndecided with an
// empty bundle.
//
// The result is freshly allocated: facts ordered by Kind then canonical ID, Users
// by canonical ID, and no User slice when neither input supplied one. That order
// is for determinism only — it is not the store's lock order, which the bundle
// locker still owns and recomputes. Descriptor admission, lease-subject rules and
// actual row authority also remain the store's to validate. The caller must not
// mutate either input concurrently with this call; later mutation of an input or
// of the result affects neither the other nor any later call.
func MergeAuthoritySnapshotBundles(
	a, b store.AuthoritySnapshotBundle,
) (store.AuthoritySnapshotBundle, error) {
	for _, in := range [...]store.AuthoritySnapshotBundle{a, b} {
		if len(in.Facts) == 0 || len(in.Facts) > maxEvidenceFacts {
			return refuseAuthorityBundleMerge("each bundle must supply 1..64 tenant facts")
		}
		if len(in.UserAuthorities) > maxMergedUserAuthorities {
			return refuseAuthorityBundleMerge("each bundle may supply at most 64 User references")
		}
	}

	facts := make(map[evidenceFactKey]store.AuthorizationFactRef, len(a.Facts)+len(b.Facts))
	for _, group := range [...][]store.AuthorizationFactRef{a.Facts, b.Facts} {
		for _, fact := range group {
			if !validMergedAuthorityFact(fact) {
				return refuseAuthorityBundleMerge("malformed tenant fact")
			}
			key := evidenceFactKey{kind: fact.Kind, id: fact.ID}
			if prior, exists := facts[key]; exists {
				if prior.Version != fact.Version || evidenceLeaseOf(prior) != evidenceLeaseOf(fact) {
					return refuseAuthorityBundleMerge("contradictory tenant fact observations")
				}
				continue
			}
			if len(facts) == maxEvidenceFacts {
				return refuseAuthorityBundleMerge("merged bundle exceeds 64 tenant facts")
			}
			facts[key] = fact
		}
	}

	users := make(map[model.ID]int64, len(a.UserAuthorities)+len(b.UserAuthorities))
	for _, group := range [...][]store.UserAuthorityFactRef{a.UserAuthorities, b.UserAuthorities} {
		for _, ref := range group {
			if !validMergedUserAuthority(ref) {
				return refuseAuthorityBundleMerge("malformed User reference")
			}
			if version, exists := users[ref.UserID]; exists {
				if version != ref.Version {
					return refuseAuthorityBundleMerge("conflicting User reference versions")
				}
				continue
			}
			if len(users) == maxMergedUserAuthorities {
				return refuseAuthorityBundleMerge("merged bundle exceeds 64 User references")
			}
			users[ref.UserID] = ref.Version
		}
	}

	out := store.AuthoritySnapshotBundle{
		Facts: make([]store.AuthorizationFactRef, 0, len(facts)),
	}
	for _, fact := range facts {
		out.Facts = append(out.Facts, fact)
	}
	sort.Slice(out.Facts, func(i, j int) bool {
		if out.Facts[i].Kind != out.Facts[j].Kind {
			return out.Facts[i].Kind < out.Facts[j].Kind
		}
		return out.Facts[i].ID.String() < out.Facts[j].ID.String()
	})
	if len(users) > 0 {
		out.UserAuthorities = make([]store.UserAuthorityFactRef, 0, len(users))
		for id, version := range users {
			out.UserAuthorities = append(out.UserAuthorities,
				store.UserAuthorityFactRef{UserID: id, Version: version})
		}
		sort.Slice(out.UserAuthorities, func(i, j int) bool {
			return out.UserAuthorities[i].UserID.String() < out.UserAuthorities[j].UserID.String()
		})
	}
	return out, nil
}

func refuseAuthorityBundleMerge(reason string) (store.AuthoritySnapshotBundle, error) {
	return store.AuthoritySnapshotBundle{}, fmt.Errorf(
		"%w: authority bundle merge: %s", ErrRouteUndecided, reason)
}

// validMergedAuthorityFact is the route-evidence fact grammar plus an exact witness
// shape: an unleased fact carries nothing beyond its coordinates, and a leased one
// is exactly what the store's typed constructor yields for its own witness. The
// rebuilt value is only compared, never returned.
func validMergedAuthorityFact(fact store.AuthorizationFactRef) bool {
	if !validEvidenceFact(fact) {
		return false
	}
	subject, fence, deadline, leased := fact.LeaseFenceWitness()
	if !leased {
		return fact == store.AuthorizationFactRef{Kind: fact.Kind, ID: fact.ID, Version: fact.Version}
	}
	rebuilt, err := store.NewLeaseFenceAuthorizationFactRef(
		fact.Kind, fact.ID, fact.Version, subject, fence, deadline)
	return err == nil && rebuilt == fact
}

// validMergedUserAuthority applies the store's own User fence grammar and requires
// the canonical ID text the result is ordered by.
func validMergedUserAuthority(ref store.UserAuthorityFactRef) bool {
	if err := (model.UserAuthority{BaseFields: model.BaseFields{
		ID: ref.UserID, TenantID: model.SystemTenantID, Version: ref.Version,
	}}).Validate(); err != nil {
		return false
	}
	parsed, err := model.ParseID(ref.UserID.String())
	return err == nil && parsed == ref.UserID
}
