// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestProcProcessNaturalExitReapsDespiteAnInheritedOutputHolder(t *testing.T) {
	stdout, wout, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	defer wout.Close()
	stderr, werr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	defer werr.Close()
	// Both fixture processes are owned and reaped by this test. The second child
	// models an inherited descriptor holder without leaving an orphan fixture.
	holder := exec.Command("/bin/sleep", "30")
	holder.Env = []string{"PATH=/usr/bin:/bin"}
	holder.Stdout = wout
	holder.Stderr = werr
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf 'final-output\\n'; exit 0")
	configureProcGroup(cmd)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdout = wout
	cmd.Stderr = werr
	cmd.WaitDelay = 50 * time.Millisecond
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = wout.Close()
	_ = werr.Close()
	p := (&procRunner{lineCap: 64}).watch(cmd, nil, stdout, stderr, 50*time.Millisecond, func() {})
	defer p.Stop(context.Background())
	sink := drainOutput(p)
	if !p.finishedWithin(2 * time.Second) {
		t.Fatal("natural exit still waits for the inherited output holder")
	}
	<-sink.closed
	exit, err := p.Wait()
	if exit != 0 || !errors.Is(err, ErrOutputAbandoned) || errors.Is(err, ErrChildNotReaped) {
		t.Fatalf("exit=%d, error=%v", exit, err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("direct child was not reaped")
	}
	if frames := sink.all(); len(frames) != 1 || frames[0] != "final-output" {
		t.Fatalf("final output = %q", frames)
	}
	if err := p.Stop(context.Background()); !errors.Is(err, ErrOutputAbandoned) {
		t.Fatalf("later Stop lost the incomplete-output classification: %v", err)
	}
}

func TestProcProcessNaturalExitReapsWithABlockedConsumer(t *testing.T) {
	p := launchOwnedChild(t, countingChildScript, 50*time.Millisecond)
	if !p.finishedWithin(2 * time.Second) {
		t.Fatal("natural exit waited for a blocked output consumer")
	}
	if _, err := p.Wait(); !errors.Is(err, ErrOutputAbandoned) || errors.Is(err, ErrChildNotReaped) {
		t.Fatalf("natural drain error=%v", err)
	}
	if p.cmd.ProcessState == nil {
		t.Fatal("direct child was not reaped")
	}
	for range p.Output() {
	}
}

func TestProcProcessNaturalExitRacingRepeatedStop(t *testing.T) {
	for range 5 {
		p := launchOwnedChild(t, "#!/bin/sh\nprintf 'final\\n'\nexit 0\n", 50*time.Millisecond)
		sink := drainOutput(p)
		var stops sync.WaitGroup
		stops.Add(2)
		for range 2 {
			go func() {
				defer stops.Done()
				if err := p.Stop(context.Background()); err != nil {
					t.Errorf("stop: %v", err)
				}
			}()
		}
		stops.Wait()
		if !p.finishedWithin(time.Second) {
			t.Fatal("racing Stop did not join the child")
		}
		<-sink.closed
		if _, err := p.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProcProcessStopAfterNaturalExitCleansTheRemainingGroup(t *testing.T) {
	p := launchOwnedChild(t, "#!/bin/sh\nread release\nexit 0\n", 50*time.Millisecond)
	sink := drainOutput(p)
	holder := exec.Command("/bin/sleep", "30")
	holder.Env = []string{"PATH=/usr/bin:/bin"}
	holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: p.PID()}
	// A same-group descendant with all output descriptors closed cannot keep a
	// pump open. Model it with an owned sibling so the fixture is always reaped.
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan error, 1)
	go func() { holderDone <- holder.Wait(); close(holderDone) }()
	t.Cleanup(func() { _ = holder.Process.Kill(); <-holderDone })
	p.closeStdin()
	if !p.finishedWithin(time.Second) {
		t.Fatal("direct child did not exit")
	}
	<-sink.closed
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-holderDone:
	case <-time.After(time.Second):
		t.Fatal("Stop after natural exit left a same-group process alive")
	}
}

