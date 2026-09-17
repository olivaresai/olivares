// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command overlay-ast reads the overlay sources the live-facts contract judges and
// answers with FACTS ABOUT THEIR SYNTAX TREE, never about their text.
//
// ⛔ WHY IT EXISTS, and it is a correction rather than an addition. Until 2026-09-05 the
// live contract asked its questions with regexes over the source: "does `StatusExpired`
// appear in this body", "does `fromGrants(` appear in that one", "is `addonGate` absent
// from this file". An independent review then produced FIVE ordinary commits, sealed by
// the production sealer, that kept every one of those tokens and changed exactly the
// behaviour the contract claims to attest — and the contract said CLEAN on all five while
// production tests went red:
//
//	294872b  the v3 branch still CALLS fromGrants(p) and always RETURNS fromClaims(p)
//	5295c36  `if len(list) == 0 { return true }` — bought nothing, granted everything
//	9f46c41  `if !h.Enabled && false` — a disabled legal-hold policy reaches approval
//	18f187f  EvaluateOverride gains an os.Getenv("OLIVARES_PAID") refusal, using no
//	         identifier any blacklist named
//	094538b  the expected AIMS call survives verbatim inside `if false`, and the
//	         function returns nil
//
// A second review of the AST correction then proved two more holes in the closed
// surface, not in the five token cases: `setCodePacks` and `packProductID` decide
// which pack `set:ids` and a v3 Identity & Scale grant buy, but were outside the
// digest; a missing reader target plus a missing acta key could shrink the set;
// and two parseable shapes (empty combinedPurchaseView closure, bare
// `return` in durableLicensed) panicked the reader so the caller reported 2.
//
// The lesson is not "add those strings to the blacklist". A token census cannot
// answer a question about CONTROL FLOW, and every blacklist is one ordinary refactor
// behind the code it guards.
//
// ⛔ SO THIS READER ANSWERS TWO KINDS OF QUESTION, AND BOTH ARE STRUCTURAL:
//
//  1. NAMED PREDICATES over the real tree (go/parser + go/ast). Every named
//     predicate is total over every parseable Go AST: it never indexes a missing
//     statement or result. An unknown but parseable shape is a failed predicate
//     in a normal report (caller 1). Only unreadable or unparseable source is 2.
//  2. A CLOSED FORM. Each reviewed node is serialised together with the file's
//     import bindings (a qualified selector whose package path changed is a
//     different decision) into a canonical, position-free, comment-free rendering
//     and hashed. The acta records the digest of the form a human reviewed. The
//     node key set is frozen here and cannot shrink because source and acta omit
//     the same name.
//
// ⛔ TWO SCHEMA IDENTIFIERS, ON PURPOSE. The REPORT is overlay-ast/v2: its node union
// gained `durableLicensedAt` (ten to eleven keys) and it names the durable construction
// it judged. The DIGEST PREIMAGE stays overlay-ast/v1 (`digestSchema`), so every
// declaration whose subtree and imports did not change keeps its reviewed hash byte for
// byte. Changing the report schema must never silently re-frame the digests.
//
// ⛔ TWO WHOLE DURABLE CONSTRUCTIONS, SELECTED BY STRUCTURE (r116 durable-facts successor):
//
//   - legacy-direct-v1: `durableLicensed` holds the direct embedded-key selection, and
//     `durableLicensedAt` is ABSENT. Its nine predicates are the original ones, unchanged.
//   - observed-crl-keyring-v1: `durableLicensed` is the exact forwarding wrapper and
//     `durableLicensedAt` holds the connected-keyring holder, the shared clock, the
//     canonical observed-revocation gate and then the unchanged purchase composition. The
//     same nine predicate IDs are evaluated over that complete call graph.
//
// The reader only names the construction it found. The evaluator requires the acta's
// COMPLETE reviewed profile of that same construction — digests, imports, required
// absence and support-file bindings — so one construction's digest can never be paired
// with the other's helper, imports or support.
//
// The second is what makes the contract honest: predicate sets are always incomplete, so
// the answer to "a readable structure I was not taught to verify" must be 1, never 0. A
// comment or a gofmt pass changes neither the predicates nor the digest, because comments
// are not parsed into the tree and positions are excluded from the serialisation.
//
// The reader NEVER decides. It reports; check-overlay-live-facts.sh compares against the
// acta and owns the 0/1/2.
//
// Usage: overlay-ast <dir>   — <dir> holds the overlay blobs at their overlay-relative paths.
// Exit:  0 report written to stdout · 2 a source could not be read or parsed (never a verdict).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// readerSchema identifies the REPORT. digestSchema frames the declaration digest preimage
// and is deliberately independent of it: see the package comment.
const (
	readerSchema = "overlay-ast/v2"
	digestSchema = "overlay-ast/v1"
)

// The durable-selection constructions this reader recognizes. Adding another is an
// explicit reviewed profile here and in the acta, never an alternative accepted per node.
const (
	constructionLegacy  = "legacy-direct-v1"
	constructionCurrent = "observed-crl-keyring-v1"
)

// The overlay paths this contract reads. Named once, here, and echoed in the report so a
// caller cannot silently ask about a file this reader never opened.
const (
	pLegalHold = "enterprise/rtbf/legalhold.go"
	pDurable   = "cmd-overlay/olivares/durablebus_enterprise.go"
	pPacks     = "cmd-overlay/olivares/addonpacks_enterprise.go"
	pCatalog   = "enterprise/activation/catalog.go"
	pCut       = "cmd-overlay/olivares/wire_enterprise_addon_cp.go"
	pStub      = "cmd-overlay/olivares/wire_enterprise_noaddon_cp.go"
)

var wantedFiles = []string{pLegalHold, pDurable, pPacks, pCatalog, pCut, pStub}

// Frozen reviewed-node union. The caller requires this exact key set in every report;
// each construction then says which keys must be present and which one must be absent.
// Omitting a name from both source and acta cannot shrink it.
type reviewedTarget struct {
	path, name, kind string // kind is "func" or "var"
}

var reviewedTargets = []reviewedTarget{
	{pLegalHold, "LegalHoldOverride.EvaluateOverride", "func"},
	{pDurable, "durableLicensed", "func"},
	{pDurable, "durableLicensedAt", "func"},
	{pPacks, "combinedPurchaseView", "func"},
	{pPacks, "purchaseViewFromGrants", "func"},
	{pPacks, "purchaseViewFromClaims", "func"},
	{pPacks, "attestedPacks", "func"},
	{pPacks, "packProductID", "func"},
	{pPacks, "setCodePacks", "var"},
	{pCut, "newAIMSPackager", "func"},
	{pStub, "newAIMSPackager", "func"},
}

func surfaceKeys() []string {
	out := make([]string, len(reviewedTargets))
	for i, t := range reviewedTargets {
		out[i] = t.path + "#" + t.name
	}
	return out
}

var (
	reviewedSetCodePacks = map[string]string{
		"biz":  "activation.PackBusiness",
		"reg":  "activation.PackRegulated",
		"airs": "activation.PackAIRuntimeSecurity",
		"cp":   "activation.PackCompliancePacks",
		"ids":  "activation.PackIdentityScale",
		"ent":  "activation.PackEnterprise",
	}
	reviewedPackProductIDs = map[string]string{
		"activation.PackBusiness":          "self_hosted.business",
		"activation.PackRegulated":         "self_hosted.business.addons.regulated",
		"activation.PackAIRuntimeSecurity": "self_hosted.business.addons.ai-runtime-security",
		"activation.PackCompliancePacks":   "self_hosted.business.addons.compliance-packs",
		"activation.PackIdentityScale":     "self_hosted.business.addons.identity-scale",
		"activation.PackEnterprise":        "self_hosted.enterprise",
	}
)

