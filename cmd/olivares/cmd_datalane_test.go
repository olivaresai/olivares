// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The governed-data lane's test harness.
//
// EVERY "the command refused" assertion here is paired with a REQUEST COUNT and
// with the positive control that the same command works when the missing thing
// is supplied. Without the count, a command that is simply broken passes a
// refusal test; without the positive control, so does one that refuses
// everything.

type datalaneRequest struct {
	Method string
	// Path is what net/http hands a handler: the DECODED path.
	Path string
	// Escaped is what actually traveled: r.URL.EscapedPath(). The two differ
	// exactly when an argument contained a separator, which is the case the
	// escaping control exists for — and asserting on Path there would measure the
	// wrong guard, since %2F is decoded back to / before a handler ever sees it.
	// chi routes on RawPath when it is set, so Escaped is also what the engine's
	// router matches.
	Escaped string
	Query   url.Values
	Body    []byte
	Header  http.Header
}

// datalaneRecorder is a control plane that records what reached it and answers
// what the test told it to.
type datalaneRecorder struct {
	mu       sync.Mutex
	requests []datalaneRequest
	status   int
	body     string
	// respond, when set, overrides status/body per request.
	respond func(r datalaneRequest) (int, string)
	server  *httptest.Server
}

func newDatalaneRecorder(t *testing.T, status int, body string) *datalaneRecorder {
	t.Helper()
	rec := &datalaneRecorder{status: status, body: body}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The recorder's own bound must sit ABOVE the bound under test. At 1 MiB it
		// equals maxDatalaneResponseSize, so a body carrying a payload AT that size
		// was recorded truncated and the instrument could no longer tell a whole
		// request from a clipped one — the boundary case is exactly where the
		// measurement matters.
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		req := datalaneRequest{
			Method: r.Method, Path: r.URL.Path, Escaped: r.URL.EscapedPath(),
			Query: r.URL.Query(), Body: raw, Header: r.Header.Clone(),
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, req)
		respond := rec.respond
		status, body := rec.status, rec.body
		rec.mu.Unlock()
		if respond != nil {
			status, body = respond(req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *datalaneRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *datalaneRecorder) last(t *testing.T) datalaneRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("no request reached the control plane")
	}
	return r.requests[len(r.requests)-1]
}

func (r *datalaneRecorder) jsonBody(t *testing.T) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(r.last(t).Body, &decoded); err != nil {
		t.Fatalf("request body is not a JSON object: %v\n%s", err, r.last(t).Body)
	}
	return decoded
}

// prepareDatalaneCLITest isolates the resolution order: no config file, no
// environment, so only the flags under test can supply a server or credential.
func prepareDatalaneCLITest(t *testing.T) {
	t.Helper()
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
}

// execDatalane runs the real root command with a NON-TERMINAL stdin, so the
// unattended branch of confirmDestructive is the one under test and the result
// does not depend on how the suite was launched.
func execDatalane(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// datalaneConn returns the connection flags every family shares.
func datalaneConn(rec *datalaneRecorder) []string {
	return []string{"--server", rec.server.URL, "--token", "secret-token", "--tenant", "tenant-a"}
}

func datalaneArgs(rec *datalaneRecorder, words ...string) []string {
	return append(append([]string{}, words...), datalaneConn(rec)...)
}

// datalaneReadVerbs is one harmless read per family: the positive control for
// every "it refused" assertion below.
var datalaneReadVerbs = []struct {
	name string
	args []string
	path string
}{
	{"knowledge", []string{"knowledge", "kbs", "ls"}, "/v1/m/knowledge/kbs"},
	{"sourcescope", []string{"sourcescope", "bindings", "ls"}, "/v1/m/sourcescope/bindings"},
	{"catalog", []string{"catalog", "entries", "ls"}, "/v1/m/catalog/entries"},
}

// TestDatalaneFamiliesRefuseWithoutAServerBeforeOpeningAConnection is the DENY
// half: with nothing to resolve a server from, the command exits 2 and the
// control plane hears nothing at all. The POSITIVE CONTROL is in the same
// subtest — supply --server and exactly one request arrives, exit 0 — because
// "zero requests" is also what a command that does nothing produces.
func TestDatalaneFamiliesRefuseWithoutAServerBeforeOpeningAConnection(t *testing.T) {
	for _, verb := range datalaneReadVerbs {
		t.Run(verb.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)

			_, _, err := execDatalane(t, "", verb.args...)
			if err == nil {
				t.Fatalf("%s with no server must fail", verb.name)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
			if got := rec.count(); got != 0 {
				t.Errorf("requests = %d, want 0: a caller with no server must not learn that any host answers", got)
			}

			// POSITIVE CONTROL.
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...); err != nil {
				t.Fatalf("%s with a server must succeed: %v", verb.name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests after the positive control = %d, want 1", got)
			}
			if got := rec.last(t).Path; got != verb.path {
				t.Errorf("path = %q, want %q", got, verb.path)
			}
			if got := rec.last(t).Header.Get("Authorization"); got != "Bearer secret-token" {
				t.Errorf("Authorization = %q, want the caller's bearer", got)
			}
			if got := rec.last(t).Header.Get("X-Olivares-Tenant"); got != "tenant-a" {
				t.Errorf("X-Olivares-Tenant = %q, want tenant-a", got)
			}
		})
	}
}

// TestDatalaneFamiliesMapServerRefusalsToTheExitContract pins the numbers a
// script branches on, per family, and checks that a refusal prints NOTHING on
// stdout — a shell capturing stdout must not receive a fragment of an error.
func TestDatalaneFamiliesMapServerRefusalsToTheExitContract(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, exitcode.Auth},
		{http.StatusForbidden, exitcode.Auth},
		{http.StatusNotFound, exitcode.NotFound},
		{http.StatusConflict, exitcode.Conflict},
		{http.StatusInternalServerError, exitcode.Server},
		{http.StatusBadGateway, exitcode.Server},
	}
	for _, verb := range datalaneReadVerbs {
		for _, tc := range cases {
			t.Run(verb.name+"/"+http.StatusText(tc.status), func(t *testing.T) {
				prepareDatalaneCLITest(t)
				rec := newDatalaneRecorder(t, tc.status, `{"error":{"message":"refused"}}`)
				out, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...)
				if err == nil {
					t.Fatalf("HTTP %d must fail", tc.status)
				}
				if got := exitcode.From(err); got != tc.want {
					t.Errorf("exit = %d, want %d for HTTP %d", got, tc.want, tc.status)
				}
				if strings.TrimSpace(out) != "" {
					t.Errorf("stdout must stay empty on a refusal, got %q", out)
				}
			})
		}
	}
}

// TestDatalaneMissingModuleIsNamedOnA404 checks the one place this lane adds to
// httpErr rather than replacing it: a 404 on a module namespace is very often
// "this engine was built without the module", and an operator who is told only
// "not found" goes looking for a missing entity that was never the problem. The
// exit code is unchanged (4) — the contract, not the wording, is what scripts
// read.
func TestDatalaneMissingModuleIsNamedOnA404(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusNotFound, `{"error":{"message":"not found"}}`)
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls")...)
	if err == nil {
		t.Fatal("a 404 must fail")
	}
	if got := exitcode.From(err); got != exitcode.NotFound {
		t.Fatalf("exit = %d, want %d", got, exitcode.NotFound)
	}
	for _, want := range []string{"HTTP 404", "knowledge module", "/v1/m/knowledge"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the 404 message must contain %q, got: %v", want, err)
		}
	}
}

