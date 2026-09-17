// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// These are the permanent controls for the NATIVE process I/O bound: a native
// stdin write that honours its context, a stop that can always reach the child,
// and a stream that is never silently continued after a half-written frame.
//
// Every fixture here is an OWNED local child or an OWNED pipe, every one of them
// is bounded, and each control fails LOUDLY rather than hanging: a regression of
// the defect they cover is an unbounded wait, and a test that hangs reports
// nothing.

// deafChildScript is a child that NEVER reads its stdin and ignores SIGTERM —
// the two properties that make a native write block and a stop escalate. `exec`
// leaves exactly one process, and an ignored signal disposition survives it.
const deafChildScript = `#!/bin/sh
trap '' TERM
printf 'deaf-ready\n'
exec sleep 300
`

// echoChildScript is a NORMAL child: it reads whole lines, answers each one and
// exits with a status of its own. It is here to prove the correction did not buy
// its bound with the ordinary path.
const echoChildScript = `#!/bin/sh
printf 'ready\n'
while IFS= read -r line; do
  case "$line" in
    quit) printf 'bye\n' ; exit 7 ;;
    *) printf 'echo:%s\n' "$line" ;;
  esac
done
exit 0
`

// chattyFrames is more output than the runner's 256-frame channel can hold, so a
// consumer that stops receiving leaves a pump blocked in delivery rather than on
// a pipe — the state closing the pipes cannot reach.
const chattyFrames = 600

// chattyChildScript floods its stdout and then refuses to die on SIGTERM, so a
// stop must escalate while a pump is blocked handing frames over.
const chattyChildScript = `#!/bin/sh
trap '' TERM
i=0
while [ $i -lt 600 ]; do printf 'frame-%s\n' "$i"; i=$((i+1)); done
exec sleep 300
`

// countingChildScript is the same flood with an ordinary exit: the control for a
// healthy consumer, which must receive every frame in order.
const countingChildScript = `#!/bin/sh
i=0
while [ $i -lt 600 ]; do printf 'frame-%s\n' "$i"; i=$((i+1)); done
exit 0
`

// groupGrandchildScript spawns a grandchild that inherits stdout and stays IN
// the process group. Both ignore SIGTERM, so only the group SIGKILL ends them.
const groupGrandchildScript = `#!/bin/sh
trap '' TERM
sh -c 'trap "" TERM ; printf "grandchild %s\n" "$$" ; exec sleep 300' &
printf 'child up\n'
exec sleep 300
`

// escapedGrandchildScript spawns a holder of the stdout pipe that LEAVES the
// process group (its own session), so a group kill cannot reach it. It is the
// only shape that can still wedge teardown, and the test that uses it kills the
// escapee itself.
const escapedGrandchildScript = `#!/bin/sh
trap '' TERM
%s sh -c 'trap "" TERM ; printf "escaped %%s\n" "$$" ; exec sleep 300' &
printf 'child up\n'
exec sleep 300
`

func writeOwnedChild(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owned-child.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write owned child: %v", err)
	}
	return path
}

// launchOwnedChild starts one owned native child through the REAL runner, so the
// stdin under test is the one exec.Cmd builds and not a stand-in.
func launchOwnedChild(t *testing.T, script string, waitDelay time.Duration) *procProcess {
	t.Helper()
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: writeOwnedChild(t, script), Dir: t.TempDir(), WaitDelay: waitDelay,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	p, ok := proc.(*procProcess)
	if !ok {
		t.Fatalf("process type = %T, want *procProcess", proc)
	}
	t.Cleanup(func() {
		// A cleanup that can hang would turn a regression of the bound into a
		// five-minute binary timeout with no name attached to it. Stop is given a
		// bound of its own here, and the group is ended by hand if it misses it.
		done := make(chan struct{})
		go func() { defer close(done); _ = p.Stop(context.Background()) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = procGroupKill(p.cmd)
			t.Errorf("Stop did not finish within 20s in cleanup; the process group was killed directly")
		}
	})
	return p
}

// frameSink drains the output channel for the whole life of the process, which is
// what the real bridge does. Without it a pump can block on a full channel and
// the teardown it is part of would be measuring the test, not the runner.
type frameSink struct {
	mu     sync.Mutex
	lines  []string
	closed chan struct{}
}