type check struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type nodeReport struct {
	Kind   string `json:"kind"`
	Found  bool   `json:"found"`
	Digest string `json:"digest,omitempty"`
}

type importBind struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type fileReport struct {
	Build   string       `json:"build"`
	Imports []importBind `json:"imports"`
}

type catalogRow struct {
	Key  string `json:"key"`
	Pack string `json:"pack"`
	Kind string `json:"kind"`
	Disp string `json:"disp"`
}

type report struct {
	Schema         string                `json:"schema"`
	DigestSchema   string                `json:"digestSchema"`
	Construction   string                `json:"construction"`
	Surface        []string              `json:"surface"`
	Files          map[string]fileReport `json:"files"`
	Nodes          map[string]nodeReport `json:"nodes"`
	Checks         map[string]check      `json:"checks"`
	Catalog        []catalogRow          `json:"catalog"`
	PackConsts     map[string]string     `json:"packConsts"`
	SetCodePacks   map[string]string     `json:"setCodePacks"`
	PackProductIDs map[string]string     `json:"packProductIDs"`
	Notes          []string              `json:"notes"`
}

func die(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, "overlay-ast: "+msg+"\n", args...)
	os.Exit(2)
}

func main() {
	if len(os.Args) != 2 {
		die("usage: overlay-ast <dir>")
	}
	dir := os.Args[1]

	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	rep := report{
		Schema:         readerSchema,
		DigestSchema:   digestSchema,
		Surface:        surfaceKeys(),
		Files:          map[string]fileReport{},
		Nodes:          map[string]nodeReport{},
		Checks:         map[string]check{},
		PackConsts:     map[string]string{},
		SetCodePacks:   map[string]string{},
		PackProductIDs: map[string]string{},
	}

	for _, rel := range wantedFiles {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			die("cannot read %s: %v", rel, err)
		}
		// ⚠ Mode 0, NOT ParseComments, and that is the whole reason a comment cannot move
		// this contract: with comments left out of the tree there is nothing for a decoy to
		// attach to. The build constraint is read separately, from the raw bytes, because it
		// IS a directive and not prose.
		f, err := parser.ParseFile(fset, rel, raw, 0)
		if err != nil {
			die("cannot parse %s: %v", rel, err)
		}
		files[rel] = f
		rep.Files[rel] = fileReport{Build: buildTag(raw), Imports: fileImports(f)}
	}

	decls := map[string]*ast.FuncDecl{}
	vars := map[string]*ast.ValueSpec{}
	for _, t := range reviewedTargets {
		key := t.path + "#" + t.name
		imps := rep.Files[t.path].Imports
		switch t.kind {
		case "func":
			d := findFunc(files[t.path], t.name)
			if d == nil {
				rep.Nodes[key] = nodeReport{Kind: t.kind, Found: false}
				continue
			}
			decls[key] = d
			digest, err := digestOf(imps, d)
			if err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("%s: closed form: %v", key, err))
				rep.Nodes[key] = nodeReport{Kind: t.kind, Found: true}
				continue
			}
			rep.Nodes[key] = nodeReport{Kind: t.kind, Found: true, Digest: digest}
		case "var":
			vs := findValueSpec(files[t.path], t.name)
			if vs == nil {
				rep.Nodes[key] = nodeReport{Kind: t.kind, Found: false}
				continue
			}
			vars[key] = vs
			digest, err := digestOf(imps, vs)
			if err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("%s: closed form: %v", key, err))
				rep.Nodes[key] = nodeReport{Kind: t.kind, Found: true}
				continue
			}
			rep.Nodes[key] = nodeReport{Kind: t.kind, Found: true, Digest: digest}
		default:
			die("internal: unknown reviewed kind %q for %s", t.kind, key)
		}
	}

	checkDurable(&rep, decls[pDurable+"#durableLicensed"], decls[pDurable+"#durableLicensedAt"])
	checkCombined(&rep, decls[pPacks+"#combinedPurchaseView"])
	checkFromGrants(&rep, decls[pPacks+"#purchaseViewFromGrants"])
	checkFromClaims(&rep, decls[pPacks+"#purchaseViewFromClaims"])
	checkLegalHold(&rep, decls[pLegalHold+"#LegalHoldOverride.EvaluateOverride"])
	checkAIMS(&rep, decls[pCut+"#newAIMSPackager"], decls[pStub+"#newAIMSPackager"])
	checkSetCodePacks(&rep, files[pPacks], vars[pPacks+"#setCodePacks"])
	checkPackProductID(&rep, decls[pPacks+"#packProductID"])
	readCatalog(&rep, files[pCatalog])

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		die("cannot render the report: %v", err)
	}
	os.Stdout.Write(append(out, '\n'))
}

func buildTag(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "package ") {
			return ""
		}
		if !constraint.IsGoBuild(s) {
			continue
		}
		if _, err := constraint.Parse(s); err != nil {
			return ""
		}
		return strings.Join(strings.Fields(strings.TrimSpace(strings.TrimPrefix(s, "//go:build"))), " ")
	}
	return ""
}

func fileImports(f *ast.File) []importBind {
	if f == nil {
		return []importBind{}
	}
	out := make([]importBind, 0, len(f.Imports))
	for _, is := range f.Imports {
		if is == nil || is.Path == nil {
			continue
		}
		path, err := strconv.Unquote(is.Path.Value)
		if err != nil {
			path = is.Path.Value
		}
		name := ""
		if is.Name != nil {
			name = is.Name.Name
		} else if i := strings.LastIndex(path, "/"); i >= 0 {
			name = path[i+1:]
		} else {
			name = path
		}
		out = append(out, importBind{Name: name, Path: path})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func findFunc(f *ast.File, name string) *ast.FuncDecl {
	if f == nil {
		return nil
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if funcName(fd) == name {
			return fd
		}
	}
	return nil
}

func findValueSpec(f *ast.File, name string) *ast.ValueSpec {
	if f == nil {
		return nil
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
			continue
		}
		for _, sp := range gd.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if n != nil && n.Name == name {
					return vs
				}
			}
		}
	}
	return nil
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return exprTypeName(fd.Recv.List[0].Type) + "." + fd.Name.Name
}

func exprTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		if t == nil {
			return ""
		}
		return exprTypeName(t.X)
	case *ast.Ident:
		if t == nil {
			return ""
		}
		return t.Name
	case *ast.SelectorExpr:
		if t == nil {
			return ""
		}
		return exprTypeName(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		if t == nil {
			return ""
		}
		return exprTypeName(t.X)
	}
	return ""
}

// digestOf serialises import bindings plus a declaration's entire subtree into a
// canonical string and hashes it. digestSchema (overlay-ast/v1) is part of the preimage
// so a framing change cannot collide with a previously reviewed form; it is NOT the
// report schema, which may evolve without re-framing reviewed declarations.
//
// ⛔ IT IS DONE BY REFLECTION, NOT BY A HAND-WRITTEN SWITCH, and that is a deliberate
// choice about blind spots: a switch over the node kinds I remembered would silently
// serialise an unlisted kind as nothing. Reflection visits every exported field of
// every node, so a construct nobody anticipated still changes the digest. Unsupported
// reflect kinds return an error instead of formatting with %v.
//
// Excluded, and only these: token.Pos, *ast.Object / *ast.Scope, and *ast.CommentGroup.
func digestOf(imports []importBind, n ast.Node) (string, error) {
	var b strings.Builder
	b.WriteString(digestSchema)
	b.WriteByte('\n')
	writeImports(&b, imports)
	b.WriteByte('\n')
	if err := serialise(&b, reflect.ValueOf(n)); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

func writeImports(b *strings.Builder, imps []importBind) {
	b.WriteString("imports[")
	b.WriteString(strconv.Itoa(len(imps)))
	b.WriteByte('|')
	for i, im := range imps {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Quote(im.Name))
		b.WriteString("=>")
		b.WriteString(strconv.Quote(im.Path))
	}
	b.WriteByte(']')
}