// TestDatalanePositionalIDsCannotRetargetTheRequest is the escaping control. A
// separator inside a positional argument must stay INSIDE one path segment, and
// a bare ".." — which url.PathEscape leaves untouched, because dots are
// unreserved — is refused outright before any connection.
func TestDatalanePositionalIDsCannotRetargetTheRequest(t *testing.T) {
	traversal := "../../v1/system/orgs/t_victim"
	cases := []struct {
		name   string
		args   []string
		prefix string
	}{
		{"knowledge", []string{"knowledge", "kbs", "get", traversal}, "/v1/m/knowledge/kbs/"},
		{"sourcescope", []string{"sourcescope", "bindings", "get", traversal}, "/v1/m/sourcescope/bindings/"},
		{"catalog", []string{"catalog", "entries", "get", traversal}, "/v1/m/catalog/entries/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{}`)
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...); err != nil {
				t.Fatalf("the request should still be made (and answered): %v", err)
			}
			got := rec.last(t).Escaped
			if !strings.HasPrefix(got, tc.prefix) {
				t.Fatalf("escaped path = %q, want it to stay under %q", got, tc.prefix)
			}
			// The separators must be percent-encoded, so the whole argument is ONE
			// segment for the router; the decoded form still reads like a path and
			// asserting on it would prove nothing.
			if strings.Contains(strings.TrimPrefix(got, tc.prefix), "/") {
				t.Fatalf("a positional argument re-targeted the request: %q", got)
			}
			if !strings.Contains(got, "%2F") {
				t.Fatalf("the separators were not escaped: %q", got)
			}
		})
	}

	t.Run("bare dot-dot is refused before connecting", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK, `{}`)
		_, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "get", "..")...)
		if err == nil {
			t.Fatal(`"kbs get .." must be refused`)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
		}
		if got := rec.count(); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
	})
}

// TestDatalanePagingIsSentAndTruncationIsAnnounced covers the paging contract in
// both directions: what goes UP the wire, and what the operator is told when the
// page is not the whole answer. The announcement is on STDERR so a `$(…)`
// capture of the table is unaffected.
func TestDatalanePagingIsSentAndTruncationIsAnnounced(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK,
		`{"items":[{"id":"kb_1","name":"handbook","status":"active","doc_count":3}],"cursor":"CUR-42","has_more":true}`)

	out, errb, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "ls", "--limit", "1", "--cursor", "CUR-41", "--status", "active")...)
	if err != nil {
		t.Fatalf("kbs ls: %v (stderr %q)", err, errb)
	}
	q := rec.last(t).Query
	for key, want := range map[string]string{"limit": "1", "cursor": "CUR-41", "status": "active"} {
		if got := q.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	for _, want := range []string{"ID", "NAME", "STATUS", "DOCS", "kb_1", "handbook", "active", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errb, "CUR-42") || !strings.Contains(errb, "--cursor") {
		t.Errorf("a truncated page must name the cursor to continue from, on stderr; got %q", errb)
	}
	if strings.Contains(out, "CUR-42") {
		t.Errorf("the truncation note must not be on stdout, which a script parses:\n%s", out)
	}

	// The raw response survives -o json, cursor and all.
	jsonOut, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("kbs ls -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("JSON output is invalid: %v\n%s", err, jsonOut)
	}
	if decoded["cursor"] != "CUR-42" || decoded["has_more"] != true {
		t.Fatalf("-o json must preserve the paging fields, got %#v", decoded)
	}
}

// TestDatalaneListRejectsANonPositiveLimitBeforeConnecting: a --limit the
// module would silently ignore (it parses with Atoi and keeps whatever it gets)
// is refused here instead, so "I asked for 0 rows and got a thousand" cannot
// happen. Zero requests, and the positive control proves a real limit works.
func TestDatalaneListRejectsANonPositiveLimitBeforeConnecting(t *testing.T) {
	for _, bad := range []string{"0", "-1"} {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)
		_, _, err := execDatalane(t, "", datalaneArgs(rec, "catalog", "entries", "ls", "--limit", bad)...)
		if err == nil {
			t.Fatalf("--limit %s must be refused", bad)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("--limit %s: exit = %d, want %d", bad, got, exitcode.Usage)
		}
		if got := rec.count(); got != 0 {
			t.Errorf("--limit %s: requests = %d, want 0", bad, got)
		}
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, "catalog", "entries", "ls", "--limit", "5")...); err != nil {
			t.Fatalf("--limit 5 must be accepted: %v", err)
		}
		if got := rec.last(t).Query.Get("limit"); got != "5" {
			t.Errorf("limit sent = %q, want 5", got)
		}
	}
}

// TestDatalaneAcceptedProposalsAreAnnouncedAsNotInEffect is the 202 contract.
// sourcescope answers 202 when a change RELAXES an existing confinement: a proposal
// was recorded, and the change is NOT in effect. The announcement is on stderr, the
// body stays parsable on stdout, AND THE EXIT CODE IS 7.
//
// ⛔ THIS TEST ASSERTED EXIT 0 UNTIL 2026-08-18, with the written reason "the command
// succeeds (a proposal was recorded, which is what was asked for)". That reason is
// half of the truth and the exit code has to carry the other half.
//
// What changed the verdict is not taste, it is that the contract contradicted ITSELF.
// cmd_agentexec.go answers the SAME event — an approval is pending, nothing was
// actuated — with Degraded, and its comment rejects the success reading in as many
// words. One lane said 0 and the other said 7 for one state, inside the very branch
// whose C08-03 exists to unify exit codes. A contract that answers one event two ways
// is not a contract.
//
// Degraded is defined as "the command succeeded but reports a degraded condition"
// (exitcode.go:26-29), and BOTH halves are true here: the request was accepted and
// recorded, and it is not in effect. The deciding asymmetry is the cost of being
// wrong. The warning lives on stderr, which is exactly what a script does not read,
// so a script that relaxes a confinement and reads 0 believes access has been widened
// when a second approver has not yet acted. An undue zero is the one error in this
// family that makes the caller carry on.
//
// The body still renders first in both text and JSON, so nothing a caller parses is
// lost — only the code changed.
func TestDatalaneAcceptedProposalsAreAnnouncedAsNotInEffect(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusAccepted,
		`{"id":"pr_1","status":"pending","op":"disable_scoping"}`)
	out, errb, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "sources", "disable-scoping",
		"--source-type", "knowledge", "--source-ref", "kb_123", "--yes")...)
	if err == nil {
		t.Fatalf("a 202 must NOT exit 0: a script that reads 0 believes the change is in "+
			"effect when a second approver has not acted (stderr %q)", errb)
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("a 202 must exit %d (Degraded: succeeded but reports a degraded condition), got %d — "+
			"cmd_agentexec.go answers the same pending-approval state with Degraded, and the "+
			"contract has to agree with itself", exitcode.Degraded, got)
	}
	for _, want := range []string{"202", "NOT in effect"} {
		if !strings.Contains(errb, want) {
			t.Errorf("a 202 must be announced as a proposal (%q missing) — got stderr %q", want, errb)
		}
	}
	if !strings.Contains(out, "pending") {
		t.Errorf("the recorded request must reach stdout so a script can read its status:\n%s", out)
	}
}

