// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command config-env-docs GENERATES the public reference of every OLIVARES_*
// environment variable the product reads, and FAILS when the published page and the
// code have drifted apart.
//
// WHY A GENERATOR AND NOT A PAGE. On 2026-08-15 the public configuration reference
// documented 7 of the 311 OLIVARES_* names the non-test Go sources declare — 2.3 % —
// while calling them "a small set of environment variables". A hand-written list of
// 311 names is stale the day somebody adds the 312th, and nothing goes red. So the
// roster is ENUMERATED FROM THE CODE on every run and the page is rewritten from that
// enumeration; the only hand-written part is one English sentence per variable, kept
// in scripts/config-env-catalog.tsv, and a missing sentence is what turns this red.
//
// WHAT IS ENUMERATED. Every string literal matching ^OLIVARES_[A-Z0-9_]+$ in every
// non-test .go file under the scanned tree, read through go/parser — an AST, not a
// grep, so a name that only appears in a comment ("note that the closed side reads
// OLIVARES_CIRCUIT_BREAKER_CONFIG") is prose about a key, not a declaration of one,
// and does not enter the roster. That distinction is the same one
// cmd/olivares/config_registry_test.go draws, and it is why this does not just run
// `grep -o`.
//
// THE THREE CLASSES, all decided by cmd/olivares/config_registry.go and not by this
// program's opinion:
//
//   - test-only — the sentinels and prefixes the registry itself declares test-only
//     (OLIVARES_TEST_, OLIVARES_E2E_, …). Not product configuration; excluded from
//     the page and REPORTED, so an exclusion is never silent.
//   - family — a literal ending in "_", or the stem a registered prefix is built
//     from (the "OLIVARES_EMBEDDINGS" that a helper appends _KEY to). It names a
//     family whose members are constructed at runtime, not a variable.
//   - variable — everything else. This is the documented roster.
//
// FAIL CLOSED. Three answers, never two: 0 the page matches the code, 1 they have
// drifted and every name is printed, 2 CANNOT LOOK — the registry is unreadable, the
// catalog is missing, the page has no generated region, or the walk saw fewer sources
// than the population floor. "I could not enumerate" is never reported as "in sync".
//
// Usage:
//
//	config-env-docs -root <repo>            # check; exit 1 on drift, naming it
//	config-env-docs -root <repo> -write     # regenerate the page and the catalog stubs
//	config-env-docs -root <repo> -list      # print the enumeration, one name per line
//	config-env-docs --self-test             # build throwaway trees and prove it can fail
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	exitClean      = 0
	exitDrift      = 1
	exitCannotLook = 2
)

// Relative to the repo root. Every path this program reads or writes is named here so
// a reader can see the whole contract in one place.
const (
	registryPath = "cmd/olivares/config_registry.go"
	catalogPath  = "scripts/config-env-catalog.tsv"
	pagePath     = "docs-site/src/content/docs/reference/configuration.md"
)

// The generated region. The markers are HTML comments, so Starlight renders nothing
// for them, and they are matched literally — a page that lost them is CANNOT LOOK,
// not "no drift".
const (
	beginMarker = "<!-- BEGIN GENERATED olivares-env-reference — regenerate with `bash scripts/check-config-env-docs.sh --write`; do not edit by hand -->"
	endMarker   = "<!-- END GENERATED olivares-env-reference -->"
)

// populationFloor is the anti-vacuity control. A walk that parses almost nothing —
// wrong root, a rename, a skip rule that swallowed the tree — would report a clean
// page over an empty enumeration. Measured 2026-08-16 on this tree: 2088 non-test Go
// sources. The floor is deliberately far below that and far above zero.
var populationFloor = 1200

// envKeyRe is anchored on both ends: a literal is a config key or it is not, and
// "OLIVARES_FOO=bar" or "prefix OLIVARES_X" is not.
var envKeyRe = regexp.MustCompile(`^OLIVARES_[A-Z0-9_]+$`)