var (
	posType     = reflect.TypeOf(token.Pos(0))
	objType     = reflect.TypeOf((*ast.Object)(nil))
	scopeType   = reflect.TypeOf((*ast.Scope)(nil))
	commentType = reflect.TypeOf((*ast.CommentGroup)(nil))
)

func reflectTypeName(t reflect.Type) string {
	if t == nil {
		return "nil"
	}
	if t.PkgPath() != "" && t.Name() != "" {
		return t.PkgPath() + "." + t.Name()
	}
	return t.String()
}

func serialise(b *strings.Builder, v reflect.Value) error {
	if !v.IsValid() {
		b.WriteString("nil")
		return nil
	}
	switch v.Type() {
	case posType, objType, scopeType, commentType:
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return nil
		}
		b.WriteByte('*')
		b.WriteString(reflectTypeName(v.Type().Elem()))
		b.WriteByte(':')
		return serialise(b, v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return nil
		}
		dyn := v.Elem()
		b.WriteByte('!')
		b.WriteString(reflectTypeName(dyn.Type()))
		b.WriteByte(':')
		return serialise(b, dyn)
	case reflect.Struct:
		t := v.Type()
		b.WriteByte('(')
		b.WriteString(reflectTypeName(t))
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			switch f.Type {
			case posType, objType, scopeType, commentType:
				continue
			}
			b.WriteByte(' ')
			b.WriteString(f.Name)
			b.WriteByte('=')
			if err := serialise(b, v.Field(i)); err != nil {
				return err
			}
		}
		b.WriteByte(')')
		return nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("nil")
			return nil
		}
		b.WriteByte('[')
		b.WriteString(strconv.Itoa(v.Len()))
		b.WriteByte('|')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			if err := serialise(b, v.Index(i)); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	case reflect.Map:
		if v.IsNil() {
			b.WriteString("nil")
			return nil
		}
		type pair struct{ k, val string }
		items := make([]pair, 0, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			var kb, vb strings.Builder
			if err := serialise(&kb, iter.Key()); err != nil {
				return err
			}
			if err := serialise(&vb, iter.Value()); err != nil {
				return err
			}
			items = append(items, pair{kb.String(), vb.String()})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].k != items[j].k {
				return items[i].k < items[j].k
			}
			return items[i].val < items[j].val
		})
		b.WriteByte('{')
		b.WriteString(strconv.Itoa(len(items)))
		b.WriteByte('|')
		for i, it := range items {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(it.k)
			b.WriteString("=>")
			b.WriteString(it.val)
		}
		b.WriteByte('}')
		return nil
	case reflect.String:
		b.WriteString(strconv.Quote(v.String()))
		return nil
	case reflect.Bool:
		if v.Bool() {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(v.Type().String())
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(v.Int(), 10))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(v.Type().String())
		b.WriteByte(':')
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
		return nil
	default:
		return fmt.Errorf("unsupported kind %s of type %s", v.Kind(), v.Type())
	}
}

func set(rep *report, name string, ok bool, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	if ok && name != "durable.pack" {
		detail = ""
	}
	rep.Checks[name] = check{OK: ok, Detail: detail}
}

func failNamed(rep *report, names []string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, n := range names {
		set(rep, n, false, "%s", msg)
	}
}

