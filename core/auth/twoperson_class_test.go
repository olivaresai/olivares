// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The two-person class gate.
//
// Three external contrasts, on three different PRs, measured the SAME defect in three
// places that had never heard of each other: a control that promises two humans decided
// it by comparing CREDENTIAL identity. Fixing the three sites leaves the fourth one
// paste away, which is how the third arrived. This file is the rule instead of the
// three fixes: anywhere in the estate, a function that SAYS it is a two-party control
// and DECIDES party identity must route that decision through core/auth's primitive.
//
// WHAT IT CAN SEE, exactly — this is a syntactic scanner, and the claim is no wider
// than its shape:
//
//   - IN SCOPE: a function whose own comments or messages use the two-party vocabulary
//     (dual-control, two distinct humans, a second/different approver…) AND that compares
//     or de-duplicates a PARTY EXPRESSION — a Principal.Actor() call, an identifier or
//     field named for a party role (initiator, proposer, decider, approver, activated_by…),
//     or a stored column read of one.
//
//   - COMPLIANT: the enclosing function references the primitive (TwoDistinctPeople,
//     DistinctPeople, PersonRef…), or every party expression it COMPARES is already a
//     STABLE-PERSON read (a *_user column, a UserID). The second form is not an escape
//     hatch: a comparison of two empty person reads is EQUAL, so a "must differ" check
//     refuses — deny-closed. It does NOT extend to counting: see the next bullet.
//     modules/governance/approvals.go passes by referencing the primitive.
//
//   - NOT COMPLIANT, since: a QUORUM keyed on a stable-person read. The exemption
//     above is about comparison, and counting is the opposite case — the empty string is
//     a valid map key, so a decision no person stands behind becomes an approver and the
//     total inflates. That is M10, and this gate used to certify that exact shape clean.
//     A count goes through auth.DistinctPeople, which refuses to total an unknown.
//
//   - NOT COMPLIANT, since: COLLAPSING THE VERDICT. PersonMatch has four values, and
//     a control that weighs it against PersonSame alone has decided authorization on an
//     incomplete case analysis whichever way its branch falls — `!= PersonSame` authorizes
//     two cases it never looked at, `== PersonSame` denies one and lets the other three
//     out through the fall-through. Caught in five shapes: `!auth.SamePerson(a, b)`; the
//     same through a boolean variable; `a.Compare(b)` weighed against PersonSame in either
//     direction; that comparison through a verdict variable; and `switch a.Compare(b)`.
//     Checked even when the function DOES reference the primitive, because mentioning
//     PersonRef cannot license undoing the verdict — and the hand-rolled form mentions it
//     by construction, so inside the exemption this check would be dead on arrival.
//
//     The exemption that IS honored is TwoDistinctPeople: it returns the decision AND the
//     verdict, so a caller that has one reads the other only to phrase its refusal. That
//     is what dr_handler.go and posture.go do. It is also the one thing a caller must not
//     re-narrow: modules/governance/approvals.go used to refuse on
//     `!ok && verdict == PersonSame`, and that conjunction let the fourth value walk
//     straight past when it was added. Ask `ok`; read the verdict for the message.
//
//   - OUT OF SCOPE, by construction and not by allowlist: identities this engine never
//     minted. core/audit's RecoveryEvidence.Approvers is a []string an operator types on
//     the CLI for a paper process; no Principal and no stored row stand behind those
//     names, so the primitive could only relabel them, never attribute them. That gap is
//     REAL and is declared in sessions-*.md — it is a copy problem, not one this
//     gate can decide.
//
//   - CANNOT SEE, measured and not guessed (from the external contrast on #615):
//     a two-party control that never uses the vocabulary; identity that reaches the
//     decision through a variable named for nothing; a decorative reference to the
//     primitive, which still exempts everything except the collapse; function literals;
//     indirect comparisons; and subtrees the walker could not read (it returns nil on a
//     walk error, so an unreadable directory is an omission, not a failure).
//
//     Two live consequences, named because they are present in this tree TODAY:
//     modules/governance/killswitch.go counts approvers correctly — it keys on
//     decider_user and floors the empty string — but it passes this gate because its loop
//     variable is called `u`, not because of the rule. And three connector PEPs
//     (connectors/claude-api/adminactions.go, connectors/claude-compliance/content.go,
//     connectors/agentcore/exporter.go) de-duplicate `Approvers []string` through a
//     variable called `a` and authorize on `>= 2`; they are two-party controls that this
//     scanner does not see, and they could not adopt the primitive anyway —
//     scripts/check-boundary.sh forbids connectors/ from importing core/auth, so the
//     remedy this gate's failure message prescribes is architecturally unavailable there.
//     The contract that feeds them is what has to change (PR #589 carries person and
//     credential separately); this gate cannot decide it.
//
//     This gate raises the cost of the fourth defect; it does not make it impossible.

