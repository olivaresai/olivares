// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// guardshapetotality_test.go is the INDEPENDENT inventory the shape table did not have.
//
// THE HOLE IT CLOSES, measured by round five rather than argued. gateEventFields() and the
// generator's setters were two hand-written lists that walked each other: production read the
// list, the generator read the same list, and a rule declared in gateEventShape was exercised
// through it. That makes the two consistent, which is worth something, and it does NOT make the
// table total — because deleting a DESCRIPTOR removes it from both sides at once. The subtests
// for that field simply stop being generated, the count drops, and every remaining case passes.
// A green suite then reports a field as governed when nothing checks it at all.
//
// Totality needs an inventory the table cannot edit, and there is exactly one: the TYPE. These
// checks walk gateEvent and gateEventShape by reflection and require the hand-written table to
// account for every field of each — so a deleted descriptor leaves a durable field neither
// covered nor exempt, and is named.
//
// AND THE NAMES ARE NOT TAKEN ON TRUST. A `Fields` path that lied would let a descriptor claim
// coverage of a field its closure never reads, which is the same hole wearing a label. Each
// descriptor's paths are therefore SET TOGETHER on a zero event and its predicate must answer
// true, and must answer false on the zero event itself.
//
// Together, not one at a time, and that is a limit rather than an oversight: "a prestate" names
// PrestatePresent and Prestate while its predicate reads only the first, so requiring each path
// to be individually sufficient would reject a correct descriptor. What this catches is a set of
// paths that does not drive its own predicate — the case where a path names the wrong field.

// gateEventStructuralExemptions are the fields of gateEvent that the shape table deliberately
// does not govern, each with the mechanism that governs it instead.
//
// A field may be exempt or covered. It may not be neither, and this map is where "neither"
// becomes a decision somebody had to write down rather than an omission nobody noticed.
var gateEventStructuralExemptions = map[string]string{
	"RolloutID": "the stream's identity; every fold selects by it, so an event of another rollout is not read at all",
	"Kind":      "the AXIS of the table — it selects the shape rather than being constrained by one",

	"Format":           "the edition tuple, compared against the opening by checkGateEventGrammar",
	"CodeEpoch":        "the edition tuple, compared against the opening by checkGateEventGrammar",
	"CodeSHA256":       "the edition tuple, compared against the opening by checkGateEventGrammar",
	"RetainedRevision": "the edition tuple, compared against the opening by checkGateEventGrammar",
	"RetainedSHA256":   "the edition tuple, compared against the opening by checkGateEventGrammar",

	"Phase":     "a VALUE rule, not a presence one: compared against the shape's Phase directly",
	"Condition": "a VALUE rule, not a presence one: compared against the shape's Conditions set",

	"EventOrdinal":    "chain position, verified by the fold against the predecessor",
	"PrevEventSHA256": "chain linkage, verified by the fold",
	"EventSHA256":     "the body digest, recomputed by the fold and compared",
	"RecordedAt":      "a wall clock; nothing may depend on it, so nothing constrains it",
}

// TestGuardGateShapeTableCoversEveryDurableField is the structural check itself.
func TestGuardGateShapeTableCoversEveryDurableField(t *testing.T) {
	t.Parallel()
	fields := gateEventFields()

	covered := map[string]string{}
	for _, f := range fields {
		if len(f.Fields) == 0 {
			t.Errorf("the descriptor %q names no field of gateEvent, so nothing ties its predicate to the record", f.What)
			continue
		}
		for _, path := range f.Fields {
			covered[strings.SplitN(path, ".", 2)[0]] = f.What
		}
	}

	typ := reflect.TypeOf(gateEvent{})
	var uncovered, doubly []string
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		_, isCovered := covered[name]
		_, isExempt := gateEventStructuralExemptions[name]
		switch {
		case !isCovered && !isExempt:
			uncovered = append(uncovered, name)
		case isCovered && isExempt:
			doubly = append(doubly, name)
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("gateEvent has %d field(s) that no descriptor covers and no exemption explains: %v — a field the shape table does not mention is a field NO kind constrains",
			len(uncovered), uncovered)
	}
	if len(doubly) > 0 {
		sort.Strings(doubly)
		t.Errorf("these fields are both covered and exempted, so one of the two statements is stale: %v", doubly)
	}
	// An exemption for a field that no longer exists is a comment about nothing, and it would
	// hide the day that field comes back under a new name.
	for name := range gateEventStructuralExemptions {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("the exemption for %q names a field gateEvent does not have", name)
		}
	}
	t.Logf("GUARD_SHAPE_STRUCTURAL|struct_fields=%d|covered=%d|exempt=%d|descriptors=%d",
		typ.NumField(), len(covered), len(gateEventStructuralExemptions), len(fields))
}

