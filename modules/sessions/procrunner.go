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

	"github.com/olivaresai/olivares/modules/sessions/confine"
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
	cmd, env, state, release, err := pr.command(ctx, spec)
	if err != nil {
		return nil, err
	}
	cmd.Dir = spec.Dir
	cmd.Env = sanitizedEnv(spec.EnvAllow, env)
	if spec.WaitDelay > 0 {
		cmd.WaitDelay = spec.WaitDelay
	}
	var p *procProcess
	if pr.usePTY {
		p, err = pr.launchPTY(cmd, spec.WaitDelay, release)
	} else {
		p, err = pr.launchPipes(cmd, spec.WaitDelay, release)
	}
	if err != nil {
		release()
		return nil, err
	}
	p.confinement = state
	return p, nil
}

func (pr *procRunner) launchPipes(cmd *exec.Cmd, waitDelay time.Duration, release func()) (*procProcess, error) {
	// Own process group so a graceful/hard stop reaches grandchildren that would
	// otherwise hold the stdout pipe open and wedge teardown.
	configureProcGroup(cmd)
	// Own both pipe ends instead of StdoutPipe/StderrPipe. Cmd.Wait must be able
	// to reap the child without closing readers that still contain final output.
	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: stdin pipe: %w", err)
	}
	defer stdinRead.Close()
	started := false
	defer func() {
		if !started {
			_ = stdin.Close()
		}
	}()
	stdout, stdoutWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: stdout pipe: %w", err)
	}
	defer stdoutWrite.Close()
	defer func() {
		if !started {
			_ = stdout.Close()
		}
	}()
	stderr, stderrWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: stderr pipe: %w", err)
	}
	defer stderrWrite.Close()
	defer func() {
		if !started {
			_ = stderr.Close()
		}
	}()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinRead, stdoutWrite, stderrWrite
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sessions: start %q: %w", specProgram(cmd), err)
	}
	started = true
	cmd.Env = nil
	return pr.watch(cmd, stdin, stdout, stderr, waitDelay, release), nil
}

func specProgram(cmd *exec.Cmd) string {
	if cmd == nil || cmd.Path == "" {
		return ""
	}
	return cmd.Path
}

