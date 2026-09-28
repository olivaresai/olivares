// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// This file declares what every stored text, JSON, bytes and UUID column says
// about principals. A column either holds a reference to an account (Ref), is a
// typed JSON document whose every string leaf is classified (Nested), is a value
// whose Go type a sibling discriminator chooses from a writer's own kind table
// (Union), is text whose positions no type fixes and is matched against every
// alias of an account (Scan), or is declared to name no principal, with the
// reader lines that show it (None).
//
// The declarations are read in three places, and all three read the same
// objects: the completeness check that runs over the closed registry, the write
// seam that refuses an unfenced reference to an account, and the per-module
// retirement steps. A kind table is the writer's own accepted set, so a kind the
// writer accepts and a kind the declaration knows can never differ.

// PrincipalClass is what a stored reference lets the referenced account do, or
// oblige it to, in the tenant that stores it.
type PrincipalClass string

const (
	// ClassAuthority lets the referenced account, or something acting for it, do
	// in the tenant what its role and grants alone would not.
	ClassAuthority PrincipalClass = "authority"
	// ClassObligation makes the referenced account a required party, or keeps its
	// identity for a duty.
	ClassObligation PrincipalClass = "obligation"
	// ClassRestrict only ever narrows what the referenced account may do.
	ClassRestrict PrincipalClass = "restrict"
	// ClassEvidence records what happened; no reader turns it into authority.
	ClassEvidence PrincipalClass = "evidence"
)

// Valid reports whether c is one of the four classes.
func (c PrincipalClass) Valid() bool {
	switch c {
	case ClassAuthority, ClassObligation, ClassRestrict, ClassEvidence:
		return true
	}
	return false
}

// Counted reports whether a reference of this class keeps authority or a duty
// for its account, so a retirement or an absence proof must account for it.
func (c PrincipalClass) Counted() bool { return c == ClassAuthority || c == ClassObligation }

// RefEncoding is how a stored value spells the principal it names.
type RefEncoding string

const (
	// EncodeUserID is a bare account id.
	EncodeUserID RefEncoding = "user-id"
	// EncodeUserRef is "user:<account id>", or "token:<credential id>", which
	// names the account the credential belongs to (CountedCredentialIDs).
	EncodeUserRef RefEncoding = "user-ref"
	// EncodeKindRef is the ref half of a (kind, ref) pair; it names an account
	// when the sibling kind is "user".
	EncodeKindRef RefEncoding = "kind-ref"
	// EncodeEmail is an email address, compared case-insensitively.
	EncodeEmail RefEncoding = "email"
	// EncodeExternalID is a directory's external id for an account.
	EncodeExternalID RefEncoding = "external-id"
	// EncodeIdentity is a row of the tenant's identity roster.
	EncodeIdentity RefEncoding = "identity"
)

// Valid reports whether e is a known encoding.
func (e RefEncoding) Valid() bool {
	switch e {
	case EncodeUserID, EncodeUserRef, EncodeKindRef, EncodeEmail, EncodeExternalID, EncodeIdentity:
		return true
	}
	return false
}

// SeamResolved reports whether the write seam can resolve this encoding to
// account ids by itself. The other encodings name an account through a
// resolving namespace, so their writers resolve them before writing.
func (e RefEncoding) SeamResolved() bool {
	return e == EncodeUserID || e == EncodeUserRef || e == EncodeKindRef
}

// DeclForm is the shape of a column declaration.
type DeclForm uint8

const (
	// FormUndeclared is the zero form: the column says nothing yet.
	FormUndeclared DeclForm = iota
	// FormRef holds a principal reference itself.
	FormRef
	// FormNested is typed JSON whose every string leaf is classified.
	FormNested
	// FormUnion is a value whose Go type a discriminator chooses.
	FormUnion
	// FormScan is text matched against every alias of an account.
	FormScan
	// FormNone names no principal.
	FormNone
)

// String names the form.
func (f DeclForm) String() string {
	switch f {
	case FormRef:
		return "Ref"
	case FormNested:
		return "Nested"
	case FormUnion:
		return "Union"
	case FormScan:
		return "Scan"
	case FormNone:
		return "None"
	}
	return "undeclared"
}

