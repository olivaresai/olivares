// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Tests for the observe-and-report lane (C09 lot 4): reporting, notify, health,
// accessmap, observability, consoleviews, adoption, identity, inventory, posture.
//
// EVERY DENY CASE CARRIES A REQUEST COUNTER AND A PAIRED POSITIVE CONTROL.
// Without the counter, "it refused" is also satisfied by a command that is simply
// broken; without the positive control, "it refused" is satisfied by a command
// that refuses everything. The pair is what makes the refusal evidence.

// observeSpy is a stub control plane that records what actually reached the wire.
type observeSpy struct {
	mu       sync.Mutex
	requests []observedRequest
	status   int
	body     string
	ctype    string
	srv      *httptest.Server
}

type observedRequest struct {
	method string
	path   string
	// rawURI is the REQUEST LINE AS IT ARRIVED. r.URL.Path is Go's decoded
	// convenience view: it shows "%2F" as "/", so a correctly escaped id looks
	// exactly like an unescaped one there. The first version of the traversal
	// test asserted on r.URL.Path and reported six false failures for a guard
	// that was working — the witness was measuring a view of the bytes rather
	// than the bytes.
	rawURI string
	query  url.Values
	body   string
	ctype  string
	accept string
	tenant string
	auth   string
}

func newObserveSpy(t *testing.T, status int, body string) *observeSpy {
	t.Helper()
	s := &observeSpy{status: status, body: body, ctype: "application/json"}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		s.mu.Lock()
		s.requests = append(s.requests, observedRequest{
			method: r.Method, path: r.URL.Path, rawURI: r.RequestURI,
			query: r.URL.Query(), body: string(raw),
			ctype:  r.Header.Get("Content-Type"),
			accept: r.Header.Get("Accept"),
			tenant: r.Header.Get("X-Olivares-Tenant"),
			auth:   r.Header.Get("Authorization"),
		})
		st, bd, ct := s.status, s.body, s.ctype
		s.mu.Unlock()
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(st)
		_, _ = io.WriteString(w, bd)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *observeSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *observeSpy) last(t *testing.T) observedRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request reached the control plane, so there is nothing to assert about the wire")
	}
	return s.requests[len(s.requests)-1]
}

// observeArgs appends the client flags every verb in this lane needs.
func observeArgs(server string, args ...string) []string {
	return append(args, "--server", server, "--token", "test-token", "--tenant", "tenant-a")
}

// laneReadVerbs is one representative READ per family, with the route it must
// reach. It is the table behind the credential, refusal and routing tests, so
// every family is covered by each of them rather than one family standing in for
// the other nine.
var laneReadVerbs = []struct {
	family string
	args   []string
	path   string
}{
	{"reporting", []string{"reporting", "reports", "ls"}, "/v1/m/reporting/reports"},
	{"notify", []string{"notify", "routes", "ls"}, "/v1/m/notify/routes"},
	{"health", []string{"health", "status"}, "/v1/m/health/status"},
	{"accessmap", []string{"accessmap", "graph"}, "/v1/m/accessmap/graph"},
	{"observability", []string{"observability", "traces", "ls"}, "/v1/m/observability/traces"},
	{"consoleviews", []string{"consoleviews", "ls"}, "/v1/m/consoleviews/views"},
	{"adoption", []string{"adoption", "summary"}, "/v1/m/adoption/summary"},
	{"identity", []string{"identity", "sso"}, "/v1/m/identity/sso"},
	{"inventory", []string{"inventory", "summary"}, "/v1/m/inventory/summary"},
	{"posture", []string{"posture", "export"}, "/v1/m/posture/export"},
	{"governance", []string{"governance", "killswitch", "ls"}, "/v1/m/governance/killswitch"},
	{"capabilities", []string{"capabilities", "servers", "ls"}, "/v1/m/capabilities/servers"},
}

// ---- DENY 1: no credential, and the paired control ---------------------------------

// TestLaneRefusesWithoutACredentialBeforeOpeningAConnection is the strongest
// property in the file: a caller with no token is refused with exit 2 AND the
// server receives NOTHING — so an unauthenticated caller cannot even learn that
// the host answers.
//
// The positive control is in the same subtest, so "zero requests" cannot be
// satisfied by a command that never makes any.
func TestLaneRefusesWithoutACredentialBeforeOpeningAConnection(t *testing.T) {
	for _, v := range laneReadVerbs {
		t.Run(v.family, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, "{}")

			// DENY: server and tenant given, credential withheld. An empty
			// --token is explicit, so no ambient context can supply one.
			args := append(append([]string{}, v.args...),
				"--server", spy.srv.URL, "--tenant", "tenant-a", "--token", "")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("a verb with no credential must not succeed")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage): a missing credential is a bad invocation, not a server failure", got, exitcode.Usage)
			}
			if n := spy.count(); n != 0 {
				t.Errorf("%d request(s) reached the server; want 0 — a caller with no credential must not learn the host answers", n)
			}

			// POSITIVE CONTROL: the same verb, with a credential, works.
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL, v.args...)...); err != nil {
				t.Fatalf("with a credential the verb must succeed, got %v (exit %d)", err, exitcode.From(err))
			}
			if n := spy.count(); n != 1 {
				t.Errorf("%d request(s) after the control; want exactly 1", n)
			}
		})
	}
}

// TestMissingClientValuesNameWhereTheyAreConfigured is the OTHER half of the
// precondition, and it exists because a mutation run found the first half blind.
//
// Deleting the `resolved.Server == ""` arm did NOT fail the credential test
// above: cliTransport refuses a missing server too, also with exit 2. So the
// precondition buys nothing in EXIT CODE — what it buys is the MESSAGE. Its
// missingCLIValueError names the flag, the environment variable, whether a
// client context is active, and where the config file lives (clitransport.go,
// the E7 fix); cliTransport's fallback says only "set --server,
// OLIVARES_SERVER_URL, or an active client context".
//
// That difference is the whole point: "no server: set --server" after a
// successful `olivares auth login` is how an operator concludes the CLI is
// broken. So this witness asserts the DIAGNOSTIC, which is the thing that would
// actually be lost — and it is what makes the removal of that arm detectable.
func TestMissingClientValuesNameWhereTheyAreConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"server", []string{"health", "status", "--server", "", "--token", "tok", "--tenant", "t"}, "no server:"},
		{"tenant", []string{"health", "status", "--server", "http://127.0.0.1:1", "--token", "tok", "--tenant", ""}, "no tenant:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := execRoot(t, tc.args...)
			if err == nil {
				t.Fatal("a missing client value must not succeed")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("the message must name what is missing (%q), got: %s", tc.want, msg)
			}
			// The two halves of the richer diagnostic: whether a context is
			// active, and where the config that would supply one lives. Losing
			// either is losing the reason this check exists.
			if !strings.Contains(msg, "context") {
				t.Errorf("the message must say whether a client context is active, got: %s", msg)
			}
			if !strings.Contains(msg, "config:") {
				t.Errorf("the message must name the config file that would supply the value, got: %s", msg)
			}
		})
	}
}

// ---- PERMIT 1: the wire ------------------------------------------------------------

// TestLaneReadsReachTheirModuleRoute pins method, path and the tenant header for
// one read per family. A path typo is otherwise invisible: the stub answers 200
// to everything, so the command would look perfectly healthy while addressing a
// route that does not exist.
func TestLaneReadsReachTheirModuleRoute(t *testing.T) {
	for _, v := range laneReadVerbs {
		t.Run(v.family, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, "{}")
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL, v.args...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			got := spy.last(t)
			if got.method != http.MethodGet {
				t.Errorf("method = %s, want GET", got.method)
			}
			if got.path != v.path {
				t.Errorf("path = %s, want %s", got.path, v.path)
			}
			if got.tenant != "tenant-a" {
				t.Errorf("X-Olivares-Tenant = %q, want tenant-a: the engine resolves the tenant from this header", got.tenant)
			}
			if got.auth == "" {
				t.Error("no Authorization header reached the engine")
			}
		})
	}
}

// ---- DENY 2: a server refusal keeps its classification ------------------------------

// TestLaneMapsServerRefusalsToTheExitContract checks every family against every
// classified status, and asserts STDOUT IS EMPTY on refusal — a command that
// printed a table and then failed would leave a script parsing a phantom.
func TestLaneMapsServerRefusalsToTheExitContract(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, exitcode.Auth},
		{http.StatusForbidden, exitcode.Auth},
		{http.StatusNotFound, exitcode.NotFound},
		{http.StatusConflict, exitcode.Conflict},
		{http.StatusInternalServerError, exitcode.Server},
		{http.StatusBadGateway, exitcode.Server},
	} {
		for _, v := range laneReadVerbs {
			t.Run(fmt.Sprintf("%d/%s", tc.status, v.family), func(t *testing.T) {
				spy := newObserveSpy(t, tc.status, `{"error":{"message":"refused"}}`)
				out, _, err := execRoot(t, observeArgs(spy.srv.URL, v.args...)...)
				if err == nil {
					t.Fatalf("HTTP %d must not exit 0", tc.status)
				}
				if got := exitcode.From(err); got != tc.want {
					t.Errorf("exit = %d, want %d for HTTP %d", got, tc.want, tc.status)
				}
				if strings.TrimSpace(out) != "" {
					t.Errorf("stdout must be empty on a refusal, got:\n%s", out)
				}
			})
		}
	}
}

