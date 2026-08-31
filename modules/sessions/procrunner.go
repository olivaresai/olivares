// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// procRunner is the NATIVE host-process Runner — the v1 default (choice)
// and the path that is fully implemented and exercised end-to-end. It launches
// `claude` with bridged stdin/stdout, adapted from the verified connectors/mcp
// stdio transport pattern (the pattern is replicated here in AGPL code rather
// than importing the connector's unexported helpers). A container/sandbox runner
// is a drop-in alternative behind the same Runner seam.
type procRunner struct {
	// lineCap bounds a single output line so a pathological frame cannot exhaust
	// memory before the ring buffer's own bound applies.
	lineCap int
}

// NewProcRunner returns the native streaming runner. It is constructed in
// cmd/olivares (the only layer that wires concrete runtimes into a module).
func NewProcRunner() Runner { return &procRunner{lineCap: maxOutputLine} }

// maxOutputLine bounds one bridged output line (1 MiB) — a stream-json frame is
// far smaller; this guards against a runaway line, never a normal one.
const maxOutputLine = 1 << 20

// Launch spawns the process with explicit env, a dedicated process group, and
// stdin/stdout/stderr pipes, then starts the output pumps. The ctx is the
// runtime manager's PER-RUN background context (NOT a request context), so the
// process outlives the create request and is torn down only on stop/cleanup or
// module shutdown.
func (pr *procRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if strings.TrimSpace(spec.Program) == "" {
		return nil, errors.New("sessions: launch spec has no program")
	}
	if err := validateExplicitEnv(spec.Env); err != nil {
		return nil, err
	}
	// The native runner runs `claude` as a plain host child — it CANNOT honor a
	// container/sandbox isolation request (it would silently run UNISOLATED while the
	// row reports isolation=container). Refuse, deny-closed: a container/sandbox launch
	// is the job of the container Runner (a documented follow-up), not this one.
	if spec.Isolation == IsolationContainer || spec.Isolation == IsolationSandbox {
		return nil, fmt.Errorf("sessions: native runner cannot honor isolation %q — only native is wired this release (the container/sandbox runner is a documented follow-up); relaunch with isolation=native", spec.Isolation)
	}
	cmd := exec.CommandContext(ctx, spec.Program, spec.Args...) // #nosec G204 -- spec.Program/Args are operator-configured session runtime; env is sanitized and container/sandbox isolation is refused deny-closed
	cmd.Dir = spec.Dir
	cmd.Env = sanitizedEnv(spec.EnvAllow, spec.Env)
	if spec.WaitDelay > 0 {
		cmd.WaitDelay = spec.WaitDelay
	}
	// Own process group so a graceful/hard stop reaches grandchildren that would
	// otherwise hold the stdout pipe open and wedge teardown.
	configureProcGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sessions: start %q: %w", spec.Program, err)
	}
	// exec has copied the environment into the started child. Do not retain the
	// raw slice (which includes short-lived inference/work bearers) for the
	// process lifetime and closed-handle retention window.
	cmd.Env = nil

	p := &procProcess{
		cmd:       cmd,
		stdin:     stdin,
		out:       make(chan OutputFrame, 256),
		waitDone:  make(chan struct{}),
		waitDelay: spec.WaitDelay,
	}

	// Two pumps (stdout, stderr); a coordinator waits for both to drain (EOF on
	// process exit) and ONLY THEN calls cmd.Wait (the pipe ordering exec requires),
	// stores the result, and closes the output channel.
	var pumps sync.WaitGroup
	pumps.Add(2)
	go pr.pump(&pumps, p.out, stdout, streamStdout)
	go pr.pump(&pumps, p.out, stderr, streamStderr)
	go func() {
		pumps.Wait()
		err := cmd.Wait()
		p.exit, p.waitErr = exitCodeOf(err)
		close(p.out)
		close(p.waitDone)
	}()
	return p, nil
}

func validateExplicitEnv(env []EnvVar) error {
	if len(env) > 128 {
		return errors.New("sessions: too many explicit environment values")
	}
	seen := make(map[string]bool, len(env))
	for _, item := range env {
		if !validEnvName(item.Name) || seen[item.Name] || strings.ContainsRune(item.Value, '\x00') ||
			len(item.Value) > 64*1024 {
			return errors.New("sessions: invalid or duplicate explicit environment value")
		}
		seen[item.Name] = true
	}
	return nil
}

// pump reads newline-delimited frames from r and forwards them, dropping a
// trailing CR/LF and bounding a single line. It never blocks the process: the
// channel is buffered and the consumer (the ring) drains it promptly.
func (pr *procRunner) pump(wg *sync.WaitGroup, out chan<- OutputFrame, r io.Reader, stream string) {
	defer wg.Done()
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readBoundedLine(br, pr.lineCap)
		if len(line) > 0 {
			out <- OutputFrame{Stream: stream, Data: line}
		}
		if err != nil {
			return // EOF (process exited) or a read error; the pipe is done
		}
	}
}