// ColumnDecl declares what one column, JSON leaf or union variant says about
// principals. Build it with Ref, KindRef, Nested, Union, Scan or None.
type ColumnDecl struct {
	// Form is the shape of the declaration.
	Form DeclForm
	// Encoding is how a Ref spells its principal.
	Encoding RefEncoding
	// Class is what the reference lets its account do. A Ref leaf inside a
	// Nested declaration may leave it empty and take the Nested class.
	Class PrincipalClass
	// KindColumn is, for EncodeKindRef, the sibling column (or sibling JSON leaf)
	// holding the kind.
	KindColumn string
	// Type is the Go type a Nested value decodes into.
	Type reflect.Type
	// Leaves classifies the string and opaque leaves of Type.
	Leaves []LeafDecl
	// Discriminator is the sibling column (or sibling JSON leaf) a Union reads
	// its kind from.
	Discriminator string
	// Kinds is the writer's own kind table a Union selects its variant from.
	Kinds KindTable
	// Reason is why no reader resolves a None value to a principal; it cites the
	// reader lines.
	Reason string
	// MemberBound marks a counted Ref whose writers name only current members of
	// the tenant's directory, under the directory fact their transaction already
	// holds (see BoundToMembers). The store honours it only for the columns it
	// names itself.
	MemberBound bool
}

// LeafDecl classifies one leaf path of a Nested type, or every occurrence of a
// Go type inside it whatever the field that holds it is called.
type LeafDecl struct {
	// Path is the leaf's JSON path: field names joined by ".", "[]" for a slice
	// element, "{}" for a map value and "{key}" for a map key.
	Path string
	// Type, when set, makes Leaves apply to every occurrence of this Go type,
	// with paths relative to the occurrence.
	Type reflect.Type
	// Decl classifies the leaf at Path.
	Decl *ColumnDecl
	// Leaves classifies the leaves of Type.
	Leaves []LeafDecl
}

// KindTable is a writer's closed set of discriminator values and the variant
// each one selects. The writer refuses any value it lacks, and a Union
// declaration reads the same object.
type KindTable interface {
	// Kinds lists every value the writer accepts, sorted.
	Kinds() []string
	// Variant returns the declaration of the value a kind selects.
	Variant(kind string) (*ColumnDecl, bool)
}

// Ref declares a column that holds a principal reference itself.
func Ref(enc RefEncoding, class PrincipalClass) *ColumnDecl {
	return &ColumnDecl{Form: FormRef, Encoding: enc, Class: class}
}

// KindRef declares the ref half of a (kind, ref) pair; kindColumn names the
// sibling column or leaf that holds the kind.
func KindRef(kindColumn string, class PrincipalClass) *ColumnDecl {
	return &ColumnDecl{Form: FormRef, Encoding: EncodeKindRef, Class: class, KindColumn: kindColumn}
}

// BoundToMembers returns a copy of the Ref c whose writers name only current
// members of the tenant's directory, under the directory fact they already hold,
// and may name more accounts in one transaction than one fence can pin (a
// fan-out). The census and the retirement steps count it as declared. The store
// grants the exception only for the delivery columns it names, and only in a
// transaction that locked the tenant's directory fact; elsewhere its readiness
// reports the declaration and the write seam reads it as any counted Ref.
func (c *ColumnDecl) BoundToMembers() *ColumnDecl {
	out := *c
	out.MemberBound = true
	return &out
}

// Nested declares typed JSON: sample's Go type, the class of its principal
// leaves, and the classification of every string and opaque leaf.
func Nested(sample any, class PrincipalClass, leaves ...LeafDecl) *ColumnDecl {
	return &ColumnDecl{Form: FormNested, Type: reflect.TypeOf(sample), Class: class, Leaves: leaves}
}

// Union declares a value whose Go type the discriminator's kind chooses from
// kinds.
func Union(discriminator string, kinds KindTable) *ColumnDecl {
	return &ColumnDecl{Form: FormUnion, Discriminator: discriminator, Kinds: kinds}
}

