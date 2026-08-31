// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The self-test battery: throwaway trees that PROVE this gate can fail.
//
// A gate nobody has watched fail is a gate nobody knows works. Every trap below is a
// tree built from scratch in TMPDIR, mutated in exactly one way, and run through the
// same run() the real invocation uses — the verdict is the process exit code, not an
// internal boolean.
//
// The GREEN cases are not decoration: without them, a gate that failed on everything
// would pass the whole red column and read as excellent.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A NOTE ON THE SESSION ID IN THE FIXTURES BELOW. The internal-marker cases spell it
// a number no session has, and not a real one, because the detector under test
// must be fed something SHAPED like an internal session id to be tested at all.
//
// This note used to say something else, and it is worth recording what: it said four
// digits were chosen because scripts/export-scrub matched \bS[0-9]{2,3} and a
// three-digit fixture would be flagged as a leak in the curated public export. That
// was true, and it was a workaround for a HOLE. The same bound made the export gate
// blind to every real four-digit session: a full export of main carried 35 such
// tokens (…) while the gate reported 0 leaks and CLEAN.
// The bound is now {2,4}, matching derive.go's own \bS[0-9]{2,4}\b, so this file IS
// flagged — and the fixture is carried where a reviewed non-leak belongs, by an
// explicit entry in scripts/export-scrub/allow-strings.txt keyed to the exact phrase
// "the shape". Do not widen that entry to the bare token: the phrase is what
// makes it a fixture rather than a licence for this file to leak anything S-shaped.

// --- the fixture ---------------------------------------------------------------

// fixtureModule is a module package in the shape the real ones have: a namespace
// constant, an APIRoutes method registering literal routes, and handlers of which one
// documents itself and one does not.
const fixtureModule = `package demo

const Namespace = "demo"

type Module struct{}

func (m *Module) APINamespace() string { return Namespace }

func (m *Module) APIRoutes(reg RouteRegistrar) {
	reg.Handle("GET", "/things", "demo:thing:read", m.handleListThings)
	reg.Handle("POST", "/things", "demo:thing:write", m.handleCreateThing)
	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
}

// handleListThings lists the demo things recorded in the tenant scope, optionally
// filtered by the kind query parameter.
func (m *Module) handleListThings(w, r, mc int) {}

func (m *Module) handleCreateThing(w, r, mc int) {}

// handleGetThing returns one demo thing.
func (m *Module) handleGetThing(w, r, mc int) {}
`

const fixtureCatalog = "GET\t/v1/agents\tLists the agents of the resolved tenant, with cursor paging.\n" +
	"POST\t/v1/m/demo/things\tCreates one demo thing from the posted document.\n"

// writeFixture builds a complete, CONSISTENT tree: three module routes, one stable
// operation, a catalog covering exactly what the code cannot describe, published
// documents carrying the composed descriptions and a generated table that matches.
func writeFixture(root string) error {
	files := map[string]string{
		"modules/demo/demo.go":           fixtureModule,
		"scripts/openapi-op-catalog.tsv": fixtureCatalog,
		"web/openapi/openapi.json":       `{"paths":{"/v1/agents":{"get":{"summary":"List agents"}}}}`,
		"web/openapi/openapi.beta.json": `{"paths":{` +
			`"/v1/m/demo/things":{"get":{"summary":"demo module route"},"post":{"summary":"demo module route"}},` +
			`"/v1/m/demo/things/{id}":{"get":{"summary":"demo module route"}}}}`,
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "core", "api"), 0o755); err != nil {
		return err
	}
	return seal(root)
}

// seal makes the fixture self-consistent: it composes the descriptions, writes the
// generated table and stamps the same text onto the published documents — which is
// exactly what `--write` plus `task openapi:dump` do in the repository.
func seal(root string) error {
	// The beta document is REBUILT from the registered routes first, because that is
	// what `task openapi:dump` does: the reflector puts a newly registered route in the
	// document without anyone editing it. Sealing without this step would make every
	// "a module added a route" case fail at setup on the stale-document check instead
	// of exercising the behaviour under test.
	routes, err := enumerateModuleRoutes(root)
	if err != nil {
		return err
	}
	paths := map[string]any{}
	for _, r := range routes {
		item, ok := paths[r.specPath()].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[r.specPath()] = item
		}
		item[strings.ToLower(r.method)] = map[string]any{"summary": r.ns + " module route"}
	}
	betaDoc, err := json.Marshal(map[string]any{"paths": paths})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, betaSpecRel), betaDoc, 0o644); err != nil {
		return err
	}

	m, err := compose(root)
	if err != nil {
		return err
	}
	if len(m.problems) > 0 {
		return fmt.Errorf("fixture is not describable: %s — %s", m.problems[0].key, m.problems[0].why)
	}
	if err := os.WriteFile(filepath.Join(root, generatedRel), []byte(renderGenerated(m)), 0o644); err != nil {
		return err
	}
	for _, rel := range []string{stableSpecRel, betaSpecRel} {
		if err := stampSpec(filepath.Join(root, rel), m); err != nil {
			return err
		}
	}
	return nil
}

