// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The gitpublish family's harness. Every verb is a thin client over one of the
// thirteen /v1/m/gitpublish routes, so every assertion here is on the wire —
// method, path, query and body — exactly as the knowledge family's tests assert
// on its routes, using the same recorder.
//
// The one property that is THIS family's own, and not shared with the datalane:
// a publication effect has three honest endings, and the CLI must not flatten
// them into one. 200/201 is settled; 202 means the host outcome is NOT known;
// 409 is either a refusal (error envelope) or a rejected intent (intent body).
// The 202 and 409-intent tests below are the undue-zero guards.

// gitpublishIntentBody is a real intentDTO shape (modules/gitpublish/routes.go),
// with the fields the assertions read. The shas are synthetic 40-hex values.
const gitpublishIntentBody = `{"id":"gpi_1","target_id":"gpt_1","target_version":3,"effect":"push",` +
	`"operation_id":"op-1","attempt":1,"state":"applied","receipt":"host_ack",` +
	`"requested":{"ref":"refs/heads/agent/run-1","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},` +
	`"observed":{"present":true},"acknowledged":{"acknowledged":true,"status":200}}`

// TestGitpublishVerbsReachTheExistingRoutes is the issue's exit criterion as one
// table: EVERY verb calls its existing route — same method, same path, same
// query — and sends exactly one request. A verb that invented its own logic, or
// mistyped a route, fails here on the path.
func TestGitpublishVerbsReachTheExistingRoutes(t *testing.T) {
	sha := strings.Repeat("1", 40)
	for _, verb := range []struct {
		name string
		args []string
		// wantQuery asserts one named query value; nil means none expected.
		wantQuery [][2]string
	}{
		{"targets ls", []string{"gitpublish", "targets", "ls"}, nil},
		{"targets list alias", []string{"gitpublish", "targets", "list"}, nil},
		{"targets get", []string{"gitpublish", "targets", "get", "gpt_1"}, nil},
		{"push", []string{"gitpublish", "push", "gpt_1", "--operation-id", "op-1",
			"--ref", "refs/heads/agent/run-1", "--commit", sha, "--tree", strings.Repeat("2", 40),
			"--expected-old", strings.Repeat("3", 40), "--acknowledge-intent", "gpi_0"}, nil},
		{"pull-request", []string{"gitpublish", "pull-request", "gpt_1", "--operation-id", "op-2",
			"--head-ref", "agent/run-1", "--base", "main", "--commit", sha,
			"--title", "Publish run 1", "--body", "From session", "--draft"}, nil},
		{"merge", []string{"gitpublish", "merge", "gpt_1", "--operation-id", "op-3",
			"--number", "7", "--expected-head", sha, "--method", "squash"}, nil},
		{"intents ls", []string{"gitpublish", "intents", "ls", "--target", "gpt_1"}, [][2]string{{"target_id", "gpt_1"}}},
		{"intents get", []string{"gitpublish", "intents", "get", "gpi_1"}, nil},
		{"intents observations", []string{"gitpublish", "intents", "observations", "gpi_1"}, nil},
		{"intents reconcile", []string{"gitpublish", "intents", "reconcile", "gpi_1"}, nil},
		{"intents abandon", []string{"gitpublish", "intents", "abandon", "gpi_1", "--reason", "investigated"}, nil},
	} {
		t.Run(verb.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[`+gitpublishIntentBody+`]}`)

			if _, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...); err != nil {
				t.Fatalf("%s must succeed: %v", verb.name, err)
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("%s sent %d requests, want exactly 1", verb.name, got)
			}
			req := rec.last(t)
			want := map[string]string{
				"targets ls":           http.MethodGet + " /v1/m/gitpublish/targets",
				"targets list alias":   http.MethodGet + " /v1/m/gitpublish/targets",
				"targets get":          http.MethodGet + " /v1/m/gitpublish/targets/gpt_1",
				"push":                 http.MethodPost + " /v1/m/gitpublish/targets/gpt_1/pushes",
				"pull-request":         http.MethodPost + " /v1/m/gitpublish/targets/gpt_1/pull-requests",
				"merge":                http.MethodPost + " /v1/m/gitpublish/targets/gpt_1/merges",
				"intents ls":           http.MethodGet + " /v1/m/gitpublish/intents",
				"intents get":          http.MethodGet + " /v1/m/gitpublish/intents/gpi_1",
				"intents observations": http.MethodGet + " /v1/m/gitpublish/intents/gpi_1/observations",
				"intents reconcile":    http.MethodPost + " /v1/m/gitpublish/intents/gpi_1/reconcile",
				"intents abandon":      http.MethodPost + " /v1/m/gitpublish/intents/gpi_1/abandon",
			}[verb.name]
			if got := req.Method + " " + req.Path; got != want {
				t.Errorf("%s reached %q, want %q", verb.name, got, want)
			}
			for _, kv := range verb.wantQuery {
				if got := req.Query.Get(kv[0]); got != kv[1] {
					t.Errorf("%s query %s = %q, want %q", verb.name, kv[0], got, kv[1])
				}
			}
		})
	}
}

// TestGitpublishPushSendsOnlyTheCallerNamedFields pins the request document: the
// flags the operator passed travel under the route's own field names, and a flag
// they did not pass is ABSENT from the body (an empty expected_old is a real
// request — a lease on an absent ref — so the CLI must not invent one).
func TestGitpublishPushSendsOnlyTheCallerNamedFields(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "push", "gpt_1", "--operation-id", "op-9",
		"--ref", "refs/heads/agent/run-9", "--commit", strings.Repeat("c", 40), "--tree", strings.Repeat("d", 40))...); err != nil {
		t.Fatalf("push: %v", err)
	}
	body := rec.jsonBody(t)
	for key, want := range map[string]any{
		"operation_id": "op-9",
		"ref":          "refs/heads/agent/run-9",
		"commit":       strings.Repeat("c", 40),
		"tree":         strings.Repeat("d", 40),
	} {
		if got := body[key]; got != want {
			t.Errorf("body[%q] = %v, want %v", key, got, want)
		}
	}
	for _, absent := range []string{"expected_old", "acknowledge_intent"} {
		if _, present := body[absent]; present {
			t.Errorf("body carries %q the caller did not name: %v", absent, body[absent])
		}
	}
}

// TestGitpublishMergeSendsTheReviewedGuards asserts the merge document carries
// the number, the sha guard and the method — and, by sending a method value the
// MODULE alone validates, that the CLI did not grow an enum check that could
// drift from the module's own.
func TestGitpublishMergeSendsTheReviewedGuards(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "merge", "gpt_1", "--operation-id", "op-3",
		"--number", "7", "--expected-head", strings.Repeat("e", 40), "--method", "rebase")...); err != nil {
		t.Fatalf("merge: %v", err)
	}
	body := rec.jsonBody(t)
	if body["number"] != float64(7) || body["expected_head"] != strings.Repeat("e", 40) || body["method"] != "rebase" {
		t.Fatalf("merge body = %#v, want the number, the sha guard and the method", body)
	}
}

// TestGitpublishEffectVerbsRefuseMissingRequiredFieldsBeforeConnecting is the
// local-refusal half: a required flag missing is the caller's error (exit 2) and
// costs zero requests. The positive control in each subtest proves the refusal
// is about the missing flag, not a broken command.
func TestGitpublishEffectVerbsRefuseMissingRequiredFieldsBeforeConnecting(t *testing.T) {
	sha := strings.Repeat("1", 40)
	full := map[string][]string{
		"push":         {"push", "gpt_1", "--operation-id", "op-1", "--ref", "refs/heads/agent/x", "--commit", sha, "--tree", strings.Repeat("2", 40)},
		"pull-request": {"pull-request", "gpt_1", "--operation-id", "op-2", "--head-ref", "agent/x", "--base", "main", "--commit", sha, "--title", "T"},
		"merge":        {"merge", "gpt_1", "--operation-id", "op-3", "--number", "7", "--expected-head", sha, "--method", "merge"},
	}
	drop := func(args []string, flag string) []string {
		out := make([]string, 0, len(args))
		skip := false
		for _, a := range args {
			if skip {
				skip = false
				continue
			}
			if a == "--"+flag {
				skip = true
				continue
			}
			out = append(out, a)
		}
		return out
	}
	for verb, required := range map[string][]string{
		"push":         {"operation-id", "ref", "commit", "tree"},
		"pull-request": {"operation-id", "head-ref", "base", "commit", "title"},
		"merge":        {"operation-id", "number", "expected-head", "method"},
	} {
		for _, flag := range required {
			t.Run(verb+" without --"+flag, func(t *testing.T) {
				prepareDatalaneCLITest(t)
				rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)

				args := append([]string{"gitpublish"}, drop(full[verb], flag)...)
				_, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...)
				if err == nil {
					t.Fatalf("%s without --%s must fail", verb, flag)
				}
				if got := exitcode.From(err); got != exitcode.Usage {
					t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
				}
				if got := rec.count(); got != 0 {
					t.Fatalf("requests = %d, want 0: a missing required flag must not reach the engine", got)
				}

				// POSITIVE CONTROL: the same verb with every flag succeeds.
				ok := append([]string{"gitpublish"}, full[verb]...)
				if _, _, err := execDatalane(t, "", datalaneArgs(rec, ok...)...); err != nil {
					t.Fatalf("%s with every flag must succeed: %v", verb, err)
				}
				if got := rec.count(); got != 1 {
					t.Fatalf("requests after positive control = %d, want 1", got)
				}
			})
		}
	}
}

// TestGitpublishIntentsListRequiresTargetBeforeConnecting: GET /intents without
// target_id is the engine's 400; the CLI refuses it locally at exit 2 instead.
func TestGitpublishIntentsListRequiresTargetBeforeConnecting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[]}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "intents", "ls")...)
	if err == nil {
		t.Fatal("intents ls without --target must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "intents", "ls", "--target", "gpt_1")...); err != nil {
		t.Fatalf("intents ls --target must succeed: %v", err)
	}
	if got := rec.last(t).Query.Get("target_id"); got != "gpt_1" {
		t.Errorf("target_id = %q, want gpt_1", got)
	}
}

// TestGitpublishEffect202IsNotSuccess is the first undue-zero guard. A 202
// answer means the request was recorded and dispatched but the HOST outcome is
// not confirmed — it may have landed. Exit 0 would tell a script it published.
// The intent renders first in BOTH modes (a -o json consumer loses nothing),
// the guidance goes to stderr, and the exit is Degraded.
func TestGitpublishEffect202IsNotSuccess(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusAccepted,
		strings.Replace(gitpublishIntentBody, `"state":"applied"`, `"state":"uncertain"`, 1))

	out, errb, err := execDatalane(t, "", datalaneArgs(rec, "-o", "json",
		"gitpublish", "push", "gpt_1", "--operation-id", "op-4",
		"--ref", "refs/heads/agent/run-1", "--commit", strings.Repeat("a", 40), "--tree", strings.Repeat("b", 40))...)
	if err == nil {
		t.Fatal("a 202 publication answer must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Errorf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(out, `"state": "uncertain"`) {
		t.Errorf("the intent must render before the exit; got:\n%s", out)
	}
	if !strings.Contains(errb, "not") || !strings.Contains(errb, "reconcile") {
		t.Errorf("stderr must say the outcome is not confirmed and name reconcile; got:\n%s", errb)
	}
	if !strings.Contains(errb, "op-4") {
		t.Errorf("stderr must name the operation id a retry must reuse; got:\n%s", errb)
	}
}

// TestGitpublishRejectedIntentRendersAndExitsConflict is the second guard: a
// 409 whose body is the INTENT (the host refused the effect and the engine
// records it) is a failure — exit Conflict — but the operator still sees the
// recorded refusal reason, which the plain error path would swallow.
func TestGitpublishRejectedIntentRendersAndExitsConflict(t *testing.T) {
	prepareDatalaneCLITest(t)
	rejected := strings.Replace(gitpublishIntentBody, `"state":"applied"`,
		`"state":"rejected","reason":"protected_branch_refused"`, 1)
	rec := newDatalaneRecorder(t, http.StatusConflict, rejected)

	out, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "push", "gpt_1", "--operation-id", "op-5",
		"--ref", "refs/heads/agent/run-1", "--commit", strings.Repeat("a", 40), "--tree", strings.Repeat("b", 40))...)
	if err == nil {
		t.Fatal("a rejected intent must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Errorf("exit = %d, want %d (conflict)", got, exitcode.Conflict)
	}
	if !strings.Contains(out, "protected_branch_refused") {
		t.Errorf("the recorded refusal reason must render; got:\n%s", out)
	}
}

// TestGitpublishRefusalsKeepTheSharedExitContract: an error-envelope answer goes
// through the ONE mapping the whole CLI shares (409→5, 404→4, 5xx→6); this
// family adds no dialect of its own.
func TestGitpublishRefusalsKeepTheSharedExitContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"unresolved scope", http.StatusConflict, `{"error":{"code":"unresolved_intent","message":"unresolved_intent"}}`, exitcode.Conflict},
		{"no such target", http.StatusNotFound, `{"error":{"code":"not_found","message":"not_found"}}`, exitcode.NotFound},
		{"engine failure", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"unavailable"}}`, exitcode.Server},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, tc.status, tc.body)

			_, _, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "targets", "get", "gpt_x")...)
			if err == nil {
				t.Fatal("a refusal must not exit 0")
			}
			if got := exitcode.From(err); got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestGitpublishEffectTransportFailureIsIndeterminate: for an effect verb, a