// skipDirNames are directory names never walked, wherever they appear.
var skipDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"testdata":     true,
	"dist":         true,
	"build":        true,
	".astro":       true,
	// ⛔ RESUELTO EL 2026-08-20, Y LA CLAVE SE QUEDA. Another lane la retiro esperando la mia y
	// yo retire la mia tomando la suya, asi que por un momento NO QUEDO NINGUNA: los dos cedimos
	// el paso y el arreglo desaparecio. Lo cazo `check-lot-heads` al re-traer la punta de #1279 y
	// contar las claves del mapa, no al leer el diff. La entrada es UNA y esta aqui.
	//
	// Su diagnostico, que es el que vale y esta en el buzon (13:51Z y 14:11Z): dos entradas con la
	// misma clave en un map literal de Go NO COMPILAN — `duplicate key ".export-tmp" in map
	// literal`. Por eso solo puede haber una, y por eso ceder los dos era peor que chocar.
	".export-tmp": true,
	// El texto original de P, conservado porque explica el sintoma si esto vuelve:
	// AQUI IBA `".export-tmp": true` Y LO RETIRO YO, porque el carril de integración escribio el MISMO
	// arreglo en `hub/r23-lote38` y **dos entradas con la misma clave en un map literal de Go no
	// compilan** — `duplicate key ".export-tmp" in map literal`, verificado antes de afirmarlo. No es
	// un duplicado tolerable que la union resuelva: es un lote roto lejos de su causa.
	//
	// El diagnostico es mio y esta en el buzon (13:51Z y 14:11Z) con su mutacion en las dos
	// direcciones; el codigo es suyo porque su lote va delante. **Lo que no podia pasar es que
	// sobrevivieran los dos**, y el que va detras es el que retira.
	//
	// ⚠ Si por lo que sea el lote 38 no aterriza, esto vuelve: el sintoma es `lint:public-counts`
	// con «22 finding(s) — the published configuration reference and the code disagree» donde los 22
	// son su propio reflejo, y el remedio de mano mientras tanto es `rm -rf .export-tmp`.
}

// skipRelDirs are named exclusions, each with its reason, in the shape
// config_registry_test.go uses. A bare skip list is how a gate stops seeing things —
// so every one of these is printed on every run by reportExclusions, with the names
// it removed. None of them exists to make the gate pass: they exist because a name
// declared in one of these places is not configuration of the shipped engine, and
// publishing it on the product's configuration page would be a false claim.
var skipRelDirs = map[string]string{
	// Repo gates and generators, not the shipped binary: OLIVARES_ERROR_MAPPER_SCAN
	// is check-error-mappers' scan-root override. This program lives here too, and
	// its selftest declares fixture keys as Go string literals — scanning ourselves
	// would publish the gate's test data as product configuration.
	"scripts": "repo gates and generators, not the engine",
	// Build-time wiring checkers, compiled as their own main packages and never
	// linked into `olivares`: OLIVARES_PG_LOCAL_DEFAULTS is checkpgwiring's local
	// opt-in.
	"cmd/olivares/tools": "build-time wiring checkers, not linked into the engine",
}

// skipRelFiles are named single-file exclusions, same contract as skipRelDirs.
var skipRelFiles = map[string]string{
	// supportPublicConfigKeys is a CAPTURE allowlist naming which environment
	// variables are safe to include in a support bundle. It is not a read: measured
	// 2026-08-16, 22 of the 23 names that appear ONLY here are read by nothing in the
	// tree — not by Go, not by a chart, not by an entrypoint (OLIVARES_BIND_ADDR,
	// OLIVARES_HOST, OLIVARES_PROFILE, OLIVARES_TIMEOUT, …). Publishing them as
	// engine configuration would document knobs that do not exist. This is the same
	// exclusion, for the same reason, that cmd/olivares/config_registry_test.go makes.
	"cmd/olivares/supportbundle.go": "a support-bundle capture allowlist, not a read",
}