// twoPartyVocabulary is what a two-party control says about itself. Kept deliberately
// close to the words the estate already uses, so a new control written in the house
// style is picked up without anyone remembering this file exists.
var twoPartyVocabulary = regexp.MustCompile(`(?i)dual.?control|two.person|two distinct (humans|people|persons)|four.?eyes|separation of dut|second,? (distinct )?(approver|human|person)|different (administrator|admin|person|human)|distinct (human|person|approver)`)

// partyRole names the ROLE a party plays in such a control. A comparison or a
// de-duplication keyed on one of these is an identity decision.
var partyRole = regexp.MustCompile(`(?i)^(actor|initiator|proposer|decider|approver|approvers|reviewer|activatedby|activated_by|requestedby|requested_by|reviewedby|reviewed_by|usedby|used_by|engagedby|engaged_by)$`)

// stablePerson names the reads that already ARE the rule: the person behind the
// credential rather than the credential.
var stablePerson = regexp.MustCompile(`(?i)(user|userid|_user)$`)

// primitiveIdents are the primitive's exported names. Referencing any of them is the
// declaration that this control's identity decision goes through core/auth.
//
// SamePerson and SamePersonAs are NOT here, and their absence is the fix for the P1 the
// external contrast on PR #615 called "the front door". Both return
// `Compare(o) == PersonSame`, so both render PersonDistinct and PersonUndetermined as the
// same `false`. Accepting them as proof of compliance meant a control could authorize on
// `!auth.SamePerson(a, b)` — treating "I cannot tell" as "two different people", the
// precise defect this file exists to make impossible — and the scanner would report clean
// BECAUSE it had seen the identifier. Referencing a wrapper that discards the third value
// is not a declaration that the decision goes through the primitive.
var primitiveIdents = map[string]bool{
	"TwoDistinctPeople": true, "DistinctPeople": true, "PersonRef": true,
	"PersonRefOf": true, "PersonRefOfUser": true, "PersonMatch": true,
}

// personSameness names the boolean forms that answer "are these the same person" with a
// single bit, discarding the difference between "different people" and "I cannot tell".
// Asking is fine; AUTHORIZING on the negation is the collapse.
var personSameness = map[string]bool{"SamePerson": true, "SamePersonAs": true}

// violation is one identity decision that a two-party control makes outside the rule.
type violation struct {
	file string // repo-relative
	line int
	fn   string
	expr string
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d  %s decides party identity on %q without core/auth's two-person primitive", v.file, v.line, v.fn, v.expr)
}

