// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares redteam`. This family's whole subject is a consent
// gate, so every test here is about not weakening it — and about not making the
// SAFE direction harder than the dangerous one.

func TestRedteamVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"redteam", "catalog"}, "GET", "/v1/m/redteam/catalog"},
		{[]string{"redteam", "targets", "ls"}, "GET", "/v1/m/redteam/targets"},
		{[]string{"redteam", "targets", "get", "rt-1"}, "GET", "/v1/m/redteam/targets/rt-1"},
		{[]string{"redteam", "targets", "register", "--agent-ref", "a", "--name", "n"}, "POST", "/v1/m/redteam/targets"},
		{[]string{"redteam", "targets", "authorize", "rt-1", "--yes"}, "POST", "/v1/m/redteam/targets/rt-1/authorize"},
		{[]string{"redteam", "targets", "revoke", "rt-1"}, "POST", "/v1/m/redteam/targets/rt-1/authorize"},
		{[]string{"redteam", "runs", "ls"}, "GET", "/v1/m/redteam/runs"},
		{[]string{"redteam", "runs", "get", "run-1"}, "GET", "/v1/m/redteam/runs/run-1"},
		{[]string{"redteam", "runs", "results", "run-1"}, "GET", "/v1/m/redteam/runs/run-1/results"},
		{[]string{"redteam", "runs", "launch", "--target-ref", "rt-1"}, "POST", "/v1/m/redteam/runs"},
	} {
		t.Run(strings.Join(tc.argv, "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false,"probes":[],"total":0,"id":"rt-1"}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestRedteamConsentIsGatedInTheDangerousDirectionOnly. Granting consent to
// attack a live agent is the dual-use line; withdrawing it is the safe direction
// and must never be harder to reach.
func TestRedteamConsentIsGatedInTheDangerousDirectionOnly(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"rt-1","authorized":true}`))

	// DENY: authorize refuses unattended, and sends nothing.
	_, _, err := execRoot(t, lot3Args(srv.URL, "redteam", "targets", "authorize", "rt-1")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("authorize must refuse unattended, got %v", err)
	}
	if !strings.Contains(err.Error(), "adversarial red-team probing of target rt-1") {
		t.Errorf("the prompt must name what is being consented to, got: %v", err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d consent request(s) were sent without --yes", n)
	}

	// ALLOW: with --yes it goes through and says authorized=true.
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"redteam", "targets", "authorize", "rt-1", "--scope", "staging", "--yes")...); err != nil {
		t.Fatalf("consenting with --yes must succeed, got %v", err)
	}
	body := srv.lastBody()
	if !strings.Contains(body, `"authorized":true`) || !strings.Contains(body, "staging") {
		t.Fatalf("the consent body is wrong: %s", body)
	}

	// THE OTHER DIRECTION: revoke is not gated at all, and sends authorized=false.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "redteam", "targets", "revoke", "rt-1")...); err != nil {
		t.Fatalf("withdrawing consent must not need a ceremony, got %v", err)
	}
	if !strings.Contains(srv.lastBody(), `"authorized":false`) {
		t.Fatalf("revoke must send authorized=false, body was: %s", srv.lastBody())
	}
	// And revoke must NOT carry a scope it was never given: a scoped revoke would
	// narrow the withdrawal instead of ending it.
	if strings.Contains(srv.lastBody(), "scope") {
		t.Fatalf("revoke invented a scope: %s", srv.lastBody())
	}
}

// TestRedteamRegisterIsNotConsentAndSaysSo. Registration and authorization are two
// decisions, and an operator who thinks `register` armed the battery will not go
// and authorize it.
func TestRedteamRegisterIsNotConsentAndSaysSo(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"rt-1","agent_ref":"a","authorized":false,"status":"registered"}`))
	out, errOut, err := execRoot(t, lot3Args(srv.URL,
		"redteam", "targets", "register", "--agent-ref", "a", "--name", "n")...)
	if err != nil {
		t.Fatalf("registering must succeed, got %v", err)
	}
	if !strings.Contains(errOut, "NOT authorized") {
		t.Errorf("stderr must say registration is not consent, got:\n%s", errOut)
	}
	if !strings.Contains(out, "rt-1") {
		t.Errorf("stdout must carry the new target id, got:\n%s", out)
	}
}

