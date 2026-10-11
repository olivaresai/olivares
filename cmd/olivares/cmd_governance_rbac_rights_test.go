// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// effectiveRightsFixture is an answer of GET /v1/auth/effective-rights: an agent reached
// through an agent group of another workspace, with one right the engine could not decide.
const effectiveRightsFixture = `{"subject":{"kind":"user","id":"u-1"},"node":{"kind":"agent","id":"a-1"},"assurance":3,` +
	`"path":[{"kind":"workspace","ref":"payments"},{"kind":"agent_group","ref":"ops","workspace":"default"},{"kind":"agent","ref":"a-1"}],` +
	`"rights":[{"name":"Supervisor","state":"not_held"},{"name":"Browse","state":"held"},{"name":"Write","state":"unknown"}]}`

// rightsServer answers the effective-rights read with body and status, recording each request.
func rightsServer(t *testing.T, status int, body string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func rightsArgs(server string, extra ...string) []string {
	return append([]string{"governance", "rbac", "rights", "agent", "a-1",
		"--server", server, "--token", "olvs_admin-fixture", "--tenant", "tenant-a", "--insecure"}, extra...)
}

func TestGovernanceRBACRightsReadsTheEngineAnswer(t *testing.T) {
	server, seen := rightsServer(t, http.StatusOK, effectiveRightsFixture)

	out, stderr, err := execRoot(t, rightsArgs(server.URL, "--subject", "u-1")...)
	if err != nil {
		t.Fatalf("rights: %v %s", err, stderr)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %d, want 1", len(*seen))
	}
	r := (*seen)[0]
	q := r.URL.Query()
	if r.Method != http.MethodGet || r.URL.Path != "/v1/auth/effective-rights" ||
		q.Get("subject_type") != "user" || q.Get("subject_id") != "u-1" || q.Get("kind") != "agent" || q.Get("id") != "a-1" {
		t.Fatalf("request = %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer olvs_admin-fixture" || r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
		t.Fatal("request did not carry the caller's credential and tenant")
	}
	for _, want := range []string{
		"workspace payments > agent_group ops (workspace default) > agent a-1",
		"user u-1", "agent a-1", "not a deny",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Each right on its own row with its own state, as the engine paired them.
	for _, row := range []string{`(?m)^Supervisor\s+not held$`, `(?m)^Browse\s+held$`, `(?m)^Write\s+unknown$`} {
		if !regexp.MustCompile(row).MatchString(out) {
			t.Errorf("output lacks row %s:\n%s", row, out)
		}
	}
}

func TestGovernanceRBACRightsRefusesABodyThatIsNotAnAnswer(t *testing.T) {
	for _, body := range []string{`{}`, `{"path":[],"rights":[]}`, `null`} {
		server, _ := rightsServer(t, http.StatusOK, body)

		out, _, err := execRoot(t, rightsArgs(server.URL, "--subject", "u-1")...)
		if err == nil || exitcode.From(err) != exitcode.Server {
			t.Fatalf("body %s: err = %v (exit %d), want exit %d", body, err, exitcode.From(err), exitcode.Server)
		}
		if strings.Contains(out, "right") {
			t.Fatalf("body %s printed an answer:\n%s", body, out)
		}
	}
}

func TestGovernanceRBACRightsAsksForATokenSubject(t *testing.T) {
	server, seen := rightsServer(t, http.StatusOK, effectiveRightsFixture)

	if _, stderr, err := execRoot(t, rightsArgs(server.URL, "--subject", "t-9", "--subject-type", "token")...); err != nil {
		t.Fatalf("rights: %v %s", err, stderr)
	}
	if q := (*seen)[0].URL.Query(); q.Get("subject_type") != "token" || q.Get("subject_id") != "t-9" {
		t.Fatalf("query = %s", (*seen)[0].URL.RawQuery)
	}
}

func TestGovernanceRBACRightsJSONIsTheEngineAnswer(t *testing.T) {
	server, _ := rightsServer(t, http.StatusOK, effectiveRightsFixture)

	out, stderr, err := execRoot(t, rightsArgs(server.URL, "--subject", "u-1", "-o", "json")...)
	if err != nil {
		t.Fatalf("rights: %v %s", err, stderr)
	}
	var got, want any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	_ = json.Unmarshal([]byte(effectiveRightsFixture), &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("json = %s, want %s", gotJSON, wantJSON)
	}
}

func TestGovernanceRBACRightsNoUnknownNoteWhenEveryRightWasDecided(t *testing.T) {
	decided := strings.Replace(effectiveRightsFixture, `"unknown"`, `"not_held"`, 1)
	server, _ := rightsServer(t, http.StatusOK, decided)

	out, stderr, err := execRoot(t, rightsArgs(server.URL, "--subject", "u-1")...)
	if err != nil {
		t.Fatalf("rights: %v %s", err, stderr)
	}
	if strings.Contains(out, "not a deny") {
		t.Fatalf("unknown note printed with no unknown right:\n%s", out)
	}
}

func TestGovernanceRBACRightsRequiresASubject(t *testing.T) {
	server, seen := rightsServer(t, http.StatusOK, effectiveRightsFixture)

	_, _, err := execRoot(t, rightsArgs(server.URL)...)
	if err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("err = %v, want the missing --subject refused", err)
	}
	if len(*seen) != 0 {
		t.Fatal("a request was sent without a subject")
	}
}

func TestGovernanceRBACRightsKeepsTheEngineRefusal(t *testing.T) {
	server, _ := rightsServer(t, http.StatusNotFound, `{"error":"not found"}`)

	out, _, err := execRoot(t, rightsArgs(server.URL, "--subject", "u-1")...)
	if err == nil || exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("err = %v (exit %d), want exit %d", err, exitcode.From(err), exitcode.NotFound)
	}
	if strings.Contains(out, "right") {
		t.Fatalf("a refusal printed rights:\n%s", out)
	}
}

func TestGovernanceRBACRightsStripsControlCharactersFromTheAnswer(t *testing.T) {
	hostile := strings.Replace(effectiveRightsFixture, `"ref":"payments"`, `"ref":"pay\u001b[2Jments"`, 1)
	server, _ := rightsServer(t, http.StatusOK, hostile)

	out, stderr, err := execRoot(t, rightsArgs(server.URL, "--subject", "u-1")...)
	if err != nil {
		t.Fatalf("rights: %v %s", err, stderr)
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("terminal escape reached the output: %q", out)
	}
}
