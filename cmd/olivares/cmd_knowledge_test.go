// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// knowledgeDestructiveVerbs are every verb of this family that destroys or
// withdraws something. FOUR of them are POST, not DELETE — a census by HTTP
// method misses exactly those, and they are the ones an operator is least
// expecting to be irreversible.
var knowledgeDestructiveVerbs = []struct {
	name string
	args []string
	path string
}{
	{"kbs rm", []string{"knowledge", "kbs", "rm", "kb_1"}, "/v1/m/knowledge/kbs/kb_1"},
	{"memory rm", []string{"knowledge", "memory", "rm", "mem_1"}, "/v1/m/knowledge/memory/mem_1"},
	{"memory purge", []string{"knowledge", "memory", "purge"}, "/v1/m/knowledge/memory/purge"},
	{"dlp rm", []string{"knowledge", "dlp", "rm", "dlp_1"}, "/v1/m/knowledge/dlp/rules/dlp_1"},
	{"data-products rm", []string{"knowledge", "data-products", "rm", "dp_1"}, "/v1/m/knowledge/data-products/dp_1"},
	{"data-products deprecate", []string{"knowledge", "data-products", "deprecate", "dp_1"}, "/v1/m/knowledge/data-products/dp_1/deprecate"},
	{"data-products archive", []string{"knowledge", "data-products", "archive", "dp_1"}, "/v1/m/knowledge/data-products/dp_1/archive"},
	{"prompts rollback", []string{"knowledge", "prompts", "rollback", "pr_1", "--rev", "2"}, "/v1/m/knowledge/prompts/pr_1/rollback"},
}

// TestKnowledgeDestructiveVerbsRefuseUnattendedConsent is the DENY half of the
// consent control, with its POSITIVE CONTROL in the same subtest: without --yes
// on a non-interactive stdin the verb exits 2 and NOTHING reaches the control
// plane; with --yes exactly one request arrives, at the right path.
func TestKnowledgeDestructiveVerbsRefuseUnattendedConsent(t *testing.T) {
	for _, verb := range knowledgeDestructiveVerbs {
		t.Run(verb.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"deleted":true}`)

			_, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...)
			if err == nil {
				t.Fatalf("%s without --yes must fail", verb.name)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if got := rec.count(); got != 0 {
				t.Fatalf("requests = %d, want 0: an unconfirmed destructive verb must not reach the engine", got)
			}

			// POSITIVE CONTROL.
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

// TestKnowledgeReadVerbsDoNotAskForConsent is the other side of the same
// control: a read must NOT have grown a confirmation, or operators learn to
// pass --yes everywhere and the guard stops meaning anything.
func TestKnowledgeReadVerbsDoNotAskForConsent(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)
	for _, args := range [][]string{
		{"knowledge", "kbs", "ls"},
		{"knowledge", "memory", "ls"},
		{"knowledge", "dlp", "ls"},
		{"knowledge", "data-products", "ls"},
		{"knowledge", "lineage", "ls"},
	} {
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); err != nil {
			t.Errorf("%s must not require consent: %v", strings.Join(args, " "), err)
		}
	}
}

// TestKnowledgeKBCreateReachesTheEngineWithTheAuthoredFields is the ALLOW half
// asserted ON THE WIRE — method, path, body and the tenant header — rather than
// only on the exit code. A command that exits 0 while sending the wrong document
// is the failure this catches.
func TestKnowledgeKBCreateReachesTheEngineWithTheAuthoredFields(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated,
		`{"id":"kb_9","name":"handbook","classification":"confidential","embed_model":"local-hash"}`)

	out, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "create", "--name", "handbook",
		"--classification", "confidential", "--residency-region", "eu",
		"--embed-policy", "local_only", "--acl", "team:support", "--acl", "team:legal")...)
	if err != nil {
		t.Fatalf("kbs create: %v", err)
	}
	req := rec.last(t)
	if req.Method != http.MethodPost || req.Path != "/v1/m/knowledge/kbs" {
		t.Fatalf("request = %s %s, want POST /v1/m/knowledge/kbs", req.Method, req.Path)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body := rec.jsonBody(t)
	if body["name"] != "handbook" || body["classification"] != "confidential" ||
		body["residency_region"] != "eu" || body["embed_policy"] != "local_only" {
		t.Fatalf("body = %#v, want the authored fields", body)
	}
	acl, ok := body["default_acl"].([]any)
	if !ok || len(acl) != 2 || acl[0] != "team:support" {
		t.Fatalf("default_acl = %#v, want both repeated --acl values", body["default_acl"])
	}
	// The response is rendered, so an operator sees the id a script needs next.
	if !strings.Contains(out, "kb_9") {
		t.Errorf("text output must carry the new id:\n%s", out)
	}
}

// TestKnowledgeKBCreateRefusesWithoutANameBeforeConnecting: a required field
// missing is the caller's error and costs no round trip.
func TestKnowledgeKBCreateRefusesWithoutANameBeforeConnecting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"id":"kb_9"}`)
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "create")...)
	if err == nil {
		t.Fatal("kbs create without --name must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "create", "--name", "x")...); err != nil {
		t.Fatalf("with --name it must succeed: %v", err)
	}
}

