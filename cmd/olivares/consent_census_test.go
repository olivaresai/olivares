// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The census is computed from the composition's own registry: every descriptor
// the store opened with, every kind table the writers accept. None of these
// cases counts columns against a number; each walks what is registered.

// censusFixtureParticipant has the shape of a work participant: a kind and a
// ref that names an account when the kind is "user".
type censusFixtureParticipant struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// censusFixtureEscalate is a step config that holds a participant under a field
// name no declared step config uses.
type censusFixtureEscalate struct {
	Assignee censusFixtureParticipant `json:"assignee"`
	Reason   string                   `json:"reason"`
}

// censusFixtureRaw is a variant with an opaque leaf nothing maps.
type censusFixtureRaw struct {
	Extra json.RawMessage `json:"extra"`
}

// censusFixtureDoc is a typed document with one leaf left unclassified.
type censusFixtureDoc struct {
	Owner censusFixtureParticipant `json:"owner"`
	Note  string                   `json:"note"`
}

// censusFixtureKinds is a writer's kind table, for the fixtures.
type censusFixtureKinds map[string]*model.ColumnDecl

func (k censusFixtureKinds) Kinds() []string {
	out := make([]string, 0, len(k))
	for name := range k {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (k censusFixtureKinds) Variant(kind string) (*model.ColumnDecl, bool) {
	v, ok := k[kind]
	return v, ok
}

var censusFixtureParticipantLeaves = model.TypeLeaves(censusFixtureParticipant{},
	model.Leaf("kind", model.None("a closed participant kind: workflow_graph.go:283")),
	model.Leaf("ref", model.KindRef("kind", "")),
)

// censusFixtureDescriptor is a one-column descriptor declared as decl.
func censusFixtureDescriptor(kind model.Kind, sqlKind model.SQLKind, decl *model.ColumnDecl) model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind: kind, Table: strings.ReplaceAll(string(kind), ".", "_"),
		Fields: []model.FieldSpec{
			{Name: "kind", Kind: model.KindText, Principal: model.None("the fixture's discriminator: consent_census_test.go:1")},
			{Name: "doc", Kind: sqlKind, Principal: decl},
		},
	}
}

// TestEveryColumnIsDeclaredAndEveryLeafClassified is the completeness check of
// the census over the composition's registry, with the fixtures that must fail
// it.
func TestEveryColumnIsDeclaredAndEveryLeafClassified(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		if e.eng.census == nil {
			t.Fatal("the composed store exposes no census")
		}
		descriptors := e.eng.census.CensusDescriptors()
		if len(descriptors) == 0 {
			t.Fatal("the composed store's registry is empty")
		}
		for _, d := range descriptors {
			for _, defect := range d.PrincipalDefects() {
				t.Error(defect)
			}
		}

		// The policy kinds the writers accept are declared, each with a class.
		policies := censusFixtureDescriptor("fixture.policies", model.KindJSON, model.Union("kind", model.PolicyKinds))
		for _, defect := range policies.PrincipalDefects() {
			t.Errorf("policy-kind registry: %v", defect)
		}
		if _, ok := model.PolicyKinds.Variant("guardrail"); ok {
			t.Errorf("the policy-kind registry accepts an unregistered kind")
		}

		// The revision content is a union over the revision writer's surfaces, and
		// an unlisted surface has no variant.
		var content *model.ColumnDecl
		for _, d := range descriptors {
			if d.Kind == "governance.policy_revision" {
				for _, f := range d.Fields {
					if f.Name == "content" {
						content = f.Principal
					}
				}
			}
		}
		if content == nil || content.Form != model.FormUnion || content.Kinds == nil {
			t.Errorf("the policy revision content is not declared as a union over the surface registry")
		} else if _, ok := content.Kinds.Variant("future-surface"); ok {
			t.Errorf("the surface registry has a variant for an unlisted surface")
		}
	})

	fixtures := []struct {
		name string
		desc model.EntityDescriptor
	}{
		{"an undeclared JSON column", censusFixtureDescriptor("fixture.undeclared", model.KindJSON, nil)},
		{"a new unclassified leaf", censusFixtureDescriptor("fixture.leaf", model.KindJSON,
			model.Nested(censusFixtureDoc{}, model.ClassObligation, censusFixtureParticipantLeaves))},
		{"a new step kind holding a participant under a new field name, with no class",
			censusFixtureDescriptor("fixture.steps", model.KindJSON, model.Union("kind", censusFixtureKinds{
				"work-escalate": model.Nested(censusFixtureEscalate{}, "", censusFixtureParticipantLeaves,
					model.Leaf("reason", model.None("rendered text: consent_census_test.go:1"))),
			}))},
		{"a variant with an unmapped raw leaf", censusFixtureDescriptor("fixture.raw", model.KindJSON,
			model.Union("kind", censusFixtureKinds{"raw": model.Nested(censusFixtureRaw{}, model.ClassObligation)}))},
		{"a None that cites no lines", censusFixtureDescriptor("fixture.none", model.KindText, model.None("no user"))},
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			if defects := fx.desc.PrincipalDefects(); len(defects) == 0 {
				t.Errorf("the census accepts %s", fx.name)
			}
		})
	}
}

