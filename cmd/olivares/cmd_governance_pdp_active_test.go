// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pdpActiveLine runs `governance pdp active` against a canned body and returns the ONE line the
// operator reads for grants. Asserting on the whole output would pass on a substring that also
// appears in the command's own help text ("...whether it is fully in force"), which is how a
// witness ends up proving that the help string exists.
func pdpActiveLine(t *testing.T, body, prefix string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/m/governance/pdp/active" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cmd := newGovernanceCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--server", srv.URL, "--token", "tok", "--tenant", "t1", "pdp", "active", "--engine", "cedar"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pdp active failed: %v (output: %s)", err, buf.String())
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	t.Fatalf("no %q line in output: %s", prefix, buf.String())
	return ""
}

// TestPdpActiveNeverClaimsGrantsInForceWithoutAnAppliedSnapshot pins the correction the
// the model contrast of 2026-08-22 asked for.
//
// `grants_expired` is only a MEASUREMENT when a snapshot is applied. The engine reports
// `deferred` when no set is loaded, `no_policy` when nothing is authored, and OPA reports
// `not_applicable`; in all of those the field is a zero value nobody computed. Rendering it as
// "in force" told the operator that positive grants were live when expiry had never been
// evaluated — a false assurance from the one command whose job is to answer that question.
func TestPdpActiveNeverClaimsGrantsInForceWithoutAnAppliedSnapshot(t *testing.T) {
	body := func(activation string, expired bool) string {
		return fmt.Sprintf(`{"engine":"cedar","live_activation":%q,"grants_expired":%t}`, activation, expired)
	}

	// FIRING DIRECTION: the four non-applied states must NOT claim the grants are in force.
	for _, st := range []string{"deferred", "no_policy", "not_applicable", "unavailable"} {
		got := pdpActiveLine(t, body(st, false), "grants:")
		if strings.Contains(got, "in force") {
			t.Errorf("activation %q renders grants as %q: the CLI asserts positive grants are live while expiry was never evaluated", st, got)
		}
		if !strings.HasPrefix(got, "n/a") {
			t.Errorf("activation %q renders grants as %q, want the n/a form that says expiry was not evaluated", st, got)
		}
	}

	// NOT-FIRING DIRECTION, and it is what gives the above its meaning: with an applied
	// snapshot the answer is still the plain one. A guard that returned n/a everywhere would
	// satisfy every assertion above and make the command useless.
	if got := pdpActiveLine(t, body(pdpActivationApplied, false), "grants:"); got != "in force" {
		t.Errorf("applied + not expired renders %q, want %q", got, "in force")
	}

	// A DANGEROUS STATE IS NEVER DOWNGRADED. If the engine says expired we say expired, whatever
	// the activation is — reporting n/a there would hide the one state this command exists for.
	for _, st := range []string{pdpActivationApplied, "deferred"} {
		got := pdpActiveLine(t, body(st, true), "grants:")
		if !strings.HasPrefix(got, "EXPIRED") {
			t.Errorf("activation %q with grants_expired=true renders %q, want the EXPIRED warning", st, got)
		}
	}
}

// TestPdpTestsForwardsTheRevisionItWasGivenOrRefusesIt is the query-forwarding witness the
// contrast said the sixteen verbs lacked, on the one flag where getting it wrong is silent:
// `--revision` used to be dropped unless `> 0`, so `--revision -1` asked for the NEWEST revision
// and the operator read results for a revision they never named.
func TestPdpTestsForwardsTheRevisionItWasGivenOrRefusesIt(t *testing.T) {
	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		var seen string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"available":false,"reason":"none stored"}`))
		}))
		defer srv.Close()
		cmd := newGovernanceCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(append([]string{"--server", srv.URL, "--token", "tok", "--tenant", "t1", "pdp", "tests", "--engine", "cedar"}, args...))
		err := cmd.Execute()
		return seen, err
	}

	// FORWARDED: an explicit, possible revision reaches the engine verbatim.
	if q, err := run(t); err != nil || strings.Contains(q, "revision=") {
		t.Errorf("no --revision sent query %q err %v; want the default request with no revision", q, err)
	}
	if q, err := run(t, "--revision", "3"); err != nil || !strings.Contains(q, "revision=3") {
		t.Errorf("--revision 3 sent query %q err %v; want revision=3 forwarded", q, err)
	}

	// REFUSED, and this is the half that was broken: an impossible revision must NOT silently
	// become "newest". Answering about a different revision than the one typed is the harm.
	for _, bad := range []string{"0", "-1"} {
		q, err := run(t, "--revision", bad)
		if err == nil {
			t.Errorf("--revision %s was accepted and sent query %q; it must be refused, not substituted", bad, q)
		}
		if strings.Contains(q, "revision=") {
			t.Errorf("--revision %s reached the engine as %q", bad, q)
		}
	}
}

// governanceLine runs one governance verb against a canned body and returns the line starting
// with prefix, or "" when there is none. Unlike a substring check on the whole output, the
// absence of a line is itself an answer here — it is the defect these two cases exist for.
func governanceLine(t *testing.T, path, body, prefix string, args ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	cmd := newGovernanceCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"--server", srv.URL, "--token", "tok", "--tenant", "t1"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v (output: %s)", err, buf.String())
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	return ""
}

// TestDetailViewsPrintTheFieldsTheyDecode covers the contrast's third finding. Both verbs decoded
// a field the engine populates and never rendered it, so a group WITH a description and one
// without, and an identity WITH a credential-age bound and one without, were identical on screen.
// A field that is decoded and not printed is worse than one that is not decoded: the code says it
// was read.
func TestDetailViewsPrintTheFieldsTheyDecode(t *testing.T) {
	const pgPath = "/v1/m/governance/rbac/permission-groups/g1"
	if got := governanceLine(t, pgPath, `{"name":"g1","description":"the on-call set"}`,
		"description:", "rbac", "permission-groups", "get", "g1"); got != "the on-call set" {
		t.Errorf("permission-groups get description = %q, want the decoded value", got)
	}
	// NOT-FIRING HALF: absent must still print a line, as "-", so absence is visible rather than
	// inferred from a missing row the eye skips.
	if got := governanceLine(t, pgPath, `{"name":"g1"}`,
		"description:", "rbac", "permission-groups", "get", "g1"); got != "-" {
		t.Errorf("permission-groups get with no description = %q, want %q", got, "-")
	}

	const nhiPath = "/v1/m/governance/nhi/svc-1"
	if got := governanceLine(t, nhiPath, `{"identity_ref":"svc-1","max_age_seconds":86400}`,
		"max age:", "nhi", "get", "svc-1"); got != "86400s" {
		t.Errorf("nhi get max age = %q, want the decoded bound", got)
	}
	// Zero is NOT a bound of zero: it means none was set, and rendering "0s" would assert a
	// policy nobody configured.
	if got := governanceLine(t, nhiPath, `{"identity_ref":"svc-1"}`,
		"max age:", "nhi", "get", "svc-1"); got != "-" {
		t.Errorf("nhi get with no bound = %q, want %q", got, "-")
	}
}