// Scan declares untyped text whose positions no type fixes; it is matched
// against every alias of an account.
func Scan(class PrincipalClass) *ColumnDecl {
	return &ColumnDecl{Form: FormScan, Class: class}
}

// None declares a value no reader resolves to a principal. reason must cite the
// reader or validator lines that show it (file.go:line).
func None(reason string) *ColumnDecl {
	return &ColumnDecl{Form: FormNone, Reason: reason}
}

// Leaf classifies one leaf path of a Nested type.
func Leaf(path string, decl *ColumnDecl) LeafDecl { return LeafDecl{Path: path, Decl: decl} }

// TypeLeaves classifies the leaves of every occurrence of sample's Go type
// inside a Nested type, whatever the field that holds it is called.
func TypeLeaves(sample any, leaves ...LeafDecl) LeafDecl {
	return LeafDecl{Type: reflect.TypeOf(sample), Leaves: leaves}
}

// ErrUnknownKind means a stored or proposed value names a discriminator the
// writer's kind table does not know.
var ErrUnknownKind = errors.New("model: the value names a kind no registry knows")

// noneCitation is the source-line citation a None reason must carry.
var noneCitation = regexp.MustCompile(`[A-Za-z0-9_./-]+\.(go|sql|ts|tsx|json):[0-9]+`)

// DeclaredSQLKind reports whether columns of k must carry a declaration.
func DeclaredSQLKind(k SQLKind) bool {
	return k == KindText || k == KindJSON || k == KindBytes || k == KindUUID
}

// PrincipalDefects returns every defect of d's column declarations: an
// undeclared text, JSON, bytes or UUID column; an unclassified or stale leaf; an
// opaque leaf that is neither a Union, Scan nor None; a Union kind with no
// variant or no class; a None without cited lines. It walks Go types, never
// column names.
func (d EntityDescriptor) PrincipalDefects() []error {
	var out []error
	for _, f := range d.Fields {
		where := string(d.Kind) + "." + f.Name
		if f.Principal == nil || f.Principal.Form == FormUndeclared {
			if DeclaredSQLKind(f.Kind) {
				out = append(out, fmt.Errorf("%s: undeclared %s column", where, sqlKindName(f.Kind)))
			}
			continue
		}
		out = append(out, validateDecl(where, f.Principal, false, func(sibling string) bool {
			_, ok := d.field(sibling)
			return ok
		})...)
	}
	return out
}

// CountedColumns returns every column of d whose declaration can hold a counted
// (AUTHORITY or OBLIGATION) reference, directly, through a Nested leaf, or
// through any variant of a Union.
func (d EntityDescriptor) CountedColumns() []string {
	var out []string
	for _, f := range d.Fields {
		if f.Principal != nil && f.Principal.counted() {
			out = append(out, f.Name)
		}
	}
	return out
}

// Counted reports whether the declaration can hold a counted (AUTHORITY or
// OBLIGATION) reference, directly, through a Nested leaf, or through any variant
// of a Union.
func (c *ColumnDecl) Counted() bool { return c.counted() }

func (c *ColumnDecl) counted() bool {
	if c == nil {
		return false
	}
	switch c.Form {
	case FormRef, FormScan:
		return c.Class.Counted()
	case FormNested:
		if !c.Class.Counted() {
			return false
		}
		return nestedHasPrincipalLeaf(c.Leaves)
	case FormUnion:
		if c.Kinds == nil {
			return false
		}
		for _, k := range c.Kinds.Kinds() {
			if v, ok := c.Kinds.Variant(k); ok && v.counted() {
				return true
			}
		}
	}
	return false
}

func nestedHasPrincipalLeaf(leaves []LeafDecl) bool {
	for _, l := range leaves {
		if l.Type != nil {
			if nestedHasPrincipalLeaf(l.Leaves) {
				return true
			}
			continue
		}
		if l.Decl == nil {
			continue
		}
		switch l.Decl.Form {
		case FormRef, FormScan:
			return true
		case FormUnion:
			if l.Decl.counted() {
				return true
			}
		}
	}
	return false
}

