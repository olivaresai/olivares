// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command openapi-op-descriptions GENERATES the published `description` of every
// operation in the two OpenAPI documents, and FAILS when the published documents and
// the code have drifted apart.
//
// WHY A GENERATOR AND NOT 757 HAND-WRITTEN LINES. Measured 2026-08-16 on this branch:
// web/openapi/openapi.json publishes 71 operations and web/openapi/openapi.beta.json
// publishes 686, and NOT ONE of the 757 carried a description — every one had a
// summary and nothing else. The beta summary is a mechanical restatement of the
// registration ("finops module route (requires finops:spend:read)"), so an integrator
// reading the contract learns the permission and nothing about what the call does.
//
// THE BETA DOCUMENT IS A REFLECTOR AND STAYS ONE. core/api/openapi_modules.go builds
// the beta document from the routes the modules actually register, so a module that
// adds a route adds it to the document for free. A hand-written description table
// would break exactly that: the day a module registers route 687, the table would be
// silently short by one. So the description is taken from the prose that ALREADY
// lives next to the route — the handler's Go doc comment — read with go/parser, not
// grep. A module that adds a route with a documented handler gets its published
// description for free, in the same way it gets the route itself.
//
// WHAT IS WRITTEN BY HAND. Exactly one thing: scripts/openapi-op-catalog.tsv, one
// sentence per operation whose handler does not document itself in a form that can be
// published (see derive.go for the four rejections, each of which NAMES the route).
// The catalog does not decide the roster: a row with no operation is red, and an
// operation with neither a publishable doc comment nor a row is red. The stable
// document's operations are built by hand in core/api/openapi.go rather than
// registered through a seam, so every one of them is a catalog row.
//
// THE THREE DIRECTIONS THIS GATE FAILS IN, all of them naming the route:
//   - an operation is published with no description, or with a description that is
//     not the one the code and the catalog compose;
//   - core/api/openapi_op_descriptions.gen.go is not what regenerating produces
//     (someone edited it, or added a route and did not regenerate);
//   - a catalog row describes an operation that no longer exists, or an operation
//     whose handler now documents itself (two sources for one description).
//
// AND IT FAILS CLOSED. If the module routes and the committed beta document disagree
// about WHICH operations exist, this gate does not check descriptions against a stale
// document and call it clean: it exits 2 (CANNOT LOOK) naming the difference. Same
// for a missing input, an unparseable document, or an empty enumeration.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Exit codes. Three answers, never two: "I could not look" is not "in sync".
const (
	exitClean     = 0
	exitDrift     = 1
	exitCannotSee = 2
)

const (
	stableSpecRel  = "web/openapi/openapi.json"
	betaSpecRel    = "web/openapi/openapi.beta.json"
	catalogRel     = "scripts/openapi-op-catalog.tsv"
	generatedRel   = "core/api/openapi_op_descriptions.gen.go"
	modulesDirRel  = "modules"
	betaPathPrefix = "/v1/m/"
)

// maxNamed caps how many offenders are printed in one report. Every FAILURE prints
// its count too, so a truncated list never reads as the whole list.
const maxNamed = 40

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--self-test" {
		os.Exit(selfTest())
	}
	var (
		root  = flag.String("root", ".", "repository root")
		write = flag.Bool("write", false, "regenerate "+generatedRel)
		list  = flag.Bool("list", false, "print the composed roster (key, source, description)")
		miss  = flag.Bool("missing", false, "print every operation that still needs a catalog row, and why")
	)
	flag.Parse()
	if *miss {
		os.Exit(runMissing(*root, os.Stdout, os.Stderr))
	}
	os.Exit(run(*root, *write, *list, os.Stdout, os.Stderr))
}

// runMissing prints the operations that still have no description, one per line, so a
// human filling scripts/openapi-op-catalog.tsv works from a list instead of from the
// capped report. It answers the same question as a failing run; it is not a second
// verdict, and it never exits 0 with work outstanding.
func runMissing(root string, out, errw io.Writer) int {
	m, err := compose(root)
	if err != nil {
		fmt.Fprintf(errw, "openapi-op-descriptions: CANNOT LOOK — %v\n", err)
		return exitCannotSee
	}
	for _, p := range m.problems {
		fmt.Fprintf(out, "%s\t%s\n", p.key, p.why)
	}
	if len(m.problems) > 0 {
		return exitDrift
	}
	return exitClean
}

