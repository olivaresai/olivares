// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"slices"
	"strings"
	"testing"
)

func TestBareCredentialReferencesCountOnlyCredentials(t *testing.T) {
	const session ID = "0192f126-d074-7396-8f58-a2c37e91d06e"
	const issuedID ID = "0192f126-d074-7396-8f58-a2c37e91d06f"
	if !EncodeCredentialID.Valid() || EncodeCredentialID.SeamResolved() {
		t.Fatal("a credential encoding must be valid without claiming to resolve its account")
	}
	for _, tc := range []struct {
		class   PrincipalClass
		counted bool
	}{
		{ClassAuthority, true},
		{ClassObligation, true},
		{ClassEvidence, false},
		{ClassRestrict, false},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			decl := Ref(EncodeCredentialID, tc.class)
			desc := EntityDescriptor{Kind: "credentialfx.permit", Table: "credentialfx_permit", Fields: []FieldSpec{
				{Name: "credential", Kind: KindUUID, Principal: decl},
			}}
			if defects := desc.PrincipalDefects(); len(defects) != 0 {
				t.Fatalf("credential declaration: %v", defects)
			}
			var columns []string
			if tc.counted {
				columns = []string{"credential"}
			}
			if got := desc.CountedColumns(); !slices.Equal(got, columns) {
				t.Fatalf("counted columns = %v, want %v", got, columns)
			}
			for _, credential := range []ID{session, issuedID} {
				rec := Record{"credential": credential.String()}
				if users, err := decl.UserIDs(rec, "credential"); err != nil || len(users) != 0 {
					t.Errorf("credential became a direct account: %v, %v", users, err)
				}
				if users, err := decl.CountedUserIDs(rec, "credential"); err != nil || len(users) != 0 {
					t.Errorf("credential became a counted account: %v, %v", users, err)
				}
				var want []ID
				if tc.counted {
					want = []ID{credential}
				}
				if got := decl.CountedCredentialIDs(rec, "credential"); !slices.Equal(got, want) {
					t.Errorf("counted credentials = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestBareCredentialReferencesRejectNonCanonicalValues(t *testing.T) {
	const id = "0192f126-d074-7396-8f58-a2c37e91d06e"
	decl := Ref(EncodeCredentialID, ClassAuthority)
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"empty", ""},
		{"nil UUID", "00000000-0000-0000-0000-000000000000"},
		{"malformed", "not-an-id"},
		{"uppercase", strings.ToUpper(id)},
		{"URN", "urn:uuid:" + id},
		{"braces", "{" + id + "}"},
		{"unhyphenated", strings.ReplaceAll(id, "-", "")},
		{"whitespace", " " + id},
		{"token prefix", "token:" + id},
		{"non-string", []byte(id)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := Record{"credential": tc.value}
			if ids := decl.CountedCredentialIDs(rec, "credential"); len(ids) != 0 {
				t.Errorf("noncanonical value became a credential: %v", ids)
			}
			if ids, err := decl.CountedUserIDs(rec, "credential"); err == nil || len(ids) != 0 {
				t.Errorf("the retirement reader could mistake malformed data for absence: %v, %v", ids, err)
			}
		})
	}
	for _, rec := range []Record{{}, {"credential": nil}} {
		if ids := decl.CountedCredentialIDs(rec, "credential"); len(ids) != 0 {
			t.Errorf("absent nullable value became a credential: %v", ids)
		}
		if ids, err := decl.CountedUserIDs(rec, "credential"); err != nil || len(ids) != 0 {
			t.Errorf("absent nullable value = %v, %v; want no reference", ids, err)
		}
	}
	for _, class := range []PrincipalClass{ClassEvidence, ClassRestrict} {
		if ids, err := Ref(EncodeCredentialID, class).CountedUserIDs(Record{"credential": "historical"}, "credential"); err != nil || len(ids) != 0 {
			t.Errorf("uncounted %s unexpectedly entered retirement decoding: %v, %v", class, ids, err)
		}
	}
}

func TestCredentialReferencesPreserveUserRefAndRequireARef(t *testing.T) {
	const id ID = "0192f126-d074-7396-8f58-a2c37e91d06e"
	legacy := Ref(EncodeUserRef, ClassAuthority)
	for _, value := range []string{"token:" + id.String(), "token:" + strings.ToUpper(id.String())} {
		if got := legacy.CountedCredentialIDs(Record{"credential": value}, "credential"); !slices.Equal(got, []ID{id}) {
			t.Errorf("existing UserRef behavior changed: %v", got)
		}
	}
	if got := legacy.CountedCredentialIDs(Record{"credential": "user:" + id.String()}, "credential"); len(got) != 0 {
		t.Errorf("a user reference became a credential: %v", got)
	}
	for _, decl := range []*ColumnDecl{nil, Scan(ClassAuthority), Ref(EncodeCredentialID, "")} {
		if got := decl.CountedCredentialIDs(Record{"credential": id.String()}, "credential"); len(got) != 0 {
			t.Errorf("an unclassified or non-Ref declaration counted a credential: %v", got)
		}
	}
}

type credentialDeclKinds struct{}

func (credentialDeclKinds) Kinds() []string { return []string{"credential"} }
func (credentialDeclKinds) Variant(kind string) (*ColumnDecl, bool) {
	return Ref(EncodeCredentialID, ClassAuthority), kind == "credential"
}

func TestCredentialCensusRefusesShapesTheReaderCannotEnumerate(t *testing.T) {
	for name, decl := range map[string]*ColumnDecl{
		"nested": Nested([]string(nil), ClassAuthority, Leaf("[]", Ref(EncodeCredentialID, ""))),
		"union":  Union("kind", credentialDeclKinds{}),
	} {
		t.Run(name, func(t *testing.T) {
			desc := EntityDescriptor{Kind: "credentialfx.permit", Table: "credentialfx_permit", Fields: []FieldSpec{
				{Name: "kind", Kind: KindText, Principal: None("selects the fixture's variant: core/model/credential_decl_test.go:126")},
				{Name: "credential", Kind: KindJSON, Principal: decl},
			}}
			if defects := desc.PrincipalDefects(); len(defects) == 0 {
				t.Fatal("the census admitted a credential shape its reader cannot enumerate")
			}
		})
	}
}