func render(e ast.Expr) string {
	if e == nil {
		return ""
	}
	switch t := e.(type) {
	case *ast.Ident:
		if t == nil {
			return ""
		}
		return t.Name
	case *ast.BasicLit:
		if t == nil {
			return ""
		}
		return t.Value
	case *ast.SelectorExpr:
		if t == nil || t.Sel == nil {
			return ""
		}
		return render(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		if t == nil {
			return ""
		}
		return "*" + render(t.X)
	case *ast.UnaryExpr:
		if t == nil {
			return ""
		}
		return t.Op.String() + render(t.X)
	case *ast.BinaryExpr:
		if t == nil {
			return ""
		}
		return render(t.X) + " " + t.Op.String() + " " + render(t.Y)
	case *ast.ParenExpr:
		if t == nil {
			return ""
		}
		return "(" + render(t.X) + ")"
	case *ast.IndexExpr:
		if t == nil {
			return ""
		}
		return render(t.X) + "[" + render(t.Index) + "]"
	case *ast.CallExpr:
		if t == nil {
			return ""
		}
		var as []string
		for _, a := range t.Args {
			as = append(as, render(a))
		}
		return render(t.Fun) + "(" + strings.Join(as, ", ") + ")"
	case *ast.CompositeLit:
		if t == nil {
			return ""
		}
		return render(t.Type) + "{…}"
	case *ast.FuncLit:
		return "func(…){…}"
	case *ast.ArrayType:
		if t == nil {
			return ""
		}
		return "[]" + render(t.Elt)
	case *ast.MapType:
		if t == nil {
			return ""
		}
		return "map[" + render(t.Key) + "]" + render(t.Value)
	}
	return reflect.TypeOf(e).String()
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id != nil && id.Name == name
}

func isCallOf(e ast.Expr, name string) (*ast.CallExpr, bool) {
	c, ok := e.(*ast.CallExpr)
	if !ok || c == nil {
		return nil, false
	}
	return c, render(c.Fun) == name
}

func isCallWithIdentArg(e ast.Expr, name, arg string) bool {
	c, ok := isCallOf(e, name)
	return ok && len(c.Args) == 1 && isIdent(c.Args[0], arg)
}

func singleReturn(b *ast.BlockStmt) *ast.ReturnStmt {
	if b == nil || len(b.List) != 1 {
		return nil
	}
	r, _ := b.List[0].(*ast.ReturnStmt)
	return r
}

func returnsLiteral(b *ast.BlockStmt, lit string) bool {
	r := singleReturn(b)
	return r != nil && len(r.Results) == 1 && isIdent(r.Results[0], lit)
}

func bodyOfReturnedFuncLit(fd *ast.FuncDecl) *ast.BlockStmt {
	if fd == nil || fd.Body == nil {
		return nil
	}
	for _, st := range fd.Body.List {
		r, ok := st.(*ast.ReturnStmt)
		if !ok || len(r.Results) != 1 {
			continue
		}
		if fl, ok := r.Results[0].(*ast.FuncLit); ok && fl != nil {
			return fl.Body
		}
	}
	return nil
}

func allReturns(n ast.Node) []*ast.ReturnStmt {
	var out []*ast.ReturnStmt
	if n == nil {
		return out
	}
	ast.Inspect(n, func(x ast.Node) bool {
		if r, ok := x.(*ast.ReturnStmt); ok {
			out = append(out, r)
		}
		return true
	})
	return out
}

func stmtKinds(b *ast.BlockStmt) []string {
	if b == nil {
		return nil
	}
	var out []string
	for _, s := range b.List {
		out = append(out, strings.TrimPrefix(reflect.TypeOf(s).String(), "*ast."))
	}
	return out
}

func renderReturn(r *ast.ReturnStmt) string {
	if r == nil {
		return "return"
	}
	if len(r.Results) == 0 {
		return "bare return"
	}
	var parts []string
	for _, e := range r.Results {
		parts = append(parts, render(e))
	}
	return strings.Join(parts, ", ")
}

var durableChecks = []string{
	"durable.present",
	"durable.denies_are_false",
	"durable.selection_is_composition",
	"durable.pack",
	"durable.term_guard_denies",
	"durable.resolve_and_blob_denies",
	"durable.invalid_key_denies",
	"durable.claims_are_read",
	"durable.statement_sequence_is_reviewed",
}

// checkDurable selects the durable construction by STRUCTURE and evaluates the nine
// durable predicates over that construction's complete call graph. The helper's presence
// is the only discriminant: a current wrapper without its helper is judged, and fails, as
// legacy; a legacy body beside a present helper is judged, and fails, as current. Neither
// half of one construction can borrow the other's acceptance.
func checkDurable(rep *report, wrapper, helper *ast.FuncDecl) {
	if helper == nil {
		rep.Construction = constructionLegacy
		checkDurableLegacy(rep, wrapper)
		return
	}
	rep.Construction = constructionCurrent
	checkDurableObservedCRL(rep, wrapper, helper)
}

// compositionPack answers whether a selection call is the reviewed purchase composition
// over ONE holder, `combinedPurchaseView(holder.claims, holder.grants)(activation.<Pack>)`,
// and which pack it selects.
func compositionPack(selection *ast.CallExpr) (string, bool) {
	if selection == nil {
		return "", false
	}
	if inner, ok := isCallOf(selection.Fun, "combinedPurchaseView"); ok &&
		len(inner.Args) == 2 &&
		render(inner.Args[0]) == "holder.claims" && render(inner.Args[1]) == "holder.grants" &&
		len(selection.Args) == 1 {
		if sel, ok := selection.Args[0].(*ast.SelectorExpr); ok && isIdent(sel.X, "activation") {
			return sel.Sel.Name, true
		}
	}
	return "", false
}

// checkDurableLegacy is the original legacy-direct-v1 program, unchanged in behavior: the
// direct embedded-key guard, holder, `Status(time.Now())` term and purchase composition.
func checkDurableLegacy(rep *report, fd *ast.FuncDecl) {
	if fd == nil || fd.Body == nil {
		failNamed(rep, durableChecks, "func durableLicensed is absent")
		return
	}
	set(rep, "durable.present", true, "")

	rets := allReturns(fd.Body)
	var selection *ast.CallExpr
	extra := []string{}
	for _, r := range rets {
		if r == nil {
			extra = append(extra, "nil return")
			continue
		}
		if len(r.Results) == 0 {
			extra = append(extra, "bare return")
			continue
		}
		if len(r.Results) == 1 && isIdent(r.Results[0], "false") {
			continue
		}
		if len(r.Results) == 1 {
			if c, ok := r.Results[0].(*ast.CallExpr); ok && selection == nil {
				selection = c
				continue
			}
		}
		extra = append(extra, renderReturn(r))
	}
	set(rep, "durable.denies_are_false", len(extra) == 0,
		"returns that are neither `false` nor the selection: %s", strings.Join(extra, " · "))

	pack, okSel := compositionPack(selection)
	detail := "no selection call at all"
	if selection != nil {
		detail = render(selection)
	}
	set(rep, "durable.selection_is_composition", okSel, "selection is %s", detail)
	set(rep, "durable.pack", okSel, "%s", pack)

	termOK, termWhy := false, "no `if !ok || …StatusExpired { return false }` guard"
	for _, st := range fd.Body.List {
		ifs, ok := st.(*ast.IfStmt)
		if !ok {
			continue
		}
		bin, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.LOR {
			continue
		}
		un, ok := bin.X.(*ast.UnaryExpr)
		if !ok || un.Op != token.NOT || !isIdent(un.X, "ok") {
			continue
		}
		cmp, ok := bin.Y.(*ast.BinaryExpr)
		if !ok || cmp.Op != token.EQL {
			termWhy = "the second term of the guard is " + render(bin.Y)
			continue
		}
		sel, ok := cmp.Y.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "StatusExpired" {
			termWhy = "the guard compares against " + render(cmp.Y)
			continue
		}
		if _, ok := cmp.X.(*ast.CallExpr); !ok {
			termWhy = "the guard's left side is " + render(cmp.X)
			continue
		}
		if !returnsLiteral(ifs.Body, "false") {
			termWhy = "the term guard does not deny"
			continue
		}
		termOK, termWhy = true, ""
		break
	}
	set(rep, "durable.term_guard_denies", termOK, "%s", termWhy)

	resolveOK, keyOK, claimsOK := false, false, false
	for _, st := range fd.Body.List {
		switch s := st.(type) {
		case *ast.IfStmt:
			if isResolveOrEmptyBlobDeny(s) {
				resolveOK = true
			}
			if isInvalidKeyDeny(s) {
				keyOK = true
			}
		case *ast.AssignStmt:
			if isClaimsRead(s) {
				claimsOK = true
			}
		}
	}
	set(rep, "durable.resolve_and_blob_denies", resolveOK,
		"no `if err != nil || strings.TrimSpace(src.Blob) == \"\" { return false }`")
	set(rep, "durable.invalid_key_denies", keyOK,
		"no `if len(pub) != ed25519.PublicKeySize { return false }`")
	set(rep, "durable.claims_are_read", claimsOK,
		"holder.claims() is not assigned to `(c, ok)`")

	kinds := strings.Join(stmtKinds(fd.Body), ",")
	const reviewed = "AssignStmt,IfStmt,AssignStmt,IfStmt,AssignStmt,AssignStmt,IfStmt,ReturnStmt"
	set(rep, "durable.statement_sequence_is_reviewed", kinds == reviewed,
		"estructura no verificada: durableLicensed top-level statements are %s", kinds)
}

func isResolveOrEmptyBlobDeny(ifs *ast.IfStmt) bool {
	if ifs == nil {
		return false
	}
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.LOR {
		return false
	}
	left, ok := bin.X.(*ast.BinaryExpr)
	if !ok || left.Op != token.NEQ || !isIdent(left.X, "err") || !isIdent(left.Y, "nil") {
		return false
	}
	right, ok := bin.Y.(*ast.BinaryExpr)
	if !ok || right.Op != token.EQL {
		return false
	}
	call, ok := isCallOf(right.X, "strings.TrimSpace")
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || !isIdent(sel.X, "src") || sel.Sel.Name != "Blob" {
		return false
	}
	lit, ok := right.Y.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || s != "" {
		return false
	}
	return returnsLiteral(ifs.Body, "false")
}

func isInvalidKeyDeny(ifs *ast.IfStmt) bool {
	if ifs == nil {
		return false
	}
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	call, ok := isCallOf(bin.X, "len")
	if !ok || len(call.Args) != 1 || !isIdent(call.Args[0], "pub") {
		return false
	}
	if render(bin.Y) != "ed25519.PublicKeySize" {
		return false
	}
	return returnsLiteral(ifs.Body, "false")
}

func isClaimsRead(as *ast.AssignStmt) bool {
	if as == nil || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
		return false
	}
	if !isIdent(as.Lhs[0], "c") || !isIdent(as.Lhs[1], "ok") {
		return false
	}
	_, ok := isCallOf(as.Rhs[0], "holder.claims")
	return ok
}

const (
	durableWrapperSignature = "(licenseFile, dataDir string, getenv func(string) string) bool"
	durableHelperSignature  = "(licenseFile, dataDir string, getenv func(string) string, now func() time.Time) bool"
	durableHelperSequence   = "AssignStmt,IfStmt,AssignStmt,AssignStmt,IfStmt,AssignStmt,IfStmt,ReturnStmt"
)