// TestDatalaneAppliedChangesStillExitZero is the PERMIT half, and it is the half this
// repository keeps forgetting to write. Making a 202 exit 7 is worthless if it was
// done by making everything exit non-zero: a control that refuses everything passes
// any test of "it refuses". A 200 means the change IS applied, and it must still be
// indistinguishable from success to the script that called it.
func TestDatalaneAppliedChangesStillExitZero(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK,
		`{"id":"pr_1","status":"applied","op":"disable_scoping"}`)
	out, errb, err := execDatalane(t, "", datalaneArgs(rec,
		"sourcescope", "sources", "disable-scoping",
		"--source-type", "knowledge", "--source-ref", "kb_123", "--yes")...)
	if err != nil {
		t.Fatalf("a 200 is an APPLIED change and must exit 0, got %v (stderr %q)", err, errb)
	}
	if strings.Contains(errb, "NOT in effect") {
		t.Errorf("a 200 must not carry the proposal warning — got stderr %q", errb)
	}
	if !strings.Contains(out, "applied") {
		t.Errorf("the applied change must reach stdout:\n%s", out)
	}
}

// TestDatalaneNoBodyResponsesStillProduceParsableJSON: several deletes in this
// lane answer 204 with no body. Text prints the command's own note; -o json must
// still emit ONE object, because a script that pipes into jq cannot parse
// nothing.
func TestDatalaneNoBodyResponsesStillProduceParsableJSON(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusNoContent, "")

	out, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "rm", "ce_1", "--yes")...)
	if err != nil {
		t.Fatalf("entries rm: %v", err)
	}
	if !strings.Contains(out, "deleted catalog entry ce_1") {
		t.Errorf("text output must name what was deleted, got %q", out)
	}

	jsonOut, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "rm", "ce_1", "--yes", "-o", "json")...)
	if err != nil {
		t.Fatalf("entries rm -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("a 204 must still produce parsable JSON: %v\n%q", err, jsonOut)
	}
	if decoded["ok"] != true || decoded["http_status"] != float64(http.StatusNoContent) {
		t.Fatalf("JSON envelope for a 204 = %#v, want ok/http_status", decoded)
	}
}

// TestDatalaneCommandsAreRegisteredOnTheRealRoot is the wiring control. A
// command tree that compiles but is never added to the root is the failure this
// lane is most exposed to: three constructors, one registration line, and
// nothing else would notice. It resolves the paths through the SAME root the
// binary builds.
func TestDatalaneCommandsAreRegisteredOnTheRealRoot(t *testing.T) {
	root := newRootCmd()
	for _, path := range []string{
		"olivares knowledge kbs ls", "olivares knowledge memory purge",
		"olivares knowledge data-products contracts add", "olivares knowledge dlp put",
		"olivares sourcescope bindings set", "olivares sourcescope posture-requests approve",
		"olivares sourcescope workspace-connectors create", "olivares sourcescope resolve",
		"olivares catalog entries admit", "olivares catalog mcp-admission policy set",
		"olivares catalog connector-admission ls", "olivares catalog instances transition",
	} {
		if cmd := resolveCommandPath(t, root, path); cmd == nil {
			t.Errorf("%s does not resolve on the real root", path)
		}
	}
}

