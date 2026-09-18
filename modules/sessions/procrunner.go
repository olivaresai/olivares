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
	"sync/atomic"
	"time"
)

// procRunner is the NATIVE host-process Runner — the v1 default (choice)
// and the path that is fully implemented and exercised end-to-end. It launches
// `claude` with bridged stdin/stdout, adapted from the verified connectors/mcp
// stdio transport pattern (the pattern is replicated here in AGPL code rather
// than importing the connector's unexported helpers). A container/sandbox runner
// is a drop-in alternative behind the same Runner seam.
//
// When usePTY is set, stdin/stdout are a local Unix PTY in raw mode (stderr
// stays a pipe so the two streams stay distinct). That is the Community
// terminal path; it is not the Identity & Scale overlay listener.
type procRunner struct {
	// lineCap bounds a single output line so a pathological frame cannot exhaust
	// memory before the ring buffer's own bound applies.
	lineCap int
	usePTY  bool
}

// NewProcRunner returns the native streaming runner (stdio pipes). It is
// constructed in cmd/olivares (the only layer that wires concrete runtimes
// into a module).
func NewProcRunner() Runner { return &procRunner{lineCap: maxOutputLine} }

// NewPTYRunner returns the native runner that attaches a local PTY for
// stdin/stdout. Container/sandbox isolation is still refused.
func NewPTYRunner() Runner { return &procRunner{lineCap: maxOutputLine, usePTY: true} }

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
	if pr.usePTY {
		return pr.launchPTY(cmd, spec.WaitDelay)
	}
	return pr.launchPipes(cmd, spec.WaitDelay)
}

func (pr *procRunner) launchPipes(cmd *exec.Cmd, waitDelay time.Duration) (Process, error) {
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
		return nil, fmt.Errorf("sessions: start %q: %w", specProgram(cmd), err)
	}
	// exec has copied the environment into the started child. Do not retain the
	// raw slice (which includes short-lived inference/work bearers) for the
	// process lifetime and closed-handle retention window.
	cmd.Env = nil
	return pr.watch(cmd, stdin, stdout, stderr, waitDelay), nil
}

func specProgram(cmd *exec.Cmd) string {
	if cmd == nil || cmd.Path == "" {
		return ""
	}
	return cmd.Path
}

func (pr *procRunner) watch(cmd *exec.Cmd, stdin io.WriteCloser, stdout, stderr io.ReadCloser, waitDelay time.Duration) *procProcess {
	p := &procProcess{
		cmd:   cmd,
		stdin: stdin,
		// The PARENT read ends are retained because teardown may have to close
		// them: a descendant that left the process group keeps the write ends open,
		// the pumps never see EOF, and cmd.Wait is therefore never reached (Stop).
		stdout:    stdout,
		stderr:    stderr,
		out:       make(chan OutputFrame, 256),
		waitDone:  make(chan struct{}),
		abandon:   make(chan struct{}),
		waitDelay: waitDelay,
	}

	// Two pumps (stdout, stderr); a coordinator waits for both to drain (EOF on
	// process exit) and ONLY THEN calls cmd.Wait (the pipe ordering exec requires),
	// stores the result, and closes the output channel.
	var pumps sync.WaitGroup
	pumps.Add(2)
	go pr.pump(&pumps, p, stdout, streamStdout)
	go pr.pump(&pumps, p, stderr, streamStderr)
	go func() {
		pumps.Wait()
		err := cmd.Wait()
		p.exit, p.waitErr = exitCodeOf(err)
		close(p.out)
		close(p.waitDone)
	}()
	return p
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
// trailing CR/LF and bounding a single line. The channel is buffered and the
// consumer (the ring) drains it promptly; when it stops draining anyway, only a
// forced teardown ends the pump — see deliver.
func (pr *procRunner) pump(wg *sync.WaitGroup, p *procProcess, r io.Reader, stream string) {
	defer wg.Done()
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readBoundedLine(br, pr.lineCap)
		if len(line) > 0 {
			if !p.deliver(OutputFrame{Stream: stream, Data: line}) {
				return // a forced teardown gave up on delivery; it reports the loss
			}
		}
		if err != nil {
			return // EOF (process exited) or a read error; the pipe is done
		}
	}
}

// deliver hands one frame to the consumer and reports whether the pump may go on.
//
// The unbuffered attempt comes FIRST and it is not an optimisation: with both a
// free slot and a closed `abandon` ready, a single select would drop, at random,
// frames the consumer was perfectly able to take. Trying delivery on its own means
// a teardown can only ever abandon a frame that is genuinely stuck.
func (p *procProcess) deliver(frame OutputFrame) bool {
	select {
	case p.out <- frame:
		return true
	default:
	}
	select {
	case p.out <- frame:
		return true
	case <-p.abandon:
		p.abandonedFrames.Add(1)
		return false
	}
}

