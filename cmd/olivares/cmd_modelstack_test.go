// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// connect returns the connection flags every case in this file shares.
func connect(server string) []string {
	return []string{"--server", server, "--token", "secret-token", "--tenant", "tenant-a"}
}

func withConnect(server string, args ...string) []string {
	return append(append([]string(nil), args...), connect(server)...)
}

// --- DENY: the control plane refuses, and the CLI says which refusal it was ---

// TestServerRefusalsCarryTheirOwnExitCode is the classification contract these
// three families need, measured status by status.
//
// The four codes httpErr already assigns are here as a REGRESSION guard, not as
// news: the point of extending httpErr rather than replacing it is that 401 and
// 404 keep meaning from `models` exactly what they mean from `agent`. The rows
// that are new — 400, 402, 410, 422, 423, 429 — are the ones httpErr left at the
// generic 1, which is the same defect C08-03 removed from four other clients.
func TestServerRefusalsCarryTheirOwnExitCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
		why    string
	}{
		{http.StatusBadRequest, exitcode.Usage, "a rejected document is the caller's invocation"},
		{http.StatusUnauthorized, exitcode.Auth, "the plane rejected the caller"},
		{http.StatusForbidden, exitcode.Auth, "the plane rejected the caller"},
		{http.StatusNotFound, exitcode.NotFound, "the entity does not exist"},
		{http.StatusConflict, exitcode.Conflict, "the request contradicts current state"},
		{http.StatusPaymentRequired, exitcode.Conflict, "an enforcing budget blocked it"},
		{http.StatusTooManyRequests, exitcode.Conflict, "an enforcing budget throttled it"},
		{http.StatusGone, exitcode.Conflict, "the grant expired"},
		{http.StatusUnprocessableEntity, exitcode.Conflict, "the document is semantically invalid"},
		{http.StatusLocked, exitcode.Conflict, "the resource is locked"},
		{http.StatusInternalServerError, exitcode.Server, "the plane failed"},
		{http.StatusServiceUnavailable, exitcode.Server, "the route is deny-closed without its executor"},
	} {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			prepareModelstackCLITest(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"refused"}}`))
			}))
			defer srv.Close()
			stdout, _, err := execRoot(t, withConnect(srv.URL, "models", "routing", "ls")...)
			assertExitCode(t, err, tc.want, fmt.Sprintf("HTTP %d (%s)", tc.status, tc.why))
			// A refused command must print NOTHING on stdout: a script that pipes
			// `-o json` into a parser must not receive half a document.
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("HTTP %d printed to stdout on failure:\n%s", tc.status, stdout)
			}
		})
	}
}

// TestBudgetDenialNamesWhichRemedyApplies pins the difference between the two
// budget statuses. They share an exit code because they are the same CLASS of
// refusal, but they do not share a remedy, and a script that backs off on a
// block would retry forever.
func TestBudgetDenialNamesWhichRemedyApplies(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusPaymentRequired: "raise or re-scope the budget",
		http.StatusTooManyRequests: "retry with backoff is meaningful",
	} {
		prepareModelstackCLITest(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"resolved":false,"budget_action":"block"}`))
		}))
		_, _, err := execRoot(t, withConnect(srv.URL, "models", "routing", "resolve", "pol-1")...)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("HTTP %d error = %v, want it to name the remedy %q", status, err, want)
		}
	}
}

