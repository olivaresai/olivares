// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// route_evidence_digest_internal_test.go qualifies the RouteEvidenceDigestFormat codec (G1).
//
// Two kinds of control live here. The CAUSAL ones mint a real witness through AuthorizeRoute
// and change one lease coordinate at a time; nothing in those witnesses is typed by hand.
// The FIXED-VECTOR ones compare the codec against digests computed by the assessment-owned
// encoder (assessments/implementation/generic-lease-digest/vectors.py) from the written
// specification in docs/design/route-evidence-digest-v2.md — never by asking this package for
// its own answer. Those constants are permanent: a value that stops matching is a codec
// change and needs a new format label, not a new constant.

// leasedRouteWitnessFixture holds one REAL generic witness minted through AuthorizeRoute for
// a resolved human principal whose scoped producer contributed one leased fact. Nothing in
// the witness is typed by hand: the seal, the digests and the canonical fact vector are the
// ones production issues, which is what makes the lease-coordinate controls below causal.
type leasedRouteWitnessFixture struct {
	now      time.Time
	req      Request
	lease    store.AuthorizationFactRef
	deadline model.Timestamp
	witness  RouteAuthorizationWitness
}

func mintLeasedRouteWitness(t *testing.T) leasedRouteWitnessFixture {
	t.Helper()
	f, p := resolvedPrincipalAuthorityEvidence(t)
	now := p.evidence.observedAt.Add(time.Second)
	deadline := model.NewTimestamp(now.Add(time.Hour))
	lease, err := store.NewLeaseFenceAuthorizationFactRef("test.lease", model.NewID(), 7, "subject", 9, deadline)
	if err != nil {
		t.Fatal(err)
	}
	scoped := &principalAuthorityScopedProducer{
		decision: principalAuthorityCleanScoped(p.evidence.observedAt, p.evidence.freshUntil, lease),
	}
	az := NewAuthorizer(nil, WithScopedGrants(scoped), WithClock(func() time.Time { return now }))
	req := principalAuthorityEvidenceRequest(p, f.tenant)
	req.Route = RouteMetadata{CedarAction: "agent:read"}
	w, err := az.AuthorizeRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("AuthorizeRoute: %v", err)
	}
	// Control positive: the minted witness verifies for its own question, so a refusal
	// below is a refusal of the mutation and not of a witness that never verified.
	if !w.VerifyFor(now, req) {
		t.Fatal("the minted leased witness does not verify for the question it was minted for")
	}
	carried := false
	for _, fact := range w.Decision.Facts {
		if fact == lease {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the minted witness does not carry the leased fact: %+v", w.Decision.Facts)
	}
	return leasedRouteWitnessFixture{now: now, req: req, lease: lease, deadline: deadline, witness: w}
}

// replaceLease returns an INDEPENDENT copy of the witness whose leased fact is replaced by
// changed and whose every other fact is byte-identical. The copy owns its Facts slice, so the
// baseline witness is never contaminated by the mutation.
func (x leasedRouteWitnessFixture) replaceLease(changed store.AuthorizationFactRef) RouteAuthorizationWitness {
	v := x.witness
	v.Decision.Facts = append([]store.AuthorizationFactRef(nil), x.witness.Decision.Facts...)
	for i, fact := range v.Decision.Facts {
		if fact.ID == x.lease.ID {
			v.Decision.Facts[i] = changed
		}
	}
	return v
}