type variable struct {
	Name       string
	Components map[string]bool // "cmd/olivares", "core/audit", …
	TestOnly   bool
	Family     bool
	Registered bool // configEnvKeyMode() != unknown, i.e. `config validate --strict` accepts it
}

func (v *variable) componentList() []string {
	out := make([]string, 0, len(v.Components))
	for c := range v.Components {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

type inventory struct {
	byName       map[string]*variable
	filesScanned int
	// excluded records, per named exclusion, the keys that exclusion removed from the
	// roster — a key that appears NOWHERE else. Printed on every run: an exclusion
	// whose effect nobody can see is indistinguishable from a blind spot.
	excluded map[string]map[string]bool
}

func (inv *inventory) names() []string {
	out := make([]string, 0, len(inv.byName))
	for n := range inv.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type registry struct {
	exact           []string
	prefixes        []string
	testOnlyKeys    []string
	testOnlyPrefixe []string
}

func (r *registry) isTestOnly(key string) bool {
	if contains(r.testOnlyKeys, key) {
		return true
	}
	return hasAnyPrefix(key, r.testOnlyPrefixe)
}

func (r *registry) registered(key string) bool {
	return contains(r.exact, key) || hasAnyPrefix(key, r.prefixes)
}

// isFamily reports whether the literal names a FAMILY rather than a variable: it ends
// in "_", or a registered prefix is built from it by appending more (the
// "OLIVARES_EMBEDDINGS" a helper turns into OLIVARES_EMBEDDINGS_KEY).
func (r *registry) isFamily(key string) bool {
	if strings.HasSuffix(key, "_") {
		return true
	}
	for _, p := range r.prefixes {
		if strings.HasPrefix(p, key) && p != key {
			return true
		}
	}
	return false
}

func contains(sorted []string, target string) bool {
	i := sort.SearchStrings(sorted, target)
	return i < len(sorted) && sorted[i] == target
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	return false
}

type catalogRow struct {
	Name     string
	Required bool
	Default  string
	Summary  string
}

func main() {
	var (
		root    string
		write   bool
		list    bool
		runSelf bool
	)
	flag.StringVar(&root, "root", ".", "repository root to enumerate")
	flag.BoolVar(&write, "write", false, "regenerate the page and catalog stubs instead of checking")
	flag.BoolVar(&list, "list", false, "print the enumerated roster and exit")
	flag.BoolVar(&runSelf, "self-test", false, "build throwaway trees and prove the gate can fail")
	flag.Parse()

	if runSelf {
		os.Exit(selfTest())
	}
	os.Exit(run(root, write, list, os.Stdout, os.Stderr))
}

func run(root string, write, list bool, out, errOut io.Writer) int {
	reg, err := parseRegistry(filepath.Join(root, registryPath))
	if err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", err)
		fmt.Fprintf(errOut, "  The registry at %s decides which names are test-only and which are\n", registryPath)
		fmt.Fprintf(errOut, "  runtime-constructed families. Without it every classification would be this\n")
		fmt.Fprintf(errOut, "  program's own guess, so it refuses to publish one.\n")
		return exitCannotLook
	}

	inv, err := enumerate(root, reg)
	if err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", err)
		return exitCannotLook
	}
	if inv.filesScanned < populationFloor {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — parsed %d non-test Go sources under %s, below the\n",
			inv.filesScanned, root)
		fmt.Fprintf(errOut, "  population floor of %d. A count that could not be taken is not a zero, and an\n", populationFloor)
		fmt.Fprintf(errOut, "  enumeration over almost nothing would report the page clean.\n")
		return exitCannotLook
	}

	if list {
		for _, n := range inv.names() {
			v := inv.byName[n]
			class := "variable"
			switch {
			case v.TestOnly:
				class = "test-only"
			case v.Family:
				class = "family"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", n, class, strings.Join(v.componentList(), ","))
		}
		fmt.Fprintf(errOut, "config-env-docs: %d literals over %d non-test Go sources\n", len(inv.byName), inv.filesScanned)
		reportExclusions(inv, errOut)
		return exitClean
	}

	documented := documentedRoster(inv)

	catalog, catErr := readCatalog(filepath.Join(root, catalogPath))
	if catErr != nil && !write {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", catErr)
		return exitCannotLook
	}
	if catErr != nil {
		catalog = map[string]catalogRow{}
	}

	if write {
		return doWrite(root, inv, documented, catalog, out, errOut)
	}
	return doCheck(root, inv, documented, catalog, out, errOut)
}

// documentedRoster is the published population: every enumerated literal that is not
// test-only. Families stay in — a family IS configuration surface — but they are
// rendered in their own table.
func documentedRoster(inv *inventory) []*variable {
	var out []*variable
	for _, n := range inv.names() {
		v := inv.byName[n]
		if v.TestOnly {
			continue
		}
		out = append(out, v)
	}
	return out
}

func doCheck(root string, inv *inventory, documented []*variable, catalog map[string]catalogRow, out, errOut io.Writer) int {
	var findings []string

	// (1) Every enumerated name must have a catalog row. This is the direction that
	//     closes the regression: add a variable to the code, do not regenerate, and
	//     the gate names it.
	for _, v := range documented {
		if _, ok := catalog[v.Name]; !ok {
			findings = append(findings, fmt.Sprintf(
				"%s is read by %s and has no row in %s — add one and regenerate",
				v.Name, strings.Join(v.componentList(), ", "), catalogPath))
		}
	}
	// (2) …and the other direction: a catalog row for a name the code no longer
	//     reads is a page documenting a variable that does not exist.
	inRoster := map[string]bool{}
	for _, v := range documented {
		inRoster[v.Name] = true
	}
	for _, name := range sortedKeys(catalog) {
		if !inRoster[name] {
			findings = append(findings, fmt.Sprintf(
				"%s has a row in %s but no non-test Go source declares it — remove the row and regenerate",
				name, catalogPath))
		}
	}

	findings = append(findings, validateCatalog(catalog)...)

	// (3) The published region must be byte-identical to what the code produces now.
	pageFile := filepath.Join(root, pagePath)
	current, region, err := readRegion(pageFile)
	if err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", err)
		return exitCannotLook
	}
	_ = current
	if len(findings) == 0 {
		want := regionBody(documented, catalog)
		if region != want {
			findings = append(findings, fmt.Sprintf(
				"the generated region in %s is not what the current tree produces — run `bash scripts/check-config-env-docs.sh --write`%s",
				pagePath, firstDiff(region, want)))
		}
	}

	if len(findings) > 0 {
		fmt.Fprintf(errOut, "FAIL config-env-docs: %d finding(s) — the published configuration reference and the code disagree\n\n", len(findings))
		for _, f := range findings {
			fmt.Fprintf(errOut, "  %s\n", f)
		}
		fmt.Fprintf(errOut, "\n")
		reportExclusions(inv, errOut)
		return exitDrift
	}

	vars, fams := splitCounts(documented)
	fmt.Fprintf(out, "OK config-env-docs: %d variables and %d families documented from %d non-test Go sources (%s)\n",
		vars, fams, inv.filesScanned, pagePath)
	reportExclusions(inv, out)
	reportUnregistered(inv, out)
	return exitClean
}

