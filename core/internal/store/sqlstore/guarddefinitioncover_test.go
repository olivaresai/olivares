// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"reflect"
	"strings"
	"testing"
)

// guarddefinitioncover_test.go answers a finding about what a TABLE-DRIVEN test cannot prove.
//
// TestGuardPlanRefusesRatherThanRepairs enumerates a dozen unusable observations and requires
// each to produce a named refusal. Every one of them passes — and deleting the comparison of
// proleakproof from guardDefinitionDiff left all twelve green, because no row in that table
// mutates that field. The same held for every other structural field the table happens not to
// touch. A hand-written table over a fifty-field structure is a sample, and a sample cannot
// say anything about the fields it did not draw.
//
// So the property is asserted the only way it can be: by ENUMERATION OF THE TYPE. Every field
// of guardDefinition, recursively, is mutated in turn, and each mutation must be visible in
// BOTH places a divergence can hide:
//
//   - guardDefinitionDiff, the comparator the plan refuses from; and
//   - definitionDigest, the hash the manifest and every receipt are bound to.
//
// A field added to the structure and forgotten in either one fails this test by construction —
// which is the point. There is no list here to keep in step with the type.

// mutateField returns a value of the same type that differs from v, or false when this test
// does not know how to vary that kind.
//
// The kinds it handles are exactly the ones the definition uses. An unknown kind is a HARD
// failure at the call site rather than a skip: a field this cannot vary is a field this cannot
// cover, and silently passing over it would restore the very blindness the test exists for.
func mutateField(v reflect.Value) (reflect.Value, bool) {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(v.String() + "-mutated").Convert(v.Type()), true
	case reflect.Bool:
		return reflect.ValueOf(!v.Bool()).Convert(v.Type()), true
	case reflect.Int64, reflect.Int, reflect.Int32:
		return reflect.ValueOf(v.Int() + 1).Convert(v.Type()), true
	case reflect.Float64, reflect.Float32:
		return reflect.ValueOf(v.Float() + 1).Convert(v.Type()), true
	case reflect.Struct:
		// optText is the only struct leaf: a NULL becomes a present empty string, and a present
		// value becomes NULL. Both directions matter — a comparator that ignored Valid would
		// read "no WHEN clause" and "an empty WHEN clause" as the same object.
		if v.Type() == reflect.TypeOf(optText{}) {
			o := v.Interface().(optText)
			if o.Valid {
				return reflect.ValueOf(optText{}), true
			}
			return reflect.ValueOf(someText("mutated")), true
		}
	}
	return reflect.Value{}, false
}

// eachLeafField walks a struct and calls fn for every leaf field, with a dotted path.
func eachLeafField(t *testing.T, root reflect.Value, path string, fn func(path string, set func(reflect.Value))) {
	t.Helper()
	rt := root.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		fv := root.Field(i)
		name := path + f.Name
		if fv.Kind() == reflect.Struct && fv.Type() != reflect.TypeOf(optText{}) {
			eachLeafField(t, fv, name+".", fn)
			continue
		}
		idx := i
		fn(name, func(nv reflect.Value) { root.Field(idx).Set(nv) })
		_ = fv
	}
}

// TestGuardDefinitionComparatorAndDigestCoverEveryField is the enumeration.
func TestGuardDefinitionComparatorAndDigestCoverEveryField(t *testing.T) {
	t.Parallel()
	key := guardKey{Schema: guardSchema, Relation: "t", Trigger: "t_immutable"}
	canonical := canonicalGuardDefinition()
	baseline, err := canonical.definitionDigest(key)
	if err != nil {
		t.Fatalf("digest the canonical definition: %v", err)
	}
	if diff := guardDefinitionDiff(canonical, canonical); len(diff) != 0 {
		t.Fatalf("the canonical definition differs from itself: %v", diff)
	}

	covered := 0
	// A pointer to a fresh copy per field, so each mutation is independent.
	eachLeafField(t, reflect.ValueOf(&canonical).Elem(), "", func(path string, _ func(reflect.Value)) {
		mutated := canonicalGuardDefinition()
		holder := reflect.ValueOf(&mutated).Elem()
		target := holder
		for _, part := range strings.Split(path, ".") {
			target = target.FieldByName(part)
		}
		nv, ok := mutateField(target)
		if !ok {
			t.Errorf("%s is of kind %s, which this test cannot vary — so it cannot say whether the comparator or the digest covers it",
				path, target.Kind())
			return
		}
		target.Set(nv)
		covered++

		if diff := guardDefinitionDiff(canonical, mutated); len(diff) == 0 {
			t.Errorf("guardDefinitionDiff reports NO difference when %s changes; a lookalike differing only in that field would be adopted as the canonical guard", path)
		}
		got, derr := mutated.definitionDigest(key)
		if derr != nil {
			t.Fatalf("digest with %s mutated: %v", path, derr)
		}
		if got == baseline {
			t.Errorf("definitionDigest is unchanged when %s changes, so that field is outside the hash every receipt is bound to", path)
		}
	})

	// The count is asserted so a refactor that stops walking part of the structure — a struct
	// leaf that starts being skipped, say — cannot make this test pass by covering less.
	if want := 5 + 14 + 28; covered != want {
		t.Errorf("the walk covered %d fields, want %d; if the definition genuinely gained or lost a field, update this number in the same commit that changes guardDefinitionDiff and the canon writers",
			covered, want)
	}
	t.Logf("GUARD_DEFINITION_COVER|fields=%d", covered)
}

// TestGuardFunctionDiffCoversEveryFunctionField is the same enumeration for the comparator the
// BOOTSTRAP uses.
//
// guardFunctionDiff is a separate function with a separate call site — verifyBootstrapFunction,
// which decides whether a pre-existing olivares_block_mutation may be reused. A field missing
// there means a legacy database's lookalike function is adopted by the whole rollout, which is
// a wider blast radius than a single guard.
func TestGuardFunctionDiffCoversEveryFunctionField(t *testing.T) {
	t.Parallel()
	canonical := canonicalGuardDefinition().Function
	if diff := guardFunctionDiff(canonical, canonical); len(diff) != 0 {
		t.Fatalf("the canonical function differs from itself: %v", diff)
	}
	covered := 0
	rt := reflect.TypeOf(canonical)
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		mutated := canonicalGuardDefinition().Function
		target := reflect.ValueOf(&mutated).Elem().Field(i)
		nv, ok := mutateField(target)
		if !ok {
			t.Errorf("%s is of kind %s, which this test cannot vary", name, target.Kind())
			continue
		}
		target.Set(nv)
		covered++
		if diff := guardFunctionDiff(canonical, mutated); len(diff) == 0 {
			t.Errorf("guardFunctionDiff reports NO difference when %s changes; a pre-existing function differing only in that field would be reused by the bootstrap", name)
		}
	}
	if want := 28; covered != want {
		t.Errorf("the walk covered %d function fields, want %d", covered, want)
	}
	t.Logf("GUARD_FUNCTION_COVER|fields=%d", covered)
}
