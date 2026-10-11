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
type AssignmentAuthority interface { Ref() int; Scoped() bool }

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

// swap is edit plus the promise that the anchor was there: a mutation that replaces
// nothing did not happen, and a check that did not happen is not a check that
// passed. A fixture that drifts under an anchor must fail setup, never pass green.
func swap(root, rel, old, new string) error {
	p := filepath.Join(root, rel)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	s := string(b)
	if !strings.Contains(s, old) {
		return fmt.Errorf("fixture anchor missing in %s: %q", rel, clip(old))
	}
	return os.WriteFile(p, []byte(strings.Replace(s, old, new, 1)), 0o644)
}

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
	cases := []selfCase{
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
		// Registration sites remain duplicates even in conditional branches.
		{name: "same-route-in-mutually-exclusive-branches", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/scoped", "demo:thing:write", 1, m.handleGetThing)
	} else {
		reg.Handle("POST", "/scoped", "demo:thing:write", m.handleGetThing)
	}`); err != nil {
				return err
			}
			return nil
		}},
		{name: "same-route-in-an-else-if-chain", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/chained", "demo:thing:write", 1, m.handleGetThing)
	} else if governed() {
		reg.Handle("POST", "/chained", "demo:thing:write", m.handleGetThing)
	} else {
		reg.Handle("POST", "/chained", "demo:thing:write", m.handleGetThing)
	}`); err != nil {
				return err
			}
			return nil
		}},
		{name: "same-route-in-nested-exclusive-branches", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		if enabled {
			reg.HandleEntity("POST", "/nested", "demo:thing:write", 1, m.handleGetThing)
		} else {
			reg.Handle("POST", "/nested", "demo:thing:write", m.handleGetThing)
		}
	} else {
		reg.Handle("POST", "/nested", "demo:thing:write", m.handleGetThing)
	}`); err != nil {
				return err
			}
			return nil
		}},
		{name: "same-route-in-nested-branches-with-a-later-handler-mismatch", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		if enabled {
			reg.HandleEntity("POST", "/nestedmismatch", "demo:thing:write", 1, m.handleGetThing)
		} else {
			reg.Handle("POST", "/nestedmismatch", "demo:thing:write", m.handleGetThing)
		}
	} else {
		reg.Handle("POST", "/nestedmismatch", "demo:thing:write", m.handleListThings)
	}`)
		}},
		{name: "same-route-in-exclusive-branches-in-a-helper", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
}`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	m.scoped(reg)
}

func (m *Module) scoped(reg RouteRegistrar) {
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/helped", "demo:thing:write", 1, m.handleGetThing)
	} else {
		reg.Handle("POST", "/helped", "demo:thing:write", m.handleGetThing)
	}
}`); err != nil {
				return err
			}
			return nil
		}},
		{name: "same-route-in-exclusive-branches-with-direct-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		m.APIRoutes(reg)
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s
			})
		}},
		{name: "same-route-in-exclusive-branches-with-helper-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		m.again(reg)
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s + `

func (m *Module) again(reg RouteRegistrar) { m.APIRoutes(reg) }
`
			})
		}},
		{name: "same-route-in-exclusive-branches-with-package-function-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		again(m, reg)
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s + `

func again(m *Module, reg RouteRegistrar) { m.APIRoutes(reg) }
`
			})
		}},
		{name: "same-route-in-exclusive-branches-with-method-value-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		again := m.APIRoutes
		again(reg)
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s
			})
		}},
		{name: "same-route-in-exclusive-branches-with-method-expression-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		(*Module).APIRoutes(m, reg)
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s
			})
		}},
		{name: "same-route-in-exclusive-branches-with-closure-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		func() { m.APIRoutes(reg) }()
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s
			})
		}},
		{name: "same-route-in-exclusive-branches-with-helper-with-stored-registrar-reentry", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				s = strings.Replace(s, "type Module struct{}", "type Module struct{ enabled bool; reg RouteRegistrar }", 1)
				s = strings.Replace(s, `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.enabled = false
		m.reg = reg
		m.again()
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, 1)
				return s + `

func (m *Module) again() { m.APIRoutes(m.reg) }
`
			})
		}},
		{name: "same-route-in-exclusive-branches-with-a-for-repeated-registration", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go", `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if enabled {
		for i := 0; i < 2; i++ {
			reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		}
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`)
		}},
		{name: "same-route-in-exclusive-branches-with-a-range-repeated-registration", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go", `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if enabled {
		for range []int{0, 1} {
			reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		}
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`)
		}},
		{name: "same-route-in-exclusive-branches-with-a-for-post-repeated-registration", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go", `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if enabled {
		for i := 0; i < 2; reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing) { i++ }
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`)
		}},
		{name: "same-route-in-exclusive-branches-with-a-once-only-for-initializer", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go", `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if enabled {
		for reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing); false; {}
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`)
		}},
		{name: "same-route-in-exclusive-branches-in-closures", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		func() { reg.HandleEntity("POST", "/lit", "demo:thing:write", 1, m.handleGetThing) }()
	} else {
		func() { reg.Handle("POST", "/lit", "demo:thing:write", m.handleGetThing) }()
	}`)
		}},
		{name: "same-route-in-exclusive-branches-with-a-goto", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
retry:
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/jumped", "demo:thing:write", 1, m.handleGetThing)
	} else {
		if governed() {
			goto retry
		}
		reg.Handle("POST", "/jumped", "demo:thing:write", m.handleGetThing)
	}`)
		}},
		{name: "single-registration-alongside-an-unrelated-loop", want: exitClean, mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	seen := 0
	for i := 0; i < 3; i++ {
		seen += i
	}
	_ = seen
	reg.Handle("POST", "/loopnear", "demo:thing:write", m.handleGetThing)`); err != nil {
				return err
			}
			return seal(root)
		}},
		{name: "single-route-with-an-unresolved-handler", want: exitClean, mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	reg.Handle("POST", "/computed", "demo:thing:write", makeHandler())`); err != nil {
				return err
			}
			if err := swap(root, catalogRel,
				"POST\t/v1/m/demo/things\tCreates one demo thing from the posted document.\n",
				"POST\t/v1/m/demo/things\tCreates one demo thing from the posted document.\n"+
					"POST\t/v1/m/demo/computed\tComputes one demo thing from the posted document.\n"); err != nil {
				return err
			}
			return seal(root)
		}},
		{name: "same-route-in-exclusive-branches-with-two-handlers", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/scoped", "demo:thing:write", 1, m.handleGetThing)
	} else {
		reg.Handle("POST", "/scoped", "demo:thing:write", m.handleListThings)
	}`)
		}},
		{name: "two-duplicated-keys-name-the-sorted-first", want: exitCannotSee, wantErr: "GET /v1/m/demo/things/{id} is registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	reg.Handle("POST", "/things", "demo:thing:write", m.handleCreateThing)
	reg.Handle("POST", "/things", "demo:thing:write", m.handleCreateThing)`)
		}},
		{name: "same-route-in-a-branch-and-unconditionally", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if governed() {
		reg.Handle("POST", "/scoped", "demo:thing:write", m.handleGetThing)
	}
	reg.Handle("POST", "/scoped", "demo:thing:write", m.handleGetThing)`)
		}},
		{name: "same-route-twice-in-one-branch", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if governed() {
		reg.Handle("POST", "/branched", "demo:thing:write", m.handleGetThing)
		reg.Handle("POST", "/branched", "demo:thing:write", m.handleGetThing)
	}`)
		}},
		{name: "same-route-collapsed-pair-plus-dup-in-one-branch", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/paired", "demo:thing:write", 1, m.handleGetThing)
	} else {
		reg.Handle("POST", "/paired", "demo:thing:write", m.handleGetThing)
		reg.Handle("POST", "/paired", "demo:thing:write", m.handleGetThing)
	}`)
		}},
		{name: "same-route-under-two-separate-ifs", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if governed() {
		reg.Handle("POST", "/forked", "demo:thing:write", m.handleGetThing)
	}
	if featured() {
		reg.Handle("POST", "/forked", "demo:thing:write", m.handleGetThing)
	}`)
		}},
		{name: "same-route-in-two-switch-cases", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	switch mode(m) {
	case 1:
		reg.HandleEntity("POST", "/switched", "demo:thing:write", demoRef, m.handleGetThing)
	case 2:
		reg.Handle("POST", "/switched", "demo:thing:write", m.handleGetThing)
	}`)
		}},
		{name: "same-key-in-two-packages", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			if err := swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if governed() {
		reg.Handle("POST", "/shared", "demo:thing:write", m.handleGetThing)
	}`); err != nil {
				return err
			}
			p := filepath.Join(root, "modules/demo2/demo.go")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			return os.WriteFile(p, []byte(`package demo2

const Namespace = "demo"

type Module struct{}

func (m *Module) APINamespace() string { return Namespace }

func (m *Module) APIRoutes(reg RouteRegistrar) {
	if governed() {
	} else {
		reg.Handle("POST", "/shared", "demo:thing:write", m.handleGetThing)
	}
}

// handleGetThing returns one shared demo thing.
func (m *Module) handleGetThing(w, r, mc int) {}
`), 0o644)
		}},
		{name: "same-route-in-a-loop-enclosed-branch-pair", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	for _, mode := range modes() {
		if mode {
			reg.HandleEntity("POST", "/looped", "demo:thing:write", demoRef, m.handleGetThing)
		} else {
			reg.Handle("POST", "/looped", "demo:thing:write", m.handleGetThing)
		}
	}`)
		}},
		{name: "same-route-in-exclusive-branches-with-unresolved-handlers", want: exitCannotSee, wantErr: "could not resolve", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if authority, ok := authorityValue.(AssignmentAuthority); ok {
		reg.HandleEntity("POST", "/opaque", "demo:thing:write", 1, m.entityHandler())
	} else {
		reg.Handle("POST", "/opaque", "demo:thing:write", m.collectionHandler())
	}`)
		}},
		{name: "same-route-split-between-apiroutes-and-a-helper", want: exitCannotSee, wantErr: "registered twice", mutate: func(root string) error {
			return swap(root, "modules/demo/demo.go",
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
}`,
				`	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	if governed() {
		reg.Handle("POST", "/split", "demo:thing:write", m.handleGetThing)
	}
	m.more(reg)
}

func (m *Module) more(reg RouteRegistrar) {
	if featured() {
	} else {
		reg.Handle("POST", "/split", "demo:thing:write", m.handleGetThing)
	}
}`)
		}},
		{name: "system-route-door", want: exitClean, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, `reg.Handle("GET", "/things", "demo:thing:read", m.handleListThings)`, `door, ok := reg.(SystemRouteRegistrar)
if !ok { return }
door.HandleSystem("GET", "/things", m.handleListThings)`, 1)
			})
		}},
		{name: "composition-root-module-routes", want: exitClean, mutate: func(root string) error {
			body, err := os.ReadFile(filepath.Join(root, "modules/demo/demo.go"))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(root, "cmd/demo"), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, "cmd/demo/demo.go"), body, 0644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "modules/demo/demo.go"), []byte("package demo\n"), 0644)
		}},

		{name: "sealed-route-documented-and-regenerated", want: exitClean, mutate: func(root string) error {
			if err := addGovernedRoute(root, "HandleSealed", "handleDeleteThing deletes one demo thing and every record that names it."); err != nil {
				return err
			}
			return seal(root)
		}},
		{name: "sealed-route-handler-has-no-doc", want: exitDrift, wantErr: "handleDeleteThing", mutate: func(root string) error {
			if err := addGovernedRoute(root, "HandleSealed", ""); err != nil {
				return err
			}
			return publishDeleteThing(root)
		}},
		{name: "policy-route-handler-has-no-doc", want: exitDrift, wantErr: "handleDeleteThing", mutate: func(root string) error {
			if err := addGovernedRoute(root, "HandlePolicy", ""); err != nil {
				return err
			}
			return publishDeleteThing(root)
		}},
		{name: "sealed-route-registered-outside-apiroutes", want: exitCannotSee, wantErr: "HandleSealed", mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return s + "\nfunc (m *Module) governedRoutes(door governedDoor) {\n" +
					"\tdoor.HandleSealed(\"DELETE\", \"/things/{id}\", \"demo:thing:admin\", demoSeal, m.handleGetThing)\n}\n"
			})
		}},
		{name: "namespace-is-unreadable", want: exitCannotSee, mutate: func(root string) error {
			return edit(root, "modules/demo/demo.go", func(s string) string {
				return strings.Replace(s, `const Namespace = "demo"`, `var namespaces = map[int]string{}`, 1)
			})
		}},
	}
	for _, variant := range []string{
		"direct", "alias", "composite",
		"local-method", "method-value", "callback-field",
		"callback-alias", "callback-registrar-alias", "value-method-alias",
		"promoted-method-alias", "defer", "closure",
		"handler-factory", "callback-zero-alias", "callback-zero-parens",
		"callback-zero-var", "callback-zero-chain", "callback-zero-index",
		"callback-zero-assertion", "callback-zero-aggregate", "callback-zero-container-alias",
		"callback-zero-global", "callback-zero-global-index", "callback-zero-external-alias",
		"callback-zero-external", "callback-zero-nested", "callback-zero-nested-alias",
		"callback-zero-global-file", "callback-zero-external-container", "callback-zero-global-container",
		"callback-zero-global-container-alias", "callback-zero-asserted-container", "callback-zero-local-wrapper",
		"callback-zero-dot", "callback-zero-metadata-alias", "callback-zero-metadata-composite",
		"callback-zero-metadata-asserted-struct", "callback-zero-metadata-shadow-value",
		"callback-zero-metadata-shadow-type", "callback-zero-declared-getter",
		"callback-zero-metadata-declared-getter",
	} {
		for _, write := range []bool{false, true} {
			cases = append(cases, selfCase{
				name: fmt.Sprintf("cross-package-reentry-%s-write-%t", variant, write),
				want: exitCannotSee, wantErr: "registered twice", write: write,
				mutate: func(root string) error { return writeCrossPackageFixture(root, variant) },
			})
		}
	}
	cases = append(cases, selfCase{
		name: "exclusive-branches-with-receiver-field-condition", want: exitCannotSee, wantErr: "registered twice",
		mutate: func(root string) error {
			if err := writeCrossPackageFixture(root, "field-condition"); err != nil {
				return err
			}
			return nil
		},
	})
	for _, variant := range channelVariants {
		for _, write := range []bool{false, true} {
			cases = append(cases, selfCase{
				name: fmt.Sprintf("channel-reentry-%s-write-%t", variant, write),
				want: exitCannotSee, wantErr: "registered twice", write: write,
				mutate: func(root string) error { return writeChannelFixture(root, variant) },
			})
		}
	}
	cases = append(cases, selfCase{
		name: "single-registration-with-channel-effects", want: exitClean,
		mutate: func(root string) error {
			if err := writeChannelFixture(root, "send-receive"); err != nil {
				return err
			}
			if err := swap(root, "modules/demo/demo.go", `} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`, "}"); err != nil {
				return err
			}
			return seal(root)
		},
	})
	return cases
}

