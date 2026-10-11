// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
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
	"github.com/olivaresai/olivares/sdk"
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
		retirement := map[string]bool{}
		for _, d := range readerCensusDescriptors(t, e.eng.census) {
			for _, col := range d.CountedColumns() {
				counted[string(d.Kind)+"."+col] = true
			}
			for _, col := range append(d.CountedColumns(), d.ContentColumns()...) {
				key := string(d.Kind) + "." + col
				if retirement[key] {
					continue
				}
				retirement[key] = true
				if len(coverage[key]) != 1 {
					t.Errorf("retirement column %s is covered by %v, want exactly one declared module", key, coverage[key])
				}
			}
		}
		for key, modules := range coverage {
			if !retirement[key] {
				t.Errorf("declared module %v covers %s, which is neither counted nor declared content", modules, key)
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
			assertEditionAbsenceProofReadiness(t, e, r)
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

// readerCensusDescriptors includes the stores declared outside the registry in
// the same composition whose module readers are checked.
func readerCensusDescriptors(t *testing.T, census store.CompositionCensus) []model.EntityDescriptor {
	t.Helper()
	contributions, ok := census.(store.CompositionContributions)
	if !ok {
		t.Fatal("the composed store exposes no edition contributions")
	}
	descriptors := append([]model.EntityDescriptor(nil), census.CensusDescriptors()...)
	for _, contribution := range contributions.CompositionContributions() {
		descriptors = append(descriptors, contribution.OutsideStores...)
	}
	return descriptors
}

// unreadCounted returns the counted columns of st's composition that no declared
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
	for _, d := range readerCensusDescriptors(t, census) {
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

// The store's real contribution API carries outside authority columns even
// though CensusDescriptors inventories only the registered core tables.
func TestCountedReaderCensusIncludesEditionOutsideColumns(t *testing.T) {
	for _, read := range []bool{false, true} {
		name := "unread outside authority"
		if read {
			name = "covered outside authority"
		}
		t.Run(name, func(t *testing.T) {
			outside := censusOutsideStore(model.Ref(model.EncodeUserID, model.ClassAuthority))
			outside.Fields = append(outside.Fields, model.FieldSpec{Name: "opaque_payload", Kind: model.KindBytes})
			st := openCensus(t, func(reg store.ExtensionRegistry) error {
				if err := reg.(store.CompositionContributionRegistry).DeclareCompositionContribution(store.CompositionContribution{
					Edition: "synthetic", Modules: []string{"synthetic"}, OutsideStores: []model.EntityDescriptor{outside},
				}); err != nil {
					return err
				}
				if read {
					return reg.(store.CompositionReaderRegistry).DeclareCompositionReader("synthetic", []string{"synthetic.outside.owner_ref"})
				}
				return nil
			})
			unread := false
			for _, column := range unreadCounted(t, st) {
				unread = unread || column == "synthetic.outside.owner_ref"
			}
			if unread == read {
				t.Fatalf("outside authority unread=%v, reader registered=%v", unread, read)
			}
			readiness := st.(store.CompositionCensus).CompositionReadiness()
			for _, cause := range readiness.Causes {
				if strings.Contains(cause, "synthetic.outside.opaque_payload: undeclared") {
					return
				}
			}
			t.Fatalf("counting a known outside authority concealed unknown payloads: %v", readiness.Causes)
		})
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
// composition cites resolves locally, except exact immutable citations whose
// readers moved to Business. Those retain historical custody in
// Community; this tree cannot prove the private readers still exist. Whether
// a reader shows what its reason says is the declaration audit's to judge.
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
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		if e.eng.census == nil {
			t.Fatal("the composed store exposes no census")
		}
		checked := 0
		for _, d := range e.eng.census.CensusDescriptors() {
			for _, f := range d.Fields {
				for _, reason := range noneReasons(f.Principal, map[*model.ColumnDecl]bool{}) {
					for _, m := range censusCitation.FindAllStringSubmatch(reason, -1) {
						first, _ := strconv.Atoi(m[2])
						last := first
						if m[3] != "" {
							last, _ = strconv.Atoi(m[3])
						}
						cited := m[1]
						// This unchanged notify declaration predates its helpers moving up 25 lines.
						// Qualify the real reader; another module's longer helpers.go is no evidence.
						if strings.HasPrefix(string(d.Kind), "notify.") && reason == "a severity label, read only as a severity: helpers.go:286-298, helpers.go:302-305" {
							switch last {
							case 298:
								cited, last = "modules/notify/helpers.go", 273
							case 305:
								cited, last = "modules/notify/helpers.go", 281
							}
						}
						checked++
						if !localNoneCitationResolves(lineCounts, d.Kind, cited, first, last) &&
							!(!orchestrationReadersLinked() && historicalOrchestrationNoneReason(d.Kind, reason)) &&
							!historicalBusinessNoneCitation(thisEdition.name == "community", d.Kind, reason, m[0]) {
							t.Errorf("%s.%s cites %s:%d, which no file of this repository has", d.Kind, f.Name, cited, last)
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

// These exact immutable declarations accompanied the reader move to Business.
// Community keeps their historical provenance for export; it cannot verify a
// private reader's current source. Business still resolves the actual citations.
// A new or changed reason is never admitted by this historical inventory.
var historicalOrchestrationNoneReasons = map[string]bool{
	"a canonical timestamp, parsed by canonicalTimestamp: workflow_graph.go:417":                                                                                                                            true,
	"a closed participant kind: workflow_graph.go:358":                                                                                                                                                      true,
	"a closed status, output or attempt value set by the executor: workflow_run.go:65,754":                                                                                                                  true,
	"a closed subject kind, trigger kind or desired status the schedule validator checks: schedules.go:67-69, schedules.go:383":                                                                             true,
	"a closed value set checked by the step validator: workflow_graph.go:358-368,669,685,781":                                                                                                               true,
	"a content digest bounded and compared for integrity: workflow_graph.go:566,784":                                                                                                                        true,
	"a cron expression or event-type reference, hashed into the fire plan, checked against the cron allowlist and bound into the target: schedules.go:128-131, schedules.go:1090-1091, targetbind.go:94-95": true,
	"a field of the schedule as it stood; restore re-validates and re-applies only its status, subject, cadence, interval and grace: revisions.go:274-292, dto.go:229-251":                                  true,
	"a ref, digest, label or state of the module's own governed-effect claim and outbox, compared for replay only: operation.go:189-196, operation.go:207-210, operation.go:276-282, operation.go:297-303":  true,
	"a remote peer, skill or runtime profile, never an account: workflow_graph.go:781-784":                                                                                                                  true,
	"a runtime session id, bounded by validWorkText: workflow_graph.go:627":                                                                                                                                 true,
	"a schedule or workflow name, rendered, searched and hashed into a plan only: dto.go:236, search.go:40, workflow.go:46-55":                                                                              true,
	"a spent approval id, the schedule it activated or the plan hash it was bound to: routinepolicy.go:684-685":                                                                                             true,
	"a step kind, refused unless stepConfigs has it: workflow_graph.go:878":                                                                                                                                 true,
	"a step ref or slug, matched by stepRefPattern: workflow_graph.go:183":                                                                                                                                  true,
	"a workflow or work-item id, status, plan hash, approval id or pause reason of a run: workflow_run.go:834-836, workflow_run.go:1668-1670, workflow_run.go:1721":                                         true,
	"an id of a non-principal row, parsed by canonicalConfigID: workflow_graph.go:373":                                                                                                                      true,
	"an id, timestamp or ref of the run's own work, recorded by the executor: workflow_run.go:1508-1512,1556-1567":                                                                                          true,
	"an op, schedule id, plan hash, approval id, gate or op status, dispatch ref, detail digest or short result of the decision ledger, rendered only: schedules.go:158-172, dto.go:271-288":                true,
	"an opaque credential-binding handle, parsed as such: modules/orchestration/workflow_work_run.go:20":                                                                                                    true,
	"an opaque target-binding fingerprint and key id: workflow_run.go:782-783":                                                                                                                              true,
	"an operator-facing status line, clamped and rendered only: workflow_run.go:1558":                                                                                                                       true,
	"create, update or restore: revisions.go:42-44, revisions.go:328":                                                                                                                                       true,
	"operator prose, bounded and rendered only: workflow.go:51, workflow.go:323":                                                                                                                            true,
	"operator text, bounded by validWorkText and rendered only: workflow_graph.go:378":                                                                                                                      true,
	"the agent, swarm or workflow a schedule or decision governs; subject kinds are agent, swarm or workflow only: schedules.go:67, schedules.go:383, workflow_run.go:1680":                                 true,
	"the caller's actor kind, recorded beside its actor ref and rendered only: revisions.go:80, dto.go:284, workflow.go:321":                                                                                true,
	"the constant fence family key: routinepolicy.go:492-499":                                                                                                                                               true,
	"the declaring caller's confined core workspace id, a routine-policy scope key: routinepolicy.go:149-150, routinepolicy.go:550":                                                                         true,
	"the id of the schedule or workflow a revision belongs to, compared before a restore: revisions.go:269, workflow.go:651":                                                                                true,
	"the initiator's actor kind, agent identity, session identity or runtime generation, replayed into the step actor beside its account id: workflow_run.go:837-842, workflow_work_run.go:18-28":           true,
	"the observed worker, tool or MCP endpoint and the edge's kind, mode, signal and confidence labels: ingest.go:65-87, ingest.go:145-147, dto.go:51-65":                                                   true,
	"the remote protocol's bounded projection, ids, hashes and verdicts only: workflow_run.go:1515-1537":                                                                                                    true,
	"the run, step, profile, key id, fingerprint or generation of an approved target binding: workflow_run.go:393-397":                                                                                      true,
}

func historicalOrchestrationNoneReason(kind model.Kind, reason string) bool {
	return strings.HasPrefix(string(kind), "orchestration.") && historicalOrchestrationNoneReasons[reason]
}

func TestHistoricalOrchestrationNoneReasonsAreExact(t *testing.T) {
	const original = "a step ref or slug, matched by stepRefPattern: workflow_graph.go:183"
	if !historicalOrchestrationNoneReason("orchestration.workflow", original) {
		t.Fatal("lost immutable historical reason")
	}
	for _, reason := range []string{original + " changed", strings.ReplaceAll(original, ":183", ":9999"), "new missing reader: workflow_graph.go:183"} {
		if historicalOrchestrationNoneReason("orchestration.workflow", reason) {
			t.Fatalf("admitted unrecorded reason %q", reason)
		}
	}
	if historicalOrchestrationNoneReason("notify.route", original) {
		t.Fatal("historical inventory covered a foreign module")
	}
}

// censusBootFixture registers real scratch schema through the runtime's module
// seam, so the test reaches the same census read as a production boot.
type censusBootFixture struct{ sdk.Module }

func (censusBootFixture) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "census-boot-fixture"}
}

func (censusBootFixture) RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(censusFixtureDescriptor("fixture.boot_none", model.KindText, model.None("no user")))
}

func TestBootWarnsWhenRetirementCensusIsUnready(t *testing.T) {
	prepareCompositionTestBoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var logs bytes.Buffer
	b := &bootState{cfg: bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", NoIngest: true,
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}}
	defer b.unwind()
	for _, phase := range []func(context.Context) error{b.configure, b.loadSigningCustody, b.buildRuntime} {
		if err := phase(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.rt.AddModule(censusBootFixture{}, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := b.openStore(ctx); err != nil {
		t.Fatalf("an unready census must keep serving: %v", err)
	}
	defer func() { _ = b.st.Close() }()
	readiness := b.census.CompositionReadiness()
	if readiness.Ready() || !strings.Contains(readiness.Cause(), "fixture.boot_none.doc") {
		t.Fatalf("want citation-less None as the first cause, got %+v", readiness)
	}
	warnings := 0
	scanner := bufio.NewScanner(&logs)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "retirement: composition census is unready; account retirement is blocked" {
			continue
		}
		warnings++
		if record["level"] != "WARN" || record["cause"] != readiness.Cause() {
			t.Fatalf("want WARN with first readiness cause %q, got %v", readiness.Cause(), record)
		}
		t.Logf("boot operator signal: %s", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if warnings != 1 {
		t.Fatalf("got %d retirement census warnings, want exactly one", warnings)
	}
}