// censusAnchors are counted rows the design names; each must be declared
// counted. They anchor the walk; the walk itself covers every declaration.
var censusAnchors = []string{
	"governance.scoped_grant.subject_ref",
	"governance.policy_revision.content",
	"governance.nhi_lifecycle.sponsor_ref",
	"sourcescope.binding.scope_ref",
	"models.model_access.subject_ref",
	"orchestration.workflow.steps",
	"orchestration.workflow_run.steps",
	"orchestration.schedule.owner_user_ref",
	"eventing.subscription.owner_actor",
	"core.policy.spec",
}

// TestEveryCountedColumnHasAReader: every counted declaration is covered by
// exactly one module the composition declares, and every declared module has a
// step.
func TestEveryCountedColumnHasAReader(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		if e.eng.census == nil {
			t.Fatal("the composed store exposes no census")
		}
		coverage := map[string][]string{}
		for _, m := range e.eng.declared {
			if m.step == nil {
				t.Errorf("declared module %s has no retirement step", m.name)
			}
			for _, c := range m.covers {
				coverage[c] = append(coverage[c], m.name)
			}
		}
		counted := map[string]bool{}
		for _, d := range e.eng.census.CensusDescriptors() {
			for _, col := range d.CountedColumns() {
				key := string(d.Kind) + "." + col
				counted[key] = true
				if len(coverage[key]) != 1 {
					t.Errorf("counted column %s is covered by %v, want exactly one declared module", key, coverage[key])
				}
			}
		}
		for key, modules := range coverage {
			if !counted[key] {
				t.Errorf("declared module %v covers %s, which is not a counted column", modules, key)
			}
		}
		for _, key := range censusAnchors {
			if !counted[key] {
				t.Errorf("%s is not declared counted", key)
			}
		}
	})
}

// TestEveryCompositionIsReadyForAnAbsenceProof: the Community composition opens
// ready, the verdict is computed once at open, and a composition whose writer
// accepts a variant with an unmapped raw leaf opens unready with that cause.
func TestEveryCompositionIsReadyForAnAbsenceProof(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		if e.eng.census == nil {
			t.Fatal("the composed store exposes no census")
		}
		r := e.eng.census.CompositionReadiness()
		if !r.Ready() {
			t.Errorf("the Community composition opened unready: %s", r.Cause())
		}
		if again := e.eng.census.CompositionReadiness(); again.Ready() != r.Ready() || again.Cause() != r.Cause() {
			t.Errorf("the readiness verdict changed after open")
		}
	})

	t.Run("a fixture composition with an unmapped raw leaf", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		fixture := model.EntityDescriptor{
			Kind: "censusfx.step", Table: "censusfx_step",
			Fields: []model.FieldSpec{
				{Name: "kind", Kind: model.KindText, Principal: model.None("the fixture's discriminator: consent_census_test.go:1")},
				{Name: "config", Kind: model.KindJSON, Principal: model.Union("kind", censusFixtureKinds{
					"raw": model.Nested(censusFixtureRaw{}, model.ClassObligation),
				})},
			},
		}
		st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
			func(reg store.ExtensionRegistry) error { return reg.Register(fixture) })
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		census, ok := st.(store.CompositionCensus)
		if !ok {
			t.Fatal("the store exposes no census")
		}
		r := census.CompositionReadiness()
		if r.Ready() {
			t.Fatal("a composition with an unmapped raw leaf opened ready")
		}
		found := false
		for _, c := range r.Causes {
			if strings.Contains(c, "censusfx.step") {
				found = true
			}
		}
		if !found {
			t.Errorf("the unready verdict does not name the fixture: %v", r.Causes)
		}
	})
}