func sqlKindName(k SQLKind) string {
	switch k {
	case KindText:
		return "TEXT"
	case KindJSON:
		return "JSON"
	case KindBytes:
		return "BYTES"
	case KindUUID:
		return "UUID"
	}
	return "typed"
}

// validateDecl checks one declaration. leaf is true for a declaration inside a
// Nested type; hasSibling answers whether a sibling column or leaf exists.
func validateDecl(where string, c *ColumnDecl, leaf bool, hasSibling func(string) bool) []error {
	var out []error
	switch c.Form {
	case FormRef:
		if !c.Encoding.Valid() {
			out = append(out, fmt.Errorf("%s: Ref has no valid encoding", where))
		}
		if !(leaf && c.Class == "") && !c.Class.Valid() {
			out = append(out, fmt.Errorf("%s: Ref has no class", where))
		}
		if c.Encoding == EncodeKindRef {
			if c.KindColumn == "" {
				out = append(out, fmt.Errorf("%s: kind/ref pair names no kind column", where))
			} else if hasSibling != nil && !hasSibling(c.KindColumn) {
				out = append(out, fmt.Errorf("%s: kind column %q does not exist", where, c.KindColumn))
			}
		}
	case FormNested:
		if c.Type == nil {
			out = append(out, fmt.Errorf("%s: Nested names no Go type", where))
			break
		}
		if !c.Class.Valid() {
			out = append(out, fmt.Errorf("%s: Nested has no class", where))
		}
		out = append(out, validateNested(where, c.Type, c.Leaves)...)
	case FormUnion:
		if c.Discriminator == "" {
			out = append(out, fmt.Errorf("%s: Union names no discriminator", where))
		} else if hasSibling != nil && !hasSibling(c.Discriminator) {
			out = append(out, fmt.Errorf("%s: discriminator %q does not exist", where, c.Discriminator))
		}
		if c.Kinds == nil {
			out = append(out, fmt.Errorf("%s: Union names no kind table", where))
			break
		}
		kinds := c.Kinds.Kinds()
		if len(kinds) == 0 {
			out = append(out, fmt.Errorf("%s: Union kind table is empty", where))
		}
		for _, k := range kinds {
			v, ok := c.Kinds.Variant(k)
			if !ok || v == nil || v.Form == FormUndeclared {
				out = append(out, fmt.Errorf("%s: kind %q has no variant", where, k))
				continue
			}
			if v.Form == FormUnion {
				out = append(out, fmt.Errorf("%s: kind %q selects another Union", where, k))
				continue
			}
			out = append(out, validateDecl(where+"<"+k+">", v, false, nil)...)
		}
	case FormScan:
		if !c.Class.Valid() {
			out = append(out, fmt.Errorf("%s: Scan has no class", where))
		}
	case FormNone:
		if !noneCitation.MatchString(c.Reason) {
			out = append(out, fmt.Errorf("%s: None cites no reader lines", where))
		}
	default:
		out = append(out, fmt.Errorf("%s: undeclared", where))
	}
	return out
}

// leafShape is what the type walk found at a path.
type leafShape uint8

