// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "strings"

// Two-person identity, three-valued since the claim was corrected.
//
// ⭐ WHAT THIS FILE DECIDES, AND WHAT IT DOES NOT — READ THIS BEFORE QUOTING IT
//
// The unit of comparison is model.User.ID: a user ACCOUNT. The model says so in its own
// words — "Status is the account lifecycle state" (core/model/auth.go:33), "PasswordHash
// … for an SSO-only account" (:35), "ExternalID is the … identifier for this account"
// (:42). PersonSame and PersonDistinct are therefore statements about ACCOUNTS, and the
// gap to "humans" runs in BOTH directions and is not closable today:
//
//   - ONE human, TWO accounts → reads as PersonDistinct. A superadmin creates the second
//     account AND picks its password in one call (POST /v1/users, gated on
//     authzSystem(…, "user:write") alone, core/api/handlers_core.go:256-273; authzSystem
//     never requires step-up, middleware.go:307-318), logs in and satisfies the gate.
//     Nothing in the schema contradicts it: Email is unique per ACCOUNT
//     (core/model/auth.go:28-30), ExternalID is the IdP's id for THAT account (:42-47),
//     and EmployeeNumber (:56) is written and read by SCIM but NEVER compared and NEVER
//     unique. No canonical-person record exists; deriving one would manufacture an
//     answer rather than measure it.
//   - TWO humans, ONE account → reads as PersonSame. Colleagues sharing an account or a
//     bearer token are refused as one party.
//
// A human/NHI CLASSIFICATION does exist elsewhere — connectors/identitysource
// PrincipalHuman/PrincipalNHI, already gated in modules/governance/identity.go, which
// refuses to bind an agent to a human identity. It does NOT raise this floor: it labels
// an account as interactive ("a human user (an interactive account)"), and nothing links
// two accounts to one natural person. external contrast caught the earlier
// absolute claim that no such source existed at all — it exists; it just cannot dedupe
// people.
//
// AND THE OTHER DIRECTION, WHICH IS EASY TO GET WRONG (contrast, H2).
// An empty User means THIS REFERENCE CARRIES NO ACCOUNT ATTRIBUTION — never that no
// account exists behind the credential. PersonRefFromActor("token:<id>") returns an empty
// User because the actor string does not encode its owner, not because the token is
// ownerless. So PersonUndetermined and PersonSameCredential are statements about what is
// KNOWABLE here; only PersonSame and PersonDistinct assert anything about accounts. Copy
// that says "no account stands behind it" overclaims in the opposite direction, and that
// is the mirror of the defect this file exists to stop.
//
// This residue was already written on Compare, where it governed one method. It is here
// now because it governs the FILE: until the residue said "customer-facing copy
// must promise distinct ACCOUNTS" while PersonSame's own doc, 145 lines above it, told
// callers they could quote it to an operator as "you are approving your own request
// without lying" — and core/api/dr_handler.go did exactly that. **A limit stated on one
// method does not bind the package.**
//
// ⇒ Anything quoted to an operator, logged, or exported must say ACCOUNT. The wrappers
// keep their Person* names for now (renaming the whole vocabulary — PersonMatch,
// PersonSame, PersonDistinct, TwoDistinctPeople, DistinctPeople, PersonRef* — is one
// atomic move, deliberately not mixed into a claim correction); NOTHING below depends on
// those names being read as "human", because every doc, verdict string and error text
// now says account.
//
// Every two-person control in this estate — dual-control restore, posture relaxation,
// break-glass post-review, kill-switch re-enable, approval separation of duty — has to
// answer ONE question: are these two parties DISTINCT? Answering it with Actor() is
// wrong, and it was wrong THREE times independently (core/api/dr_handler.go,
// modules/sourcescope/posture.go, and the decision trail every remote quorum consumer
// reads): Actor() renders "user:<UserID>" for a session and "token:<CredID>" for a
// token, so a single ACCOUNT holding a session AND a token minted for itself produced
// two different strings and satisfied a gate whose entire purpose is to require two
// parties.
//
// The rule the correct implementations already encode (modules/governance/approvals.go,
// killswitch.go, breakglass.go) is: compare the stable USER; a credential with no user
// behind it cannot be counted as one of the two. This file is that rule, once, as pure
// identity — it knows nothing about restores, posture requests or approvals, and it
// makes no policy decision. Callers decide what to DO (which status, which message);
// this only says who is who.
//
// WHY THE VERDICT HAS THREE VALUES AND NOT TWO. A boolean has to lie in the case
// that actually exists. When no account stands behind a party there is nothing to
// compare, and "false" — the only answer a boolean can give — reads at every call site
// as "these are two different parties", which is precisely how an identity-less party
// silently SATISFIES a distinct-approver check. Two calls (a Stable() floor, then a
// comparison) express it correctly but let a caller forget the floor and get the same
// silent pass. PersonMatch names the third case so it cannot be skipped, and
// TwoDistinctPeople makes the caller state, in the call itself, what it does about it.