// checkDurableObservedCRL evaluates the nine durable predicates for observed-crl-keyring-v1
// over the complete call graph: the wrapper must be exactly one forwarding return with the
// production clock, and every helper statement is judged AT ITS REVIEWED POSITION. The
// closed-form digests of both declarations still decide everything a named predicate does
// not; these predicates say which decision moved when one fails.
//
// durable.invalid_key_denies here is only the LOCAL call shape — the keyring-verifying
// holder built from the same data directory and clock. The trust refusal it relies on lives
// in the Community holder/trust files, which the evaluator binds by exact bytes before it can
// report success.
func checkDurableObservedCRL(rep *report, wrapper, helper *ast.FuncDecl) {
	if wrapper == nil || wrapper.Body == nil || helper == nil || helper.Body == nil {
		failNamed(rep, durableChecks, "construction %s needs func durableLicensed and func durableLicensedAt, both with bodies",
			constructionCurrent)
		return
	}
	set(rep, "durable.present", true, "")

	stmts := helper.Body.List
	at := func(i int) ast.Stmt {
		if i < len(stmts) {
			return stmts[i]
		}
		return nil
	}
	var final *ast.ReturnStmt
	if len(stmts) > 0 {
		final, _ = stmts[len(stmts)-1].(*ast.ReturnStmt)
	}
	var selection *ast.CallExpr
	if final != nil && len(final.Results) == 1 {
		selection, _ = final.Results[0].(*ast.CallExpr)
	}

	forwardOK, forwardWhy := isForwardingWrapper(wrapper)
	extra := []string{}
	if !forwardOK {
		extra = append(extra, "durableLicensed "+forwardWhy)
	}
	for _, r := range allReturns(helper.Body) {
		if r == final && selection != nil {
			continue
		}
		if len(r.Results) == 1 && isIdent(r.Results[0], "false") {
			continue
		}
		extra = append(extra, "durableLicensedAt "+renderReturn(r))
	}
	set(rep, "durable.denies_are_false", len(extra) == 0,
		"returns that are neither `false`, the final selection nor the exact forwarding: %s", strings.Join(extra, " · "))

	pack, okSel := compositionPack(selection)
	detail := "no final selection call in durableLicensedAt"
	if selection != nil {
		detail = render(selection)
	}
	set(rep, "durable.selection_is_composition", okSel, "selection is %s", detail)
	set(rep, "durable.pack", okSel, "%s", pack)

	set(rep, "durable.term_guard_denies", isObservedTermGuard(at(4)),
		"statement 5 of durableLicensedAt is not `if !ok || c.Status(now()) == license.StatusExpired { return false }`")
	set(rep, "durable.resolve_and_blob_denies", isResolveAssign(at(0)) && isResolveDeny(at(1)),
		"statements 1-2 of durableLicensedAt are not `src, err := resolveLicense(licenseFile, dataDir, getenv)` "+
			"and `if err != nil || strings.TrimSpace(src.Blob) == \"\" { return false }`")
	set(rep, "durable.invalid_key_denies", isVerifiedHolder(at(2)),
		"statement 3 of durableLicensedAt is not `holder := newDataDirLicenseHolder(dataDir, src, now, slog.Default())`")
	set(rep, "durable.claims_are_read", isClaimsDefine(at(3)),
		"statement 4 of durableLicensedAt is not `c, ok := holder.claims()`")

	var why []string
	if sig := renderSignature(wrapper); sig != durableWrapperSignature {
		why = append(why, "durableLicensed signature is "+sig)
	}
	if !forwardOK {
		why = append(why, "durableLicensed "+forwardWhy)
	}
	if sig := renderSignature(helper); sig != durableHelperSignature {
		why = append(why, "durableLicensedAt signature is "+sig)
	}
	if kinds := strings.Join(stmtKinds(helper.Body), ","); kinds != durableHelperSequence {
		why = append(why, "durableLicensedAt top-level statements are "+kinds)
	}
	if !isRevocationGate(at(5)) {
		why = append(why, "statement 6 is not `gate := addongate.New(\"durablebus\", holder.claims)."+
			"WithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir))).WithClock(now)`")
	}
	if !isStateGuard(at(6)) {
		why = append(why, "statement 7 is not `if gate.State() == addongate.StateUnentitled { return false }`")
	}
	set(rep, "durable.statement_sequence_is_reviewed", len(why) == 0,
		"estructura no verificada: %s", strings.Join(why, " · "))
}

// isForwardingWrapper: exactly `return durableLicensedAt(licenseFile, dataDir, getenv, time.Now)`.
func isForwardingWrapper(fd *ast.FuncDecl) (bool, string) {
	r := singleReturn(fd.Body)
	if r == nil {
		return false, fmt.Sprintf("has top-level statements %s, not one forwarding return",
			strings.Join(stmtKinds(fd.Body), ","))
	}
	if len(r.Results) != 1 {
		return false, "returns " + renderReturn(r)
	}
	if _, ok := callExactly(r.Results[0], "durableLicensedAt", "licenseFile", "dataDir", "getenv", "time.Now"); !ok {
		return false, "returns " + render(r.Results[0])
	}
	return true, ""
}

// callExactly: e calls fun with exactly these rendered arguments and no variadic spread.
// Rendering distinguishes identifiers, selectors and calls, and the declaration digest
// still pins every byte of structure this comparison does not name.
func callExactly(e ast.Expr, fun string, args ...string) (*ast.CallExpr, bool) {
	c, ok := isCallOf(e, fun)
	if !ok || c.Ellipsis != token.NoPos || len(c.Args) != len(args) {
		return nil, false
	}
	for i, want := range args {
		if render(c.Args[i]) != want {
			return nil, false
		}
	}
	return c, true
}

// define: `lhs... := <one expression>`, returning that expression.
func define(s ast.Stmt, lhs ...string) (ast.Expr, bool) {
	as, ok := s.(*ast.AssignStmt)
	if !ok || as == nil || as.Tok != token.DEFINE || len(as.Lhs) != len(lhs) || len(as.Rhs) != 1 {
		return nil, false
	}
	for i, name := range lhs {
		if !isIdent(as.Lhs[i], name) {
			return nil, false
		}
	}
	return as.Rhs[0], true
}

// plainIf: an if statement with no init clause and no else branch.
func plainIf(s ast.Stmt) (*ast.IfStmt, bool) {
	ifs, ok := s.(*ast.IfStmt)
	return ifs, ok && ifs != nil && ifs.Init == nil && ifs.Else == nil
}

func isResolveAssign(s ast.Stmt) bool {
	rhs, ok := define(s, "src", "err")
	if !ok {
		return false
	}
	_, ok = callExactly(rhs, "resolveLicense", "licenseFile", "dataDir", "getenv")
	return ok
}

func isResolveDeny(s ast.Stmt) bool {
	ifs, ok := plainIf(s)
	return ok && isResolveOrEmptyBlobDeny(ifs)
}

func isVerifiedHolder(s ast.Stmt) bool {
	rhs, ok := define(s, "holder")
	if !ok {
		return false
	}
	c, ok := callExactly(rhs, "newDataDirLicenseHolder", "dataDir", "src", "now", "slog.Default()")
	if !ok {
		return false
	}
	_, ok = callExactly(c.Args[3], "slog.Default")
	return ok
}

func isClaimsDefine(s ast.Stmt) bool {
	rhs, ok := define(s, "c", "ok")
	if !ok {
		return false
	}
	_, ok = callExactly(rhs, "holder.claims")
	return ok
}

func isObservedTermGuard(s ast.Stmt) bool {
	ifs, ok := plainIf(s)
	if !ok {
		return false
	}
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.LOR {
		return false
	}
	un, ok := bin.X.(*ast.UnaryExpr)
	if !ok || un.Op != token.NOT || !isIdent(un.X, "ok") {
		return false
	}
	cmp, ok := bin.Y.(*ast.BinaryExpr)
	if !ok || cmp.Op != token.EQL || render(cmp.Y) != "license.StatusExpired" {
		return false
	}
	status, ok := callExactly(cmp.X, "c.Status", "now()")
	if !ok {
		return false
	}
	if _, ok := callExactly(status.Args[0], "now"); !ok {
		return false
	}
	return returnsLiteral(ifs.Body, "false")
}