// TestRedteamLaunchIsNotGatedButAnUnauthorizedTargetIsRefused: the everyday CI
// verb stays usable, and the control that matters is the engine's.
func TestRedteamLaunchIsNotGatedButAnUnauthorizedTargetIsRefused(t *testing.T) {
	// ALLOW: an authorized target runs, with no ceremony in the way.
	ok := newLot3Server(t, lot3OK(`{"id":"run-1","target_ref":"rt-1","status":"completed","score":0.9}`))
	if _, _, err := execRoot(t, lot3Args(ok.URL, "redteam", "runs", "launch", "--target-ref", "rt-1")...); err != nil {
		t.Fatalf("launching against an authorized target must succeed, got %v", err)
	}
	if n := ok.calls.Load(); n != 1 {
		t.Fatalf("launch made %d requests, want 1", n)
	}

	// DENY: the engine refuses an unauthorized target, and the code says so.
	denied := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"target is not authorized for red-teaming; authorize it first"}}`)
	})
	_, _, err := execRoot(t, lot3Args(denied.URL, "redteam", "runs", "launch", "--target-ref", "rt-2")...)
	if err == nil || exitcode.From(err) != exitcode.Auth {
		t.Fatalf("an unauthorized target must exit %d, got %v", exitcode.Auth, err)
	}
	if !strings.Contains(err.Error(), "authorize it first") {
		t.Errorf("the engine's remedy must survive, got: %v", err)
	}
}

// TestRedteamCatalogReportsItsSizeAndItsProbes. The catalog is not the standard
// list envelope — it carries `probes` plus coverage maps — so a renderer written
// for `items` would print "no probes" over a full battery.
func TestRedteamCatalogReportsItsSizeAndItsProbes(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"total":2,"families":{"injection":2},
		"probes":[{"id":"p1","family":"injection","severity":"high","owasp":"LLM01"},
		          {"id":"p2","family":"injection","severity":"medium","atlas":"AML.T0051"}]}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "redteam", "catalog")...)
	if err != nil {
		t.Fatalf("the catalog read must succeed, got %v", err)
	}
	for _, want := range []string{"2 probe", "p1", "p2", "LLM01", "AML.T0051"} {
		if !strings.Contains(out, want) {
			t.Errorf("the catalog output is missing %q:\n%s", want, out)
		}
	}

	// THE CONTROL: a build with no probes for a suite says so, rather than
	// printing an empty table that reads as a rendering bug.
	empty := newLot3Server(t, lot3OK(`{"total":0,"probes":[]}`))
	eout, _, eerr := execRoot(t, lot3Args(empty.URL, "redteam", "catalog", "--suite", "nope")...)
	if eerr != nil {
		t.Fatalf("an empty catalog must exit 0, got %v", eerr)
	}
	if !strings.Contains(eout, "no probes") {
		t.Errorf("an empty catalog must say so, got: %q", eout)
	}
}

// TestRedteamCatalogUnreadableBodyIsAServerFailure. The catalog reads its own
// envelope instead of going through renderAgentExecList, and that private path
// used to return the decode error BARE — exit 1, which a script cannot tell from
// a usage mistake of its own. Every other envelope reader in this lot classifies
// the identical condition as a server failure.
func TestRedteamCatalogUnreadableBodyIsAServerFailure(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`this is not json`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "redteam", "catalog")...)
	if err == nil {
		t.Fatal("a body that cannot be read must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Server {
		t.Fatalf("exit = %d, want %d (server) — the same code the other four envelope readers give",
			got, exitcode.Server)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("an unreadable answer must print nothing on stdout, got:\n%s", out)
	}

	// THE CONTROL: a readable catalog still exits 0 and renders. Without it,
	// "always exit 6" would satisfy the assertion above.
	ok := newLot3Server(t, lot3OK(`{"total":1,"probes":[{"id":"p1","family":"injection"}]}`))
	okOut, _, okErr := execRoot(t, lot3Args(ok.URL, "redteam", "catalog")...)
	if okErr != nil {
		t.Fatalf("a readable catalog must exit 0, got %v (code %d)", okErr, exitcode.From(okErr))
	}
	if !strings.Contains(okOut, "p1") {
		t.Errorf("the readable catalog did not render its probe:\n%s", okOut)
	}
}