// connection that failed mid-flight leaves the host outcome unknown — the
// module's own recovery rule is to resend with the SAME operation id (it returns
// the recorded intent; it does not dispatch twice), or to reconcile.
// Indeterminate (8) says exactly that; a read verb keeps the shared Server (6).
func TestGitpublishEffectTransportFailureIsIndeterminate(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)
	rec.server.Close() // every later request dies before the engine answers

	_, errb, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "push", "gpt_1", "--operation-id", "op-6",
		"--ref", "refs/heads/agent/run-1", "--commit", strings.Repeat("a", 40), "--tree", strings.Repeat("b", 40))...)
	if err == nil {
		t.Fatal("a dead engine must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Indeterminate {
		t.Errorf("effect exit = %d, want %d (indeterminate)", got, exitcode.Indeterminate)
	}
	for _, want := range []string{"op-6", "operation"} {
		if !strings.Contains(errb, want) {
			t.Errorf("stderr must carry %q so the retry reuses it; got:\n%s", want, errb)
		}
	}

	_, _, err = execDatalane(t, "", datalaneArgs(rec, "gitpublish", "targets", "ls")...)
	if err == nil {
		t.Fatal("a dead engine must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Server {
		t.Errorf("read exit = %d, want %d (server)", got, exitcode.Server)
	}
}

// TestGitpublishListTextAndJsonAssertBothModes: text is a projection an
// operator reads (the ids and states are there), and -o json is the ENGINE'S
// bytes, not a struct this CLI re-marshaled — a field the CLI does not model
// (the whole nested requested document) must survive.
func TestGitpublishListTextAndJsonAssertBothModes(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[`+gitpublishIntentBody+`]}`)

	out, _, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "intents", "ls", "--target", "gpt_1")...)
	if err != nil {
		t.Fatalf("intents ls: %v", err)
	}
	for _, want := range []string{"gpi_1", "push", "applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("text listing must carry %q; got:\n%s", want, out)
		}
	}

	out, _, err = execDatalane(t, "", datalaneArgs(rec, "-o", "json", "gitpublish", "intents", "ls", "--target", "gpt_1")...)
	if err != nil {
		t.Fatalf("intents ls -o json: %v", err)
	}
	if !strings.Contains(out, `"requested"`) || !strings.Contains(out, `"operation_id"`) {
		t.Errorf("-o json must be the engine's own document, nested fields included; got:\n%s", out)
	}
}

// gitpublishTargetBody is a real targetDTO shape (modules/gitpublish/routes.go).
const gitpublishTargetBody = `{"id":"gpt_1","workspace_id":"ws_9","push_prefix":"refs/heads/agent/",` +
	`"merge_bases":["main","release"],"version":3}`

// TestGitpublishPullRequestSendsTheCallerNamedFields: the verb with the most
// fields has its OWN body pin — a dropped struct literal, a swapped binding or
// a json-tag typo here would pass every route-level assertion.
func TestGitpublishPullRequestSendsTheCallerNamedFields(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "pull-request", "gpt_1", "--operation-id", "op-7",
		"--head-ref", "agent/run-7", "--base", "main", "--commit", strings.Repeat("f", 40),
		"--title", "Publish run 7", "--body", "From session", "--draft",
		"--acknowledge-intent", "gpi_0")...); err != nil {
		t.Fatalf("pull-request: %v", err)
	}
	body := rec.jsonBody(t)
	for key, want := range map[string]any{
		"operation_id":       "op-7",
		"head_ref":           "agent/run-7",
		"base":               "main",
		"commit":             strings.Repeat("f", 40),
		"title":              "Publish run 7",
		"body":               "From session",
		"draft":              true,
		"acknowledge_intent": "gpi_0",
	} {
		if got := body[key]; got != want {
			t.Errorf("body[%q] = %v, want %v", key, got, want)
		}
	}

	// The minimal form sends only what the route requires; the optional fields
	// are ABSENT, not zero-filled.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "pull-request", "gpt_1", "--operation-id", "op-8",
		"--head-ref", "agent/run-8", "--base", "main", "--commit", strings.Repeat("f", 40),
		"--title", "T")...); err != nil {
		t.Fatalf("pull-request minimal: %v", err)
	}
	body = rec.jsonBody(t)
	for _, absent := range []string{"body", "draft", "acknowledge_intent"} {
		if _, present := body[absent]; present {
			t.Errorf("minimal body carries %q the caller did not name: %v", absent, body[absent])
		}
	}
}

// TestGitpublishOptionalGuardsPropagate pins the optional fields a script
// composes deliberately: the lease (--expected-old), the scope-release
// handshake (--acknowledge-intent) and the recorded responsibility note
// (--reason). A flag registered but never wired into its body passes every
// other test in this file.
func TestGitpublishOptionalGuardsPropagate(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "push", "gpt_1", "--operation-id", "op-9",
		"--ref", "refs/heads/agent/run-9", "--commit", strings.Repeat("c", 40), "--tree", strings.Repeat("d", 40),
		"--expected-old", strings.Repeat("3", 40), "--acknowledge-intent", "gpi_0")...); err != nil {
		t.Fatalf("push: %v", err)
	}
	body := rec.jsonBody(t)
	if body["expected_old"] != strings.Repeat("3", 40) || body["acknowledge_intent"] != "gpi_0" {
		t.Errorf("push optional guards = %v %v, want the lease and the acknowledged intent",
			body["expected_old"], body["acknowledge_intent"])
	}

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "merge", "gpt_1", "--operation-id", "op-3",
		"--number", "7", "--expected-head", strings.Repeat("e", 40), "--method", "merge",
		"--acknowledge-intent", "gpi_0")...); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if body := rec.jsonBody(t); body["acknowledge_intent"] != "gpi_0" {
		t.Errorf("merge acknowledge_intent = %v, want gpi_0", body["acknowledge_intent"])
	}

	if _, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "intents", "abandon", "gpi_1", "--reason", "investigated: pushed by hand")...); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if body := rec.jsonBody(t); body["reason"] != "investigated: pushed by hand" {
		t.Errorf("abandon reason = %v, want the operator's note", body["reason"])
	}
}

// TestGitpublishEffectRefusalEnvelopeIsNotAnIntent: the discrimination runs in
// BOTH directions. A 409 carrying an error envelope is a refusal — exit
// Conflict through the shared mapping, and NO intent is rendered, because
// stdout then claims a record the engine says it never made.
func TestGitpublishEffectRefusalEnvelopeIsNotAnIntent(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusConflict,
		`{"error":{"code":"unresolved_intent","message":"unresolved_intent"}}`)

	out, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "push", "gpt_1", "--operation-id", "op-5",
		"--ref", "refs/heads/agent/run-1", "--commit", strings.Repeat("a", 40), "--tree", strings.Repeat("b", 40))...)
	if err == nil {
		t.Fatal("a refusal must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Errorf("exit = %d, want %d (conflict)", got, exitcode.Conflict)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a refusal envelope must render no intent; got:\n%s", out)
	}
}

// TestGitpublishRefusalKeepsTheEnginesIntentID: the engine attaches a top-level
// intent_id to every refusal that names a recorded intent (writeErr,
// modules/gitpublish/routes.go) so the operator can reach it. The CLI must
// carry it into its refusal, not drop it on the floor.
func TestGitpublishRefusalKeepsTheEnginesIntentID(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusConflict,
		`{"error":{"code":"unresolved_intent","message":"unresolved_intent"},"intent_id":"gpi_7"}`)

	_, _, err := execDatalane(t, "", datalaneArgs(rec,
		"gitpublish", "push", "gpt_1", "--operation-id", "op-5",
		"--ref", "refs/heads/agent/run-1", "--commit", strings.Repeat("a", 40), "--tree", strings.Repeat("b", 40))...)
	if err == nil {
		t.Fatal("a refusal must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Errorf("exit = %d, want %d (conflict)", got, exitcode.Conflict)
	}
	for _, want := range []string{"gpi_7", "intents get gpi_7"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the recorded intent and the verb that reaches it (%q missing): %v", want, err)
		}
	}
}

// TestGitpublishReconcileTransportFailureIsIndeterminate: reconcile is a
// non-GET verb with its own recovery sentence (no operation id); if the
// non-GET branch ever narrowed to the effects alone, it would silently become
// Server and invite a blind retry.
func TestGitpublishReconcileTransportFailureIsIndeterminate(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, gitpublishIntentBody)
	rec.server.Close()

	_, errb, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "intents", "reconcile", "gpi_6")...)
	if err == nil {
		t.Fatal("a dead engine must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Indeterminate {
		t.Errorf("reconcile exit = %d, want %d (indeterminate)", got, exitcode.Indeterminate)
	}
	if !strings.Contains(errb, "gpi_6") {
		t.Errorf("stderr must name the intent to follow; got:\n%s", errb)
	}
}

// TestGitpublishReadVerbsRenderTheirColumns: every list verb's text projection
// names the DTO keys it renders, so a mistyped column key renders an empty
// cell under a green test elsewhere.
func TestGitpublishReadVerbsRenderTheirColumns(t *testing.T) {
	prepareDatalaneCLITest(t)

	targets := newDatalaneRecorder(t, http.StatusOK, `{"items":[`+gitpublishTargetBody+`]}`)
	out, _, err := execDatalane(t, "", datalaneArgs(targets, "gitpublish", "targets", "ls")...)
	if err != nil {
		t.Fatalf("targets ls: %v", err)
	}
	for _, want := range []string{"gpt_1", "ws_9", "refs/heads/agent/", "main"} {
		if !strings.Contains(out, want) {
			t.Errorf("targets ls text must carry %q; got:\n%s", want, out)
		}
	}

	one := newDatalaneRecorder(t, http.StatusOK, gitpublishTargetBody)
	out, _, err = execDatalane(t, "", datalaneArgs(one, "gitpublish", "targets", "get", "gpt_1")...)
	if err != nil {
		t.Fatalf("targets get: %v", err)
	}
	if !strings.Contains(out, "gpt_1") || !strings.Contains(out, "push_prefix") {
		t.Errorf("targets get text must carry the id and the push prefix; got:\n%s", out)
	}

	obs := newDatalaneRecorder(t, http.StatusOK,
		`{"items":[{"attempt":1,"source":"host","result":"applied","at":"2026-10-06T10:00:00Z"}]}`)
	out, _, err = execDatalane(t, "", datalaneArgs(obs, "gitpublish", "intents", "observations", "gpi_1")...)
	if err != nil {
		t.Fatalf("observations: %v", err)
	}
	for _, want := range []string{"host", "applied", "2026-10-06T10:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("observations text must carry %q; got:\n%s", want, out)
		}
	}
}

// TestGitpublish202InTextModeAndOnReconcile: the render-before-exit claim holds
// in BOTH output modes, and reconcile — the recovery verb — answers 202 with
// the same contract, not a silent success.
func TestGitpublish202InTextModeAndOnReconcile(t *testing.T) {
	uncertain := strings.Replace(gitpublishIntentBody, `"state":"applied"`, `"state":"uncertain"`, 1)
	for _, verb := range []struct {
		name string
		args []string
	}{
		{"push text", []string{"gitpublish", "push", "gpt_1", "--operation-id", "op-4",
			"--ref", "refs/heads/agent/run-1", "--commit", strings.Repeat("a", 40), "--tree", strings.Repeat("b", 40)}},
		{"reconcile", []string{"gitpublish", "intents", "reconcile", "gpi_1"}},
	} {
		t.Run(verb.name, func(t *testing.T) {
			prepareDatalaneCLITest(t)
			rec := newDatalaneRecorder(t, http.StatusAccepted, uncertain)

			out, _, err := execDatalane(t, "", datalaneArgs(rec, verb.args...)...)
			if err == nil {
				t.Fatalf("%s answering 202 must not exit 0", verb.name)
			}
			if got := exitcode.From(err); got != exitcode.Degraded {
				t.Errorf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
			}
			if !strings.Contains(out, "uncertain") {
				t.Errorf("%s must render the intent in text mode before exiting; got:\n%s", verb.name, out)
			}
		})
	}
}

// TestGitpublishAliasesResolveToTheSameRoutes: the aliases the help advertises
// (list on both ls verbs, status on intents get) resolve to the same routes as
// their primary spellings.
func TestGitpublishAliasesResolveToTheSameRoutes(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusOK, `{"items":[]}`)

	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "intents", "list", "--target", "gpt_1")...); err != nil {
		t.Fatalf("intents list alias: %v", err)
	}
	if got := rec.last(t).Path; got != "/v1/m/gitpublish/intents" {
		t.Errorf("intents list path = %q, want /v1/m/gitpublish/intents", got)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "gitpublish", "intents", "status", "gpi_1")...); err != nil {
		t.Fatalf("intents status alias: %v", err)
	}
	if got := rec.last(t).Path; got != "/v1/m/gitpublish/intents/gpi_1" {
		t.Errorf("intents status path = %q, want /v1/m/gitpublish/intents/gpi_1", got)
	}
}

// TestGitpublishFamilyTimeoutCoversTheEngineDeadline pins the family's one
// default: the shared 10s would cut a healthy dispatch off mid-flight and turn
// it into the ambiguous Indeterminate case the timeout exists to avoid.
func TestGitpublishFamilyTimeoutCoversTheEngineDeadline(t *testing.T) {
	cmd := newGitpublishCmd()
	f := cmd.PersistentFlags().Lookup("timeout")
	if f == nil {
		t.Fatal("the gitpublish family must carry the shared --timeout flag")
	}
	if f.DefValue != "4m15s" {
		t.Errorf("timeout default = %q, want 4m15s (including settlement and uncertainty observation)", f.DefValue)
	}
}