// procProcess is a live native process and its bridged streams.
type procProcess struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	out       chan OutputFrame
	waitDone  chan struct{}
	waitDelay time.Duration

	exit    int
	waitErr error

	mu        sync.Mutex
	stdinDone bool
}

// Send writes one NDJSON line (plus a newline) to the process's stdin.
func (p *procProcess) Send(_ context.Context, line []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdinDone {
		return errors.New("sessions: process stdin is closed")
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("sessions: write stdin: %w", err)
	}
	return nil
}

// Output returns the channel of output frames (closed on exit).
func (p *procProcess) Output() <-chan OutputFrame { return p.out }

// Wait blocks until the process exits and returns its exit code.
func (p *procProcess) Wait() (int, error) {
	<-p.waitDone
	return p.exit, p.waitErr
}

// Stop closes stdin, signals the process group to terminate, and escalates to a
// hard kill after WaitDelay if it has not exited.
func (p *procProcess) Stop(_ context.Context) error {
	p.closeStdin()
	_ = procGroupTerminate(p.cmd) // SIGTERM the group: let claude flush its transcript
	delay := p.waitDelay
	if delay <= 0 {
		delay = defaultWaitDelay
	}
	select {
	case <-p.waitDone:
		return nil
	case <-time.After(delay):
		_ = procGroupKill(p.cmd) // escalate to SIGKILL
		<-p.waitDone
		return nil
	}
}

// PID is the process id (0 if not started).
func (p *procProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *procProcess) closeStdin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stdinDone {
		_ = p.stdin.Close()
		p.stdinDone = true
	}
}

// defaultWaitDelay bounds a graceful stop before SIGKILL when no spec delay is set.
const defaultWaitDelay = 5 * time.Second

// readBoundedLine reads up to and including a newline, capping the line at cap
// bytes (a longer line is truncated and the rest of it is discarded up to the
// newline). It returns the line WITHOUT the trailing CR/LF.
func readBoundedLine(br *bufio.Reader, cap int) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if len(line) > cap {
		line = line[:cap]
	}
	return trimCRLF(line), err
}

// trimCRLF drops a trailing CR/LF from a line.
func trimCRLF(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// exitCodeOf maps a cmd.Wait error to an exit code. A non-zero exit returns the
// code with a nil error (an expected outcome the caller branches on); a true
// execution failure returns -1 with the error.
func exitCodeOf(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// baseEnvAllow is the minimal, non-sensitive host environment the launched
// `claude` needs: PATH (to find node/claude), HOME (its ~/.claude config +
// transcripts), and locale/term/tmp basics. Nothing secret is in this set.
var baseEnvAllow = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "TZ", "USER", "SHELL",
}

// sanitizedEnv builds the child environment as an ALLOWLIST: ONLY the minimal
// safe base (baseEnvAllow) plus the operator-named `allow` variables are inherited
// from the host; EVERYTHING else — every OLIVARES_* signing key / KMS token the
// control-plane process holds — is withheld (minimal-data, docs/SECURITY-HARDENING.md: a denylist
// would leak the whole secret set to an agent running under bypassPermissions).
// The explicit spec env (the governed ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL) is
// appended last. ANTHROPIC_*/CLAUDE_CODE_* host vars are dropped even if allowlisted
// (a static key/cloud-provider var would shadow the minted WIF token).
func sanitizedEnv(allow []string, extra []EnvVar) []string {
	allowed := make(map[string]bool, len(baseEnvAllow)+len(allow))
	for _, n := range baseEnvAllow {
		allowed[n] = true
	}
	for _, n := range allow {
		if n = strings.TrimSpace(n); validEnvName(n) && !forbiddenInheritedEnvName(n) {
			allowed[n] = true
		}
	}
	explicitNames := make(map[string]bool, len(extra))
	for _, e := range extra {
		if validEnvName(e.Name) {
			explicitNames[e.Name] = true
		}
	}
	out := make([]string, 0, len(allowed)+len(extra))
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if !allowed[name] || forbiddenInheritedEnvName(name) || explicitNames[name] {
			continue // withhold everything not explicitly allowed (incl. all OLIVARES_*)
		}
		out = append(out, kv)
	}
	seenExplicit := make(map[string]bool, len(extra))
	for _, e := range extra {
		if !validEnvName(e.Name) || seenExplicit[e.Name] {
			continue
		}
		seenExplicit[e.Name] = true
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' ||
			(i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func forbiddenInheritedEnvName(name string) bool {
	return strings.HasPrefix(name, "OLIVARES_") ||
		strings.HasPrefix(name, "ANTHROPIC_") ||
		strings.HasPrefix(name, "CLAUDE_CODE_") ||
		name == "DISABLE_AUTOUPDATER"
}