// cannotLook is the error type that means "the inputs did not let me answer the
// question", which is exit 2 and never exit 0.
type cannotLook struct{ msg string }

func (e cannotLook) Error() string { return e.msg }

func blind(format string, a ...any) error { return cannotLook{fmt.Sprintf(format, a...)} }

func run(root string, write, list bool, out, errw io.Writer) int {
	model, err := compose(root)
	if err != nil {
		var cl cannotLook
		if errors.As(err, &cl) {
			fmt.Fprintf(errw, "openapi-op-descriptions: CANNOT LOOK — %s\n", cl.msg)
			fmt.Fprintf(errw, "  A gate that could not enumerate is not a gate that passed.\n")
			return exitCannotSee
		}
		fmt.Fprintf(errw, "openapi-op-descriptions: CANNOT LOOK — %v\n", err)
		return exitCannotSee
	}

	if list {
		for _, k := range model.keys {
			e, ok := model.entries[k]
			if !ok {
				// An operation nothing could describe. Until 2026-08-17 this printed
				// "<key>\t\t" and the command exited 0 — a roster with a hole in it,
				// rendered as a roster, answered with the same code that means "every
				// operation has one". Read as a TSV, the hole was an empty cell.
				fmt.Fprintf(out, "%s\tNONE\t!! no description composed for this operation\n", k)
				continue
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", k, e.source, e.description)
		}
		if len(model.problems) > 0 {
			reportProblems(errw, model)
			return exitDrift
		}
		return exitClean
	}

	rendered := renderGenerated(model)

	if write {
		if len(model.problems) > 0 {
			// -write must not paper over an unanswerable roster: writing a file that
			// omits the operations it could not describe would make the NEXT run green
			// with those operations still undescribed in the published document.
			reportProblems(errw, model)
			fmt.Fprintf(errw, "openapi-op-descriptions: refusing to write %s while %d operation(s) have no description.\n",
				generatedRel, len(model.problems))
			return exitDrift
		}
		if err := os.WriteFile(filepath.Join(root, generatedRel), []byte(rendered), 0o644); err != nil {
			fmt.Fprintf(errw, "openapi-op-descriptions: CANNOT LOOK — writing %s: %v\n", generatedRel, err)
			return exitCannotSee
		}
		fmt.Fprintf(out, "openapi-op-descriptions: wrote %s (%d operations: %d from a handler doc comment, %d from %s)\n",
			generatedRel, len(model.keys), model.countBySource(sourceDoc), model.countBySource(sourceCatalog), catalogRel)
		fmt.Fprintf(out, "  Now refresh the published documents and the typed web client:\n")
		fmt.Fprintf(out, "    task openapi:dump && pnpm --dir web run codegen\n")
		return exitClean
	}

	failed := false

	if len(model.problems) > 0 {
		reportProblems(errw, model)
		failed = true
	}
	if len(model.orphans) > 0 {
		fmt.Fprintf(errw, "openapi-op-descriptions: %d row(s) of %s describe an operation that is not published:\n",
			len(model.orphans), catalogRel)
		for i, o := range model.orphans {
			if i == maxNamed {
				fmt.Fprintf(errw, "  … and %d more\n", len(model.orphans)-maxNamed)
				break
			}
			fmt.Fprintf(errw, "  %s (%s:%d) — delete the row or restore the operation\n", o.key, catalogRel, o.line)
		}
		failed = true
	}
	if len(model.redundant) > 0 {
		fmt.Fprintf(errw, "openapi-op-descriptions: %d row(s) of %s describe an operation whose handler now documents itself:\n",
			len(model.redundant), catalogRel)
		for i, o := range model.redundant {
			if i == maxNamed {
				fmt.Fprintf(errw, "  … and %d more\n", len(model.redundant)-maxNamed)
				break
			}
			fmt.Fprintf(errw, "  %s (%s:%d) — delete the row; the doc comment on %s is the single source\n",
				o.key, catalogRel, o.line, o.handler)
		}
		failed = true
	}

	// The generated file must be exactly what regenerating produces.
	committed, err := os.ReadFile(filepath.Join(root, generatedRel))
	switch {
	case err != nil:
		fmt.Fprintf(errw, "openapi-op-descriptions: CANNOT LOOK — %s is missing (%v), so nothing stamps the\n", generatedRel, err)
		fmt.Fprintf(errw, "  published documents and no comparison is possible.\n")
		return exitCannotSee
	case string(committed) != rendered:
		fmt.Fprintf(errw, "openapi-op-descriptions: %s is not what regenerating produces.\n", generatedRel)
		reportGeneratedDiff(errw, string(committed), rendered)
		fmt.Fprintf(errw, "  Regenerate with: bash scripts/check-openapi-op-descriptions.sh --write\n")
		failed = true
	}

	// …and the PUBLISHED documents must carry those descriptions. This is the check
	// that makes the artifact true rather than the table: a regenerated .gen.go that
	// nobody dumped leaves 757 operations undescribed in the file integrators read.
	if n := reportPublishedDrift(errw, model); n > 0 {
		failed = true
	}

	if failed {
		return exitDrift
	}
	fmt.Fprintf(out, "OK openapi-op-descriptions: %d published operations, every one with a description "+
		"(%d from a handler doc comment, %d from %s; %d stable + %d beta)\n",
		len(model.keys), model.countBySource(sourceDoc), model.countBySource(sourceCatalog), catalogRel,
		model.countByTier(tierStable), model.countByTier(tierBeta))
	return exitClean
}

func reportProblems(errw io.Writer, m *model) {
	fmt.Fprintf(errw, "openapi-op-descriptions: %d published operation(s) have no description:\n", len(m.problems))
	for i, p := range m.problems {
		if i == maxNamed {
			fmt.Fprintf(errw, "  … and %d more\n", len(m.problems)-maxNamed)
			break
		}
		fmt.Fprintf(errw, "  %s — %s\n", p.key, p.why)
		fmt.Fprintf(errw, "      fix: %s\n", p.fix)
	}
}

func reportGeneratedDiff(errw io.Writer, committed, want string) {
	old := parseGeneratedKeys(committed)
	new := parseGeneratedKeys(want)
	var added, removed, changed []string
	for k, v := range new {
		ov, ok := old[k]
		switch {
		case !ok:
			added = append(added, k)
		case ov != v:
			changed = append(changed, k)
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	named := func(label string, keys []string) {
		if len(keys) == 0 {
			return
		}
		fmt.Fprintf(errw, "  %d %s:\n", len(keys), label)
		for i, k := range keys {
			if i == maxNamed {
				fmt.Fprintf(errw, "    … and %d more\n", len(keys)-maxNamed)
				break
			}
			fmt.Fprintf(errw, "    %s\n", k)
		}
	}
	named("operation(s) the code has and the generated file does not", added)
	named("operation(s) the generated file has and the code does not", removed)
	named("operation(s) whose description changed", changed)
	if len(added)+len(removed)+len(changed) == 0 {
		fmt.Fprintf(errw, "  the entries agree but the bytes do not: the file was edited around them\n")
	}
}

// reportPublishedDrift compares the description each published operation CARRIES with
// the one the code and the catalog compose, and names every disagreement.
func reportPublishedDrift(errw io.Writer, m *model) int {
	type miss struct{ key, why string }
	var missing []miss
	for _, k := range m.keys {
		want := m.entries[k].description
		got := m.published[k]
		switch {
		case got == "":
			missing = append(missing, miss{k, "the published document carries no description for it"})
		case got != want:
			missing = append(missing, miss{k, "the published description is not the one the code composes"})
		}
	}
	if len(missing) == 0 {
		return 0
	}
	fmt.Fprintf(errw, "openapi-op-descriptions: %d published operation(s) do not carry the composed description:\n", len(missing))
	for i, mm := range missing {
		if i == maxNamed {
			fmt.Fprintf(errw, "  … and %d more\n", len(missing)-maxNamed)
			break
		}
		fmt.Fprintf(errw, "  %s — %s\n", mm.key, mm.why)
	}
	fmt.Fprintf(errw, "  Refresh them with: task openapi:dump && pnpm --dir web run codegen\n")
	return len(missing)
}

// --- the composed model -------------------------------------------------------

type descSource string

const (
	sourceDoc     descSource = "handler-doc"
	sourceCatalog descSource = "catalog"
)

type tier string

const (
	tierStable tier = "stable"
	tierBeta   tier = "beta"
)

type entry struct {
	description string
	source      descSource
	tier        tier
}

type problem struct{ key, why, fix string }

type rowRef struct {
	key     string
	line    int
	handler string
}

type model struct {
	keys      []string // sorted; the published roster
	entries   map[string]entry
	published map[string]string // description as the committed documents carry it
	problems  []problem
	orphans   []rowRef
	redundant []rowRef
}

func (m *model) countBySource(s descSource) int {
	n := 0
	for _, e := range m.entries {
		if e.source == s {
			n++
		}
	}
	return n
}

func (m *model) countByTier(t tier) int {
	n := 0
	for _, e := range m.entries {
		if e.tier == t {
			n++
		}
	}
	return n
}

// compose is the whole question: which operations are published, what description does
// each one get, and where does it come from.
func compose(root string) (*model, error) {
	stable, err := readPublishedOps(filepath.Join(root, stableSpecRel))
	if err != nil {
		return nil, err
	}
	beta, err := readPublishedOps(filepath.Join(root, betaSpecRel))
	if err != nil {
		return nil, err
	}
	if len(stable) == 0 {
		return nil, blind("%s publishes no operations at all; every check below would pass vacuously", stableSpecRel)
	}
	if len(beta) == 0 {
		return nil, blind("%s publishes no operations at all; every check below would pass vacuously", betaSpecRel)
	}
	for k := range beta {
		if _, dup := stable[k]; dup {
			return nil, blind("%q is published by BOTH documents, so one key cannot name one description", k)
		}
	}

	routes, err := enumerateModuleRoutes(root)
	if err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, blind("no module route was found under %s/, while %s publishes %d operations",
			modulesDirRel, betaSpecRel, len(beta))
	}

	// FAIL CLOSED ON A STALE DOCUMENT. The beta document is the reflection of these
	// routes; if the two sets differ, the committed document is not the one this code
	// builds, and checking descriptions against it would be checking yesterday's
	// contract.
	byKey := map[string]moduleRoute{}
	for _, r := range routes {
		k := r.key()
		if prev, dup := byKey[k]; dup {
			return nil, blind("%s is registered twice (%s:%d and %s:%d), so one operation would carry two descriptions",
				k, prev.file, prev.line, r.file, r.line)
		}
		byKey[k] = r
	}
	if msg := setDifference(byKey, beta); msg != "" {
		return nil, blind("the registered module routes and %s disagree about which operations exist:\n%s"+
			"  Refresh the document with: task openapi:dump", betaSpecRel, msg)
	}

	catalog, err := readCatalog(filepath.Join(root, catalogRel))
	if err != nil {
		return nil, err
	}

	m := &model{entries: map[string]entry{}, published: map[string]string{}}
	for k, op := range stable {
		m.keys = append(m.keys, k)
		m.published[k] = op.description
	}
	for k, op := range beta {
		m.keys = append(m.keys, k)
		m.published[k] = op.description
	}
	sort.Strings(m.keys)

	used := map[string]bool{}
	for _, k := range m.keys {
		t := tierStable
		if strings.HasPrefix(strings.SplitN(k, " ", 2)[1], betaPathPrefix) {
			t = tierBeta
		}
		route, isModuleRoute := byKey[k]

		derived, derr := "", error(nil)
		if isModuleRoute {
			derived, derr = deriveFromDoc(route.handlerName, route.doc)
		}

		if row, ok := catalog[k]; ok {
			used[k] = true
			if derr == nil && isModuleRoute {
				m.redundant = append(m.redundant, rowRef{key: k, line: row.line, handler: route.where()})
				continue
			}
			if err := validateDescription(row.description); err != nil {
				m.problems = append(m.problems, problem{
					key: k,
					why: fmt.Sprintf("its row in %s is not publishable: %v", catalogRel, err),
					fix: fmt.Sprintf("rewrite %s:%d", catalogRel, row.line),
				})
				continue
			}
			m.entries[k] = entry{description: row.description, source: sourceCatalog, tier: t}
			continue
		}

		switch {
		case isModuleRoute && derr == nil:
			m.entries[k] = entry{description: derived, source: sourceDoc, tier: t}
		case isModuleRoute:
			m.problems = append(m.problems, problem{
				key: k,
				why: fmt.Sprintf("%s cannot be published as written: %v", route.where(), derr),
				fix: fmt.Sprintf("document the handler so it can be published, or add a row to %s", catalogRel),
			})
		default:
			m.problems = append(m.problems, problem{
				key: k,
				why: fmt.Sprintf("it is built by hand in core/api/openapi.go and has no row in %s", catalogRel),
				fix: fmt.Sprintf("add a row to %s: %s<TAB>one sentence", catalogRel, strings.Replace(k, " ", "\t", 1)),
			})
		}
	}

	for k, row := range catalog {
		if !used[k] {
			m.orphans = append(m.orphans, rowRef{key: k, line: row.line})
		}
	}
	sort.Slice(m.orphans, func(i, j int) bool { return m.orphans[i].line < m.orphans[j].line })
	sort.Slice(m.redundant, func(i, j int) bool { return m.redundant[i].line < m.redundant[j].line })
	sort.Slice(m.problems, func(i, j int) bool { return m.problems[i].key < m.problems[j].key })
	return m, nil
}

// setDifference renders the disagreement between the registered routes and the
// published beta operations, or "" when they are the same set.
func setDifference(routes map[string]moduleRoute, published map[string]publishedOp) string {
	var onlyCode, onlyDoc []string
	for k := range routes {
		if _, ok := published[k]; !ok {
			onlyCode = append(onlyCode, k)
		}
	}
	for k := range published {
		if _, ok := routes[k]; !ok {
			onlyDoc = append(onlyDoc, k)
		}
	}
	if len(onlyCode)+len(onlyDoc) == 0 {
		return ""
	}
	sort.Strings(onlyCode)
	sort.Strings(onlyDoc)
	var b strings.Builder
	block := func(label string, keys []string) {
		if len(keys) == 0 {
			return
		}
		fmt.Fprintf(&b, "  %d %s:\n", len(keys), label)
		for i, k := range keys {
			if i == maxNamed {
				fmt.Fprintf(&b, "    … and %d more\n", len(keys)-maxNamed)
				break
			}
			fmt.Fprintf(&b, "    %s\n", k)
		}
	}
	block("registered by a module and NOT in the document", onlyCode)
	block("in the document and registered by no module", onlyDoc)
	return b.String()
}

// --- the published documents --------------------------------------------------

type publishedOp struct{ description string }

var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// readPublishedOps reads one OpenAPI document and returns "<METHOD> <path>" →
// operation. Anything it cannot read is CANNOT LOOK: an unreadable contract is not an
// empty one.
func readPublishedOps(path string) (map[string]publishedOp, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, blind("%v — the published contract could not be read", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, blind("%s is not a document this gate can read: %v", path, err)
	}
	if doc.Paths == nil {
		return nil, blind("%s has no `paths` object", path)
	}
	out := map[string]publishedOp{}
	for p, item := range doc.Paths {
		for method, rawOp := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue // parameters, summary, $ref and the other path-item fields
			}
			var op struct {
				Description string `json:"description"`
			}
			if err := json.Unmarshal(rawOp, &op); err != nil {
				return nil, blind("%s: %s %s is not an operation object: %v", path, strings.ToUpper(method), p, err)
			}
			out[strings.ToUpper(method)+" "+p] = publishedOp{description: op.Description}
		}
	}
	return out, nil
}