// isRevocationGate: the local, caller-labelled gate over the SAME holder claims, the
// canonical CRL view of the SAME data directory and the SAME clock — and nothing else:
// no pack, no entitlement filter, no process-wide installation.
func isRevocationGate(s ast.Stmt) bool {
	rhs, ok := define(s, "gate")
	if !ok {
		return false
	}
	withClock, ok := rhs.(*ast.CallExpr)
	if !ok || withClock == nil || withClock.Ellipsis != token.NoPos || len(withClock.Args) != 1 ||
		!isIdent(withClock.Args[0], "now") {
		return false
	}
	clockSel, ok := withClock.Fun.(*ast.SelectorExpr)
	if !ok || clockSel.Sel == nil || clockSel.Sel.Name != "WithClock" {
		return false
	}
	withRevocation, ok := clockSel.X.(*ast.CallExpr)
	if !ok || withRevocation == nil || withRevocation.Ellipsis != token.NoPos || len(withRevocation.Args) != 1 {
		return false
	}
	revSel, ok := withRevocation.Fun.(*ast.SelectorExpr)
	if !ok || revSel.Sel == nil || revSel.Sel.Name != "WithRevocation" {
		return false
	}
	view, ok := callExactly(withRevocation.Args[0], "addongate.CRLView", "crlViewFromDataDir(dataDir)")
	if !ok {
		return false
	}
	if _, ok := callExactly(view.Args[0], "crlViewFromDataDir", "dataDir"); !ok {
		return false
	}
	gate, ok := callExactly(revSel.X, "addongate.New", `"durablebus"`, "holder.claims")
	return ok && litString(gate.Args[0]) == "durablebus"
}

func isStateGuard(s ast.Stmt) bool {
	ifs, ok := plainIf(s)
	if !ok {
		return false
	}
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.EQL || render(bin.Y) != "addongate.StateUnentitled" {
		return false
	}
	if _, ok := callExactly(bin.X, "gate.State"); !ok {
		return false
	}
	return returnsLiteral(ifs.Body, "false")
}

// renderSignature renders a declaration's receiver, type parameters, parameters and
// results, keeping the parameter grouping the source wrote.
func renderSignature(fd *ast.FuncDecl) string {
	if fd == nil || fd.Type == nil {
		return ""
	}
	sig := renderFieldList(fd.Type.Params) + renderResults(fd.Type.Results)
	if fd.Type.TypeParams != nil {
		sig = "[type parameters]" + renderFieldList(fd.Type.TypeParams) + sig
	}
	if fd.Recv != nil {
		sig = "receiver" + renderFieldList(fd.Recv) + " " + sig
	}
	return sig
}

func renderFieldList(fl *ast.FieldList) string {
	var parts []string
	if fl != nil {
		for _, f := range fl.List {
			if f == nil {
				continue
			}
			var names []string
			for _, n := range f.Names {
				if n != nil {
					names = append(names, n.Name)
				}
			}
			if len(names) == 0 {
				parts = append(parts, renderType(f.Type))
				continue
			}
			parts = append(parts, strings.Join(names, ", ")+" "+renderType(f.Type))
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func renderResults(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	if len(fl.List) == 1 && fl.List[0] != nil && len(fl.List[0].Names) == 0 {
		return " " + renderType(fl.List[0].Type)
	}
	return " " + renderFieldList(fl)
}

func renderType(e ast.Expr) string {
	if ft, ok := e.(*ast.FuncType); ok && ft != nil {
		return "func" + renderFieldList(ft.Params) + renderResults(ft.Results)
	}
	return render(e)
}

var combinedChecks = []string{
	"combined.present",
	"combined.v3_branch_returns_fromGrants",
	"combined.fallback_is_fromClaims",
}

func checkCombined(rep *report, fd *ast.FuncDecl) {
	body := bodyOfReturnedFuncLit(fd)
	if body == nil {
		failNamed(rep, combinedChecks, "func combinedPurchaseView does not return a func literal")
		return
	}
	set(rep, "combined.present", true, "")

	v3OK, v3Why := false, "no `if grants != nil { if list, ok := grants(); ok && list != nil { … } }`"
	for _, st := range body.List {
		outer, ok := st.(*ast.IfStmt)
		if !ok {
			continue
		}
		bin, ok := outer.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ || !isIdent(bin.X, "grants") || !isIdent(bin.Y, "nil") {
			continue
		}
		if outer.Body == nil || len(outer.Body.List) != 1 {
			v3Why = "the `grants != nil` branch holds more than the v3 test"
			continue
		}
		inner, ok := outer.Body.List[0].(*ast.IfStmt)
		if !ok || inner.Init == nil {
			v3Why = "the `grants != nil` branch does not test for a real v3 list"
			continue
		}
		cond, ok := inner.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.LAND || !isIdent(cond.X, "ok") {
			v3Why = "the v3 test is " + render(inner.Cond)
			continue
		}
		nilTest, ok := cond.Y.(*ast.BinaryExpr)
		if !ok || nilTest.Op != token.NEQ || !isIdent(nilTest.X, "list") || !isIdent(nilTest.Y, "nil") {
			v3Why = "the v3 test is " + render(inner.Cond)
			continue
		}
		r := singleReturn(inner.Body)
		if r == nil || len(r.Results) != 1 {
			v3Why = "the v3 branch is not a single return"
			continue
		}
		if !isCallWithIdentArg(r.Results[0], "fromGrants", "p") {
			v3Why = "with a real v3 list the branch returns " + render(r.Results[0])
			continue
		}
		v3OK, v3Why = true, ""
		break
	}
	set(rep, "combined.v3_branch_returns_fromGrants", v3OK, "%s", v3Why)

	if len(body.List) == 0 {
		set(rep, "combined.fallback_is_fromClaims", false,
			"estructura no verificada: combinedPurchaseView closure is empty")
		return
	}
	last := body.List[len(body.List)-1]
	fallOK := false
	fallWhy := "the composition does not end in a return"
	if r, ok := last.(*ast.ReturnStmt); ok {
		if len(r.Results) == 0 {
			fallWhy = "estructura no verificada: the fallback is a bare return"
		} else if len(r.Results) == 1 && isCallWithIdentArg(r.Results[0], "fromClaims", "p") {
			fallOK = true
			fallWhy = ""
		} else {
			fallWhy = "the fallback returns " + renderReturn(r)
		}
	}
	set(rep, "combined.fallback_is_fromClaims", fallOK, "%s", fallWhy)
}

var grantsChecks = []string{
	"grants.present",
	"grants.nil_provider_denies",
	"grants.unreadable_denies",
	"grants.empty_list_denies",
	"grants.unknown_pack_denies",
	"grants.product_match_grants",
}

func checkFromGrants(rep *report, fd *ast.FuncDecl) {
	body := bodyOfReturnedFuncLit(fd)
	if body == nil {
		failNamed(rep, grantsChecks, "func purchaseViewFromGrants does not return a func literal")
		return
	}
	set(rep, "grants.present", true, "")

	type guard struct {
		name string
		want string
	}
	guards := map[string]bool{}
	why := map[string]string{}
	for _, st := range body.List {
		ifs, ok := st.(*ast.IfStmt)
		if !ok {
			continue
		}
		cond := render(ifs.Cond)
		denies := returnsLiteral(ifs.Body, "false")
		switch {
		case cond == "grants == nil":
			guards["grants.nil_provider_denies"] = denies
		case cond == "!ok || list == nil":
			guards["grants.unreadable_denies"] = denies
		case cond == "len(list) == 0":
			guards["grants.empty_list_denies"] = denies
			if !denies {
				why["grants.empty_list_denies"] = "an empty v3 list is answered " +
					renderBlock(ifs.Body)
			}
		case cond == `want == ""`:
			guards["grants.unknown_pack_denies"] = denies
		}
	}
	for _, g := range []guard{
		{"grants.nil_provider_denies", "grants == nil"},
		{"grants.unreadable_denies", "!ok || list == nil"},
		{"grants.empty_list_denies", "len(list) == 0"},
		{"grants.unknown_pack_denies", `want == ""`},
	} {
		d := why[g.name]
		if d == "" && !guards[g.name] {
			d = "no guard `if " + g.want + " { return false }`"
		}
		set(rep, g.name, guards[g.name], "%s", d)
	}

	matchOK, matchWhy := false, "no `for … range list` that grants on a ProductID match"
	ast.Inspect(body, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok || !isIdent(rng.X, "list") {
			return true
		}
		if rng.Body == nil {
			return false
		}
		for _, st := range rng.Body.List {
			ifs, ok := st.(*ast.IfStmt)
			if !ok {
				continue
			}
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok || bin.Op != token.EQL {
				continue
			}
			sel, ok := bin.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ProductID" {
				continue
			}
			if returnsLiteral(ifs.Body, "true") {
				matchOK, matchWhy = true, ""
				return false
			}
			matchWhy = "the ProductID match answers " + renderBlock(ifs.Body)
		}
		return false
	})
	set(rep, "grants.product_match_grants", matchOK, "%s", matchWhy)
}

func renderBlock(b *ast.BlockStmt) string {
	if b == nil {
		return "{}"
	}
	var parts []string
	for _, s := range b.List {
		if r, ok := s.(*ast.ReturnStmt); ok {
			if len(r.Results) == 0 {
				parts = append(parts, "bare return")
				continue
			}
			var rs []string
			for _, e := range r.Results {
				rs = append(rs, render(e))
			}
			parts = append(parts, "return "+strings.Join(rs, ", "))
			continue
		}
		parts = append(parts, strings.TrimPrefix(reflect.TypeOf(s).String(), "*ast."))
	}
	return "{ " + strings.Join(parts, "; ") + " }"
}

var claimsChecks = []string{
	"claims.present",
	"claims.vocabulary_answers_attestation",
	"claims.legacy_switch_present",
	"claims.legacy_never_grants_by_default",
	"claims.legacy_grants_exactly_self_serve",
}

func checkFromClaims(rep *report, fd *ast.FuncDecl) {
	body := bodyOfReturnedFuncLit(fd)
	if body == nil {
		failNamed(rep, claimsChecks, "func purchaseViewFromClaims does not return a func literal")
		return
	}
	set(rep, "claims.present", true, "")

	vocabOK := false
	ast.Inspect(body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || !isIdent(ifs.Cond, "vocabulary") {
			return true
		}
		r := singleReturn(ifs.Body)
		if r != nil && len(r.Results) == 1 && render(r.Results[0]) == "packs[p]" {
			vocabOK = true
		}
		return false
	})
	set(rep, "claims.vocabulary_answers_attestation", vocabOK,
		"the `if vocabulary` branch must answer packs[p] alone")

	var granted []string
	sawSwitch := false
	defaultGrants := false
	ast.Inspect(body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || !isIdent(sw.Tag, "p") {
			return true
		}
		sawSwitch = true
		if sw.Body == nil {
			return false
		}
		for _, cl := range sw.Body.List {
			cc, ok := cl.(*ast.CaseClause)
			if !ok {
				continue
			}
			grants := false
			for _, st := range cc.Body {
				if r, ok := st.(*ast.ReturnStmt); ok && len(r.Results) == 1 && isIdent(r.Results[0], "true") {
					grants = true
				}
			}
			if !grants {
				continue
			}
			if cc.List == nil {
				defaultGrants = true
				continue
			}
			for _, e := range cc.List {
				granted = append(granted, render(e))
			}
		}
		return false
	})
	sort.Strings(granted)
	want := []string{
		"activation.PackAIRuntimeSecurity", "activation.PackBusiness",
		"activation.PackCompliancePacks", "activation.PackIdentityScale",
		"activation.PackRegulated",
	}
	set(rep, "claims.legacy_switch_present", sawSwitch,
		"a credential predating pack attestation must still be read")
	set(rep, "claims.legacy_never_grants_by_default", !defaultGrants,
		"the legacy read grants by DEFAULT: every pack, bought or not")
	set(rep, "claims.legacy_grants_exactly_self_serve",
		sawSwitch && !defaultGrants && strings.Join(granted, ",") == strings.Join(want, ","),
		"the legacy read grants %s", strings.Join(granted, " · "))
}

