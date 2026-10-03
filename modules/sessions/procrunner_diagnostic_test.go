// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeDiagnosticOverflowPreservesHealthyProtocolOutput(t *testing.T) {
	for _, tail := range []string{"\n", ""} {
		script := "#!/bin/sh\nprintf '%s' '" + strings.Repeat("d", 4096) + tail + "' >&2\nprintf '%s\\n' '" + `{"type":"assistant"}` + "'\n"
		proc, err := (&procRunner{lineCap: 64}).Launch(context.Background(), LaunchSpec{Program: writeOwnedChild(t, script), WaitDelay: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		p := proc.(*procProcess)
		t.Cleanup(func() { _ = p.Stop(context.Background()) })
		frames := make(chan []OutputFrame, 1)
		go func() {
			var all []OutputFrame
			for f := range p.Output() {
				all = append(all, f)
			}
			frames <- all
		}()
		if !p.finishedWithin(2 * time.Second) {
			t.Fatal("diagnostic overflow blocked natural exit")
		}
		if _, err := p.Wait(); err != nil {
			t.Fatalf("diagnostic overflow terminated the session: %v", err)
		}
		stdout, stderr := 0, 0
		for _, f := range <-frames {
			switch f.Stream {
			case streamStdout:
				stdout++
				if string(f.Data) != `{"type":"assistant"}` {
					t.Fatalf("protocol output %q", f.Data)
				}
			case streamStderr:
				stderr++
				if len(f.Data) > 128 || !strings.Contains(string(f.Data), "truncated") {
					t.Fatalf("diagnostic is not bounded/marked: %q", f.Data)
				}
			}
		}
		if stdout != 1 || stderr != 1 {
			t.Fatalf("stdout/stderr frames = %d/%d", stdout, stderr)
		}
	}
}

func TestNativeDiagnosticUnterminatedDiscardStopsWithinTheBound(t *testing.T) {
	script := "#!/bin/sh\nprintf '%s' '" + strings.Repeat("d", 4096) + "' >&2\nexec sleep 30\n"
	proc, err := (&procRunner{lineCap: 64}).Launch(context.Background(), LaunchSpec{Program: writeOwnedChild(t, script), WaitDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	p := proc.(*procProcess)
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	select {
	case f, ok := <-p.Output():
		if !ok || f.Stream != streamStderr || !strings.Contains(string(f.Data), "truncated") {
			t.Fatalf("no bounded diagnostic before discard: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no diagnostic before unterminated discard")
	}
	done := make(chan struct{})
	go func() {
		for range p.Output() {
		}
		close(done)
	}()
	err = p.Stop(context.Background())
	if errors.Is(err, ErrChildNotReaped) || !p.finishedWithin(time.Second) {
		t.Fatalf("discard did not join: %v", err)
	}
	<-done
}