const (
	leafString leafShape = iota + 1
	leafOpaque
)

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// validateNested walks t and requires a classification for every string and
// opaque leaf, and no classification for a path t does not have.
func validateNested(where string, t reflect.Type, leaves []LeafDecl) []error {
	var out []error
	byPath := map[string]*ColumnDecl{}
	typed := map[reflect.Type][]LeafDecl{}
	for _, l := range leaves {
		if l.Type != nil {
			typed[l.Type] = l.Leaves
			continue
		}
		if l.Decl == nil {
			out = append(out, fmt.Errorf("%s: leaf %q has no declaration", where, l.Path))
			continue
		}
		byPath[l.Path] = l.Decl
	}
	found := map[string]leafShape{}
	owner := map[string]reflect.Type{}
	walkLeaves(t, "", typed, map[reflect.Type]bool{}, found, owner)
	used := map[string]bool{}
	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		shape := found[p]
		decl := byPath[p]
		if decl == nil {
			if ot, ok := owner[p]; ok {
				decl = typedLeafDecl(typed[ot], p)
			}
		}
		if decl == nil {
			if shape == leafOpaque {
				out = append(out, fmt.Errorf("%s: opaque leaf %q is neither a Union, Scan nor None", where, p))
			} else {
				out = append(out, fmt.Errorf("%s: leaf %q is unclassified", where, p))
			}
			continue
		}
		used[p] = true
		if elementLeaf(p) && readsSibling(decl) {
			out = append(out, fmt.Errorf("%s: leaf %q reads its kind from a sibling, and an element has none", where, p))
			continue
		}
		if shape == leafOpaque && decl.Form != FormUnion && decl.Form != FormScan && decl.Form != FormNone {
			out = append(out, fmt.Errorf("%s: opaque leaf %q must be a Union, Scan or None", where, p))
			continue
		}
		if shape == leafString && decl.Form == FormNested {
			out = append(out, fmt.Errorf("%s: string leaf %q cannot be Nested", where, p))
			continue
		}
		out = append(out, validateDecl(where+"."+p, decl, true, func(sibling string) bool {
			_, ok := found[siblingPath(p, sibling)]
			return ok
		})...)
	}
	for p := range byPath {
		if !used[p] {
			if _, ok := found[p]; !ok {
				out = append(out, fmt.Errorf("%s: declared leaf %q does not exist in %s", where, p, t))
			}
		}
	}
	return out
}

// elementLeaf reports whether p ends at a slice element, a map value or a map
// key: a leaf with no sibling of its own.
func elementLeaf(p string) bool {
	return strings.HasSuffix(p, "[]") || strings.HasSuffix(p, "{}") || strings.HasSuffix(p, "{key}")
}

// readsSibling reports whether reading decl needs a sibling leaf: the kind of a
// kind/ref pair, or the discriminator of a Union.
func readsSibling(decl *ColumnDecl) bool {
	return decl.Form == FormUnion || (decl.Form == FormRef && decl.Encoding == EncodeKindRef)
}

// typedLeafDecl returns the classification a TypeLeaves declaration gives the
// leaf at p, a path inside an occurrence of the declared type.
func typedLeafDecl(leaves []LeafDecl, p string) *ColumnDecl {
	for _, l := range leaves {
		if l.Decl != nil && (p == l.Path || strings.HasSuffix(p, "."+l.Path)) {
			return l.Decl
		}
	}
	return nil
}

// siblingPath resolves a sibling leaf name against a leaf path.
func siblingPath(p, sibling string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[:i+1] + sibling
	}
	return sibling
}

func joinLeaf(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// walkLeaves records every string and opaque leaf of t under path. A struct of
// a type declared through TypeLeaves records its leaves with owner = that type.
func walkLeaves(t reflect.Type, path string, typed map[reflect.Type][]LeafDecl, seen map[reflect.Type]bool, found map[string]leafShape, owner map[string]reflect.Type) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType {
		found[path] = leafOpaque
		return
	}
	switch t.Kind() {
	case reflect.String:
		found[path] = leafString
	case reflect.Interface:
		found[path] = leafOpaque
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			found[path] = leafString
			return
		}
		walkLeaves(t.Elem(), path+"[]", typed, seen, found, owner)
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			found[path+"{key}"] = leafString
		}
		walkLeaves(t.Elem(), path+"{}", typed, seen, found, owner)
	case reflect.Struct:
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		_, declared := typed[t]
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, skip := jsonFieldName(f)
			if skip {
				continue
			}
			next := joinLeaf(path, name)
			if f.Anonymous && f.Tag.Get("json") == "" && f.Type.Kind() == reflect.Struct {
				next = path
			}
			before := len(found)
			walkLeaves(f.Type, next, typed, seen, found, owner)
			if declared && len(found) != before {
				for p := range found {
					if _, has := owner[p]; !has && strings.HasPrefix(p, next) {
						owner[p] = t
					}
				}
			}
		}
	}
}

// jsonFieldName returns the JSON name encoding/json gives f and whether the
// field is skipped.
func jsonFieldName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	name := tag
	if i := strings.IndexByte(tag, ','); i >= 0 {
		name = tag[:i]
	}
	if name == "" {
		name = f.Name
	}
	return name, false
}

