// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const filtersPath = "/v1/m/governance/rbac/inheritance-filters"

func TestGovernanceRBACFiltersSetPostsTheNodeAndClass(t *testing.T) {
	spy := newObserveSpy(t, http.StatusCreated,
		`{"id":"f-1","scope_tree":"workspace","scope_ref":"payments","scope_class":"session","created_by":"u-admin"}`)

	out, stderr, err := execRoot(t, observeArgs(spy.srv.URL,
		"governance", "rbac", "filters", "set", "workspace", "payments", "--class", "session")...)
	if err != nil {
		t.Fatalf("filters set: %v %s", err, stderr)
	}
	got := spy.last(t)
	if got.method != http.MethodPost || got.path != filtersPath || got.ctype != "application/json" {
		t.Fatalf("request = %s %s (%s)", got.method, got.path, got.ctype)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("body %q: %v", got.body, err)
	}
	want := map[string]string{"scope_tree": "workspace", "scope_ref": "payments", "scope_class": "session"}
	if len(body) != len(want) {
		t.Fatalf("body = %v, want exactly %v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %q, want %q", k, body[k], v)
		}
	}
	if !strings.Contains(out, "f-1") || !strings.Contains(out, "workspace payments") {
		t.Errorf("output does not name the stored filter:\n%s", out)
	}
}

func TestGovernanceRBACFiltersSetReportsTheEngineRefusal(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
	}{
		{http.StatusForbidden, exitcode.Auth},
		{http.StatusConflict, exitcode.Conflict},
	} {
		spy := newObserveSpy(t, tc.status,
			`{"error":{"message":"only an admin of the node may set or remove its inheritance filter"}}`)
		_, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"governance", "rbac", "filters", "set", "agent_group", "ops", "--class", "agent")...)
		if err == nil || exitcode.From(err) != tc.code {
			t.Fatalf("HTTP %d: err = %v (exit %d), want exit %d", tc.status, err, exitcode.From(err), tc.code)
		}
		if !strings.Contains(err.Error(), "only an admin of the node") {
			t.Errorf("HTTP %d: the engine's reason is lost: %v", tc.status, err)
		}
	}
}

func TestGovernanceRBACFiltersSetNeedsAClass(t *testing.T) {
	spy := newObserveSpy(t, http.StatusCreated, `{}`)
	_, _, err := execRoot(t, observeArgs(spy.srv.URL,
		"governance", "rbac", "filters", "set", "workspace", "payments")...)
	if err == nil {
		t.Fatal("set without --class succeeded")
	}
	if spy.count() != 0 {
		t.Fatalf("set without --class sent %d request(s)", spy.count())
	}
}

func TestGovernanceRBACFiltersLsPrintsEachNode(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"items":[`+
		`{"id":"f-1","scope_tree":"workspace","scope_ref":"payments","scope_class":"session","created_by":"u-admin"},`+
		`{"id":"f-2","scope_tree":"folder","scope_ref":"res-9","scope_class":"resource","created_by":"u-admin"}]}`)

	out, stderr, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "rbac", "filters", "ls")...)
	if err != nil {
		t.Fatalf("filters ls: %v %s", err, stderr)
	}
	if got := spy.last(t); got.method != http.MethodGet || got.path != filtersPath {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	for _, row := range []string{
		`(?m)^f-1\s+workspace\s+payments\s+session\s+u-admin$`,
		`(?m)^f-2\s+folder\s+res-9\s+resource\s+u-admin$`,
		`(?m)^2 filter\(s\)$`,
	} {
		if !regexp.MustCompile(row).MatchString(out) {
			t.Errorf("output lacks row %s:\n%s", row, out)
		}
	}
}

func TestGovernanceRBACFiltersGetReadsOneFilter(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK,
		`{"id":"f-1","scope_tree":"agent_group","scope_ref":"ops","scope_class":"agent","created_by":"u-admin"}`)
	out, stderr, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "rbac", "filters", "get", "f-1")...)
	if err != nil {
		t.Fatalf("filters get: %v %s", err, stderr)
	}
	if got := spy.last(t); got.method != http.MethodGet || got.path != filtersPath+"/f-1" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	for _, want := range []string{"f-1", "agent_group ops", "agent", "u-admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The get help promises exit 4 for an id the engine does not know.
func TestGovernanceRBACFiltersGetUnknownIDExitsNotFound(t *testing.T) {
	spy := newObserveSpy(t, http.StatusNotFound, `{"error":{"code":"not_found","message":"not found"}}`)
	_, _, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "rbac", "filters", "get", "f-404")...)
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("filters get on an unknown id: err = %v (exit %d), want exit %d", err, exitcode.From(err), exitcode.NotFound)
	}
}

// Removing a filter widens access below the node, so an unconfirmed rm sends nothing.
func TestGovernanceRBACFiltersRmAsksBeforeWidening(t *testing.T) {
	spy := newObserveSpy(t, http.StatusNoContent, ``)
	_, _, err := execRootStdin(t, "", observeArgs(spy.srv.URL, "governance", "rbac", "filters", "rm", "f-1")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("unconfirmed rm: err = %v (exit %d), want exit %d", err, exitcode.From(err), exitcode.Usage)
	}
	if spy.count() != 0 {
		t.Fatalf("unconfirmed rm sent %d request(s)", spy.count())
	}

	out, stderr, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "rbac", "filters", "rm", "f-1", "--yes")...)
	if err != nil {
		t.Fatalf("filters rm --yes: %v %s", err, stderr)
	}
	if got := spy.last(t); got.method != http.MethodDelete || got.path != filtersPath+"/f-1" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if !strings.Contains(out, "f-1") {
		t.Errorf("output does not name the removed filter:\n%s", out)
	}
}

// An empty list means every node inherits everything, so an answer that is not a list must
// not read as one, and a set or get that returns no record must not print an empty one.
func TestGovernanceRBACFiltersRefuseABodyThatIsNotAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		args []string
		body string
	}{
		{[]string{"ls"}, ``},
		{[]string{"ls"}, `{}`},
		{[]string{"ls"}, `{"items":null}`},
		{[]string{"get", "f-1"}, `{}`},
		{[]string{"set", "workspace", "payments", "--class", "session"}, ``},
	} {
		spy := newObserveSpy(t, http.StatusOK, tc.body)
		args := append([]string{"governance", "rbac", "filters"}, tc.args...)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, args...)...)
		if err == nil || exitcode.From(err) != exitcode.Server {
			t.Errorf("%v with body %q: err = %v (exit %d), want exit %d; out %q",
				tc.args, tc.body, err, exitcode.From(err), exitcode.Server, out)
		}
	}
}

func TestGovernanceRBACFiltersLsSaysWhenNoneIsSet(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"items":[]}`)
	out, stderr, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "rbac", "filters", "ls")...)
	if err != nil {
		t.Fatalf("filters ls: %v %s", err, stderr)
	}
	if strings.TrimSpace(out) != "no inheritance filter" {
		t.Errorf("output = %q, want only %q", out, "no inheritance filter")
	}
}