func drainOutput(p *procProcess) *frameSink {
	s := &frameSink{closed: make(chan struct{})}
	go func() {
		defer close(s.closed)
		for f := range p.Output() {
			s.mu.Lock()
			s.lines = append(s.lines, string(f.Data))
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *frameSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

func (s *frameSink) find(prefix string) (string, bool) {
	for _, l := range s.all() {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}
	return "", false
}

func (s *frameSink) waitClosed(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-s.closed:
	case <-time.After(d):
		t.Fatalf("the output channel was not closed within %s: the pumps never drained", d)
	}
}

// newPipeBackedProcess builds a procProcess over an OWNED os.Pipe with nothing
// draining it. exec.Cmd's StdinPipe hands the runner the write end of exactly
// this kind of pipe, so these controls exercise the real mechanism.
func newPipeBackedProcess(t *testing.T) (*procProcess, *os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return &procProcess{stdin: w}, r, w
}

// fillPipe writes until the kernel buffer will take no more, so the NEXT write
// blocks. Each attempt gets its own fresh deadline: a slow scheduler must not be
// able to pass for a full pipe.
func fillPipe(t *testing.T, w *os.File) int {
	t.Helper()
	defer func() { _ = w.SetWriteDeadline(time.Time{}) }()
	chunk := bytes.Repeat([]byte("F"), 4096) // ≤ PIPE_BUF: all or nothing
	total := 0
	for i := 0; i < 4096; i++ {
		if err := w.SetWriteDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatalf("this pipe does not support write deadlines: %v", err)
		}
		n, err := w.Write(chunk)
		total += n
		if n < len(chunk) {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("filling the pipe: wrote %d, err %v", n, err)
			}
			return total
		}
	}
	t.Fatalf("the pipe never filled after %d bytes", total)
	return total
}

// readAvailable reads everything the pipe holds right now and stops at the first
// quiet moment. It is the byte accounting these controls rest on.
func readAvailable(t *testing.T, r *os.File, quiet time.Duration) []byte {
	t.Helper()
	defer func() { _ = r.SetReadDeadline(time.Time{}) }()
	var got []byte
	buf := make([]byte, 32*1024)
	for {
		if err := r.SetReadDeadline(time.Now().Add(quiet)); err != nil {
			t.Fatalf("read deadline: %v", err)
		}
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, os.ErrClosed) {
				return got
			}
			return got // EOF
		}
	}
}

var procfsAvailable = func() bool { _, err := os.Stat("/proc/self/stat"); return err == nil }()

// processGoneOrZombie reports that pid is no longer a running process. A zombie
// counts: an orphaned grandchild is reparented, and whether ITS reaper has run
// yet is not this runner's business — that it stopped running is.
func processGoneOrZombie(pid int) bool {
	if pid <= 0 {
		return true
	}
	if !procfsAvailable {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 || i+2 >= len(raw) {
		return false
	}
	return raw[i+2] == 'Z'
}

// gateOf builds the write gate BEFORE any writer runs and hands it back, so the
// waits below never take the lifecycle mutex. A regression that holds that mutex
// across a write must fail a control, not freeze the helper watching for it.
func gateOf(p *procProcess) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeGate == nil {
		p.writeGate = make(chan struct{}, 1)
	}
	return p.writeGate
}

// waitGateHeld blocks until a writer owns the write gate, which is how these
// controls know the SECOND writer is really queueing behind the first one.
func waitGateHeld(t *testing.T, gate chan struct{}) {
	t.Helper()
	waitFor(t, "the first writer to hold the stdin write gate", func() bool {
		return len(gate) == 1
	})
}

// sendWithin runs ONE Send and fails the control if it does not come back in
// time. Every Send in this file goes through it: the bound is what is under
// test, so it is asserted rather than assumed, and an unbounded write reports a
// named expectation instead of stalling the binary.
func sendWithin(t *testing.T, p *procProcess, ctx context.Context, line []byte, d time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.Send(ctx, line) }()
	return mustFinishWithin(t, "Send", d, done)
}

func mustFinishWithin(t *testing.T, what string, d time.Duration, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("%s did not return within %s: the native I/O bound is not bounded", what, d)
		return nil
	}
}

// ---------------------------------------------------------------------------
// Blocked stdin: the deadline and the cancellation.
// ---------------------------------------------------------------------------