// PersonMatch is the verdict of comparing two parties in a two-person control.
//
// The zero value is PersonUndetermined on purpose: a caller that forgets to assign a
// verdict, or a struct field left unset, reads as "I do not know", never as an answer.
type PersonMatch uint8

const (
	// PersonUndetermined means the engine cannot attribute one or both parties to an
	// account, so it can neither confirm nor deny that they are the same party. It is
	// NOT "different parties": treating it as such is the original defect. What the
	// caller does about it is the caller's policy, not this package's.
	PersonUndetermined PersonMatch = iota
	// PersonSame means both parties resolve to ONE STABLE ACCOUNT. It is an assertion
	// about an ACCOUNT, never about a human: two colleagues sharing an account or a
	// bearer token also land here and are refused as one party.
	//
	// ⛔ Its previous doc said this "is an assertion about a human … a caller may quote
	// it back to the operator as 'you are approving your own request' without lying",
	// and core/api/dr_handler.go took the invitation. A caller may quote this as
	// "the same ACCOUNT requested and approved"; anything stronger is unverifiable here.
	PersonSame
	// PersonDistinct means the two parties are two different stable ACCOUNTS — which is
	// the most this engine can establish, not two different humans. See the file header:
	// two accounts held by one human read as distinct, because this product stores
	// nothing that links them, and one of them is two HTTP calls away.
	PersonDistinct
	// PersonSameCredential means both parties are literally the SAME credential, with
	// account attribution MISSING ON AT LEAST ONE SIDE. It is knowledge — one party,
	// whoever is holding it — but it is NOT a statement about accounts.
	//
	// Read the guard before quoting this (contrast, H2): Compare reaches here
	// whenever the actors match and EITHER side lacks User, so one side may carry a
	// perfectly good account. {User:"u-1", Actor:"token:t"} vs {User:"", Actor:"token:t"}
	// lands here. The honest phrasing is "same credential; account attribution is
	// unavailable on at least one side" — NOT "no account stands behind it".
	//
	// It exists (from the external contrast on PR #615) because the alternatives
	// were both wrong. Calling it PersonSame, as this file did, made two gates accuse an
	// operator of SELF-APPROVAL for an act no human was attributed to: it swapped a
	// verifiable truth ("I cannot attribute persons") for an unverifiable one ("the same
	// person"). Folding it into PersonUndetermined instead would have been worse than
	// cosmetic — separation of duty in modules/governance/approvals.go runs with
	// AcceptWhenUndetermined, so ONE token on both sides would have gone from refused to
	// ACCEPTED. Naming the case keeps the refusal and tells the truth about why.
	//
	// It is appended, not inserted, so PersonUndetermined stays the zero value and the
	// numeric values of the other three do not move.
	PersonSameCredential
)

// String renders the verdict for messages and logs.
func (m PersonMatch) String() string {
	switch m {
	case PersonSame:
		return "same-account"
	case PersonDistinct:
		return "distinct-accounts"
	case PersonSameCredential:
		return "same-credential"
	default:
		return "undetermined"
	}
}

// UndeterminedPolicy is what a caller does when the verdict is PersonUndetermined.
//
// It is a REQUIRED argument of TwoDistinctPeople rather than a default, because the
// right answer differs per gate and the wrong one is invisible. A dual-control restore
// must refuse (there may be one party behind both sides); a control whose second party
// is a machine step by design may accept. The zero value refuses, so an unassigned
// policy fails closed.
type UndeterminedPolicy uint8

const (
	// RefuseWhenUndetermined denies the two-party check when attribution is unknown.
	// This is the deny-closed choice and the right one for any gate whose promise to
	// the operator is "two separate administrators".
	RefuseWhenUndetermined UndeterminedPolicy = iota
	// AcceptWhenUndetermined passes the check when attribution is unknown. A caller
	// picking this is asserting that its second party need not be an attributable
	// account, and owes that reason in a comment at the call site.
	AcceptWhenUndetermined
)