func doWrite(root string, inv *inventory, documented []*variable, catalog map[string]catalogRow, out, errOut io.Writer) int {
	// Seed a row for every name the catalog is missing, so the operator edits prose
	// instead of transcribing names. A seeded row is deliberately INVALID prose-wise
	// (the summary is the TODO marker) and validateCatalog refuses it, so `--write`
	// can never turn a red gate green on its own.
	added := 0
	for _, v := range documented {
		if _, ok := catalog[v.Name]; ok {
			continue
		}
		catalog[v.Name] = catalogRow{Name: v.Name, Summary: catalogTODO}
		added++
	}
	removed := 0
	inRoster := map[string]bool{}
	for _, v := range documented {
		inRoster[v.Name] = true
	}
	for _, name := range sortedKeys(catalog) {
		if !inRoster[name] {
			delete(catalog, name)
			removed++
		}
	}
	if err := writeCatalog(filepath.Join(root, catalogPath), catalog); err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", err)
		return exitCannotLook
	}

	pageFile := filepath.Join(root, pagePath)
	current, _, err := readRegion(pageFile)
	if err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", err)
		return exitCannotLook
	}
	updated, err := replaceRegion(current, regionBody(documented, catalog))
	if err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — %v\n", err)
		return exitCannotLook
	}
	if err := os.WriteFile(pageFile, []byte(updated), 0o644); err != nil {
		fmt.Fprintf(errOut, "config-env-docs: CANNOT LOOK — write %s: %v\n", pagePath, err)
		return exitCannotLook
	}

	vars, fams := splitCounts(documented)
	fmt.Fprintf(out, "config-env-docs: wrote %d variables and %d families to %s (%d catalog rows seeded, %d stale removed)\n",
		vars, fams, pagePath, added, removed)
	if bad := validateCatalog(catalog); len(bad) > 0 {
		fmt.Fprintf(errOut, "config-env-docs: %d catalog row(s) still need prose — the gate stays red until they are written\n", len(bad))
		return exitDrift
	}
	return exitClean
}