var channelVariants = []string{"send-receive", "send", "receive", "select-send", "select-receive", "range", "close", "unnamed-receiver", "panic-recovery", "bounds-panic-recovery"}

// The module never calls a helper or exposes its receiver. Channel effects and
// panic recovery let external code re-enter through the same interface as the seam.
func writeChannelFixture(root, variant string) error {
	if err := writeCrossPackageFixture(root, "field-condition"); err != nil {
		return err
	}
	var action, coordinate string
	switch variant {
	case "send-receive", "unnamed-receiver":
		action = "Wake <- struct{}{}; <-Done"
		coordinate = "<-wake; m.APIRoutes(reg); close(done)"
	case "send":
		action = "Wake <- struct{}{}; Done <- struct{}{}"
		coordinate = "<-wake; m.APIRoutes(reg); <-done"
	case "receive":
		action = "<-Wake; <-Done"
		coordinate = "wake <- struct{}{}; m.APIRoutes(reg); close(done)"
	case "select-send":
		action = "select { case Wake <- struct{}{}: }; select { case Done <- struct{}{}: }"
		coordinate = "<-wake; m.APIRoutes(reg); <-done"
	case "select-receive":
		action = "select { case <-Wake: }; select { case <-Done: }"
		coordinate = "wake <- struct{}{}; m.APIRoutes(reg); close(done)"
	case "range":
		action = "for range Wake {}"
		coordinate = "wake <- struct{}{}; m.APIRoutes(reg); close(wake)"
	case "close":
		action = "close(Wake)"
		coordinate = "<-wake; m.APIRoutes(reg)"
	case "panic-recovery":
		action = `panic("re-enter on recovery")`
	case "bounds-panic-recovery":
		action = `_ = ([]int{})[0]`
	default:
		return fmt.Errorf("unknown channel fixture %q", variant)
	}
	if err := swap(root, "modules/demo/demo.go", `if m.Enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.Enabled = false`, `if m.Enabled {
		m.Enabled = false
		`+action+`
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`); err != nil {
		return err
	}
	if err := edit(root, "modules/demo/demo.go", func(s string) string {
		if strings.HasSuffix(variant, "panic-recovery") {
			s = strings.Replace(s, `m.Enabled = false
		`+action+`
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.Enabled = false
		`+action, 1)
		}
		if variant == "unnamed-receiver" {
			s = strings.ReplaceAll(s, "m.Enabled", "Enabled")
			s = strings.Replace(s, "func (m *Module) APIRoutes", "func (*Module) APIRoutes", 1)
			s = strings.ReplaceAll(s, "m.handle", "handle")
			s = strings.ReplaceAll(s, "func (m *Module) handle", "func handle")
		}
		return s + "\nvar Wake, Done chan struct{}\nvar Enabled bool\n"
	}); err != nil {
		return err
	}
	// Count only the affected route, atomically: the close witness can register
	// both sites concurrently. Channel handshakes publish Enabled before re-entry.
	return edit(root, "helper/helper.go", func(s string) string {
		s = strings.Replace(s, "package helper", "package helper\n\nimport \"sync/atomic\"", 1)
		s = strings.Replace(s, "Count int", "Count atomic.Int64", 1)
		s = strings.Replace(s, "r.Count++", "r.Count.Add(1)", 1)
		if strings.HasSuffix(variant, "panic-recovery") {
			s += "\nfunc RecoverRoute(m Router, reg RouteRegistrar) { defer func() { if recover() != nil { m.APIRoutes(reg) } }(); m.APIRoutes(reg) }\n"
		}
		return s + "\nfunc Coordinate(m Router, reg RouteRegistrar, wake, done chan struct{}) { " + coordinate + " }\n"
	})
}