// --- the hand-written catalog -------------------------------------------------

type catalogRow struct {
	description string
	line        int
}

// readCatalog reads the ONE hand-written input: "<METHOD>\t<path>\t<description>".
// A malformed row is a failure, not a skipped line — a row nobody reads is a
// description nobody publishes.
func readCatalog(path string) (map[string]catalogRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, blind("%v — the hand-written description catalog could not be read", err)
	}
	out := map[string]catalogRow{}
	for i, line := range strings.Split(string(raw), "\n") {
		n := i + 1
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, blind("%s:%d has %d tab-separated fields, want 3 (METHOD, path, description)", path, n, len(parts))
		}
		method, p, desc := parts[0], parts[1], parts[2]
		if !httpMethods[strings.ToLower(method)] || method != strings.ToUpper(method) {
			return nil, blind("%s:%d: %q is not an upper-case HTTP method", path, n, method)
		}
		if !strings.HasPrefix(p, "/") {
			return nil, blind("%s:%d: %q is not a path", path, n, p)
		}
		key := method + " " + p
		if prev, dup := out[key]; dup {
			return nil, blind("%s:%d: %s is already described at line %d", path, n, key, prev.line)
		}
		out[key] = catalogRow{description: desc, line: n}
	}
	return out, nil
}

// --- the generated file -------------------------------------------------------