// TestNotWiredIsReportedAsAProductBoundaryNotAFailure pins the one status this
// lane classifies beyond httpErr. 501 from an unwired enterprise seam must not
// read as "request failed", and it must carry the engine's own reason.
func TestNotWiredIsReportedAsAProductBoundaryNotAFailure(t *testing.T) {
	spy := newObserveSpy(t, http.StatusNotImplemented,
		`{"error":{"message":"report scheduling is an enterprise capability and is not wired in this build"}}`)
	_, _, err := execRoot(t, observeArgs(spy.srv.URL, "reporting", "schedules", "ls")...)
	if err == nil {
		t.Fatal("a 501 must not exit 0: the command did not do what was asked")
	}
	if got := exitcode.From(err); got != exitcode.Err {
		t.Errorf("exit = %d, want %d — matching the identical decision in cmd_compliance.go", got, exitcode.Err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "report scheduling") {
		t.Errorf("the engine's own reason must survive, got: %s", msg)
	}
	if !strings.Contains(msg, "not wired in this build") {
		t.Errorf("the message must name this as a build boundary, got: %s", msg)
	}
	if strings.Contains(msg, "request failed") {
		t.Errorf("a 501 must NOT read as a generic failure, got: %s", msg)
	}
}

// ---- DENY 3: destructive verbs require consent --------------------------------------

// laneDestructiveVerbs are the five DELETEs in this lot. Each is checked for a
// refusal WITHOUT --yes at zero cost, and for working WITH it.
var laneDestructiveVerbs = []struct {
	name string
	args []string
	path string
}{
	{"reporting-schedules-rm", []string{"reporting", "schedules", "rm", "sch-1"}, "/v1/m/reporting/schedules/sch-1"},
	{"reporting-templates-rm", []string{"reporting", "templates", "rm", "audit-summary"}, "/v1/m/reporting/templates/audit-summary"},
	{"notify-routes-rm", []string{"notify", "routes", "rm", "rt-1"}, "/v1/m/notify/routes/rt-1"},
	{"health-checks-rm", []string{"health", "checks", "rm", "chk-1"}, "/v1/m/health/checks/chk-1"},
	{"consoleviews-rm", []string{"consoleviews", "rm", "sv-1"}, "/v1/m/consoleviews/views/sv-1"},
}

func TestObserveLaneDestructiveVerbsRefuseUnattendedConsent(t *testing.T) {
	for _, v := range laneDestructiveVerbs {
		t.Run(v.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, `{"deleted":true}`)

			// DENY. The test process's stdin is not a terminal, so this is the
			// unattended case the confirmation exists for.
			_, _, err := execRoot(t, observeArgs(spy.srv.URL, v.args...)...)
			if err == nil {
				t.Fatal("a destructive verb must refuse without --yes in a non-interactive session")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if n := spy.count(); n != 0 {
				t.Errorf("%d request(s) reached the server; want 0 — an unconfirmed delete must cost nothing", n)
			}

			// POSITIVE CONTROL: with --yes it deletes, and reaches the DELETE.
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL, append(append([]string{}, v.args...), "--yes")...)...); err != nil {
				t.Fatalf("with --yes the delete must succeed, got %v", err)
			}
			got := spy.last(t)
			if got.method != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", got.method)
			}
			if got.path != v.path {
				t.Errorf("path = %s, want %s", got.path, v.path)
			}
		})
	}
}

// ---- DENY 4: a required query parameter is refused locally ---------------------------

func TestRoutesWithARequiredParameterRefuseBeforeTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing []string
		full    []string
	}{
		{
			"accessmap-neighbors-id",
			[]string{"accessmap", "neighbors"},
			[]string{"accessmap", "neighbors", "--id", "ag-7"},
		},
		{
			"accessmap-reachability-agent-id",
			[]string{"accessmap", "attack-paths", "reachability"},
			[]string{"accessmap", "attack-paths", "reachability", "--agent-id", "ag-7"},
		},
		{
			"accessmap-escalation-agent-id",
			[]string{"accessmap", "attack-paths", "escalation"},
			[]string{"accessmap", "attack-paths", "escalation", "--agent-id", "ag-7"},
		},
		{
			"accessmap-exfil-resource-id",
			[]string{"accessmap", "attack-paths", "exfil"},
			[]string{"accessmap", "attack-paths", "exfil", "--resource-id", "res-3"},
		},
		{
			"health-sla-subject",
			[]string{"health", "sla"},
			[]string{"health", "sla", "--subject-kind", "agent", "--subject-ref", "ag-7"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, `{"has_data":true,"paths":[],"nodes":[],"edges":[]}`)

			_, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.missing...)...)
			if err == nil {
				t.Fatal("a route whose required parameter is absent must refuse")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if n := spy.count(); n != 0 {
				t.Errorf("%d request(s); want 0 — a request that cannot succeed must not be sent", n)
			}

			// POSITIVE CONTROL.
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.full...)...); err != nil {
				t.Fatalf("with the parameter the verb must succeed, got %v", err)
			}
			if spy.count() != 1 {
				t.Errorf("%d request(s) after the control; want exactly 1", spy.count())
			}
		})
	}
}

// ---- DENY 5: a positional id cannot retarget the request -----------------------------

// TestPositionalIDsCannotRetargetTheRequest plants a traversal in the id every
// verb that takes one. A script interpolating a hostile id must not be able to
// move the request to another route.
//
// IT ASSERTS ON THE REQUEST LINE, NOT ON r.URL.Path. Go decodes %2F back to "/"
// in r.URL.Path, so a correctly escaped id and a raw one are indistinguishable
// there — measuring it reports a working guard as broken (and, in the other
// direction, would pass a broken one). r.RequestURI is what actually crossed the
// socket.
func TestPositionalIDsCannotRetargetTheRequest(t *testing.T) {
	evil := "../../../v1/system/orgs/t_victim"
	for _, tc := range []struct {
		name   string
		args   []string
		prefix string
	}{
		{"observability-traces-get", []string{"observability", "traces", "get", evil}, "/v1/m/observability/traces/"},
		{"observability-traces-export", []string{"observability", "traces", "export", evil}, "/v1/m/observability/traces/"},
		{"health-incidents-get", []string{"health", "incidents", "get", evil}, "/v1/m/health/incidents/"},
		{"health-incidents-resolve", []string{"health", "incidents", "resolve", evil}, "/v1/m/health/incidents/"},
		{"health-checks-get", []string{"health", "checks", "get", evil}, "/v1/m/health/checks/"},
		{"health-checks-rm", []string{"health", "checks", "rm", evil, "--yes"}, "/v1/m/health/checks/"},
		{"notify-routes-get", []string{"notify", "routes", "get", evil}, "/v1/m/notify/routes/"},
		{"notify-routes-rm", []string{"notify", "routes", "rm", evil, "--yes"}, "/v1/m/notify/routes/"},
		{"notify-outbox-redeliver", []string{"notify", "outbox", "redeliver", evil}, "/v1/m/notify/outbox/"},
		{"capabilities-servers-get", []string{"capabilities", "servers", "get", evil}, "/v1/m/capabilities/servers/"},
		{"governance-approvals-get", []string{"governance", "approvals", "get", evil}, "/v1/m/governance/approvals/"},
		{"governance-approvals-decisions", []string{"governance", "approvals", "decisions", evil}, "/v1/m/governance/approvals/"},
		{"governance-breakglass-get", []string{"governance", "breakglass", "get", evil}, "/v1/m/governance/breakglass/"},
		{"governance-breakglass-uses", []string{"governance", "breakglass", "uses", evil}, "/v1/m/governance/breakglass/"},
		{"governance-pdp-get-version", []string{"governance", "pdp", "get-version", evil, "--engine", "cedar"}, "/v1/m/governance/pdp/versions/"},
		{"governance-rbac-roles-get", []string{"governance", "rbac", "roles", "get", evil}, "/v1/m/governance/rbac/roles/"},
		{"governance-rbac-permgroups-get", []string{"governance", "rbac", "permission-groups", "get", evil}, "/v1/m/governance/rbac/permission-groups/"},
		{"governance-rbac-grants-get", []string{"governance", "rbac", "grants", "get", evil}, "/v1/m/governance/rbac/grants/"},
		{"governance-nhi-get", []string{"governance", "nhi", "get", evil}, "/v1/m/governance/nhi/"},
		{"governance-nhi-events", []string{"governance", "nhi", "events", evil}, "/v1/m/governance/nhi/"},
		{"consoleviews-get", []string{"consoleviews", "get", evil}, "/v1/m/consoleviews/views/"},
		{"consoleviews-rm", []string{"consoleviews", "rm", evil, "--yes"}, "/v1/m/consoleviews/views/"},
		{"inventory-entities-get", []string{"inventory", "entities", "get", "agent", evil}, "/v1/m/inventory/entities/agent/"},
		{"reporting-schedules-rm", []string{"reporting", "schedules", "rm", evil, "--yes"}, "/v1/m/reporting/schedules/"},
		{"reporting-schedule-runs", []string{"reporting", "schedules", "runs", evil}, "/v1/m/reporting/schedules/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, "{}")
			// Whether the verb succeeds is beside the point; where the bytes
			// LANDED is the whole assertion.
			_, _, _ = execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			got := spy.last(t)
			if !strings.HasPrefix(got.rawURI, tc.prefix) {
				t.Fatalf("request line %q left %q — the positional id retargeted the route", got.rawURI, tc.prefix)
			}
			// The separators inside the id must be ESCAPED, so the whole id stays
			// one path segment. This is the positive evidence that escaping ran:
			// without it, "still under the prefix" is also true of a request that
			// merely failed to be sent anywhere interesting.
			if !strings.Contains(got.rawURI, "%2F") {
				t.Fatalf("request line %q carries no escaped separator: the id was concatenated raw", got.rawURI)
			}
			// And the traversal must not appear as real path structure.
			if strings.Contains(got.rawURI, "/system/orgs") {
				t.Fatalf("request line %q reached the system-org route", got.rawURI)
			}
			if strings.Contains(got.rawURI, "/../") {
				t.Fatalf("request line %q carries an unescaped traversal segment", got.rawURI)
			}
		})
	}
}