func stampSpec(path string, m *model) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	paths, _ := doc["paths"].(map[string]any)
	for p, item := range paths {
		ops, _ := item.(map[string]any)
		for method, raw := range ops {
			op, _ := raw.(map[string]any)
			if op == nil {
				continue
			}
			if e, ok := m.entries[strings.ToUpper(method)+" "+p]; ok {
				op["description"] = e.description
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// --- helpers the mutations use --------------------------------------------------

func edit(root, rel string, f func(string) string) error {
	p := filepath.Join(root, rel)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(f(string(b))), 0o644)
}

func editSpec(root, rel string, f func(map[string]any)) error {
	return edit(root, rel, func(s string) string {
		var doc map[string]any
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			return s
		}
		f(doc)
		out, err := json.Marshal(doc)
		if err != nil {
			return s
		}
		return string(out)
	})
}

func firstOp(doc map[string]any) map[string]any {
	paths, _ := doc["paths"].(map[string]any)
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ops, _ := paths[k].(map[string]any)
		mk := make([]string, 0, len(ops))
		for m := range ops {
			mk = append(mk, m)
		}
		sort.Strings(mk)
		for _, m := range mk {
			if op, ok := ops[m].(map[string]any); ok {
				return op
			}
		}
	}
	return nil
}

// --- the battery -----------------------------------------------------------------

type selfCase struct {
	name string
	want int // the exit code this tree MUST produce
	// mutate perturbs the sealed fixture in exactly one way. nil means "leave the
	// consistent tree alone" — the green control.
	mutate func(root string) error
	// wantErr is a substring the report MUST carry. An exit code alone says the tree
	// went red SOMEWHERE, which is not the same as "this guard fired": measured
	// 2026-08-17, two guards could be deleted outright and every case kept its exit
	// code because a second guard failed the same tree.
	wantErr string
	// write runs the -write path instead of the check path.
	write bool
	// list runs the -list path instead of the check path.
	list bool
}

