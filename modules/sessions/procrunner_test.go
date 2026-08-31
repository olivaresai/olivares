// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaude is a minimal stream-json `claude` stand-in: it prints an init line
// carrying a session id (the --resume value when given), echoes one assistant
// frame per stdin line, exits 0 on stdin EOF, and exits 0 on SIGTERM. It lets the
// REAL procRunner + bridge be exercised end-to-end with a real OS process.
const fakeClaudeScript = `#!/bin/sh
trap 'exit 0' TERM
SID="sess-real-1"
while [ $# -gt 0 ]; do
  case "$1" in
    --resume) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$SID"
while IFS= read -r line; do
  printf '{"type":"assistant","echo":true}\n'
done
exit 0
`

func writeFakeClaude(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(path, []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake claude: %v", err)
	}
	return path
}

func TestProcRunner_StreamSendStop(t *testing.T) {
	t.Parallel()

	script := writeFakeClaude(t)
	r := NewProcRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := r.Launch(ctx, LaunchSpec{
		Program: script, Args: []string{"--permission-mode", "default"},
		Dir: t.TempDir(), WaitDelay: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	// First frame is the init line carrying the session id.
	init := recvFrame(t, proc, 2*time.Second)
	if !strings.Contains(string(init.Data), `"session_id":"sess-real-1"`) {
		t.Fatalf("first frame not an init: %s", init.Data)
	}
	// Send a line → the fake echoes one assistant frame.
	if err := proc.Send(ctx, []byte(`{"type":"user"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	echo := recvFrame(t, proc, 2*time.Second)
	if !strings.Contains(string(echo.Data), `"echo":true`) {
		t.Fatalf("expected an echo frame, got: %s", echo.Data)
	}
	// Stop → SIGTERM-trapped clean exit 0.
	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	exit, _ := proc.Wait()
	if exit != 0 {
		t.Fatalf("exit = %d want 0", exit)
	}
}

func TestProcRunner_DoesNotRetainStartedChildEnvironment(t *testing.T) {
	t.Parallel()

	script := writeFakeClaude(t)
	r := NewProcRunner()
	proc, err := r.Launch(context.Background(), LaunchSpec{
		Program: script, Isolation: IsolationNative,
		Env: []EnvVar{{Name: "OLIVARES_WORK_TOKEN", Value: "show-once-bearer"}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	native, ok := proc.(*procProcess)
	if !ok {
		t.Fatalf("process type = %T, want *procProcess", proc)
	}
	if native.cmd.Env != nil {
		t.Fatalf("started process handle retained %d raw environment entries", len(native.cmd.Env))
	}
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestProcRunner_RejectsInvalidOrDuplicateExplicitEnvironment(t *testing.T) {
	t.Parallel()

	r := NewProcRunner()
	for _, env := range [][]EnvVar{
		{{Name: "BAD=NAME", Value: "x"}},
		{{Name: "OLIVARES_WORK_TOKEN", Value: "one"}, {Name: "OLIVARES_WORK_TOKEN", Value: "two"}},
		{{Name: "SAFE_NAME", Value: "bad\x00value"}},
	} {
		if _, err := r.Launch(context.Background(), LaunchSpec{
			Program: writeFakeClaude(t), Isolation: IsolationNative, Env: env,
		}); err == nil {
			t.Fatalf("invalid explicit environment launched: %#v", env)
		}
	}
}

func TestProcRunner_EnvIsAllowlisted(t *testing.T) {
	// The child env is an ALLOWLIST: control-plane secrets (OLIVARES_*) and ambient
	// inference config (ANTHROPIC_*/CLAUDE_CODE_*) must NEVER reach the launched
	// claude; only the minimal base + explicitly allowlisted names + the spec env do.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-should-not-leak")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("OLIVARES_AUDIT_SIGNING_KEY", "ed25519-root-of-trust-must-not-leak")
	t.Setenv("MY_PROJECT_FLAG", "ok-to-forward")

	env := sanitizedEnv(
		[]string{"MY_PROJECT_FLAG", "OLIVARES_AUDIT_SIGNING_KEY", "ANTHROPIC_API_KEY"},
		[]EnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "tok"}},
	)

	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") ||
			strings.HasPrefix(kv, "CLAUDE_CODE_USE_BEDROCK=") ||
			strings.HasPrefix(kv, "OLIVARES_") {
			t.Fatalf("secret/ambient env leaked into child: %s", kv)
		}
	}
	if !contains(env, "ANTHROPIC_AUTH_TOKEN=tok") {
		t.Fatalf("explicit governed token not present in child env")
	}
	if !contains(env, "MY_PROJECT_FLAG=ok-to-forward") {
		t.Fatalf("operator-allowlisted var was not forwarded")
	}
}

func recvFrame(t *testing.T, proc Process, d time.Duration) OutputFrame {
	t.Helper()
	select {
	case f, ok := <-proc.Output():
		if !ok {
			t.Fatalf("output channel closed before a frame arrived")
		}
		return f
	case <-time.After(d):
		t.Fatalf("timed out waiting for an output frame")
		return OutputFrame{}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestRuntime_E2E_RealProcess drives the FULL governed lifecycle through the real
// native runner launching the fake `claude`: create → capture session id → input
// → bridged output in the ring → stop → resume → stop → cleanup → delete.
func TestRuntime_E2E_RealProcess(t *testing.T) {
	t.Parallel()

	script := writeFakeClaude(t)
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(NewProcRunner()), WithProgram(script),
		WithCredentialSource(staticCred()), WithStopWaitDelay(2*time.Second))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	ref := dto.RunRef
	if dto.State != stateRunning {
		t.Fatalf("state after create = %s want running", dto.State)
	}

	// The real init line is bridged → the session id is captured for resume.
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, ref)
		return d.ClaudeSessionID == "sess-real-1"
	})

	// Send input; the fake echoes a frame that lands in the ring (attach source).
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatalf("no live handle for a running session")
	}
	if err := m.sendInput(ctx, tenant, ref, []byte(`{"type":"user"}`)); err != nil {
		t.Fatalf("sendInput: %v", err)
	}
	waitFor(t, "bridged echo frame in ring", func() bool {
		return len(lr.ring.readFrom(0).frames) >= 2 // init + echo
	})

	// Stop → terminal state stopped (resumable).
	sd, err := m.stopRun(ctx, tenant, ref, "user:u1", "user")
	if err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if sd.State != stateStopped {
		t.Fatalf("state after stop = %s want stopped", sd.State)
	}
	assertSubsequence(t, eventNames(listRunEvents(t, st, tenant, ref)), "created", "launched", "stopped")

	// Resume relaunches the real process against --resume sess-real-1.
	rd, err := m.resumeRun(ctx, tenant, ref, "user:u1", "user", "")
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if rd.State != stateRunning {
		t.Fatalf("state after resume = %s want running", rd.State)
	}
	waitFor(t, "resumed session id", func() bool {
		d, _ := m.getRun(ctx, tenant, ref)
		return d.ClaudeSessionID == "sess-real-1"
	})

	if _, err := m.stopRun(ctx, tenant, ref, "user:u1", "user"); err != nil {
		t.Fatalf("stop after resume: %v", err)
	}
	if _, err := m.cleanupRun(ctx, tenant, ref, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}
	if err := m.deleteRun(ctx, tenant, ref); err != nil {
		t.Fatalf("deleteRun: %v", err)
	}
}

// TestProcRunner_RejectsNonNativeIsolation proves the native runner REFUSES a
// container/sandbox launch rather than silently running `claude` unisolated while the
// row reports isolation=container. Only native is wired this release; the
// container Runner is a documented follow-up.
func TestProcRunner_RejectsNonNativeIsolation(t *testing.T) {
	t.Parallel()

	for _, iso := range []Isolation{IsolationContainer, IsolationSandbox} {
		if _, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Program: "true", Isolation: iso}); err == nil {
			t.Fatalf("isolation %q: native runner must refuse, got nil error", iso)
		}
	}
}