// openCensus opens a core composition on SQLite with register, and returns its
// store.
func openCensus(t *testing.T, register func(store.ExtensionRegistry) error) store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, register)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// unreadCounted returns the counted columns of st's registry that no declared
// module reads.
func unreadCounted(t *testing.T, st store.Store) []string {
	t.Helper()
	census, ok := st.(store.CompositionCensus)
	readers, ok2 := st.(store.CompositionReaders)
	if !ok || !ok2 {
		t.Fatal("the store exposes no census")
	}
	read := map[string]bool{}
	for _, cols := range readers.CompositionReaders() {
		for _, c := range cols {
			read[c] = true
		}
	}
	var out []string
	for _, d := range census.CensusDescriptors() {
		for _, col := range d.CountedColumns() {
			if key := string(d.Kind) + "." + col; !read[key] {
				out = append(out, key)
			}
		}
	}
	return out
}

// censusOutsideStore is a table an edition keeps outside the registry, whose
// owner column is declared as owner.
func censusOutsideStore(owner *model.ColumnDecl) model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind: "synthetic.outside", Table: "synthetic_outside",
		Fields: []model.FieldSpec{
			{Name: "owner_ref", Kind: model.KindText, Principal: owner},
			{Name: "note", Kind: model.KindText, Principal: model.None("rendered prose: consent_census_test.go:1")},
		},
	}
}

// TestAnEditionsContributionIsExpectedWhetherOrNotItIsLinked: a composition
// opens ready only when every module an edition declares registered its reader
// and every table the edition keeps outside the registry is declared and read.
// An omitted module, an undeclared outside column or an unread outside reference
// each leave it unready, naming the cause, and a declared module the build does
// not link stays declared, with no step.
func TestAnEditionsContributionIsExpectedWhetherOrNotItIsLinked(t *testing.T) {
	leftover := unreadCounted(t, openCensus(t, nil))
	readiness := func(t *testing.T, c store.CompositionContribution, reads []string) store.Readiness {
		t.Helper()
		st := openCensus(t, func(reg store.ExtensionRegistry) error {
			expected, ok := reg.(store.CompositionContributionRegistry)
			readers, ok2 := reg.(store.CompositionReaderRegistry)
			if !ok || !ok2 {
				t.Fatal("the registry takes no contribution")
			}
			if err := expected.DeclareCompositionContribution(c); err != nil {
				return err
			}
			if len(reads) == 0 {
				return nil
			}
			return readers.DeclareCompositionReader("synthetic", reads)
		})
		return st.(store.CompositionCensus).CompositionReadiness()
	}
	contribution := func(owner *model.ColumnDecl) store.CompositionContribution {
		return store.CompositionContribution{
			Edition: "synthetic", Modules: []string{"synthetic"},
			OutsideStores: []model.EntityDescriptor{censusOutsideStore(owner)},
		}
	}
	counted := model.Ref(model.EncodeUserID, model.ClassAuthority)
	allReads := append(append([]string(nil), leftover...), "synthetic.outside.owner_ref")
	cause := func(r store.Readiness, fragment string) bool {
		for _, c := range r.Causes {
			if strings.Contains(c, fragment) {
				return true
			}
		}
		return false
	}

	if r := readiness(t, contribution(counted), allReads); !r.Ready() {
		t.Fatalf("a complete synthetic contribution opened unready: %v", r.Causes)
	}
	t.Run("the edition's module registered no reader", func(t *testing.T) {
		r := readiness(t, contribution(counted), nil)
		if r.Ready() || !cause(r, "synthetic: a module the synthetic edition declares registered no retirement reader") {
			t.Errorf("an omitted module = %v, want unready naming it", r.Causes)
		}
	})
	t.Run("an outside column with no declaration", func(t *testing.T) {
		r := readiness(t, contribution(nil), leftover)
		if r.Ready() || !cause(r, "synthetic.outside") {
			t.Errorf("an undeclared outside column = %v, want unready naming its table", r.Causes)
		}
	})
	t.Run("an outside reference no module reads", func(t *testing.T) {
		r := readiness(t, contribution(counted), leftover)
		if r.Ready() || !cause(r, "synthetic.outside.owner_ref") {
			t.Errorf("an unread outside reference = %v, want unready naming it", r.Causes)
		}
	})
	t.Run("a declared module the build does not link", func(t *testing.T) {
		declared := declaredModules(moduleSet{}, []store.CompositionContribution{contribution(counted)})
		if len(declared) != 1 || declared[0].name != "synthetic" || declared[0].step != nil {
			t.Fatalf("declared = %+v, want the synthetic module with no step", declared)
		}
		if steps := retirementSteps(declared); len(steps) != 0 {
			t.Errorf("a module with no step contributed %d steps", len(steps))
		}
	})
}