// procProcess is a live native process and its bridged streams.
type procProcess struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	out       chan OutputFrame
	waitDone  chan struct{}
	waitDelay time.Duration

	exit    int
	waitErr error

	// mu guards the stdin LIFECYCLE state and the lazily built write gate. It is
	// held for state decisions ONLY and NEVER across a write to the child.
	//
	// ⛔ THAT SEPARATION IS THE FIX. Send used to take this same mutex and hold
	// it across a blocking Write, so a child that stopped draining its stdin froze
	// the one lock Stop needs to close that stdin: the process could not be told to
	// go away by the only path that could have unblocked the writer.
	mu        sync.Mutex
	stdinDone bool
	// stdinBroken is set when a frame was left HALF WRITTEN. It is terminal: the
	// child is mid-line, so the next frame would be read as this one's tail.
	stdinBroken error
	// writeGate serialises frames (capacity 1). It is a channel and not a Mutex
	// because a waiting writer must be able to give up when ITS context ends — a
	// sync.Mutex has no bounded acquire.
	writeGate chan struct{}

	// abandon is closed by the LAST rung of a forced teardown, and it is the only
	// thing that can release a pump blocked DELIVERING a frame.
	//
	// ⛔ CLOSING THE PIPES IS NOT ENOUGH, and that gap kept a dead child alive on
	// the process table. A pump blocked in `p.out <- frame` is not reading a pipe,
	// so closing the read ends does not touch it; the coordinator waits for both
	// pumps before cmd.Wait, so the child is never reaped. The consumer that
	// blocks is not hypothetical: Module.bridge calls the governed recorder and
	// the store synchronously between receives, and the independent review
	// reproduced this with the REAL bridge and a paused recorder.
	abandon         chan struct{}
	abandonOnce     sync.Once
	abandonedFrames atomic.Int64
}

// ErrOutputAbandoned classifies a stop that DID reap the child but had to give up
// on output to get there. It is additive and fixed: the runtime wraps a Stop error
// in a credential-safe wrapper whose text is deliberately generic, and that wrapper
// preserves Unwrap — so this sentinel is what survives for errors.Is, while nothing
// provider-controlled travels with it.
var ErrOutputAbandoned = errors.New("sessions: native output was abandoned to complete a forced teardown")

// ErrChildNotReaped classifies the other outcome, and the distinction is the point:
// a stop that returns ErrOutputAbandoned has collected its child, a stop that
// returns this one has NOT and the caller must not treat the process as gone.
var ErrChildNotReaped = errors.New("sessions: the native child was not reaped")

// writeDeadliner is the bound the native stdin pipe ACTUALLY offers.
//
// ⛔ CONTEXT DOES NOT WRAP Write. io.Writer takes no context and no amount of
// select around it can retract a write that is already blocked in the kernel, so
// a Send that only reads ctx at the door is not bounded by it. exec.Cmd's
// StdinPipe hands back the write end of an os.Pipe (behind a close-once shim that
// promotes the file's methods), and an os.Pipe IS registered with the runtime
// poller: a write deadline aborts a blocked write and returns what it managed to
// write. That is the mechanism this file uses, and it is probed rather than
// assumed — a stdin that is not pollable answers ErrNoDeadline.
type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

// writeAborted is the past instant used to expire an in-flight write. Any time in
// the past works; a fixed one keeps the intent readable.
var writeAborted = time.Unix(1, 0)