// leaseMutations are the four retained-coordinate mutations: each keeps the fact's Kind, ID
// and Version and changes exactly one lease coordinate (or drops the lease altogether).
func (x leasedRouteWitnessFixture) leaseMutations(t *testing.T) map[string]store.AuthorizationFactRef {
	t.Helper()
	mutate := func(subject string, fence int64, until model.Timestamp) store.AuthorizationFactRef {
		changed, err := store.NewLeaseFenceAuthorizationFactRef(x.lease.Kind, x.lease.ID, x.lease.Version, subject, fence, until)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	return map[string]store.AuthorizationFactRef{
		"presence": {Kind: x.lease.Kind, ID: x.lease.ID, Version: x.lease.Version},
		"subject":  mutate("changed", 9, x.deadline),
		"fence":    mutate("subject", 10, x.deadline),
		"deadline": mutate("subject", 9, model.NewTimestamp(x.deadline.Time().Add(time.Second))),
	}
}

// TestRouteEvidenceDigestBindsLeaseCoordinates is the G1 causal control. A witness minted
// with a leased fact is copied and its lease presence, subject, fence or deadline is changed
// while Kind, ID and Version are retained. The generic evidence digest must move, and the
// retained-digest copy must be neither sound nor verifiable.
//
// Before G1 this test is RED on unchanged production: the generic codec committed only
// Kind/ID/Version per fact, so every one of these copies recomputed to its original digest
// and stayed sound. That is an integrity-digest regression, not an exploit: final SQL
// validation already rejected a missing or mismatched lease, and this file claims nothing
// about it. The test references no symbol introduced by G1, so its red is a semantic red and
// not a compiler failure.
func TestRouteEvidenceDigestBindsLeaseCoordinates(t *testing.T) {
	x := mintLeasedRouteWitness(t)
	for name, changed := range x.leaseMutations(t) {
		t.Run("lease-"+name, func(t *testing.T) {
			v := x.replaceLease(changed)
			if evidenceDigest(v) == x.witness.EvidenceDigest {
				t.Errorf("the generic evidence digest omits the lease %s: a copy whose leased fact changed only in that coordinate recomputes to its original digest", name)
			}
			if v.IsSound(x.now) {
				t.Errorf("a retained-digest copy with a changed lease %s is still sound", name)
			}
			if v.VerifyFor(x.now, x.req) {
				t.Errorf("a retained-digest copy with a changed lease %s still verifies for the original question", name)
			}
		})
	}
	// The independent copies must not have touched the baseline.
	if evidenceDigest(x.witness) != x.witness.EvidenceDigest || !x.witness.VerifyFor(x.now, x.req) {
		t.Fatal("the mutation copies contaminated the baseline witness")
	}
}

// TestRouteEvidenceDigestIssuerCanonicalizesAndRefusesContradictions exercises the issuer
// rather than the codec: the same leased facts presented in a different input order produce
// one canonical vector and one digest, while contradictory duplicates — the same Kind/ID with
// a different lease, or the same Kind/subject under a different ID — are refused at issuance
// instead of being repaired by the codec.
func TestRouteEvidenceDigestIssuerCanonicalizesAndRefusesContradictions(t *testing.T) {
	f, p := resolvedPrincipalAuthorityEvidence(t)
	now := p.evidence.observedAt.Add(time.Second)
	deadline := model.NewTimestamp(now.Add(time.Hour))
	lease := func(id model.ID, version int64, subject string, fence int64) store.AuthorizationFactRef {
		fact, err := store.NewLeaseFenceAuthorizationFactRef("test.lease", id, version, subject, fence, deadline)
		if err != nil {
			t.Fatal(err)
		}
		return fact
	}
	first, second := lease(model.NewID(), 7, "alpha", 9), lease(model.NewID(), 8, "beta", 10)
	scoped := &principalAuthorityScopedProducer{
		decision: principalAuthorityCleanScoped(p.evidence.observedAt, p.evidence.freshUntil, first, second),
	}
	az := NewAuthorizer(nil, WithScopedGrants(scoped), WithClock(func() time.Time { return now }))
	req := principalAuthorityEvidenceRequest(p, f.tenant)
	req.Route = RouteMetadata{CedarAction: "agent:read"}
	left, err := az.AuthorizeRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("AuthorizeRoute: %v", err)
	}
	scoped.decision.Facts = []store.AuthorizationFactRef{second, first}
	right, err := az.AuthorizeRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("AuthorizeRoute (reversed input): %v", err)
	}
	if left.EvidenceDigest != right.EvidenceDigest || !slices.Equal(left.Decision.Facts, right.Decision.Facts) {
		t.Fatal("input order changed the issued fact vector or the generic digest")
	}
	if !slices.IsSortedFunc(left.Decision.Facts, func(a, b store.AuthorizationFactRef) int {
		if a.Kind != b.Kind {
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		if a.ID.String() < b.ID.String() {
			return -1
		}
		if a.ID.String() > b.ID.String() {
			return 1
		}
		return 0
	}) {
		t.Fatalf("the issued facts are not in canonical Kind/ID order: %+v", left.Decision.Facts)
	}
	for name, facts := range map[string][]store.AuthorizationFactRef{
		"same Kind/ID with a different lease subject": {first, lease(first.ID, first.Version, "gamma", first.Version+2)},
		"same Kind/subject under a different ID":      {first, lease(model.NewID(), 7, "alpha", 9)},
		"same Kind/ID with the lease dropped":         {first, {Kind: first.Kind, ID: first.ID, Version: first.Version}},
	} {
		t.Run(name, func(t *testing.T) {
			scoped.decision.Facts = facts
			w, err := az.AuthorizeRoute(context.Background(), req)
			if !errors.Is(err, ErrRouteUndecided) || w.minted {
				t.Fatalf("contradictory facts were issued as a witness: err=%v minted=%v", err, w.minted)
			}
		})
	}
}