// TestClass_TwoPersonControlsUseThePrimitive is the gate. It must stay green on main;
// a new control that compares credentials fails it BY NAME.
func TestClass_TwoPersonControlsUseThePrimitive(t *testing.T) {
	root := repoRoot(t)
	trees := []string{"core", "modules", "cmd", "connectors"}

	var found []violation
	scanned, candidates := 0, 0
	for _, tree := range trees {
		base := filepath.Join(root, tree)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("tree %s is missing — a gate that scans nothing must not report clean: %v", tree, err)
		}
		walkGoFiles(t, base, func(path string, fset *token.FileSet, file *ast.File) {
			scanned++
			for _, fn := range twoPartyFuncs(fset, file) {
				candidates++
				rel, _ := filepath.Rel(root, path)
				found = append(found, scanFunc(fset, filepath.ToSlash(rel), fn)...)
			}
		})
	}

	if scanned == 0 || candidates == 0 {
		t.Fatalf("the scanner examined %d files and found %d two-party functions — a gate that finds no candidates is broken, not clean", scanned, candidates)
	}
	t.Logf("scanned %d non-test Go files, %d of their functions declare a two-party control", scanned, candidates)

	if len(found) > 0 {
		sort.Slice(found, func(i, j int) bool {
			if found[i].file != found[j].file {
				return found[i].file < found[j].file
			}
			return found[i].line < found[j].line
		})
		var b strings.Builder
		b.WriteString("two-party controls deciding identity outside core/auth's primitive:\n")
		for _, v := range found {
			b.WriteString("  " + v.String() + "\n")
		}
		b.WriteString("\nCompare PEOPLE, not credential strings: one human holds a session AND a token\n")
		b.WriteString("they minted, which render two different actor strings and satisfy a two-person\n")
		b.WriteString("gate. Use auth.TwoDistinctPeople / auth.DistinctPeople, and decide the\n")
		b.WriteString("PersonUndetermined case explicitly (auth.RefuseWhenUndetermined for any gate\n")
		b.WriteString("whose promise to the operator is two humans).")
		t.Fatal(b.String())
	}
}