// A child that stops reading its stdin must not be able to hold a caller past its
// deadline. This is the defect the independent review reproduced: Send took no
// notice of its context and returned only when the pipe was destroyed.
func TestProcProcessSendStopsAtItsDeadlineWhenTheChildDoesNotRead(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, deafChildScript, 300*time.Millisecond)
	sink := drainOutput(p)
	waitFor(t, "the deaf child to announce itself", func() bool {
		_, ok := sink.find("deaf-ready")
		return ok
	})

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := sendWithin(t, p, ctx, bytes.Repeat([]byte("X"), 1<<20), 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send error = %v, want a wrapped context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Send took %s to honour a 250ms deadline", elapsed)
	}
	// The child is still there and the stop ladder still works on it.
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("stop after a bounded write: %v", err)
	}
	sink.waitClosed(t, 5*time.Second)
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// The same bound, reached by cancellation rather than by a deadline: a stdin with
// no deadline of its own must still stop when the caller gives up.
func TestProcProcessSendStopsWhenItsContextIsCancelled(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, deafChildScript, 300*time.Millisecond)
	sink := drainOutput(p)
	waitFor(t, "the deaf child to announce itself", func() bool {
		_, ok := sink.find("deaf-ready")
		return ok
	})

	gate := gateOf(p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Send(ctx, bytes.Repeat([]byte("X"), 1<<20)) }()
	waitGateHeld(t, gate)
	cancel()

	err := mustFinishWithin(t, "a cancelled Send", 10*time.Second, done)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v, want a wrapped context cancellation", err)
	}
}

// An already-cancelled request is NOT attempted: no byte of it reaches the child,
// so the caller may record a clean refusal instead of an uncertainty.
func TestProcProcessAnAlreadyCancelledSendWritesNoBytes(t *testing.T) {
	t.Parallel()

	p, r, _ := newPipeBackedProcess(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sendWithin(t, p, ctx, []byte(`{"type":"user"}`), 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v, want a wrapped context cancellation", err)
	}
	if !strings.Contains(err.Error(), "not attempted") {
		t.Fatalf("Send error = %q, want it to report that nothing was attempted", err)
	}
	if got := readAvailable(t, r, 150*time.Millisecond); len(got) != 0 {
		t.Fatalf("a cancelled request put %d byte(s) on the child's stdin: %q", len(got), got)
	}
}

// Waiting for ANOTHER writer is bounded too. A frame queued behind a blocked one
// gives up on its own context and writes nothing at all.
func TestProcProcessAQueuedWriterGivesUpWithoutWritingAnything(t *testing.T) {
	t.Parallel()

	p, r, _ := newPipeBackedProcess(t)
	fillPipe(t, p.stdin.(*os.File))
	gate := gateOf(p)

	first := make(chan error, 1)
	go func() { first <- p.Send(context.Background(), bytes.Repeat([]byte("A"), 8192)) }()
	waitGateHeld(t, gate)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	queued := make(chan error, 1)
	go func() { queued <- p.Send(ctx, bytes.Repeat([]byte("B"), 64)) }()

	err := mustFinishWithin(t, "a queued Send whose context expired", 10*time.Second, queued)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued Send error = %v, want a wrapped context deadline", err)
	}
	if !strings.Contains(err.Error(), "not attempted") {
		t.Fatalf("queued Send error = %q, want it to report that nothing was attempted", err)
	}

	// Release the first writer and read EVERYTHING the pipe ever carried: the
	// queued frame must appear nowhere in it.
	p.closeStdin()
	if err := mustFinishWithin(t, "the blocked first Send", 10*time.Second, first); err == nil {
		t.Fatalf("the first Send reported success after its pipe was closed")
	}
	if got := readAvailable(t, r, 250*time.Millisecond); bytes.ContainsRune(got, 'B') {
		t.Fatalf("the queued frame reached the child's stdin (%d bytes carried)", len(got))
	}
}

// ---------------------------------------------------------------------------
// The lifecycle: a stop that can always reach the child.
// ---------------------------------------------------------------------------