// Send writes one NDJSON line (plus a newline) to the process's stdin, BOUNDED BY
// ITS CONTEXT.
//
// The bound is real, not decorative: a driver dispatch that says "30 seconds" is
// answered by an expiring write, never by a caller blocked on a child that has
// stopped reading. Three facts are kept separate and reported honestly, because a
// caller has to know what to record:
//
//   - NOT ATTEMPTED — an already-expired context, or a context that ends while
//     this frame waits behind another one. NO byte of it reached the child.
//   - NOTHING WRITTEN — the write was attempted and the child took zero bytes.
//     The stream is still clean and the next frame may use it.
//   - HALF WRITTEN — bytes crossed and the frame did not finish. The stream is
//     now unusable and stays that way: see stdinBroken.
func (p *procProcess) Send(ctx context.Context, line []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// An already-cancelled request sends NOTHING. Checked before the gate so the
	// answer cannot depend on who else happened to be writing.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sessions: stdin write not attempted: %w", err)
	}
	gate, err := p.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer func() { <-gate }()
	// ⛔ THE CANCELLATION IS RE-READ HERE, AND THE REASON IS A SCHEDULING FACT, NOT
	// A STYLE. When a frame queues behind another one, the gate and ctx.Done() can
	// become ready TOGETHER — the previous writer finishes at the moment the caller
	// gives up — and Go's select then picks either arm, both legally. Winning the
	// gate is not evidence that the caller still wants this frame: for a
	// cancellation-only context there is no absolute deadline on the pipe, and a
	// small frame is written and reported as a SUCCESS before the watchdog can run.
	// The independent review measured that: 17 of 32 controlled handoffs put
	// `cancelled-queued-frame` on the child's stdin and returned nil.
	//
	// So the decision is made once more, here, where "already cancelled" is a fact
	// and not a race: BEFORE any byte of this frame is attempted. A cancellation
	// that arrives AFTER the write has started is a different answer and keeps its
	// existing one — attempted, possibly partial, never retroactively unattempted.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sessions: stdin write not attempted: the request was cancelled while it waited for another frame: %w", err)
	}
	// Re-read the state AFTER the gate: a Stop may have closed stdin, or the
	// previous holder may have broken the stream, while this frame waited.
	if err := p.stdinWritable(); err != nil {
		return err
	}
	// One private buffer, written in ONE call. `append(line, '\n')` would write the
	// terminator into the CALLER's backing array when it has spare capacity, and
	// line+newline as two writes would let a concurrent frame land between them.
	frame := make([]byte, 0, len(line)+1)
	frame = append(frame, line...)
	frame = append(frame, '\n')

	n, werr := p.writeFrame(ctx, frame)
	if n == len(frame) && werr == nil {
		return nil
	}
	// The deadline or cancellation is the answer the caller asked for; the file's
	// own error is the mechanism that delivered it. Both travel, and the context
	// error is the one errors.Is can match.
	cause := werr
	_, hasDeadline := ctx.Deadline()
	switch {
	case ctx.Err() != nil:
		cause = fmt.Errorf("%w (%v)", ctx.Err(), werr)
	case hasDeadline && errors.Is(werr, os.ErrDeadlineExceeded):
		// ⛔ THE WRITE'S DEADLINE AND THE CONTEXT'S ARE TWO TIMERS FOR ONE INSTANT,
		// and the write's fires first as often as not — the poller wakes the writer
		// while context's own goroutine has yet to record the expiry. The write
		// expired on the deadline THIS context set, so the caller is told about the
		// context and not about a bare i/o timeout it cannot match on.
		cause = fmt.Errorf("%w (%v)", context.DeadlineExceeded, werr)
	}
	if n > 0 && n < len(frame) {
		// ⛔ A HALF FRAME IS ON THE CHILD'S STDIN. The child is parked mid-line, so
		// the next frame would be appended to this one and read as its tail: a
		// silently corrupted NDJSON stream that answers a request nobody sent. The
		// stream is retired here; the run is still stoppable, and Stop is the way out.
		p.poisonStdin(fmt.Errorf(
			"sessions: process stdin is unusable: a frame was left half written (%d of %d bytes) and the child's NDJSON stream cannot be continued", n, len(frame)))
		return fmt.Errorf("sessions: write stdin: %d of %d frame bytes were written before the write ended: %w", n, len(frame), cause)
	}
	if n == 0 {
		return fmt.Errorf("sessions: write stdin: no bytes were written: %w", cause)
	}
	return fmt.Errorf("sessions: write stdin: %w", cause)
}

// acquireWrite takes the write gate, giving up when the caller's context ends.
// Waiting for ANOTHER writer is exactly as bounded as writing: a blocked frame
// must not be able to hold a queue of callers past their own deadlines.
func (p *procProcess) acquireWrite(ctx context.Context) (chan struct{}, error) {
	p.mu.Lock()
	if p.writeGate == nil {
		p.writeGate = make(chan struct{}, 1)
	}
	gate := p.writeGate
	p.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return gate, nil // uncontended: never lose the race to an equally ready ctx
	default:
	}
	select {
	case gate <- struct{}{}:
		return gate, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("sessions: stdin write not attempted: another frame was still being written: %w", ctx.Err())
	}
}