// TestClass_ScannerCatchesAFakeControl is the gate ON the gate, in both directions. A
// class test nobody has seen fail is a claim, not a measurement — and this one runs
// against fixtures instead of the live tree so it proves the same thing without a
// commit that breaks main.
func TestClass_ScannerCatchesAFakeControl(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src      string
		wantExpr string // "" = must be clean
	}{
		{
			name: "the fourth control, written the way the first three were",
			src: `package fake
func decide(rec Record, p Principal) error {
	// DUAL-CONTROL: the approver must differ from the proposer.
	if rec.String(colProposer) == p.Actor() {
		return errSelf
	}
	return nil
}`,
			wantExpr: "p.Actor()",
		},
		{
			name: "the same control routed through the primitive is clean",
			src: `package fake
func decide(rec Record, p Principal) error {
	// DUAL-CONTROL: the approver must differ from the proposer.
	ok, verdict := auth.TwoDistinctPeople(auth.PersonRef{User: rec.String(colProposerUser)}, auth.PersonRefOf(p), auth.RefuseWhenUndetermined)
	if !ok {
		return fmt.Errorf("refused: %v", verdict)
	}
	return nil
}`,
		},
		{
			name: "a quorum counted on credential strings",
			src: `package fake
func quorum(decs []Record) int {
	// dual-control: at least 2 distinct approvers.
	seen := map[string]struct{}{}
	for _, d := range decs {
		seen[d.String(colDecider)] = struct{}{}
	}
	return len(seen)
}`,
			wantExpr: `d.String(colDecider)`,
		},
		{
			//. This fixture used to assert CLEAN, and the external contrast on PR
			// #615 named it as a deliberate false negative: a quorum keyed on the stable
			// person is still wrong when it counts, because the empty string is a valid
			// map key. decider_user is plain text with no non-empty requirement and the
			// unique index admits ONE empty row, so "one person + one row nobody stands
			// behind" counts as two approvers — M10, exactly, blessed by the gate that
			// exists to catch it. Reading the person is the rule for a COMPARISON; for a
			// COUNT the rule is auth.DistinctPeople, which refuses to total an
			// unattributable party at all.
			name: "a quorum counted on the stable person STILL inflates on the empty string",
			src: `package fake
func quorum(decs []Record) int {
	// dual-control: at least 2 distinct approvers.
	seen := map[string]struct{}{}
	for _, d := range decs {
		seen[d.String(colDeciderUser)] = struct{}{}
	}
	return len(seen)
}`,
			wantExpr: `d.String(colDeciderUser)`,
		},
		{
			name: "the same quorum through the primitive is clean — it cannot total an unknown",
			src: `package fake
func quorum(decs []Record) int {
	// dual-control: at least 2 distinct approvers.
	refs := make([]auth.PersonRef, 0, len(decs))
	for _, d := range decs {
		refs = append(refs, auth.PersonRef{User: d.String(colDeciderUser), Actor: d.String(colDecider)})
	}
	people, _ := auth.DistinctPeople(refs)
	return people
}`,
		},
		{
			// the P1 the contrast called "the front door". SamePerson collapses BOTH
			// PersonDistinct and PersonUndetermined into false, so authorizing on its
			// negation treats "I cannot tell" as "two different people" — the precise
			// defect this whole file was built to make impossible. The gate used to accept
			// the mere presence of the identifier as proof of compliance.
			name: "the collapse: authorizing on the negation of a person-sameness boolean",
			src: `package fake
func consume(initiator, approver Principal) error {
	// DUAL-CONTROL: a restore needs two distinct humans.
	if !auth.SamePerson(initiator, approver) {
		return nil
	}
	return errSelf
}`,
			wantExpr: "auth.SamePerson(initiator, approver)",
		},
		{
			// The same collapse written by hand, which is why deleting the wrapper would
			// not have closed this: any caller can rebuild it out of Compare.
			name: "the collapse, hand-rolled out of Compare, and decorated with a primitive reference",
			src: `package fake
func consume(rec Record, p Principal) error {
	// DUAL-CONTROL: the approver must be a second, distinct human.
	initiator := auth.PersonRef{User: rec.String(colInitiatorUser)}
	if initiator.Compare(auth.PersonRefOf(p)) != auth.PersonSame {
		return nil
	}
	return errSelf
}`,
			wantExpr: "initiator.Compare(auth.PersonRefOf(p))",
		},
		{
			// The contrast listed the evasions the first detector missed. These four
			// are the ones a scanner can honestly close: the collapse is still written
			// down, just bound to a name first. (4) a helper with no vocabulary, (5) a
			// function literal and (7) a decorative reference plus a credential compare
			// are NOT closed and are declared in the header — they need flow analysis or
			// a different unit of scanning, not a bigger regex.
			name: "the collapse via a boolean variable, which is the same thing with a name",
			src: `package fake
func consume(initiator, approver Principal) error {
	// DUAL-CONTROL: a restore needs two distinct humans.
	same := auth.SamePerson(initiator, approver)
	if !same {
		return nil
	}
	return errSelf
}`,
			wantExpr: "same",
		},
		{
			name: "denying only PersonSame and authorizing on the fall-through",
			src: `package fake
func consume(a, b PersonRefLike) error {
	// DUAL-CONTROL: the approver must be a second, distinct human.
	if a.Compare(b) == auth.PersonSame {
		return errSelf
	}
	return nil
}`,
			wantExpr: "a.Compare(b)",
		},
		{
			name: "the same deny/default written as a switch on the verdict",
			src: `package fake
func consume(a, b PersonRefLike) error {
	// DUAL-CONTROL: the approver must be a second, distinct human.
	switch a.Compare(b) {
	case auth.PersonSame:
		return errSelf
	default:
		return nil
	}
}`,
			wantExpr: "a.Compare(b)",
		},
		{
			name: "the verdict bound to a variable and tested against one of its four values",
			src: `package fake
func consume(a, b PersonRefLike) error {
	// DUAL-CONTROL: the approver must be a second, distinct human.
	m := a.Compare(b)
	if m != auth.PersonSame {
		return nil
	}
	return errSelf
}`,
			wantExpr: "m",
		},
		{
			name: "the same decision through TwoDistinctPeople keeps the third value and is clean",
			src: `package fake
func consume(rec Record, p Principal) error {
	// DUAL-CONTROL: the approver must be a second, distinct human.
	initiator := auth.PersonRef{User: rec.String(colInitiatorUser)}
	ok, verdict := auth.TwoDistinctPeople(initiator, auth.PersonRefOf(p), auth.RefuseWhenUndetermined)
	if ok {
		return nil
	}
	if verdict == auth.PersonSame {
		return errSelf
	}
	return errUnknownIdentity
}`,
		},
		{
			// VERBATIM from core/api/dr_handler.go as it stood before this change, minus
			// one session reference in its doc comment: the fixture is a Go STRING, and
			// lint:export scrubs internal refs out of comments but not out of strings, so
			// a truly verbatim copy is unpublishable. Nothing the scanner reads changes.
			// This is the site the #583 contrast reproduced end-to-end: a
			// superadmin session requested the restore and the SAME human approved with
			// a token they had minted for themselves. A class gate nobody has seen fail
			// on the real defect is a claim; this is the measurement.
			name: "the DR restore gate as it actually stood — the reproduced bypass",
			src: `package fake
// approvePending consumes a pending restore under dual-control: it must exist, match
// the upload, and be approved by a DIFFERENT admin than the initiator (the structural
// two-humans check). On success the pending entry is removed and returned; a
// self-approval is refused and the entry is LEFT in place (another admin can approve).
func (ds *drService) approvePending(requestID, uploadID, approver string) (*pendingRestore, error) {
	ds.pmu.Lock()
	defer ds.pmu.Unlock()
	pr, ok := ds.pending[requestID]
	if !ok || pr.UploadID != uploadID {
		return nil, errNoPendingRestore
	}
	if pr.Initiator == approver {
		return nil, errSelfApprove
	}
	delete(ds.pending, requestID)
	return pr, nil
}`,
			wantExpr: "pr.Initiator",
		},
		{
			// VERBATIM from modules/sourcescope/posture.go as it stood before. The
			// #580 contrast reproduced the self-signature here independently, and no
			// open PR touched this file — three lanes fixed DR and none of them fixed
			// this one, which is the class defect stated as a fact about the estate.
			name: "the sourcescope posture gate as it actually stood",
			src: `package fake
func (m *Module) decidePostureRequest(rec Record, mc ModuleContext) error {
	// DUAL-CONTROL: the decider must differ from the proposer (two-person integrity).
	if rec.String(colPRProposer) == mc.Principal.Actor() {
		return validationError("dual-control: the approver must differ from the proposer")
	}
	return nil
}`,
			wantExpr: "mc.Principal.Actor()",
		},
		{
			name: "an identity comparison with NO two-party vocabulary is out of scope, honestly",
			src: `package fake
func sameSession(a, b Principal) bool {
	return a.Actor() == b.Actor()
}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fake.go", tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("fixture does not parse: %v", err)
			}
			var got []violation
			for _, fn := range twoPartyFuncs(fset, file) {
				got = append(got, scanFunc(fset, "fake.go", fn)...)
			}
			if tc.wantExpr == "" {
				if len(got) != 0 {
					t.Fatalf("clean fixture flagged: %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("the scanner did NOT catch the fake control — the gate cannot fail, so it proves nothing")
			}
			var exprs []string
			for _, v := range got {
				exprs = append(exprs, v.expr)
			}
			if !contains(exprs, tc.wantExpr) {
				t.Fatalf("caught %v, want it to name %q", exprs, tc.wantExpr)
			}
		})
	}
}

// twoPartyFuncs returns the functions in file whose OWN text declares a two-party
// control: the doc comment, any comment inside the body, and any string literal it
// builds (the client-facing refusal is usually where the promise is written down).
func twoPartyFuncs(fset *token.FileSet, file *ast.File) []*ast.FuncDecl {
	// Comments are attached to the file, not to statements, so index them by line and
	// claim the ones that fall inside each function's span.
	type span struct{ lo, hi int }
	comments := map[int]string{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			comments[fset.Position(c.Pos()).Line] = c.Text
		}
	}
	var out []*ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var text strings.Builder
		if fn.Doc != nil {
			text.WriteString(fn.Doc.Text())
		}
		s := span{fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line}
		for line, c := range comments {
			if line >= s.lo && line <= s.hi {
				text.WriteString("\n" + c)
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				text.WriteString("\n" + lit.Value)
			}
			return true
		})
		if twoPartyVocabulary.MatchString(text.String()) {
			out = append(out, fn)
		}
	}
	return out
}

// scanFunc reports the identity decisions fn makes outside the rule. A function that
// references the primitive anywhere in its body has declared its mechanism and is not
// second-guessed — the gate's job is to force the decision through core/auth, not to
// re-audit code that already routes there.
func scanFunc(fset *token.FileSet, rel string, fn *ast.FuncDecl) []violation {
	name := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		name = recvName(fn.Recv.List[0].Type) + "." + name
	}
	// The COLLAPSE is checked unconditionally, before the exemption below.
	// Referencing the primitive is a declaration about MECHANISM, and no declaration
	// licenses turning the three-valued verdict back into a boolean that authorizes on
	// "not the same person". Left inside the exemption this check would be dead on
	// arrival: a function that mentions PersonRef anywhere is exempt, and the hand-rolled
	// collapse `initiator.Compare(x) != PersonSame` mentions it by construction.
	out := scanCollapse(fset, rel, name, fn)
	if referencesPrimitive(fn) {
		return dedupe(out)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op != token.EQL && node.Op != token.NEQ {
				return true
			}
			// Both sides matter: `stored == p.Actor()` is caught by either.
			for _, side := range []ast.Expr{node.X, node.Y} {
				if isPartyExpr(side) && !isStablePersonExpr(side) {
					out = append(out, violation{rel, fset.Position(side.Pos()).Line, name, render(side)})
				}
			}
		case *ast.IndexExpr:
			// The de-duplication form: seen[<party>] = struct{}{} — a quorum is a
			// comparison repeated, and it inflates the same way.
			//
			// NO stable-person exemption here, unlike the comparison above. A
			// comparison of two person reads is deny-closed when both are empty: they
			// compare EQUAL, so a "must differ" check refuses. A COUNT is the opposite —
			// the empty string is a perfectly good map key, so a row no person stands
			// behind becomes an approver and inflates the total. That is M10 itself, and
			// the gate used to certify this exact shape as clean.
			if isPartyExpr(node.Index) {
				out = append(out, violation{rel, fset.Position(node.Index.Pos()).Line, name, render(node.Index)})
			}
		}
		return true
	})
	return dedupe(out)
}

// scanCollapse reports the places fn turns the three-valued verdict into a boolean and
// authorizes on its negation. Two shapes, because removing the wrapper would only have
// closed the first: the exported wrapper (`!auth.SamePerson(a, b)`) and the same thing
// rebuilt by hand out of the verdict (`a.Compare(b) != auth.PersonSame`).
//
// It names the person-sameness EXPRESSION rather than the `!`, because that is the thing
// the author has to replace with auth.TwoDistinctPeople.
func scanCollapse(fset *token.FileSet, rel, name string, fn *ast.FuncDecl) []violation {
	// A function that routes through TwoDistinctPeople has already had the verdict decided
	// FOR it — `ok` is the decision — so reading the verdict afterwards is how it phrases
	// the refusal, not how it authorizes. That is the shape core/api/dr_handler.go and
	// modules/sourcescope/posture.go use, and it must stay clean or the rule would forbid
	// its own answer.
	if referencesIdent(fn, "TwoDistinctPeople") {
		return nil
	}
	// The collapse survives being given a name, so bind the names first: `same :=
	// SamePerson(a, b)` and `m := a.Compare(b)` are the same two expressions one
	// assignment later. This is deliberately shallow — one hop, within the function — and
	// the header says so.
	boolAlias, verdictAlias := map[string]bool{}, map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, rhs := range as.Rhs {
			id, ok := as.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			if isPersonSamenessCall(rhs) {
				boolAlias[id.Name] = true
			}
			if isCompareCall(rhs) {
				verdictAlias[id.Name] = true
			}
		}
		return true
	})
	isVerdict := func(e ast.Expr) bool {
		if isCompareCall(e) {
			return true
		}
		id, ok := e.(*ast.Ident)
		return ok && verdictAlias[id.Name]
	}

	var out []violation
	add := func(e ast.Expr) { out = append(out, violation{rel, fset.Position(e.Pos()).Line, name, render(e)}) }
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.UnaryExpr:
			// !SamePerson(a, b) — "not the same person" is not "two people".
			if node.Op != token.NOT {
				return true
			}
			if isPersonSamenessCall(node.X) {
				add(node.X)
			} else if id, ok := node.X.(*ast.Ident); ok && boolAlias[id.Name] {
				add(id)
			}
		case *ast.BinaryExpr:
			// A verdict weighed against PersonSame ALONE, in either direction. PersonMatch
			// has four values; testing one of them decides authorization on an incomplete
			// case analysis, whichever way the branch falls. `!= PersonSame` authorizes
			// two cases it never looked at; `== PersonSame` denies one and lets the other
			// three through the fall-through. Both are the same defect.
			if node.Op != token.EQL && node.Op != token.NEQ {
				return true
			}
			for _, pair := range [][2]ast.Expr{{node.X, node.Y}, {node.Y, node.X}} {
				if isVerdict(pair[0]) && strings.HasSuffix(render(pair[1]), "PersonSame") {
					add(pair[0])
				}
			}
		case *ast.SwitchStmt:
			// switch a.Compare(b) { case PersonSame: deny; default: allow } — the same
			// incomplete analysis with the comparison implied by the case clause.
			if node.Tag != nil && isVerdict(node.Tag) {
				add(node.Tag)
			}
		}
		return true
	})
	return out
}