func selfCases() []selfCase {
	return []selfCase{
		// ---- GREEN: the gate must not fire on a tree that is in order ----------
		{name: "consistent-tree", want: exitClean},
		{name: "terse-but-real-handler-sentence", want: exitClean, mutate: func(root string) error {
			// "Returns one demo thing." is the shortest sentence in the fixture and is
			// published as written: the floor rejects an empty gesture, not brevity.
			return nil
		}},
		{name: "stable-operation-described-by-a-row", want: exitClean, mutate: func(root string) error {
			return editSpec(root, stableSpecRel, func(doc map[string]any) {})
		}},
		{name: "handler-documented-later-row-removed", want: exitClean, mutate: func(root string) error {
			// The undocumented handler gains a doc comment AND loses its catalog row:
			// the code becomes the single source, which is the direction we want.
			if err := edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s,
					"func (m *Module) handleCreateThing",
					"// handleCreateThing creates one demo thing from the posted document.\nfunc (m *Module) handleCreateThing", 1)
			}); err != nil {
				return err
			}
			if err := edit(root, catalogRel, func(s string) string {
				return strings.Replace(s, "POST\t/v1/m/demo/things\tCreates one demo thing from the posted document.\n", "", 1)
			}); err != nil {
				return err
			}
			return seal(root)
		}},
		{name: "new-route-added-and-regenerated", want: exitClean, mutate: func(root string) error {
			if err := addRoute(root); err != nil {
				return err
			}
			return seal(root)
		}},

		// ---- RED: drift the gate must name -------------------------------------
		{name: "new-route-not-regenerated", want: exitDrift, mutate: func(root string) error {
			// The route reaches the published document (the reflector puts it there)
			// but nobody regenerated the table: the operation ships with no description.
			if err := addRoute(root); err != nil {
				return err
			}
			return editSpec(root, betaSpecRel, func(doc map[string]any) {
				paths, _ := doc["paths"].(map[string]any)
				paths["/v1/m/demo/widgets"] = map[string]any{"get": map[string]any{"summary": "demo module route"}}
			})
		}},
		// The wantErr is the point of this case, not the exit code: without it, deleting
		// the "no description" report entirely left the tree red through the generated-table
		// and published-drift guards, and the case still passed.
		{name: "handler-loses-its-doc-comment", want: exitDrift, wantErr: "have no description", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, "// handleGetThing returns one demo thing.\n", "", 1)
			})
		}},
		{name: "handler-doc-names-an-internal-session", want: exitDrift, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, "returns one demo thing.", "returns one demo thing, the S1234 shape.", 1)
			})
		}},
		{name: "handler-doc-names-a-source-file", want: exitDrift, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, "returns one demo thing.", "returns one demo thing, see demo.go for the shape.", 1)
			})
		}},
		{name: "published-description-edited-by-hand", want: exitDrift, mutate: func(root string) error {
			return editSpec(root, betaSpecRel, func(doc map[string]any) {
				if op := firstOp(doc); op != nil {
					op["description"] = "Something a human typed straight into the contract."
				}
			})
		}},
		{name: "published-description-erased", want: exitDrift, mutate: func(root string) error {
			return editSpec(root, stableSpecRel, func(doc map[string]any) {
				if op := firstOp(doc); op != nil {
					delete(op, "description")
				}
			})
		}},
		{name: "generated-table-edited-by-hand", want: exitDrift, mutate: func(root string) error {
			return edit(root, generatedRel, func(s string) string {
				return strings.Replace(s, "Lists the agents of the resolved tenant", "Lists agents", 1)
			})
		}},
		{name: "catalog-row-for-an-operation-that-is-gone", want: exitDrift, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return s + "GET\t/v1/m/demo/removed\tLists the things this route used to list.\n"
			})
		}},
		{name: "catalog-row-shadows-a-documented-handler", want: exitDrift, mutate: func(root string) error {
			// The row says EXACTLY what the handler's doc comment yields. That is
			// deliberate: with any other text the table and the published document would
			// disagree too, and the case would go red through THAT guard while the
			// two-sources check slept. Isolated like this, only the check under test can
			// fail it.
			return edit(root, catalogRel, func(s string) string {
				return s + "GET\t/v1/m/demo/things/{id}\tReturns one demo thing.\n"
			})
		}},
		{name: "catalog-row-is-a-vacuous-gesture", want: exitDrift, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return strings.Replace(s, "Creates one demo thing from the posted document.", "Creates it.", 1)
			})
		}},
		{name: "catalog-row-restates-the-summary", want: exitDrift, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return strings.Replace(s, "Creates one demo thing from the posted document.",
					"Demo module route that requires demo:thing:write.", 1)
			})
		}},
		{name: "catalog-row-would-corrupt-a-generated-comment", want: exitDrift, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return strings.Replace(s, "Creates one demo thing from the posted document.",
					"Creates one demo thing from the posted document */ and more.", 1)
			})
		}},
		{name: "catalog-row-makes-a-forbidden-claim", want: exitDrift, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return strings.Replace(s, "Creates one demo thing from the posted document.",
					"Creates one demo thing through a tamper-proof path.", 1)
			})
		}},
		{name: "catalog-row-hedges-a-count", want: exitDrift, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return strings.Replace(s, "Creates one demo thing from the posted document.",
					"Creates one demo thing out of more than 20 candidate shapes.", 1)
			})
		}},
		// -list has its own verdict too, and it was "clean" whatever it found: it printed
		// a row with two empty fields for an operation nothing could describe and exited
		// 0, which is the code that means every operation has one.
		{name: "list-does-not-certify-a-roster-with-a-hole", want: exitDrift, list: true,
			wantErr: "have no description", mutate: func(root string) error {
				return edit(root, "modules/demo/demo.go", func(s string) string {
					return strings.Replace(s, "// handleGetThing returns one demo thing.\n", "", 1)
				})
			}},
		// -write has its own verdict and until 2026-08-17 nothing exercised it. Writing a
		// table that omits the operations it could not describe would make the NEXT run
		// green over a document where those operations still ship with no description.
		{name: "write-refuses-while-an-operation-has-no-description", want: exitDrift, write: true,
			wantErr: "refusing to write", mutate: func(root string) error {
				return edit(root, "modules/demo/demo.go", func(s string) string {
					return strings.Replace(s, "// handleGetThing returns one demo thing.\n", "", 1)
				})
			}},

		// ---- CANNOT LOOK: never a clean exit ------------------------------------
		{name: "document-and-routes-disagree", want: exitCannotSee, mutate: func(root string) error {
			return addRoute(root) // registered, never dumped
		}},
		{name: "stable-document-missing", want: exitCannotSee, mutate: func(root string) error {
			return os.Remove(filepath.Join(root, stableSpecRel))
		}},
		{name: "beta-document-missing", want: exitCannotSee, mutate: func(root string) error {
			return os.Remove(filepath.Join(root, betaSpecRel))
		}},
		{name: "catalog-missing", want: exitCannotSee, mutate: func(root string) error {
			return os.Remove(filepath.Join(root, catalogRel))
		}},
		{name: "generated-table-missing", want: exitCannotSee, mutate: func(root string) error {
			return os.Remove(filepath.Join(root, generatedRel))
		}},
		// Both emptiness guards need their wantErr: an empty document also makes the
		// registered routes and the document disagree, so the tree goes to 2 through the
		// stale-document guard whether these two exist or not. Measured 2026-08-17:
		// deleting the beta guard changed nothing the battery could see.
		{name: "document-has-no-operations", want: exitCannotSee, wantErr: "publishes no operations at all", mutate: func(root string) error {
			return os.WriteFile(filepath.Join(root, betaSpecRel), []byte(`{"paths":{}}`), 0o644)
		}},
		{name: "stable-document-has-no-operations", want: exitCannotSee, wantErr: "publishes no operations at all", mutate: func(root string) error {
			return os.WriteFile(filepath.Join(root, stableSpecRel), []byte(`{"paths":{}}`), 0o644)
		}},
		{name: "one-key-published-by-both-documents", want: exitCannotSee, wantErr: "published by BOTH documents", mutate: func(root string) error {
			// One operation key cannot name two descriptions. Without this guard the key
			// lands twice in the generated map literal, which is a Go compile error in
			// core/api — loud, but three steps downstream and unexplained.
			return editSpec(root, betaSpecRel, func(doc map[string]any) {
				paths, _ := doc["paths"].(map[string]any)
				paths["/v1/agents"] = map[string]any{"get": map[string]any{"summary": "List agents"}}
			})
		}},
		{name: "document-is-not-readable", want: exitCannotSee, mutate: func(root string) error {
			return os.WriteFile(filepath.Join(root, stableSpecRel), []byte("not json at all"), 0o644)
		}},
		{name: "catalog-row-is-malformed", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return s + "GET /v1/agents no tabs here\n"
			})
		}},
		{name: "catalog-describes-one-operation-twice", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, catalogRel, func(s string) string {
				return s + "GET\t/v1/agents\tLists the agents of the resolved tenant, a second time.\n"
			})
		}},
		{name: "module-source-does-not-parse", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string { return s + "\nfunc broken( {\n" })
		}},
		{name: "route-registered-outside-apiroutes", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return s + "\nfunc (m *Module) extraRoutes(reg RouteRegistrar) {\n" +
					"\treg.Handle(\"GET\", \"/hidden\", \"demo:thing:read\", m.handleGetThing)\n}\n"
			})
		}},
		{name: "route-pattern-is-computed", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, `reg.Handle("GET", "/things",`, `reg.Handle("GET", thingsPath,`, 1)
			})
		}},
		{name: "same-route-registered-twice", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s,
					`reg.Handle("GET", "/things", "demo:thing:read", m.handleListThings)`,
					`reg.Handle("GET", "/things", "demo:thing:read", m.handleListThings)`+"\n\t"+
						`reg.Handle("GET", "/things", "demo:thing:read", m.handleGetThing)`, 1)
			})
		}},
		{name: "namespace-is-unreadable", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, `const Namespace = "demo"`, `var namespaces = map[int]string{}`, 1)
			})
		}},
	}
}

