// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// selfTest builds throwaway trees and asserts what this gate does with each. A gate
// nobody has watched fail reports green because it looked at nothing, so the RED
// cases run first and on every push.
//
// Each case fixes ONE thing. The greens are as load-bearing as the reds: without
// them a gate that returned 1 for every input would pass the whole red column, and
// the two that matter most are `transitive` (delegation through a helper is the
// shape twelve real mappers use — if it were not credited the gate would be red on
// main and someone would weaken it) and `aliased-import`.
func selfTest() int {
	base, err := os.MkdirTemp("", "check-error-mappers-selftest")
	if err != nil {
		fmt.Printf("check-error-mappers: CANNOT LOOK — no scratch dir: %v\n", err)
		return 2
	}
	defer os.RemoveAll(base)

	const header = "package m\n\nimport (\n\t\"net/http\"\n\n\t%s\n)\n\nfunc writeJSON(w http.ResponseWriter, s int, v any) {}\nfunc errorBody(m string) any          { return nil }\n\n"
	realImport := `"` + canonicalPkg + `"`

	delegating := fmt.Sprintf(header, realImport) + `
func writeStoreError(w http.ResponseWriter, err error) {
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}
`
	cases := []struct {
		name   string
		want   int // expected exit code
		files  map[string]string
		expect string // substring the report must contain ("" = only the code matters)
	}{
		{
			name:  "delegates directly is clean",
			want:  0,
			files: map[string]string{"a/mapper.go": delegating},
		},
		{
			name: "delegates through a helper of its own package is clean",
			want: 0,
			files: map[string]string{"a/mapper.go": delegating, "a/depth.go": `package m

import "net/http"

func writeDepthError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	writeStoreError(w, err)
}
`},
		},
		{
			name: "a mapper that answers on its own authority is named",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/rogue.go": `package m

import "net/http"

func writeRogueError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
}
`},
			expect: "a/rogue.go:5: writeRogueError",
		},
		{
			// THE GREP TRAP. A gate that looked for the text would pass this file, and
			// the text is exactly what a hurried author leaves behind.
			name: "naming the mapper in a comment or a string does not count",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/pretend.go": `package m

import "net/http"

// writePretendError should call api.StoreErrorStatus one day.
func writePretendError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, errorBody("api.StoreErrorStatus"))
}
`},
			expect: "writePretendError",
		},
		{
			name: "an aliased import is still the canonical mapper",
			want: 0,
			files: map[string]string{"a/mapper.go": fmt.Sprintf(header, `coreapi `+realImport) + `
func writeStoreError(w http.ResponseWriter, err error) {
	status, msg, _ := coreapi.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}
`},
		},
		{
			// A DIFFERENT PACKAGE THAT HAPPENS TO BE CALLED api. Resolving the alias
			// from the import block rather than trusting the identifier is what
			// separates these two, and without this case that distinction is untested.
			name: "a same-named package from elsewhere is not the canonical mapper",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/impostor.go": `package m

import (
	"net/http"

	"example.com/somewhere/else/api"
)

func writeImpostorError(w http.ResponseWriter, err error) {
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}
`},
			expect: "writeImpostorError",
		},
		{
			// Same name, other package: reaching a helper is a PACKAGE-scoped question.
			name: "a helper of a different package does not launder the delegation",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "b/other.go": `package n

import "net/http"

func writeOtherError(w http.ResponseWriter, err error) {
	writeStoreError(w, err)
}

func writeStoreError(w http.ResponseWriter, err error) {}
`},
			expect: "b/other.go:5: writeOtherError",
		},
		{
			name: "a method with the family signature is judged too",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/method.go": `package m

import "net/http"

type Module struct{}

func (m *Module) writeThing(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
}
`},
			expect: "writeThing",
		},
		{
			// Terminates instead of hanging the gate, and is still red.
			name: "mutual recursion terminates and does not launder the delegation",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/loop.go": `package m

import "net/http"

func writeLoopA(w http.ResponseWriter, err error) { writeLoopB(w, err) }
func writeLoopB(w http.ResponseWriter, err error) { writeLoopA(w, err) }
`},
			expect: "writeLoopA",
		},
		{
			// A near-miss signature is NOT the family: three params, or a result.
			name: "a near-miss signature is not in the family",
			want: 0,
			files: map[string]string{"a/mapper.go": delegating, "a/near.go": `package m

import "net/http"

func writeErrorWithReq(w http.ResponseWriter, r *http.Request, err error) {
	writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
}

func writeAndReport(w http.ResponseWriter, err error) bool {
	writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
	return false
}
`},
		},
		{
			// _test.go is not shipped, and a test writing its own mapper is not a defect.
			name: "a mapper in a _test.go file is not in the family",
			want: 0,
			files: map[string]string{"a/mapper.go": delegating, "a/x_test.go": `package m