// isPersonSamenessCall reports whether e calls one of the boolean wrappers.
func isPersonSamenessCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr: // auth.SamePerson(a, b) / ref.SamePersonAs(o)
		return personSameness[fn.Sel.Name]
	case *ast.Ident: // SamePerson(a, b), inside package auth itself
		return personSameness[fn.Name]
	}
	return false
}

// isCompareCall reports whether e is a `.Compare(...)` call — the verdict, about to be
// squeezed into a bit.
func isCompareCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Compare" && len(call.Args) == 1
}

// isPartyExpr reports whether e names a party in a two-person control: a call to
// Actor(), a stored column read of a party role, or an identifier/field named for one.
func isPartyExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if sel.Sel.Name == "Actor" {
			return true
		}
		// rec.String(colPRProposer) / d.String(colDecider)
		if sel.Sel.Name == "String" && len(x.Args) == 1 {
			return partyRole.MatchString(trimColPrefix(render(x.Args[0]))) || stablePerson.MatchString(render(x.Args[0]))
		}
	case *ast.SelectorExpr:
		return partyRole.MatchString(x.Sel.Name)
	case *ast.Ident:
		return partyRole.MatchString(x.Name)
	}
	return false
}

// isStablePersonExpr reports whether e already reads the PERSON rather than the
// credential — a *_user column, a UserID field, a variable named for the user.
func isStablePersonExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "String" && len(x.Args) == 1 {
			return stablePerson.MatchString(render(x.Args[0]))
		}
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			return stablePerson.MatchString(sel.Sel.Name)
		}
	case *ast.SelectorExpr:
		return stablePerson.MatchString(x.Sel.Name)
	case *ast.Ident:
		return stablePerson.MatchString(x.Name)
	}
	return false
}