// UserIDs returns the account ids the value of column in rec names under c, in
// the encodings the write seam resolves by itself: a bare id, "user:<id>", a
// (kind, ref) pair whose kind is "user", and those encodings at any leaf of a
// Nested or Union value. A value that is not a canonical id cannot name an
// account and yields nothing. A Union value whose kind the table lacks is
// ErrUnknownKind.
func (c *ColumnDecl) UserIDs(rec Record, column string) ([]ID, error) {
	if c == nil {
		return nil, nil
	}
	switch c.Form {
	case FormRef:
		return refUserIDs(c, rec[column], func(sibling string) string { return rec.String(sibling) }), nil
	case FormNested:
		raw, ok := jsonBytes(rec[column])
		if !ok {
			return nil, nil
		}
		return c.nestedUserIDs(raw)
	case FormUnion:
		if rec.IsNull(column) {
			return nil, nil
		}
		kind := rec.String(c.Discriminator)
		v, ok := c.Kinds.Variant(kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
		}
		return v.UserIDs(Record{column: rec[column]}, column)
	}
	return nil, nil
}

// CountedUserIDs is UserIDs restricted to the counted declarations: a Ref,
// Nested leaf or Union variant whose class is EVIDENCE or RESTRICT contributes
// nothing, and a Nested value with no counted leaf is not decoded at all. It is
// what the write seam reads.
func (c *ColumnDecl) CountedUserIDs(rec Record, column string) ([]ID, error) {
	return countedDecl(c).UserIDs(rec, column)
}

// CountedCredentialIDs returns the credential ids the value of column in rec
// names under the counted view of c: a counted Ref in the EncodeUserRef encoding
// whose value is "token:<credential id>". Such a value names the account the
// credential belongs to, which only the auth partition can tell: the write seam
// cannot resolve it, so its writer fences the credential's account, and a
// retirement step matches it against the account's credentials.
func (c *ColumnDecl) CountedCredentialIDs(rec Record, column string) []ID {
	d := countedDecl(c)
	if d == nil || d.Form != FormRef || d.Encoding != EncodeUserRef {
		return nil
	}
	if rest, ok := strings.CutPrefix(rec.String(column), "token:"); ok {
		if id, err := ParseID(rest); err == nil && !id.IsZero() {
			return []ID{id}
		}
	}
	return nil
}

// countedCache memoizes the counted-only view of each declaration: the
// declarations are package-level values that never change after registration.
var countedCache sync.Map // map[*ColumnDecl]*ColumnDecl

// countedDecl returns the counted-only view of c, or nil when c can hold no
// counted reference.
func countedDecl(c *ColumnDecl) *ColumnDecl {
	if c == nil {
		return nil
	}
	if v, ok := countedCache.Load(c); ok {
		return v.(*ColumnDecl)
	}
	out := pruneCounted(c, "")
	countedCache.Store(c, out)
	return out
}

// pruneCounted keeps the parts of c whose class, or the class inherited from
// the enclosing Nested declaration, is counted, and only the forms the seam can
// resolve.
func pruneCounted(c *ColumnDecl, inherited PrincipalClass) *ColumnDecl {
	if c == nil {
		return nil
	}
	class := c.Class
	if class == "" {
		class = inherited
	}
	switch c.Form {
	case FormRef:
		if !class.Counted() || !c.Encoding.SeamResolved() {
			return nil
		}
		return c
	case FormNested:
		if !class.Counted() {
			return nil
		}
		out := *c
		out.Leaves = nil
		for _, l := range c.Leaves {
			if l.Type != nil {
				typed := LeafDecl{Type: l.Type}
				for _, sub := range l.Leaves {
					if d := pruneCounted(sub.Decl, class); d != nil {
						typed.Leaves = append(typed.Leaves, LeafDecl{Path: sub.Path, Decl: d})
					}
				}
				if len(typed.Leaves) > 0 {
					out.Leaves = append(out.Leaves, typed)
				}
				continue
			}
			if d := pruneCounted(l.Decl, class); d != nil {
				out.Leaves = append(out.Leaves, LeafDecl{Path: l.Path, Decl: d})
			}
		}
		if len(out.Leaves) == 0 {
			return nil
		}
		return &out
	case FormUnion:
		if c.Kinds == nil || !c.counted() {
			return nil
		}
		return &ColumnDecl{Form: FormUnion, Discriminator: c.Discriminator, Kinds: countedKinds{c.Kinds}}
	}
	return nil
}

