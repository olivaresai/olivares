// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// sourcescopeDestructiveVerbs. `disable-scoping` is in this list even though it
// deletes nothing: it removes the confinement that decided who could reach a
// source, which is the destruction that matters in this family. A census by HTTP
// method never finds it.
var sourcescopeDestructiveVerbs = []struct {
	name string
	args []string
	path string
}{
	{"bindings rm", []string{"sourcescope", "bindings", "rm", "bnd_1"}, "/v1/m/sourcescope/bindings/bnd_1"},
	{"assignments rm", []string{"sourcescope", "assignments", "rm", "asg_1"}, "/v1/m/sourcescope/assignments/asg_1"},
	{"workspace-connectors rm", []string{"sourcescope", "workspace-connectors", "rm", "wc_1"}, "/v1/m/sourcescope/workspace-connectors/wc_1"},
	{"sources disable-scoping", []string{"sourcescope", "sources", "disable-scoping", "--source-type", "knowledge", "--source-ref", "kb_1"}, "/v1/m/sourcescope/sources/disable-scoping"},
	{"posture-requests approve", []string{"sourcescope", "posture-requests", "approve", "pr_1"}, "/v1/m/sourcescope/posture-requests/pr_1/approve"},
}

// TestSourceScopeWideningVerbsRefuseUnattendedConsent: DENY with a request
// count, then the POSITIVE CONTROL in the same subtest.
func TestSourceScopeWideningVerbsRefuseUnattendedConsent(t *testing.T) {
	for _, verb := range sourcescopeDestructiveVerbs {
		t.Run(verb.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"pr_1","status":"pending"}`)

			_, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...)
			if err == nil {
				t.Fatalf("%s without --yes must fail", verb.name)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if got := rec.count(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}

			args := append(append([]string{}, verb.args...), "--yes")
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); err != nil {
				t.Fatalf("%s --yes must succeed: %v", verb.name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests with --yes = %d, want 1", got)
			}
			if got := rec.last(t).Path; got != verb.path {
				t.Errorf("path = %q, want %q", got, verb.path)
			}
		})
	}
}

// TestSourceScopeRejectDoesNotAskForConsent is the asymmetry that proves the
// guard is about EFFECT and not about the word "decision": approving applies a
// relaxation, rejecting applies nothing. Making both ask would teach operators
// that --yes is ceremony.
func TestSourceScopeRejectDoesNotAskForConsent(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"pr_1","status":"rejected"}`)
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "posture-requests", "reject", "pr_1")...); err != nil {
		t.Fatalf("reject must not require consent: %v", err)
	}
	if got := rec.last(t).Path; got != "/v1/m/sourcescope/posture-requests/pr_1/reject" {
		t.Errorf("path = %q", got)
	}
	root := newRootCmd()
	reject := resolveCommandPath(t, root, "olivares sourcescope posture-requests reject")
	if reject == nil {
		t.Fatal("reject does not resolve")
	}
	if reject.Flags().Lookup("yes") != nil {
		t.Error("reject must not carry --yes: it changes nothing")
	}
	// POSITIVE CONTROL for the assertion above: approve DOES carry it.
	approve := resolveCommandPath(t, root, "olivares sourcescope posture-requests approve")
	if approve == nil || approve.Flags().Lookup("yes") == nil {
		t.Error("approve must carry --yes, or the check above proves nothing")
	}
}

// TestSourceScopeDisableScopingCarriesTheSourceAndIsProposed asserts the wire
// AND the 202 semantics of the most widening operation in the lane.
func TestSourceScopeDisableScopingCarriesTheSourceAndIsProposed(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusAccepted,
		`{"id":"pr_9","status":"pending","op":"disable_scoping","reason":"one-way relaxation"}`)

	out, errb, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "sources", "disable-scoping",
		"--source-type", "knowledge", "--source-ref", "kb_123", "--yes")...)
	// Un 202 sale DEGRADED a propósito, y el propio cmd_datalane.go:405-416 lo razona: «un cero
	// indebido es la forma más cara de esta familia, porque el llamante carries on». La petición
	// se aceptó y NO está en efecto; el test se alinea con ese diseño en vez de exigir un 0 que
	// haría que un script diese por aplicada una propuesta pendiente.
	if err == nil {
		t.Fatal("un 202 debe salir DEGRADED, no 0: una propuesta pendiente no está en efecto")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	req := rec.last(t)
	if req.Method != http.MethodPost || req.Path != "/v1/m/sourcescope/sources/disable-scoping" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	body := rec.jsonBody(t)
	if body["source_type"] != "knowledge" || body["source_ref"] != "kb_123" {
		t.Fatalf("body = %#v", body)
	}
	if !strings.Contains(errb, "NOT in effect") {
		t.Errorf("a 202 must be announced as not-yet-applied, stderr = %q", errb)
	}
	if !strings.Contains(out, "pending") || !strings.Contains(out, "pr_9") {
		t.Errorf("stdout must carry the pending request a reviewer will act on:\n%s", out)
	}
}