// trimColPrefix turns colPRProposer / colDecider into the bare role, so the column
// naming convention does not have to be repeated inside the regex.
func trimColPrefix(s string) string {
	s = strings.TrimPrefix(s, "col")
	for _, p := range []string{"PR", "KS", "BG", "AC"} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

// referencesIdent reports whether fn mentions the named identifier anywhere.
func referencesIdent(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func referencesPrimitive(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && primitiveIdents[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

func render(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return render(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		var args []string
		for _, a := range x.Args {
			args = append(args, render(a))
		}
		return render(x.Fun) + "(" + strings.Join(args, ", ") + ")"
	case *ast.BasicLit:
		return x.Value
	case *ast.IndexExpr:
		return render(x.X) + "[" + render(x.Index) + "]"
	}
	return "?"
}

func recvName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		return render(star.X)
	}
	return render(e)
}

func dedupe(in []violation) []violation {
	seen := map[string]bool{}
	var out []violation
	for _, v := range in {
		k := fmt.Sprintf("%s:%d:%s", v.file, v.line, v.expr)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func walkGoFiles(t *testing.T, base string, fn func(string, *token.FileSet, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(base, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if de.IsDir() {
			switch name := de.Name(); {
			case name == "node_modules", name == "testdata", name == "vendor", strings.HasPrefix(name, "."):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Errorf("read %s: %v", path, rerr)
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		fn(path, fset, file)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.work not found above %s", dir)
		}
		dir = parent
	}
}
