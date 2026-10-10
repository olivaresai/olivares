// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"strings"
	"testing"
	"time"
)

// A tool that exits as it starts leaves its reason on the run (HU-06): Claude
// Code's error result first, else its last stderr line; one line, bounded, and
// never a credential.
func TestExitCause_SaysWhatTheToolSaidLast(t *testing.T) {
	t.Parallel()
	at := time.Unix(0, 0)
	ring := func(frames ...[2]string) *outputRing {
		r := newOutputRing(0, 0)
		for _, f := range frames {
			r.append(f[0], []byte(f[1]), at)
		}
		return r
	}

	if got := exitCause(ring(
		[2]string{streamStdout, `{"type":"system","subtype":"init"}`},
		[2]string{streamStderr, "warning: something minor"},
		[2]string{streamStdout, `{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}`},
	)); got != "Invalid API key · Please run /login" {
		t.Fatalf("result error = %q", got)
	}
	if got := exitCause(ring(
		[2]string{streamStderr, "Error: unknown option '--frobnicate'\n\n"},
	)); got != "Error: unknown option '--frobnicate'" {
		t.Fatalf("stderr line = %q", got)
	}
	if got := exitCause(ring([2]string{streamStdout, `{"type":"result","is_error":false,"result":"ok"}`})); got != "" {
		t.Fatalf("a successful result is not a cause: %q", got)
	}
	secret := "sk-ant-api03-" + strings.Repeat("x", 40)
	if got := exitCause(ring([2]string{streamStderr, "request failed with key " + secret})); strings.Contains(got, "sk-ant") || strings.Contains(got, strings.Repeat("x", 20)) {
		t.Fatalf("a credential reached the cause: %q", got)
	}
	if got := exitCause(ring([2]string{streamStderr, strings.Repeat("word ", 200)})); len(got) > exitCauseMax+len("…") {
		t.Fatalf("cause is %d bytes, want at most %d", len(got), exitCauseMax)
	}
	if exitCause(nil) != "" {
		t.Fatal("no ring, no cause")
	}
}

// CLX on 08b: a Claude Code child that exited "Not logged in · Please run /login"
// recorded the vendor's own step. The reason names the product's step; the vendor's
// words stay as the detail. Anything that is not a sign-in failure is left alone.
func TestProductCause_NamesTheProductsStepForAToolThatIsNotSignedIn(t *testing.T) {
	for _, tc := range []struct{ driver, cause, want string }{
		{"claude", "Not logged in · Please run /login", "Claude Code is not signed in for this session: sign it in under AI tools (olivares tool login claude), or use an API key from Providers. Claude Code said: Not logged in · Please run /login"},
		{"codex", "Not logged in. Run codex login", "Codex is not signed in for this session: sign it in under AI tools (olivares tool login codex)"},
		{"grok", "error: unauthenticated", "Grok Build is not signed in for this session: sign it in under AI tools (olivares tool login grok)"},
		{"opencode", "No provider configured", "OpenCode has no credential it can use: add a key or a local model (Ollama) in Providers"},
	} {
		if got := productCause(tc.driver, tc.cause); !strings.HasPrefix(got, tc.want) || !strings.HasSuffix(got, tc.cause) {
			t.Errorf("%s %q -> %q, want it to start with %q and keep the vendor's words", tc.driver, tc.cause, got, tc.want)
		}
	}
	for _, other := range []struct{ driver, cause string }{
		{"claude", "Error: ENOSPC: no space left on device"},
		{"codex", "panic: index out of range"},
		{"someagent", "Not logged in"},
	} {
		if got := productCause(other.driver, other.cause); got != other.cause {
			t.Errorf("%s %q was rewritten to %q", other.driver, other.cause, got)
		}
	}
}

// A Node launcher that dies prints its error with the stack and the error's
// properties after it, or Node's version line: its last line ("}") says nothing.
// The cause is the error line that heads the stack (#499: the run said "exit 1: }"
// for Codex's "spawn ... EACCES"). The runner records stderr one line per frame
// without its newline (pumpDiagnostics); a frame that holds several lines reads
// the same.
func TestExitCause_NamesTheErrorAboveAStackTrace(t *testing.T) {
	t.Parallel()
	at := time.Unix(0, 0)
	ring := func(text string, perLine bool) *outputRing {
		r := newOutputRing(0, 0)
		if !perLine {
			r.append(streamStderr, []byte(text), at)
			return r
		}
		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				r.append(streamStderr, []byte(line), at)
			}
		}
		return r
	}
	spawn := "Error: spawn /home/u/tools/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex EACCES"
	for name, tc := range map[string]struct{ text, want string }{
		"error properties": {spawn + `
    at ChildProcess._handle.onexit (node:internal/child_process:285:19)
    at onErrorNT (node:internal/child_process:483:16)
    at process.processTicksAndRejections (node:internal/process/task_queues:90:21) {
  errno: -13,
  code: 'EACCES',
  syscall: 'spawn /home/u/tools/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex',
  path: '/home/u/tools/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex',
  spawnargs: [ 'app-server' ]
}`, clipCause(spawn)},
		"uncaught with version line": {"/x/cli.js:3\n    throw new Error('boom');\n    ^\n\nError: boom\n    at Object.<anonymous> (/x/cli.js:3:11)\n    at node:internal/main/run_main_module:36:49\n\nNode.js v22.11.0", "Error: boom"},
		"java frames last":           {"Exception in thread \"main\" java.lang.IllegalStateException: no config\n\tat com.example.Main.main(Main.java:12)", `Exception in thread "main" java.lang.IllegalStateException: no config`},
		"sentence after a trace":     {"Error: retrying\n    at retry (/x/a.js:1:1)\nfatal: no provider reachable", "fatal: no provider reachable"},
		"last of several lines":      {"warning: slow start\nerror: cannot open the session folder", "error: cannot open the session folder"},
		"prose that starts with at":  {"at least one provider is required", "at least one provider is required"},
		"prose shaped like a frame":  {"at startup (see the docs)", "at startup (see the docs)"},
	} {
		for _, perLine := range []bool{true, false} {
			if got := exitCause(ring(tc.text, perLine)); got != tc.want {
				t.Errorf("%s (one frame per line: %v): cause = %q, want %q", name, perLine, got, tc.want)
			}
		}
	}
}