// TestTheTraversalWitnessCanFail is the negative control ON THE WITNESS ABOVE.
//
// Every assertion in that test is of the form "the bad thing is absent", and an
// absence is exactly what a broken measurement reports for free. This proves the
// same three checks FAIL when handed a request line built the unsafe way — so a
// green traversal test means the escaping ran, not that the check is blind.
func TestTheTraversalWitnessCanFail(t *testing.T) {
	unsafe := "/v1/m/health/checks/" + "../../../v1/system/orgs/t_victim"
	if !strings.Contains(unsafe, "/system/orgs") {
		t.Error("the system-org check cannot detect a raw traversal, so it proves nothing")
	}
	if !strings.Contains(unsafe, "/../") {
		t.Error("the traversal-segment check cannot detect a raw traversal, so it proves nothing")
	}
	if strings.Contains(unsafe, "%2F") {
		t.Error("the escaped-separator check would pass on a raw concatenation, so it proves nothing")
	}
	// And the safe form, built the way the code builds it, satisfies all three.
	safe := "/v1/m/health/checks" + observeIDPath("../../../v1/system/orgs/t_victim")
	if strings.Contains(safe, "/system/orgs") || strings.Contains(safe, "/../") || !strings.Contains(safe, "%2F") {
		t.Errorf("observeIDPath did not neutralize the traversal: %q", safe)
	}
}

// ---- DENY 6: local allow-lists refuse a value the engine would ABSORB -----------------

// TestLocalAllowListsRefuseWithoutSpendingARequest covers the checks whose value
// is not merely saving a round trip: each of these is a value the ENGINE would
// accept or silently coerce, producing a plausible answer to a question nobody
// asked.
func TestLocalAllowListsRefuseWithoutSpendingARequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		why  string
	}{
		{"posture-severity", []string{"posture", "export", "--severity", "urgent"},
			"an unknown severity floor"},
		{"adoption-lens", []string{"adoption", "trend", "--lens", "both"},
			"a lens that does not exist"},
		{"adoption-since-not-rfc3339", []string{"adoption", "summary", "--since", "2026-08-01"},
			"a date the engine parses as RFC3339 only"},
		{"reporting-format", []string{"reporting", "reports", "get", "audit-summary", "--format", "pdff", "--out", "-"},
			"a format the engine silently coerces to html"},
		{"reporting-from", []string{"reporting", "reports", "get", "audit-summary", "--from", "lastweek", "--out", "-"},
			"a date the engine silently replaces with its default window"},
		{"reporting-unknown-type", []string{"reporting", "reports", "get", "not-a-report", "--out", "-"},
			"a report type this build does not offer"},
		{"health-report-unknown-state", []string{"health", "checks", "report", "chk-1", "--state", "unknown"},
			"a state that is INFERRED from silence and cannot be asserted"},
		{"health-report-bad-state", []string{"health", "checks", "report", "chk-1", "--state", "flaky"},
			"a state outside healthy/degraded/down"},
		{"health-create-no-interval", []string{"health", "checks", "create", "--subject-kind", "agent", "--subject-ref", "ag-7"},
			"a check with no expected interval, whose silence could never become unknown"},
		{"health-create-bad-subject-kind", []string{"health", "checks", "create", "--subject-kind", "server", "--subject-ref", "s-1", "--interval", "60"},
			"a subject kind this module does not monitor"},
		{"notify-min-severity", []string{"notify", "routes", "create", "--name", "n", "--destination", "d", "--min-severity", "urgent"},
			"a severity floor outside the vocabulary"},
		{"notify-create-no-destination", []string{"notify", "routes", "create", "--name", "n"},
			"a route with nowhere to send"},
		{"notify-evaluate-no-event-type", []string{"notify", "evaluate"},
			"an evaluation with no signal to evaluate"},
		{"consoleviews-params-not-object", []string{"consoleviews", "create", "--feature-id", "f", "--name", "n", "--params", "[1,2]"},
			"params that are not a JSON object"},
		{"consoleviews-params-both", []string{"consoleviews", "create", "--feature-id", "f", "--name", "n", "--params", "{}", "--params-file", "x.json"},
			"two mutually exclusive sources for one document"},
		{"consoleviews-update-no-name", []string{"consoleviews", "update", "sv-1", "--params", "{}"},
			"a replace that omits the name it would clear"},
		{"accessmap-direction", []string{"accessmap", "neighbors", "--id", "ag-7", "--direction", "sideways"},
			"a traversal direction that does not exist"},
		{"negative-limit", []string{"inventory", "entities", "ls", "--limit", "-5"},
			"a negative page size the engine would ignore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, "{}")
			_, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			if err == nil {
				t.Fatalf("must refuse %s", tc.why)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage) for %s", got, exitcode.Usage, tc.why)
			}
			if n := spy.count(); n != 0 {
				t.Errorf("%d request(s); want 0 — %s must be refused before the wire", n, tc.why)
			}
		})
	}
}

// TestTheAllowListsAcceptWhatTheEngineAccepts is the other half. Without it,
// "refuse everything" would satisfy every case above.
func TestTheAllowListsAcceptWhatTheEngineAccepts(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"posture-severity-high", []string{"posture", "export", "--severity", "high"}},
		{"adoption-lens-telemetry", []string{"adoption", "trend", "--lens", "telemetry"}},
		{"adoption-since-rfc3339", []string{"adoption", "summary", "--since", "2026-08-01T00:00:00Z"}},
		{"reporting-from-date-only", []string{"reporting", "reports", "ls"}},
		{"health-report-degraded", []string{"health", "checks", "report", "chk-1", "--state", "degraded"}},
		{"health-create-ok", []string{"health", "checks", "create", "--subject-kind", "agent", "--subject-ref", "ag-7", "--interval", "60"}},
		{"notify-create-ok", []string{"notify", "routes", "create", "--name", "n", "--destination", "d", "--min-severity", "critical"}},
		{"notify-evaluate-ok", []string{"notify", "evaluate", "--event-type", "finding.created"}},
		{"consoleviews-create-ok", []string{"consoleviews", "create", "--feature-id", "findings", "--name", "n", "--params", `{"a":1}`}},
		{"accessmap-direction-incoming", []string{"accessmap", "neighbors", "--id", "ag-7", "--direction", "incoming"}},
		{"limit-positive", []string{"inventory", "entities", "ls", "--limit", "5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, `{"id":"x","items":[],"has_data":true}`)
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...); err != nil {
				t.Fatalf("a valid invocation must succeed, got %v (exit %d)", err, exitcode.From(err))
			}
			if n := spy.count(); n != 1 {
				t.Fatalf("%d request(s); want exactly 1 — a valid invocation must reach the engine", n)
			}
		})
	}
}

// ---- PERMIT 2: -o json preserves what the CLI does not model -------------------------

// TestJSONOutputPreservesFieldsTheCLIDoesNotModel is the observeJSON contract.
// A struct re-marshal would drop every unmodelled field, and a consumer would
// silently stop seeing data the engine still sends.
func TestJSONOutputPreservesFieldsTheCLIDoesNotModel(t *testing.T) {
	// attribution_reason, signal_sources and a field from a hypothetical newer
	// engine are all absent from accessMapEdge.
	body := `{"nodes":[],"edges":[{"id":"e1","origin_id":"ag-7","resource_id":"r1","mode":"rw",
		"attribution_reason":"shared-account","signal_sources":"otel,pg_audit",
		"a_field_added_next_release":"must survive"}]}`
	spy := newObserveSpy(t, http.StatusOK, body)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "graph", "-o", "json")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	for _, field := range []string{"attribution_reason", "shared-account", "signal_sources", "a_field_added_next_release"} {
		if !strings.Contains(out, field) {
			t.Errorf("-o json dropped %q — it re-marshaled the CLI's struct instead of forwarding the engine's document:\n%s", field, out)
		}
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("-o json did not emit valid JSON:\n%s", out)
	}
}

// ---- PERMIT 3: pagination exists exactly where the engine reads it --------------------