func splitCounts(documented []*variable) (vars, fams int) {
	for _, v := range documented {
		if v.Family {
			fams++
			continue
		}
		vars++
	}
	return vars, fams
}

// reportUnregistered names, on success, the variables the CLI registry does not
// recognize — `olivares config validate --strict` would reject a deployment that sets
// them. It is a DIAGNOSTIC, not a verdict: the registry's scope is a CLI question
// (C08), and a documentation gate that failed on it would be blocking a lane it does
// not own. Printed rather than swallowed, because a silent finding is a lost one.
func reportUnregistered(inv *inventory, out io.Writer) {
	var names []string
	for _, n := range inv.names() {
		v := inv.byName[n]
		if v.TestOnly || v.Family || v.Registered {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(out, "config-env-docs: NOTE — %d documented variable(s) are outside %s, so `config validate --strict` rejects them:\n",
		len(names), registryPath)
	for _, n := range names {
		fmt.Fprintf(out, "  %s (%s)\n", n, strings.Join(inv.byName[n].componentList(), ", "))
	}
}

// reportExclusions prints, for every named exclusion, the keys it removed from the
// roster — keys that appear NOWHERE else in the tree. It runs on RED and on green: an
// exclusion whose effect nobody can see is a blind spot with a comment on it.
func reportExclusions(inv *inventory, out io.Writer) {
	reasons := make([]string, 0, len(inv.excluded))
	for reason := range inv.excluded {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		var removed []string
		for key := range inv.excluded[reason] {
			if _, stillThere := inv.byName[key]; !stillThere {
				removed = append(removed, key)
			}
		}
		if len(removed) == 0 {
			continue
		}
		sort.Strings(removed)
		fmt.Fprintf(out, "config-env-docs: EXCLUDED %d key(s) — %s:\n", len(removed), reason)
		fmt.Fprintf(out, "  %s\n", strings.Join(removed, " "))
	}
}

// ── enumeration ────────────────────────────────────────────────────────────────────

func enumerate(root string, reg *registry) (*inventory, error) {
	inv := &inventory{byName: map[string]*variable{}, excluded: map[string]map[string]bool{}}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q: %w", root, err)
	}
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is "I could not look at part of the
			// tree", which must not be silently counted as "nothing there".
			return fmt.Errorf("walk %s: %w", path, err)
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return fmt.Errorf("relativize %s: %w", path, relErr)
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			// A source this program cannot parse is a source it cannot enumerate.
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		// An excluded source is still PARSED — its keys go to the exclusion bucket so
		// the report can name what the exclusion removed. Skipping the directory
		// outright would make the exclusion's effect unobservable.
		exclusion := exclusionFor(rel)
		if exclusion == "" {
			inv.filesScanned++
		}
		component := componentOf(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !envKeyRe.MatchString(key) {
				return true
			}
			if exclusion != "" {
				if inv.excluded[exclusion] == nil {
					inv.excluded[exclusion] = map[string]bool{}
				}
				inv.excluded[exclusion][key] = true
				return true
			}
			v := inv.byName[key]
			if v == nil {
				v = &variable{
					Name:       key,
					Components: map[string]bool{},
					TestOnly:   reg.isTestOnly(key),
					Family:     reg.isFamily(key),
					Registered: reg.registered(key),
				}
				inv.byName[key] = v
			}
			v.Components[component] = true
			return true
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return inv, nil
}

// exclusionFor returns the reason this source is not a declaration site, or "".
func exclusionFor(rel string) string {
	if reason, ok := skipRelFiles[rel]; ok {
		return reason
	}
	for dir, reason := range skipRelDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return reason
		}
	}
	return ""
}

