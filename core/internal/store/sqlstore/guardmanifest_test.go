// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"strings"
	"testing"
)

// guardmanifest_test.go pins the properties every guard digest rests on.
//
// They are properties of the ENCODING rather than of any particular value, because that is
// where the failures live: a canonicalisation that is not injective produces two different
// objects with one fingerprint, and nothing downstream can recover from that — the golden
// compares equal and the drift is ratified.

// TestGuardCanonicalEncodingIsInjective pins the five confusions a length-prefixed,
// type-tagged encoding exists to prevent.
//
// Each pair below encodes DIFFERENT data and must produce different bytes. A naive encoder
// — concatenate the strings, write integers as decimal text, treat NULL as "" — collapses
// every one of them.
func TestGuardCanonicalEncodingIsInjective(t *testing.T) {
	t.Parallel()

	sum := func(f func(w *canonWriter)) string {
		w := newCanonWriter(canonDomainEntry, 1)
		f(w)
		d, err := w.sum()
		if err != nil {
			t.Fatalf("canonicalise: %v", err)
		}
		return hexDigest(d)
	}

	cases := []struct {
		name string
		a, b func(w *canonWriter)
	}{{
		// The classic: two adjacent strings whose concatenation is the same.
		name: "field boundaries",
		a:    func(w *canonWriter) { w.str("ab"); w.str("c") },
		b:    func(w *canonWriter) { w.str("a"); w.str("bc") },
	}, {
		// NULL is not the empty string. tgqual NULL means "no WHEN clause"; tgqual '' would
		// mean a clause whose text is empty, which is not a state PostgreSQL produces.
		name: "null against empty",
		a:    func(w *canonWriter) { w.opt(optText{}) },
		b:    func(w *canonWriter) { w.opt(someText("")) },
	}, {
		// An empty collection is not NULL either.
		name: "empty list against null",
		a:    func(w *canonWriter) { w.list(0) },
		b:    func(w *canonWriter) { w.opt(optText{}) },
	}, {
		// A signed value and its unsigned reinterpretation must not collide: -1 as uint64 is
		// 18446744073709551615, so a shared tag would make those one value.
		name: "signed against unsigned",
		a:    func(w *canonWriter) { w.i64(-1) },
		b:    func(w *canonWriter) { w.u64(^uint64(0)) },
	}, {
		// A key's three components are separate fields, not a dotted string. `"a.b"."c"` and
		// `"a"."b.c"` are two different relations.
		name: "dotted identifiers",
		a:    func(w *canonWriter) { guardKey{Schema: "a.b", Relation: "c", Trigger: "t"}.canon(w) },
		b:    func(w *canonWriter) { guardKey{Schema: "a", Relation: "b.c", Trigger: "t"}.canon(w) },
	}}

	for _, tc := range cases {
		if got, want := sum(tc.a), sum(tc.b); got == want {
			t.Errorf("%s: two different values canonicalise to the same digest %s", tc.name, got)
		}
	}
}

// TestGuardCanonicalDomainsAreSeparated pins that bytes hashed for one purpose cannot be
// replayed as bytes for another.
//
// Without the domain tag, a prestate digest and a receipt digest over the same fields would
// be the same value, and a value that is valid in two contexts is a value that can be moved
// between them.
func TestGuardCanonicalDomainsAreSeparated(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, domain := range []string{
		canonDomainManifest, canonDomainEntry, canonDomainDefinition,
		canonDomainPrestate, canonDomainDiagnostic, canonDomainReceipt, canonDomainEvent,
	} {
		w := newCanonWriter(domain, guardManifestFormat)
		w.str("the same body")
		d, err := w.sum()
		if err != nil {
			t.Fatalf("canonicalise under %s: %v", domain, err)
		}
		if prev, dup := seen[hexDigest(d)]; dup {
			t.Errorf("domains %q and %q produce the same digest for the same body", prev, domain)
		}
		seen[hexDigest(d)] = domain
	}
}

// TestGuardCanonicalRefusesNonFiniteFloats pins the one input with no comparable canonical
// form.
//
// procost and prorows are floats. NaN is not equal to itself, so a golden containing one
// would report drift forever; an infinity is not a cost PostgreSQL produces. Refusing is
// honest where hashing the bits would produce a stable fingerprint for an unusable value.
func TestGuardCanonicalRefusesNonFiniteFloats(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{nan(), inf(1), inf(-1)} {
		w := newCanonWriter(canonDomainDefinition, 1)
		w.float(v)
		if _, err := w.sum(); err == nil {
			t.Errorf("canonicalising %v produced a digest; a non-finite value has no comparable canonical form", v)
		}
	}
}

func nan() float64 { return zero() / zero() }
func inf(s int) float64 {
	if s < 0 {
		return -1 / zero()
	}
	return 1 / zero()
}
func zero() float64 { return 0 }