// Stop must be able to close the input of a child that is not reading it WHILE a
// Send is blocked on that same input. The defect was structural: Send held the
// lifecycle mutex across the write, and closeStdin needed it.
func TestProcProcessStopUnblocksABlockedSendAndReapsTheChild(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, deafChildScript, 300*time.Millisecond)
	sink := drainOutput(p)
	waitFor(t, "the deaf child to announce itself", func() bool {
		_, ok := sink.find("deaf-ready")
		return ok
	})

	gate := gateOf(p)
	blocked := make(chan error, 1)
	go func() { blocked <- p.Send(context.Background(), bytes.Repeat([]byte("X"), 1<<20)) }()
	waitGateHeld(t, gate)

	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	if err := mustFinishWithin(t, "Stop with a Send blocked on stdin", 15*time.Second, stopped); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := mustFinishWithin(t, "the blocked Send after Stop", 10*time.Second, blocked); err == nil {
		t.Fatalf("the blocked Send reported success although the stop closed its stdin")
	}
	sink.waitClosed(t, 5*time.Second)
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	waitFor(t, "the stopped child to be reaped", func() bool { return processGoneOrZombie(p.PID()) })
}

// A stop reaps the child AND the grandchild it left holding the output pipe. The
// process group is what makes that true, and this control keeps it true.
func TestProcProcessStopReapsAChildAndItsGrandchild(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, groupGrandchildScript, 300*time.Millisecond)
	sink := drainOutput(p)
	var grandchild int
	waitFor(t, "the grandchild to announce its pid", func() bool {
		line, ok := sink.find("grandchild ")
		if !ok {
			return false
		}
		grandchild, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "grandchild ")))
		return grandchild > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	child := p.PID()
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	if err := mustFinishWithin(t, "Stop with a grandchild holding the output pipe", 15*time.Second, stopped); err != nil {
		t.Fatalf("stop: %v", err)
	}
	sink.waitClosed(t, 5*time.Second)
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	waitFor(t, "the child to be reaped", func() bool { return processGoneOrZombie(child) })
	waitFor(t, "the grandchild to be reaped", func() bool { return processGoneOrZombie(grandchild) })
}

// The remaining lifecycle edge, made explicit rather than left as a hang: a
// holder of the output pipe that LEFT the process group cannot be signalled, the
// pumps therefore never see EOF and cmd.Wait is never reached. Stop closes the
// parent read ends as its last rung, reaps the child, and REPORTS the tail it had
// to drop — it never returns nil for a child it did not reap.
func TestProcProcessStopClosesTheOutputPipesWhenAHolderEscapedTheGroup(t *testing.T) {
	t.Parallel()

	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is not installed: this control needs a holder that can leave the process group")
	}
	p := launchOwnedChild(t, fmt.Sprintf(escapedGrandchildScript, setsid), 300*time.Millisecond)
	sink := drainOutput(p)
	var escaped int
	waitFor(t, "the escaped holder to announce its pid", func() bool {
		line, ok := sink.find("escaped ")
		if !ok {
			return false
		}
		escaped, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "escaped ")))
		return escaped > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(escaped, syscall.SIGKILL) })

	child := p.PID()
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	stopErr := mustFinishWithin(t, "Stop with an escaped holder of the output pipes", 20*time.Second, stopped)
	if stopErr == nil {
		t.Fatalf("Stop reported a clean teardown although it had to close the output pipes to finish it")
	}
	if !strings.Contains(stopErr.Error(), "held the output pipes open") {
		t.Fatalf("Stop error = %v, want the dropped-tail report", stopErr)
	}
	sink.waitClosed(t, 5*time.Second)
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	waitFor(t, "the child to be reaped", func() bool { return processGoneOrZombie(child) })

	// The escapee is this fixture's own doing, so this test ends it and proves it.
	if err := syscall.Kill(escaped, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("killing the escaped holder: %v", err)
	}
	waitFor(t, "the escaped holder to be gone", func() bool { return processGoneOrZombie(escaped) })
}

// ---------------------------------------------------------------------------
// The stream: whole frames, and no continuation of a corrupted one.
// ---------------------------------------------------------------------------