// writeCrossPackageFixture is compilable: the helper has no APIRoutes method and
// the module has no APIRoutes selector. The interface call crosses the package
// boundary the package-local re-entry fence cannot see. Runtime probes can use
// the same source as the refusal checks, rather than a similar synthetic sketch.
func writeCrossPackageFixture(root, variant string) error {
	action, extra := "", ""
	switch variant {
	case "direct":
		action = "helper.Again(m, reg)"
	case "alias":
		action = "alias := m; helper.Again(alias, reg)"
	case "composite":
		action = "wrapped := struct{ module helper.Router }{m}; helper.Again(wrapped.module, reg)"
	case "local-method":
		action = "m.again(reg)"
		extra = "func (m *Module) again(reg RouteRegistrar) { helper.Again(m, reg) }\n"
	case "method-value":
		action = "helper.Invoke(m.again, reg)"
		extra = "func (m *Module) again(reg RouteRegistrar) { helper.Again(m, reg) }\n"
	case "callback-field":
		action = "m.Callback(reg)"
	case "callback-alias":
		action = "callback := m.Callback; callback(reg)"
	case "callback-zero-alias":
		action = "callback := m.Callback; callback()"
	case "callback-zero-parens":
		action = "callback := m.Callback; (callback)()"
	case "callback-zero-var":
		action = "var callback = m.Callback; callback()"
	case "callback-zero-chain":
		action = "first := m.Callback; callback := first; callback()"
	case "callback-zero-index":
		action = "callbacks := []func(){m.Callback}; callbacks[0]()"
	case "callback-zero-assertion":
		action = "var callback any = m.Callback; callback.(func())()"
	case "callback-zero-aggregate":
		action = "callbacks := struct{ invoke func() }{m.Callback}; callbacks.invoke()"
	case "callback-zero-container-alias":
		action = "callbacks := m.Callbacks; callbacks.Invoke()"
	case "callback-zero-global":
		action = "Callback()"
		extra = "var Callback func()\n"
	case "callback-zero-global-index":
		action = "Callbacks[0]()"
		extra = "var Callbacks []func()\n"
	case "callback-zero-external-alias":
		action = "callback := helper.Callback; callback()"
	case "callback-zero-external":
		action = "helper.Callback()"
	case "callback-zero-nested":
		action = `reg.Handle("GET", "/things", "demo:thing:read", helper.HandlerCallback())`
	case "callback-zero-nested-alias":
		action = `factory := m.Factory; reg.Handle("GET", "/things", "demo:thing:read", factory())`
	case "callback-zero-global-file":
		action = "Callback()"
	case "callback-zero-external-container":
		action = "helper.CallbackContainer.Invoke()"
	case "callback-zero-global-container":
		action = "Container.Invoke()"
		extra = "var Container struct { Invoke func() }\n"
	case "callback-zero-global-container-alias":
		action = "callbacks := Container; callbacks.Invoke()"
		extra = "var Container struct { Invoke func() }\n"
	case "callback-zero-asserted-container":
		action = "callbacks := m.Object.(interface{ Invoke() }); callbacks.Invoke()"
	case "callback-zero-local-wrapper":
		action = "invokeBound()"
		extra = "func invokeBound() { helper.Callback() }\n"
	case "callback-zero-dot":
		action = "Callback()"
	case "callback-zero-declared-getter":
		action = "authority, _ := m.Object.(AssignmentAuthority); authority.Ref()"
	case "callback-zero-metadata-declared-getter":
		action = `authority, _ := m.Object.(AssignmentAuthority); reg.HandleEntity("GET", "/things", "demo:thing:read", authority.Ref(), m.handleListThings)`
	case "callback-zero-metadata-alias":
		action = `callbacks := helper.MetadataContainer; reg.HandleEntity("GET", "/things", "demo:thing:read", callbacks.Ref(), m.handleListThings)`
	case "callback-zero-metadata-composite":
		action = `callbacks := struct{ Ref func() int }{helper.MetadataContainer.Ref}; reg.HandleEntity("GET", "/things", "demo:thing:read", callbacks.Ref(), m.handleListThings)`
	case "callback-zero-metadata-asserted-struct":
		action = `callbacks := m.Object.(MetadataHolder); reg.HandleEntity("GET", "/things", "demo:thing:read", callbacks.Ref(), m.handleListThings)`
		extra = "type MetadataHolder struct { Ref func() int }\n"
	case "callback-zero-metadata-shadow-value":
		action = `callbacks := m.Object.(AssignmentAuthority); _ = callbacks; { callbacks := helper.MetadataContainer; reg.HandleEntity("GET", "/things", "demo:thing:read", callbacks.Ref(), m.handleListThings) }`
	case "callback-zero-metadata-shadow-type":
		action = `type AssignmentAuthority struct { Ref func() int }; callbacks := any(AssignmentAuthority{helper.MetadataContainer.Ref}).(AssignmentAuthority); reg.HandleEntity("GET", "/things", "demo:thing:read", callbacks.Ref(), m.handleListThings)`
	case "callback-registrar-alias":
		action = "callback := m.Callback; alias := reg; callback(alias)"
	case "value-method-alias":
		action = "callback := m.again; callback(reg)"
		extra = "func (m Module) again(reg RouteRegistrar) { helper.Again(&m, reg) }\n"
	case "promoted-method-alias":
		action = "callback := m.again; callback(reg)"
		extra = "type Embedded struct { module *Module }; func (e Embedded) again(reg RouteRegistrar) { helper.Again(e.module, reg) }; func (m *Module) Bind() { m.Embedded.module = m }\n"
	case "defer":
		action = "defer helper.Again(m, reg)"
	case "closure":
		action = "func() { helper.Again(m, reg) }()"
	case "handler-factory":
		action = `reg.Handle("GET", "/things", "demo:thing:read", helper.Reenter(m, reg))`
	case "field-condition":
	default:
		return fmt.Errorf("unknown cross-package fixture %q", variant)
	}
	path := filepath.Join(root, "modules/demo/demo.go")
	if err := os.WriteFile(path, []byte(fixtureModule), 0o644); err != nil {
		return err
	}
	if err := swap(root, "modules/demo/demo.go", "package demo", "package demo\n\nimport \"fixture/helper\"\n\ntype RouteRegistrar = helper.RouteRegistrar"); err != nil {
		return err
	}
	if variant == "callback-zero-dot" {
		if err := swap(root, "modules/demo/demo.go", `import "fixture/helper"`, `import . "fixture/helper"`); err != nil {
			return err
		}
		if err := swap(root, "modules/demo/demo.go", "type RouteRegistrar = helper.RouteRegistrar", ""); err != nil {
			return err
		}
	}
	fields := "Enabled bool; Callback func(RouteRegistrar)"
	if strings.HasPrefix(variant, "callback-zero-") {
		fields = "Enabled bool; Callback func(); Callbacks struct { Invoke func() }; Factory func() func(int, int, int); Object any"
	}
	if variant == "promoted-method-alias" {
		fields += "; Embedded"
	}
	if err := swap(root, "modules/demo/demo.go", "type Module struct{}", "type Module struct { "+fields+" }"); err != nil {
		return err
	}
	if strings.Contains(variant, "-metadata-") {
		if err := swap(root, "modules/demo/demo.go", "\treg.Handle(\"GET\", \"/things\", \"demo:thing:read\", m.handleListThings)\n", ""); err != nil {
			return err
		}
	}
	if variant == "handler-factory" || variant == "callback-zero-nested" || variant == "callback-zero-nested-alias" {
		if err := swap(root, "modules/demo/demo.go", "\treg.Handle(\"GET\", \"/things\", \"demo:thing:read\", m.handleListThings)\n", ""); err != nil {
			return err
		}
		if err := edit(root, catalogRel, func(s string) string {
			return s + "GET\t/v1/m/demo/things\tLists the demo things recorded in the tenant scope, optionally filtered by the kind query parameter.\n"
		}); err != nil {
			return err
		}
	}
	if err := swap(root, "modules/demo/demo.go", `	reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)`, `	if m.Enabled {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
		m.Enabled = false
		`+action+`
	} else {
		reg.Handle("GET", "/things/{id}", "demo:thing:read", m.handleGetThing)
	}`); err != nil {
		return err
	}
	if err := edit(root, "modules/demo/demo.go", func(s string) string { return s + "\n" + extra }); err != nil {
		return err
	}
	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.26.8\n",
		"helper/helper.go": `package helper

type RouteRegistrar interface { Handle(string, string, string, func(int, int, int)); HandleEntity(string, string, string, int, func(int, int, int)) }
type Router interface { APIRoutes(RouteRegistrar) }
var Callback func()
var HandlerCallback func() func(int, int, int)
var CallbackContainer struct { Invoke func() }
type MetadataHolder struct { Ref func() int }
var MetadataContainer MetadataHolder
type GetterCallback struct { Callback func() }
func (g *GetterCallback) Ref() int { if g.Callback != nil { g.Callback() }; return 1 }
func (*GetterCallback) Scoped() bool { return true }
type Callable struct { Callback func() }
func (c *Callable) Invoke() { c.Callback() }
func Again(m Router, reg RouteRegistrar) { m.APIRoutes(reg) }
func Invoke(f func(RouteRegistrar), reg RouteRegistrar) { f(reg) }
func Reenter(m Router, reg RouteRegistrar) func(int, int, int) {
	m.APIRoutes(reg)
	return func(int, int, int) {}
}
type Recorder struct { Count int }
func (r *Recorder) HandleEntity(method, path, perm string, ref int, handler func(int, int, int)) { r.Handle(method, path, perm, handler) }
func (r *Recorder) Handle(method, path, perm string, handler func(int, int, int)) {
	if method == "GET" && path == "/things/{id}" { r.Count++ }
}
`,
	}
	if variant == "callback-zero-global-file" {
		files["modules/demo/callback.go"] = "package demo\n\nvar Callback func()\n"
	}
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
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