// addRoute registers a fourth route with a publishable doc comment, WITHOUT touching
// the published document — the "a module added a route" move.
func addRoute(root string) error {
	return edit(root, "modules/demo/demo.go", func(s string) string {
		s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}",`,
			`	reg.Handle("GET", "/widgets", "demo:widget:read", m.handleListWidgets)`+"\n"+
				`	reg.Handle("GET", "/things/{id}",`, 1)
		return s + "\n// handleListWidgets lists the demo widgets recorded in the tenant scope.\n" +
			"func (m *Module) handleListWidgets(w, r, mc int) {}\n"
	})
}

// predicateCase exercises one PURE control directly. A tree-level case cannot isolate
// these: change an input after the fixture is sealed and the table/publication guards
// fire on the same tree, so a mutant that disabled the predicate would still be
// "killed" — by a witness measuring a different guard. Measured 2026-08-16: four
// controls were exactly in that position.
type predicateCase struct {
	name     string
	reject   bool // the control must REFUSE this text
	text     string
	handler  string // non-empty ⇒ run deriveFromDoc(handler, text) instead
	docInput bool
}

func predicateCases() []predicateCase {
	return []predicateCase{
		{name: "accept-a-real-sentence", reject: false, text: "Lists the demo things recorded in the tenant scope."},
		{name: "accept-the-shortest-true-sentence", reject: false, text: "Returns one demo thing."},
		{name: "reject-a-vacuous-gesture", reject: true, text: "Lists them."},
		{name: "reject-a-summary-restatement", reject: true, text: "Demo module route that requires demo:thing:read."},
		{name: "reject-an-internal-session-id", reject: true, text: "Returns one demo thing, the S1234 shape."},
		{name: "reject-a-source-file-name", reject: true, text: "Returns one demo thing, see demo.go for the shape."},
		{name: "reject-a-work-marker", reject: true, text: "Returns one demo thing. TODO: page this properly."},
		{name: "reject-comment-corrupting-text", reject: true, text: "Returns one demo thing */ and then some."},
		{name: "reject-a-template-substitution", reject: true, text: "Returns one demo thing for ${tenant} scope."},
		{name: "reject-a-forbidden-claim", reject: true, text: "Returns one demo thing through a tamper-proof path."},
		{name: "reject-a-hedged-count", reject: true, text: "Returns one demo thing out of more than 20 shapes."},
		{name: "reject-a-sentence-with-no-full-stop", reject: true, text: "Returns one demo thing"},
		{name: "reject-a-lower-case-opening", reject: true, text: "returns one demo thing of the tenant."},
		// The five floors below had no case of their own until 2026-08-17: each could be
		// deleted from validateDescription and the whole battery stayed green.
		{name: "reject-a-two-word-gesture", reject: true, text: "Superlongwordthing enough."},
		{name: "reject-leading-whitespace", reject: true, text: " Returns one demo thing of the tenant."},
		{name: "reject-a-double-space", reject: true, text: "Returns one  demo thing of the tenant."},
		// A vertical tab, mid-sentence so the full-stop rule cannot fire first. The real
		// arrival is a CRLF catalog file: readCatalog splits on \n and leaves the \r on
		// the description, which then reaches the JSON and the generated Go.
		{name: "reject-a-control-character", reject: true, text: "Returns one\vdemo thing of the tenant."},
		{name: "reject-a-description-that-becomes-the-page", reject: true,
			text: strings.TrimSpace(strings.Repeat("Returns one demo thing of the tenant scope. ", 20))},
		{name: "derive-accepts-a-documented-handler", reject: false, docInput: true, handler: "handleGetThing",
			text: "handleGetThing returns one demo thing of the tenant scope."},
		{name: "derive-refuses-a-handler-with-no-doc", reject: true, docInput: true, handler: "handleGetThing", text: ""},
		{name: "derive-refuses-a-handler-it-could-not-resolve", reject: true, docInput: true, handler: "",
			text: "handleGetThing returns one demo thing of the tenant scope."},
		{name: "derive-refuses-prose-about-the-function", reject: true, docInput: true, handler: "handleGetThing",
			text: "handleGetThing is the demo thing dispatch for one route."},
		{name: "derive-refuses-a-doc-that-does-not-open-with-the-name", reject: true, docInput: true, handler: "handleGetThing",
			text: "Returns one demo thing of the tenant scope."},
	}
}