// writeFrame performs the ONE bounded write, and owns the whole mechanism:
// the deadline probe, the watchdog that converts a cancellation into an expired
// write, and the joins that leave nothing running behind it.
func (p *procProcess) writeFrame(ctx context.Context, frame []byte) (int, error) {
	deadliner, canDeadline := p.stdin.(writeDeadliner)
	if canDeadline {
		// Probing IS the point: a stdin that is not pollable (a regular file, a
		// writer a test supplied) answers ErrNoDeadline, and then the deadline is
		// not the bound we have. Setting the context's own deadline here also means
		// the kernel enforces it even if the watchdog is slow to be scheduled, and
		// it clears any deadline a previous frame left behind.
		deadline := time.Time{}
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
		if err := deadliner.SetWriteDeadline(deadline); err != nil {
			canDeadline = false
		}
	}
	if done := ctx.Done(); done != nil {
		stop := make(chan struct{})
		watching := make(chan struct{})
		go func() {
			defer close(watching)
			select {
			case <-done:
				if canDeadline {
					_ = deadliner.SetWriteDeadline(writeAborted)
					return
				}
				// No deadline on this writer, so the only bound left is to take the
				// pipe away. It is terminal for the stream and that is acceptable
				// HERE and only here: the caller has already given up on this frame,
				// and an unbounded write would be worse than a closed one.
				p.closeStdin()
			case <-stop:
			}
		}()
		n, err := p.stdin.Write(frame)
		close(stop)
		// Joining the watchdog before returning is what makes "no writer goroutine
		// is left behind" TRUE, and it is also what lets the deadline be cleared
		// below without racing a watchdog that is still about to expire it.
		<-watching
		if canDeadline {
			_ = deadliner.SetWriteDeadline(time.Time{})
		}
		return n, err
	}
	return p.stdin.Write(frame)
}

// stdinWritable reports the stdin lifecycle state under the mutex, held for the
// decision alone.
func (p *procProcess) stdinWritable() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdinBroken != nil {
		return p.stdinBroken
	}
	if p.stdinDone {
		return errors.New("sessions: process stdin is closed")
	}
	return nil
}

// poisonStdin retires the stream after a half-written frame. The FIRST cause is
// kept: what broke the stream is the fact worth reporting, not what noticed it.
func (p *procProcess) poisonStdin(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdinBroken == nil {
		p.stdinBroken = err
	}
}

// Output returns the channel of output frames (closed on exit).
func (p *procProcess) Output() <-chan OutputFrame { return p.out }

// Wait blocks until the process exits and returns its exit code.
func (p *procProcess) Wait() (int, error) {
	<-p.waitDone
	return p.exit, p.waitErr
}

// Stop closes stdin, signals the process group to terminate, escalates to a hard
// kill after WaitDelay, and — only if that STILL leaves the run unfinished —
// takes the output pipes away so the child can be reaped at all.
//
// The context is deliberately IGNORED. Callers reach Stop with a request context
// and one of them already passes context.WithoutCancel: honouring cancellation
// here would return from a teardown that had signalled a process and then not
// waited for it, which is how a session leaks a live child and its group. The
// bound is the ladder itself (at most three WaitDelay steps), not the caller.
func (p *procProcess) Stop(_ context.Context) error {
	p.closeStdin()                // EOF for a child that reads stdin, and it unblocks any writer
	_ = procGroupTerminate(p.cmd) // SIGTERM the group: let claude flush its transcript
	delay := p.waitDelay
	if delay <= 0 {
		delay = defaultWaitDelay
	}
	if p.finishedWithin(delay) {
		return nil
	}
	_ = procGroupKill(p.cmd) // escalate to SIGKILL
	if p.finishedWithin(delay) {
		return nil
	}
	// ⛔ SIGKILL REACHED THE GROUP AND THE RUN IS STILL NOT FINISHED, so something
	// holding the output pipes open is NOT in the group — a descendant that called
	// setsid, or a process that inherited the write end elsewhere. The pumps never
	// see EOF, so pumps.Wait never returns, so cmd.Wait is NEVER CALLED and the
	// child stays unreaped: the wait here used to be unbounded, and Stop is called
	// under the per-run operation lock, so it took /input, /interrupt, /stop and
	// /resume down with it.
	//
	// Ending the pumps is what lets Wait reap the child, and a pump can be stuck in
	// EITHER of two places, so the last rung releases both: `abandon` for one that
	// is blocked handing a frame to a consumer that stopped receiving, and the
	// parent read ends for one that is blocked on a pipe somebody outside the group
	// still holds open. Closing pipes alone leaves the first case wedged for ever,
	// which is exactly how a killed child stayed unreaped.
	//
	// It costs whatever could not be delivered, so it is the LAST step and it is
	// REPORTED: abandoned output is a fact the caller records, never a silent drop,
	// and Stop returning nil has to keep meaning "the child was reaped".
	p.abandonOutput()
	p.closeOutputPipes()
	if p.finishedWithin(delay) {
		return p.forcedTeardownReport()
	}
	return fmt.Errorf("sessions: the process did not finish after SIGTERM, SIGKILL, a forced close of its output pipes and abandoning its output: %w", ErrChildNotReaped)
}