var legalholdChecks = []string{
	"legalhold.present",
	"legalhold.statement_sequence_is_reviewed",
	"legalhold.hold_id_required",
	"legalhold.disabled_refuses",
	"legalhold.justification_refuses",
	"legalhold.approver_floor_refuses",
	"legalhold.success_allows",
	"legalhold.calls_are_reviewed",
}

func checkLegalHold(rep *report, fd *ast.FuncDecl) {
	if fd == nil || fd.Body == nil {
		failNamed(rep, legalholdChecks, "LegalHoldOverride.EvaluateOverride is absent")
		return
	}
	set(rep, "legalhold.present", true, "")

	kinds := strings.Join(stmtKinds(fd.Body), ",")
	const reviewed = "IfStmt,AssignStmt,IfStmt,IfStmt,IfStmt,AssignStmt,AssignStmt,ReturnStmt"
	set(rep, "legalhold.statement_sequence_is_reviewed", kinds == reviewed,
		"estructura no verificada: EvaluateOverride top-level statements are %s", kinds)

	holdOK := false
	if len(fd.Body.List) > 0 {
		if ifs, ok := fd.Body.List[0].(*ast.IfStmt); ok {
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if ok && bin.Op == token.EQL && isIdent(bin.X, "holdID") {
				if lit, ok := bin.Y.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err == nil && s == "" {
						r := singleReturn(ifs.Body)
						holdOK = r != nil && len(r.Results) == 2 && isIdent(r.Results[0], "nil")
					}
				}
			}
		}
	}
	set(rep, "legalhold.hold_id_required", holdOK,
		"the first statement is not `if holdID == \"\" { return nil, … }`")

	successOK := false
	n := len(fd.Body.List)
	if n >= 3 {
		a1, ok1 := fd.Body.List[n-3].(*ast.AssignStmt)
		a2, ok2 := fd.Body.List[n-2].(*ast.AssignStmt)
		ret, ok3 := fd.Body.List[n-1].(*ast.ReturnStmt)
		if ok1 && ok2 && ok3 &&
			len(a1.Lhs) == 1 && render(a1.Lhs[0]) == "decision.Allowed" &&
			len(a1.Rhs) == 1 && isIdent(a1.Rhs[0], "true") &&
			len(a2.Lhs) == 1 && render(a2.Lhs[0]) == "decision.Reason" &&
			ret != nil && len(ret.Results) == 2 && isIdent(ret.Results[0], "decision") && isIdent(ret.Results[1], "nil") {
			successOK = true
		}
	}
	set(rep, "legalhold.success_allows", successOK,
		"the reviewed success path (Allowed=true, Reason, return decision) is absent")

	conds := map[string]*ast.IfStmt{}
	for _, st := range fd.Body.List {
		if ifs, ok := st.(*ast.IfStmt); ok {
			conds[render(ifs.Cond)] = ifs
		}
	}
	for name, cond := range map[string]string{
		"legalhold.disabled_refuses":       "!h.Enabled",
		"legalhold.justification_refuses":  `h.RequireJustification && reason == ""`,
		"legalhold.approver_floor_refuses": "approvers < h.MinApprovers",
	} {
		ifs, ok := conds[cond]
		if !ok {
			set(rep, name, false, "no refusal guarded by exactly `%s`", cond)
			continue
		}
		set(rep, name, refusesWithReason(ifs.Body), "the `%s` branch does not refuse", cond)
	}

	var calls []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			calls = append(calls, render(c.Fun))
		}
		return true
	})
	allowed := map[string]bool{"fmt.Errorf": true, "fmt.Sprintf": true}
	var foreign []string
	for _, c := range calls {
		if !allowed[c] {
			foreign = append(foreign, c)
		}
	}
	sort.Strings(foreign)
	set(rep, "legalhold.calls_are_reviewed", len(foreign) == 0,
		"the evaluation calls %s: the protection's evaluation consults nothing", strings.Join(foreign, " · "))
}

