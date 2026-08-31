// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// These three tests pin three SEPARATE properties of the two-person identity rule, so
// that breaking any one of them fails exactly one test. Each corresponds to one way the
// rule has actually been got wrong:
//
//	1. comparing the actor string instead of the person      (the defect)
//	2. comparing the user even when no user exists           (collapses distinct system tokens)
//	3. treating an identity-less party as a countable human  (a missing deny-closed floor)

// session is a human's browser session; token is a credential that human minted for
// themselves — SAME person, DIFFERENT actor strings. This is the shape that defeated
// the original check.
func session(user model.ID) Principal {
	return newPrincipal(KindUser, user, model.ID("sess-"+user.String()), false, "s", nil, nil)
}

func ownedToken(user, cred model.ID) Principal {
	return newPrincipal(KindToken, user, cred, false, "t", nil, nil)
}

// systemToken is a credential with NO person behind it (model.APIToken.UserID zero —
// "a standalone system token").
func systemToken(cred model.ID) Principal {
	return newPrincipal(KindToken, "", cred, false, "sys", nil, nil)
}

// PROPERTY 1 — the person is what counts. One human's two credentials are one human,
// even though their actor strings differ. Comparing Actor() answers this wrongly.
func TestSamePersonSeesOneAccountBehindTwoCredentials(t *testing.T) {
	alice := model.ID("alice")
	s, tok := session(alice), ownedToken(alice, model.ID("pat-1"))

	if s.Actor() == tok.Actor() {
		t.Fatalf("premise broken: both credentials render the same actor %q — nothing to distinguish", s.Actor())
	}
	if !SamePerson(s, tok) {
		t.Fatalf("a human's session (%s) and their own token (%s) are the SAME person", s.Actor(), tok.Actor())
	}
	// And two genuinely different humans stay different.
	if SamePerson(s, session(model.ID("bob"))) {
		t.Fatal("two different users must not be the same person")
	}
	// Including when the second human uses a token.
	if SamePerson(s, ownedToken(model.ID("bob"), model.ID("pat-2"))) {
		t.Fatal("bob's token is not alice")
	}
}

// PROPERTY 2 — when NO person exists, the user id must not be the comparison. Two
// distinct system tokens both carry an empty user; comparing users would collapse them
// into "the same party" and refuse a legitimate pairing. The actor is the only identity
// they have.
func TestSamePersonDoesNotCollapseDistinctIdentitylessCredentials(t *testing.T) {
	a, b := systemToken(model.ID("sys-1")), systemToken(model.ID("sys-2"))

	if a.UserID.String() != b.UserID.String() {
		t.Fatalf("premise broken: identity-less credentials should share the empty user, got %q and %q", a.UserID, b.UserID)
	}
	if SamePersonAs := SamePerson(a, b); SamePersonAs {
		t.Fatal("two DIFFERENT identity-less credentials are not the same party — comparing their (empty) user ids collapsed them")
	}
	// The same credential twice IS the same party — the fallback still has to work. What
	// it is NOT is the same PERSON: nothing here says how many humans hold that token, so
	// since the verdict names the credential and SamePerson (which only answers
	// "PersonSame") correctly declines to claim a person.
	if SamePerson(a, systemToken(model.ID("sys-1"))) {
		t.Fatal("a person-less credential must not be reported as the same PERSON — there is no person to report")
	}
	if got := PersonRefOf(a).Compare(PersonRefOf(systemToken(model.ID("sys-1")))); got != PersonSameCredential {
		t.Fatalf("the same identity-less credential must compare as PersonSameCredential, got %v", got)
	}
	// And that verdict must still be load-bearing for a gate: one party is one party.
	if ok, _ := TwoDistinctPeople(PersonRefOf(a), PersonRefOf(systemToken(model.ID("sys-1"))), AcceptWhenUndetermined); ok {
		t.Fatal("the same credential on both sides satisfied a two-person gate")
	}
	// Two empty actors must never collide into "same".
	if (PersonRef{}).SamePersonAs(PersonRef{}) {
		t.Fatal("two absent identities must not be reported as the same party")
	}
	// A person and an identity-less credential are not the same party.
	if SamePerson(session(model.ID("alice")), a) {
		t.Fatal("a system token is not alice")
	}
}

// PROPERTY 3 — the deny-closed floor. An identity-less party is not a countable human.
// Without this, it compares unequal to every real person and silently PASSES a
// distinct-approver check.
func TestStableRejectsAPartyWithNoAccountBehindIt(t *testing.T) {
	if PersonRefOf(systemToken(model.ID("sys-1"))).Stable() {
		t.Fatal("a credential with no user behind it must not count as a stable human identity")
	}
	if !PersonRefOf(session(model.ID("alice"))).Stable() {
		t.Fatal("a human session must count as a stable human identity")
	}
	if !PersonRefOf(ownedToken(model.ID("alice"), model.ID("pat-1"))).Stable() {
		t.Fatal("a token owned by a human carries that human's identity")
	}
}

// PersonRefOf must capture BOTH identities: comparing by person is impossible if the
// stored row kept only the actor, so this is a precondition of the rule, not audit
// decoration.
func TestPersonRefCapturesBothIdentities(t *testing.T) {
	ref := PersonRefOf(ownedToken(model.ID("alice"), model.ID("pat-1")))
	if ref.User != "alice" {
		t.Fatalf("PersonRef.User = %q, want the owning person", ref.User)
	}
	if ref.Actor != "token:pat-1" {
		t.Fatalf("PersonRef.Actor = %q, want the credential actor", ref.Actor)
	}
	sys := PersonRefOf(systemToken(model.ID("sys-1")))
	if sys.User != "" {
		t.Fatalf("an identity-less credential must yield an empty user, got %q", sys.User)
	}
	if sys.Actor != "token:sys-1" {
		t.Fatalf("PersonRef.Actor = %q, want the credential actor", sys.Actor)
	}
}