// TestDestructiveVerbsRefuseUnattendedConsent covers EVERY destructive verb in
// the lot, in both directions: without --yes the command must refuse before
// opening a connection, and with --yes the very same invocation must reach the
// control plane exactly once.
//
// The positive half is what makes the negative half mean something. "zero
// requests" is also what a command that cannot run at all produces.
func TestDestructiveVerbsRefuseUnattendedConsent(t *testing.T) {
	destructive := [][]string{}
	for _, route := range modelstackRoutes() {
		if route.method == http.MethodDelete {
			destructive = append(destructive, route.args)
		}
	}
	// The device approval is destructive in the sense that matters — it mints a
	// credential — without being a DELETE, so it is added by name.
	destructive = append(destructive,
		[]string{"inference-proxy", "device", "approve", "--user-code", "ABCD-EFGH", "--yes"})
	if len(destructive) != 15 {
		t.Fatalf("found %d verbs needing consent, want 14 DELETEs + the device approval", len(destructive))
	}
	for _, args := range destructive {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			prepareModelstackCLITest(t)
			rec := &modelstackStub{}
			srv := newModelstackStub(t, rec)
			defer srv.Close()

			without := make([]string, 0, len(args))
			for _, a := range args {
				if a != "--yes" {
					without = append(without, a)
				}
			}
			_, _, err := execRoot(t, withConnect(srv.URL, without...)...)
			assertExitCode(t, err, exitcode.Usage, "olivares "+name+" without --yes")
			if len(rec.requests) != 0 {
				t.Fatalf("olivares %s reached the control plane without consent: %v", name, rec.requests)
			}

			if _, stderr, err := execRoot(t, withConnect(srv.URL, args...)...); err != nil {
				t.Fatalf("olivares %s with --yes: %v (stderr %q)", name, err, stderr)
			}
			if len(rec.requests) != 1 {
				t.Fatalf("olivares %s with --yes made %d requests, want 1: %v", name, len(rec.requests), rec.requests)
			}
		})
	}
}

// TestAPositionalIdentifierCannotRetargetTheRequest is the escaping witness.
// A positional identifier is operator text; unescaped, "../.." walks out of the
// collection and addresses another module's route with the caller's credential.
//
// IT MEASURES THE REQUEST LINE, NOT r.URL.Path, and the first version of this
// test measured the wrong one. net/http DECODES %2F back into "/" in
// r.URL.Path, so a correctly escaped identifier reads there exactly like a
// traversal that got through — the test failed against an escaping that was
// working. What the engine routes on is the escaped form: chi reads
// r.URL.RawPath whenever it is non-empty (chi v5 mux.go:452-458), and RawPath is
// non-empty precisely when the escaping changed something.
func TestAPositionalIdentifierCannotRetargetTheRequest(t *testing.T) {
	prepareModelstackCLITest(t)
	rec := &modelstackStub{}
	srv := newModelstackStub(t, rec)
	defer srv.Close()
	if _, _, err := execRoot(t, withConnect(srv.URL,
		"models", "routing", "rm", "../../v1/system/orgs/t_victim", "--yes")...); err != nil {
		t.Fatalf("the request should have been made (and landed inside the collection): %v", err)
	}
	if len(rec.requestURIs) != 1 {
		t.Fatalf("requests = %v, want exactly one", rec.requests)
	}
	uri, rawPath := rec.requestURIs[0], rec.rawPaths[0]
	// The request line is what traveled. No traversal in it, and the separators
	// of the injected value arrived percent-encoded.
	if strings.Contains(uri, "../") || strings.Contains(uri, "/v1/system/orgs") {
		t.Fatalf("request line = %q: the identifier walked out of its collection", uri)
	}
	if !strings.Contains(uri, "%2F") {
		t.Fatalf("request line = %q: the separators inside the identifier were not escaped", uri)
	}
	// And the string the engine's router matches on stays inside the collection.
	if rawPath == "" {
		t.Fatal("RawPath is empty, so this case never exercised the escaping at all")
	}
	if !strings.HasPrefix(rawPath, modelsAPIBase+"/routing-policies/") ||
		strings.Contains(rawPath, "/v1/system/orgs") {
		t.Fatalf("routed path = %q, want it to stay under %s/routing-policies/", rawPath, modelsAPIBase)
	}
	// POSITIVE CONTROL: an ordinary identifier is NOT mangled, so the escaping
	// above is not simply "the CLI corrupts every identifier".
	rec2 := &modelstackStub{}
	srv2 := newModelstackStub(t, rec2)
	defer srv2.Close()
	if _, _, err := execRoot(t, withConnect(srv2.URL, "models", "routing", "rm", "pol-1", "--yes")...); err != nil {
		t.Fatalf("an ordinary identifier failed: %v", err)
	}
	if len(rec2.paths) != 1 || rec2.paths[0] != modelsAPIBase+"/routing-policies/pol-1" {
		t.Fatalf("an ordinary identifier reached %v, want %s/routing-policies/pol-1",
			rec2.paths, modelsAPIBase)
	}
}