// TestPagingFlagsExistOnlyWhereTheEngineReadsThem is the census's headline
// finding made executable.
//
// A --cursor on a route that ignores it is worse than a missing flag: the command
// accepts it, the engine drops it, and page two is page one forever. So the four
// namespaces without a cursor must NOT offer one, and the five with one must.
func TestPagingFlagsExistOnlyWhereTheEngineReadsThem(t *testing.T) {
	// EVERY LEAF IN THE LOT, not a sample. The first version of this test carried
	// eleven rows and cited each namespace's paging HELPER — "adoption/dto.go:207
	// parses limit only". That is a true fact about a namespace, and a fact about a
	// namespace cannot say which of its ROUTES read the parameter: limitParam has
	// two call sites (api.go:60 and api.go:161) and `adoption developers` shipped
	// with no --limit while this test stayed green, because no row named it.
	//
	// So the referent is now the route, and the table is exhaustive. The count
	// assertion below is what keeps it that way: a verb added to the lot without a
	// row here fails rather than being silently unmeasured.
	cases := []struct {
		path       string
		wantCursor bool
		wantLimit  bool
	}{
		{"olivares reporting branding get", false, false},
		{"olivares reporting branding set", false, false},
		{"olivares reporting enterprise bundle", false, false},
		{"olivares reporting enterprise posture", false, false},
		{"olivares reporting enterprise risk", false, false},
		{"olivares reporting reports get", false, false},
		{"olivares reporting reports ls", false, false},
		{"olivares reporting schedules create", false, false},
		{"olivares reporting schedules ls", false, false},
		{"olivares reporting schedules rm", false, false},
		{"olivares reporting schedules run", false, false},
		{"olivares reporting schedules runs", false, false},
		{"olivares reporting templates get", false, false},
		{"olivares reporting templates rm", false, false},
		{"olivares reporting templates set", false, false},
		{"olivares notify deliveries", true, true},
		{"olivares notify destinations", false, false},
		{"olivares notify evaluate", false, false},
		{"olivares notify match-types", false, false},
		{"olivares notify outbox ls", true, true},
		{"olivares notify outbox redeliver", false, false},
		{"olivares notify routes create", false, false},
		{"olivares notify routes get", false, false},
		{"olivares notify routes ls", true, true},
		{"olivares notify routes restore", false, false},
		{"olivares notify routes revisions", true, true},
		{"olivares notify routes rm", false, false},
		{"olivares notify routes test", false, false},
		{"olivares notify routes update", false, false},
		{"olivares governance approvals decisions", false, false},
		{"olivares governance approvals get", false, false},
		{"olivares governance approvals ls", true, true},
		{"olivares governance breakglass get", false, false},
		{"olivares governance breakglass ls", true, true},
		{"olivares governance breakglass uses", false, false},
		{"olivares governance guardian actions", true, true},
		{"olivares governance guardian rules", true, true},
		{"olivares governance killswitch ls", true, true},
		{"olivares governance killswitch state", false, false},
		{"olivares health checks create", false, false},
		{"olivares health checks get", false, false},
		{"olivares health checks ls", true, true},
		{"olivares health checks report", false, false},
		{"olivares health checks rm", false, false},
		{"olivares health checks update", false, false},
		{"olivares health dependencies", true, true},
		{"olivares health events", true, true},
		{"olivares health incidents get", false, false},
		{"olivares health incidents ls", true, true},
		{"olivares health incidents resolve", false, false},
		{"olivares health sla", false, false},
		{"olivares health status", true, true},
		{"olivares health watch", false, false},
		{"olivares accessmap attack-paths escalation", false, false},
		{"olivares accessmap attack-paths exfil", false, false},
		{"olivares accessmap attack-paths reachability", false, false},
		{"olivares accessmap attack-paths summary", false, false},
		{"olivares accessmap drift", true, true},
		{"olivares accessmap graph", true, true},
		{"olivares accessmap neighbors", false, false},
		{"olivares observability attestation", false, false},
		{"olivares observability ingestion-health", false, false},
		{"olivares observability traces export", false, false},
		{"olivares observability traces get", false, false},
		{"olivares observability traces ls", true, true},
		{"olivares consoleviews create", false, false},
		{"olivares capabilities servers get", false, false},
		{"olivares capabilities servers ls", true, true},
		{"olivares capabilities skills", true, true},
		{"olivares capabilities tools", true, true},
		{"olivares capabilities wiring", false, false},
		{"olivares governance pdp versions", false, false},
		{"olivares governance pdp get-version", false, false},
		{"olivares governance pdp active", false, false},
		{"olivares governance pdp tests", false, false},
		{"olivares governance rbac catalog", false, false},
		{"olivares governance rbac delegation-authority", false, false},
		{"olivares governance rbac roles ls", false, false},
		{"olivares governance rbac roles get", false, false},
		{"olivares governance rbac permission-groups ls", false, false},
		{"olivares governance rbac permission-groups get", false, false},
		{"olivares governance rbac grants ls", false, false},
		{"olivares governance rbac grants get", false, false},
		{"olivares governance nhi ls", true, true},
		{"olivares governance nhi posture", false, false},
		{"olivares governance nhi get", false, false},
		{"olivares governance nhi events", false, false},
		{"olivares consoleviews get", false, false},
		{"olivares consoleviews ls", false, false},
		{"olivares consoleviews rm", false, false},
		{"olivares consoleviews update", false, false},
		{"olivares adoption developers", false, true},
		{"olivares adoption discrepancy", false, false},
		{"olivares adoption summary", false, true},
		{"olivares adoption teams", false, false},
		{"olivares adoption trend", false, false},
		{"olivares identity external-keys", false, false},
		{"olivares identity residency", false, false},
		{"olivares identity sso", false, false},
		{"olivares identity wif", false, false},
		{"olivares inventory entities get", false, false},
		{"olivares inventory entities ls", true, true},
		{"olivares inventory summary", false, false},
		{"olivares posture export", false, false},
	}
	// The engine side of the table, measured in the module sources: cursor+limit
	// come from a per-namespace list helper (notify/helpers.go:103,
	// health/helpers.go:128, access-map/api.go:67 edgeQuery,
	// observability/traces.go:324 traceListParams, inventory/api.go:211), and each
	// is wired to the handlers named below. limit-without-cursor is adoption's
	// top-N (dto.go:207) at its two call sites. Everything else reads neither.
	if len(cases) != 104 {
		t.Fatalf("the table has %d rows, not the 104 leaves the lot exposes", len(cases))
	}
	cursorN, limitN := 0, 0
	for _, tc := range cases {
		if tc.wantCursor {
			cursorN++
		}
		if tc.wantLimit {
			limitN++
		}
	}
	if cursorN != 22 || limitN != 24 {
		t.Fatalf("the table itself claims %d cursor and %d limit verbs, want 22 and 24", cursorN, limitN)
	}
	root := newRootCmd()
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if seen[tc.path] {
				t.Fatalf("%s appears twice: a duplicated row hides a missing one", tc.path)
			}
			seen[tc.path] = true
			cmd := resolveCommandPath(t, root, tc.path)
			if cmd == nil {
				t.Fatalf("%s does not resolve — this row can prove nothing", tc.path)
			}
			if cmd.HasSubCommands() {
				t.Fatalf("%s is a group, not a verb: it addresses no route", tc.path)
			}
			hasCursor := cmd.Flags().Lookup("cursor") != nil
			hasLimit := cmd.Flags().Lookup("limit") != nil
			if hasCursor != tc.wantCursor {
				t.Errorf("--cursor present = %v, want %v: a cursor the engine ignores makes page two page one", hasCursor, tc.wantCursor)
			}
			if hasLimit != tc.wantLimit {
				t.Errorf("--limit present = %v, want %v: a route whose top-N the CLI cannot set returns a silently cut list", hasLimit, tc.wantLimit)
			}
		})
	}
	// And the table must cover the tree, not merely agree with itself: every leaf
	// of the ten families has a row. Without this, deleting a row would "fix" a
	// failure by making the verb unmeasured.
	// ⛔ ESTA LISTA ESTABA ESCRITA A MANO, y eso reabria por otra puerta el agujero que el
	// parrafo de arriba cierra: una familia NUEVA no aparece en ella, asi que ninguna de sus
	// hojas se mide y el test sigue verde. Medido al anadir `governance`: sus cinco hojas
	// habrian quedado sin fila y sin queja.
	//
	// Se deriva de `laneReadVerbs`, que ya es la tabla de familias del lote y que hay que tocar
	// igualmente para dar de alta una: una sola lista que mantener en vez de dos que se
	// desincronizan en silencio.
	familias := map[string]bool{}
	for _, v := range laneReadVerbs {
		familias[v.family] = true
	}
	if len(familias) < 10 {
		t.Fatalf("only %d families derived from laneReadVerbs; the coverage walk would check almost nothing", len(familias))
	}
	for fam := range familias {
		f, _, err := root.Find([]string{fam})
		if err != nil || f == nil {
			t.Fatalf("family %s does not resolve", fam)
		}
		var walk func(c *cobra.Command, path string)
		walk = func(c *cobra.Command, path string) {
			if !c.HasSubCommands() {
				if !seen[path] {
					t.Errorf("%s has no row in the paging table: it is unmeasured", path)
				}
				return
			}
			for _, ch := range c.Commands() {
				if ch.Name() == "help" || ch.Name() == "completion" {
					continue
				}
				walk(ch, path+" "+ch.Name())
			}
		}
		walk(f, "olivares "+fam)
	}
}

// TestAdoptionDevelopersCanAskForItsTopN is the witness for a route whose
// top-N the CLI could not express.
//
// `limitParam` has TWO call sites, not one: GET /summary caps at 10
// (claudeadoption/api.go:60) and GET /developers caps at 100 (api.go:161). The
// row above cites the HELPER (dto.go:207), which is a fact about the namespace,
// and a namespace-level fact cannot say which of its five routes read the
// parameter. `developers` therefore shipped with no --limit at all.
//
// WHAT THAT COSTS IS NOT A MISSING CONVENIENCE. handleDevelopers slices to the
// top 100 (api.go:174-176) WITHOUT setting Truncated — that field carries the
// aggregation cap, not this cut — so a tenant with more than 100 developers gets
// 100 rows, no flag to widen them, and nothing in the payload or on stdout
// saying the roster was cut. This is the lot's own headline failure ("a script
// that believes it read the whole list") reached from the missing-flag side; and
// it lands on the one view in the namespace that is identity-exposing and
// separately permissioned, where under-reporting reads as good news.
func TestAdoptionDevelopersCanAskForItsTopN(t *testing.T) {
	// PERMIT: the value reaches the engine as ?limit.
	t.Run("reaches the wire", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"developers":[],"boundary":{}}`)
		if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"adoption", "developers", "--limit", "250")...); err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if got := spy.last(t).query.Get("limit"); got != "250" {
			t.Errorf("limit = %q, want 250 — the engine reads it at claudeadoption/api.go:161", got)
		}
	})
	// DENY: a negative top-N is refused before the request, like every other
	// limit in this lane.
	t.Run("negative is refused at zero cost", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"developers":[],"boundary":{}}`)
		_, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"adoption", "developers", "--limit", "-5")...)
		if err == nil {
			t.Fatal("a negative top-N must be refused")
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
		}
		if n := spy.count(); n != 0 {
			t.Errorf("%d request(s); want 0", n)
		}
		// AND IT MUST BE THE LIMIT CHECK THAT REFUSED, not cobra rejecting a
		// flag that does not exist. Both exit 2 at zero requests, so without
		// this line the subtest passes just as well BEFORE the flag is added —
		// green for the neighboring reason.
		if msg := err.Error(); strings.Contains(msg, "unknown flag") || !strings.Contains(msg, "--limit must not be negative") {
			t.Errorf("the refusal must come from the limit check, got: %s", msg)
		}
	})
	// CONTROL, and it is the half that keeps the fix honest: the three routes
	// that do NOT read limit must still REJECT the flag. Spraying --limit across
	// the namespace to close this finding would rebuild, one namespace over,
	// exactly the phantom-parameter defect this lot was written against.
	for _, verb := range []string{"trend", "teams", "discrepancy"} {
		t.Run("no phantom limit on "+verb, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, `{"boundary":{}}`)
			_, _, err := execRoot(t, observeArgs(spy.srv.URL, "adoption", verb, "--limit", "5")...)
			if err == nil {
				t.Fatalf("adoption %s must reject --limit: the engine does not read it there", verb)
			}
			if n := spy.count(); n != 0 {
				t.Errorf("%d request(s); want 0 — an unknown flag must not be sent", n)
			}
		})
	}
}