// TestSourceScopeDisableScopingRefusesAnIncompleteTargetBeforeConnecting: an
// argument missing on THIS verb must not become a request the engine has to
// reject, because the verb is the one an operator least wants to fire twice.
func TestSourceScopeDisableScopingRefusesAnIncompleteTargetBeforeConnecting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusAccepted, `{"id":"pr_1"}`)
	for _, args := range [][]string{
		{"sourcescope", "sources", "disable-scoping", "--yes"},
		{"sourcescope", "sources", "disable-scoping", "--source-type", "knowledge", "--yes"},
		{"sourcescope", "sources", "disable-scoping", "--source-ref", "kb_1", "--yes"},
	} {
		_, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...)
		if err == nil {
			t.Fatalf("%v must fail", args)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("%v: exit = %d, want %d", args, got, exitcode.Usage)
		}
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

// TestSourceScopeBindingSetRefusesAPartialReplace: PUT /bindings/{id}
// re-resolves the scope from the payload and rewrites the row, so an omitted
// field is not "unchanged" — it is reset, and resetting a scope MOVES the
// binding.
func TestSourceScopeBindingSetRefusesAPartialReplace(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"bnd_1"}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "bindings", "set", "bnd_1", "--effect", "allow")...)
	if err == nil {
		t.Fatal("a partial replace must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
	if !strings.Contains(err.Error(), "--scope-tree") {
		t.Errorf("the refusal must name the scope fields that would be reset: %v", err)
	}

	// The guard must NOT demand the fields the control plane forces back from the
	// stored row. Asking for --source-type/--source-ref would teach an operator
	// that passing them matters, when handleUpdateBinding discards them.
	if strings.Contains(err.Error(), "--source-type") || strings.Contains(err.Error(), "--source-ref") {
		t.Errorf("the guard must not demand the immutable natural key, which the engine "+
			"overwrites from the stored row: %v", err)
	}

	// POSITIVE CONTROL, both ways out of the guard.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "bindings", "set", "bnd_1", "--effect", "allow", "--replace")...); err != nil {
		t.Fatalf("--replace must be accepted: %v", err)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "bindings", "set", "bnd_1",
		"--scope-tree", "workspace", "--scope-ref", "ws-1",
		"--effect", "forbid", "--enabled", "--note", "tightened",
		"--cred-name", "wiki", "--cred-ref-kind", "secret", "--cred-ref", "sec/wiki",
		"--cred-hint", "svc-wiki")...); err != nil {
		t.Fatalf("a complete replace must be accepted without --replace: %v", err)
	}
	body := rec.jsonBody(t)
	if body["scope_tree"] != "workspace" || body["effect"] != "forbid" || body["enabled"] != true {
		t.Fatalf("body = %#v", body)
	}
	if body["cred_ref"] != "sec/wiki" {
		t.Fatalf("the credential locator must reach the engine: %#v", body["cred_ref"])
	}
}