// TestABlankIdentifierIsRefusedRatherThanAddressingTheCollection: DELETE on a
// collection is a different request from DELETE on a member, and an empty
// argument must not silently become the first.
func TestABlankIdentifierIsRefusedRatherThanAddressingTheCollection(t *testing.T) {
	prepareModelstackCLITest(t)
	rec := &modelstackStub{}
	srv := newModelstackStub(t, rec)
	defer srv.Close()
	_, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "rm", "   ", "--yes")...)
	assertExitCode(t, err, exitcode.Usage, "a blank identifier")
	if len(rec.requests) != 0 {
		t.Fatalf("a blank identifier reached the control plane: %v", rec.requests)
	}
}

// TestRejectedDocumentsNeverReachTheControlPlane: every --data failure mode is
// a usage error decided locally, so a malformed document costs no round trip and
// cannot be half-applied.
func TestRejectedDocumentsNeverReachTheControlPlane(t *testing.T) {
	dir := t.TempDir()
	oversize := filepath.Join(dir, "big.json")
	big := append([]byte(`{"pad":"`), append(make([]byte, maxModelstackRequestSize), []byte(`"}`)...)...)
	for i := 9; i < len(big)-2; i++ {
		big[i] = 'x'
	}
	if err := os.WriteFile(oversize, big, 0o600); err != nil {
		t.Fatalf("write oversize document: %v", err)
	}
	for _, tc := range []struct {
		name string
		data []string
	}{
		{"absent", nil},
		{"not JSON", []string{"--data", "this is not json"}},
		{"truncated JSON", []string{"--data", `{"name":`}},
		{"missing file", []string{"--data", "@" + filepath.Join(dir, "does-not-exist.json")}},
		{"empty @", []string{"--data", "@"}},
		{"larger than the engine accepts", []string{"--data", "@" + oversize}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareModelstackCLITest(t)
			rec := &modelstackStub{}
			srv := newModelstackStub(t, rec)
			defer srv.Close()
			args := append([]string{"models", "owned", "create"}, tc.data...)
			_, _, err := execRoot(t, withConnect(srv.URL, args...)...)
			assertExitCode(t, err, exitcode.Usage, "--data "+tc.name)
			if len(rec.requests) != 0 {
				t.Fatalf("--data %s reached the control plane: %v", tc.name, rec.requests)
			}
		})
	}
}

// TestANegativeLimitIsRefusedLocally: the engine's listQuery silently ignores an
// unparseable ?limit (modules/models/dto.go:178-182), so a negative one would
// return the DEFAULT page while the operator believes they asked for something
// else. Refuse it here instead.
func TestANegativeLimitIsRefusedLocally(t *testing.T) {
	prepareModelstackCLITest(t)
	rec := &modelstackStub{}
	srv := newModelstackStub(t, rec)
	defer srv.Close()
	_, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls", "--limit", "-1")...)
	assertExitCode(t, err, exitcode.Usage, "--limit -1")
	if len(rec.requests) != 0 {
		t.Fatalf("a negative --limit reached the control plane: %v", rec.requests)
	}
}