func refusesWithReason(b *ast.BlockStmt) bool {
	if b == nil {
		return false
	}
	allowedFalse, reason, ret := false, false, false
	for _, st := range b.List {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) == 1 {
				switch render(s.Lhs[0]) {
				case "decision.Allowed":
					allowedFalse = len(s.Rhs) == 1 && isIdent(s.Rhs[0], "false")
				case "decision.Reason":
					reason = true
				}
			}
		case *ast.ReturnStmt:
			ret = len(s.Results) == 2 && isIdent(s.Results[0], "decision") && isIdent(s.Results[1], "nil")
		}
	}
	return allowedFalse && reason && ret
}

func checkAIMS(rep *report, cut, stub *ast.FuncDecl) {
	ok, why := false, "func newAIMSPackager is absent from the addon_cp build"
	if cut != nil && cut.Body != nil {
		if len(cut.Body.List) != 1 {
			why = fmt.Sprintf("the composition has %d top-level statements, not one return",
				len(cut.Body.List))
		} else if r := singleReturn(cut.Body); r == nil || len(r.Results) != 1 {
			why = "the composition is not a single return"
		} else if c, isCall := isCallOf(r.Results[0], "iso42001.NewPackager"); !isCall {
			why = "the addon_cp build returns " + render(r.Results[0])
		} else if len(c.Args) != 1 {
			why = "iso42001.NewPackager takes " + render(r.Results[0])
		} else if g, isGate := isCallOf(c.Args[0], "addonGate"); !isGate ||
			len(g.Args) != 1 || litString(g.Args[0]) != "iso42001" {
			why = "the packager is not built behind addonGate(\"iso42001\"): " + render(r.Results[0])
		} else {
			ok, why = true, ""
		}
	}
	set(rep, "aims.composition_is_the_only_return", ok, "%s", why)

	sok, swhy := false, "func newAIMSPackager is absent from the !addon_cp build"
	if stub != nil && stub.Body != nil {
		if len(stub.Body.List) == 1 && returnsLiteral(stub.Body, "nil") {
			sok, swhy = true, ""
		} else {
			swhy = "the no-addon_cp build is " + renderBlock(stub.Body) +
				": the cut would ship the module to a customer who did not buy the pack"
		}
	}
	set(rep, "aims.stub_returns_nil", sok, "%s", swhy)
}

func litString(e ast.Expr) string {
	l, ok := e.(*ast.BasicLit)
	if !ok || l == nil || l.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(l.Value)
	if err != nil {
		return ""
	}
	return s
}

var setpacksChecks = []string{
	"setpacks.present",
	"setpacks.ids_is_identity_scale",
	"setpacks.mapping_is_reviewed",
}

func checkSetCodePacks(rep *report, f *ast.File, vs *ast.ValueSpec) {
	if vs == nil {
		vs = findValueSpec(f, "setCodePacks")
	}
	if vs == nil || len(vs.Values) != 1 {
		failNamed(rep, setpacksChecks, "var setCodePacks is absent")
		return
	}
	cl, ok := vs.Values[0].(*ast.CompositeLit)
	if !ok || cl == nil {
		failNamed(rep, setpacksChecks, "setCodePacks is not a composite literal")
		return
	}
	mt, ok := cl.Type.(*ast.MapType)
	if !ok || !isIdent(mt.Key, "string") || render(mt.Value) != "activation.Pack" {
		failNamed(rep, setpacksChecks, "setCodePacks is not map[string]activation.Pack")
		return
	}
	set(rep, "setpacks.present", true, "")
	out := map[string]string{}
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok || kv == nil {
			continue
		}
		k := litString(kv.Key)
		if k == "" {
			continue
		}
		out[k] = render(kv.Value)
	}
	rep.SetCodePacks = out
	set(rep, "setpacks.ids_is_identity_scale", out["ids"] == "activation.PackIdentityScale",
		"`set:ids` maps to %s, not activation.PackIdentityScale", out["ids"])
	set(rep, "setpacks.mapping_is_reviewed", maps.Equal(out, reviewedSetCodePacks),
		"setCodePacks maps %v", out)
}

var productidChecks = []string{
	"productid.present",
	"productid.identity_scale_is_reviewed",
	"productid.mapping_is_reviewed",
}

func checkPackProductID(rep *report, fd *ast.FuncDecl) {
	if fd == nil || fd.Body == nil {
		failNamed(rep, productidChecks, "func packProductID is absent")
		return
	}
	set(rep, "productid.present", true, "")
	out := map[string]string{}
	trailingEmpty := false
	for _, st := range fd.Body.List {
		switch s := st.(type) {
		case *ast.SwitchStmt:
			if !isIdent(s.Tag, "p") || s.Body == nil {
				continue
			}
			for _, cl := range s.Body.List {
				cc, ok := cl.(*ast.CaseClause)
				if !ok {
					continue
				}
				r := singleReturn(&ast.BlockStmt{List: cc.Body})
				if r == nil || len(r.Results) != 1 {
					continue
				}
				val := litString(r.Results[0])
				if cc.List == nil {
					continue
				}
				for _, e := range cc.List {
					out[render(e)] = val
				}
			}
		case *ast.ReturnStmt:
			if len(s.Results) == 1 && litString(s.Results[0]) == "" {
				trailingEmpty = true
			}
		}
	}
	rep.PackProductIDs = out
	ids := out["activation.PackIdentityScale"]
	set(rep, "productid.identity_scale_is_reviewed",
		ids == "self_hosted.business.addons.identity-scale",
		"Identity & Scale product id is %q", ids)
	set(rep, "productid.mapping_is_reviewed",
		trailingEmpty && maps.Equal(out, reviewedPackProductIDs),
		"packProductID maps %v (trailing empty return: %v)", out, trailingEmpty)
}

func readCatalog(rep *report, f *ast.File) {
	if f == nil {
		set(rep, "catalog.present", false, "the activation catalog could not be read")
		return
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gd.Tok == token.CONST {
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok || exprTypeName(vs.Type) != "Pack" {
					continue
				}
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if v := litString(vs.Values[i]); v != "" {
							rep.PackConsts[n.Name] = v
						}
					}
				}
			}
			continue
		}
		if gd.Tok != token.VAR {
			continue
		}
		for _, sp := range gd.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "catalog" || len(vs.Values) != 1 {
				continue
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			at, ok := cl.Type.(*ast.ArrayType)
			if !ok || exprTypeName(at.Elt) != "AddonSpec" {
				continue
			}
			set(rep, "catalog.present", true, "")
			for _, el := range cl.Elts {
				row, ok := el.(*ast.CompositeLit)
				if !ok {
					continue
				}
				var r catalogRow
				for _, fe := range row.Elts {
					kv, ok := fe.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					switch render(kv.Key) {
					case "Key":
						r.Key = litString(kv.Value)
					case "Pack":
						r.Pack = render(kv.Value)
					case "Kind":
						r.Kind = render(kv.Value)
					case "Disp":
						r.Disp = render(kv.Value)
					}
				}
				rep.Catalog = append(rep.Catalog, r)
			}
			return
		}
	}
	set(rep, "catalog.present", false, "no `var catalog = []AddonSpec{…}` declaration")
}
