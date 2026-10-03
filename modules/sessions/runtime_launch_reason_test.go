// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// HU-06: a failed launch must say why. A refusal this module wrote at a launch stage
// (the Codex sandbox check's, for example) was replaced by "<stage> failed; the session
// was not started"; it is now kept as written. The runner's own text stays hidden.
func TestLaunchFailureKeepsTheRefusalTheStageWrote(t *testing.T) {
	own := &runErr{502, "Codex's native sandbox is unavailable and this server cannot enforce session confinement"}
	for name, lerr := range map[string]error{"as returned": own, "wrapped": fmt.Errorf("stage: %w", own)} {
		if got := launchFailedErr("Codex sandbox check", lerr); got.msg != own.msg || got.status != own.status {
			t.Fatalf("%s: launch refusal = %d %q, want the stage's own %q", name, got.status, got.msg, own.msg)
		}
	}
	runner := errors.New("fork/exec /srv/secret-token-path: exec format error")
	if got := launchFailedErr("tool process launch", runner).msg; strings.Contains(got, "secret-token") ||
		got != "tool process launch failed; the session was not started" {
		t.Fatalf("runner text reached the refusal: %q", got)
	}
	notFound := fmt.Errorf("start: %w", exec.ErrNotFound)
	if got := launchFailedErr("Codex sandbox check", notFound).msg; !strings.HasPrefix(got, "Codex sandbox check: the tool is not installed") {
		t.Fatalf("missing program = %q, want the install sentence under the stage's name", got)
	}
}

// The same, through the real Codex sandbox probe: a host without the native sandbox
// and without OS confinement is refused with the probe's own sentence.
func TestCodexSandboxRefusalReachesTheLaunchReason(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native sandbox")
	}
	m, _, _, profile := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, profile, codexFixture{SandboxExit: 1})
	p := CreateRunParams{PermissionMode: "default", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}}
	spec := LaunchSpec{Program: os.Args[0], Dir: t.TempDir(), Env: []EnvVar{{Name: envCodexHome, Value: profile.ConfigHome}, {Name: envUserHome, Value: profile.UserHome}}}
	stage, err := m.prepareSessionLaunch(t.Context(), t.Context(), "fixture", "run", &p, &spec)
	if err == nil {
		t.Fatal("a host without the native sandbox or OS confinement launched Codex")
	}
	got := launchFailedErr(stage, err).msg
	if got != "Codex's native sandbox is unavailable and this server cannot enforce session confinement" {
		t.Fatalf("launch reason = %q, want the sandbox check's own sentence", got)
	}
}