// TestPagingFlagsReachTheWire proves the flags that DO exist are actually sent,
// and that the truncation note names the cursor to continue from — "truncated"
// without the token tells an operator they have a problem and not how to finish.
func TestPagingFlagsReachTheWire(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"items":[{"id":"e-1","kind":"agent","entity_id":"ag-7","name":"a","status":"active"}],"cursor":"NEXT-PAGE","has_more":true}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"inventory", "entities", "ls", "--limit", "25", "--cursor", "PAGE-2", "--kind", "agent")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	got := spy.last(t)
	if got.query.Get("limit") != "25" {
		t.Errorf("limit = %q, want 25", got.query.Get("limit"))
	}
	if got.query.Get("cursor") != "PAGE-2" {
		t.Errorf("cursor = %q, want PAGE-2", got.query.Get("cursor"))
	}
	if got.query.Get("kind") != "agent" {
		t.Errorf("kind = %q, want agent", got.query.Get("kind"))
	}
	if !strings.Contains(out, "NEXT-PAGE") {
		t.Errorf("a truncated page must NAME the cursor to continue from, got:\n%s", out)
	}
}

// ---- PERMIT 4: the two pointer-semantics contracts ------------------------------------

// TestCheckUpdateOmitsTheSLATargetUnlessItWasPassed is the contract the engine
// made a pointer for. Sending sla_target_ppm unconditionally would zero every
// stored SLA target on every partial update — the exact bug health's own comment
// records having fixed.
func TestCheckUpdateOmitsTheSLATargetUnlessItWasPassed(t *testing.T) {
	t.Run("omitted when not passed", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"chk-1"}`)
		if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"health", "checks", "update", "chk-1", "--interval", "600")...); err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		body := spy.last(t).body
		if strings.Contains(body, "sla_target_ppm") {
			t.Fatalf("sla_target_ppm was sent without being asked for; this ZEROES the stored target. Body: %s", body)
		}
		if !strings.Contains(body, `"expected_interval_seconds":600`) {
			t.Errorf("the field that WAS passed must be sent. Body: %s", body)
		}
	})
	t.Run("sent when passed, including zero", func(t *testing.T) {
		// Zero is the interesting value: it is the one an int64 could not
		// distinguish from "omitted", and an operator who explicitly asks for a
		// zero target must get one.
		spy := newObserveSpy(t, http.StatusOK, `{"id":"chk-1"}`)
		if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"health", "checks", "update", "chk-1", "--sla-target-ppm", "0")...); err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		body := spy.last(t).body
		if !strings.Contains(body, `"sla_target_ppm":0`) {
			t.Fatalf("an explicitly passed zero must reach the engine. Body: %s", body)
		}
	})
}

// TestRouteUpdateOmitsEnabledUnlessItWasPassed is the same property on notify:
// a nil `enabled` means "keep what is stored", so always sending the flag's
// default would re-enable every route updated for an unrelated reason.
func TestRouteUpdateOmitsEnabledUnlessItWasPassed(t *testing.T) {
	t.Run("omitted when not passed", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"rt-1"}`)
		if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"notify", "routes", "update", "rt-1", "--destination", "ops")...); err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if body := spy.last(t).body; strings.Contains(body, "enabled") {
			t.Fatalf("enabled was sent without being asked for, overriding the stored value. Body: %s", body)
		}
	})
	t.Run("sent when passed false", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"rt-1"}`)
		if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"notify", "routes", "update", "rt-1", "--destination", "ops", "--enabled=false")...); err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if body := spy.last(t).body; !strings.Contains(body, `"enabled":false`) {
			t.Fatalf("an explicitly passed --enabled=false must reach the engine. Body: %s", body)
		}
	})
}

// ---- PERMIT 5: the three-answer posture reads ------------------------------------------

// TestIdentityPostureTellsOffApartFromUnmeasured is the central claim of
// cmd_identity.go: "SSO is off" and "we could not look" must not exit alike.
func TestIdentityPostureTellsOffApartFromUnmeasured(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		body     string
		wantExit int
		wantText string
	}{
		{
			"sso-configured", []string{"identity", "sso"},
			`{"configured":true,"protocol":"oidc"}`, exitcode.OK, "connected",
		},
		{
			"sso-genuinely-off", []string{"identity", "sso"},
			`{"configured":false}`, exitcode.OK, "not configured",
		},
		{
			"sso-undetermined", []string{"identity", "sso"},
			`{"configured":false,"reason":"the engine's federation posture is not readable from this build"}`,
			exitcode.Indeterminate, "UNDETERMINED",
		},
		{
			"external-keys-real-zero", []string{"identity", "external-keys"},
			`{"items":[],"available":true}`, exitcode.OK, "read cleanly and is empty",
		},
		{
			"external-keys-unmeasured", []string{"identity", "external-keys"},
			`{"items":[],"available":false,"reason":"no admin credential is wired"}`,
			exitcode.Indeterminate, "UNAVAILABLE",
		},
		{
			"residency-real-zero", []string{"identity", "residency"},
			`{"items":[],"available":true}`, exitcode.OK, "read cleanly and is empty",
		},
		{
			"residency-unmeasured", []string{"identity", "residency"},
			`{"items":[],"available":false,"reason":"the fetch failed"}`,
			exitcode.Indeterminate, "UNAVAILABLE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, tc.body)
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			got := exitcode.OK
			if err != nil {
				got = exitcode.From(err)
			}
			if got != tc.wantExit {
				t.Errorf("exit = %d, want %d — an unmeasured posture must not be reported as a measured one", got, tc.wantExit)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("stdout must contain %q, got:\n%s", tc.wantText, out)
			}
			// The report is always on stdout, whatever the code.
			if strings.TrimSpace(out) == "" {
				t.Error("the report must reach stdout even when the exit code is non-zero")
			}
		})
	}
}

// TestStrictFalseTurnsOffTheIndeterminateExit proves the opt-out works, so the
// new exit code cannot become an unavoidable surprise for an existing script.
func TestStrictFalseTurnsOffTheIndeterminateExit(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"configured":false,"reason":"not readable"}`)
	if _, _, err := execRoot(t, observeArgs(spy.srv.URL, "identity", "sso", "--strict=false")...); err != nil {
		t.Fatalf("--strict=false must exit 0, got %v (exit %d)", err, exitcode.From(err))
	}
}

// TestSLAWithNoObservationIsIndeterminateNotPerfect: an empty window has no
// uptime. Reporting 0% or 100% would both be artifacts.
func TestSLAWithNoObservationIsIndeterminateNotPerfect(t *testing.T) {
	t.Run("no data", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"has_data":false,"uptime_percent":0,"subject_kind":"agent","subject_ref":"ag-7"}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"health", "sla", "--subject-kind", "agent", "--subject-ref", "ag-7")...)
		if err == nil || exitcode.From(err) != exitcode.Indeterminate {
			t.Fatalf("exit = %v, want %d (indeterminate)", err, exitcode.Indeterminate)
		}
		if !strings.Contains(out, "NO DATA") {
			t.Errorf("stdout must say there is no data, got:\n%s", out)
		}
		if strings.Contains(out, "0.0000%") {
			t.Errorf("a percentage must not be printed when there is nothing to compute it from, got:\n%s", out)
		}
	})
	t.Run("with data", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"has_data":true,"uptime_percent":99.95,"uptime_ppm":999500,"subject_kind":"agent","subject_ref":"ag-7"}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"health", "sla", "--subject-kind", "agent", "--subject-ref", "ag-7")...)
		if err != nil {
			t.Fatalf("an SLA with observations must exit 0, got %v", err)
		}
		if !strings.Contains(out, "99.95") {
			t.Errorf("the measured uptime must be printed, got:\n%s", out)
		}
	})
}

// TestPostureTruncationIsDegradedNotClean: a capped export understates findings,
// entities and drift — all in the direction that reads as good news.
func TestPostureTruncationIsDegradedNotClean(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"complete", `{"tenant":"t","inventory":[],"findings":[],"posture_drift":{}}`, exitcode.OK},
		{"findings truncated", `{"tenant":"t","findings_truncated":true,"posture_drift":{}}`, exitcode.Degraded},
		{"inventory truncated", `{"tenant":"t","inventory_truncated":true,"posture_drift":{}}`, exitcode.Degraded},
		{"drift truncated", `{"tenant":"t","posture_drift":{"truncated":true}}`, exitcode.Degraded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, tc.body)
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, "posture", "export")...)
			got := exitcode.OK
			if err != nil {
				got = exitcode.From(err)
			}
			if got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
			if tc.want == exitcode.Degraded && !strings.Contains(out, "TRUNCATED") {
				t.Errorf("a capped export must say so on stdout, got:\n%s", out)
			}
		})
	}
}

// ---- PERMIT 6: the byte-artifact routes -------------------------------------------------

// TestArtifactVerbsRequireAnExplicitDestination: these routes answer with HTML or
// PDF, so a default of "print to the terminal" would be a mangled screen and a
// default that depends on isatty would be a contract a script cannot rely on.
func TestArtifactVerbsRequireAnExplicitDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"reports-get", []string{"reporting", "reports", "get", "audit-summary"}},
		{"templates-get", []string{"reporting", "templates", "get", "audit-summary"}},
		{"schedule-run", []string{"reporting", "schedules", "run", "sch-1", "run-7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, "<html></html>")
			_, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			if err == nil {
				t.Fatal("an artifact verb must refuse without --out")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if n := spy.count(); n != 0 {
				t.Errorf("%d request(s); want 0 — generating a report only to refuse to write it wastes the engine's work", n)
			}
		})
	}
}