// TestAnOversizedResponseIsRefusedRatherThanTruncated: a silently short export
// is a wrong answer that looks like a right one.
func TestAnOversizedResponseIsRefusedRatherThanTruncated(t *testing.T) {
	prepareModelstackCLITest(t)
	payload := make([]byte, maxModelstackResponseSize+64)
	for i := range payload {
		payload[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	stdout, _, err := execRoot(t, withConnect(srv.URL, "finops", "spend", "export")...)
	assertExitCode(t, err, exitcode.Server, "an oversized response")
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want it to say the response exceeded the limit", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("a refused oversized response still wrote %d bytes to stdout", len(stdout))
	}
}

// TestAListResponseWithoutItemsIsAnErrorNotAnEmptyList: "the field is absent"
// and "there is nothing here" are different facts, and a report that flattens
// the first into the second reports a zero that was never measured.
//
// IT ASSERTS THE MESSAGE, not just the exit code, and the reason is a mutant
// that survived the first version of this test. Removing the guard leaves an
// absent array to fail later, in the decoder, with the SAME exit 6 — so an
// exit-code-only assertion passed with the guard deleted. The code is the same;
// only the sentence distinguishes "the control plane answered a shape we do not
// know" from "the bytes would not decode".
func TestAListResponseWithoutItemsIsAnErrorNotAnEmptyList(t *testing.T) {
	prepareModelstackCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"has_more":false}`))
	}))
	defer srv.Close()
	stdout, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls")...)
	assertExitCode(t, err, exitcode.Server, "a list response with no items array")
	if !strings.Contains(err.Error(), `no "items" array`) {
		t.Fatalf("error = %v, want it to name the array the response is missing", err)
	}
	if strings.Contains(stdout, "no owned models") {
		t.Fatalf("a shape violation was rendered as an empty list:\n%s", stdout)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("a shape violation still wrote to stdout:\n%s", stdout)
	}
}

// TestTheCredentialSurvivesNeitherTheMessageNorTheExitCode: the redaction must
// remove the token from an error text WITHOUT collapsing its classification —
// the exact pair C08-03 established.
// The credential value is chosen to be EXACTLY the malformed pin below, because
// otherwise this test is vacuous. The first version used an unrelated token: the
// error text never contained it, so "the token is absent from the message" was
// true before the redaction ran and would stay true if the redaction were
// deleted. A witness that cannot fail proves nothing.
//
// It is 12 characters, which is minRedactableCredential exactly — the shortest
// value that is redacted by substring rather than withheld whole — so the
// assertions below observe the substitution itself.
const modelstackRedactionProbe = "not-a-digest"

func TestTheCredentialSurvivesNeitherTheMessageNorTheExitCode(t *testing.T) {
	prepareModelstackCLITest(t)
	// TLS, because the pin DECODER is only reached on an https server: over http
	// cliTransport refuses earlier, with a message that never contains the pin.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, _, err := execRoot(t, "models", "routing", "ls",
		"--server", srv.URL, "--token", modelstackRedactionProbe,
		"--pin-sha256", modelstackRedactionProbe)
	assertExitCode(t, err, exitcode.Usage, "a malformed pin whose text is the credential")
	if strings.Contains(err.Error(), modelstackRedactionProbe) {
		t.Fatalf("the credential survived into the error message: %v", err)
	}
	// POSITIVE CONTROL on the redaction itself: the message must show that a
	// value was removed, not merely fail to contain one.
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("the message carries no redaction marker, so nothing was removed: %v", err)
	}
}

// --- PERMIT: with the right to call it, it works, and on the wire ------------

func TestListVerbsRenderATableAndPreserveTheRawDocument(t *testing.T) {
	prepareModelstackCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		if got := r.Header.Get("X-Olivares-Tenant"); got != "tenant-a" {
			t.Errorf("X-Olivares-Tenant = %q, want tenant-a", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id": "om-1", "name": "internal-classifier", "kind": "finetune",
				"base_ref": "claude-haiku-4", "visibility": "private", "status": "active",
				"a_field_the_cli_does_not_model": "must survive -o json",
			}},
			"has_more":   false,
			"request_id": "req-preserved",
		})
	}))
	defer srv.Close()

	text, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls")...)
	if err != nil {
		t.Fatalf("models owned ls: %v", err)
	}
	for _, want := range []string{"ID", "NAME", "KIND", "BASE", "VISIBILITY", "STATUS",
		"om-1", "internal-classifier", "finetune", "claude-haiku-4", "private", "active"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}

	raw, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("models owned ls -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("json output is not valid JSON: %v\n%s", err, raw)
	}
	if decoded["request_id"] != "req-preserved" {
		t.Errorf("an envelope field the CLI does not model was dropped from -o json:\n%s", raw)
	}
	items, _ := decoded["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one", decoded["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["a_field_the_cli_does_not_model"] != "must survive -o json" {
		t.Errorf("an item field the CLI does not model was dropped from -o json:\n%s", raw)
	}
}

// TestAnEmptyListSaysSoInItsOwnWords: rendering zero rows as zero bytes makes
// "nothing here" indistinguishable from "the command did nothing".
func TestAnEmptyListSaysSoInItsOwnWords(t *testing.T) {
	prepareModelstackCLITest(t)
	rec := &modelstackStub{}
	srv := newModelstackStub(t, rec)
	defer srv.Close()
	text, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls")...)
	if err != nil {
		t.Fatalf("models owned ls: %v", err)
	}
	if !strings.Contains(text, "no owned models registered") {
		t.Fatalf("an empty list printed %q, want this command's own empty note", text)
	}
}

// TestDeclaredFiltersReachTheControlPlaneAsQueryParameters: a filter flag that
// did not travel would silently return an unfiltered result, which is the
// failure mode that looks most like success.
func TestDeclaredFiltersReachTheControlPlaneAsQueryParameters(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"models", "owned", "ls", "--kind", "finetune", "--status", "active"}, "kind=finetune&status=active"},
		{[]string{"models", "access", "ls", "--subject-kind", "role", "--subject-ref", "admin"}, "subject_kind=role&subject_ref=admin"},
		{[]string{"finops", "spend", "summary", "--since", "2026-08-01T00:00:00Z"}, "since=2026-08-01T00%3A00%3A00Z"},
		{[]string{"finops", "rates", "ls", "--provider", "anthropic", "--model", "claude-opus-4"}, "model=claude-opus-4&provider=anthropic"},
		{[]string{"models", "aibom", "get", "om-1", "--format", "spdx"}, "format=spdx"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			prepareModelstackCLITest(t)
			rec := &modelstackStub{}
			srv := newModelstackStub(t, rec)
			defer srv.Close()
			if _, _, err := execRoot(t, withConnect(srv.URL, tc.args...)...); err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if len(rec.queries) != 1 || rec.queries[0] != tc.want {
				t.Fatalf("query = %q, want %q", rec.queries, tc.want)
			}
		})
	}
}

// TestPaginationFlagsExistOnlyWhereTheEngineReadsThem is the honesty half of the
// pagination work: `finops cost-centers mappings ls` is served under a fixed
// server-side cap and ignores ?cursor and ?limit
// (modules/finops/costcenter.go:306-309), so offering the flags there would tell
// the operator their page was theirs to choose.
func TestPaginationFlagsExistOnlyWhereTheEngineReadsThem(t *testing.T) {
	root := newRootCmd()
	paged, _, err := root.Find([]string{"models", "owned", "ls"})
	if err != nil || paged == nil {
		t.Fatalf("models owned ls does not resolve: %v", err)
	}
	for _, flag := range []string{"limit", "cursor", "all"} {
		if paged.Flags().Lookup(flag) == nil {
			t.Errorf("models owned ls has no --%s, but the engine reads it", flag)
		}
	}
	capped, _, err := root.Find([]string{"finops", "cost-centers", "mappings", "ls"})
	if err != nil || capped == nil {
		t.Fatalf("finops cost-centers mappings ls does not resolve: %v", err)
	}
	for _, flag := range []string{"limit", "cursor", "all"} {
		if capped.Flags().Lookup(flag) != nil {
			t.Errorf("finops cost-centers mappings ls offers --%s, but the engine ignores it there", flag)
		}
	}
}

// TestAPartialPageSaysSoOnStderrNamingTheCursor: the note has to be readable by
// a human and invisible to a pipeline, which is exactly what stderr is for.
func TestAPartialPageSaysSoOnStderrNamingTheCursor(t *testing.T) {
	prepareModelstackCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":    []map[string]any{{"id": "om-1"}},
			"cursor":   "opaque-cursor-42",
			"has_more": true,
		})
	}))
	defer srv.Close()
	stdout, stderr, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls")...)
	if err != nil {
		t.Fatalf("models owned ls: %v", err)
	}
	if !strings.Contains(stderr, "opaque-cursor-42") {
		t.Fatalf("stderr does not name the cursor to continue from:\n%s", stderr)
	}
	if strings.Contains(stdout, "opaque-cursor-42") || strings.Contains(stdout, "note:") {
		t.Fatalf("the truncation note leaked into stdout, where a parser reads it:\n%s", stdout)
	}
}

// TestAllFollowsTheCursorAndMergesEveryPage: the permit half of --all.
func TestAllFollowsTheCursorAndMergesEveryPage(t *testing.T) {
	prepareModelstackCLITest(t)
	var cursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		w.Header().Set("Content-Type", "application/json")
		switch cursor {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "om-1"}}, "cursor": "c2", "has_more": true})
		case "c2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "om-2"}}, "cursor": "c3", "has_more": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "om-3"}}, "has_more": false})
		}
	}))
	defer srv.Close()
	out, stderr, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls", "--all", "-o", "json")...)
	if err != nil {
		t.Fatalf("models owned ls --all: %v", err)
	}
	if len(cursors) != 3 {
		t.Fatalf("pages fetched = %d (%v), want 3", len(cursors), cursors)
	}
	var decoded struct {
		Items   []map[string]any `json:"items"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("merged output is not valid JSON: %v\n%s", err, out)
	}
	if len(decoded.Items) != 3 {
		t.Fatalf("merged items = %d, want the 3 that were served: %s", len(decoded.Items), out)
	}
	if decoded.HasMore {
		t.Fatal("the merged page claims has_more, but --all reached the end")
	}
	if strings.Contains(stderr, "note:") {
		t.Fatalf("--all reached the end and still printed a truncation note:\n%s", stderr)
	}
}