// componentOf collapses a source path to the component that reads the variable: at
// most the first two path segments. Deliberately NOT file:line — a line number moves
// on every unrelated edit above it, which would make the published page churn and
// train everyone to regenerate without reading.
func componentOf(rel string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}

// ── the registry, parsed from the code that owns it ────────────────────────────────

func parseRegistry(path string) (*registry, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", registryPath, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", registryPath, err)
	}
	want := map[string]*[]string{}
	reg := &registry{}
	want["exactConfigEnvKeys"] = &reg.exact
	want["prefixConfigEnvKeys"] = &reg.prefixes
	want["testOnlyConfigEnvKeys"] = &reg.testOnlyKeys
	want["testOnlyConfigEnvPrefixes"] = &reg.testOnlyPrefixe

	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, ident := range spec.Names {
			target, wanted := want[ident.Name]
			if !wanted || i >= len(spec.Values) {
				continue
			}
			composite, ok := spec.Values[i].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range composite.Elts {
				lit, ok := elt.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, uerr := strconv.Unquote(lit.Value); uerr == nil {
					*target = append(*target, s)
				}
			}
		}
		return true
	})

	// Every one of the four must be present and non-empty. A registry that parsed but
	// yielded an empty test-only list would silently promote fixtures into the public
	// page; a registry with no prefixes would publish every family member twice.
	for _, missing := range []struct {
		name string
		got  []string
	}{
		{"exactConfigEnvKeys", reg.exact},
		{"prefixConfigEnvKeys", reg.prefixes},
		{"testOnlyConfigEnvKeys", reg.testOnlyKeys},
		{"testOnlyConfigEnvPrefixes", reg.testOnlyPrefixe},
	} {
		if len(missing.got) == 0 {
			return nil, fmt.Errorf("%s declares no %s — the classification it owns cannot be read", registryPath, missing.name)
		}
	}
	sort.Strings(reg.exact)
	sort.Strings(reg.prefixes)
	sort.Strings(reg.testOnlyKeys)
	sort.Strings(reg.testOnlyPrefixe)
	return reg, nil
}

// ── the catalog: the ONE hand-written field per variable ───────────────────────────

// catalogTODO is what `--write` seeds and what validateCatalog refuses. A seeded row
// is a red gate with a name on it, never a quiet pass.
const catalogTODO = "TODO: describe what this configures"