// TestDatalaneReplaceGuardRefusesAPartialReplace covers the control that exists
// because these PUTs REPLACE rather than patch. Three states, all measured:
// partial is refused with zero requests; --replace accepts the reset; naming
// every field needs no flag at all.
func TestDatalaneReplaceGuardRefusesAPartialReplace(t *testing.T) {
	full := []string{
		"knowledge", "kbs", "set", "kb_1",
		"--classification", "confidential", "--residency-region", "eu",
		"--embed-policy", "local_only", "--acl", "team:support", "--status", "active",
	}
	t.Run("partial is refused", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"kb_1"}`)
		_, _, err := execDatalane(t, "", datalaneArgs(rec,
			"knowledge", "kbs", "set", "kb_1", "--status", "archived")...)
		if err == nil {
			t.Fatal("a partial replace must be refused")
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit = %d, want %d", got, exitcode.Usage)
		}
		if got := rec.count(); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
		for _, want := range []string{"--classification", "--residency-region", "--replace"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must name %q so the operator knows what would be reset: %v", want, err)
			}
		}
	})
	t.Run("--replace accepts the reset", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"kb_1"}`)
		if _, _, err := execDatalane(t, "", datalaneArgs(rec,
			"knowledge", "kbs", "set", "kb_1", "--status", "archived", "--replace")...); err != nil {
			t.Fatalf("--replace must be accepted: %v", err)
		}
		if got := rec.count(); got != 1 {
			t.Fatalf("requests = %d, want 1", got)
		}
		if got := rec.last(t).Method; got != http.MethodPut {
			t.Errorf("method = %s, want PUT", got)
		}
	})
	t.Run("naming every field needs no flag", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"kb_1"}`)
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, full...)...); err != nil {
			t.Fatalf("a complete replace must be accepted without --replace: %v", err)
		}
		body := rec.jsonBody(t)
		if body["classification"] != "confidential" || body["residency_region"] != "eu" {
			t.Fatalf("body = %#v, want the fields the caller named", body)
		}
	})
}

// TestDatalaneJSONFlagsAreValidatedBeforeConnecting: a malformed JSON argument
// is the caller's mistake, so it is a usage error (2) reported with the source
// named — not a round trip that comes back as a generic bad request.
func TestDatalaneJSONFlagsAreValidatedBeforeConnecting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"ce_1"}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "create", "--kind", "mcp", "--name", "n", "--slug", "s",
		"--version", "1.0.0", "--spec", "{not json")...)
	if err == nil {
		t.Fatal("an invalid --spec must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}

	// POSITIVE CONTROL: valid JSON is sent through untouched.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "create", "--kind", "mcp", "--name", "n", "--slug", "s",
		"--version", "1.0.0", "--spec", `{"url":"https://mcp.example.com"}`)...); err != nil {
		t.Fatalf("valid JSON must be accepted: %v", err)
	}
	body := rec.jsonBody(t)
	spec, ok := body["spec"].(map[string]any)
	if !ok || spec["url"] != "https://mcp.example.com" {
		t.Fatalf("spec did not reach the control plane intact: %#v", body["spec"])
	}
}

// TestDatalaneTwoSpellingsOfOneValueAreRefused: --x and --x-file are the same
// value, and accepting both would make which one wins an accident.
func TestDatalaneTwoSpellingsOfOneValueAreRefused(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{}`)
	file := filepath.Join(t.TempDir(), "spec.json")
	if err := writeTestFile(file, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"catalog", "entries", "create", "--kind", "mcp", "--name", "n", "--slug", "s",
		"--version", "1.0.0", "--spec", `{"a":1}`, "--spec-file", file)...)
	if err == nil {
		t.Fatal("--spec together with --spec-file must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

// TestDatalaneGroupsCarryTheSharedConnectionFlags: the three groups must resolve
// their connection exactly as `auth` and `mcp` do, or an operator learns a
// different rule per family.
func TestDatalaneGroupsCarryTheSharedConnectionFlags(t *testing.T) {
	root := newRootCmd()
	for _, group := range []string{"knowledge", "sourcescope", "catalog"} {
		cmd, _, err := root.Find([]string{group})
		if err != nil || cmd == nil {
			t.Fatalf("cannot resolve %q: %v", group, err)
		}
		for _, flag := range []string{"server", "token", "tenant", "ca-cert", "pin-sha256", "insecure", "timeout"} {
			if cmd.PersistentFlags().Lookup(flag) == nil {
				t.Errorf("%s is missing the shared --%s flag", group, flag)
			}
		}
	}
}

// TestDatalaneOversizedStdinIsRefusedNotTruncated is the witness for the one
// place this lane was losing bytes without saying so.
//
// datalaneTextArg reads stdin through a LimitReader of maxDatalaneResponseSize+1
// — the +1 exists so an overflow is DETECTABLE — and the comparison was missing,
// so the sentinel byte was read and discarded and the command sent the first
// mebibyte of a longer value and exited 0. Measured before the repair: each of
// the five verbs below shipped 1048577 of 1052672 supplied bytes and reported
// success, so an operator was told a whole memory entry, prompt template,
// revision or retrieval query had been stored when a prefix of it had.
//
// The two halves are both here on purpose. The refusal half alone is satisfied
// by a command that rejects every stdin; the ACCEPT half proves a payload AT the
// bound still arrives whole and byte-identical.
func TestDatalaneOversizedStdinIsRefusedNotTruncated(t *testing.T) {
	cases := []struct {
		name string
		args []string
		// field is the request-body key carrying the payload; "" means the whole
		// raw body is the payload (memory import posts the bundle verbatim).
		field string
	}{
		{"memory put", []string{"knowledge", "memory", "put",
			"--agent-ref", "a1", "--key", "k1", "--content-file", "-"}, "content"},
		{"prompts create", []string{"knowledge", "prompts", "create",
			"--name", "p1", "--template-file", "-"}, "template"},
		{"prompts revisions add", []string{"knowledge", "prompts", "revisions", "add", "p1",
			"--template-file", "-"}, "template"},
		{"kbs query", []string{"knowledge", "kbs", "query", "kb1", "--query-file", "-"}, "query"},
		{"memory import", []string{"knowledge", "memory", "import", "--bundle-file", "-"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"x"}`)

			// DENY: one byte over the bound.
			over := strings.Repeat("a", maxDatalaneResponseSize+1)
			_, _, err := execDatalane(t, over, datalaneArgs(rec, tc.args...)...)
			if err == nil {
				t.Fatalf("%s: a stdin payload over the bound must be refused, not truncated", tc.name)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
			if !strings.Contains(err.Error(), "exceeds") {
				t.Errorf("the refusal must name the bound, got %v", err)
			}
			if got := rec.count(); got != 0 {
				t.Errorf("requests = %d, want 0: a value the CLI cannot carry whole must not be "+
					"half-written to the control plane", got)
			}

			// ACCEPT: exactly at the bound, and every byte arrives. A trailing
			// newline is what datalaneTextArg strips by contract, so the payload
			// ends in a non-newline byte.
			atLimit := strings.Repeat("b", maxDatalaneResponseSize)
			if _, _, err := execDatalane(t, atLimit, datalaneArgs(rec, tc.args...)...); err != nil {
				t.Fatalf("%s: a payload AT the bound must be accepted: %v", tc.name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
			sent := rec.last(t).Body
			if tc.field != "" {
				var decoded map[string]any
				if err := json.Unmarshal(sent, &decoded); err != nil {
					t.Fatalf("request body is not JSON: %v", err)
				}
				value, _ := decoded[tc.field].(string)
				sent = []byte(value)
			}
			if len(sent) != len(atLimit) {
				t.Errorf("%d of %d bytes reached the control plane: the payload was truncated",
					len(sent), len(atLimit))
			}
		})
	}
}

// TestDatalaneEveryVerbAddressesItsOwnRoute is the lane's ROUTE-PARITY control:
// one row per leaf command, naming the method and the exact path that command
// must put on the wire.
//
// It exists because six mutants proved the suite could not see a re-pointed
// verb. `memory all` sent to "/memory", `memory export` sent to "/memory",
// `sourcescope resolve` sent to "/resources", `catalog pubkey` sent to
// "/entries", `kbs documents` sent to "/documents/{id}" and `guard-postures ls`
// sent to "/bindings" each compiled and left every other test in this package
// GREEN. The damage is silent by construction: the CLI reports success, the
// operator reads a well-formed answer, and it is the answer to a different
// question — a caller-scoped page presented as the admin cross-scope view, a
// list page written to disk as if it were the signed portability bundle, the
// resource inventory read as a resolver decision, the entry list read as the
// signing key.
//
// The table is checked for COMPLETENESS against the real command tree before it
// is used. A table that silently stopped covering a family would be a gate that
// certifies rather than one that fails, so a leaf with no row — and a row naming
// no leaf — is itself a failure.
func TestDatalaneEveryVerbAddressesItsOwnRoute(t *testing.T) {
	bundleFile := filepath.Join(t.TempDir(), "bundle.ndjson")
	if err := writeTestFile(bundleFile, "{\"schema\":\"memport.v1\"}\n{\"key\":\"a\"}\n"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args   []string
		method string
		path   string
	}{
		{[]string{"catalog", "connector-admission", "ls"}, http.MethodGet, "/v1/m/catalog/connector-admissions"},
		{[]string{"catalog", "connector-admission", "policy", "get"}, http.MethodGet, "/v1/m/catalog/connector-admission/policy"},
		{[]string{"catalog", "connector-admission", "policy", "set"}, http.MethodPut, "/v1/m/catalog/connector-admission/policy"},
		{[]string{"catalog", "entries", "admit", "id1", "--bundle", "{\"a\":1}"}, http.MethodPost, "/v1/m/catalog/entries/id1/admit"},
		{[]string{"catalog", "entries", "approve", "id1"}, http.MethodPost, "/v1/m/catalog/entries/id1/approve"},
		{[]string{"catalog", "entries", "create", "--kind", "mcp", "--name", "n1", "--slug", "s1", "--version", "1.0.0"}, http.MethodPost, "/v1/m/catalog/entries"},
		{[]string{"catalog", "entries", "deprecate", "id1"}, http.MethodPost, "/v1/m/catalog/entries/id1/deprecate"},
		{[]string{"catalog", "entries", "get", "id1"}, http.MethodGet, "/v1/m/catalog/entries/id1"},
		{[]string{"catalog", "entries", "instantiate", "id1", "--name", "inst1"}, http.MethodPost, "/v1/m/catalog/entries/id1/instantiate"},
		{[]string{"catalog", "entries", "ls"}, http.MethodGet, "/v1/m/catalog/entries"},
		{[]string{"catalog", "entries", "rm", "id1"}, http.MethodDelete, "/v1/m/catalog/entries/id1"},
		{[]string{"catalog", "entries", "set", "id1"}, http.MethodPut, "/v1/m/catalog/entries/id1"},
		{[]string{"catalog", "entries", "submit", "id1"}, http.MethodPost, "/v1/m/catalog/entries/id1/submit"},
		{[]string{"catalog", "entries", "verify", "id1"}, http.MethodGet, "/v1/m/catalog/entries/id1/verify"},
		{[]string{"catalog", "instances", "get", "id1"}, http.MethodGet, "/v1/m/catalog/instances/id1"},
		{[]string{"catalog", "instances", "ls"}, http.MethodGet, "/v1/m/catalog/instances"},
		{[]string{"catalog", "instances", "transition", "id1", "--status", "approved"}, http.MethodPost, "/v1/m/catalog/instances/id1/transition"},
		{[]string{"catalog", "mcp-admission", "ls"}, http.MethodGet, "/v1/m/catalog/mcp-admissions"},
		{[]string{"catalog", "mcp-admission", "policy", "get"}, http.MethodGet, "/v1/m/catalog/mcp-admission/policy"},
		{[]string{"catalog", "mcp-admission", "policy", "set"}, http.MethodPut, "/v1/m/catalog/mcp-admission/policy"},
		{[]string{"catalog", "pubkey"}, http.MethodGet, "/v1/m/catalog/pubkey"},
		{[]string{"knowledge", "context-policies", "ls"}, http.MethodGet, "/v1/m/knowledge/context-policies"},
		{[]string{"knowledge", "context-policies", "put", "--scope-kind", "agent", "--scope-ref", "a1"}, http.MethodPost, "/v1/m/knowledge/context-policies"},
		{[]string{"knowledge", "data-products", "archive", "id1"}, http.MethodPost, "/v1/m/knowledge/data-products/id1/archive"},
		{[]string{"knowledge", "data-products", "contracts", "active", "id1"}, http.MethodGet, "/v1/m/knowledge/data-products/id1/contracts/active"},
		{[]string{"knowledge", "data-products", "contracts", "add", "id1"}, http.MethodPost, "/v1/m/knowledge/data-products/id1/contracts"},
		{[]string{"knowledge", "data-products", "contracts", "get", "dp1", "3"}, http.MethodGet, "/v1/m/knowledge/data-products/dp1/contracts/3"},
		{[]string{"knowledge", "data-products", "contracts", "ls", "id1"}, http.MethodGet, "/v1/m/knowledge/data-products/id1/contracts"},
		{[]string{"knowledge", "data-products", "create", "--name", "dp1"}, http.MethodPost, "/v1/m/knowledge/data-products"},
		{[]string{"knowledge", "data-products", "deprecate", "id1"}, http.MethodPost, "/v1/m/knowledge/data-products/id1/deprecate"},
		{[]string{"knowledge", "data-products", "events", "id1"}, http.MethodGet, "/v1/m/knowledge/data-products/id1/events"},
		{[]string{"knowledge", "data-products", "get", "id1"}, http.MethodGet, "/v1/m/knowledge/data-products/id1"},
		{[]string{"knowledge", "data-products", "health", "id1"}, http.MethodGet, "/v1/m/knowledge/data-products/id1/health"},
		{[]string{"knowledge", "data-products", "ls"}, http.MethodGet, "/v1/m/knowledge/data-products"},
		{[]string{"knowledge", "data-products", "publish", "id1"}, http.MethodPost, "/v1/m/knowledge/data-products/id1/publish"},
		{[]string{"knowledge", "data-products", "rm", "id1"}, http.MethodDelete, "/v1/m/knowledge/data-products/id1"},
		{[]string{"knowledge", "data-products", "set", "id1"}, http.MethodPut, "/v1/m/knowledge/data-products/id1"},
		{[]string{"knowledge", "data-products", "validate", "id1"}, http.MethodPost, "/v1/m/knowledge/data-products/id1/validate"},
		{[]string{"knowledge", "dlp", "ls"}, http.MethodGet, "/v1/m/knowledge/dlp/rules"},
		{[]string{"knowledge", "dlp", "put", "--class", "pii", "--action", "redact"}, http.MethodPut, "/v1/m/knowledge/dlp/rules"},
		{[]string{"knowledge", "dlp", "rm", "id1"}, http.MethodDelete, "/v1/m/knowledge/dlp/rules/id1"},
		{[]string{"knowledge", "documents", "get", "id1"}, http.MethodGet, "/v1/m/knowledge/documents/id1"},
		{[]string{"knowledge", "kbs", "create", "--name", "kb1"}, http.MethodPost, "/v1/m/knowledge/kbs"},
		{[]string{"knowledge", "kbs", "documents", "id1"}, http.MethodGet, "/v1/m/knowledge/kbs/id1/documents"},
		{[]string{"knowledge", "kbs", "get", "id1"}, http.MethodGet, "/v1/m/knowledge/kbs/id1"},
		{[]string{"knowledge", "kbs", "ingest", "id1", "--source", "src1"}, http.MethodPost, "/v1/m/knowledge/kbs/id1/ingest"},
		{[]string{"knowledge", "kbs", "ls"}, http.MethodGet, "/v1/m/knowledge/kbs"},
		{[]string{"knowledge", "kbs", "query", "id1", "--query", "hello"}, http.MethodPost, "/v1/m/knowledge/kbs/id1/query"},
		{[]string{"knowledge", "kbs", "reindex", "id1"}, http.MethodPost, "/v1/m/knowledge/kbs/id1/reindex"},
		{[]string{"knowledge", "kbs", "rm", "id1"}, http.MethodDelete, "/v1/m/knowledge/kbs/id1"},
		{[]string{"knowledge", "kbs", "scan", "id1"}, http.MethodPost, "/v1/m/knowledge/kbs/id1/scan"},
		{[]string{"knowledge", "kbs", "set", "id1"}, http.MethodPut, "/v1/m/knowledge/kbs/id1"},
		{[]string{"knowledge", "kbs", "sync", "id1", "--source", "src1"}, http.MethodPost, "/v1/m/knowledge/kbs/id1/sync"},
		{[]string{"knowledge", "labels", "ls"}, http.MethodGet, "/v1/m/knowledge/labels"},
		{[]string{"knowledge", "lineage", "get", "id1"}, http.MethodGet, "/v1/m/knowledge/lineage/id1"},
		{[]string{"knowledge", "lineage", "ls"}, http.MethodGet, "/v1/m/knowledge/lineage"},
		{[]string{"knowledge", "memory", "all"}, http.MethodGet, "/v1/m/knowledge/memory/all"},
		{[]string{"knowledge", "memory", "export"}, http.MethodGet, "/v1/m/knowledge/memory/export"},
		{[]string{"knowledge", "memory", "get", "id1"}, http.MethodGet, "/v1/m/knowledge/memory/id1"},
		{[]string{"knowledge", "memory", "import", "--bundle-file", "@ndjson"}, http.MethodPost, "/v1/m/knowledge/memory/import"},
		{[]string{"knowledge", "memory", "ls"}, http.MethodGet, "/v1/m/knowledge/memory"},
		{[]string{"knowledge", "memory", "purge"}, http.MethodPost, "/v1/m/knowledge/memory/purge"},
		{[]string{"knowledge", "memory", "put", "--agent-ref", "a1", "--key", "k1", "--content", "hello"}, http.MethodPost, "/v1/m/knowledge/memory"},
		{[]string{"knowledge", "memory", "rm", "id1"}, http.MethodDelete, "/v1/m/knowledge/memory/id1"},
		{[]string{"knowledge", "memory", "verify"}, http.MethodPost, "/v1/m/knowledge/memory/verify"},
		{[]string{"knowledge", "prompts", "create", "--name", "p1", "--template", "hello"}, http.MethodPost, "/v1/m/knowledge/prompts"},
		{[]string{"knowledge", "prompts", "get", "id1"}, http.MethodGet, "/v1/m/knowledge/prompts/id1"},
		{[]string{"knowledge", "prompts", "ls"}, http.MethodGet, "/v1/m/knowledge/prompts"},
		{[]string{"knowledge", "prompts", "revisions", "add", "id1", "--template", "hello"}, http.MethodPost, "/v1/m/knowledge/prompts/id1/revisions"},
		{[]string{"knowledge", "prompts", "revisions", "get", "p1", "3"}, http.MethodGet, "/v1/m/knowledge/prompts/p1/revisions/3"},
		{[]string{"knowledge", "prompts", "revisions", "ls", "id1"}, http.MethodGet, "/v1/m/knowledge/prompts/id1/revisions"},
		{[]string{"knowledge", "prompts", "rollback", "id1", "--rev", "2"}, http.MethodPost, "/v1/m/knowledge/prompts/id1/rollback"},
		{[]string{"knowledge", "scans", "ls"}, http.MethodGet, "/v1/m/knowledge/scans"},
		{[]string{"knowledge", "sources", "scan", "id1"}, http.MethodPost, "/v1/m/knowledge/sources/id1/scan"},
		{[]string{"sourcescope", "assignments", "create", "--connector-name", "c1", "--workspace-ref", "w1"}, http.MethodPost, "/v1/m/sourcescope/assignments"},
		{[]string{"sourcescope", "assignments", "get", "id1"}, http.MethodGet, "/v1/m/sourcescope/assignments/id1"},
		{[]string{"sourcescope", "assignments", "ls"}, http.MethodGet, "/v1/m/sourcescope/assignments"},
		{[]string{"sourcescope", "assignments", "rm", "id1"}, http.MethodDelete, "/v1/m/sourcescope/assignments/id1"},
		{[]string{"sourcescope", "assignments", "set", "id1"}, http.MethodPut, "/v1/m/sourcescope/assignments/id1"},
		{[]string{"sourcescope", "bindings", "create", "--source-type", "connector", "--source-ref", "sr1", "--scope-tree", "{\"a\":1}"}, http.MethodPost, "/v1/m/sourcescope/bindings"},
		{[]string{"sourcescope", "bindings", "get", "id1"}, http.MethodGet, "/v1/m/sourcescope/bindings/id1"},
		{[]string{"sourcescope", "bindings", "ls"}, http.MethodGet, "/v1/m/sourcescope/bindings"},
		{[]string{"sourcescope", "bindings", "rm", "id1"}, http.MethodDelete, "/v1/m/sourcescope/bindings/id1"},
		{[]string{"sourcescope", "bindings", "set", "id1"}, http.MethodPut, "/v1/m/sourcescope/bindings/id1"},
		{[]string{"sourcescope", "guard-postures", "ls"}, http.MethodGet, "/v1/m/sourcescope/guard-postures"},
		{[]string{"sourcescope", "guard-postures", "set", "--source-ref", "sr1", "--profile", "strict"}, http.MethodPut, "/v1/m/sourcescope/guard-postures"},
		{[]string{"sourcescope", "posture-requests", "approve", "id1"}, http.MethodPost, "/v1/m/sourcescope/posture-requests/id1/approve"},
		{[]string{"sourcescope", "posture-requests", "get", "id1"}, http.MethodGet, "/v1/m/sourcescope/posture-requests/id1"},
		{[]string{"sourcescope", "posture-requests", "ls"}, http.MethodGet, "/v1/m/sourcescope/posture-requests"},
		{[]string{"sourcescope", "posture-requests", "reject", "id1"}, http.MethodPost, "/v1/m/sourcescope/posture-requests/id1/reject"},
		{[]string{"sourcescope", "resolve", "--source-type", "connector", "--source-ref", "sr1", "--actor-kind", "agent", "--actor-ref", "a1"}, http.MethodGet, "/v1/m/sourcescope/resolve"},
		{[]string{"sourcescope", "resources", "ls"}, http.MethodGet, "/v1/m/sourcescope/resources"},
		{[]string{"sourcescope", "sources", "disable-scoping", "--source-type", "connector", "--source-ref", "sr1"}, http.MethodPost, "/v1/m/sourcescope/sources/disable-scoping"},
		{[]string{"sourcescope", "workspace-connectors", "create", "--name", "n1", "--kind", "slack", "--workspace-ref", "w1"}, http.MethodPost, "/v1/m/sourcescope/workspace-connectors"},
		{[]string{"sourcescope", "workspace-connectors", "get", "id1"}, http.MethodGet, "/v1/m/sourcescope/workspace-connectors/id1"},
		{[]string{"sourcescope", "workspace-connectors", "ls"}, http.MethodGet, "/v1/m/sourcescope/workspace-connectors"},
		{[]string{"sourcescope", "workspace-connectors", "rm", "id1"}, http.MethodDelete, "/v1/m/sourcescope/workspace-connectors/id1"},
		{[]string{"sourcescope", "workspace-connectors", "set", "id1"}, http.MethodPut, "/v1/m/sourcescope/workspace-connectors/id1"},
	}

	// COMPLETENESS: every leaf of the three families has exactly one row.
	root := newRootCmd()
	leaves := map[string]bool{}
	var walk func(cmd *cobra.Command, path []string)
	walk = func(cmd *cobra.Command, path []string) {
		if !cmd.HasSubCommands() {
			leaves[strings.Join(path, " ")] = true
			return
		}
		for _, child := range cmd.Commands() {
			if child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			walk(child, append(append([]string{}, path...), child.Name()))
		}
	}
	for _, family := range []string{"knowledge", "sourcescope", "catalog"} {
		cmd, _, err := root.Find([]string{family})
		if err != nil || cmd == nil || cmd.Name() != family {
			t.Fatalf("family %q is not registered on the real root: %v", family, err)
		}
		walk(cmd, []string{family})
	}
	// leafOf reduces a row's arguments to the command path it names, by dropping
	// the flags and then the positional arguments until a real leaf remains.
	leafOf := func(args []string) string {
		words := make([]string, 0, len(args))
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") {
				break
			}
			words = append(words, arg)
		}
		for len(words) > 0 && !leaves[strings.Join(words, " ")] {
			words = words[:len(words)-1]
		}
		return strings.Join(words, " ")
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		name := leafOf(tc.args)
		if name == "" {
			t.Errorf("row %v names no leaf command", tc.args)
			continue
		}
		if covered[name] {
			t.Errorf("%s has more than one row", name)
		}
		covered[name] = true
	}
	for leaf := range leaves {
		if !covered[leaf] {
			t.Errorf("%s has NO row: its route is unpinned and can be re-pointed unnoticed", leaf)
		}
	}
	if len(covered) != len(leaves) {
		t.Fatalf("rows cover %d leaves, the tree has %d", len(covered), len(leaves))
	}

	// Every row: the command reaches the wire once, at exactly that route.
	for _, tc := range cases {
		args := make([]string, 0, len(tc.args)+4)
		for _, arg := range tc.args {
			if arg == "@ndjson" {
				arg = bundleFile
			}
			args = append(args, arg)
		}
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false,"id":"x"}`)
			full := datalaneArgs(rec, args...)
			leaf, _, err := newRootCmd().Find(strings.Fields(leafOf(tc.args)))
			if err != nil || leaf == nil {
				t.Fatalf("cannot resolve %q: %v", name, err)
			}
			if leaf.Flags().Lookup("yes") != nil {
				full = append(full, "--yes")
			}
			if leaf.Flags().Lookup("replace") != nil {
				full = append(full, "--replace")
			}
			if _, _, err := execDatalane(t, "", full...); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
			last := rec.last(t)
			if last.Method != tc.method || last.Escaped != tc.path {
				t.Errorf("asked %s %s, want %s %s", last.Method, last.Escaped, tc.method, tc.path)
			}
		})
	}
}