// TestAllRefusesToPageForever: a control plane that repeats a cursor would hang
// the loop, and a hang in a script reports nothing at all.
func TestAllRefusesToPageForever(t *testing.T) {
	prepareModelstackCLITest(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "om-1"}}, "cursor": "same", "has_more": true})
	}))
	defer srv.Close()
	_, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls", "--all")...)
	assertExitCode(t, err, exitcode.Server, "a control plane repeating one cursor")
	if !strings.Contains(err.Error(), "same cursor twice") {
		t.Fatalf("error = %v, want it to name the repeated cursor", err)
	}
	if requests > 3 {
		t.Fatalf("the loop made %d requests before refusing; it should stop on the repeat", requests)
	}
}

// TestADocumentReachesTheEngineByteIdenticalFromEverySpelling: inline, @file and
// stdin are the three ways a script supplies a payload, and all three must
// deliver the same bytes with the JSON content type.
func TestADocumentReachesTheEngineByteIdenticalFromEverySpelling(t *testing.T) {
	const document = `{"name":"nightly","enabled":true,"spec":{"strategy":"cheapest"}}`
	file := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(file, []byte(document), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	for _, tc := range []struct {
		name  string
		data  string
		stdin string
	}{
		{"inline", document, ""},
		{"@file", "@" + file, ""},
		{"stdin", "-", document},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareModelstackCLITest(t)
			var (
				gotBody        string
				gotContentType string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, 4096)
				n, _ := r.Body.Read(buf)
				gotBody = string(buf[:n])
				gotContentType = r.Header.Get("Content-Type")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"pol-1"}`))
			}))
			defer srv.Close()
			root := newRootCmd()
			root.SetIn(strings.NewReader(tc.stdin))
			var out, errb strings.Builder
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(withConnect(srv.URL, "models", "routing", "create", "--data", tc.data))
			if err := root.Execute(); err != nil {
				t.Fatalf("models routing create (--data %s): %v (stderr %q)", tc.name, err, errb.String())
			}
			if gotBody != document {
				t.Fatalf("body = %q, want the document byte-identical: %q", gotBody, document)
			}
			if gotContentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", gotContentType)
			}
			if !strings.Contains(out.String(), "pol-1") {
				t.Fatalf("the created document was not reported back:\n%s", out.String())
			}
		})
	}
}

// TestANonJSONPayloadGoesToStdoutVerbatim: an export is bytes an operator
// redirects to a file. Re-encoding it would corrupt it, and printing the note on
// stdout would corrupt it just as thoroughly.
func TestANonJSONPayloadGoesToStdoutVerbatim(t *testing.T) {
	prepareModelstackCLITest(t)
	const card = "# internal-classifier\n\n| field | value |\n| --- | --- |\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(card))
	}))
	defer srv.Close()
	stdout, stderr, err := execRoot(t, withConnect(srv.URL,
		"models", "aibom", "card", "om-1", "--format", "md", "-o", "json")...)
	if err != nil {
		t.Fatalf("models aibom card: %v", err)
	}
	if stdout != card {
		t.Fatalf("stdout = %q, want the payload byte-identical", stdout)
	}
	if !strings.Contains(stderr, "text/markdown") {
		t.Fatalf("stderr does not name the media type it returned instead of JSON:\n%s", stderr)
	}
}

// TestADeleteWithNoBodyStillReportsWhatItDid: a 204 has nothing to render, and
// zero bytes on stdout is indistinguishable from a command that did nothing.
func TestADeleteWithNoBodyStillReportsWhatItDid(t *testing.T) {
	prepareModelstackCLITest(t)
	rec := &modelstackStub{}
	srv := newModelstackStub(t, rec)
	defer srv.Close()
	text, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "rm", "om-1", "--yes")...)
	if err != nil {
		t.Fatalf("models owned rm: %v", err)
	}
	if !strings.Contains(text, "deleted owned model om-1") {
		t.Fatalf("text output = %q, want it to name what was deleted", text)
	}
	raw, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "rm", "om-1", "--yes", "-o", "json")...)
	if err != nil {
		t.Fatalf("models owned rm -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("json output is not valid JSON: %v\n%s", err, raw)
	}
	if decoded["deleted"] != true {
		t.Fatalf("json output does not report the deletion: %s", raw)
	}
}

// TestAnAcceptedIngestReportsThatItWasOnlyAccepted: the three ingest routes
// answer 202. "accepted for processing" and "processed" are different facts to
// a script that then reads the report the ingest was supposed to move.
func TestAnAcceptedIngestReportsThatItWasOnlyAccepted(t *testing.T) {
	prepareModelstackCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	text, _, err := execRoot(t, withConnect(srv.URL, "finops", "cost", "ingest", "--data", `{"cost_micro_usd":1}`)...)
	if err != nil {
		t.Fatalf("finops cost ingest: %v", err)
	}
	if !strings.Contains(text, "202") || !strings.Contains(text, "accepted") {
		t.Fatalf("text output = %q, want it to say the ingest was accepted with its status", text)
	}
}

// TestAReportRendersBothFormsFromTheSameBytes: the text form is derived from the
// SAME document -o json emits, so the two cannot disagree about what the control
// plane said.
func TestAReportRendersBothFormsFromTheSameBytes(t *testing.T) {
	prepareModelstackCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resolved":true,"policy":"nightly","primary":{"provider_ref":"anthropic","model_ref":"claude-opus-4"},"fallbacks":[],"chain":[]}`))
	}))
	defer srv.Close()
	text, _, err := execRoot(t, withConnect(srv.URL, "models", "routing", "resolve", "pol-1")...)
	if err != nil {
		t.Fatalf("models routing resolve: %v", err)
	}
	for _, want := range []string{"resolved", "true", "primary.model_ref", "claude-opus-4", "fallbacks"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report missing %q:\n%s", want, text)
		}
	}
	raw, _, err := execRoot(t, withConnect(srv.URL, "models", "routing", "resolve", "pol-1", "-o", "json")...)
	if err != nil {
		t.Fatalf("models routing resolve -o json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("json output is not valid JSON: %v\n%s", err, raw)
	}
	if decoded["policy"] != "nightly" {
		t.Fatalf("json output lost a field the text form showed: %s", raw)
	}
}

// TestATableCellCannotEmitControlCharacters: every cell is server-controlled
// text landing in a terminal.
func TestATableCellCannotEmitControlCharacters(t *testing.T) {
	prepareModelstackCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":    []map[string]any{{"id": "om-1", "name": "clean\x1b[31mred\x07"}},
			"has_more": false,
		})
	}))
	defer srv.Close()
	text, _, err := execRoot(t, withConnect(srv.URL, "models", "owned", "ls")...)
	if err != nil {
		t.Fatalf("models owned ls: %v", err)
	}
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Fatalf("a control character from the control plane reached the terminal: %q", text)
	}
	if !strings.Contains(text, "red") {
		t.Fatalf("the value was dropped instead of sanitized:\n%s", text)
	}
}
