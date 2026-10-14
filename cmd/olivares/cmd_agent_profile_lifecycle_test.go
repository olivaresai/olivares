// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"strings"
	"testing"
)

// The `agent profile` plane had create/get/ls and NOTHING else (measured
// 2026-09-18): a profile created with the wrong authorization or
// the wrong label was unrepairable from the CLI, and could not be retired either.
// These tests drive the real Cobra tree against a controlled listener and pin what
// each verb sends — which is the whole contract of a thin HTTP client.

const profileJSON = `{"profile_ref":"ppf-1","driver":"claude","environment_ref":"env-1",` +
	`"display_name":"Claude (work)","state":"active","local_environment":true,"operable":true,` +
	`"auth_source":"provider_account_home","session_tools":["Grep","Read"],"session_tools_declared":true,` +
	`"session_permission_mode":"plan"}`

func profileArgs(server string, rest ...string) []string {
	base := []string{
		"agent", "profile",
	}
	base = append(base, rest...)
	return append(base, "--server", server, "--token", "test-token", "--tenant", "tenant-a")
}

func TestAgentProfileUpdateSendsOnlyWhatTheOperatorNamed(t *testing.T) {
	p := newCreateProbe(t, http.StatusOK, profileJSON)
	t.Setenv("HOME", t.TempDir())

	out, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1", "--tools", "Read,Grep")...)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if p.lastMethod() != "PATCH" {
		t.Fatalf("method=%q, want PATCH", p.lastMethod())
	}
	if p.lastPath() != "/v1/m/sessions/provider-profiles/ppf-1" {
		t.Fatalf("path=%q", p.lastPath())
	}
	got := decodeCreateBody(t, p.lastBody())
	tools, ok := got["session_tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("session_tools=%v", got["session_tools"])
	}
	// A PATCH that sent every flag would WITHDRAW authorizations nobody touched.
	for _, unexpected := range []string{"display_name", "state", "auth_source", "provider_record_ref", "session_permission_mode"} {
		if _, present := got[unexpected]; present {
			t.Fatalf("update sent %q, which the operator did not name: %v", unexpected, got)
		}
	}
	if !strings.Contains(out, "Grep, Read") {
		t.Fatalf("the printed record must show the declared surface, got %q", out)
	}
}

func TestAgentProfileUpdateDeclaresNoToolsOutLoud(t *testing.T) {
	p := newCreateProbe(t, http.StatusOK, profileJSON)
	t.Setenv("HOME", t.TempDir())

	if _, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1", "--tools", "")...); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := decodeCreateBody(t, p.lastBody())
	raw, present := got["session_tools"]
	if !present {
		t.Fatalf("declaring NO tools must still send the declaration: %v", got)
	}
	if list, ok := raw.([]any); !ok || len(list) != 0 {
		t.Fatalf(`--tools "" must send an EMPTY list (declared none), got %v`, raw)
	}
}

func TestAgentProfileUpdateRefusesToRetireAndToChangeNothing(t *testing.T) {
	p := newCreateProbe(t, http.StatusOK, profileJSON)
	t.Setenv("HOME", t.TempDir())

	if _, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1", "--state", "retired")...); err == nil {
		t.Fatal("update accepted an irreversible retirement")
	}
	if _, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1")...); err == nil {
		t.Fatal("update with nothing to change was accepted")
	}
	if _, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1",
		"--provider", "prv-1", "--unbind-provider")...); err == nil {
		t.Fatal("update accepted two opposite requests in one call")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("a refused update reached the server %d times", p.calls.Load())
	}
}

func TestAgentProfileRemoveRetiresOnlyWithConsent(t *testing.T) {
	p := newCreateProbe(t, http.StatusOK, profileJSON)
	t.Setenv("HOME", t.TempDir())

	if _, _, err := execRoot(t, profileArgs(p.URL, "rm", "ppf-1")...); err == nil {
		t.Fatal("rm retired a profile without --yes")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("a refused rm reached the server %d times", p.calls.Load())
	}
	if _, _, err := execRoot(t, profileArgs(p.URL, "rm", "ppf-1", "--yes")...); err != nil {
		t.Fatalf("rm --yes: %v", err)
	}
	if p.lastMethod() != "POST" || p.lastPath() != "/v1/m/sessions/provider-profiles/ppf-1/retire" {
		t.Fatalf("rm called %s %s", p.lastMethod(), p.lastPath())
	}
}

// TestAgentProfileGetNamesAnUndeclaredToolPolicy: the operator must be able to
// SEE that nothing narrows the tool's default set on this profile.
func TestAgentProfileGetNamesAnUndeclaredToolPolicy(t *testing.T) {
	p := newCreateProbe(t, http.StatusOK,
		`{"profile_ref":"ppf-2","driver":"claude","state":"active","session_tools_declared":false}`)
	t.Setenv("HOME", t.TempDir())

	out, _, err := execRoot(t, profileArgs(p.URL, "get", "ppf-2")...)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(out, "not declared") || !strings.Contains(out, "the tool's default set") {
		t.Fatalf("an undeclared policy must read as the default set, got %q", out)
	}
}

// dev#1517: the CLI declares a profile's programs and npm registries, shows them,
// and withdraws them with an empty value; a malformed registry never leaves.
func TestAgentProfileUpdateDeclaresProgramsAndRegistries(t *testing.T) {
	withPackages := strings.TrimSuffix(profileJSON, "}") + `,"session_programs":["rea"],` +
		`"session_registries":[{"kind":"npm","url":"https://npm.acme.dev/","scope":"@acme","secret":"env/npm-acme"}]}`
	p := newCreateProbe(t, http.StatusOK, withPackages)
	t.Setenv("HOME", t.TempDir())

	out, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1", "--program", "rea",
		"--npm-registry", "url=https://npm.acme.dev/,scope=@acme,secret=env/npm-acme")...)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got := decodeCreateBody(t, p.lastBody())
	if programs, _ := got["session_programs"].([]any); len(programs) != 1 || programs[0] != "rea" {
		t.Fatalf("session_programs=%v", got["session_programs"])
	}
	regs, _ := got["session_registries"].([]any)
	reg, _ := regs[0].(map[string]any)
	if len(regs) != 1 || reg["kind"] != "npm" || reg["url"] != "https://npm.acme.dev/" || reg["scope"] != "@acme" || reg["secret"] != "env/npm-acme" {
		t.Fatalf("session_registries=%v", got["session_registries"])
	}
	if _, present := got["session_tools"]; present {
		t.Fatalf("update sent session_tools, which the operator did not name: %v", got)
	}
	if !strings.Contains(out, "rea") || !strings.Contains(out, "@acme https://npm.acme.dev/ (token from env/npm-acme)") {
		t.Fatalf("the printed record must show the programs and registries, got %q", out)
	}

	if _, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1", "--program", "", "--npm-registry", "")...); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	got = decodeCreateBody(t, p.lastBody())
	for _, key := range []string{"session_programs", "session_registries"} {
		if list, ok := got[key].([]any); !ok || len(list) != 0 {
			t.Fatalf(`an empty value must send an EMPTY %s list, got %v`, key, got[key])
		}
	}

	calls := p.calls.Load()
	for _, bad := range []string{"https://npm.acme.dev/", "url=https://npm.acme.dev/,secret=", "url=,url=https://npm.acme.dev/"} {
		if _, _, err := execRoot(t, profileArgs(p.URL, "update", "ppf-1", "--npm-registry", bad)...); err == nil {
			t.Fatalf("--npm-registry %q was accepted", bad)
		}
	}
	if p.calls.Load() != calls {
		t.Fatal("a malformed registry reached the server")
	}
}