// TestGuardManifestIsAFunctionOfItsInputAndNotOfItsOrder pins that the census cannot depend
// on registration order.
//
// It matters because the order is INSIDE the canonical bytes: an order-dependent manifest
// would compute a different code_sha256 when a module was enabled in a different sequence, and
// the database would then report drift for a binary whose declarations are identical.
func TestGuardManifestIsAFunctionOfItsInputAndNotOfItsOrder(t *testing.T) {
	t.Parallel()
	forward := []string{"audit_events", "b_table", "a_table", "c_table"}
	reverse := []string{"c_table", "a_table", "b_table", "audit_events"}

	m1, err := buildGuardManifest(forward)
	if err != nil {
		t.Fatalf("build forward: %v", err)
	}
	m2, err := buildGuardManifest(reverse)
	if err != nil {
		t.Fatalf("build reverse: %v", err)
	}
	if m1.CodeSHA256 != m2.CodeSHA256 {
		t.Errorf("the manifest digest depends on input order: %s against %s",
			hexDigest(m1.CodeSHA256), hexDigest(m2.CodeSHA256))
	}
	if len(m1.Specs) != len(forward) {
		t.Fatalf("the manifest holds %d entries for %d tables", len(m1.Specs), len(forward))
	}
	for i := 1; i < len(m1.Specs); i++ {
		if !m1.Specs[i-1].Key.less(m1.Specs[i].Key) {
			t.Errorf("entry %d (%s) does not sort before entry %d (%s)",
				i-1, m1.Specs[i-1].Key, i, m1.Specs[i].Key)
		}
	}
}

// TestGuardManifestRefusesWhatItCannotRepresent pins the constructor's refusals.
func TestGuardManifestRefusesWhatItCannotRepresent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tables []string
		want   string
	}{
		{"a duplicate table", []string{"t", "t"}, "twice"},
		{"an empty name", []string{""}, "empty"},
		{"a NUL byte", []string{"t\x00u"}, "NUL"},
	}
	for _, tc := range cases {
		if _, err := buildGuardManifest(tc.tables); err == nil {
			t.Errorf("%s was accepted", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s was refused with %q, which does not mention %q", tc.name, err, tc.want)
		}
	}
}

// TestGuardManifestSeparatesDefinitionFromSpec is the property that makes O -> A
// representable at all.
//
// Two entries with the same object and DIFFERENT desired states must share a definition
// digest and differ in their spec digest. A single fingerprint covering both would make a
// legitimate transition look like a redefinition of the object.
func TestGuardManifestSeparatesDefinitionFromSpec(t *testing.T) {
	t.Parallel()
	key := guardKey{Schema: guardSchema, Relation: "t", Trigger: "t" + guardTriggerSuffix}
	base := guardSpec{
		Key: key, Producer: guardProducerEngine, Definition: canonicalGuardDefinition(),
		LegacyAllowedStates: []string{guardStateOrigin, guardStateAlways},
	}
	var err error
	if base.DefinitionSHA256, err = base.Definition.definitionDigest(key); err != nil {
		t.Fatal(err)
	}
	atO, atA := base, base
	atO.DesiredEnableState, atA.DesiredEnableState = guardStateOrigin, guardStateAlways
	sO, err := atO.specDigest()
	if err != nil {
		t.Fatal(err)
	}
	sA, err := atA.specDigest()
	if err != nil {
		t.Fatal(err)
	}
	if sO == sA {
		t.Error("two entries differing only in their desired state produce the same spec digest, so one edition's policy could ratify another's")
	}
	dO, err := atO.Definition.definitionDigest(key)
	if err != nil {
		t.Fatal(err)
	}
	dA, err := atA.Definition.definitionDigest(key)
	if err != nil {
		t.Fatal(err)
	}
	if dO != dA {
		t.Error("the DEFINITION digest changed with the desired state, which would make an O -> A transition look like a redefinition of the object")
	}
}