func TestProcProcessCanceledWaitRetainsConfirmedReaping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinRead.Close()
	defer stdin.Close()
	stdout, wout, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	defer wout.Close()
	stderr, werr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	defer werr.Close()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "read release; exit 0")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	configureProcGroup(cmd)
	canceled := make(chan struct{})
	// Make the documented exec outcome deterministic: cancellation is observed,
	// then the directly owned child exits normally and OS Wait collects it.
	cmd.Cancel = func() error { close(canceled); return nil }
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinRead, wout, werr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdinRead.Close()
	_ = wout.Close()
	_ = werr.Close()
	p := (&procRunner{lineCap: 64}).watch(cmd, stdin, stdout, stderr, 50*time.Millisecond, func() {})
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	sink := drainOutput(p)
	cancel()
	<-canceled
	p.closeStdin()
	if !p.finishedWithin(time.Second) {
		t.Fatal("canceled Wait did not join")
	}
	<-sink.closed
	exit, err := p.Wait()
	if cmd.ProcessState == nil || exit != 0 || !errors.Is(err, context.Canceled) || !childWasReaped(err) || errors.Is(err, ErrChildNotReaped) {
		t.Fatalf("confirmed collection lost: exit=%d, err=%v", exit, err)
	}
	if err := p.Stop(context.Background()); !childWasReaped(err) {
		t.Fatalf("completed Stop claimed an unreaped child: %v", err)
	}
}

type fixedOutputProcessRunner struct{ process Process }

func (r fixedOutputProcessRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	return r.process, nil
}

func TestTerminalEvidenceNativeStopRetainsIncompleteOutputWithNilWait(t *testing.T) {
	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinRead.Close()
	defer stdin.Close()
	stdout, wout, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	defer wout.Close()
	stderr, werr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	defer werr.Close()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "read release; exit 0")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	configureProcGroup(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinRead, wout, werr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdinRead.Close()
	_ = werr.Close()
	// This owned writer models a retained descriptor. It is closed by cleanup;
	// no process or goroutine outside the fixture is required to keep EOF pending.
	p := (&procRunner{lineCap: 64}).watch(cmd, stdin, stdout, stderr, 50*time.Millisecond, func() {})
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fixedOutputProcessRunner{p}), WithCredentialSource(staticCred()))
	ref, _ := launchedRun(t, m, tenant)
	if err := p.Stop(context.Background()); !errors.Is(err, ErrOutputAbandoned) {
		t.Fatalf("fixture did not force an incomplete drain: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("explicit Stop changed the successful collection contract: %v", err)
	}
	waitFor(t, "native terminal evidence", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateStopped || d.State == stateFailed
	})
	ev, _ := terminalEventOf(t, m, st, tenant, ref)
	if ev.TerminalObservation != obsProcessExitObserved || !strings.Contains(ev.Detail, "output incomplete") {
		t.Fatalf("native Stop lost durable output evidence: observation=%s, detail=%q", ev.TerminalObservation, ev.Detail)
	}
}

type forcedStopReader struct {
	done chan struct{}
	once sync.Once
}

func (r *forcedStopReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *forcedStopReader) Close() error             { r.once.Do(func() { close(r.done) }); return nil }

func TestProcProcessStopOwnedForcedDrainRetainsOutputEvidence(t *testing.T) {
	done := make(chan struct{})
	// Model collection/pump completion that can occur only after Stop closes its
	// owned reader. This makes Stop's last rung deterministic, without a race
	// between its timer and the native wait coordinator's drain timer.
	p := &procProcess{cmd: &exec.Cmd{}, stdout: &forcedStopReader{done: done}, waitDone: done, waitDelay: time.Millisecond, abandon: make(chan struct{})}
	err := p.Stop(context.Background())
	if !errors.Is(err, ErrOutputAbandoned) || !p.OutputIncomplete() {
		t.Fatalf("Stop's forced drain lost completion evidence: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("explicit Stop changed successful collection: %v", err)
	}
}