func runPredicates() (failed, ran int) {
	for _, c := range predicateCases() {
		ran++
		var err error
		if c.docInput {
			_, err = deriveFromDoc(c.handler, c.text)
		} else {
			err = validateDescription(c.text)
		}
		rejected := err != nil
		if rejected != c.reject {
			verb := "accepted"
			if rejected {
				verb = "refused"
			}
			fmt.Fprintf(os.Stderr, "self-test %-42s %s %q\n", c.name, verb, clip(c.text))
			failed++
		}
	}
	return failed, ran
}

// ruleBase is a sentence validateDescription accepts. Every roster probe below is this
// sentence plus ONE offending fragment, so a probe that goes red went red for its own
// rule and not because the carrier sentence was short, lower-case or unpunctuated. The
// coverage run asserts the carrier itself is accepted before it trusts a single probe.
const ruleBase = "Creates one demo thing from the posted document"

// runRuleCoverage walks the ROSTERS validateDescription is built from — the forbidden
// substrings, the hedge words, the internal markers — and proves each entry refuses on
// its own, NAMING itself in the error.
//
// It replaces a hand-written case per entry on purpose. Measured 2026-08-17 against the
// battery as it stood: a mutant that deleted five of the twelve forbidden substrings
// survived, and so did one that left only "more than" of the seven hedge words — the
// cases that existed covered the three somebody thought of, and an entry added later
// would have arrived with no witness at all. Walking the slice means the roster IS the
// battery: a new entry with no probe cannot pass.
func runRuleCoverage() (failed, ran int) {
	if err := validateDescription(ruleBase + "."); err != nil {
		fmt.Fprintf(os.Stderr, "self-test rule-coverage: the carrier sentence is not otherwise valid (%v), "+
			"so every probe below would be red for the wrong reason\n", err)
		return 1, 0
	}
	check := func(kind, name, text string, want ...string) {
		ran++
		err := validateDescription(text)
		if err == nil {
			fmt.Fprintf(os.Stderr, "self-test rule-coverage %-9s %-46s ACCEPTED %q\n", kind, name, clip(text))
			failed++
			return
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				fmt.Fprintf(os.Stderr, "self-test rule-coverage %-9s %-46s refused, but not for its own rule: %v\n",
					kind, name, err)
				failed++
				return
			}
		}
	}
	for _, f := range forbidden {
		check("forbidden", f.sub, ruleBase+", with "+f.sub+" in it.", fmt.Sprintf("%q", f.sub))
	}
	for _, w := range hedgeWords {
		check("hedge", w, ruleBase+", one of "+w+" 20 shapes.", "hedges a count", w)
	}
	for _, m := range internalMarkers {
		if strings.TrimSpace(m.probe) == "" {
			fmt.Fprintf(os.Stderr, "self-test rule-coverage %-9s %-46s has no probe, so nothing proves it fires\n",
				"internal", m.why)
			failed++
			ran++
			continue
		}
		check("internal", m.why, ruleBase+", "+m.probe+".", m.why)
	}
	return failed, ran
}