// TestTheArtifactBackstopRefusesAnEmptyDestination gives the INNER --out guard a
// witness of its own.
//
// It exists because the mutation round measured it BLIND. Every artifact verb
// checks --out before spending the engine's work, so the check inside
// writeObserveArtifact never fires through a command, and a mutant that made it
// default to stdout SURVIVED the entire suite (M06). The behavior was never
// wrong — the backstop was simply unreachable from the tests, and an unreachable
// guard does not fail, it certifies. The fix is a witness, not a deletion: the
// second layer is what protects the next caller added to this lane, who will not
// have read the first.
func TestTheArtifactBackstopRefusesAnEmptyDestination(t *testing.T) {
	res := observeResult{status: http.StatusOK, raw: []byte("<html>doc</html>"), contentType: "text/html"}

	// DENY: no destination, and whitespace is not a destination either.
	for _, dest := range []string{"", "   ", "\t"} {
		var out, errOut bytes.Buffer
		cmd := &cobra.Command{Use: "probe"}
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		err := writeObserveArtifact(cmd, dest, res, "the probe artifact")
		if err == nil {
			t.Fatalf("dest %q: the backstop must refuse a destination that names nothing", dest)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("dest %q: exit = %d, want %d (usage)", dest, got, exitcode.Usage)
		}
		if out.Len() != 0 {
			t.Errorf("dest %q: the refusal put %d byte(s) on stdout; a pipe must stay clean", dest, out.Len())
		}
	}

	// PERMIT: with a destination it writes the engine's bytes verbatim and the
	// receipt names what it wrote. Without this half, "it refused" would also be
	// satisfied by a function that refuses everything.
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{Use: "probe"}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	dest := filepath.Join(t.TempDir(), "artifact.html")
	if err := writeObserveArtifact(cmd, dest, res, "the probe artifact"); err != nil {
		t.Fatalf("with a destination the backstop must write, got %v", err)
	}
	got, rerr := os.ReadFile(dest) //nolint:gosec // test-owned temp path
	if rerr != nil {
		t.Fatalf("read written artifact: %v", rerr)
	}
	if string(got) != string(res.raw) {
		t.Errorf("the backstop rewrote the artifact:\n got %q\nwant %q", got, res.raw)
	}
	if !strings.Contains(out.String(), "text/html") {
		t.Errorf("the receipt must name the content type the server sent, got:\n%s", out.String())
	}
}

// TestArtifactWritesTheEnginesBytesVerbatim: what lands on disk must be what the
// control plane produced, byte for byte. An evidence bundle the CLI reformatted
// is not evidence of anything.
func TestArtifactWritesTheEnginesBytesVerbatim(t *testing.T) {
	doc := "<html><body>Compliance Evidence &amp; \u00e9\u00e9\u00e9</body></html>"
	spy := newObserveSpy(t, http.StatusOK, doc)
	spy.ctype = "text/html; charset=utf-8"
	dest := filepath.Join(t.TempDir(), "evidence.html")

	out, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"reporting", "reports", "get", "compliance-evidence", "--out", dest)...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	got, rerr := os.ReadFile(dest) //nolint:gosec // test-owned temp path
	if rerr != nil {
		t.Fatalf("read written artifact: %v", rerr)
	}
	if string(got) != doc {
		t.Fatalf("the artifact was not written verbatim:\n got %q\nwant %q", got, doc)
	}
	// The receipt names the size and the type the server actually sent.
	if !strings.Contains(out, fmt.Sprintf("%d bytes", len(doc))) {
		t.Errorf("the receipt must state the byte count, got:\n%s", out)
	}
	if !strings.Contains(out, "text/html") {
		t.Errorf("the receipt must name the content type the server sent, got:\n%s", out)
	}
}

// TestArtifactToStdoutKeepsTheReceiptOffStdout is the property that makes
// `... --out - > report.html` produce a clean document: the receipt goes to
// STDERR when the artifact owns stdout.
func TestArtifactToStdoutKeepsTheReceiptOffStdout(t *testing.T) {
	doc := "<html>report</html>"
	spy := newObserveSpy(t, http.StatusOK, doc)
	spy.ctype = "text/html"
	out, errOut, err := execRoot(t, observeArgs(spy.srv.URL,
		"reporting", "reports", "get", "audit-summary", "--out", "-")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if out != doc {
		t.Fatalf("stdout must carry the document ALONE:\n got %q\nwant %q", out, doc)
	}
	if !strings.Contains(errOut, "bytes") {
		t.Errorf("the receipt must go to stderr, got stderr:\n%s", errOut)
	}
}

// TestScheduleRunNamesWhichOfTheTwoAnswersItGot: this route returns the artifact
// when the run produced one and JSON metadata when it did not. Writing the JSON
// into report.pdf without a word would look like a corrupt report.
func TestScheduleRunNamesWhichOfTheTwoAnswersItGot(t *testing.T) {
	t.Run("artifact", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, "<html>ran</html>")
		spy.ctype = "text/html"
		out, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"reporting", "schedules", "run", "sch-1", "run-7", "--out", filepath.Join(t.TempDir(), "r.html"))...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if !strings.Contains(out, "report artifact") {
			t.Errorf("an artifact answer must be named as such, got:\n%s", out)
		}
	})
	t.Run("metadata for a failed run", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"run-7","status":"failed","error":"render failed"}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"reporting", "schedules", "run", "sch-1", "run-7", "--out", filepath.Join(t.TempDir(), "r.json"))...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if !strings.Contains(out, "NO report artifact") {
			t.Errorf("a JSON answer must say the run stored no artifact, got:\n%s", out)
		}
	})
}

// TestTemplateSetSendsRawHTMLNotAJSONString: this route takes a raw body. Wrapping
// the template in a JSON string would store escaped text where the renderer
// expects markup — a report that renders as source code.
func TestTemplateSetSendsRawHTMLNotAJSONString(t *testing.T) {
	tmpl := "<html><body>{{ .Title }}</body></html>"
	dir := t.TempDir()
	path := filepath.Join(dir, "tmpl.html")
	if err := os.WriteFile(path, []byte(tmpl), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	spy := newObserveSpy(t, http.StatusOK, `{"stored":true,"report_type":"audit-summary"}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"reporting", "templates", "set", "audit-summary", path)...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	got := spy.last(t)
	if got.method != http.MethodPut {
		t.Errorf("method = %s, want PUT", got.method)
	}
	if got.body != tmpl {
		t.Fatalf("the template must be sent VERBATIM:\n got %q\nwant %q", got.body, tmpl)
	}
	if !strings.HasPrefix(got.ctype, "text/html") {
		t.Errorf("Content-Type = %q, want text/html — this route does not take JSON", got.ctype)
	}
	if !strings.Contains(out, "stored") {
		t.Errorf("a confirmed store must be reported, got:\n%s", out)
	}
}

// TestTemplateSetRefusesAnEmptyDocument: "store nothing" has no reading other
// than `templates rm`, and the engine refuses it too.
func TestTemplateSetRefusesAnEmptyDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.html")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	spy := newObserveSpy(t, http.StatusOK, `{"stored":true}`)
	_, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"reporting", "templates", "set", "audit-summary", path)...)
	if err == nil {
		t.Fatal("an empty template must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
	}
	if n := spy.count(); n != 0 {
		t.Errorf("%d request(s); want 0", n)
	}
}

// TestTemplateSetDoesNotClaimAStoreTheEngineDidNotConfirm: the engine answers
// {"stored":true}. Anything else must not be reported as a success.
func TestTemplateSetDoesNotClaimAStoreTheEngineDidNotConfirm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmpl.html")
	if err := os.WriteFile(path, []byte("<html></html>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	spy := newObserveSpy(t, http.StatusOK, `{"stored":false}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"reporting", "templates", "set", "audit-summary", path)...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if strings.Contains(out, "stored a ") {
		t.Errorf("an unconfirmed store must not be reported as done, got:\n%s", out)
	}
	if !strings.Contains(out, "did not confirm") {
		t.Errorf("the operator must be told the store was not confirmed, got:\n%s", out)
	}
}

// ---- PERMIT 7: a create that created nothing is not a success ---------------------------

func TestCreatesRefuseToConfirmWithoutAnID(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"consoleviews", []string{"consoleviews", "create", "--feature-id", "f", "--name", "n", "--params", "{}"}},
		{"health-checks", []string{"health", "checks", "create", "--subject-kind", "agent", "--subject-ref", "a", "--interval", "60"}},
		{"notify-routes", []string{"notify", "routes", "create", "--name", "n", "--destination", "d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 200 with no id: the shape a dual-control or queued write produces.
			spy := newObserveSpy(t, http.StatusOK, `{"status":"pending"}`)
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			if err == nil {
				t.Fatal("a create that returns no id must not report success")
			}
			if got := exitcode.From(err); got != exitcode.Server {
				t.Errorf("exit = %d, want %d (server)", got, exitcode.Server)
			}
			if strings.Contains(out, "created") || strings.Contains(out, "declared") {
				t.Errorf("nothing may claim a create happened, got:\n%s", out)
			}
		})
	}
}

// ---- PERMIT 8: the SSE stream becomes NDJSON ---------------------------------------------

// TestHealthWatchEmitsNDJSONAndDropsKeepalives: the point of the verb is that it
// pipes into jq. Emitting the SSE framing, or the keepalive comments, would make
// every consumer parse it out.
func TestHealthWatchEmitsNDJSONAndDropsKeepalives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/m/health/stream" {
			t.Errorf("path = %s, want /v1/m/health/stream", r.URL.Path)
		}
		if r.URL.Query().Get("subject_ref") != "ag-7" {
			t.Errorf("subject_ref = %q, want ag-7", r.URL.Query().Get("subject_ref"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// The exact framing the engine writes: a connected comment, two events
		// and a keepalive ping between them.
		_, _ = io.WriteString(w, ": connected\n\n")
		_, _ = io.WriteString(w, "event: health\ndata: {\"subject_ref\":\"ag-7\",\"state\":\"healthy\"}\n\n")
		_, _ = io.WriteString(w, ": ping\n\n")
		_, _ = io.WriteString(w, "event: health\ndata: {\"subject_ref\":\"ag-7\",\"state\":\"down\"}\n\n")
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, observeArgs(srv.URL, "health", "watch", "--subject-ref", "ag-7")...)
	if err != nil {
		t.Fatalf("a stream that ends cleanly must exit 0, got %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON: %q", i, line)
		}
	}
	for _, banned := range []string{"event:", "data:", "ping", "connected"} {
		if strings.Contains(out, banned) {
			t.Errorf("the SSE framing %q leaked into the output:\n%s", banned, out)
		}
	}
	if !strings.Contains(lines[0], "healthy") || !strings.Contains(lines[1], "down") {
		t.Errorf("both events must be emitted in order, got:\n%s", out)
	}
}