// addGovernedRoute mounts DELETE /things/{id} through a governed door the way a
// module does: on a registrar found by type assertion on the RouteRegistrar, never
// on the RouteRegistrar itself. doc is the handler's doc comment, "" for none.
func addGovernedRoute(root, door, doc string) error {
	return edit(root, "modules/demo/demo.go", func(s string) string {
		last := "\treg.Handle(\"GET\", \"/things/{id}\", \"demo:thing:read\", m.handleGetThing)\n"
		s = strings.Replace(s, last, last+
			"\tgoverned, ok := reg.(governedDoor)\n\tif !ok {\n\t\treturn\n\t}\n"+
			"\tgoverned."+door+"(\"DELETE\", \"/things/{id}\", \"demo:thing:admin\", demoSeal, m.handleDeleteThing)\n", 1)
		if doc != "" {
			s += "\n// " + doc
		}
		return s + "\nfunc (m *Module) handleDeleteThing(w, r, mc int) {}\n"
	})
}

// publishDeleteThing puts DELETE /things/{id} in the beta document, as the
// reflector does for a route mounted through any door, without regenerating the
// table.
func publishDeleteThing(root string) error {
	return editSpec(root, betaSpecRel, func(doc map[string]any) {
		paths, _ := doc["paths"].(map[string]any)
		item, _ := paths["/v1/m/demo/things/{id}"].(map[string]any)
		item["delete"] = map[string]any{"summary": "demo module route"}
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
