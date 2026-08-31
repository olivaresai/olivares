// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// The two-person rule over its WHOLE input space, including the states no HTTP fixture can
// reach (a user-less decider is not constructible through the API today).
//
// WHY THIS FILE WAS REWRITTEN AT INTEGRATION, and it is not cosmetic. It used to call a
// module-local `dualControlVerdict`. PR #615 replaced that helper's only call site
// with the shared primitive (`auth.PersonRefFromActor` + `auth.TwoDistinctPeople`), and the
// two lanes had fixed the same defect independently. Merging both left the helper with ZERO
// production callers while this table kept passing — eleven green cases measuring code the
// product no longer runs, which is the failure this repository keeps removing: a test that
// cannot go red is not evidence. The cases are preserved and now drive the path the route
// actually takes, so the coverage did not shrink, it moved onto the live code.
//
// The expectations are reproduced EXACTLY, with ONE deliberate change, marked below: the
// case pinned as an unclosable limit is now closed by the shared primitive.
func TestDualControlTwoPersonRule(t *testing.T) {
	const (
		alice     = "user:U-alice"
		aliceTok  = "token:C-alice" // Alice's own API token: a DIFFERENT actor string, same human
		bob       = "user:U-bob"
		uAlice    = "U-alice"
		uBob      = "U-bob"
		svcActor  = "token:C-service"
		svcActor2 = "token:C-service-2"
	)
	cases := []struct {
		name                                           string
		proposerActor, proposerUser, decActor, decUser string
		allow                                          bool
	}{
		// THE P0. One human, two credentials. Actor() differs; the human does not.
		{"alice proposes with her session, approves with her own token", alice, uAlice, aliceTok, uAlice, false},
		{"alice proposes with her token, approves with her session", aliceTok, uAlice, alice, uAlice, false},
		{"alice proposes and approves with the same session", alice, uAlice, alice, uAlice, false},

		// The legitimate path must stay open, or the control is just an outage.
		{"two distinct humans", alice, uAlice, bob, uBob, true},
		{"bob's TOKEN is a valid second person", alice, uAlice, "token:C-bob", uBob, true},

		// A decider with no human behind it can never be the second person.
		{"user-less decider is refused outright", alice, uAlice, svcActor, "", false},
		{"user-less decider refused even against a user-less proposer", svcActor, "", svcActor2, "", false},

		// Rows with no stored user: written before the column existed, or proposed by a
		// user-less principal. The actor strings are all such a row supports.
		{"legacy row, same actor", alice, "", alice, uAlice, false},
		{"legacy row, different actor", alice, "", bob, uBob, true},
		{"service-token proposal approved by a human", svcActor, "", bob, uBob, false},

		// THE ONE EXPECTATION THAT CHANGED, and it changed for the better. Pinned this
		// as a documented hole: a LEGACY row proposed by alice's session and approved by
		// alice's TOKEN could not be caught, "the row never recorded who she was". The shared
		// primitive DOES catch it, because a session actor literally contains the user id
		// (`user:` + UserID, core/auth/principal.go:126-131) and PersonRefFromActor reads it
		// back. The old helper compared the raw actor strings in that fallback and saw two.
		{"legacy row DOES catch alice's own token (the documented limit, now closed)", alice, "", aliceTok, uAlice, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Exactly what the route builds before it decides (posture.go, decide path).
			proposer := auth.PersonRefFromActor(c.proposerActor)
			if c.proposerUser != "" {
				proposer.User = c.proposerUser
			}
			decider := auth.PersonRef{Actor: c.decActor, User: c.decUser}

			got, verdict := auth.TwoDistinctPeople(proposer, decider, auth.RefuseWhenUndetermined)
			if got != c.allow {
				t.Fatalf("allowed = %v, want %v (verdict %v)", got, c.allow, verdict)
			}
		})
	}
}

// A service-token proposal approved by a human is REFUSED, and the case above says so — a
// reversal of what allowed, so it gets its own reason rather than a row in a table.
//
// Let it through: with no stored user it fell back to comparing actor strings, and
// "token:C-service" != "user:U-bob" reads as two parties. It is not two PEOPLE. Nobody
// stands behind the proposal, so there is no first human for bob to be the second of, and
// approving it would be one human authorizing a change alone. RefuseWhenUndetermined is the
// rule the governance quorum already writes (modules/governance/approvals.go): an
// unattributable party may STOP an action, never authorize one.
func TestServiceTokenProposalIsNotAFirstPerson(t *testing.T) {
	proposer := auth.PersonRefFromActor("token:C-service")
	if proposer.Stable() {
		t.Fatalf("a token actor must not yield a person: %+v", proposer)
	}
	ok, verdict := auth.TwoDistinctPeople(proposer, auth.PersonRef{Actor: "user:U-bob", User: "U-bob"}, auth.RefuseWhenUndetermined)
	if ok {
		t.Fatalf("a person-less proposer must not be counted as the first human (verdict %v)", verdict)
	}
}
