// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/modulespec"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
)

// optionalReads are derived dependencies that are deliberately not activation
// edges (docs/module-spec.md): the consumer reads the owner's retained tables and
// tolerates an absent or dormant owner. Each entry must still be derived.
var optionalReads = map[[2]string]string{
	{"catalog", "models"}:       "model approval reads the retained admission policy and verdicts, denying closed without them",
	{"compliance", "finops"}:    "capability probes report an absent kind as a gap; erasure covers retained data",
	{"compliance", "knowledge"}: "capability probes report an absent kind as a gap; erasure covers retained data",
	{"compliance", "models"}:    "capability probes report an absent kind as a gap",
	{"compliance", "recording"}: "capability probes report an absent kind as a gap",
	{"compliance", "voice"}:     "erasure covers retained data",
	{"liveingest", "voice"}:     "the voice probe has no production caller; voice is its own telemetry producer",
	{"security", "knowledge"}:   "the forensic timeline omits lineage when knowledge is absent",
}

// TestModuleSpecRequiresDerivedDependencies derives module dependencies from
// production imports of another module package and from literals naming another
// module's registered kinds (sc.Ext reads its table). Each owner must be active
// whenever its consumer is: same package, the kernel, a transitive requires edge,
// or a documented optional read.
//
// ponytail: whole-literal kinds only (no concatenated kinds or raw SQL; none on
// main), and an import of a multi-namespace package needs any one of them.
func TestModuleSpecRequiresDerivedDependencies(t *testing.T) {
	derived := derivedDependencies(t)
	specs := modulespec.All()
	for _, missing := range undeclaredDependencies(specs, derived) {
		t.Errorf("undeclared module dependency %s: add the requires edge to core/modulespec/modules.json", missing)
	}
	for pair := range optionalReads {
		if _, ok := derived[pair[0]][pair[1]]; !ok {
			t.Errorf("optional read %s -> %s is no longer derived: remove it", pair[0], pair[1])
		}
	}

	// Planted missing edges must be reported, so neither derivation is vacuous.
	// Each pair is a direct edge with no other path through the closure.
	for _, planted := range [][2]string{
		{"compliance", "accessmap"}, // import
		{"posture", "inventory"},    // kind read
	} {
		cut := modulespec.All()
		for i := range cut {
			if cut[i].Namespace == planted[0] {
				cut[i].Requires = slices.DeleteFunc(cut[i].Requires, func(ns string) bool { return ns == planted[1] })
			}
		}
		edge := planted[0] + " -> " + planted[1] + " "
		missing := undeclaredDependencies(cut, derived)
		if !slices.ContainsFunc(missing, func(m string) bool { return strings.HasPrefix(m, edge) }) {
			t.Errorf("planted missing edge %s not reported: %v", edge, missing)
		}
	}
}

// derivedDependencies maps a consumer namespace to the owner namespaces it
// depends on, each with the first source position that shows it. Code in a
// package serving several namespaces counts for each of them.
func derivedDependencies(t *testing.T) map[string]map[string]string {
	t.Helper()
	modules, err := Build(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := kindOwners{owner: map[string]string{}}
	for _, m := range modules {
		if sp, ok := m.(runtime.SchemaProvider); ok {
			kinds.namespace = m.APINamespace()
			if err := sp.RegisterSchema(&kinds); err != nil {
				t.Fatalf("%s: %v", kinds.namespace, err)
			}
		}
	}
	if len(kinds.owner) == 0 {
		t.Fatal("no module-owned kinds registered")
	}
	byPackage := map[string][]string{}
	for _, spec := range modulespec.All() {
		byPackage[spec.Package] = append(byPackage[spec.Package], spec.Namespace)
	}

	derived := map[string]map[string]string{}
	for pkg, consumers := range byPackage {
		add := func(owner string, pos token.Position) {
			if slices.Contains(consumers, owner) {
				return
			}
			for _, consumer := range consumers {
				if derived[consumer] == nil {
					derived[consumer] = map[string]string{}
				}
				if _, seen := derived[consumer][owner]; !seen {
					derived[consumer][owner] = pos.String()
				}
			}
		}
		fset := token.NewFileSet()
		err := filepath.WalkDir(filepath.Join("..", pkg), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return fs.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if rest, ok := strings.CutPrefix(p, "github.com/olivaresai/olivares/modules/"); ok {
					if owners := byPackage[strings.Split(rest, "/")[0]]; len(owners) > 0 {
						add(owners[0], fset.Position(imp.Pos()))
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					if owner, ok := kinds.owner[s]; ok {
						add(owner, fset.Position(lit.Pos()))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return derived
}

// undeclaredDependencies lists "consumer -> owner (position)" for each derived
// dependency whose owner package has no namespace active with the consumer: the
// requires closure of the consumer and the kernel.
func undeclaredDependencies(specs []modulespec.Spec, derived map[string]map[string]string) []string {
	rows := map[string]modulespec.Spec{}
	var kernel []string
	for _, spec := range specs {
		rows[spec.Namespace] = spec
		if spec.Kind == "kernel" {
			kernel = append(kernel, spec.Namespace)
		}
	}
	var missing []string
	for consumer, owners := range derived {
		active := map[string]bool{}
		var visit func(string)
		visit = func(ns string) {
			if !active[ns] {
				active[ns] = true
				for _, required := range rows[ns].Requires {
					visit(required)
				}
			}
		}
		for _, ns := range append([]string{consumer}, kernel...) {
			visit(ns)
		}
		for owner, pos := range owners {
			satisfied := slices.ContainsFunc(specs, func(s modulespec.Spec) bool {
				return active[s.Namespace] && s.Package == rows[owner].Package
			})
			if _, optional := optionalReads[[2]string{consumer, owner}]; !satisfied && !optional {
				missing = append(missing, consumer+" -> "+owner+" ("+pos+")")
			}
		}
	}
	slices.Sort(missing)
	return missing
}

// kindOwners records which namespace registers each module-owned kind.
type kindOwners struct {
	namespace string
	owner     map[string]string
}

func (k *kindOwners) Register(d model.EntityDescriptor) error {
	k.owner[string(d.Kind)] = k.namespace
	return nil
}

func (*kindOwners) Migrations(string, fs.FS) error { return nil }

func (*kindOwners) SchemaInvariants(string, map[store.Engine][]store.SchemaTrigger) error {
	return nil
}

func (*kindOwners) WorkspaceInitializer(store.WorkspaceInitializer) error { return nil }

func (*kindOwners) RolloutControl(store.RolloutControl) error { return nil }
