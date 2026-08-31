// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// session and token build the two credential shapes ONE human holds. The whole class of
// defect this file pins lives in the gap between them: same human, two actor strings.
func session(user model.ID) auth.Principal {
	return auth.Principal{Kind: auth.KindUser, UserID: user, CredID: model.NewID()}
}

func apiToken(owner, cred model.ID) auth.Principal {
	return auth.Principal{Kind: auth.KindToken, UserID: owner, CredID: cred}
}

// TestCompare_TheThirdCaseIsNamed is the reason the verdict is not a boolean.
//
// Each row states what the engine can actually KNOW, not what would be convenient. The
// rows that matter most are the undetermined ones: a boolean must render them false,
// and every caller in this estate reads false as "these are two different people".
func TestCompare_TheThirdCaseIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b auth.PersonRef
		want auth.PersonMatch
	}{
		{
			name: "one human, session and their own token — the reproduced self-approval",
			a:    auth.PersonRef{User: "u-1", Actor: "user:u-1"},
			b:    auth.PersonRef{User: "u-1", Actor: "token:t-9"},
			want: auth.PersonSame,
		},
		{
			name: "two accounts — distinct, and only ACCOUNTS (see the declared residue)",
			a:    auth.PersonRef{User: "u-1", Actor: "user:u-1"},
			b:    auth.PersonRef{User: "u-2", Actor: "user:u-2"},
			want: auth.PersonDistinct,
		},
		{
			name: "two DIFFERENT person-less credentials: unknown, NOT two people",
			a:    auth.PersonRef{Actor: "token:t-1"},
			b:    auth.PersonRef{Actor: "token:t-2"},
			want: auth.PersonUndetermined,
		},
		{
			// from the external contrast: this used to expect PersonSame, and the
			// name already flinched ("one party, knowably") because PersonSame was the
			// wrong word for it. Sharing a credential string does not say how many humans
			// hold that credential — the honest verdict names the CREDENTIAL, and the
			// gates that refuse it now refuse it for the true reason instead of accusing
			// somebody of self-approval.
			name: "the SAME person-less credential is one PARTY, and says so without claiming a person",
			a:    auth.PersonRef{Actor: "token:t-1"},
			b:    auth.PersonRef{Actor: "token:t-1"},
			want: auth.PersonSameCredential,
		},
		{
			name: "a person against a person-less credential is unknown, not distinct",
			a:    auth.PersonRef{User: "u-1", Actor: "user:u-1"},
			b:    auth.PersonRef{Actor: "token:t-1"},
			want: auth.PersonUndetermined,
		},
		{
			name: "two absent identities do not collide into one",
			a:    auth.PersonRef{},
			b:    auth.PersonRef{},
			want: auth.PersonUndetermined,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Fatalf("Compare = %v, want %v", got, tc.want)
			}
			// Identity is symmetric; a gate must not depend on argument order.
			if got := tc.b.Compare(tc.a); got != tc.want {
				t.Fatalf("Compare (reversed) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestZeroValues_FailClosed pins that a forgotten assignment cannot read as an answer.
func TestZeroValues_FailClosed(t *testing.T) {
	var m auth.PersonMatch
	if m != auth.PersonUndetermined {
		t.Fatalf("zero PersonMatch = %v, want PersonUndetermined", m)
	}
	var pol auth.UndeterminedPolicy
	if pol != auth.RefuseWhenUndetermined {
		t.Fatalf("zero UndeterminedPolicy = %v, want RefuseWhenUndetermined", pol)
	}
	ok, verdict := auth.TwoDistinctPeople(auth.PersonRef{Actor: "token:a"}, auth.PersonRef{Actor: "token:b"}, pol)
	if ok || verdict != auth.PersonUndetermined {
		t.Fatalf("zero policy admitted two unknown parties: ok=%v verdict=%v", ok, verdict)
	}
}

// TestTwoDistinctPeople_PolicyDecidesOnlyTheUnknownCase pins that the policy argument
// moves the undecidable case and NOTHING else. If AcceptWhenUndetermined could also
// admit two parties known to be the same human, the escape hatch would be the gate.
func TestTwoDistinctPeople_PolicyDecidesOnlyTheUnknownCase(t *testing.T) {
	same := [2]auth.PersonRef{{User: "u-1", Actor: "user:u-1"}, {User: "u-1", Actor: "token:t-9"}}
	distinct := [2]auth.PersonRef{{User: "u-1"}, {User: "u-2"}}
	unknown := [2]auth.PersonRef{{Actor: "token:t-1"}, {Actor: "token:t-2"}}

	for _, pol := range []auth.UndeterminedPolicy{auth.RefuseWhenUndetermined, auth.AcceptWhenUndetermined} {
		if ok, _ := auth.TwoDistinctPeople(same[0], same[1], pol); ok {
			t.Fatalf("policy %v admitted one human twice", pol)
		}
		if ok, _ := auth.TwoDistinctPeople(distinct[0], distinct[1], pol); !ok {
			t.Fatalf("policy %v refused two distinct people — a legitimate pair must still pass", pol)
		}
	}
	if ok, m := auth.TwoDistinctPeople(unknown[0], unknown[1], auth.RefuseWhenUndetermined); ok || m != auth.PersonUndetermined {
		t.Fatalf("RefuseWhenUndetermined: ok=%v verdict=%v", ok, m)
	}
	if ok, m := auth.TwoDistinctPeople(unknown[0], unknown[1], auth.AcceptWhenUndetermined); !ok || m != auth.PersonUndetermined {
		t.Fatalf("AcceptWhenUndetermined: ok=%v verdict=%v", ok, m)
	}
}

// TestPersonSameCredential_IsKnowledgeAndNoPolicyOverridesIt is the regression guard for
// the fix that produced it.
//
// The obvious way to stop calling a person-less credential "the same person" is to fold
// that case into PersonUndetermined. That would have opened a hole, and this test is the
// measurement of it: modules/governance/approvals.go decides separation of duty with
// AcceptWhenUndetermined, so under that policy ONE token presented as both the requester
// and the decider would have gone from refused to ACCEPTED.
//
// PersonSameCredential exists because "I know this is one party" is knowledge, not
// ignorance. A policy argument moves the case the engine cannot decide; it must not be
// able to move a case the engine decided.
func TestPersonSameCredential_IsKnowledgeAndNoPolicyOverridesIt(t *testing.T) {
	tok := auth.PersonRef{Actor: "token:t-1"}

	for _, pol := range []auth.UndeterminedPolicy{auth.RefuseWhenUndetermined, auth.AcceptWhenUndetermined} {
		ok, verdict := auth.TwoDistinctPeople(tok, tok, pol)
		if ok {
			t.Fatalf("policy %v admitted ONE credential as two parties — separation of duty is open", pol)
		}
		if verdict != auth.PersonSameCredential {
			t.Fatalf("policy %v: verdict = %v, want PersonSameCredential", pol, verdict)
		}
	}
	// It must be distinguishable from a self-approval, or the caller cannot phrase the
	// honest refusal — that IS the defect this fix closes.
	if auth.PersonSameCredential == auth.PersonSame {
		t.Fatal("PersonSameCredential must be its own value, not an alias of PersonSame")
	}
	if got, want := auth.PersonSameCredential.String(), "same-credential"; got != want {
		t.Fatalf("String() = %q, want %q — the operator reads this", got, want)
	}
	// Every verdict string is pinned, not just this one (contrast, H3): the two
	// account-verdict values were changed from "same-person"/"distinct-persons" precisely
	// because an operator reads them, and until now nothing would have gone red if they
	// drifted back to claiming people.
	for _, tc := range []struct {
		m    auth.PersonMatch
		want string
	}{
		{auth.PersonSame, "same-account"},
		{auth.PersonDistinct, "distinct-accounts"},
		{auth.PersonUndetermined, "undetermined"},
	} {
		if got := tc.m.String(); got != tc.want {
			t.Fatalf("String() = %q, want %q — the operator reads this", got, tc.want)
		}
		// PersonSame/PersonDistinct compare model.User.ID, an ACCOUNT. A verdict string
		// that says person/people/human would promise what the comparison cannot show.
		low := strings.ToLower(tc.m.String())
		for _, banned := range []string{"person", "people", "human"} {
			if strings.Contains(low, banned) {
				t.Fatalf("verdict %v renders %q, which promises %s where the unit is an account", tc.m, tc.m.String(), banned)
			}
		}
	}
	// And it must not become the zero value: a forgotten assignment still reads "I do not
	// know", never "one party".
	var zero auth.PersonMatch
	if zero != auth.PersonUndetermined {
		t.Fatalf("zero PersonMatch = %v, want PersonUndetermined", zero)
	}
}

// TestDistinctPeople_QuorumDoesNotInflate is the counting form of the same rule: a
// threshold is a comparison repeated, and it broke the same way.
func TestDistinctPeople_QuorumDoesNotInflate(t *testing.T) {
	for _, tc := range []struct {
		name              string
		refs              []auth.PersonRef
		wantD, wantUnknow int
	}{
		{
			name:  "one human's two credentials is ONE approver, not two",
			refs:  []auth.PersonRef{{User: "u-1", Actor: "user:u-1"}, {User: "u-1", Actor: "token:t-9"}},
			wantD: 1,
		},
		{
			name:  "two humans are two approvers",
			refs:  []auth.PersonRef{{User: "u-1"}, {User: "u-2"}},
			wantD: 2,
		},
		{
			name:       "three system tokens are ZERO approvers and three unknowns",
			refs:       []auth.PersonRef{{Actor: "token:a"}, {Actor: "token:b"}, {Actor: "token:c"}},
			wantUnknow: 3,
		},
		{
			name:       "one person plus system tokens does not reach a quorum of two",
			refs:       []auth.PersonRef{{User: "u-1"}, {Actor: "token:a"}, {Actor: "token:b"}},
			wantD:      1,
			wantUnknow: 2,
		},
		{
			name:       "the same person-less credential twice is ONE unknown",
			refs:       []auth.PersonRef{{Actor: "token:a"}, {Actor: "token:a"}},
			wantUnknow: 1,
		},
		{
			name:       "an empty ref is an unknown, never an approver",
			refs:       []auth.PersonRef{{User: "u-1"}, {}},
			wantD:      1,
			wantUnknow: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, u := auth.DistinctPeople(tc.refs)
			if d != tc.wantD || u != tc.wantUnknow {
				t.Fatalf("DistinctPeople = (%d distinct, %d undetermined), want (%d, %d)", d, u, tc.wantD, tc.wantUnknow)
			}
		})
	}
}

// TestPersonRefOf_CapturesWhatSurvivesStorage pins the bridge from a live principal to
// the stored form, including the person-less token whose User must stay empty.
func TestPersonRefOf_CapturesWhatSurvivesStorage(t *testing.T) {
	user := model.NewID()
	s, tk := session(user), apiToken(user, model.NewID())

	if ref := auth.PersonRefOf(s); ref.User != user.String() || ref.Actor != s.Actor() {
		t.Fatalf("session ref = %+v", ref)
	}
	if ref := auth.PersonRefOf(tk); ref.User != user.String() || ref.Actor != tk.Actor() {
		t.Fatalf("token ref = %+v", ref)
	}
	// The reproduced bypass, at the level of the primitive: two actor strings, one human.
	if s.Actor() == tk.Actor() {
		t.Fatal("fixture is inert: the session and the token must render different actors")
	}
	if !auth.SamePerson(s, tk) {
		t.Fatal("a session and its owner's token must be the same person")
	}
	// A standalone system token: model.APIToken.UserID is documented as zero.
	sys := auth.Principal{Kind: auth.KindToken, CredID: model.NewID()}
	if ref := auth.PersonRefOf(sys); ref.Stable() {
		t.Fatalf("a person-less token must not be Stable: %+v", ref)
	}
	if auth.PersonRefOfUser("").Stable() {
		t.Fatal("an empty stored user id must not be Stable")
	}
}

// TestSamePersonAs_IsExactlyTheSameVerdict pins the compatibility wrapper against the
// verdict, so the boolean callers that exist cannot drift from the three-valued rule.
func TestSamePersonAs_IsExactlyTheSameVerdict(t *testing.T) {
	refs := []auth.PersonRef{
		{}, {User: "u-1"}, {User: "u-2"}, {Actor: "token:a"}, {Actor: "token:b"},
		{User: "u-1", Actor: "user:u-1"}, {User: "u-1", Actor: "token:a"},
	}
	for _, a := range refs {
		for _, b := range refs {
			if got, want := a.SamePersonAs(b), a.Compare(b) == auth.PersonSame; got != want {
				t.Fatalf("SamePersonAs(%+v, %+v) = %v, Compare = %v", a, b, got, a.Compare(b))
			}
		}
	}
}

// TestPersonRefFromActor_LegacyRowsAreRecoveredNotInvented pins the legacy bridge in both
// directions. A session actor CONTAINS the user id by the definition of Actor(); a token
// actor does not, and must stay undetermined rather than acquire a person.
func TestPersonRefFromActor_LegacyRowsAreRecoveredNotInvented(t *testing.T) {
	sess := auth.PersonRefFromActor("user:u-1")
	if sess.User != "u-1" || sess.Actor != "user:u-1" {
		t.Fatalf("session actor = %+v", sess)
	}
	for _, actor := range []string{"token:t-1", "", "user:", "svc:thing", "USER:u-1"} {
		if ref := auth.PersonRefFromActor(actor); ref.Stable() {
			t.Fatalf("actor %q must not yield a person, got %+v", actor, ref)
		}
	}
	// The point of the bridge: a legacy session-proposed row can still be decided by a
	// DIFFERENT person, and can still not be self-approved by that person's own token.
	if ok, m := auth.TwoDistinctPeople(sess, auth.PersonRef{User: "u-2", Actor: "user:u-2"}, auth.RefuseWhenUndetermined); !ok || m != auth.PersonDistinct {
		t.Fatalf("legacy row vs a second person: ok=%v verdict=%v", ok, m)
	}
	if ok, m := auth.TwoDistinctPeople(sess, auth.PersonRef{User: "u-1", Actor: "token:t-9"}, auth.RefuseWhenUndetermined); ok || m != auth.PersonSame {
		t.Fatalf("legacy row vs its own owner's token: ok=%v verdict=%v", ok, m)
	}
	// A legacy TOKEN-proposed row is refused, not silently counted as a second human.
	if ok, m := auth.TwoDistinctPeople(auth.PersonRefFromActor("token:t-1"), auth.PersonRef{User: "u-2"}, auth.RefuseWhenUndetermined); ok || m != auth.PersonUndetermined {
		t.Fatalf("legacy token-proposed row: ok=%v verdict=%v", ok, m)
	}
}
