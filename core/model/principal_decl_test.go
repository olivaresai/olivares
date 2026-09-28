// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// declContact holds an account one struct below a declared type's occurrence.
type declContact struct {
	ID string `json:"id"`
}

// declParty is a type whose leaves a TypeLeaves declaration classifies.
type declParty struct {
	Contact declContact `json:"contact"`
	Note    string      `json:"note"`
}

// declDocument names accounts at a slice element, a map value, a map key and a
// field nested below a declared type's occurrence.
type declDocument struct {
	Owners  []string          `json:"owners"`
	ByRole  map[string]string `json:"by_role"`
	Holders map[string]bool   `json:"holders"`
	Party   declParty         `json:"party"`
}

// declPairs has leaves where a kind/ref pair or a Union has no sibling to read.
type declPairs struct {
	Kind    string            `json:"kind"`
	Refs    []string          `json:"refs"`
	ByName  map[string]string `json:"by_name"`
	Configs []json.RawMessage `json:"configs"`
}

// declKinds is a one-kind table.
type declKinds struct{}

func (declKinds) Kinds() []string { return []string{"note"} }

func (declKinds) Variant(kind string) (*ColumnDecl, bool) {
	if kind != "note" {
		return nil, false
	}
	return None("a note, rendered only: core/model/principal_decl.go:232"), true
}

// declNone classifies a leaf that names no account.
func declNone(path string) LeafDecl {
	return Leaf(path, None("a label, rendered only: core/model/principal_decl.go:232"))
}

// TestTheExtractorReadsEveryLeafTheCensusAdmits: an account named at a slice
// element, a map value, a map key or a field below a declared type's occurrence
// passes the census and is found by the extractor the write seam reads, and a
// restricting or evidence leaf stays out of the counted reading.
func TestTheExtractorReadsEveryLeafTheCensusAdmits(t *testing.T) {
	ids := []ID{NewID(), NewID(), NewID(), NewID(), NewID()}
	d := EntityDescriptor{Kind: "declfx.document", Table: "declfx_document", Fields: []FieldSpec{
		{Name: "doc", Kind: KindJSON, Principal: Nested(declDocument{}, ClassAuthority,
			Leaf("owners[]", Ref(EncodeUserID, "")),
			declNone("by_role{key}"),
			Leaf("by_role{}", Ref(EncodeUserRef, "")),
			Leaf("holders{key}", Ref(EncodeUserID, "")),
			TypeLeaves(declParty{},
				Leaf("contact.id", Ref(EncodeUserID, "")),
				declNone("note")),
		)},
		{Name: "list", Kind: KindJSON, Principal: Nested([]string(nil), ClassAuthority, Leaf("[]", Ref(EncodeUserID, "")))},
		{Name: "seen", Kind: KindJSON, Principal: Nested([]string(nil), ClassEvidence, Leaf("[]", Ref(EncodeUserRef, ClassEvidence)))},
	}}
	if defects := d.PrincipalDefects(); len(defects) != 0 {
		t.Fatalf("the census refuses the declarations: %v", defects)
	}
	doc, err := json.Marshal(declDocument{
		Owners:  []string{ids[0].String()},
		ByRole:  map[string]string{"approver": "user:" + ids[1].String()},
		Holders: map[string]bool{ids[2].String(): true},
		Party:   declParty{Contact: declContact{ID: ids[3].String()}, Note: "no account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{
		"doc":  string(doc),
		"list": `["` + ids[4].String() + `"]`,
		"seen": `["user:` + ids[0].String() + `"]`,
	}
	byColumn := map[string]*ColumnDecl{}
	for _, f := range d.Fields {
		byColumn[f.Name] = f.Principal
	}

	got, err := byColumn["doc"].CountedUserIDs(rec, "doc")
	if err != nil {
		t.Fatal(err)
	}
	for i, where := range []string{"a slice element", "a map value", "a map key", "a field below a declared type"} {
		if !slices.Contains(got, ids[i]) {
			t.Errorf("the counted reading misses the account at %s: got %v", where, got)
		}
	}
	if got, err := byColumn["list"].CountedUserIDs(rec, "list"); err != nil || !slices.Equal(got, []ID{ids[4]}) {
		t.Errorf("the counted reading of a list of accounts = %v, %v; want %v", got, err, ids[4])
	}
	if got, err := byColumn["seen"].UserIDs(rec, "seen"); err != nil || !slices.Equal(got, []ID{ids[0]}) {
		t.Errorf("the reading of an evidence list = %v, %v; want %v", got, err, ids[0])
	}
	if got, err := byColumn["seen"].CountedUserIDs(rec, "seen"); err != nil || len(got) != 0 {
		t.Errorf("the counted reading of an evidence list = %v, %v; want nothing", got, err)
	}
}

// TestALeafTheExtractorCannotReadIsRefusedByTheCensus: a kind/ref pair or a
// Union at a slice element or a map value has no sibling there to read its kind
// from, so the census refuses it instead of admitting a leaf whose reading is
// always empty.
func TestALeafTheExtractorCannotReadIsRefusedByTheCensus(t *testing.T) {
	for _, tc := range []struct {
		path string
		leaf LeafDecl
	}{
		{"refs[]", Leaf("refs[]", KindRef("kind", ""))},
		{"by_name{}", Leaf("by_name{}", KindRef("kind", ""))},
		{"configs[]", Leaf("configs[]", Union("kind", declKinds{}))},
	} {
		t.Run(tc.path, func(t *testing.T) {
			leaves := []LeafDecl{tc.leaf}
			for _, p := range []string{"kind", "refs[]", "by_name{key}", "by_name{}", "configs[]"} {
				if p != tc.path {
					leaves = append(leaves, declNone(p))
				}
			}
			d := EntityDescriptor{Kind: "declfx.pairs", Table: "declfx_pairs", Fields: []FieldSpec{
				{Name: "doc", Kind: KindJSON, Principal: Nested(declPairs{}, ClassAuthority, leaves...)},
			}}
			defects := d.PrincipalDefects()
			refused := false
			for _, err := range defects {
				if strings.Contains(err.Error(), `"`+tc.path+`"`) {
					refused = true
				}
			}
			if !refused {
				t.Errorf("the census admits %s at %q, which the extractor cannot read: defects %v", tc.leaf.Decl.Form, tc.path, defects)
			}
		})
	}
}