const generatedHeader = `// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Code generated by scripts/openapi-op-descriptions. DO NOT EDIT.
//
// One description per operation published by the two OpenAPI documents, keyed by
// "<METHOD> <spec path>". handlerDocOperationDescriptions separately records which
// descriptions were derived directly from the registered handler's doc comment; every
// other description is a row of scripts/openapi-op-catalog.tsv. Regenerate with:
//
//	bash scripts/check-openapi-op-descriptions.sh --write
//	task openapi:dump && pnpm --dir web run codegen

package api

// operationDescriptions is consulted by applyOperationDescriptions (see
// openapi_descriptions.go), which stamps the description onto every operation of both
// published documents.
var operationDescriptions = map[string]string{
`

func renderGenerated(m *model) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	for _, k := range m.keys {
		e, ok := m.entries[k]
		if !ok {
			continue // an operation with no description; the caller already failed
		}
		fmt.Fprintf(&b, "\t%q: %q,\n", k, e.description)
	}
	b.WriteString("}\n\n")
	b.WriteString("// handlerDocOperationDescriptions is the set whose prose was derived directly\n")
	b.WriteString("// from the registered handler's Go doc comment, rather than supplied by the catalog.\n")
	b.WriteString("var handlerDocOperationDescriptions = map[string]struct{}{\n")
	for _, k := range m.keys {
		e, ok := m.entries[k]
		if !ok || e.source != sourceDoc {
			continue
		}
		fmt.Fprintf(&b, "\t%q: {},\n", k)
	}
	b.WriteString("}\n")
	// gofmt the result HERE rather than trusting the emitter's spacing: gofmt aligns a
	// map literal's values in columns, so an unformatted emission is a file the repo's
	// format gate rejects and, worse, one whose bytes change the first time anybody runs
	// gofmt on it — which would read as drift this gate cannot explain.
	out, err := format.Source([]byte(b.String()))
	if err != nil {
		// Unreachable with %q-quoted keys and values; if it ever happens, the caller
		// compares the unformatted text and reports a difference rather than writing
		// something it could not parse.
		return b.String()
	}
	return string(out)
}

// parseGeneratedKeys reads back the entries of a rendered file so a difference can be
// reported as operations rather than as bytes.
func parseGeneratedKeys(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`) || !strings.HasSuffix(line, `,`) {
			continue
		}
		k, rest, ok := splitQuoted(line)
		if !ok {
			continue
		}
		rest = strings.TrimPrefix(strings.TrimSpace(rest), ":")
		v, _, ok := splitQuoted(strings.TrimSpace(rest))
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// splitQuoted reads one Go-quoted string off the front of s and returns it unquoted
// plus the remainder. It handles the escapes %q emits (\" and \\).
func splitQuoted(s string) (string, string, bool) {
	if len(s) == 0 || s[0] != '"' {
		return "", "", false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 >= len(s) {
				return "", "", false
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(s[i])
		}
	}
	return "", "", false
}