// Concurrent frames arrive whole and in one piece. Each frame here is larger than
// the pipe buffer, so an unserialised writer would have to interleave.
func TestProcProcessConcurrentFramesArriveWholeAndUninterleaved(t *testing.T) {
	t.Parallel()

	p, r, w := newPipeBackedProcess(t)
	const writers, size = 16, 512 * 1024 // each frame needs many kernel writes

	type readResult struct {
		lines []string
		err   error
	}
	read := make(chan readResult, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		var lines []string
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		read <- readResult{lines: lines, err: sc.Err()}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- p.Send(context.Background(), bytes.Repeat([]byte{byte('a' + i)}, size))
		}(i)
	}
	writes := make(chan struct{})
	go func() { wg.Wait(); close(writes) }()
	select {
	case <-writes:
	case <-time.After(30 * time.Second):
		t.Fatalf("the concurrent frames did not finish within 30s")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a normal concurrent Send failed: %v", err)
		}
	}
	_ = w.Close() // EOF for the reader; every frame is already written

	got := <-read
	if got.err != nil {
		t.Fatalf("reading the frames: %v", got.err)
	}
	if len(got.lines) != writers {
		t.Fatalf("read %d lines, want %d: the frames did not stay whole", len(got.lines), writers)
	}
	seen := map[byte]bool{}
	for _, line := range got.lines {
		if len(line) != size {
			t.Fatalf("a frame is %d bytes, want %d: two writers interleaved", len(line), size)
		}
		if strings.Count(line, string(line[0])) != len(line) {
			t.Fatalf("a frame carries more than one writer's bytes")
		}
		seen[line[0]] = true
	}
	if len(seen) != writers {
		t.Fatalf("%d distinct frames arrived, want %d", len(seen), writers)
	}
}

// A HALF-WRITTEN frame retires the stream. The child is parked mid-line, so a
// later frame would be read as this one's tail: the next Send is refused instead,
// and it is refused without writing anything.
func TestProcProcessAHalfWrittenFrameRetiresTheStream(t *testing.T) {
	t.Parallel()

	p, r, _ := newPipeBackedProcess(t)
	const size = 256 * 1024 // larger than the pipe buffer, and nothing is draining
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := sendWithin(t, p, ctx, bytes.Repeat([]byte("X"), size), 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send error = %v, want a wrapped context deadline", err)
	}
	if !strings.Contains(err.Error(), "frame bytes were written") {
		t.Fatalf("Send error = %q, want it to report the half-written frame", err)
	}

	// The stream is retired, and a later attempt is neither a context failure nor
	// a write: it is a refusal, and a refusal is instant. The bound is here so that
	// LOSING the refusal reports a failed expectation instead of blocking on the
	// full pipe for ever.
	nextCtx, cancelNext := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelNext()
	next := sendWithin(t, p, nextCtx, []byte("YYYY"), 10*time.Second)
	if next == nil {
		t.Fatalf("a later Send succeeded on a stream left mid-frame")
	}
	if !strings.Contains(next.Error(), "unusable") {
		t.Fatalf("later Send error = %q, want the unusable-stream refusal", next)
	}

	carried := readAvailable(t, r, 250*time.Millisecond)
	if len(carried) == 0 || len(carried) >= size {
		t.Fatalf("the pipe carried %d bytes, want a partial frame under %d", len(carried), size)
	}
	if bytes.ContainsRune(carried, 'Y') {
		t.Fatalf("the refused frame put bytes on a stream that was already corrupted")
	}
	if bytes.ContainsRune(carried, '\n') {
		t.Fatalf("a frame terminator crossed although the frame never finished")
	}
}

// A write that moved NO bytes leaves the stream usable, and leaves nothing behind
// that could abort the next one: no stale deadline on the pipe, and no watchdog
// still holding the previous context.
func TestProcProcessAWriteThatMovedNoBytesLeavesTheStreamUsable(t *testing.T) {
	t.Parallel()

	p, r, _ := newPipeBackedProcess(t)
	resident := fillPipe(t, p.stdin.(*os.File))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := sendWithin(t, p, ctx, []byte(`{"type":"user"}`), 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send error = %v, want a wrapped context deadline", err)
	}
	if !strings.Contains(err.Error(), "no bytes were written") {
		t.Fatalf("Send error = %q, want it to report that nothing crossed", err)
	}

	drained := readAvailable(t, r, 250*time.Millisecond)
	if len(drained) != resident {
		t.Fatalf("drained %d bytes, want the %d resident ones: the refused frame wrote", len(drained), resident)
	}
	if bytes.ContainsRune(drained, '{') {
		t.Fatalf("the refused frame reached the child after reporting that nothing crossed")
	}

	// The same stream, immediately afterwards, with no deadline of its own.
	if err := sendWithin(t, p, context.Background(), []byte(`{"type":"user"}`), 10*time.Second); err != nil {
		t.Fatalf("a normal Send after a bounded refusal failed: %v", err)
	}
	if got := string(readAvailable(t, r, 250*time.Millisecond)); got != "{\"type\":\"user\"}\n" {
		t.Fatalf("the recovered stream carried %q", got)
	}
}

