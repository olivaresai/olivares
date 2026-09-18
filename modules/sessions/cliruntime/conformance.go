// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// RunConformance exercises launch, stdin/stdout/stderr, attach, reconnect,
// stop-with-exit, process-loss honesty, and resume when the driver implements
// Resumer. It is the contract battery: call it on Fake always, and on a local
// PTY driver when a peer program is available.
func RunConformance(t *testing.T, d Driver) {
	t.Helper()
	if d == nil {
		t.Fatal("nil driver")
	}
	if OfficialProgram(d.Kind()) == "" {
		t.Fatalf("kind %q is not an official CLI", d.Kind())
	}
	t.Run("launch-io-stop", func(t *testing.T) { conformLaunchIOStop(t, d) })
	t.Run("stderr-distinct", func(t *testing.T) { conformStderr(t, d) })
	t.Run("attach-cursor", func(t *testing.T) { conformAttachCursor(t, d) })
	t.Run("reconnect-same-process", func(t *testing.T) { conformReconnect(t, d) })
	t.Run("reconnect-after-process-loss", func(t *testing.T) { conformProcessLoss(t, d) })
	t.Run("stop-exit-status", func(t *testing.T) { conformStopExit(t, d) })
	if r, ok := d.(Resumer); ok {
		t.Run("resume-same-conversation", func(t *testing.T) { conformResume(t, r) })
	}
}

func conformLaunchIOStop(t *testing.T, d Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _, _ = s.Stop(ctx) }()
	if s.Ref() == "" {
		t.Fatal("session ref is empty")
	}
	ch, unsub := s.Attach(0)
	defer unsub()
	if err := s.Send(ctx, []byte(`{"type":"user","text":"ping"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !waitFrame(t, ch, 3*time.Second, func(f Frame) bool {
		return f.Stream == StreamStdout && len(f.Data) > 0
	}) {
		t.Fatal("no stdout after send")
	}
	res, err := s.Stop(ctx)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !res.ProcessExited {
		t.Fatal("stop did not observe process exit")
	}
}

func conformStderr(t *testing.T, d Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _, _ = s.Stop(ctx) }()
	ch, unsub := s.Attach(0)
	defer unsub()
	_ = s.Send(ctx, []byte("ping"))
	if !waitFrame(t, ch, 3*time.Second, func(f Frame) bool {
		return f.Stream == StreamStderr && len(f.Data) > 0
	}) {
		t.Fatal("no stderr frame; stdout and stderr must stay distinct streams")
	}
}

func conformAttachCursor(t *testing.T, d Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _, _ = s.Stop(ctx) }()
	ch, unsub := s.Attach(0)
	_ = s.Send(ctx, []byte("one"))
	seen := map[int64]struct{}{}
	var last int64
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("attach closed while live")
			}
			if _, dup := seen[f.Seq]; dup {
				t.Fatalf("duplicate seq %d on first attach", f.Seq)
			}
			seen[f.Seq] = struct{}{}
			if f.Seq > last {
				last = f.Seq
			}
			if len(seen) >= 2 {
				goto cut
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
cut:
	unsub()
	if last < 1 {
		t.Fatal("first attach delivered no sequenced frame")
	}
	ch2, unsub2 := s.Attach(last + 1)
	defer unsub2()
	_ = s.Send(ctx, []byte("two"))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case f, ok := <-ch2:
			if !ok {
				return
			}
			if _, dup := seen[f.Seq]; dup {
				t.Fatalf("duplicate seq %d after cursor attach", f.Seq)
			}
			if f.Seq <= last {
				t.Fatalf("cursor attach replayed seq %d at or below last=%d", f.Seq, last)
			}
			seen[f.Seq] = struct{}{}
			if len(seen) >= 3 {
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func conformReconnect(t *testing.T, d Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _, _ = s.Stop(ctx) }()
	ch, unsub := s.Attach(0)
	_ = s.Send(ctx, []byte("a"))
	var last int64
	_ = waitFrame(t, ch, 3*time.Second, func(f Frame) bool {
		last = f.Seq
		return last >= 1
	})
	unsub()
	ch2, unsub2, err := s.Reconnect(last + 1)
	if err != nil {
		t.Fatalf("reconnect on live process: %v", err)
	}
	defer unsub2()
	_ = s.Send(ctx, []byte("b"))
	if !waitFrame(t, ch2, 3*time.Second, func(f Frame) bool {
		return f.Seq > last
	}) {
		t.Fatal("reconnect delivered no new frame")
	}
}

func conformProcessLoss(t *testing.T, d Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := s.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, _, err = s.Reconnect(0)
	if !errors.Is(err, ErrProcessGone) {
		t.Fatalf("reconnect after process loss = %v, want ErrProcessGone (must not launch a replacement)", err)
	}
}

func conformStopExit(t *testing.T, d Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	res, err := s.Stop(ctx)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !res.ProcessExited {
		t.Fatal("stop result must record that the process exited")
	}
	waited, err := s.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if waited.ExitCode != res.ExitCode {
		t.Fatalf("wait exit %d != stop exit %d", waited.ExitCode, res.ExitCode)
	}
}

func conformResume(t *testing.T, d Resumer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	first, err := d.Launch(ctx, LaunchRequest{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	ch, unsub := first.Attach(0)
	_ = waitFrame(t, ch, 3*time.Second, func(f Frame) bool {
		return first.ConversationID() != "" || strings.Contains(string(f.Data), "session_id")
	})
	unsub()
	conv := first.ConversationID()
	if conv == "" {
		t.Fatal("first generation nominated no conversation")
	}
	gen1 := first.Generation()
	if _, err := first.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	second, err := d.Resume(ctx, ResumeRequest{
		LaunchRequest:  LaunchRequest{ResumeID: conv},
		ConversationID: conv,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer func() { _, _ = second.Stop(ctx) }()
	if second.ConversationID() != conv {
		t.Fatalf("resume conversation %q != %q (must not start a different conversation)", second.ConversationID(), conv)
	}
	if second.Generation() == gen1 {
		t.Fatal("resume kept the same process generation")
	}
}

func waitFrame(t *testing.T, ch <-chan Frame, d time.Duration, ok func(Frame) bool) bool {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, open := <-ch:
			if !open {
				return false
			}
			if ok(f) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