// TestGuardIdentitiesAreDistinctAcrossIntentsAndPurposes pins that a unit id, a bootstrap
// unit id and a rollout id cannot collide.
//
// A collision would be a silent authorisation transfer: a bootstrap receipt read as a unit's,
// or an adoption's receipt accepted for the transition that follows it.
func TestGuardIdentitiesAreDistinctAcrossIntentsAndPurposes(t *testing.T) {
	t.Parallel()
	key := guardKey{Schema: guardSchema, Relation: "t", Trigger: "t" + guardTriggerSuffix}
	seen := map[string]string{}
	add := func(what, id string) {
		if prev, dup := seen[id]; dup {
			t.Errorf("%s and %s share the identity %s", prev, what, id)
		}
		seen[id] = what
	}
	for _, intent := range []unitIntent{intentCreateGuard, intentAdoptLegacy, intentTransitionLegacyOToA, intentRepair} {
		id, err := guardUnitID(guardManifestFormat, key, intent)
		if err != nil {
			t.Fatalf("unit id for %s: %v", intent, err)
		}
		add("unit:"+string(intent), id)
	}
	bootstrapID, err := guardBootstrapUnitID(guardManifestFormat, key)
	if err != nil {
		t.Fatal(err)
	}
	add("bootstrap", bootstrapID)

	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rollout, err := guardRolloutID(guardManifestFormat, 1, empty, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	add("rollout", rollout)

	// A unit id must also be a function of its key: two relations must not share one.
	other, err := guardUnitID(guardManifestFormat, guardKey{Schema: guardSchema, Relation: "u", Trigger: "u" + guardTriggerSuffix}, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	add("unit:other-relation", other)

	if _, err := guardUnitID(guardManifestFormat, key, ""); err == nil {
		t.Error("a unit id was produced for an empty intent")
	}
}

// TestGuardRolloutIDBindsTheWholeEdition pins that the identity moves when any member of the
// authorizing tuple moves.
//
// This is what makes a second boot recognize the SAME rollout rather than open a duplicate,
// and what makes a changed edition compute a DIFFERENT id instead of silently reusing the
// old rollout's authorisation.
func TestGuardRolloutIDBindsTheWholeEdition(t *testing.T) {
	t.Parallel()
	base, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	other := base
	other[0] ^= 0xff

	id := func(format, epoch int64, code [32]byte, rev int64, retained [32]byte) string {
		got, err := guardRolloutID(format, epoch, code, rev, retained)
		if err != nil {
			t.Fatalf("rollout id: %v", err)
		}
		return got
	}
	ref := id(1, 1, base, 0, base)
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"format", id(2, 1, base, 0, base)},
		{"epoch", id(1, 2, base, 0, base)},
		{"code digest", id(1, 1, other, 0, base)},
		{"retained revision", id(1, 1, base, 1, base)},
		{"retained digest", id(1, 1, base, 0, other)},
	} {
		if tc.got == ref {
			t.Errorf("changing the %s did not change the rollout identity", tc.name)
		}
	}
	if id(1, 1, base, 0, base) != ref {
		t.Error("the rollout identity is not a function of its inputs alone")
	}
}

// TestEmptyRetainedDigestIsNotZero pins that "no retained history" is distinguishable from "I
// could not read the history".
//
// A zero digest is also what an uninitialised column, a failed scan and a truncated value
// read as, so using zero for the empty stream would make absence and failure the same bytes —
// and noticing when history has gone missing is the whole purpose of the retained pair.
func TestEmptyRetainedDigestIsNotZero(t *testing.T) {
	t.Parallel()
	d, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	if d == ([32]byte{}) {
		t.Error("the empty retained stream hashes to 32 zero bytes, which is also what a failed read looks like")
	}
}

// TestPostgresMajorSupportIsARange pins the refusal boundary, including its edges.
//
// The range is a claim about which majors this repository has reasoned about, so both ends
// matter: one past either edge must be refused, and refusing the supported ones would break
// every deployment.
func TestPostgresMajorSupportIsARange(t *testing.T) {
	t.Parallel()
	// THE LIST IS WRITTEN OUT, not derived from the constants under test.
	//
	// Iterating supportedPostgresMajorMin..Max and asserting the predicate agrees is a
	// tautology: raising the maximum to 99 kept it green while declaring support for majors
	// nobody had looked at. A ratified literal is the only form in which widening the range
	// requires a deliberate edit to a SECOND place — this one — which is where the reviewer
	// asks what evidence the new major has.
	//
	// Ratified: 15..18 (an internal design note (not shipped)). All four have
	// been RUN since — see certifiedPostgresMajors for the dated measurement and the predicate
	// comparison it turns on. The sentence that said only 15 had run outlived that measurement by
	// several rounds; verifiedPostgresMajor is the PROVENANCE of the declared strings, which is a
	// different question and the one it still answers.
	ratified := []int{15, 16, 17, 18}
	for _, major := range ratified {
		if !postgresMajorSupported(major) {
			t.Errorf("major %d is ratified but refused", major)
		}
	}
	for _, major := range []int{0, -1, 9, 13, 14, 19, 20, 99, 1000} {
		if postgresMajorSupported(major) {
			t.Errorf("major %d is accepted and is not in the ratified list %v; a range this repository has not reasoned about may carry a structural catalog field the comparator does not read",
				major, ratified)
		}
	}
	// And the constants must express exactly that list, so widening one without the other is
	// itself the failure rather than a silent divergence.
	if supportedPostgresMajorMin != ratified[0] || supportedPostgresMajorMax != ratified[len(ratified)-1] {
		t.Errorf("the declared range is %d..%d and the ratified list is %v",
			supportedPostgresMajorMin, supportedPostgresMajorMax, ratified)
	}
	// The VERIFIED major is the one the catalog projection has actually been executed against,
	// and it has to be inside the range it is the evidence for.
	if !postgresMajorSupported(verifiedPostgresMajor) {
		t.Errorf("the verified major %d is outside the supported range %d..%d",
			verifiedPostgresMajor, supportedPostgresMajorMin, supportedPostgresMajorMax)
	}
}