// --- independently specified fixed vectors --------------------------------------------------

// The hex digests below are the assessment-owned encoder's output over the fixed inputs
// reproduced by the helpers that follow. See the file comment for why they are permanent.
const (
	vectorOrdinaryFacts        = "50d1bf7eb21029f1ba0b6188c2a5dbd5b49928e8f54c29d7435a682c68e9a585"
	vectorLeaseOnly            = "91b046671cc0464e6adc49976e9a477627bc5fe238b3c972274bb9e51c2e72b4"
	vectorMixed                = "e4557d5f26b171282aadd6104726f283073bfa45cdfc0bd68c2355a677f15f26"
	vectorEmptyFacts           = "a2f11ac632d25b3b3383c02205620bf8af3265e047f15c1e7b63c99571605044"
	vectorInvalidUTF8Subject   = "b42d374a622b7a4026bb6fede6ce5cb7de47d1068d4830bbed4405a037a26182"
	vectorExtremeWindow        = "eb16c6f4b5456baa8f7b1cffffa25d97b33ad8a0d97acd755a643281c8d417dd"
	vectorExtremeWindowPlusOne = "5d54582172ceb57ac67faa72e8a46c59a838f3a4fa28cdd03bed95e73b9ad696"
	vectorCompleteReadHuman    = "80b5008de7247f701bfb549a2fd1f3c94c565e16b29d9aab6c313f7455a5b4f1"
	vectorCompleteReadToken    = "578d177303130d054346291dfe8e5025187e9801e998ff580ab2c14302e3202e"
	// vectorOtherDomainControl is the mixed vector framed under a label that is NOT the
	// current format. The codec must not produce it, and a witness carrying it is unsound.
	vectorOtherDomainControl = "e58a82d772fa0ef6ff8f4abbbcd11aaf28d6a101f9f2a0da6debd1568c4b5048"
	// The two references below are the pre-G1 codec (no format label, UnixNano window,
	// Kind/ID/Version per fact) over the mixed vector, and the unchanged U2 human complete
	// digest wrapping those old inner bytes. They record the explicit inner-byte change and
	// prove that an old value no longer satisfies the current recomputation. They are a
	// reference transcribed from the pre-G1 source, not a specification.
	referencePreG1Inner        = "fcccbe29650b8eadffe1f81d96f82e55cda1f3830998a81d6b51e1e87f5adeec"
	referencePreG1CompleteRead = "05f08a24cfee42d68724f38b8635e92677a4069902bd768a3ed27222a0102dde"
)

func vectorDigest(t *testing.T, want string) [32]byte {
	t.Helper()
	raw, err := hex.DecodeString(want)
	if err != nil || len(raw) != 32 {
		t.Fatalf("vector %q is not a 32-byte hex digest", want)
	}
	var out [32]byte
	copy(out[:], raw)
	return out
}

// fixedVectorBytes is the 32-byte run first, first+1, ..., first+31 the encoder uses for
// opaque digests and seals.
func fixedVectorBytes(first byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = first + byte(i)
	}
	return out
}

var (
	fixedVectorObservedAt = time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	fixedVectorFreshUntil = time.Date(2026, time.September, 9, 12, 5, 0, 250000000, time.UTC)
	fixedLeaseDeadline    = time.Date(2026, time.September, 9, 13, 0, 0, 0, time.UTC)
	fixedFactA            = store.AuthorizationFactRef{Kind: "core.agent", ID: "11111111-1111-7111-8111-111111111111", Version: 3}
	fixedFactB            = store.AuthorizationFactRef{Kind: "core.directory_epoch", ID: "22222222-2222-7222-8222-222222222222", Version: 17}
)