// The ordinary path is unchanged: whole frames in order, whole frames back, and
// the child's own exit status.
func TestProcProcessNormalSendsOutputAndExitAreUnchanged(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, echoChildScript, 2*time.Second)
	sink := drainOutput(p)
	for _, line := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`, "quit"} {
		if err := sendWithin(t, p, context.Background(), []byte(line), 10*time.Second); err != nil {
			t.Fatalf("send %q: %v", line, err)
		}
	}
	sink.waitClosed(t, 10*time.Second)
	exit, err := p.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if exit != 7 {
		t.Fatalf("exit = %d, want the child's own 7", exit)
	}
	want := []string{"ready", `echo:{"n":1}`, `echo:{"n":2}`, `echo:{"n":3}`, "bye"}
	got := sink.all()
	if len(got) != len(want) {
		t.Fatalf("output = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("output[%d] = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
}

// ---------------------------------------------------------------------------
// The queued handoff, and the forced teardown that must still reap.
//
// Both were found by the independent review of the first correction (N1, N2) and
// both are scheduling facts, so both are controlled here rather than sampled.
// ---------------------------------------------------------------------------

// queuedHandoffContext parks a caller INSIDE its first Done() evaluation, which is
// the moment Send has passed the uncontended attempt and reached the blocking
// queue select. The test then cancels and only afterwards releases the gate, so
// both select arms are ready at once and either scheduling decision is legal.
type queuedHandoffContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *queuedHandoffContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Context.Done()
}

// Winning the gate is not evidence that the caller still wants the frame. A
// cancellation that was already known at the handoff must leave the stream empty
// and be reported as no-attempt, on EVERY legal schedule.
func TestProcProcessACancelledQueueHandoffWritesNoBytes(t *testing.T) {
	t.Parallel()

	const schedules = 32
	violations := 0
	for i := 0; i < schedules; i++ {
		p, r, w := newPipeBackedProcess(t)
		gate := gateOf(p)
		gate <- struct{}{} // a preceding writer holds it
		base, cancel := context.WithCancel(context.Background())
		ctx := &queuedHandoffContext{Context: base, entered: make(chan struct{}), release: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- p.Send(ctx, []byte("cancelled-queued-frame")) }()
		select {
		case <-ctx.entered:
		case <-time.After(5 * time.Second):
			cancel()
			<-gate
			close(ctx.release)
			t.Fatalf("schedule %d: the queued writer never reached the queue select", i)
		}
		cancel() // known BEFORE the gate is released
		<-gate   // the preceding writer finishes: both arms are now ready
		close(ctx.release)

		err := mustFinishWithin(t, "the cancelled queue handoff", 10*time.Second, done)
		carried := readAvailable(t, r, time.Millisecond)
		if len(carried) > 0 || !errors.Is(err, context.Canceled) {
			violations++
			t.Logf("schedule %d: Send=%v bytes=%q", i, err, carried)
		}
		_ = r.Close()
		_ = w.Close()
	}
	if violations != 0 {
		t.Fatalf("%d/%d legal cancelled handoffs attempted a frame or reported success", violations, schedules)
	}
}

// A consumer that stops receiving must not be able to keep a killed child on the
// process table. Closing the pipes cannot release a pump blocked in `out <- frame`
// — it is not reading a pipe — so the last teardown rung releases delivery too.
// The proof of reaping is cmd.Wait completing, not a /proc observation.
func TestProcProcessStopReapsWhenTheConsumerStopsReceiving(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, chattyChildScript, 100*time.Millisecond)
	// NOTHING drains p.Output(): the buffered channel fills and a pump blocks on it.
	waitFor(t, "the output channel to fill behind a consumer that is not receiving",
		func() bool { return len(p.out) == cap(p.out) })

	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	err := mustFinishWithin(t, "Stop with a consumer that stopped receiving", 20*time.Second, stopped)

	select {
	case <-p.waitDone:
	default:
		t.Fatalf("Stop returned with the child still unreaped: %v", err)
	}
	if _, waitErr := p.Wait(); waitErr != nil {
		t.Fatalf("cmd.Wait did not complete: %v", waitErr)
	}
	if !errors.Is(err, ErrOutputAbandoned) {
		t.Fatalf("Stop error = %v, want it classified as abandoned output", err)
	}
	if errors.Is(err, ErrChildNotReaped) {
		t.Fatalf("Stop reported an unreaped child although cmd.Wait completed: %v", err)
	}
	if got := p.abandonedFrames.Load(); got == 0 {
		t.Fatalf("output was abandoned but the report counted none")
	}
	// What was already buffered is NOT discarded: the consumer that comes back
	// still receives it, and then sees the channel closed.
	sink := drainOutput(p)
	sink.waitClosed(t, 10*time.Second)
	if n := len(sink.all()); n < cap(p.out) {
		t.Fatalf("a returning consumer received %d frames, want at least the %d buffered", n, cap(p.out))
	}
}

// The same forced teardown through the REAL consumer: Module.bridge with the
// governed recorder paused mid-frame, which is how the review reproduced it. The
// child must be reaped inside the stop bound, the loss must be reported, and the
// run must still finalize normally once the recorder returns.
func TestProcProcessActualBridgeWithABlockedRecorderStillReapsTheChild(t *testing.T) {
	t.Parallel()

	rec := &pausedRecorder{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(rec.unblock)
	script := writeOwnedChild(t, chattyChildScript)
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(NewProcRunner()), WithProgram(script), WithCredentialSource(staticCred()),
		WithStopWaitDelay(100*time.Millisecond),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true, RecordIO: true}}),
		WithRecorder(rec))
	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatalf("no live handle for a running session")
	}
	p, ok := lr.proc.(*procProcess)
	if !ok {
		t.Fatalf("process type = %T, want *procProcess", lr.proc)
	}
	select {
	case <-rec.entered:
	case <-time.After(20 * time.Second):
		t.Fatalf("the bridge never reached the governed recorder")
	}
	waitFor(t, "the output channel to fill behind the blocked bridge",
		func() bool { return len(p.out) == cap(p.out) })

	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	stopErr := mustFinishWithin(t, "Stop behind a blocked governed recorder", 20*time.Second, stopped)
	select {
	case <-p.waitDone:
	default:
		t.Fatalf("Stop returned with the child still unreaped: %v", stopErr)
	}
	if _, waitErr := p.Wait(); waitErr != nil {
		t.Fatalf("cmd.Wait did not complete: %v", waitErr)
	}
	if !errors.Is(stopErr, ErrOutputAbandoned) || errors.Is(stopErr, ErrChildNotReaped) {
		t.Fatalf("Stop error = %v, want abandoned output on a reaped child", stopErr)
	}

	// The real bridge resumes, delivers what was buffered and finalizes the run.
	rec.unblock()
	select {
	case <-lr.finalizedCh:
	case <-time.After(20 * time.Second):
		t.Fatalf("the bridge did not finalize after the recorder returned")
	}
	if n := len(lr.ring.readFrom(0).frames); n < cap(p.out) {
		t.Fatalf("the ring holds %d frames, want at least the %d that were buffered", n, cap(p.out))
	}
}

// pausedRecorder is the governed recorder seam, held inside its first call. It is
// the honest shape of the blocked consumer: Module.bridge calls Record (and the
// store) synchronously between receives.
type pausedRecorder struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	freed   sync.Once
}

func (r *pausedRecorder) Record(context.Context, model.TenantID, string, RecordedFrame) error {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return nil
}

func (r *pausedRecorder) Finalize(context.Context, model.TenantID, string) error { return nil }

func (r *pausedRecorder) unblock() { r.freed.Do(func() { close(r.release) }) }

// A healthy consumer loses nothing, which is the other half of the teardown rung:
// abandoning is reachable only when delivery is genuinely stuck.
func TestProcProcessAHealthyConsumerLosesNoOutput(t *testing.T) {
	t.Parallel()

	p := launchOwnedChild(t, countingChildScript, 2*time.Second)
	sink := drainOutput(p)
	sink.waitClosed(t, 30*time.Second)

	exit, err := p.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	got := sink.all()
	if len(got) != chattyFrames {
		t.Fatalf("received %d frames, want all %d", len(got), chattyFrames)
	}
	for i, line := range got {
		if want := "frame-" + strconv.Itoa(i); line != want {
			t.Fatalf("frame %d = %q, want %q: the ordinary stream is not intact", i, line, want)
		}
	}
	if dropped := p.abandonedFrames.Load(); dropped != 0 {
		t.Fatalf("a healthy consumer lost %d frame(s)", dropped)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("stop after a clean exit: %v", err)
	}
}

// The forced-teardown classification has to survive the runtime's credential-safe
// wrapper — that wrapper is what every Stop call site goes through, and its text
// is deliberately generic — or the caller cannot tell "reaped, output lost" from
// "not reaped" at all.
func TestProcProcessForcedTeardownClassificationSurvivesTheCredentialSafeWrapper(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  error
		want error
	}{
		{"abandoned output", (&procProcess{}).forcedTeardownReport(), ErrOutputAbandoned},
		{"unreaped child", fmt.Errorf("teardown: %w", ErrChildNotReaped), ErrChildNotReaped},
	} {
		wrapped := secretSafeCredentialError("session process stop", tc.raw)
		if !errors.Is(wrapped, tc.want) {
			t.Fatalf("%s: wrapped error lost its classification: %v", tc.name, wrapped)
		}
		if errors.Is(wrapped, ErrOutputAbandoned) && errors.Is(wrapped, ErrChildNotReaped) {
			t.Fatalf("%s: the two outcomes are not distinguishable", tc.name)
		}
		if strings.Contains(wrapped.Error(), "output pipes") || strings.Contains(wrapped.Error(), "teardown") {
			t.Fatalf("%s: the redacted text leaked the diagnostic: %q", tc.name, wrapped.Error())
		}
	}
}

// Abandonment is the last resort, never a coin toss. With a teardown already
// signalled AND room in the channel, a single select would drop frames the
// consumer could still take; delivery is attempted on its own first, so only a
// genuinely stuck frame is ever abandoned.
func TestProcProcessDeliveryIsPreferredOverAbandonment(t *testing.T) {
	t.Parallel()

	const room = 64
	p := &procProcess{out: make(chan OutputFrame, room), abandon: make(chan struct{})}
	close(p.abandon) // the teardown is already signalled for every call below
	for i := 0; i < room; i++ {
		if !p.deliver(OutputFrame{Stream: streamStdout, Data: []byte("frame")}) {
			t.Fatalf("frame %d was abandoned although the consumer had room", i)
		}
	}
	if p.abandonedFrames.Load() != 0 {
		t.Fatalf("%d deliverable frame(s) were abandoned", p.abandonedFrames.Load())
	}
	// Full channel and a signalled teardown: NOW it gives up, and it counts it.
	if p.deliver(OutputFrame{Stream: streamStdout, Data: []byte("frame")}) {
		t.Fatalf("a frame was reported delivered into a full channel")
	}
	if got := p.abandonedFrames.Load(); got != 1 {
		t.Fatalf("abandoned frames = %d, want 1", got)
	}
	if len(p.out) != room {
		t.Fatalf("the channel holds %d frames, want %d", len(p.out), room)
	}
}

// The reaping verdict the runtime acts on: a report about lost output must not be
// read as an unstopped child, and ErrChildNotReaped must not be read as a stopped
// one. Both arrive through the credential-safe wrapper, which is what every Stop
// call site sees.
func TestProcProcessTheReapingVerdictSeparatesLostOutputFromAnUnstoppedChild(t *testing.T) {
	t.Parallel()

	abandoned := (&procProcess{}).forcedTeardownReport()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a clean stop", nil, true},
		{"output abandoned, child reaped", abandoned, true},
		{"output abandoned, wrapped", secretSafeCredentialError("session process teardown", abandoned), true},
		{"child not reaped", fmt.Errorf("teardown: %w", ErrChildNotReaped), false},
		{"child not reaped, wrapped", secretSafeCredentialError("session process teardown", fmt.Errorf("teardown: %w", ErrChildNotReaped)), false},
		{"an unrelated failure", errors.New("something else"), false},
	} {
		if got := childWasReaped(tc.err); got != tc.want {
			t.Fatalf("%s: childWasReaped = %v, want %v (err %v)", tc.name, got, tc.want, tc.err)
		}
	}
}