// TestGuardGateShapeFieldPathsMatchTheirPredicates keeps the paths honest.
//
// Without it `Fields` is a second hand-written list that can drift from the closure beside it,
// and a descriptor could claim a field it never reads — restoring the hole under a new name.
func TestGuardGateShapeFieldPathsMatchTheirPredicates(t *testing.T) {
	t.Parallel()
	for _, f := range gateEventFields() {
		t.Run(f.What, func(t *testing.T) {
			t.Parallel()
			var zero gateEvent
			if f.Present(zero) {
				t.Fatalf("the predicate reports %q present on a zero event, so absence is indistinguishable from presence", f.What)
			}
			ev := zero
			v := reflect.ValueOf(&ev).Elem()
			for _, path := range f.Fields {
				target, err := fieldByPath(v, path)
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				setNonZero(target)
			}
			if !f.Present(ev) {
				t.Errorf("setting %v did not make %q present, so the declared paths are not the ones the predicate reads",
					f.Fields, f.What)
			}
		})
	}
}

// TestGuardGateShapeRulesAreAllRead is the same argument from the other side.
//
// gateEventShape carries one fieldRule per governed field. A rule no descriptor returns is a rule
// the fold never consults — declared, maintained, and dead. Deleting a descriptor produces
// exactly that, so this is the second thing that reddens when one goes missing.
func TestGuardGateShapeRulesAreAllRead(t *testing.T) {
	t.Parallel()
	fields := gateEventFields()
	typ := reflect.TypeOf(gateEventShape{})
	ruleType := reflect.TypeOf(fieldForbidden)

	var unread []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Type != ruleType {
			continue // Phase and Conditions are value rules, checked elsewhere.
		}
		// One rule set to required, everything else forbidden: a descriptor that returns
		// required is a descriptor that reads THIS field and no other.
		var shape gateEventShape
		reflect.ValueOf(&shape).Elem().Field(i).Set(reflect.ValueOf(fieldRequired))
		read := false
		for _, d := range fields {
			if d.Rule(shape) == fieldRequired {
				read = true
				break
			}
		}
		if !read {
			unread = append(unread, f.Name)
		}
	}
	if len(unread) > 0 {
		sort.Strings(unread)
		t.Errorf("gateEventShape declares %d rule(s) no descriptor ever reads, so the fold cannot be enforcing them: %v",
			len(unread), unread)
	}
	t.Logf("GUARD_SHAPE_RULES_READ|rules=%d|unread=%d", typ.NumField()-2, len(unread))
}

// TestGuardGateShapeEveryDescriptorHasASetter states the generator's side of the contract here,
// beside the other two, so all three inventories are compared in one place.
func TestGuardGateShapeEveryDescriptorHasASetter(t *testing.T) {
	t.Parallel()
	setters := gateFieldSetters()
	for _, f := range gateEventFields() {
		if _, ok := setters[f.What]; !ok {
			t.Errorf("no setter for %q, so the generator cannot produce its cases and the field is declared but never mutated", f.What)
		}
	}
	for name := range setters {
		found := false
		for _, f := range gateEventFields() {
			if f.What == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the setter %q mutates a field the table no longer declares", name)
		}
	}
}

// fieldByPath walks a dotted path into a struct value.
func fieldByPath(v reflect.Value, path string) (reflect.Value, error) {
	for _, part := range strings.Split(path, ".") {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, errNotAStruct
		}
		f := v.FieldByName(part)
		if !f.IsValid() {
			return reflect.Value{}, errNoSuchField
		}
		v = f
	}
	return v, nil
}

// setNonZero gives a value something distinguishable from its zero, recursively, so a predicate
// that reads any part of it answers true.
func setNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Slice:
		e := reflect.New(v.Type().Elem()).Elem()
		setNonZero(e)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), e))
	case reflect.Array:
		for i := range v.Len() {
			setNonZero(v.Index(i))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				setNonZero(f)
			}
		}
	}
}

var (
	errNotAStruct  = reflectPathError("the path descends into a value that is not a struct")
	errNoSuchField = reflectPathError("no such field")
)

type reflectPathError string

func (e reflectPathError) Error() string { return string(e) }