var forbiddenSummaryWords = []string{
	// Canon §1.2: the absolute vocabulary is forbidden on public surfaces.
	"impossible", "infallible", "tamper-proof", "tamperproof", "unbreakable", "guaranteed",
	// Canon §1.2: the counts are exact, so a hedge is a defect, not a softener.
	"more than", "about ", "roughly", "approximately", "around ",
}

func readCatalog(path string) (map[string]catalogRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w — the prose catalog is the only hand-written input; without it nothing can be rendered", catalogPath, err)
	}
	rows := map[string]catalogRow{}
	for i, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("%s:%d has %d tab-separated fields, want 4 (name, required, default, summary)", catalogPath, i+1, len(fields))
		}
		name := fields[0]
		if _, dup := rows[name]; dup {
			return nil, fmt.Errorf("%s:%d declares %s twice", catalogPath, i+1, name)
		}
		rows[name] = catalogRow{
			Name:     name,
			Required: fields[1] == "yes",
			Default:  fields[2],
			Summary:  fields[3],
		}
		if fields[1] != "yes" && fields[1] != "no" {
			return nil, fmt.Errorf("%s:%d: required is %q, want yes or no", catalogPath, i+1, fields[1])
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s has no rows — an empty catalog would render an empty page and call it in sync", catalogPath)
	}
	return rows, nil
}

func validateCatalog(catalog map[string]catalogRow) []string {
	var findings []string
	for _, name := range sortedKeys(catalog) {
		row := catalog[name]
		switch {
		case strings.TrimSpace(row.Summary) == "":
			findings = append(findings, fmt.Sprintf("%s has an empty summary in %s", name, catalogPath))
		case strings.HasPrefix(row.Summary, "TODO"):
			findings = append(findings, fmt.Sprintf("%s still carries the seeded %q summary in %s — write the sentence", name, catalogTODO, catalogPath))
		}
		lower := strings.ToLower(row.Summary)
		for _, w := range forbiddenSummaryWords {
			if strings.Contains(lower, w) {
				findings = append(findings, fmt.Sprintf("%s's summary uses the forbidden word %q (canon §1.2)", name, strings.TrimSpace(w)))
			}
		}
		if strings.Contains(row.Summary, "|") {
			findings = append(findings, fmt.Sprintf("%s's summary contains a pipe, which would break the rendered table row", name))
		}
	}
	return findings
}