// fixedVectorWitness is the encoder's BASE witness over the given facts, which must already
// be in canonical order: the codec does not sort.
func fixedVectorWitness(facts ...store.AuthorizationFactRef) RouteAuthorizationWitness {
	return RouteAuthorizationWitness{
		CedarAction: "session:stop", ScopedEffect: EffectGrant,
		ResourceDigest: fixedVectorBytes(1), QuestionDigest: fixedVectorBytes(101), PolicyVersion: 7,
		Decision: AuthorizationEvidence{
			Outcome:        EvidenceAllow,
			CorePermission: CheckEvidence{Verdict: CheckClean, Code: "rbac_permitted"},
			ResourceGuard:  CheckEvidence{Verdict: CheckClean, Code: "guard_clean"},
			ForbidAbsence:  CheckEvidence{Verdict: CheckClean, Code: "no_forbid"},
			Facts:          facts,
			ObservedAt:     fixedVectorObservedAt,
			FreshUntil:     fixedVectorFreshUntil,
		},
	}
}

func fixedLease(t *testing.T, id string, version int64, subject string, fence int64, deadline time.Time) store.AuthorizationFactRef {
	t.Helper()
	fact, err := store.NewLeaseFenceAuthorizationFactRef("test.lease", model.ID(id), version, subject, fence, model.NewTimestamp(deadline))
	if err != nil {
		t.Fatal(err)
	}
	return fact
}

func fixedLeaseOne(t *testing.T) store.AuthorizationFactRef {
	return fixedLease(t, "33333333-3333-7333-8333-333333333333", 7, "alpha", 9, fixedLeaseDeadline)
}

func fixedLeaseTwo(t *testing.T) store.AuthorizationFactRef {
	return fixedLease(t, "44444444-4444-7444-8444-444444444444", 8, "beta", 10, fixedLeaseDeadline.Add(500*time.Millisecond))
}

// fixedLeaseRawSubject is fixedLeaseOne with a subject that is NOT valid UTF-8 and contains a
// NUL byte. The constructor accepts it, and the codec frames exactly those seven bytes.
func fixedLeaseRawSubject(t *testing.T) store.AuthorizationFactRef {
	return fixedLease(t, "33333333-3333-7333-8333-333333333333", 7, "\xff\xfe\x00sub\x80", 9, fixedLeaseDeadline)
}

func fixedMixedWitness(t *testing.T) RouteAuthorizationWitness {
	return fixedVectorWitness(fixedFactA, fixedFactB, fixedLeaseOne(t), fixedLeaseTwo(t))
}

// TestRouteEvidenceDigestMatchesTheIndependentFixedVectors compares the codec against the
// encoder over ordinary, leased, mixed and empty fact sets, an accepted non-UTF-8 subject, and
// window endpoints before 1678 and after 2262 (outside int64 UnixNano range), which must
// remain distinct at nanosecond resolution rather than be folded.
func TestRouteEvidenceDigestMatchesTheIndependentFixedVectors(t *testing.T) {
	extreme := fixedVectorWitness(fixedFactA, fixedFactB)
	extreme.Decision.ObservedAt = time.Date(1600, time.June, 15, 1, 2, 3, 4, time.UTC)
	extreme.Decision.FreshUntil = time.Date(2300, time.January, 1, 0, 0, 0, 0, time.UTC)
	plusOne := extreme
	plusOne.Decision.ObservedAt = extreme.Decision.ObservedAt.Add(time.Nanosecond)
	cases := []struct {
		name    string
		witness RouteAuthorizationWitness
		want    string
	}{
		{"ordinary facts", fixedVectorWitness(fixedFactA, fixedFactB), vectorOrdinaryFacts},
		{"lease only", fixedVectorWitness(fixedLeaseOne(t)), vectorLeaseOnly},
		{"mixed", fixedMixedWitness(t), vectorMixed},
		{"empty facts", fixedVectorWitness(), vectorEmptyFacts},
		{"invalid UTF-8 subject", fixedVectorWitness(fixedLeaseRawSubject(t)), vectorInvalidUTF8Subject},
		{"window before 1678 and after 2262", extreme, vectorExtremeWindow},
		{"window plus one nanosecond", plusOne, vectorExtremeWindowPlusOne},
	}
	seen := map[[32]byte]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evidenceDigest(c.witness)
			if hex.EncodeToString(got[:]) != c.want {
				t.Fatalf("digest = %x, want the independent vector %s", got, c.want)
			}
			if prior, dup := seen[got]; dup {
				t.Fatalf("collides with %s", prior)
			}
			seen[got] = c.name
			if evidenceDigest(c.witness) != got {
				t.Fatal("the digest is not stable across calls")
			}
		})
	}
}