// TestKnowledgeDataProductSetSendsOnlyTheFieldsTheCallerNamed pins the ONE
// asymmetry in this family that a reader would otherwise have to guess: this PUT
// is a genuine patch, so the CLI must not fill the gaps with zero values. If it
// did, `--enforcement-mode strict` would also blank the owner and the SLA.
func TestKnowledgeDataProductSetSendsOnlyTheFieldsTheCallerNamed(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"dp_1"}`)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "data-products", "set", "dp_1", "--enforcement-mode", "strict")...); err != nil {
		t.Fatalf("data-products set: %v", err)
	}
	req := rec.last(t)
	if req.Method != http.MethodPut || req.Path != "/v1/m/knowledge/data-products/dp_1" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	body := rec.jsonBody(t)
	if body["enforcement_mode"] != "strict" {
		t.Fatalf("enforcement_mode = %#v", body["enforcement_mode"])
	}
	for _, absent := range []string{"name", "owner_ref", "kb_ref", "freshness_sla_seconds", "quality_score", "tags"} {
		if _, present := body[absent]; present {
			t.Errorf("%q was sent although the caller never named it: a patch that fills gaps "+
				"silently overwrites stored governance (body %#v)", absent, body)
		}
	}

	// POSITIVE CONTROL: a field the caller DOES name is sent, including a zero
	// value, which is a real intention and must not be mistaken for "unset".
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "data-products", "set", "dp_1", "--freshness-sla-seconds", "0")...); err != nil {
		t.Fatalf("data-products set --freshness-sla-seconds 0: %v", err)
	}
	body = rec.jsonBody(t)
	if got, present := body["freshness_sla_seconds"]; !present || got != float64(0) {
		t.Fatalf("an explicit zero must be sent, got %#v (present=%v)", got, present)
	}
}

// TestKnowledgeMemoryPutKeepsTheContentOffTheCommandLine: a memory entry can
// carry personal data, so --content-file (and `-` for stdin) must work and the
// value must arrive intact.
func TestKnowledgeMemoryPutKeepsTheContentOffTheCommandLine(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"mem_1"}`)

	file := filepath.Join(t.TempDir(), "content.txt")
	if err := writeTestFile(file, "the customer prefers email\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "memory", "put", "--agent-ref", "agent-1", "--key", "prefs",
		"--content-file", file, "--ttl-seconds", "3600")...); err != nil {
		t.Fatalf("memory put: %v", err)
	}
	body := rec.jsonBody(t)
	if body["content"] != "the customer prefers email" {
		t.Fatalf("content = %#v, want the file's contents with the trailing newline trimmed", body["content"])
	}
	if body["agent_ref"] != "agent-1" || body["key"] != "prefs" || body["ttl_seconds"] != float64(3600) {
		t.Fatalf("body = %#v", body)
	}
	// An UNDECLARED scope must be absent, not blank: the module rejects a declared
	// blank on purpose, and the two are different facts.
	for _, absent := range []string{"user_ref", "session_ref"} {
		if _, present := body[absent]; present {
			t.Errorf("%q must be absent when the caller did not declare it, body %#v", absent, body)
		}
	}

	// Stdin path, and a DECLARED scope.
	if _, _, err := execDatalane(t, "from stdin", datalaneArgs(rec,
		"knowledge", "memory", "put", "--agent-ref", "agent-1", "--key", "k",
		"--content-file", "-", "--user-ref", "u-9")...); err != nil {
		t.Fatalf("memory put --content-file -: %v", err)
	}
	body = rec.jsonBody(t)
	if body["content"] != "from stdin" || body["user_ref"] != "u-9" {
		t.Fatalf("body = %#v", body)
	}
}