// TestSourceScopeReplaceGuardsCoverExactlyWhatTheEndpointRewrites is the control
// on the control. A guard that demands a field the engine ignores is noise an
// operator learns to bypass with --replace; a guard that omits a field the
// engine DOES rewrite is the silent data loss it exists to prevent. Both lists
// were read off the handlers, so both directions are asserted here.
func TestSourceScopeReplaceGuardsCoverExactlyWhatTheEndpointRewrites(t *testing.T) {
	cases := []struct {
		path     string
		rewrites []string // the engine writes these from the payload
		forced   []string // the engine forces these back from the stored row
	}{
		{
			"sourcescope assignments set",
			[]string{"--enabled", "--note"},
			[]string{"--connector-name", "--workspace-ref", "--mode"},
		},
		{
			"sourcescope workspace-connectors set",
			[]string{"--config", "--poll-seconds", "--enabled", "--note"},
			[]string{"--name", "--kind", "--workspace-ref", "--secrets-file"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"x"}`)
			args := append(strings.Fields(tc.path), "id_1")
			_, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...)
			if err == nil {
				t.Fatal("a bare set must be refused")
			}
			if got := rec.count(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
			for _, want := range tc.rewrites {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the guard omits %s, which the engine DOES rewrite: %v", want, err)
				}
			}
			for _, unwanted := range tc.forced {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("the guard demands %s, which the engine forces back from the "+
						"stored row and cannot lose: %v", unwanted, err)
				}
			}
		})
	}
}

// TestSourceScopeWorkspaceConnectorSecretsNeverComeFromAnArgument. A credential
// passed on a command line is readable in the process table by every user on the
// machine, so this family offers no --secret flag at all — the absence IS the
// control, and the positive control below proves the capability still exists
// through a file.
func TestSourceScopeWorkspaceConnectorSecretsNeverComeFromAnArgument(t *testing.T) {
	root := newRootCmd()
	for _, path := range []string{
		"olivares sourcescope workspace-connectors create",
		"olivares sourcescope workspace-connectors set",
	} {
		cmd := resolveCommandPath(t, root, path)
		if cmd == nil {
			t.Fatalf("%s does not resolve", path)
		}
		// POSITIVE CONTROL FIRST: without --secrets-file the absence below is
		// trivially true and would prove nothing.
		if cmd.Flags().Lookup("secrets-file") == nil {
			t.Fatalf("%s must be able to carry secrets from a FILE", path)
		}
		for _, banned := range []string{"secret", "secrets"} {
			if f := cmd.Flags().Lookup(banned); f != nil {
				t.Errorf("%s must not accept --%s: a credential in argv is readable by every "+
					"user on the box", path, banned)
			}
		}
	}

	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"id":"wc_1"}`)
	file := filepath.Join(t.TempDir(), "secrets.json")
	if err := writeTestFile(file, `{"token":"s3cr3t"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "workspace-connectors", "create",
		"--name", "docs", "--kind", "confluence", "--workspace-ref", "ws-1",
		"--config", "base_url=https://wiki.example.com", "--secrets-file", file, "--enabled")...); err != nil {
		t.Fatalf("workspace-connectors create: %v", err)
	}
	body := rec.jsonBody(t)
	secrets, ok := body["secrets"].(map[string]any)
	if !ok || secrets["token"] != "s3cr3t" {
		t.Fatalf("secrets did not reach the engine: %#v", body["secrets"])
	}
	config, ok := body["config"].(map[string]any)
	if !ok || config["base_url"] != "https://wiki.example.com" {
		t.Fatalf("config = %#v", body["config"])
	}
	if body["enabled"] != true {
		t.Fatalf("enabled = %#v", body["enabled"])
	}
}

// TestSourceScopeConfigPairsMustBeKeyValue: guessing what `--config base_url`
// meant would put an operator's typo into a stored connector configuration.
//
// The BLANK-KEY spelling is here because it had no witness and is the one a shell
// produces by accident: `--config "$KEY=$VALUE"` with KEY unset becomes "=value",
// which datalaneKeyValues refuses and which — measured — the rest of the lot did
// not notice being accepted. A connector configuration with a "" key is a row the
// operator cannot name afterwards to correct or remove.
//
// The POSITIVE CONTROL was missing too: a refusal test with no counter-example is
// also passed by a command that refuses every --config it is given.
func TestSourceScopeConfigPairsMustBeKeyValue(t *testing.T) {
	for _, bad := range []struct{ label, pair string }{
		{"no separator", "base_url"},
		{"blank key", "=https://example.com"},
		{"whitespace key", "   =https://example.com"},
	} {
		t.Run(bad.label, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusCreated, `{"id":"wc_1"}`)
			_, _, err := execDatalane(t, "", datalaneArgs(rec,
				"sourcescope", "workspace-connectors", "create",
				"--name", "docs", "--kind", "confluence", "--workspace-ref", "ws-1",
				"--config", bad.pair)...)
			if err == nil {
				t.Fatalf("--config %q must be refused", bad.pair)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d", got, exitcode.Usage)
			}
			if got := rec.count(); got != 0 {
				t.Errorf("requests = %d, want 0", got)
			}
		})
	}

	t.Run("a real pair reaches the engine", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusCreated, `{"id":"wc_1"}`)
		if _, _, err := execDatalane(t, "", datalaneArgs(rec,
			"sourcescope", "workspace-connectors", "create",
			"--name", "docs", "--kind", "confluence", "--workspace-ref", "ws-1",
			"--config", "base_url=https://example.com", "--config", "space=ENG")...); err != nil {
			t.Fatalf("a well-formed --config must be accepted: %v", err)
		}
		config, ok := rec.jsonBody(t)["config"].(map[string]any)
		if !ok {
			t.Fatalf("config = %#v, want an object", rec.jsonBody(t)["config"])
		}
		if config["base_url"] != "https://example.com" || config["space"] != "ENG" {
			t.Errorf("config = %#v, want both pairs the caller named", config)
		}
	})
}

// TestSourceScopeSecretsFileMustBeAJSONObjectOfStrings: a nested value would be
// silently dropped by the module's decoder shape, so the CLI refuses it here
// with the file named.
func TestSourceScopeSecretsFileMustBeAJSONObjectOfStrings(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"id":"wc_1"}`)
	file := filepath.Join(t.TempDir(), "secrets.json")
	if err := writeTestFile(file, `{"token":{"nested":true}}`); err != nil {
		t.Fatal(err)
	}
	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "workspace-connectors", "create",
		"--name", "docs", "--kind", "confluence", "--workspace-ref", "ws-1",
		"--secrets-file", file)...)
	if err == nil {
		t.Fatal("a non-string secret value must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

// TestSourceScopeResolveRequiresTheWholeQuestion: a preview missing one of its
// four coordinates is not a narrower question, it is a different one — and the
// module answers 400. Refusing locally keeps the exit code a usage error.
func TestSourceScopeResolveRequiresTheWholeQuestion(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"decision":"allow"}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "resolve", "--source-type", "knowledge", "--source-ref", "kb_1")...)
	if err == nil {
		t.Fatal("resolve without an actor must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "resolve", "--source-type", "knowledge", "--source-ref", "kb_1",
		"--actor-kind", "agent", "--actor-ref", "agent-1")...); err != nil {
		t.Fatalf("a complete resolve must succeed: %v", err)
	}
	q := rec.last(t).Query
	for key, want := range map[string]string{
		"source_type": "knowledge", "source_ref": "kb_1",
		"actor_kind": "agent", "actor_ref": "agent-1",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
}