// countedKinds is the counted-only view of a kind table. A kind the table
// lacks stays unknown; a known kind whose variant holds no counted reference
// selects nothing.
type countedKinds struct{ KindTable }

// Variant implements KindTable.
func (k countedKinds) Variant(kind string) (*ColumnDecl, bool) {
	v, ok := k.KindTable.Variant(kind)
	if !ok {
		return nil, false
	}
	return countedDecl(v), true
}

func refUserIDs(c *ColumnDecl, v any, sibling func(string) string) []ID {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	switch c.Encoding {
	case EncodeUserID:
		if id, err := ParseID(s); err == nil && !id.IsZero() {
			return []ID{id}
		}
	case EncodeUserRef:
		if rest, ok := strings.CutPrefix(s, "user:"); ok {
			if id, err := ParseID(rest); err == nil && !id.IsZero() {
				return []ID{id}
			}
		}
	case EncodeKindRef:
		if sibling(c.KindColumn) == "user" {
			if id, err := ParseID(s); err == nil && !id.IsZero() {
				return []ID{id}
			}
		}
	}
	return nil
}

// jsonBytes returns a JSON column value as bytes.
func jsonBytes(v any) ([]byte, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		if x == "" {
			return nil, false
		}
		return []byte(x), true
	case []byte:
		return x, len(x) > 0
	case json.RawMessage:
		return x, len(x) > 0
	default:
		b, err := json.Marshal(x)
		return b, err == nil
	}
}

func (c *ColumnDecl) nestedUserIDs(raw []byte) ([]ID, error) {
	ptr := reflect.New(c.Type)
	if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("model: decode %s: %w", c.Type, err)
	}
	byPath := map[string]*ColumnDecl{}
	typed := map[reflect.Type][]LeafDecl{}
	for _, l := range c.Leaves {
		if l.Type != nil {
			typed[l.Type] = l.Leaves
		} else if l.Decl != nil {
			byPath[l.Path] = l.Decl
		}
	}
	var out []ID
	err := collectUserIDs(ptr.Elem(), "", byPath, typed, nil, &out)
	return out, err
}