// TestRouteEvidenceDigestNormalizesUTCOffsets: equal instants written with different UTC
// offsets, or with an offset on one endpoint only, digest identically to the UTC vector; a
// calendar text that merely LOOKS the same in another offset is a different instant.
func TestRouteEvidenceDigestNormalizesUTCOffsets(t *testing.T) {
	east := time.FixedZone("UTC+05:30", 5*3600+30*60)
	west := time.FixedZone("UTC-07:00", -7*3600)
	want := vectorDigest(t, vectorOrdinaryFacts)
	for name, window := range map[string][2]time.Time{
		"both east": {time.Date(2026, time.September, 9, 17, 30, 0, 0, east), time.Date(2026, time.September, 9, 17, 35, 0, 250000000, east)},
		"both west": {time.Date(2026, time.September, 9, 5, 0, 0, 0, west), time.Date(2026, time.September, 9, 5, 5, 0, 250000000, west)},
		"mixed":     {time.Date(2026, time.September, 9, 17, 30, 0, 0, east), time.Date(2026, time.September, 9, 5, 5, 0, 250000000, west)},
	} {
		t.Run(name, func(t *testing.T) {
			w := fixedVectorWitness(fixedFactA, fixedFactB)
			w.Decision.ObservedAt, w.Decision.FreshUntil = window[0], window[1]
			if evidenceDigest(w) != want {
				t.Fatalf("an offset representation of the same instants changed the digest: %x", evidenceDigest(w))
			}
		})
	}
	shifted := fixedVectorWitness(fixedFactA, fixedFactB)
	shifted.Decision.ObservedAt = time.Date(2026, time.September, 9, 12, 0, 0, 0, east)
	if evidenceDigest(shifted) == want {
		t.Fatal("a different instant with the same wall-clock text digested as the UTC vector")
	}
}

// TestRouteEvidenceDigestRefusesOtherDomainsAndUnmintedValues: the codec does not produce a
// digest framed under another format label, and a witness carrying one — or carrying the
// pre-G1 value, or never minted, or zero — is not sound.
func TestRouteEvidenceDigestRefusesOtherDomainsAndUnmintedValues(t *testing.T) {
	now := fixedVectorObservedAt.Add(time.Minute)
	w := fixedMixedWitness(t)
	w.minted = true
	w.EvidenceDigest = evidenceDigest(w)
	if !w.IsSound(now) {
		t.Fatal("control positive: a minted witness carrying its own digest is not sound")
	}
	for name, digest := range map[string][32]byte{
		"another format label": vectorDigest(t, vectorOtherDomainControl),
		"the pre-G1 codec":     vectorDigest(t, referencePreG1Inner),
	} {
		t.Run(name, func(t *testing.T) {
			if w.EvidenceDigest == digest {
				t.Fatalf("the codec produced the digest of %s", name)
			}
			carrying := w
			carrying.EvidenceDigest = digest
			if carrying.IsSound(now) || carrying.VerifyFor(now, Request{}) {
				t.Fatalf("a witness carrying the digest of %s is sound", name)
			}
		})
	}
	unminted := w
	unminted.minted = false
	if unminted.IsSound(now) || unminted.Allows(now) {
		t.Fatal("an unminted copy with a correct digest is sound")
	}
	if (RouteAuthorizationWitness{}).IsSound(now) {
		t.Fatal("the zero witness is sound")
	}
}