// TestARetirementWithoutADeclaredModulesStepIsIncomplete: a retirement pass
// whose step list lacks the step of a module the composition declares records
// the pass as incomplete, naming the module, and never retires the account.
func TestARetirementWithoutADeclaredModulesStepIsIncomplete(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "missing-step@consent.test", "viewer")
		e.scimDelete(e.tT, user)
		var partial []declaredModule
		for _, d := range e.pump().modules {
			if d.name != "eventing" {
				partial = append(partial, d)
			}
		}
		worker := &retirementPump{authr: e.pump().authr, modules: partial}
		advancePumpClock(worker)
		if _, err := worker.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		rec, found := e.record(user, e.tT)
		if !found || rec.RetirementState != model.RetirementRetiring || !strings.Contains(rec.ModuleResults, "census_incomplete:eventing") {
			t.Errorf("the record after a pass without a declared step = %+v, want retiring with census_incomplete:eventing", rec)
		}
	})
}

// censusCitation is one file:line or file:first-last citation in a None reason.
var censusCitation = regexp.MustCompile(`([A-Za-z0-9_./-]+\.(?:go|sql|ts|tsx|json)):([0-9]+)(?:-([0-9]+))?`)

// noneReasons returns the reasons of every None declaration reachable from d:
// the column's own, and those of every leaf of a Nested value and every variant
// of a Union.
func noneReasons(d *model.ColumnDecl, seen map[*model.ColumnDecl]bool) []string {
	if d == nil || seen[d] {
		return nil
	}
	seen[d] = true
	var out []string
	switch d.Form {
	case model.FormNone:
		out = append(out, d.Reason)
	case model.FormNested:
		var walk func([]model.LeafDecl)
		walk = func(leaves []model.LeafDecl) {
			for _, l := range leaves {
				out = append(out, noneReasons(l.Decl, seen)...)
				walk(l.Leaves)
			}
		}
		walk(d.Leaves)
	case model.FormUnion:
		if d.Kinds != nil {
			for _, k := range d.Kinds.Kinds() {
				v, _ := d.Kinds.Variant(k)
				out = append(out, noneReasons(v, seen)...)
			}
		}
	}
	return out
}

// TestEveryNoneCitesALineThatExists: every file:line a None declaration of the
// composition cites names a file of this repository that has that line. It
// checks that the reader a reason points at is there; whether the line shows
// what the reason says is the declaration audit's to judge.
func TestEveryNoneCitesALineThatExists(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	lineCounts := map[string][]int{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "build", ".astro":
				return filepath.SkipDir
			}
			return nil
		}
		if !censusCitation.MatchString(d.Name() + ":1") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		n := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			n++
		}
		rel, _ := filepath.Rel(root, path)
		lineCounts[filepath.ToSlash(rel)] = append(lineCounts[filepath.ToSlash(rel)], n)
		return sc.Err()
	}); err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	resolves := func(cited string, last int) bool {
		for rel, counts := range lineCounts {
			if rel != cited && !strings.HasSuffix(rel, "/"+cited) {
				continue
			}
			for _, n := range counts {
				if n >= last {
					return true
				}
			}
		}
		return false
	}
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		if e.eng.census == nil {
			t.Fatal("the composed store exposes no census")
		}
		checked := 0
		for _, d := range e.eng.census.CensusDescriptors() {
			for _, f := range d.Fields {
				for _, reason := range noneReasons(f.Principal, map[*model.ColumnDecl]bool{}) {
					for _, m := range censusCitation.FindAllStringSubmatch(reason, -1) {
						last, _ := strconv.Atoi(m[2])
						if m[3] != "" {
							last, _ = strconv.Atoi(m[3])
						}
						checked++
						if !resolves(m[1], last) {
							t.Errorf("%s.%s cites %s:%d, which no file of this repository has", d.Kind, f.Name, m[1], last)
						}
					}
				}
			}
		}
		if checked == 0 {
			t.Fatal("no None citation was checked")
		}
	})
}