// PersonRef is a party's identity in the form that SURVIVES STORAGE. It carries the
// stable user ACCOUNT behind the credential, plus the audit actor string — it is not a
// person reference and this product has no person id (file header).
//
// Both fields exist because a two-person control needs both and they answer different
// questions. User is WHICH ACCOUNT ACTED and is the only sound basis for "is this the
// same party"; Actor is WHICH CREDENTIAL was used and is what the audit trail must
// show. A row that stores only Actor cannot be compared by account at all — the
// comparison has nothing to read — which is why adopting this rule means storing User
// too, not merely changing an `==`.
//
// User is empty when THIS REFERENCE HAS NO ACCOUNT ATTRIBUTION. That covers two
// different facts and the difference matters: a live principal with model.APIToken.UserID
// zero genuinely has no owner ("a standalone system token", core/model/auth.go:251-253),
// but a reference rebuilt from a legacy token actor (PersonRefFromActor) is empty because
// the actor string never carried the owner. Empty is not an error here: it is a fact the
// caller must decide about.
type PersonRef struct {
	// User is the stable user-ACCOUNT id ("" when this reference carries no account
	// attribution — see above; that is not the same as "no account exists"). It is
	// model.User.ID; there is no person id to hold.
	User string
	// Actor is the audit-actor string ("user:<id>" / "token:<id>").
	Actor string
}

// PersonRefOf captures a live principal's identity in storable form. Store BOTH fields
// next to whatever the party did; compare later with Compare or SamePersonAs.
func PersonRefOf(p Principal) PersonRef {
	ref := PersonRef{Actor: p.Actor()}
	if !p.UserID.IsZero() {
		ref.User = p.UserID.String()
	}
	return ref
}

// PersonRefFromActor recovers what a LEGACY row can still tell us: one written before its
// table carried the account, holding only the actor string.
//
// This is not inference and not a fallback rule. Actor() is defined as "user:" + UserID
// for a session (principal.go:126-131), so for a session actor the account id is
// LITERALLY IN the stored string; for a token actor ("token:" + CredID) it is not there
// at all and this returns no user, which leaves the row PersonUndetermined and refused by
// any gate whose promise is two parties. That asymmetry is the point: it un-breaks the
// in-flight requests we can still attribute without inventing an answer for the ones we
// cannot.
//
// New writes must store the account (PersonRefOf). This exists so adopting that rule does
// not strand rows an operator already created.
func PersonRefFromActor(actor string) PersonRef {
	ref := PersonRef{Actor: actor}
	if u, ok := strings.CutPrefix(actor, "user:"); ok && u != "" {
		ref.User = u
	}
	return ref
}

// PersonRefOfUser builds a reference from a STORED stable user id, for rows that
// recorded the account without the actor string (the decision trail's decider_user
// column, and every quorum counted from it). An empty id is a party with NO ACCOUNT
// ATTRIBUTION and stays undetermined — it does not become a distinct approver by being
// empty, and it is not evidence that no account exists.
func PersonRefOfUser(userID string) PersonRef { return PersonRef{User: userID} }

// Stable reports whether this reference CARRIES A STABLE ACCOUNT ATTRIBUTION — the floor
// a two-person control needs before it can count this party as one of its two. It is the
// killswitch.go rule ("a stable user identity is required to review; a system token
// cannot") expressed once. False means "I cannot attribute this party", which is the
// deny-closed answer; it does not assert that no account exists (file header).
//
// Prefer Compare/TwoDistinctPeople: they carry this floor INSIDE the verdict, so it
// cannot be skipped. Stable remains for the callers that must phrase their own refusal
// for a single party before there is a second one to compare against.
func (r PersonRef) Stable() bool { return r.User != "" }

// Compare returns the three-valued verdict for two parties.
//
// When BOTH parties have a stable user, that is the comparison — this is the whole
// point: one account's session and one account's token carry the same user and different
// actor strings, so only the user answers correctly.
//
// When either party has no user there is no account to compare, with ONE exception that
// is knowledge rather than inference: two references to the SAME non-empty credential
// are the same PARTY whoever is holding it, so that is PersonSameCredential — one party,
// and explicitly NOT a claim about an account, because none is attributable. Everything
// else in that region is PersonUndetermined — including two different account-less
// credentials, which the old boolean reported as "not the same" and which every caller
// then counted as two approvers. Two empty actors never collide: an absent identity is
// not an identity.
//
// The residue that used to live here — two stable user ids are two ACCOUNTS, not
// provably two humans — is now in the FILE HEADER, with its measurement. It was moved
// because a limit stated on one method did not bind the package: PersonSame's own doc
// contradicted it and an operator-facing error text followed PersonSame.
func (r PersonRef) Compare(o PersonRef) PersonMatch {
	if r.User != "" && o.User != "" {
		if r.User == o.User {
			return PersonSame
		}
		return PersonDistinct
	}
	if r.Actor != "" && r.Actor == o.Actor {
		return PersonSameCredential
	}
	return PersonUndetermined
}