// TestHealthWatchOutlivesTheOrdinaryRequestDeadline is the witness for a defect
// this lane SHIPPED IN ITS FIRST DRAFT and a mutation run did not find, because
// nothing was measuring it.
//
// http.Client.Timeout covers reading the BODY. So the ordinary ten-second CLI
// deadline does not cut a slow request short — it kills a perfectly healthy SSE
// attach ten seconds in. Shipped exactly this on `agent session attach`,
// which is why cliTransportOptions has an Unbounded field at all
// (clitransport.go:41-51); the first version of `health watch` passed Timeout
// instead and reproduced it, with help text recommending `--timeout 0` — which
// means "unspecified" and yields the same ten seconds.
//
// defaultCLIRequestTimeout is a var precisely so this can be proven in
// milliseconds instead of ten seconds of wall clock.
func TestHealthWatchOutlivesTheOrdinaryRequestDeadline(t *testing.T) {
	restore := defaultCLIRequestTimeout
	t.Cleanup(func() { defaultCLIRequestTimeout = restore })
	defaultCLIRequestTimeout = 80 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_, _ = io.WriteString(w, ": connected\n\n")
		_ = rc.Flush()
		// Hold the stream open well past the ordinary deadline, then deliver.
		// A bounded client dies here with nothing on stdout.
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, "event: health\ndata: {\"state\":\"healthy\"}\n\n")
		_ = rc.Flush()
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, observeArgs(srv.URL, "health", "watch")...)
	if err != nil {
		t.Fatalf("the stream must outlive the ordinary request deadline, got %v (exit %d)", err, exitcode.From(err))
	}
	if !strings.Contains(out, "healthy") {
		t.Fatalf("the event delivered after the deadline never arrived — the stream was cut short:\n%s", out)
	}
}

// TestHealthWatchClassifiesARefusal: a refusal on the stream must be classified
// like every other verb rather than read as an empty stream.
func TestHealthWatchClassifiesARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"no"}}`)
	}))
	t.Cleanup(srv.Close)
	out, _, err := execRoot(t, observeArgs(srv.URL, "health", "watch")...)
	if err == nil {
		t.Fatal("a 403 on the stream must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Errorf("exit = %d, want %d (auth)", got, exitcode.Auth)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout must be empty on a refusal, got:\n%s", out)
	}
}

// TestHealthWatchRefusesWithoutACredential: the stream does not go through
// observeCall, so its precondition checks are its own and need their own witness.
func TestHealthWatchRefusesWithoutACredential(t *testing.T) {
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	_, _, err := execRoot(t, "health", "watch", "--server", srv.URL, "--tenant", "t", "--token", "")
	if err == nil {
		t.Fatal("the stream must refuse without a credential")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
	}
	if reached != 0 {
		t.Errorf("%d request(s) reached the server; want 0", reached)
	}
}

// ---- PERMIT 9: the honest-rendering properties -------------------------------------------

// TestUnknownableGateStateRendersAsUnknownNotOff: the engine sends nil for a gate
// it cannot observe, deliberately. Rendering nil as "no" converts "I cannot see"
// into "it is off".
func TestUnknownableGateStateRendersAsUnknownNotOff(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"since":"2026-08-01T00:00:00Z","engine_scope":true,"sources":[],
		"standards":[
			{"id":"a","label":"unknowable","status":"available"},
			{"id":"b","label":"off","status":"opt_in_off","opt_in_active":false},
			{"id":"c","label":"on","status":"active","opt_in_active":true,"records_total":42}
		]}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "observability", "ingestion-health")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("an unobservable opt-in gate must render as unknown, got:\n%s", out)
	}
	// The counter is omitted rather than guessed when not attributable.
	if !strings.Contains(out, "42") {
		t.Errorf("an attributable count must be shown, got:\n%s", out)
	}
	if !strings.Contains(out, "ENGINE-WIDE") {
		t.Errorf("the counters must be labeled engine-wide, not per-tenant, got:\n%s", out)
	}
}

// TestEmptyListsSayWhichEmptinessTheyMean: on a fresh install every list is
// empty, and "no rows" must be distinguishable from "the command did nothing".
func TestEmptyListsSayWhichEmptinessTheyMean(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"notify-routes", []string{"notify", "routes", "ls"}, "no notification route is declared"},
		{"notify-destinations", []string{"notify", "destinations"}, "no destination is provisioned"},
		{"health-status", []string{"health", "status"}, "nothing is being monitored yet"},
		{"health-checks", []string{"health", "checks", "ls"}, "no check has been declared"},
		{"accessmap-graph", []string{"accessmap", "graph"}, "no access edges match this query"},
		{"inventory-entities", []string{"inventory", "entities", "ls"}, "no catalog entities match"},
		{"consoleviews", []string{"consoleviews", "ls"}, "no saved views are visible to you"},
		{"observability-traces", []string{"observability", "traces", "ls"}, "no traces in the audit ledger"},
		{"reporting-schedules", []string{"reporting", "schedules", "ls"}, "no report schedule is declared"},
		{"adoption-teams", []string{"adoption", "teams"}, "no team recorded activity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, `{"items":[],"nodes":[],"edges":[],"teams":[],"destinations":[]}`)
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			if err != nil {
				t.Fatalf("an empty list must exit 0, got %v", err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatal("an empty list emitted ZERO BYTES: an operator cannot tell that from a swallowed command")
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("want the empty note %q, got:\n%s", tc.want, out)
			}
		})
	}
}

// TestAdoptionRefusesToTotalTheTwoLenses: the two lenses are two views of the
// same activity. A total would double-count it, and the command must say so.
func TestAdoptionRefusesToTotalTheTwoLenses(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"developers":3,"teams":2,"analytics":{"totals":{"sessions":10}},"telemetry":{"totals":{"sessions":7}},
		  "boundary":{"claude_api_only":true,"excludes":["bedrock"]}}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "adoption", "summary")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if !strings.Contains(out, "NOT additive") {
		t.Errorf("the non-additivity must be stated, got:\n%s", out)
	}
	if strings.Contains(out, "17") {
		t.Errorf("the two lenses must never be summed (10+7), got:\n%s", out)
	}
	if !strings.Contains(out, "Claude API activity only") {
		t.Errorf("the measurement boundary must be stated, got:\n%s", out)
	}
}

// TestAcceptanceRateWithNoDecisionsIsNotZeroPercent: the engine sends null when
// no tool decision was made. 0% would report perfect rejection.
func TestAcceptanceRateWithNoDecisionsIsNotZeroPercent(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"analytics":{"totals":{"sessions":1,"acceptance_rate":null}},"telemetry":{"totals":{}},"boundary":{}}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "adoption", "summary")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if strings.Contains(out, "0.0%") {
		t.Errorf("a null acceptance rate must not render as 0%%, got:\n%s", out)
	}
	if !strings.Contains(out, "n/a") {
		t.Errorf("a null acceptance rate must render as n/a, got:\n%s", out)
	}
}

// TestResolveIncidentDoesNotClaimAnActThatDidNotHappen: the engine returns the
// row either way and only transitions one that was open.
func TestResolveIncidentDoesNotClaimAnActThatDidNotHappen(t *testing.T) {
	t.Run("actually resolved", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"inc-1","state":"resolved","resolved_at":"2026-08-16T10:00:00Z"}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "health", "incidents", "resolve", "inc-1")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if !strings.Contains(out, "is resolved") {
			t.Errorf("a real resolution must be reported, got:\n%s", out)
		}
	})
	t.Run("not resolved", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"inc-1","state":"open"}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "health", "incidents", "resolve", "inc-1")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if !strings.Contains(out, "was NOT closed by this call") {
			t.Errorf("an incident that did not transition must not be reported as closed, got:\n%s", out)
		}
	})
}

// TestEvaluateNamesADisabledRouteAsUnableToFire: "matches" and "will fire" are
// different, and the gap between them is the usual explanation for a rule that
// looks right and delivers nothing.
func TestEvaluateNamesADisabledRouteAsUnableToFire(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"matched_count":1,"items":[{"id":"rt-1","name":"criticals","enabled":false,"matched":true,"mismatches":[]}]}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"notify", "evaluate", "--event-type", "finding.created")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if !strings.Contains(out, "DISABLED") {
		t.Errorf("a matching-but-disabled route must be called out, got:\n%s", out)
	}
	if !strings.Contains(out, "NOT simulated") {
		t.Errorf("the predicate-only scope must be stated, got:\n%s", out)
	}
	// It is a dry run: it must reach the evaluate route and nothing else.
	if got := spy.last(t); got.path != "/v1/m/notify/routes/evaluate" || got.method != http.MethodPost {
		t.Errorf("evaluate reached %s %s, want POST /v1/m/notify/routes/evaluate", got.method, got.path)
	}
}

// TestKeyShadowIsReportedAboveTheGraph: a static API key takes precedence over
// federation, so a perfectly configured WIF graph can be inert. Printing the
// graph without that warning shows federation that is not in effect.
func TestKeyShadowIsReportedAboveTheGraph(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"issuers":[{"id":"fdis_1","issuer_url":"https://idp","ca_cert_configured":true}],
		  "rules":[],"service_accounts":[{"id":"svac_1","organization_role":"admin"}],
		  "key_shadow":{"present":true,"var":"ANTHROPIC_API_KEY"},
		  "reconciliation":{"reconciled":false,"unavailable":"no org:admin token"}}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "identity", "wif")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if !strings.Contains(out, "STATIC KEY SHADOWS FEDERATION") {
		t.Errorf("the shadowing key must be reported, got:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Errorf("the shadowing variable must be named, got:\n%s", out)
	}
	if !strings.Contains(out, "NOT RECONCILED") {
		t.Errorf("an unreconciled graph must not read as verified, got:\n%s", out)
	}
	if !strings.Contains(out, "ADMIN org role") {
		t.Errorf("an admin-role service account is a posture signal and must be named, got:\n%s", out)
	}
	if !strings.Contains(out, "fdis_1") {
		t.Errorf("the issuer must be listed with the real wire field names, got:\n%s", out)
	}
}

// TestAttackPathHopsCountEdgesNotNodes: a two-node path is ONE hop. Counting
// nodes inflates every adjacent thing into a two-hop chain.
func TestAttackPathHopsCountEdgesNotNodes(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"paths":[{"kind":"reachability","min_confidence":"firm","attribution":"firm","steps":[
			{"node_kind":"agent","node_id":"ag-7","node_name":"builder"},
			{"node_kind":"resource","node_id":"r1","node_name":"secrets","mode":"rw"}]}]}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"accessmap", "attack-paths", "reachability", "--agent-id", "ag-7")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	if !strings.Contains(out, "builder -> secrets(rw)") {
		t.Errorf("the path must be rendered from the real node fields, got:\n%s", out)
	}
	// One edge between two nodes.
	if !strings.Contains(out, "\t1\t") && !strings.Contains(out, " 1 ") {
		t.Errorf("a two-node path is ONE hop, got:\n%s", out)
	}
}

// ---- the response-size bound -------------------------------------------------------------

// TestAnOverLargeResponseIsDetectedNotTruncated: a body past the cap must fail
// loudly. A silently truncated body can still parse, and then the CLI reports a
// partial answer as a complete one.
func TestAnOverLargeResponseIsDetectedNotTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"items":[`)
		chunk := strings.Repeat("x", 1<<16)
		for written := 0; written < maxObserveBodySize+(1<<20); written += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	_, _, err := execRoot(t, observeArgs(srv.URL, "inventory", "entities", "ls")...)
	if err == nil {
		t.Fatal("an over-cap body must be reported, not silently truncated")
	}
	if got := exitcode.From(err); got != exitcode.Server {
		t.Errorf("exit = %d, want %d (server)", got, exitcode.Server)
	}
	// AND IT MUST FAIL FOR THE RIGHT REASON. The first version of this test
	// asserted only "non-nil, exit 6", and a mutant that deleted the cap check
	// SURVIVED it (M05): without the check the truncated body is invalid JSON, so
	// decode() failed and the test went red on a parse error while the bound it
	// exists for was gone. Naming the bound is what makes the witness measure the
	// guard rather than its neighbor.
	if msg := err.Error(); !strings.Contains(msg, "exceeds") {
		t.Errorf("the refusal must name the size bound it enforced, not a downstream parse failure; got: %s", msg)
	}
}