// TestDatalaneSegmentsThatResolveToTheCollectionAreRefused closes the half of
// the escaping control that was written in only ONE direction.
//
// datalaneSegment refuses three spellings — "", "." and ".." — and until this
// test only ".." had a witness. Both survivors matter, and they matter MORE than
// the one that was covered: url.PathEscape leaves "." and "" untouched because
// neither contains a reserved byte, so `kbs rm .` travels as
// `DELETE /v1/m/knowledge/kbs/.` and `kbs rm ""` as `DELETE /v1/m/knowledge/kbs/`.
// A server that normalizes the path — every proxy in front of one does — resolves
// both to the COLLECTION. The operator asked to delete one row and addressed the
// route that holds all of them, and the CLI would have reported success.
//
// Measured, not assumed: with the "." arm removed from datalaneSegment the whole
// lot-2 suite stayed GREEN, and so did it with the "" arm removed.
//
// The ALLOW half is in the same subtest, because a guard that refuses every id
// also passes the refusal half.
func TestDatalaneSegmentsThatResolveToTheCollectionAreRefused(t *testing.T) {
	families := []struct {
		name string
		// rm is a DESTRUCTIVE entity verb: the damage of addressing the collection
		// instead of the row is at its worst here.
		rm   []string
		get  []string
		path string
	}{
		{"knowledge", []string{"knowledge", "kbs", "rm"}, []string{"knowledge", "kbs", "get"},
			"/v1/m/knowledge/kbs/"},
		{"sourcescope", []string{"sourcescope", "bindings", "rm"}, []string{"sourcescope", "bindings", "get"},
			"/v1/m/sourcescope/bindings/"},
		{"catalog", []string{"catalog", "entries", "rm"}, []string{"catalog", "entries", "get"},
			"/v1/m/catalog/entries/"},
	}
	for _, family := range families {
		for _, bad := range []struct{ label, id string }{
			{"dot", "."}, {"dot-dot", ".."}, {"empty", ""}, {"blank", "   "},
		} {
			t.Run(family.name+"/"+bad.label, func(t *testing.T) {
				prepareDatalaneCLITest(t)
				rec := newDatalaneRecorder(t, http.StatusOK, `{"deleted":true}`)

				// DENY, on the destructive verb, WITH --yes supplied: the consent
				// gate must not be what saves this, or the test would be measuring
				// the wrong guard.
				args := append(append([]string{}, family.rm...), bad.id, "--yes")
				_, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...)
				if err == nil {
					t.Fatalf("%s %q must be refused: it addresses the collection, not a row",
						strings.Join(family.rm, " "), bad.id)
				}
				if got := exitcode.From(err); got != exitcode.Usage {
					t.Errorf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
				}
				if got := rec.count(); got != 0 {
					t.Fatalf("requests = %d, want 0: a DELETE that would land on the collection "+
						"must not leave this process", got)
				}

				// The read verb is refused for the same reason. A caller who can
				// only READ the collection still learns rows they addressed one of.
				getArgs := append(append([]string{}, family.get...), bad.id)
				if _, _, err := execDatalane(t, "", datalaneArgs(rec, getArgs...)...); err == nil {
					t.Errorf("%s %q must be refused too", strings.Join(family.get, " "), bad.id)
				}
				if got := rec.count(); got != 0 {
					t.Errorf("requests = %d, want 0", got)
				}
			})
		}

		// POSITIVE CONTROL: a real id still reaches its own entity route, so the
		// refusals above are a guard and not a broken command.
		t.Run(family.name+"/a real id still works", func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"id":"real_1"}`)
			args := append(append([]string{}, family.get...), "real_1")
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); err != nil {
				t.Fatalf("a real id must be accepted: %v", err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
			if got := rec.last(t).Escaped; got != family.path+"real_1" {
				t.Errorf("escaped path = %q, want %q", got, family.path+"real_1")
			}
		})
	}
}

// TestDatalaneOversizedResponsesAreRefusedNotClipped is the response-side twin of
// TestDatalaneOversizedStdinIsRefusedNotTruncated, and it exists because the two
// request paths of this client DISAGREED.
//
// `do` reads maxDatalaneResponseSize+1 bytes and compares, so a body that does
// not fit is an error naming the bound. `doRawBody` — the path
// `knowledge memory import` posts the SIGNED portability bundle through — read
// the same +1 sentinel and never compared it, so an oversized answer came back
// silently clipped. Nothing in the lot noticed: with `do`'s comparison removed
// the whole suite stayed GREEN, which is why this witness is here rather than
// only the repair.
//
// Both halves, on both paths: over the bound is refused as a server-side fault
// (exit 6, the bound named), and a body AT the bound arrives whole.
func TestDatalaneOversizedResponsesAreRefusedNotClipped(t *testing.T) {
	bundleFile := filepath.Join(t.TempDir(), "bundle.ndjson")
	if err := writeTestFile(bundleFile, "{\"schema\":\"memport.v1\"}\n"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		// through datalaneClient.do
		{"kbs ls", []string{"knowledge", "kbs", "ls"}},
		// through datalaneClient.doRawBody
		{"memory import", []string{"knowledge", "memory", "import", "--bundle-file", bundleFile}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/over the bound is refused", func(t *testing.T) {
			prepareDatalaneCLITest(t)
			// Valid JSON, one byte too long: an invalid body would fail to decode
			// for a DIFFERENT reason and the test would measure the wrong guard.
			padding := maxDatalaneResponseSize + 1 - len(`{"items":[],"has_more":false,"pad":""}`)
			body := `{"items":[],"has_more":false,"pad":"` + strings.Repeat("p", padding) + `"}`
			if len(body) != maxDatalaneResponseSize+1 {
				t.Fatalf("harness built a %d-byte body, want %d", len(body), maxDatalaneResponseSize+1)
			}
			rec := newDatalaneRecorder(t, http.StatusOK, body)
			out, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...)
			if err == nil {
				t.Fatalf("%s: a response over the bound must be refused, not clipped", tc.name)
			}
			if got := exitcode.From(err); got != exitcode.Server {
				t.Errorf("exit = %d, want %d (server): %v", got, exitcode.Server, err)
			}
			if !strings.Contains(err.Error(), "exceeds") {
				t.Errorf("the refusal must name the bound, got %v", err)
			}
			if strings.TrimSpace(out) != "" {
				t.Errorf("stdout must stay empty, got %d bytes", len(out))
			}
		})
		t.Run(tc.name+"/at the bound is accepted", func(t *testing.T) {
			prepareDatalaneCLITest(t)
			padding := maxDatalaneResponseSize - len(`{"items":[],"has_more":false,"pad":""}`)
			body := `{"items":[],"has_more":false,"pad":"` + strings.Repeat("p", padding) + `"}`
			if len(body) != maxDatalaneResponseSize {
				t.Fatalf("harness built a %d-byte body, want %d", len(body), maxDatalaneResponseSize)
			}
			rec := newDatalaneRecorder(t, http.StatusOK, body)
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, tc.args...)...); err != nil {
				t.Fatalf("%s: a response AT the bound must be accepted: %v", tc.name, err)
			}
		})
	}
}

// TestDatalaneExportedBundleIsWrittenOwnerOnly pins the mode of the ONE file this
// lane creates. `knowledge memory export --out` writes agent memory — the
// module's own portability route calls it personal data — and it lands in
// whatever directory the operator names, very often a shared one. 0o600 was
// already what the code wrote and NOTHING asserted it: widening it to 0o644
// survived the whole lot-2 suite.
func TestDatalaneExportedBundleIsWrittenOwnerOnly(t *testing.T) {
	prepareDatalaneCLITest(t)
	const bundle = "{\"schema\":\"memport.v1\",\"signature\":\"AAAA\"}\n{\"key\":\"a\"}\n"
	rec := newDatalaneRecorder(t, http.StatusOK, bundle)

	dest := filepath.Join(t.TempDir(), "memory.ndjson")
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "memory", "export", "--out", dest)...); err != nil {
		t.Fatalf("memory export --out: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("the export wrote no file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %#o, want 0600: an exported memory bundle must not be readable "+
			"by every account on the host it was written on", got)
	}
	// The mode assertion must not be satisfiable by an EMPTY file.
	written, err := readTestFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if written != bundle {
		t.Fatalf("written file = %q, want the bundle", written)
	}
}

// TestDatalaneResponseValuesCannotDriveTheTerminal: a text table renders values
// the CONTROL PLANE chose, and a knowledge base name or a data-product tag is
// operator-supplied text that reached the store from somewhere else. datalaneCell
// puts every string through safeCLIValue, which replaces control runes with a
// space — dropping that call survived the whole lot-2 suite, so a stored name
// carrying an ANSI escape could repaint an operator's terminal, and a carriage
// return could overwrite the line above it.
//
// Both directions: the control characters are neutralized, and an ORDINARY value
// is passed through unchanged, so the guard cannot be a blanket mangling of text.
func TestDatalaneResponseValuesCannotDriveTheTerminal(t *testing.T) {
	prepareDatalaneCLITest(t)
	// The escape is written \u001b, NOT as a raw 0x1B byte: a raw control
	// character inside a JSON string is illegal (RFC 8259 section 7), so the
	// fixture this replaces never got past the response decoder -- the test died
	// on "invalid character '\\x1b' in string literal" without ever calling the
	// guard it names. A witness that cannot reach its guard measures nothing.
	// Decoded, this body still carries a real ESC and a real CR into the renderer.
	rec := newDatalaneRecorder(t, http.StatusOK,
		`{"items":[{"id":"kb_1","name":"handbook\u001b[2Jwiped\rSAFE","status":"active"}],"has_more":false}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls")...)
	if err != nil {
		t.Fatalf("kbs ls: %v", err)
	}
	for _, forbidden := range []string{"\x1b", "\r"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a control character from the response reached stdout verbatim (%q): %q",
				forbidden, out)
		}
	}
	// The value is still SHOWN — neutralized, not swallowed.
	for _, want := range []string{"handbook", "SAFE"} {
		if !strings.Contains(out, want) {
			t.Errorf("the printable part of the value must survive, %q missing from %q", want, out)
		}
	}
	// -o json is the engine's own bytes and is NOT a terminal rendering: it must
	// still carry the value as sent, or a script would read a different name than
	// the one stored.
	jsonOut, _, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("kbs ls -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	items, _ := decoded["items"].([]any)
	first, _ := items[0].(map[string]any)
	if name, _ := first["name"].(string); !strings.Contains(name, "\x1b") {
		t.Errorf("-o json must forward the stored value unchanged, got %q", name)
	}
}

