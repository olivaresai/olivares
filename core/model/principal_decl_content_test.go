// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"reflect"
	"slices"
	"testing"
)

type contentKinds map[string]*ColumnDecl

func (k contentKinds) Kinds() []string {
	keys := make([]string, 0, len(k))
	for key := range k {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
func (k contentKinds) Variant(kind string) (*ColumnDecl, bool) { v, ok := k[kind]; return v, ok }

type contentOriginal struct {
	Label string `json:"label"`
}
type contentExpanded struct {
	Label string `json:"label"`
	Owner string `json:"owner"`
}

func contentOwner() *ColumnDecl {
	c := Ref(EncodeUserID, ClassAuthority)
	c.ContentErasure = &ContentErasure{Rule: "erase-owned-document: core/model/principal_decl_content_test.go:1", Reader: "fixture"}
	return c
}

func TestContentAndCountedViewsShareAllLeaves(t *testing.T) {
	none := func() *ColumnDecl { return None("display-only label: core/model/principal_decl_content_test.go:1") }
	nested := Nested(contentOriginal{}, ClassAuthority, Leaf("label", none()))
	kinds := contentKinds{"original": Nested(contentOriginal{}, ClassEvidence, Leaf("label", none()))}
	d := EntityDescriptor{Kind: "fixture.document", Fields: []FieldSpec{{Name: "kind", Kind: KindText, Principal: none()}, {Name: "nested", Kind: KindJSON, Principal: nested}, {Name: "union", Kind: KindJSON, Principal: Union("kind", kinds)}}}
	if len(d.CountedColumns()) != 0 || len(d.ContentColumns()) != 0 {
		t.Fatal("unannotated labels appeared in either retirement view")
	}
	// Mutate the actual declared type and leaf table, then the writer's own Union
	// table. New leaves and kinds must reach both views without another inventory.
	nested.Type = reflect.TypeOf(contentExpanded{})
	nested.Leaves = append(nested.Leaves, Leaf("owner", contentOwner()))
	if !slices.Equal(d.CountedColumns(), []string{"nested"}) || !slices.Equal(d.ContentColumns(), []string{"nested"}) {
		t.Fatalf("new nested leaf missing: counted=%v content=%v", d.CountedColumns(), d.ContentColumns())
	}
	kinds["expanded"] = Nested(contentExpanded{}, ClassAuthority, Leaf("label", none()), Leaf("owner", contentOwner()))
	want := []string{"nested", "union"}
	if !slices.Equal(d.CountedColumns(), want) || !slices.Equal(d.ContentColumns(), want) {
		t.Fatalf("new Union variant missing: counted=%v content=%v", d.CountedColumns(), d.ContentColumns())
	}
	if defects := d.PrincipalDefects(); len(defects) != 0 {
		t.Fatalf("complete declaration refused: %v", defects)
	}
	// TypeLeaves uses the same walk, including an annotation inside a counted Union.
	typed := Nested(struct {
		Doc contentExpanded `json:"doc"`
	}{}, ClassAuthority, TypeLeaves(contentExpanded{}, Leaf("label", none()), Leaf("owner", contentOwner())))
	kinds["typed"] = typed
	if !typed.Counted() || !typed.HasContentErasure() || !d.Fields[2].Principal.HasContentErasure() {
		t.Fatal("typed leaves diverged between the counted and content views")
	}
}

func TestContentErasureAndThirdPartyTextDeclarationsFailClosed(t *testing.T) {
	valid := contentOwner()
	if !slices.Equal((EntityDescriptor{Kind: "fixture.doc", Fields: []FieldSpec{{Name: "owner", Kind: KindText, Principal: valid}}}).ContentColumns(), []string{"owner"}) {
		t.Fatal("valid content declaration missing")
	}
	for _, tc := range []struct {
		name string
		decl *ColumnDecl
	}{
		{"missing rule", func() *ColumnDecl { c := contentOwner(); c.ContentErasure.Rule = ""; return c }()},
		{"uncited rule", func() *ColumnDecl { c := contentOwner(); c.ContentErasure.Rule = "erase-owned-document"; return c }()},
		{"missing reader", func() *ColumnDecl { c := contentOwner(); c.ContentErasure.Reader = ""; return c }()},
		{"third-party ref", Ref(EncodeUserID, ClassThirdPartyText)},
		{"third-party erasure", func() *ColumnDecl { c := Scan(ClassThirdPartyText); c.ContentErasure = valid.ContentErasure; return c }()},
		{"unknown payload", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := EntityDescriptor{Kind: "fixture.doc", Fields: []FieldSpec{{Name: "body", Kind: KindText, Principal: tc.decl}}}
			if len(d.PrincipalDefects()) == 0 {
				t.Fatal("incomplete or false erasure claim accepted")
			}
		})
	}
	prose := Scan(ClassThirdPartyText)
	d := EntityDescriptor{Kind: "fixture.doc", Fields: []FieldSpec{{Name: "prose", Kind: KindText, Principal: prose}}}
	if len(d.PrincipalDefects()) != 0 || prose.Counted() || prose.HasContentErasure() || len(d.ContentColumns()) != 0 || !slices.Equal(d.ThirdPartyTextColumns(), []string{"prose"}) {
		t.Fatal("third-party prose became counted or erased")
	}
	user := NewID()
	if ids, err := prose.CountedUserIDs(Record{"prose": "notes about " + user.String()}, "prose"); err != nil || len(ids) != 0 {
		t.Fatalf("text match counted as erasure: %v %v", ids, err)
	}
}

func TestContentViewDoesNotWidenExistingReferenceClasses(t *testing.T) {
	for _, class := range []PrincipalClass{ClassEvidence, ClassRestrict} {
		t.Run(string(class), func(t *testing.T) {
			user := NewID()
			c := Nested(struct {
				Owner string `json:"owner"`
			}{}, class, Leaf("owner", contentOwner()))
			d := EntityDescriptor{Kind: "fixture.existing", Fields: []FieldSpec{{Name: "doc", Kind: KindJSON, Principal: c}}}
			if len(d.PrincipalDefects()) != 0 {
				t.Fatalf("existing valid declaration refused: %v", d.PrincipalDefects())
			}
			if len(d.ContentColumns()) != 1 || len(d.CountedColumns()) != 0 {
				t.Fatalf("content view changed counted scope: content=%v counted=%v", d.ContentColumns(), d.CountedColumns())
			}
			if ids, err := c.CountedUserIDs(Record{"doc": `{"owner":"` + user.String() + `"}`}, "doc"); err != nil || len(ids) != 0 {
				t.Fatalf("existing non-counted enclosing class widened: %v %v", ids, err)
			}
		})
	}
}
