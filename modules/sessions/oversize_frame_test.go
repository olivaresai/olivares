// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptRunner launches one fixed script through the real native runner, so the
// pump and the bridge are the product's own; only the line limit is the test's.
type scriptRunner struct {
	program string
	lineCap int
}

func (r scriptRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	return (&procRunner{lineCap: r.lineCap}).Launch(ctx, LaunchSpec{Program: r.program, Dir: spec.Dir, Env: raceGuardEnv, WaitDelay: 2 * time.Second})
}

// toolResultLine is a stream-json tool_result line of exactly n bytes.
func toolResultLine(n int) string {
	head := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_big","content":"`
	tail := `"}]},"parent_tool_use_id":null,"session_id":"s","uuid":"u"}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// A tool that reads a big file writes one tool_result line of several MiB. Every line
// a provider could take passes whole, up to the limit; a line past it becomes one
// compact frame that names its size and hash; the session goes on, and the tool's own
// clean exit is the run's end.
func TestSessionOutlivesAnOutputLineOverTheLimit(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "exit")
	const lineCap = 8 << 20 // the product's limit (maxOutputLine) is too big to send twice here
	small, atLimit, over := toolResultLine(3<<20), toolResultLine(lineCap-1), toolResultLine(lineCap+1)
	result := `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s"}`
	frames := filepath.Join(dir, "frames.jsonl")
	if err := os.WriteFile(frames, []byte(small+"\n"+atLimit+"\n"+over+"\n"+result+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + frames + "'\nwhile [ ! -e '" + gate + "' ]; do sleep 0.05; done\nexit 0\n"
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(scriptRunner{writeOwnedChild(t, script), lineCap}), WithCredentialSource(staticCred()))
	m.rt.ringBytes = 4 * lineCap // this test reads back every frame it sent
	ref, _ := launchedRun(t, m, tenant)
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("the run is not live after its launch")
	}
	ended := func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateStopped || d.State == stateFailed
	}

	deadline := time.Now().Add(60 * time.Second)
	for !ringHolds(lr, `"type":"result"`) {
		if ended() {
			rec, _ := m.loadRun(context.Background(), tenant, ref)
			t.Fatalf("the session ended on a long line: state=%s, detail=%q", rec.String(colState), rec.String(colReason))
		}
		if time.Now().After(deadline) {
			t.Fatal("the frame after the long lines never reached the stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got []string
	for _, f := range lr.ring.readFrom(0).frames {
		if f.Stream == streamStdout {
			got = append(got, string(f.Data))
		}
	}
	sum := sha256.Sum256([]byte(over))
	compact := `{"type":"user","olivares_oversize":{"bytes":` +
		strconv.Itoa(len(over)) + `,"sha256":"` + hex.EncodeToString(sum[:]) + `"}}`
	if len(got) != 4 || got[0] != small || got[1] != atLimit || got[2] != compact || got[3] != result {
		sizes := make([]int, len(got))
		for i, g := range got {
			sizes[i] = len(g)
		}
		t.Fatalf("stdout frames of %v bytes; want %d and %d bytes whole, then %s, then the result", sizes, len(small), len(atLimit), compact)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for !ended() {
		if time.Now().After(deadline) {
			t.Fatal("the run did not end after the tool exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, _ := m.loadRun(context.Background(), tenant, ref)
	if detail := rec.String(colReason); strings.Contains(detail, "protocol output limit exceeded") || strings.Contains(detail, "output incomplete") ||
		rec.String(colState) != stateStopped || rec.Int(colExitCode) != 0 {
		t.Fatalf("run ended state=%s exit=%d detail=%q, want a clean stop", rec.String(colState), rec.Int(colExitCode), detail)
	}
}

func ringHolds(lr *liveRun, s string) bool {
	for _, f := range lr.ring.readFrom(0).frames {
		if strings.Contains(string(f.Data), s) {
			return true
		}
	}
	return false
}

// What the compact frame keeps: the small values that say what the record was, never
// its payload, and an error answer in place of a JSON-RPC answer, so the request that
// waits for it fails instead of reading an empty result as success.
func TestOversizeFrameKeepsWhatTheRecordWas(t *testing.T) {
	const limit = 1 << 10
	big := strings.Repeat("y", 2*limit)
	for name, tc := range map[string]struct{ line, want string }{
		"stream-json result": {`{"type":"result","subtype":"success","is_error":false,"result":"` + big + `"}`,
			`{"type":"result","subtype":"success","is_error":false,"olivares_oversize":`},
		"json-rpc answer": {`{"jsonrpc":"2.0","id":7,"result":{"text":"` + big + `"}}`,
			`{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"the answer was over the session output line limit and was left out"},"olivares_oversize":`},
	} {
		line, err := readBoundedLineWithOverflow(bufio.NewReaderSize(strings.NewReader(tc.line+"\r\n"+`{"type":"next"}`+"\n"), 16), limit, oversizeFrame)
		sum := sha256.Sum256([]byte(tc.line))
		want := tc.want + `{"bytes":` + strconv.Itoa(len(tc.line)) + `,"sha256":"` + hex.EncodeToString(sum[:]) + `"}}`
		if err != nil || string(line) != want || !json.Valid(line) {
			t.Fatalf("%s: frame = %s, %v; want %s", name, line, err, want)
		}
	}
}

// A handoff reads OpenCode's whole export: a line the pump left out is a record too
// large to hand over, never a document with a hole in it.
func TestOpenCodeExportRefusesALineThePumpLeftOut(t *testing.T) {
	p := &fakeProc{out: make(chan OutputFrame, 2), stopped: make(chan struct{})}
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"info":{"id":"s"},"messages":[`)}
	p.out <- OutputFrame{Stream: streamStdout, Data: oversizeFrame([]byte(`{"parts":[`), 2<<20, make([]byte, 32))}
	close(p.out)
	close(p.stopped)
	if _, err := collectStdout(t.Context(), p, openCodeExportMax); !errors.Is(err, errHandoffRecordTooLarge) {
		t.Fatalf("collectStdout = %v, want errHandoffRecordTooLarge", err)
	}
}