func ruleCoverageCount() int {
	return len(forbidden) + len(hedgeWords) + len(internalMarkers) + len(requiredRefusals())
}

// THE SECOND DIRECTION, and the reason the spec below is written out instead of derived.
//
// runRuleCoverage walks the rosters the implementation HAS, so it catches an entry added
// with no probe or one that does not fire. It cannot catch a DELETION: delete
// "impossible" from `forbidden` and the walk shrinks with it and stays green — measured
// 2026-08-17, exactly that mutant survived. These lists are therefore an independent
// statement of what the CONTRACT requires, each traced to where the requirement is
// written, and a rule that leaves the implementation now leaves a hole this can see.
var (
	// CANON-OPERATIVO §371-372: absolute claims are forbidden on a public surface.
	canonLexicon = []string{"impossible", "infallible", "tamper-proof", "tamper proof",
		"unhackable", "bulletproof", "100% secure"}
	// clients/generator/spec.go validateCommentText: the substrings that close a
	// generated comment or docstring early. A description reaches the @description JSDoc
	// of web/src/lib/api/openapi.gen.ts, which is the same kind of block comment.
	corruptingSubstrings = []string{`"""`, "*/", "`", "${", `\`}
	// scripts/check-public-counts.sh:565: the counts a public surface states are exact.
	hedgeSpec = []string{"more than", "over", "about", "around", "approximately",
		"nearly", "some", "roughly", "almost"}
	// The internal facts a published contract must not carry.
	markerSpec = []string{"the S1234 shape", "see demo.go for the shape", "TODO: page this properly"}
)

func requiredRefusals() []struct{ kind, text string } {
	var out []struct{ kind, text string }
	add := func(kind, text string) {
		out = append(out, struct{ kind, text string }{kind, text})
	}
	for _, w := range canonLexicon {
		add("canon", ruleBase+", with "+w+" in it.")
	}
	for _, w := range corruptingSubstrings {
		add("corrupting", ruleBase+", with "+w+" in it.")
	}
	for _, w := range hedgeSpec {
		add("hedge-spec", ruleBase+", one of "+w+" 20 shapes.")
	}
	for _, w := range markerSpec {
		add("marker-spec", ruleBase+", "+w+".")
	}
	return out
}

func runRequiredRefusals() (failed, ran int) {
	for _, r := range requiredRefusals() {
		ran++
		if err := validateDescription(r.text); err == nil {
			fmt.Fprintf(os.Stderr, "self-test required-refusal %-11s ACCEPTED %q — a rule the contract requires is gone\n",
				r.kind, clip(r.text))
			failed++
		}
	}
	return failed, ran
}

func selfTest() int {
	cases := selfCases()
	predFailed, predRan := runPredicates()
	coverFailed, coverRan := runRuleCoverage()
	reqFailed, reqRan := runRequiredRefusals()
	failed := predFailed + coverFailed + reqFailed
	// COUNT WHAT RAN, not what exists. Every check above is a call in this function, and
	// a call is deletable: measured 2026-08-17, unplugging runRequiredRefusals() left the
	// battery green and its printed total unchanged, because the total was computed from
	// the rosters rather than from the work. The three counters below are returned by the
	// runs themselves, so a run that no longer happens cannot be reported as one that
	// passed.
	if ran, want := predRan+coverRan+reqRan, len(predicateCases())+ruleCoverageCount(); ran != want {
		fmt.Fprintf(os.Stderr, "self-test: %d predicate/roster checks ran but %d exist — a run was unplugged, "+
			"and a check that did not happen is not a check that passed\n", ran, want)
		failed++
	}
	for _, c := range cases {
		root, err := os.MkdirTemp("", "openapi-op-desc-selftest-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "self-test: CANNOT LOOK — no scratch dir: %v\n", err)
			return exitCannotSee
		}
		got, report, err := runCase(root, c)
		os.RemoveAll(root)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "self-test %-52s SETUP FAILED: %v\n", c.name, err)
			failed++
		case got != c.want:
			fmt.Fprintf(os.Stderr, "self-test %-52s want exit %d, got %d\n", c.name, c.want, got)
			failed++
		case c.wantErr != "" && !strings.Contains(report, c.wantErr):
			// The exit code alone only says the tree went red somewhere. Where a case
			// names the guard it exists for, the guard has to be the one that spoke.
			fmt.Fprintf(os.Stderr, "self-test %-52s exited %d as promised, but its own guard never spoke: no %q in the report\n",
				c.name, got, c.wantErr)
			failed++
		}
	}
	total := len(cases) + len(predicateCases()) + ruleCoverageCount()
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "self-test: %d of %d checks did NOT behave as the gate promises\n", failed, total)
		return exitDrift
	}
	red, green, blind := 0, 0, 0
	for _, c := range cases {
		switch c.want {
		case exitClean:
			green++
		case exitDrift:
			red++
		default:
			blind++
		}
	}
	fmt.Printf("openapi-op-descriptions self-test: %d checks, every red case red, every green case green "+
		"(%d green, %d drift, %d cannot-look, %d predicate, %d rule-roster, %d required-refusal)\n",
		total, green, red, blind, len(predicateCases()),
		len(forbidden)+len(hedgeWords)+len(internalMarkers), len(requiredRefusals()))
	return exitClean
}

func runCase(root string, c selfCase) (int, string, error) {
	if err := writeFixture(root); err != nil {
		return 0, "", err
	}
	if c.mutate != nil {
		if err := c.mutate(root); err != nil {
			return 0, "", err
		}
	}
	var out, errw bytes.Buffer
	rc := run(root, c.write, c.list, &out, &errw)
	return rc, out.String() + errw.String(), nil
}