// TestDatalaneTruncationNoteCoversBothPagingAnswers: `has_more` with no cursor is
// the case a paging script cannot recover from, and it is the one that had no
// witness — deleting the announcement entirely survived the lot-2 suite. A
// script that loops "while has_more, pass the cursor" with no cursor to pass
// processes the FIRST page forever, and nothing on stderr said so.
func TestDatalaneTruncationNoteCoversBothPagingAnswers(t *testing.T) {
	t.Run("has_more with a cursor names the cursor", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK,
			`{"items":[{"id":"kb_1"}],"cursor":"CUR-9","has_more":true}`)
		_, errb, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls")...)
		if err != nil {
			t.Fatalf("kbs ls: %v", err)
		}
		if !strings.Contains(errb, "CUR-9") || !strings.Contains(errb, "--cursor") {
			t.Errorf("stderr must name the cursor to continue from, got %q", errb)
		}
	})
	t.Run("has_more with no cursor says it cannot be continued", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK,
			`{"items":[{"id":"kb_1"}],"cursor":"","has_more":true}`)
		out, errb, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls")...)
		if err != nil {
			t.Fatalf("kbs ls: %v", err)
		}
		if !strings.Contains(errb, "cannot be continued") {
			t.Errorf("a truncated page with NO cursor must say the paging cannot be continued, "+
				"got stderr %q", errb)
		}
		if strings.Contains(errb, "--cursor ") {
			t.Errorf("stderr must not offer a cursor there is none of: %q", errb)
		}
		if !strings.Contains(out, "kb_1") {
			t.Errorf("the page itself must still be on stdout: %q", out)
		}
	})
	t.Run("a complete page announces nothing", func(t *testing.T) {
		prepareDatalaneCLITest(t)
		rec := newDatalaneRecorder(t, http.StatusOK,
			`{"items":[{"id":"kb_1"}],"has_more":false}`)
		_, errb, err := execDatalane(t, "", datalaneArgs(rec, "knowledge", "kbs", "ls")...)
		if err != nil {
			t.Fatalf("kbs ls: %v", err)
		}
		if strings.Contains(errb, "more rows") {
			t.Errorf("a complete page must not warn about paging: %q", errb)
		}
	})
}

