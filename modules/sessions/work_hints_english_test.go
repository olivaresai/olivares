// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/olivaresai/olivares/core/model"
)

// TestWorkValidationHintsAreEnglish pins the exact hint a caller reads in `evidence_ref` when
// validate, plan or apply rejects a work command. The hints reach the REST API, the CLI text
// output and `-o json`, so they are user-facing text and US English (olivares-dev #506).
func TestWorkValidationHintsAreEnglish(t *testing.T) {
	t.Parallel()

	create := func(acceptance ...AcceptanceInput) WorkCommand {
		return WorkCommand{
			Command: "item.create", WorkspaceID: model.NewID(), Title: "t", BriefMD: "b",
			WorkKind: "task", Priority: "p2", OwnerKind: "user", OwnerRef: "u",
			ProvenanceKind: "human", ProvenanceRef: "user:x", Acceptance: acceptance,
		}
	}
	depID := model.NewID()
	for _, row := range []struct {
		name string
		cmd  WorkCommand
		want string
	}{
		{"create without acceptance", create(), "acceptance (1 to 64 criteria)"},
		{"create with a keyless criterion", create(AcceptanceInput{Statement: "s"}),
			"criterion_key (flag --criterion-key, or acceptance[].key)"},
		{"create with a statementless criterion", create(AcceptanceInput{Key: "k1"}),
			"statement (flag --statement, or acceptance[].statement)"},
		{"create without a required criterion", create(AcceptanceInput{Key: "k1", Statement: "s"}),
			"required (flag --required; at least one required criterion)"},
		{"create with a duplicate criterion key",
			create(AcceptanceInput{Key: "k1", Statement: "s", Required: true},
				AcceptanceInput{Key: "k1", Statement: "s"}),
			"criterion_key (flag --criterion-key; duplicated in this same command)"},
		{"block without a code",
			WorkCommand{Command: "item.block", WorkItemID: model.NewID(), Reason: "r"},
			"blocked_code (or code)"},
		{"fail without a code",
			WorkCommand{Command: "item.fail", WorkItemID: model.NewID(), Reason: "r"},
			"terminal_code (or code)"},
		{"update with nothing to update",
			WorkCommand{Command: "item.update", WorkItemID: model.NewID()},
			"title|brief_md|priority|context_refs|due_at (at least one)"},
		{"unknown command", WorkCommand{Command: "item.teleport"}, "command (unrecognized command)"},
		{"update with too many context refs",
			WorkCommand{Command: "item.update", WorkItemID: model.NewID(), ContextRefs: make([]ContextRef, 65)},
			"context_refs (at most 64)"},
		{"dependency on the item itself",
			WorkCommand{Command: "dependency.add", WorkItemID: depID, DependsOnID: depID},
			"depends_on_id (cannot be the work_item_id itself)"},
		{"dependency removal without a target",
			WorkCommand{Command: "dependency.remove", WorkItemID: model.NewID()},
			"target_id (or dependency_id)"},
		{"acceptance update that sends the key",
			WorkCommand{Command: "acceptance.update", WorkItemID: model.NewID(), CriterionID: model.NewID(),
				Acceptance: []AcceptanceInput{{Key: "k", Statement: "s"}}},
			"criterion_key (immutable: not sent on an update)"},
		{"decision without a statement",
			WorkCommand{Command: "decision.set", WorkItemID: model.NewID(), DecisionKey: "k", AuthorityRef: "a"},
			"statement_md (or statement)"},
		{"internal lease.expire from outside",
			WorkCommand{Command: "lease.expire", WorkItemID: model.NewID()},
			"command (lease.expire is internal: not accepted from outside)"},
		{"internal lease.owner_died from outside",
			WorkCommand{Command: "lease.owner_died", WorkItemID: model.NewID()},
			"command (lease.owner_died is internal: not accepted from outside)"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			err := validateCommandSyntax(row.cmd)
			if we := asWorkError(err); we == nil || we.field != row.want {
				t.Fatalf("validateCommandSyntax hint = %q, want %q (err=%v)", fieldOf(err), row.want, err)
			}
			// validate and plan carry the hint in checks[0].evidence_ref: check the real hand-off.
			a, ok := assessmentFromError("t", err)
			if !ok || len(a.Checks) != 1 || a.Checks[0].EvidenceRef != row.want {
				t.Fatalf("assessment evidence_ref = %+v, want %q", a.Checks, row.want)
			}
		})
	}
}

func fieldOf(err error) string {
	if we := asWorkError(err); we != nil {
		return we.field
	}
	return ""
}

// spanishHintWords are Spanish words that are not English words. The hint text of the work plane
// is the only place they appeared, so a hit means a caller-facing hint went back to Spanish.
const spanishHintVocabulary = "entre maximo obligatorio bandera comando reconocido criterio criterios menos solo vale vacio crear crea congelado congelada evaluar inmutable aqui envia excede conjunto serializado puede propio exactamente duplicada mismo participante prefijo uno con sin una los las del nombra definicion esta interno acepta exterior campo falta sobra o y de el la al es un debe ser invalido invalida largo valor requerido requerida formato demasiado positivo negativo vacia tiene tienen hay ningun ninguno" // language-data: Spanish hint rejection vocabulary

var spanishHintWords = setOf(strings.Fields(spanishHintVocabulary)...)

var hexLiteralRE = regexp.MustCompile(`^[0-9a-f]+$`)

// TestWorkPlaneStringLiteralsAreEnglish scans every string literal of the work plane's non-test
// source files (the hint text is built in work_state.go and work_service.go, and any other
// work_*.go file may pass one to brokenField) so a new Spanish hint cannot slip in beside the
// ones the table above pins. Its subject is the source, so it follows the files, not a list of
// hints typed here.
func TestWorkPlaneStringLiteralsAreEnglish(t *testing.T) {
	t.Parallel()
	var offenders []string
	checked := 0
	files, err := filepath.Glob("work_*.go")
	if err != nil || len(files) < 5 {
		t.Fatalf("work_*.go glob found %d files (err=%v): the scanner is not looking at the work plane", len(files), err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			checked++
			if hexLiteralRE.MatchString(s) {
				return true // a digest, not text: the digit split would leave stray letters like "de"
			}
			for _, word := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
				if spanishHintWords[word] || strings.ContainsFunc(word, func(r rune) bool { return r > unicode.MaxASCII }) {
					offenders = append(offenders, file+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+": "+strconv.Quote(s))
					break
				}
			}
			return true
		})
	}
	if checked < 100 {
		t.Fatalf("scanned only %d string literals: the scanner is not looking at the hint source", checked)
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("%d Spanish or non-ASCII string literals reach callers; write them in US English:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