import "net/http"

func writeTestOnlyError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
}
`},
		},
		{
			// THE EVASION THAT MATCHES THIS REPOSITORY'S ARCHITECTURE, and the one the
			// external contrast broke the first version with. Two build-tag variants of
			// one function: the gate used to index by directory+name, so the second
			// declaration replaced the first and an enterprise writeStoreError that
			// answered 418 went green on its community sibling's credit.
			name: "a build-tag variant that does not delegate is judged on its own",
			want: 1,
			files: map[string]string{"a/mapper.go": "//go:build !enterprise\n\n" + delegating,
				"a/mapper_ent.go": `//go:build enterprise

package m

import "net/http"

func writeStoreError(w http.ResponseWriter, err error) {
	w.WriteHeader(418)
}
`},
			expect: "[enterprise]",
		},
		{
			// A call whose results go nowhere delegates nothing.
			name: "discarding every result is not delegation",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/discard.go": fmt.Sprintf(header, realImport) + `
func writeDiscardError(w http.ResponseWriter, err error) {
	api.StoreErrorStatus(err)
	writeJSON(w, 418, errorBody("teapot"))
}
`},
			expect: "writeDiscardError",
		},
		{
			// The compiler drops it, so it cannot be the delegation.
			name: "a call inside if false is not delegation",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/dead.go": fmt.Sprintf(header, realImport) + `
func writeDeadError(w http.ResponseWriter, err error) {
	if false {
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
	writeJSON(w, 418, errorBody("teapot"))
}
`},
			expect: "writeDeadError",
		},
		{
			// The identifier is not the package: a local of the same name shadows it.
			name: "a local variable shadowing the api package is not the api package",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/shadow.go": fmt.Sprintf(header, realImport) + `
type fake struct{}

func (fake) StoreErrorStatus(error) (int, string, bool) { return 418, "teapot", true }

func writeShadowError(w http.ResponseWriter, err error) {
	api := fake{}
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}
`},
			expect: "writeShadowError",
		},
		{
			// The family is defined by the TYPE, not by the spelling of its package.
			name: "an aliased net/http import is still the family",
			want: 1,
			files: map[string]string{"a/mapper.go": delegating, "a/hidden.go": `package m

import h "net/http"

func writeHiddenError(w h.ResponseWriter, err error) {
	w.WriteHeader(418)
}
`},
			expect: "writeHiddenError",
		},
		{
			// THE THIRD ANSWER. A tree with no mappers is not a clean tree.
			name:   "a tree with no mappers cannot be approved",
			want:   2,
			files:  map[string]string{"a/nothing.go": "package m\n\nfunc Nothing() {}\n"},
			expect: "CANNOT LOOK",
		},
		{
			name:   "a source file that does not parse cannot be approved",
			want:   2,
			files:  map[string]string{"a/mapper.go": delegating, "a/broken.go": "package m\n\nfunc oops( {\n"},
			expect: "CANNOT LOOK",
		},
	}

	pass, fail := 0, 0
	for i, tc := range cases {
		dir := filepath.Join(base, fmt.Sprintf("case%02d", i))
		bad := false
		for name, body := range tc.files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				fmt.Printf("  ERROR   %s: %v\n", tc.name, err)
				bad = true
				break
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				fmt.Printf("  ERROR   %s: %v\n", tc.name, err)
				bad = true
				break
			}
		}
		if bad {
			fail++
			continue
		}
		rc, report := check(dir)
		switch {
		case rc != tc.want:
			fmt.Printf("  FAIL    %s: exit %d, want %d\n%s\n", tc.name, rc, tc.want, indent(report))
			fail++
		case tc.expect != "" && !strings.Contains(report, strings.ReplaceAll(tc.expect, "a/", dir+"/a/")) &&
			!strings.Contains(report, tc.expect):
			fmt.Printf("  FAIL    %s: report does not mention %q\n%s\n", tc.name, tc.expect, indent(report))
			fail++
		default:
			fmt.Printf("  ok      %s (exit %d)\n", tc.name, rc)
			pass++
		}
	}

	// THE MISSING-TREE ANSWER, which needs no fixture.
	if rc, report := check(filepath.Join(base, "does-not-exist")); rc != 2 || !strings.Contains(report, "CANNOT LOOK") {
		fmt.Printf("  FAIL    a missing tree cannot be approved: exit %d\n%s\n", rc, indent(report))
		fail++
	} else {
		fmt.Printf("  ok      a missing tree cannot be approved (exit %d)\n", rc)
		pass++
	}

	fmt.Printf("check-error-mappers --self-test: %d passed, %d failed\n", pass, fail)
	if fail > 0 {
		return 1
	}
	return 0
}

func indent(s string) string {
	return "      " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n      ")
}