func (pr *procRunner) watch(cmd *exec.Cmd, stdin io.WriteCloser, stdout, stderr io.ReadCloser, waitDelay time.Duration, release func()) *procProcess {
	p := &procProcess{
		cmd:   cmd,
		stdin: stdin,
		// Retain the owned read ends to bound final drain even when a descendant
		// keeps a writer open after the direct child exits.
		stdout:    stdout,
		stderr:    stderr,
		out:       make(chan OutputFrame, 256),
		waitDone:  make(chan struct{}),
		abandon:   make(chan struct{}),
		waitDelay: waitDelay,
	}

	// Reaping and output drain have separate ownership. Wait does not own these
	// readers, so it can collect the direct child while the pumps preserve final
	// output. After exit, WaitDelay (or five seconds) bounds the remaining drain.
	var pumps sync.WaitGroup
	pumps.Add(2)
	go pr.pump(&pumps, p, stdout, streamStdout)
	go pr.pump(&pumps, p, stderr, streamStderr)
	drained := make(chan struct{})
	go func() { pumps.Wait(); close(drained) }()
	go func() {
		err := cmd.Wait()
		p.closeStdin()
		delay := waitDelay
		if delay <= 0 {
			delay = defaultWaitDelay
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		var drainErr error
		select {
		case <-drained:
		case <-timer.C:
			// Give an already completed drain precedence over the expired timer.
			select {
			case <-drained:
			default:
				p.drainAbandoned.Store(true)
				if !p.stopping.Load() {
					drainErr = ErrOutputAbandoned
				}
				_ = procGroupKill(cmd)
				p.abandonOutput()
				p.closeOutputPipes()
				<-drained
			}
		}
		p.closeOutputPipes()
		p.exit, p.waitErr = exitCodeOf(err)
		if p.waitErr != nil {
			if cmd.ProcessState != nil {
				// exec may return a context error after OS Wait collected the child.
				p.exit = cmd.ProcessState.ExitCode()
				p.waitErr = &reapedWaitError{cause: p.waitErr}
			} else {
				p.waitErr = errors.Join(ErrChildNotReaped, p.waitErr)
			}
		}
		p.waitErr = errors.Join(p.waitErr, p.outputErr, drainErr)
		release()
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
	if stream == streamStderr {
		pr.pumpDiagnostics(p, r)
		return
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readBoundedLine(br, pr.lineCap)
		if errors.Is(err, ErrOutputLineTooLong) {
			p.outputFailure.Do(func() {
				p.outputErr = errors.Join(ErrOutputLineTooLong, ErrOutputAbandoned)
				p.closeStdin()
				_ = procGroupKill(p.cmd)
				p.abandonOutput()
				p.closeOutputPipes()
			})
			return
		}
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

// diagnosticTruncatedMark ends a stderr line cut at the line cap. The secret
// redactor reads it: the text right before it may be the beginning of a value.
const diagnosticTruncatedMark = " [diagnostic truncated]"

// pumpDiagnostics retains a bounded stderr prefix, marks truncation once, then
// discards fragments without growing storage. Closing the owned reader releases
// an unterminated discard during Stop or an expired natural-exit drain. Diagnostic
// prefixes never enter the stdout protocol parser.
func (pr *procRunner) pumpDiagnostics(p *procProcess, r io.Reader) {
	limit := pr.lineCap
	if limit <= 0 {
		limit = maxOutputLine
	}
	br := bufio.NewReaderSize(r, min(64*1024, limit+2))
	var prefix []byte
	truncated := false
	for {
		fragment, err := br.ReadSlice('\n')
		complete := !errors.Is(err, bufio.ErrBufferFull)
		if !truncated {
			size := len(prefix) + len(fragment)
			keep := min(len(fragment), limit+2-len(prefix))
			if len(prefix)+keep > cap(prefix) {
				grown := make([]byte, len(prefix), min(limit+2, max(len(prefix)+keep, 2*cap(prefix))))
				copy(grown, prefix)
				prefix = grown
			}
			prefix = append(prefix, fragment[:keep]...)
			if size > limit+2 || (complete && len(trimCRLF(prefix)) > limit) {
				diagnostic := append(append([]byte(nil), prefix[:min(limit, len(prefix))]...), diagnosticTruncatedMark...)
				if !p.deliver(OutputFrame{Stream: streamStderr, Data: diagnostic}) {
					return
				}
				truncated = true
				prefix = nil
			} else if complete && (err == nil || errors.Is(err, io.EOF)) {
				line := trimCRLF(prefix)
				if len(line) > 0 && !p.deliver(OutputFrame{Stream: streamStderr, Data: line}) {
					return
				}
				prefix = nil
			}
		}
		if complete {
			if err != nil {
				return
			}
			truncated = false
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
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout         io.ReadCloser
	stderr         io.ReadCloser
	out            chan OutputFrame
	waitDone       chan struct{}
	waitDelay      time.Duration
	outputFailure  sync.Once
	outputErr      error // written once by a pump, read after both pumps join
	stopping       atomic.Bool
	drainAbandoned atomic.Bool

	exit    int
	waitErr error

	// confinement is how the child runs (Landlock, or none and why).
	confinement confine.State

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

	// abandon releases pumps blocked on delivery after a forced Stop or an
	// expired natural-exit drain. The ordinary EOF path never abandons output.
	abandon         chan struct{}
	abandonOnce     sync.Once
	abandonedFrames atomic.Int64
}

// ErrOutputAbandoned classifies a Wait or Stop that DID reap the child but had to give up
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

// Stop closes stdin, signals the process group, and escalates after WaitDelay.
// If output still cannot finish, it forces pipe closure and joins the pumps.
// The child is reaped independently; incomplete output is reported by Stop.
//
// The context is deliberately IGNORED. Callers reach Stop with a request context
// and one of them already passes context.WithoutCancel: honouring cancellation
// here would return from a teardown that had signalled a process and then not
// waited for it, which is how a session leaks a live child and its group. The
// bound is the ladder itself (at most three WaitDelay steps), not the caller.
func (p *procProcess) Stop(_ context.Context) error {
	select {
	case <-p.waitDone:
		// The direct child and its output are finished, but other group members
		// may have closed their descriptors and continued running.
		_ = procGroupKill(p.cmd)
		return p.completedStopReport()
	default:
	}
	p.stopping.Store(true)
	p.closeStdin()                // EOF for a child that reads stdin, and it unblocks any writer
	_ = procGroupTerminate(p.cmd) // SIGTERM the group: let claude flush its transcript
	delay := p.waitDelay
	if delay <= 0 {
		delay = defaultWaitDelay
	}
	if p.finishedWithin(delay) {
		return p.completedStopReport()
	}
	_ = procGroupKill(p.cmd) // escalate to SIGKILL
	if p.finishedWithin(delay) {
		return p.completedStopReport()
	}
	// A holder outside the group or a blocked consumer survived both signals.
	// Close readers and release blocked deliveries, then join the owned work. This
	// last rung reports incomplete output separately from an unreaped child.
	p.drainAbandoned.Store(true)
	p.abandonOutput()
	p.closeOutputPipes()
	if p.finishedWithin(delay) {
		return p.forcedTeardownReport()
	}
	return fmt.Errorf("sessions: the process did not finish after SIGTERM, SIGKILL, a forced close of its output pipes and abandoning its output: %w", ErrChildNotReaped)
}

// completedStopReport preserves incomplete-output evidence after a natural drain
// timeout or a previous Stop. The closed waitDone guarantees the child was reaped.
func (p *procProcess) completedStopReport() error {
	if errors.Is(p.waitErr, ErrChildNotReaped) {
		return p.waitErr
	}
	if p.drainAbandoned.Load() || errors.Is(p.waitErr, ErrOutputAbandoned) {
		return p.forcedTeardownReport()
	}
	return p.waitErr
}

// childWasReaped reads a Wait or Stop error for the ONE question a caller's lifecycle
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
	if errors.Is(err, ErrChildNotReaped) {
		return false
	}
	var observed *reapedWaitError
	return errors.As(err, &observed) || errors.Is(err, ErrOutputAbandoned)
}

// reapedWaitError retains a non-exit exec error and the independent positive
// ProcessState evidence. Only the native wait coordinator constructs it.
type reapedWaitError struct{ cause error }

func (e *reapedWaitError) Error() string { return e.cause.Error() }
func (e *reapedWaitError) Unwrap() error { return e.cause }

// OutputIncomplete is read after Wait. It retains forced-drain evidence even
// when explicit Stop keeps the existing successful-collection Wait result.
func (p *procProcess) OutputIncomplete() bool {
	<-p.waitDone
	return p.drainAbandoned.Load() || p.abandonedFrames.Load() > 0 || errors.Is(p.outputErr, ErrOutputAbandoned)
}

// abandonOutput releases blocked deliveries after a forced output drain.
// It is idempotent; healthy EOF delivery never reaches it.
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
		return fmt.Errorf("sessions: the child was reaped, but %d output frame(s) could not be delivered and were abandoned, and the output pipes were closed, to finish the teardown: %w", dropped, errors.Join(ErrOutputAbandoned, p.waitErr))
	}
	if errors.Is(p.waitErr, ErrOutputLineTooLong) {
		return fmt.Errorf("sessions: the child was reaped after an oversized protocol record was refused; output was abandoned: %w", p.waitErr)
	}
	return fmt.Errorf("sessions: the child was reaped, but output did not finish within the drain bound; its pipes were closed and further output is not bridged: %w", ErrOutputAbandoned)
}

// finishedWithin reports whether the child was reaped and both pumps joined within d.
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

// closeOutputPipes closes the owned readers. Closing an os.File twice is harmless.
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

// ErrOutputLineTooLong means a native output record exceeded its byte ceiling.
// No prefix is delivered: it could otherwise become a different valid JSON record.
var ErrOutputLineTooLong = errors.New("sessions: native output line exceeds its byte limit")

// readBoundedLine retains at most limit bytes plus two bytes for CRLF. ReadSlice
// adds at most the reader's fixed buffer as read-ahead; it never collects a whole
// oversized record. Excess is terminal, so no unbounded discard is needed.
func readBoundedLine(br *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = maxOutputLine
	}
	var line []byte
	for {
		fragment, err := br.ReadSlice('\n')
		size := len(line) + len(fragment)
		if size > limit+2 {
			return nil, ErrOutputLineTooLong
		}
		if size > cap(line) {
			grown := make([]byte, len(line), min(limit+2, max(size, 2*cap(line))))
			copy(grown, line)
			line = grown
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = trimCRLF(line)
		if len(line) > limit {
			return nil, ErrOutputLineTooLong
		}
		return line, err
	}
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