func writeCatalog(path string, catalog map[string]catalogRow) error {
	var b bytes.Buffer
	b.WriteString("# SPDX-FileCopyrightText: 2026 Olivares.AI\n")
	b.WriteString("# SPDX-License-Identifier: AGPL-3.0-only\n")
	b.WriteString("#\n")
	b.WriteString("# config-env-catalog.tsv — the ONE hand-written field per OLIVARES_* variable.\n")
	b.WriteString("#\n")
	b.WriteString("# THE ROSTER IS NOT HERE. It is enumerated from the non-test Go sources on every\n")
	b.WriteString("# run by scripts/config-env-docs, so this file cannot decide which variables exist:\n")
	b.WriteString("# a name in the code with no row here FAILS the gate, and a row here for a name the\n")
	b.WriteString("# code no longer reads FAILS it too. Rows are rewritten sorted by\n")
	b.WriteString("# `bash scripts/check-config-env-docs.sh --write`; edit the prose, not the roster.\n")
	b.WriteString("#\n")
	b.WriteString("# Columns, tab-separated: name\trequired(yes|no)\tdefault(empty for none)\tsummary\n")
	for _, name := range sortedKeys(catalog) {
		row := catalog[name]
		required := "no"
		if row.Required {
			required = "yes"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", name, required, row.Default, row.Summary)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func sortedKeys(m map[string]catalogRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── rendering ──────────────────────────────────────────────────────────────────────

// regionBody is the ONE definition of what sits between the markers, whitespace
// included. Rendering and checking both go through it: when they did not, the writer
// added a blank line the checker did not expect and every run reported drift.
func regionBody(documented []*variable, catalog map[string]catalogRow) string {
	return "\n\n" + renderRegion(documented, catalog) + "\n"
}

func renderRegion(documented []*variable, catalog map[string]catalogRow) string {
	var vars, fams []*variable
	for _, v := range documented {
		if v.Family {
			fams = append(fams, v)
			continue
		}
		vars = append(vars, v)
	}

	var b bytes.Buffer
	b.WriteString("### Complete variable reference\n\n")
	fmt.Fprintf(&b, "The table below is generated from the product's own sources: %d variables and %d "+
		"runtime-constructed families, covering the engine, the CLI, the Kubernetes operator, the "+
		"Terraform provider and the connectors. It is regenerated and checked against those sources "+
		"on every change, so it does not fall behind the binary.\n\n", len(vars), len(fams))
	b.WriteString("**Required** means the feature that reads the variable does not start without it; " +
		"most variables are optional and the engine runs with none of them set.\n\n")
	b.WriteString("| Variable | Required | Default | What it configures |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, v := range vars {
		row := catalog[v.Name]
		b.WriteString(renderRow(v, row))
	}

	if len(fams) > 0 {
		b.WriteString("\n### Variable families\n\n")
		b.WriteString("These prefixes name families whose member variables are built at runtime — " +
			"the per-provider and per-backend keys the engine composes from a provider name. " +
			"The concrete members it composes are in the table above.\n\n")
		b.WriteString("| Prefix | Required | Default | What it configures |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		for _, v := range fams {
			row := catalog[v.Name]
			b.WriteString(renderRow(v, row))
		}
	}
	return b.String()
}

func renderRow(v *variable, row catalogRow) string {
	required := "No"
	if row.Required {
		required = "Yes"
	}
	def := "—"
	if row.Default != "" {
		def = "`" + row.Default + "`"
	}
	// Deliberately NO "read by" column. It was there and it was removed: the component
	// is internal package layout, it is the same value for most rows, and it made the
	// published page churn — and this gate go red — every time a read moved between
	// packages in a refactor that changed no knob. `--list` still prints it for
	// maintainers, where it belongs.
	return fmt.Sprintf("| `%s` | %s | %s | %s |\n", v.Name, required, def, row.Summary)
}

// ── the generated region inside the page ───────────────────────────────────────────

func readRegion(path string) (whole, region string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", pagePath, err)
	}
	whole = string(raw)
	start := strings.Index(whole, beginMarker)
	end := strings.Index(whole, endMarker)
	if start < 0 || end < 0 {
		return whole, "", fmt.Errorf("%s has no generated region — the BEGIN/END markers are gone, so there is nothing to compare against and a page missing its roster would read as clean", pagePath)
	}
	if end < start {
		return whole, "", fmt.Errorf("%s has its generated markers in the wrong order", pagePath)
	}
	if strings.Count(whole, beginMarker) != 1 || strings.Count(whole, endMarker) != 1 {
		return whole, "", fmt.Errorf("%s has more than one generated region", pagePath)
	}
	return whole, whole[start+len(beginMarker) : end], nil
}

func replaceRegion(whole, region string) (string, error) {
	start := strings.Index(whole, beginMarker)
	end := strings.Index(whole, endMarker)
	if start < 0 || end < 0 || end < start {
		return "", errors.New("cannot replace a generated region that is not there")
	}
	return whole[:start+len(beginMarker)] + region + whole[end:], nil
}

// firstDiff names the first line that differs, so a red gate points at a name instead
// of at a file.
func firstDiff(got, want string) string {
	g := strings.Split(got, "\n")
	w := strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("\n      first difference at generated line %d:\n        published: %s\n        code says: %s", i+1, trim(gl), trim(wl))
		}
	}
	return ""
}

func trim(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	if s == "" {
		return "(end of region)"
	}
	return s
}