// TestRouteEvidenceDigestDoesNotRepairOrderOrDuplicates: the codec neither sorts,
// deduplicates nor discards, so a vector that was mutated away from its canonical issuance
// recomputes to a different value instead of matching; and the subject bytes are part of the
// lease identity.
func TestRouteEvidenceDigestDoesNotRepairOrderOrDuplicates(t *testing.T) {
	canonical := fixedMixedWitness(t)
	want := vectorDigest(t, vectorMixed)
	if evidenceDigest(canonical) != want {
		t.Fatal("control positive: the canonical mixed witness does not match its vector")
	}
	swapped := canonical
	swapped.Decision.Facts = slices.Clone(canonical.Decision.Facts)
	swapped.Decision.Facts[0], swapped.Decision.Facts[1] = swapped.Decision.Facts[1], swapped.Decision.Facts[0]
	duplicated := canonical
	duplicated.Decision.Facts = append(slices.Clone(canonical.Decision.Facts), fixedFactA)
	dropped := canonical
	dropped.Decision.Facts = slices.Clone(canonical.Decision.Facts[:3])
	seen := map[[32]byte]string{want: "canonical"}
	for name, w := range map[string]RouteAuthorizationWitness{"swapped": swapped, "duplicated": duplicated, "dropped": dropped} {
		got := evidenceDigest(w)
		if prior, dup := seen[got]; dup {
			t.Fatalf("%s digests like %s: the codec repaired the vector", name, prior)
		}
		seen[got] = name
	}
	if evidenceDigest(canonical) != want {
		t.Fatal("the mutation copies contaminated the canonical witness")
	}
	if evidenceDigest(fixedVectorWitness(fixedLeaseOne(t))) == evidenceDigest(fixedVectorWitness(fixedLeaseRawSubject(t))) {
		t.Fatal("two leases that differ only in subject bytes digest identically")
	}
}

// TestCompleteReadDigestMatchesTheNewFixedVectorsAndRecordsTheInnerByteChange is the U2
// compatibility boundary. The complete-read domain and sequence are unchanged; only the inner
// generic bytes moved. Both human and token modes are compared against the encoder, the
// unchanged sequence is proved by wrapping the pre-G1 inner bytes and recovering the pre-G1
// complete digest, and a decision carrying those old inner bytes is not intact. The sealed
// principal seal golden is a different codec and is not touched here.
func TestCompleteReadDigestMatchesTheNewFixedVectorsAndRecordsTheInnerByteChange(t *testing.T) {
	now := fixedVectorObservedAt.Add(time.Minute)
	w := fixedMixedWitness(t)
	w.minted = true
	w.EvidenceDigest = evidenceDigest(w)
	if hex.EncodeToString(w.EvidenceDigest[:]) != vectorMixed {
		t.Fatal("control positive: the inner witness does not match its vector")
	}
	seal := fixedVectorBytes(201)
	human := RouteReadDecision{
		witness: w, issued: true, authorityMode: principalHumanAuthority,
		userAuthority: store.UserAuthorityFactRef{UserID: "55555555-5555-7555-8555-555555555555", Version: 23},
		principalSeal: seal,
	}
	human.authorityDigest = human.completeDigest()
	token := RouteReadDecision{witness: w, issued: true, authorityMode: principalTokenDirectoryOnly, principalSeal: seal}
	token.authorityDigest = token.completeDigest()
	for name, c := range map[string]struct {
		decision RouteReadDecision
		want     string
	}{
		"human": {human, vectorCompleteReadHuman},
		"token": {token, vectorCompleteReadToken},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hex.EncodeToString(c.decision.authorityDigest[:]); got != c.want {
				t.Fatalf("complete read digest = %s, want the independent vector %s", got, c.want)
			}
			if !c.decision.intact(now) {
				t.Fatal("control positive: the fixed complete decision is not intact")
			}
		})
	}
	if hex.EncodeToString(human.authorityDigest[:]) == referencePreG1CompleteRead {
		t.Fatal("the complete read digest did not record the inner-byte change")
	}
	old := human
	old.witness.EvidenceDigest = vectorDigest(t, referencePreG1Inner)
	old.authorityDigest = old.completeDigest()
	if got := hex.EncodeToString(old.authorityDigest[:]); got != referencePreG1CompleteRead {
		t.Fatalf("the complete read sequence changed: wrapping the pre-G1 inner bytes gives %s, want %s", got, referencePreG1CompleteRead)
	}
	if old.intact(now) {
		t.Fatal("a decision carrying the pre-G1 inner digest is intact")
	}
	mixedH := token
	mixedH.userAuthority = human.userAuthority
	mixedH.authorityDigest = mixedH.completeDigest()
	if mixedH.intact(now) {
		t.Fatal("a token-directory-only decision carrying an H value is intact")
	}
}