// TestKnowledgeMemoryExportIsForwardedByteForByte: the bundle is signed NDJSON.
// Re-encoding it would break the verification it exists for, so both output
// modes must hand back exactly what the control plane sent.
func TestKnowledgeMemoryExportIsForwardedByteForByte(t *testing.T) {
	prepareDatalaneCLITest(t)
	const bundle = "{\"schema\":\"memport.v1\",\"signature\":\"AAAA\"}\n{\"key\":\"a\"}\n"
	rec := newDatalaneRecorder(t, http.StatusOK, bundle)

	out, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "memory", "export")...)
	if err != nil {
		t.Fatalf("memory export: %v", err)
	}
	if out != bundle {
		t.Fatalf("stdout = %q, want the bundle unchanged (%q)", out, bundle)
	}
	jsonOut, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "memory", "export", "-o", "json")...)
	if err != nil {
		t.Fatalf("memory export -o json: %v", err)
	}
	if jsonOut != bundle {
		t.Fatalf("-o json reshaped a signed bundle: %q", jsonOut)
	}

	// --out writes the file and keeps stdout clean for a pipeline.
	dest := filepath.Join(t.TempDir(), "memory.ndjson")
	out, errb, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "memory", "export", "--out", dest)...)
	if err != nil {
		t.Fatalf("memory export --out: %v", err)
	}
	if out != "" {
		t.Errorf("stdout must be empty when --out is given, got %q", out)
	}
	if !strings.Contains(errb, dest) {
		t.Errorf("stderr must name the file written, got %q", errb)
	}
	written, err := readTestFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if written != bundle {
		t.Fatalf("written file = %q, want the bundle unchanged", written)
	}
}

// TestKnowledgeQueryRefusesAnEmptyRetrievalBeforeConnecting: an empty query is
// not a retrieval, and sending one would write a lineage record for a question
// nobody asked.
func TestKnowledgeQueryRefusesAnEmptyRetrievalBeforeConnecting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"lineage_id":"ln_1","count":0}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "query", "kb_1")...)
	if err == nil {
		t.Fatal("a query with no text must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}

	// POSITIVE CONTROL: a real query reaches the engine with its top_k.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "query", "kb_1", "--query", "expenses policy", "--top-k", "5")...); err != nil {
		t.Fatalf("kbs query: %v", err)
	}
	req := rec.last(t)
	if req.Method != http.MethodPost || req.Path != "/v1/m/knowledge/kbs/kb_1/query" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	body := rec.jsonBody(t)
	if body["query"] != "expenses policy" || body["top_k"] != float64(5) {
		t.Fatalf("body = %#v", body)
	}
	// agent_ref is accepted by the module but IGNORED for authorization, so this
	// CLI never offers a way to send one: a caller must not appear to be able to
	// name a privileged agent.
	if _, present := body["agent_ref"]; present {
		t.Errorf("the CLI must not send agent_ref: authorization comes from the authenticated identity")
	}
}