// TestDatalaneUnsetFiltersAreNotSentAsBlanks: `--status ""` and an absent
// --status are different intentions, and datalaneFilter keeps them different by
// sending NEITHER. Sending `status=` instead survived the lot-2 suite. It matters
// because the module handlers read these with r.URL.Query().Get, where a present
// key and an absent one are the same only until one of them grows a meaning.
func TestDatalaneUnsetFiltersAreNotSentAsBlanks(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[],"has_more":false}`)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "ls", "--status", "")...); err != nil {
		t.Fatalf("kbs ls --status '': %v", err)
	}
	if _, present := rec.last(t).Query["status"]; present {
		t.Errorf("an explicitly BLANK filter was sent as a query key: %v", rec.last(t).Query)
	}

	// POSITIVE CONTROL: a real filter is sent.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "ls", "--status", "active")...); err != nil {
		t.Fatalf("kbs ls --status active: %v", err)
	}
	if got := rec.last(t).Query.Get("status"); got != "active" {
		t.Errorf("status = %q, want active", got)
	}
}

// TestDatalaneTransportFailuresNeverEchoTheCredential: every error return in this
// client passes through redactCLIError, and the transport branch had no witness —
// removing the redaction there survived the lot-2 suite. A dial failure quotes
// the whole target URL, so a credential that reached the URL comes back in the
// error text, and error text is what ends up in CI logs and pasted tickets.
func TestDatalaneTransportFailuresNeverEchoTheCredential(t *testing.T) {
	prepareDatalaneCLITest(t)
	const token = "tok-do-not-print-me-0123456789"
	// Port 1 on the loopback refuses instantly: no DNS, no network, no timeout.
	// The token is in the URL PATH, which is what a dial error quotes back.
	root := newRootCmd()
	var out, errb strings.Builder
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"knowledge", "kbs", "ls",
		"--server", "http://127.0.0.1:1/" + token, "--token", token, "--tenant", "tenant-a"})
	err := root.Execute()
	if err == nil {
		t.Fatal("a refused connection must fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the credential survived into the error text: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("the redaction must be visible so nobody reads the message as complete: %v", err)
	}
	// POSITIVE CONTROL: the error still says what went wrong, or redaction would
	// have been bought by making the failure unreadable.
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("the message must still name the target that failed: %v", err)
	}
}

// TestDatalaneRawBundleKeepsItsOwnContentType: `memory import` posts NDJSON, not
// JSON, and says so. Relabelling it survived the lot-2 suite. The bundle's first
// line signs the bytes that follow, so anything that invites a middlebox or a
// handler to re-parse it as one JSON document is a step towards breaking the
// verification the import performs before it writes.
func TestDatalaneRawBundleKeepsItsOwnContentType(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"imported":2}`)
	const bundle = "{\"schema\":\"memport.v1\",\"signature\":\"AAAA\"}\n{\"key\":\"a\"}\n"
	file := filepath.Join(t.TempDir(), "bundle.ndjson")
	if err := writeTestFile(file, bundle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "memory", "import", "--bundle-file", file)...); err != nil {
		t.Fatalf("memory import: %v", err)
	}
	req := rec.last(t)
	if got := req.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", got)
	}
	if string(req.Body) != bundle {
		t.Errorf("the bundle was not posted byte for byte:\n got %q\nwant %q", req.Body, bundle)
	}

	// POSITIVE CONTROL: an ordinary JSON verb still labels itself as JSON, so the
	// assertion above is about THIS route and not about the client losing the
	// header everywhere.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"knowledge", "kbs", "create", "--name", "kb")...); err != nil {
		t.Fatalf("kbs create: %v", err)
	}
	if got := rec.last(t).Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// writeTestFile / readTestFile keep the table tests readable.
func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func readTestFile(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a path this test just created
	return string(raw), err
}