// TestSourceScopeEmptyListsSayWhatEmptyMeans. "No assignments" does NOT mean no
// connector is reachable — with no rows at all a connector is visible
// tenant-wide — and an empty table that says nothing invites the opposite
// reading.
func TestSourceScopeEmptyListsSayWhatEmptyMeans(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sourcescope", "assignments", "ls"}, "visible tenant-wide"},
		{[]string{"sourcescope", "bindings", "ls"}, "stay tenant-wide"},
		{[]string{"sourcescope", "guard-postures", "ls"}, "ACL-aware"},
	} {
		out, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%v printed %q, want it to explain what an empty list means (%q)",
				tc.args, out, tc.want)
		}
	}
}

// TestSourceScopeGuardPostureSetReachesThePolicyRoute pins the route and body of
// the axis that decides whether retrieval stays ACL-aware.
func TestSourceScopeGuardPostureSetReachesThePolicyRoute(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusAccepted, `{"id":"pr_2","status":"pending"}`)
	_, errb, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "guard-postures", "set",
		"--source-ref", "kb_1", "--profile", "public_only", "--reason", "public FAQ")...)
	if err == nil {
		t.Fatal("un 202 debe salir DEGRADED, no 0 (ver cmd_datalane.go:405-416)")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	req := rec.last(t)
	if req.Method != http.MethodPut || req.Path != "/v1/m/sourcescope/guard-postures" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	body := rec.jsonBody(t)
	if body["source_ref"] != "kb_1" || body["profile"] != "public_only" || body["reason"] != "public FAQ" {
		t.Fatalf("body = %#v", body)
	}
	if !strings.Contains(errb, "NOT in effect") {
		t.Errorf("a relaxing posture change is 202 and must be announced as such: %q", errb)
	}
}