// TestKnowledgeIngestRequiresExactlyOneSource: --source and inline documents are
// two different ingests, and guessing between them would be a governance
// decision taken by a CLI.
func TestKnowledgeIngestRequiresExactlyOneSource(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"docs_synced":1}`)

	for _, args := range [][]string{
		{"knowledge", "kbs", "ingest", "kb_1"},
		{"knowledge", "kbs", "ingest", "kb_1", "--source", "confluence", "--documents", `[{"source_doc_id":"1","body":"x"}]`},
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

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "ingest", "kb_1", "--source", "confluence")...); err != nil {
		t.Fatalf("ingest --source: %v", err)
	}
	body := rec.jsonBody(t)
	if body["source"] != "confluence" {
		t.Fatalf("body = %#v", body)
	}
	if _, present := body["documents"]; present {
		t.Errorf("documents must be absent when the caller pulled a source, body %#v", body)
	}
}

// TestKnowledgeListsRenderTheirOwnEmptyNote: an empty page must SAY it is empty.
// A naked loop prints zero bytes and exits 0, which on a fresh install is
// indistinguishable from a command that did nothing.
func TestKnowledgeListsRenderTheirOwnEmptyNote(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"knowledge", "kbs", "ls"}, "no knowledge bases"},
		{[]string{"knowledge", "dlp", "ls"}, "deny-closed"},
		{[]string{"knowledge", "memory", "ls"}, "no memory entries"},
		{[]string{"knowledge", "scans", "ls"}, "no scans"},
	} {
		out, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%v printed %q, want it to name the empty case (%q)", tc.args, out, tc.want)
		}
	}
}

// TestKnowledgeContractVersionMustBeANumber keeps a mistyped version out of the
// path: `contracts get dp_1 latest` would otherwise become a 404 that reads like
// a missing contract rather than a typo.
func TestKnowledgeContractVersionMustBeANumber(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"version":2}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "data-products", "contracts", "get", "dp_1", "latest")...)
	if err == nil {
		t.Fatal("a non-numeric contract version must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "data-products", "contracts", "get", "dp_1", "2")...); err != nil {
		t.Fatalf("a numeric version must be accepted: %v", err)
	}
	if got := rec.last(t).Path; got != "/v1/m/knowledge/data-products/dp_1/contracts/2" {
		t.Errorf("path = %q", got)
	}
}

// TestKnowledgeMemoryVerbsAddressTheirOwnRoute pins, on the wire, WHICH route
// each memory verb calls.
//
// Nothing else in this suite did, and two mutants proved it. `ls` and `all` are
// built by ONE constructor that differs only in its path argument
// (newKnowledgeMemoryListCmd), so pointing `all` at "/memory" compiled, kept
// every test green, and turned the admin-tier CROSS-SCOPE governance view into
// the caller-scoped list — an auditor would read a filtered page as "every entry
// in the tenant". Pointing `export` at "/memory" also kept every test green:
// TestKnowledgeMemoryExportIsForwardedByteForByte asserts that whatever the
// server returns is forwarded unchanged, which stays true when the CLI asks the
// WRONG endpoint, so the operator would receive an ordinary list page in a file
// named like a signed bundle — and the engine's fail-closed export audit, the
// only trace of that egress, would never be written because the export route was
// never called.
//
// The pair (ls, all) is asserted to be DIFFERENT explicitly: equal paths are the
// exact failure being excluded, and two independent equality checks against the
// same constant would both still pass if someone edited the constant.
func TestKnowledgeMemoryVerbsAddressTheirOwnRoute(t *testing.T) {
	bundleFile := filepath.Join(t.TempDir(), "bundle.ndjson")
	if err := writeTestFile(bundleFile, "{\"schema\":\"memport.v1\"}\n{\"key\":\"a\"}\n"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		args   []string
		method string
		path   string
	}{
		{"ls", []string{"knowledge", "memory", "ls"}, http.MethodGet, "/v1/m/knowledge/memory"},
		{"all", []string{"knowledge", "memory", "all"}, http.MethodGet, "/v1/m/knowledge/memory/all"},
		{"get", []string{"knowledge", "memory", "get", "mem_1"}, http.MethodGet, "/v1/m/knowledge/memory/mem_1"},
		{"put", []string{"knowledge", "memory", "put", "--agent-ref", "a1", "--key", "k1", "--content", "c"},
			http.MethodPost, "/v1/m/knowledge/memory"},
		{"rm", []string{"knowledge", "memory", "rm", "mem_1", "--yes"},
			http.MethodDelete, "/v1/m/knowledge/memory/mem_1"},
		{"purge", []string{"knowledge", "memory", "purge", "--yes"},
			http.MethodPost, "/v1/m/knowledge/memory/purge"},
		{"verify", []string{"knowledge", "memory", "verify"},
			http.MethodPost, "/v1/m/knowledge/memory/verify"},
		{"export", []string{"knowledge", "memory", "export"},
			http.MethodGet, "/v1/m/knowledge/memory/export"},
		{"import", []string{"knowledge", "memory", "import", "--bundle-file", bundleFile},
			http.MethodPost, "/v1/m/knowledge/memory/import"},
	}
	seen := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false,"id":"mem_1"}`)
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...); err != nil {
				t.Fatalf("memory %s: %v", tc.name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
			last := rec.last(t)
			if last.Method != tc.method || last.Path != tc.path {
				t.Errorf("memory %s asked %s %s, want %s %s",
					tc.name, last.Method, last.Path, tc.method, tc.path)
			}
			seen[tc.name] = last.Method + " " + last.Path
		})
	}
	if seen["ls"] == seen["all"] {
		t.Errorf("`memory ls` and `memory all` addressed the SAME route (%s): the admin-tier "+
			"cross-scope view would silently return the caller-scoped page", seen["ls"])
	}
	if seen["export"] == seen["ls"] {
		t.Errorf("`memory export` addressed the list route (%s): a list page would be written "+
			"as if it were the signed portability bundle", seen["export"])
	}
}

// TestKnowledgeRawJSONPreservesFieldsTheCLIDoesNotModel: the text table is a
// projection, so a field this CLI has no column for must still survive -o json.
func TestKnowledgeRawJSONPreservesFieldsTheCLIDoesNotModel(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK,
		`{"items":[{"id":"kb_1","name":"n","future_field":"kept"}],"has_more":false,"request_id":"req-7"}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("kbs ls -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if decoded["request_id"] != "req-7" {
		t.Errorf("a top-level field the CLI does not model was dropped: %#v", decoded)
	}
	items, _ := decoded["items"].([]any)
	first, _ := items[0].(map[string]any)
	if first["future_field"] != "kept" {
		t.Errorf("an item field the CLI does not model was dropped: %#v", first)
	}
}
