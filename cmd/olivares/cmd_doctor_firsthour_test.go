// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestDoctorFirstHourChecksDoNotFailAHealthyInstall(t *testing.T) {
	o, deps := doctorFixture(t)
	deps.lookPath = func(string) (string, error) { return "", errNotFound{} }
	deps.getenv = func(string) string { return "" }

	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK || report.Overall != "healthy" {
		t.Fatalf("doctor = code %d overall %q checks=%+v", code, report.Overall, report.Checks)
	}
	agent := doctorCheckByName(report, "first-hour-coding-agent")
	if agent.Status != "unknown" || agent.Required {
		t.Errorf("coding-agent = %+v, want optional unknown", agent)
	}
	// The engine always runs its hook PEP; no variable is needed (Root, refresh 03).
	pep := doctorCheckByName(report, "first-hour-hook-pep")
	if pep.Status != "pass" || pep.Required {
		t.Errorf("hook-pep = %+v, want optional pass", pep)
	}
	next := doctorCheckByName(report, "first-hour-next-step")
	if next.Status != "pass" {
		t.Errorf("next-step status = %q, want pass", next.Status)
	}
	if !strings.Contains(next.Detail, "olivares tool install claude") {
		t.Errorf("next-step does not name the install: %q", next.Detail)
	}
}

func TestDoctorFirstHourNextStepWhenAgentPresentAndPEPMissing(t *testing.T) {
	o, deps := doctorFixture(t)
	deps.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", errNotFound{}
	}
	deps.getenv = func(string) string { return "" }
	deps.toolSignIn = func(string) doctorToolState { return toolStateSignedOut }
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK {
		t.Fatalf("doctor code = %d, want 0; checks=%+v", code, report.Checks)
	}
	if got := doctorCheckByName(report, "first-hour-coding-agent"); got.Status != "pass" || got.Detail != "official CLI on PATH: claude" {
		t.Errorf("coding-agent = %+v", got)
	}
	next := doctorCheckByName(report, "first-hour-next-step")
	if !strings.Contains(next.Detail, "olivares tool login claude") || !strings.Contains(next.Detail, "start a session") {
		t.Errorf("next-step does not name sign-in and a session: %q", next.Detail)
	}
}

// TestDoctorNextStepFollowsTheToolsSignIn is HU 016: after the tool was installed and
// signed in, doctor still said "sign the tool in". The next step now follows the
// engine's answer for the tool the coding-agent row found, and the last line of the
// report runs the same command.
func TestDoctorNextStepFollowsTheToolsSignIn(t *testing.T) {
	_, deps := doctorFixture(t)
	claude := doctorCheck{Status: "pass", Detail: "official CLI on PATH: claude"}
	codex := doctorCheck{Status: "pass", Detail: "official CLI on PATH: codex"}
	for _, tc := range []struct {
		agent      doctorCheck
		state      doctorToolState
		want, next string
	}{
		{claude, toolStateSignedIn, "start a session in a folder: olivares session start <folder>", "olivares session start <folder>"},
		{codex, toolStateSignedIn, "start a session in a folder: olivares session start <folder> --tool codex",
			"olivares session start <folder> --tool codex"},
		{claude, toolStateSignedOut, "sign Claude Code in, then start a session: olivares tool login claude", "olivares tool login claude"},
		{codex, toolStateSignedOut, "sign Codex in, then start a session: olivares tool login codex", "olivares tool login codex"},
		{claude, toolStateNoLogin, "sign in to the engine, then sign the tool in: olivares login", "olivares login"},
		{claude, toolStateUnknown, "see whether Claude Code is signed in: olivares tool ls", "olivares tool ls"},
	} {
		state := tc.state
		deps.toolSignIn = func(string) doctorToolState { return state }
		got := doctorFirstHourNextStep(deps, tc.agent, doctorCheck{})
		if got.Detail != tc.want {
			t.Errorf("%s state=%d: next = %q, want %q", tc.agent.Detail, state, got.Detail, tc.want)
		}
		if footer := doctorNextCommand(doctorReport{Checks: []doctorCheck{got}}); footer != tc.next {
			t.Errorf("%s state=%d: last line = %q, want %q", tc.agent.Detail, state, footer, tc.next)
		}
	}
	if got := doctorFirstHourNextStep(deps, doctorCheck{Status: "unknown"}, doctorCheck{}); got.Detail != "install Claude Code: olivares tool install claude" {
		t.Errorf("no agent: %q", got.Detail)
	}
}

func TestDoctorFirstHourHookPEPSetNeverPrintsTheValue(t *testing.T) {
	o, deps := doctorFixture(t)
	secret := "http://127.0.0.1:8447/?token=never-print-this-pep"
	deps.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", errNotFound{}
	}
	deps.getenv = func(key string) string {
		if key == "OLIVARES_HOOK_PEP_URL" {
			return secret
		}
		return ""
	}
	deps.toolSignIn = func(string) doctorToolState { return toolStateSignedIn }
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK {
		t.Fatalf("doctor code = %d", code)
	}
	pep := doctorCheckByName(report, "first-hour-hook-pep")
	if pep.Status != "pass" || strings.Contains(pep.Detail, "OLIVARES_HOOK_PEP") {
		t.Errorf("hook-pep = %+v", pep)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte("never-print-this-pep")) {
		t.Fatalf("doctor JSON disclosed the hook PEP URL:\n%s", body)
	}
	next := doctorCheckByName(report, "first-hour-next-step")
	if !strings.Contains(next.Detail, "olivares session start") {
		t.Errorf("next-step does not name a session: %q", next.Detail)
	}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }

// TestDoctorCountsAToolInstalledByOlivares is HU-13: with Claude Code installed by
// Olivares under <data-dir>/tools and nothing on PATH, doctor said no coding agent
// was installed.
//
// The fixture is a release the launch's resolver accepts: since installedSessionProgram
// (FH abdf67b8, ARCH item 8) a managed install counts only when its retained signature
// verifies, so a bare executable file no longer stands for "installed by Olivares".
// verifiedManagedProgram is the resolver's own hermetic seam for that check.
func TestDoctorCountsAToolInstalledByOlivares(t *testing.T) {
	o, deps := doctorFixture(t)
	deps.lookPath = func(string) (string, error) { return "", errNotFound{} }
	deps.getenv = func(string) string { return "" }
	exe := filepath.Join(o.dataDir, "tools", "claude", "2.1.286-linux-x64", "bin", "claude")
	prev := verifiedManagedProgram
	t.Cleanup(func() { verifiedManagedProgram = prev })
	verifiedManagedProgram = func(_ *hostToolObserver, driver string) string {
		if driver == "claude" {
			return exe
		}
		return ""
	}
	if got := doctorFirstHourCodingAgent(deps, o.dataDir); got.Status != "pass" || got.Detail != "installed by Olivares: claude 2.1.286-linux-x64" {
		t.Fatalf("coding-agent = %+v", got)
	}
	// A release whose signature no longer verifies is not a tool sessions run.
	verifiedManagedProgram = func(*hostToolObserver, string) string { return "" }
	if got := doctorFirstHourCodingAgent(deps, o.dataDir); got.Status != "unknown" || got.Remediation != "olivares tool install claude" {
		t.Fatalf("a release that does not verify is not an installed tool: %+v", got)
	}
	// The pin a launch honors first is what doctor names first.
	deps.getenv = func(k string) string {
		if k == envSessionClaudeBin {
			return "/opt/claude"
		}
		return ""
	}
	if got := doctorFirstHourCodingAgent(deps, o.dataDir); got.Status != "pass" || got.Detail != "claude pinned by "+envSessionClaudeBin+": /opt/claude" {
		t.Fatalf("pinned: %+v", got)
	}
}

// TestDoctorHookPEPIsOneRowThatPassesByDefault: the hook PEP row passes on an engine
// that answers, with no configuration variable (the engine always runs its PEP). It
// fails only when this host's Claude Code managed settings block the hooks (the shared
// hook-policy check), and it never recommends a variable.
func TestDoctorHookPEPIsOneRowThatPassesByDefault(t *testing.T) {
	_, deps := doctorFixture(t)
	deps.goos = "linux"
	prev := checkClaudeHookHostPolicy
	t.Cleanup(func() { checkClaudeHookHostPolicy = prev })
	up, down := doctorCheck{Name: "readyz", Status: "pass"}, doctorCheck{Name: "readyz", Status: "fail"}

	checkClaudeHookHostPolicy = func() error { return nil }
	if got := doctorFirstHourHookPEP(deps, up); got.Status != "pass" || got.Required || got.Remediation != "" {
		t.Fatalf("default: %+v", got)
	}
	if got := doctorFirstHourHookPEP(deps, down); got.Status != "unknown" || !strings.Contains(got.Detail, "not answering") {
		t.Fatalf("engine down: %+v", got)
	}
	checkClaudeHookHostPolicy = func() error {
		return errors.New("this host's Claude Code managed settings block Olivares' hooks; ask the host administrator to enable the session hooks")
	}
	got := doctorFirstHourHookPEP(deps, up)
	if got.Status != "fail" || !strings.Contains(got.Detail, "block Olivares' hooks") ||
		!strings.Contains(got.Remediation, "/etc/claude-code/managed-settings.json") ||
		!strings.Contains(got.Remediation, "disableAllHooks") || !strings.Contains(got.Remediation, "allowManagedHooksOnly") {
		t.Fatalf("hooks blocked: %+v", got)
	}
	for _, c := range []doctorCheck{got, doctorFirstHourHookPEP(deps, down)} {
		if strings.Contains(c.Detail+c.Remediation, "OLIVARES_HOOK_PEP") {
			t.Fatalf("the row recommends a variable: %+v", c)
		}
	}

	// One row: the separate managed-hooks row is gone.
	o, deps := doctorFixture(t)
	checkClaudeHookHostPolicy = func() error { return nil }
	report, _, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Checks {
		if c.Name == "claude-managed-hooks" {
			t.Fatalf("doctor still reports a second hook row: %+v", c)
		}
	}
}