// TwoDistinctPeople reports whether these two parties satisfy a two-person control, and
// returns the verdict that decided it so the caller can phrase its own message.
//
// "Distinct people" is the name of the CONTROL, not a property this proves. A true
// return means two distinct ACCOUNTS **only when the verdict is PersonDistinct**; under
// AcceptWhenUndetermined it can also be true for PersonUndetermined, where nothing was
// compared at all (contrast, H2). That is why the verdict is returned alongside the
// bool: a caller phrasing an operator message must read it, and must say account.
//
// The policy argument is not a convenience: it is how the undecidable case stays
// DECIDABLE BY THE CALLER. There is no default, so no gate can inherit another gate's
// answer to a question only it can answer.
//
// The policy moves EXACTLY ONE case — PersonUndetermined, the one the engine cannot
// decide. PersonSameCredential is decided: it is one party, so it refuses under every
// policy. A policy argument that could overturn knowledge would be the escape hatch this
// whole file exists to remove.
func TwoDistinctPeople(a, b PersonRef, onUndetermined UndeterminedPolicy) (bool, PersonMatch) {
	switch m := a.Compare(b); m {
	case PersonDistinct:
		return true, m
	case PersonSame, PersonSameCredential:
		return false, m
	default:
		return onUndetermined == AcceptWhenUndetermined, m
	}
}

// DistinctPeople counts how many distinct ACCOUNTS a set of parties represents, and how
// many carry no account attribution. It is the quorum form of the same rule: a
// threshold ("at least 2 distinct approvers") is a comparison repeated, and counting raw
// credential strings inflates it exactly the way comparing them did. It does not count
// humans — one human with two accounts counts as two (file header).
//
// The two returns are deliberately separate. unattributable parties are NOT folded into
// distinct — an approval carrying one account and three unattributable credentials is one
// account and three unknowns, never four approvers — and they are not silently dropped
// either, so a
// caller can refuse evidence it cannot attribute instead of quietly counting past it.
func DistinctPeople(refs []PersonRef) (distinct, undetermined int) {
	people := make(map[string]struct{}, len(refs))
	creds := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		if r.User != "" {
			people[r.User] = struct{}{}
			continue
		}
		// An account-less party is unknown, but two references to the SAME credential
		// are one unknown, not two — the same knowledge Compare uses.
		if r.Actor != "" {
			if _, dup := creds[r.Actor]; dup {
				continue
			}
			creds[r.Actor] = struct{}{}
		}
		undetermined++
	}
	return len(people), undetermined
}

// SamePersonAs reports whether both parties are the same user ACCOUNT. It is exactly
// `Compare(o) == PersonSame`: it answers ONE narrow question truthfully and it is the
// only question it can answer. It is not "the same human" — see the file header.
//
// ⛔ IT IS NOT AN AUTHORIZATION PRIMITIVE, and `!SamePersonAs(...)` IS THE ORIGINAL
// DEFECT. This was the P1 the external contrast on PR #615 found: a bool has two
// states and the verdict has four, so PersonDistinct, PersonUndetermined and
// PersonSameCredential all arrive as the same `false`. A gate that authorizes on the
// negation therefore reads "I cannot attribute either party" as "two different people" —
// exactly the silent pass this package was written to remove, reintroduced through the
// package's own front door. (`PersonDistinct` here means two ACCOUNTS, so even the
// correct reading of `false` is weaker than the name suggests.)
//
// Two things stop that now, and neither is deleting this function — deleting it would
// only have moved the collapse to `Compare(o) != PersonSame`, which any caller can write.
// First, the deny direction is safe by construction: TwoDistinctPeople is the only thing
// that says who may proceed. Second, the class gate REFUSES this shape by name: the
// two-party scanner in twoperson_class_test.go no longer accepts a reference to these
// wrappers as proof of compliance, and it reports any two-party control that authorizes
// on a negated person-sameness boolean — including the hand-rolled form, and including
// functions that reference the primitive elsewhere.
//
// Use it to ask "is this the same ACCOUNT?". Use TwoDistinctPeople to decide anything.
func (r PersonRef) SamePersonAs(o PersonRef) bool { return r.Compare(o) == PersonSame }

// SamePerson reports whether two live principals are the same user ACCOUNT, by the rule
// in Compare. Use it when both parties are principals in hand; when one side was recorded
// earlier, store a PersonRef and compare with Compare instead.
//
// The warning on SamePersonAs applies verbatim: this inherits its boolean, so it inherits
// the collapse. `!SamePerson(a, b)` is not "two parties" and the class gate refuses it.
func SamePerson(a, b Principal) bool {
	return PersonRefOf(a).SamePersonAs(PersonRefOf(b))
}