// childWasReaped reads a Stop error for the ONE question a caller's lifecycle
// decision turns on: is that process gone?
//
// ⛔ AN ERROR ABOUT OUTPUT IS NOT AN ANSWER OF "NO". A forced teardown that
// abandoned output still collected its child, and the runtime converts this verdict
// into "the claim may be released" (runtime.go, teardownLiveWithContext), so reading
// any non-nil error as "still running" would keep a claim and a pending generation
// alive for a process that no longer exists. ErrChildNotReaped is the "no", and it
// wins if both ever travel together.
func childWasReaped(err error) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, ErrOutputAbandoned) && !errors.Is(err, ErrChildNotReaped)
}

// abandonOutput releases pumps blocked delivering to a consumer. It is idempotent
// and it is never reached by an ordinary stop: an EOF that drains normally ends
// the pumps by itself, two rungs earlier.
func (p *procProcess) abandonOutput() {
	if p.abandon == nil {
		return // a handle built without pumps (no Launch); nothing can be blocked
	}
	p.abandonOnce.Do(func() { close(p.abandon) })
}

// forcedTeardownReport says what the last rung actually gave up. The child WAS
// reaped in both cases — that is why they are errors about output and not about
// the process — and neither carries a byte of what was dropped: a count and the
// fixed sentinel, nothing provider-controlled.
func (p *procProcess) forcedTeardownReport() error {
	if dropped := p.abandonedFrames.Load(); dropped > 0 {
		return fmt.Errorf("sessions: the child was reaped, but %d output frame(s) could not be delivered and were abandoned, and the output pipes were closed, to finish the teardown: %w", dropped, ErrOutputAbandoned)
	}
	return fmt.Errorf("sessions: the child was reaped, but a process outside its group held the output pipes open; they were closed to finish the teardown, so any further output from that process is not bridged: %w", ErrOutputAbandoned)
}

// finishedWithin reports whether the run completed (pumps drained AND cmd.Wait
// returned) within d.
func (p *procProcess) finishedWithin(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.waitDone:
		return true
	case <-timer.C:
		return false
	}
}

// closeOutputPipes closes the PARENT ends of stdout/stderr. cmd.Wait closes them
// too when it eventually runs, and closing an os.File twice is a no-op error that
// nothing reads.
func (p *procProcess) closeOutputPipes() {
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
}

// PID is the process id (0 if not started).
func (p *procProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// closeStdin closes the child's input once. It is safe to call WHILE a Send is
// blocked in Write on the same pipe — that is the point of it no longer sharing a
// mutex with the write: os.File's poller unblocks the pending write instead of
// waiting for it, so a stop can always reach a child that stopped reading.
func (p *procProcess) closeStdin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stdinDone {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
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

// baseEnvAllow is the minimal, non-sensitive host environment a launched official
// CLI needs: PATH (to find node/claude/codex), HOME (its config + transcripts) and
// locale/term/tmp basics. Nothing secret is in this set, and a PROFILED launch
// overrides HOME explicitly, so the inherited one only ever serves an unprofiled
// legacy run.
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

// forbiddenInheritedEnvName names what the child never INHERITS, whatever an
// operator allowlists.
//
// The set is the control plane's own secrets plus EVERY provider's credential and
// routing family, not just the one this launch uses. An inherited OPENAI_API_KEY
// would silently authenticate a Codex child as whoever runs the engine, and a
// CODEX_HOME or GROK_HOME would point it at a home nobody selected — both are the
// same accident §6 refuses from a caller, arriving through the host environment
// instead of a request body. The explicit launch values are unaffected: they are
// appended after this filter, which is what lets a profile set the home it owns.
func forbiddenInheritedEnvName(name string) bool {
	return strings.HasPrefix(name, "OLIVARES_") ||
		strings.HasPrefix(name, "ANTHROPIC_") ||
		strings.HasPrefix(name, "CLAUDE_CODE_") ||
		strings.HasPrefix(name, "CODEX_") ||
		strings.HasPrefix(name, "OPENAI_") ||
		strings.HasPrefix(name, "GROK_") ||
		strings.HasPrefix(name, "XAI_") ||
		strings.HasPrefix(name, "OPENCODE_") ||
		name == "CLAUDE_CONFIG_DIR" ||
		name == "DISABLE_AUTOUPDATER"
}