// collectUserIDs walks a decoded value along the same paths walkLeaves names,
// and reads each leaf by the declaration validateNested gives it. local holds
// the TypeLeaves of the nearest enclosing declared type.
func collectUserIDs(v reflect.Value, path string, byPath map[string]*ColumnDecl, typed map[reflect.Type][]LeafDecl, local []LeafDecl, out *[]ID) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Interface {
			break
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		// A string reached here is a slice element or a map value, which has no
		// sibling: the census admits only a Ref there that needs none. A struct
		// field's leaf is read below, with its siblings.
		if decl := leafDeclAt(byPath, local, path); decl != nil && decl.Form == FormRef {
			*out = append(*out, refUserIDs(decl, v.String(), noSibling)...)
		}
	case reflect.Slice, reflect.Array:
		if v.Type() == rawMessageType || v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := collectUserIDs(v.Index(i), path+"[]", byPath, typed, local, out); err != nil {
				return err
			}
		}
	case reflect.Map:
		key := leafDeclAt(byPath, local, path+"{key}")
		iter := v.MapRange()
		for iter.Next() {
			if key != nil && key.Form == FormRef && iter.Key().Kind() == reflect.String {
				*out = append(*out, refUserIDs(key, iter.Key().String(), noSibling)...)
			}
			if err := collectUserIDs(iter.Value(), path+"{}", byPath, typed, local, out); err != nil {
				return err
			}
		}
	case reflect.Struct:
		if tl, ok := typed[v.Type()]; ok {
			local = tl
		}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, skip := jsonFieldName(f)
			if skip {
				continue
			}
			next := joinLeaf(path, name)
			if f.Anonymous && f.Tag.Get("json") == "" && f.Type.Kind() == reflect.Struct {
				next = path
			}
			decl := leafDeclAt(byPath, local, next)
			fv := v.Field(i)
			if decl != nil {
				ids, err := leafUserIDs(decl, fv, v)
				if err != nil {
					return err
				}
				*out = append(*out, ids...)
				continue
			}
			if err := collectUserIDs(fv, next, byPath, typed, local, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// leafDeclAt returns the declaration of the leaf at p: the one declared at that
// path, or the one a TypeLeaves declaration of the nearest enclosing declared
// type gives it, matched as validateNested matches it.
func leafDeclAt(byPath map[string]*ColumnDecl, local []LeafDecl, p string) *ColumnDecl {
	if decl := byPath[p]; decl != nil {
		return decl
	}
	return typedLeafDecl(local, p)
}

// noSibling reads the siblings of a leaf that has none.
func noSibling(string) string { return "" }

// leafUserIDs applies a leaf declaration to the field value fv of the struct
// value parent.
func leafUserIDs(decl *ColumnDecl, fv, parent reflect.Value) ([]ID, error) {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return nil, nil
		}
		fv = fv.Elem()
	}
	sibling := func(name string) string {
		t := parent.Type()
		for i := 0; i < t.NumField(); i++ {
			if n, skip := jsonFieldName(t.Field(i)); !skip && n == name {
				if s, ok := parent.Field(i).Interface().(string); ok {
					return s
				}
				if s := parent.Field(i); s.Kind() == reflect.String {
					return s.String()
				}
			}
		}
		return ""
	}
	switch decl.Form {
	case FormRef:
		if fv.Kind() != reflect.String {
			return nil, nil
		}
		return refUserIDs(decl, fv.String(), sibling), nil
	case FormUnion:
		kind := sibling(decl.Discriminator)
		v, ok := decl.Kinds.Variant(kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
		}
		var raw []byte
		switch x := fv.Interface().(type) {
		case json.RawMessage:
			raw = x
		default:
			b, err := json.Marshal(x)
			if err != nil {
				return nil, err
			}
			raw = b
		}
		if len(raw) == 0 || string(raw) == "null" {
			return nil, nil
		}
		return v.UserIDs(Record{"v": string(raw)}, "v")
	}
	return nil, nil
}

// PolicyKindRegistry is the closed set of policy kinds a composition's writers
// accept. The core policy writer refuses any kind it lacks, and the policy spec
// column's Union reads the same object.
type PolicyKindRegistry struct {
	mu    sync.RWMutex
	kinds map[string]*ColumnDecl
}

// PolicyKinds is the composition's policy-kind registry. The modules that write
// policies register their kinds when they are linked in.
var PolicyKinds = &PolicyKindRegistry{}

// Register adds kind with the declaration of its spec. Registering the same
// declaration again is a no-op; a different declaration for a known kind is an
// error.
func (r *PolicyKindRegistry) Register(kind string, variant *ColumnDecl) error {
	if kind == "" || variant == nil {
		return errors.New("model: a policy kind needs a name and a declaration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kinds == nil {
		r.kinds = map[string]*ColumnDecl{}
	}
	if prev, ok := r.kinds[kind]; ok {
		if prev != variant {
			return fmt.Errorf("model: policy kind %q is already registered with another declaration", kind)
		}
		return nil
	}
	r.kinds[kind] = variant
	return nil
}

// Kinds implements KindTable.
func (r *PolicyKindRegistry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Variant implements KindTable.
func (r *PolicyKindRegistry) Variant(kind string) (*ColumnDecl, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.kinds[kind]
	return v, ok
}

// MustRegisterPolicyKind registers kind in PolicyKinds from a package's init
// and panics on a conflicting declaration, which is a composition defect.
func MustRegisterPolicyKind(kind string, variant *ColumnDecl) {
	if err := PolicyKinds.Register(kind, variant); err != nil {
		panic(err)
	}
}