// TestTemplateSetRefusesADocumentOverTheCap is the INPUT half of the same bound,
// and it is here because the mutation round found that half blind (M18): no test
// reached it, so removing it changed nothing that anything could see.
//
// It exercises readObserveDocument directly through stdin. Going through the
// command would need a 32 MiB temp file for the same assertion.
func TestTemplateSetRefusesADocumentOverTheCap(t *testing.T) {
	cmd := &cobra.Command{Use: "probe"}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	// DENY: one byte past the cap. `x` is not whitespace, so this cannot be
	// mistaken for the empty-document refusal.
	cmd.SetIn(io.LimitReader(repeatReader{'x'}, int64(maxObserveBodySize)+1))
	_, err := readObserveDocument(cmd, "-")
	if err == nil {
		t.Fatal("a document past the cap must be refused before it is sent")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want %d (usage): the oversized file is the caller's own argument", got, exitcode.Usage)
	}
	if msg := err.Error(); !strings.Contains(msg, "exceeds") {
		t.Errorf("the refusal must name the bound it enforced, got: %s", msg)
	}

	// PERMIT: a document AT the cap is accepted whole — the bound is "more than",
	// not "close to", and without this half the refusal above is also satisfied by
	// a reader that refuses everything.
	cmd.SetIn(io.LimitReader(repeatReader{'x'}, int64(maxObserveBodySize)))
	doc, err := readObserveDocument(cmd, "-")
	if err != nil {
		t.Fatalf("a document exactly at the cap must be accepted, got %v", err)
	}
	if len(doc) != maxObserveBodySize {
		t.Errorf("read %d bytes, want %d — the document must arrive whole", len(doc), maxObserveBodySize)
	}
}

// repeatReader yields one byte forever. It is the cheap way to present a body
// larger than the cap without allocating one.
type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

// ---- the lane's own registration ----------------------------------------------------------

// TestEveryFamilyInTheLotIsRegistered is the positive control for the whole file:
// without it, a family whose constructor was never added to the root would make
// every test above vacuous for that family by simply not existing.
func TestEveryFamilyInTheLotIsRegistered(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{
		"reporting", "notify", "health", "accessmap", "observability",
		"consoleviews", "adoption", "identity", "inventory", "posture",
	} {
		if cmd := resolveCommandPath(t, root, "olivares "+name); cmd == nil {
			t.Errorf("olivares %s is not registered on the root command", name)
		}
	}
}

// TestLaneVerbCountMatchesTheRouteCensus pins the surface at the 73 routes the
// census counted, so a verb silently lost in a merge is a failing test rather
// than a quietly smaller CLI.
func TestLaneVerbCountMatchesTheRouteCensus(t *testing.T) {
	root := newRootCmd()
	want := map[string]int{
		"reporting": 15, "notify": 14, "health": 14, "accessmap": 7,
		"observability": 5, "consoleviews": 5, "adoption": 5,
		"identity": 4, "inventory": 3, "posture": 1,
	}
	total := 0
	for family, n := range want {
		cmd := resolveCommandPath(t, root, "olivares "+family)
		if cmd == nil {
			t.Errorf("%s is not registered", family)
			continue
		}
		got := countLeafVerbs(cmd)
		if got != n {
			t.Errorf("%s exposes %d verb(s), want %d (one per route the census counted)", family, got, n)
		}
		total += n
	}
	if total != 73 {
		t.Fatalf("the census table itself sums to %d, not the 73 routes it describes", total)
	}
}

// countLeafVerbs counts the commands that address a route — the LEAVES.
//
// It cannot ask "does it have a RunE": enforceSubcommandContract (main.go) gives
// EVERY group one, so that an unknown subcommand exits non-zero instead of
// printing help and exiting 0. Counting runnable commands therefore counts
// navigation as well as verbs, which is how the first version of this test
// reported 21 verbs for reporting's 15 routes and 5 groups.
func countLeafVerbs(cmd *cobra.Command) int {
	if !cmd.HasSubCommands() {
		return 1
	}
	n := 0
	for _, child := range cmd.Commands() {
		n += countLeafVerbs(child)
	}
	return n
}

// ---- GOVERNANCE: the fact the list cannot show -------------------------------------

// TestKillSwitchStateReportsTheEstateFlagTheListCannot is the reason `killswitch state`
// exists beside `killswitch ls --status active` instead of being folded into it.
//
// An estate-wide stop is a property of the enforcement PLANE, not a row in the collection.
// A caller who only listed active switches could see an EMPTY list while every scope in the
// estate is denied — the worst possible reading during an incident, because it looks like
// nothing is wrong. Both directions are asserted: without them, a command that always
// printed "ESTATE STOPPED", or never did, would satisfy the other half.
func TestKillSwitchStateReportsTheEstateFlagTheListCannot(t *testing.T) {
	t.Run("stopped with an empty list is still STOPPED", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"estate_stopped":true,"active":[]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "killswitch", "state")...)
		if err != nil {
			t.Fatalf("state failed: %v", err)
		}
		if !strings.Contains(out, "ESTATE STOPPED") {
			t.Fatalf("an estate-wide stop was not reported; a caller reading this would think nothing is halted:\n%s", out)
		}
	})

	t.Run("running says so, and does not cry stop", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"estate_stopped":false,"active":[]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "killswitch", "state")...)
		if err != nil {
			t.Fatalf("state failed: %v", err)
		}
		if strings.Contains(out, "ESTATE STOPPED") {
			t.Fatalf("a running estate was reported as stopped, so the flag is not being read:\n%s", out)
		}
		if !strings.Contains(out, "estate running") || !strings.Contains(out, "no kill switch is active") {
			t.Fatalf("the running case says neither half of what it should:\n%s", out)
		}
	})

	t.Run("an active switch is rendered with its scope and reason", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"estate_stopped":false,"active":[{"id":"ks-1","scope_kind":"agent","agent_external_id":"agent-7",`+
				`"status":"active","source":"operator","engaged_by":"ops@example.invalid","reason":"cost runaway"}]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "killswitch", "state")...)
		if err != nil {
			t.Fatalf("state failed: %v", err)
		}
		for _, want := range []string{"ks-1", "agent-7", "cost runaway"} {
			if !strings.Contains(out, want) {
				t.Errorf("the table does not show %q, so the operator cannot act on it:\n%s", want, out)
			}
		}
	})
}

// TestKillSwitchListSendsItsFiltersToTheEngine. --limit and --cursor are declared on this
// verb only because the route READS them (modules/governance/killswitch.go calls listQuery,
// which parses both). The transport's own comment warns that a paging flag on a route that
// ignores it makes the second page the first page forever, so the witness asserts the
// parameters LEAVE — not merely that the flag parses.
func TestKillSwitchListSendsItsFiltersToTheEngine(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"items":[]}`)
	if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"governance", "killswitch", "ls", "--status", "active", "--limit", "20")...); err != nil {
		t.Fatalf("ls failed: %v", err)
	}
	got := spy.last(t)
	if got.query.Get("status") != "active" {
		t.Errorf("status = %q, want active: the filter never reached the engine", got.query.Get("status"))
	}
	if got.query.Get("limit") != "20" {
		t.Errorf("limit = %q, want 20: paging is declared but not sent, so the second page repeats the first",
			got.query.Get("limit"))
	}

	// NOT-FIRING DIRECTION: with no flags, neither parameter is invented. A client that
	// always sent status= would silently narrow every listing.
	spy2 := newObserveSpy(t, http.StatusOK, `{"items":[]}`)
	if _, _, err := execRoot(t, observeArgs(spy2.srv.URL, "governance", "killswitch", "ls")...); err != nil {
		t.Fatalf("bare ls failed: %v", err)
	}
	if q := spy2.last(t).query; q.Get("status") != "" || q.Get("limit") != "" {
		t.Errorf("a bare `ls` sent status=%q limit=%q; it must send neither", q.Get("status"), q.Get("limit"))
	}
}
