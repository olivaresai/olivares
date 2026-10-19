// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// claudeInitializeAnswer is Claude Code's answer to the SDK `initialize` control
// request, as measured on Claude Code 2.1.296: commands (slash commands AND skills,
// each with the source in its text), agents, and a lot this feature never reads.
func claudeInitializeAnswer(id, commands, agents string) []byte {
	return []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"` + id +
		`","response":{"commands":` + commands + `,"agents":` + agents +
		`,"models":[{"value":"opus"}],"account":{"tokenSource":"none"}}}}`)
}

// claudeCommandsStub answers every `initialize` control request the engine writes
// to the fake child with the catalog current() returns, like Claude Code does for
// the live session. requests() counts the requests seen.
type claudeCommandsStub struct {
	mu   sync.Mutex
	seen int
	stop chan struct{}
	once sync.Once
}

// halt stops the stub: the tool goes silent from here on.
func (s *claudeCommandsStub) halt() { s.once.Do(func() { close(s.stop) }) }

func (s *claudeCommandsStub) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

func startClaudeCommandsStub(t *testing.T, p *fakeProc, current func() (commands, agents string)) *claudeCommandsStub {
	t.Helper()
	s := &claudeCommandsStub{stop: make(chan struct{})}
	t.Cleanup(s.halt)
	go func() {
		answered := 0
		for {
			select {
			case <-s.stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			p.mu.Lock()
			sent := append([][]byte(nil), p.sent...)
			p.mu.Unlock()
			for ; answered < len(sent); answered++ {
				var req struct {
					Type      string `json:"type"`
					RequestID string `json:"request_id"`
					Request   struct {
						Subtype string `json:"subtype"`
					} `json:"request"`
				}
				if json.Unmarshal(sent[answered], &req) != nil || req.Type != "control_request" || req.Request.Subtype != "initialize" {
					continue
				}
				s.mu.Lock()
				s.seen++
				s.mu.Unlock()
				commands, agents := current()
				p.mu.Lock()
				done := p.done
				p.mu.Unlock()
				if !done {
					select {
					case p.out <- OutputFrame{Stream: streamStdout, Data: claudeInitializeAnswer(req.RequestID, commands, agents)}:
					case <-s.stop:
						return
					}
				}
			}
		}
	}()
	return s
}

const (
	catalogCommands = `[
	  {"name":"compact","description":"Free up context by summarizing the conversation so far","argumentHint":"<optional custom summarization instructions>","builtin":true},
	  {"name":"user-skill","description":"Review my notes (user)","argumentHint":""},
	  {"name":"demo-cmd","description":"Demo project command (project)","argumentHint":"x"},
	  {"name":"demoplug:plug-skill","description":"(demoplug) Plugin skill","argumentHint":"","aliases":["plug-skill"]},
	  {"name":"odd one","description":"a name with a space is not a command","argumentHint":""},
	  {"name":"plain","description":"No source is stated","argumentHint":""}
	]`
	catalogAgents = `[{"name":"Explore","description":"Fast read-only search agent","model":"inherit"},{"name":"proj-agent","description":"Project agent"}]`
)

func commandByName(t *testing.T, got toolCommandsDTO, name string) toolCommand {
	t.Helper()
	for _, c := range got.Commands {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("catalog has no %q: %+v", name, got.Commands)
	return toolCommand{}
}

func startCommandsRun(t *testing.T, fr *fakeRunner) (*Module, context.Context, string, *liveRun, *fakeProc) {
	t.Helper()
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID != "" })
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	return m, ctx, dto.RunRef, lr, fr.lastProc()
}

// #1801: typing "/" lists what the TOOL reports. Claude Code
// reports it on the live session's stdin protocol; the engine asks and serves it.
func TestToolCommands_ListsWhatClaudeReportsWithItsSource(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	startClaudeCommandsStub(t, p, func() (string, string) { return catalogCommands, catalogAgents })

	got, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("runToolCommands: %v", err)
	}
	if !got.Reported || got.Tool != "claude" {
		t.Fatalf("catalog = reported %v tool %q, want reported claude", got.Reported, got.Tool)
	}
	for _, want := range []toolCommand{
		{Name: "compact", Description: "Free up context by summarizing the conversation so far", ArgumentHint: "<optional custom summarization instructions>", Source: "built-in", Kind: "command"},
		{Name: "user-skill", Description: "Review my notes", Source: "user", Kind: "command"},
		{Name: "demo-cmd", Description: "Demo project command", ArgumentHint: "x", Source: "project", Kind: "command"},
		{Name: "demoplug:plug-skill", Description: "Plugin skill", Source: "plugin", Kind: "command"},
		{Name: "plain", Description: "No source is stated", Source: "", Kind: "command"},
		{Name: "Explore", Description: "Fast read-only search agent", Source: "", Kind: "agent"},
		{Name: "proj-agent", Description: "Project agent", Source: "", Kind: "agent"},
	} {
		if have := commandByName(t, got, want.Name); have != want {
			t.Errorf("%s = %+v, want %+v", want.Name, have, want)
		}
	}
	for _, c := range got.Commands {
		if strings.ContainsAny(c.Name, " \t\n") {
			t.Errorf("a name the tool cannot type was served: %q", c.Name)
		}
	}
}

// The control response is the engine's own conversation with the tool: it carries
// the tool's account block and 58 KB of text, so it must not reach the attach
// stream or the recorded I/O evidence (minimal data).
func TestToolCommands_TheAnswerIsNotBridgedToTheAttachStream(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-ring"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	startClaudeCommandsStub(t, p, func() (string, string) { return catalogCommands, catalogAgents })
	if _, err := m.runToolCommands(ctx, lr.tenant, ref); err != nil {
		t.Fatalf("runToolCommands: %v", err)
	}
	for _, f := range lr.ring.readFrom(0).frames {
		if bytes.Contains(f.Data, []byte(commandsRequestPrefix)) || bytes.Contains(f.Data, []byte(`"commands"`)) {
			t.Fatalf("the engine's catalog answer reached the attach stream: %.120s", f.Data)
		}
	}
}

// A frame that merely looks like the answer is the child's output, not ours: only
// an id the engine issued and still waits for is withheld.
func TestToolCommands_AForgedAnswerIsPublishedNotSwallowed(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-forged"}
	_, _, _, lr, p := startCommandsRun(t, fr)
	forged := claudeInitializeAnswer(commandsRequestPrefix+"nobody-asked", "[]", "[]")
	p.out <- OutputFrame{Stream: streamStdout, Data: forged}
	waitFor(t, "the forged frame is bridged", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if bytes.Equal(f.Data, forged) {
				return true
			}
		}
		return false
	})
}

// Acceptance: a stub tool that reports a new command makes it appear without an
// Olivares release; and within the cache window the tool is not asked again.
func TestToolCommands_CachedPerSessionThenRefreshedFromTheTool(t *testing.T) {
	old := toolCommandsTTL
	toolCommandsTTL = time.Hour
	t.Cleanup(func() { toolCommandsTTL = old })
	fr := &fakeRunner{initSID: "sess-commands-cache"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	var mu sync.Mutex
	commands := `[{"name":"first","description":"one (project)"}]`
	stub := startClaudeCommandsStub(t, p, func() (string, string) { mu.Lock(); defer mu.Unlock(); return commands, "[]" })

	first, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	mu.Lock()
	commands = `[{"name":"first","description":"one (project)"},{"name":"brand-new","description":"added to the project folder (project)"}]`
	mu.Unlock()
	cached, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("cached read: %v", err)
	}
	if stub.requests() != 1 || len(cached.Commands) != len(first.Commands) {
		t.Fatalf("inside the cache window the tool was asked %d times and served %d commands, want 1 and %d",
			stub.requests(), len(cached.Commands), len(first.Commands))
	}

	toolCommandsTTL = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	fresh, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("fresh read: %v", err)
	}
	if c := commandByName(t, fresh, "brand-new"); c.Source != "project" || c.Description != "added to the project folder" {
		t.Fatalf("new command = %+v, want the project skill the tool reported after the TTL", c)
	}
}

// The tool says when its catalog changed (Claude Code sends system/commands_changed when
// a skill or command appears in a folder it watches): the next read asks again, inside
// the cache window too. Any other system frame leaves the cache alone.
func TestToolCommands_ACommandsChangedFrameMakesTheNextReadAskAgain(t *testing.T) {
	old := toolCommandsTTL
	toolCommandsTTL = time.Hour
	t.Cleanup(func() { toolCommandsTTL = old })
	fr := &fakeRunner{initSID: "sess-commands-changed"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	var mu sync.Mutex
	commands := `[{"name":"first","description":"one (project)"}]`
	stub := startClaudeCommandsStub(t, p, func() (string, string) { mu.Lock(); defer mu.Unlock(); return commands, "[]" })
	if _, err := m.runToolCommands(ctx, lr.tenant, ref); err != nil {
		t.Fatalf("first read: %v", err)
	}
	mu.Lock()
	commands = `[{"name":"first","description":"one (project)"},{"name":"brand-new","description":"two (project)"}]`
	mu.Unlock()

	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"status","permissionMode":"default"}`)}
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"assistant","message":"the word commands_changed in a turn is not the event"}`)}
	// It passes the bridge's cheap byte filter, and is still not the event.
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"user","subtype":"commands_changed"}`)}
	// Frames are bridged in order: once this one is in the ring the two before it were handled.
	marker := []byte(`{"type":"system","subtype":"marker"}`)
	p.out <- OutputFrame{Stream: streamStdout, Data: marker}
	waitFor(t, "the frames before the marker are handled", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if bytes.Equal(f.Data, marker) {
				return true
			}
		}
		return false
	})
	if got, err := m.runToolCommands(ctx, lr.tenant, ref); err != nil || len(got.Commands) != 1 || stub.requests() != 1 {
		t.Fatalf("unrelated frames: %d commands after %d requests (err %v), want the cache kept: 1 and 1", len(got.Commands), stub.requests(), err)
	}

	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"commands_changed","commands":[]}`)}
	waitFor(t, "the change is noted", func() bool { return lr.commands.dirty.Load() })
	got, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("read after the change: %v", err)
	}
	if c := commandByName(t, got, "brand-new"); c.Source != "project" {
		t.Fatalf("brand-new = %+v", c)
	}
	if stub.requests() != 2 {
		t.Fatalf("the tool was asked %d times, want 2 (once, and once after it said the catalog changed)", stub.requests())
	}
	if lr.commands.dirty.Load() {
		t.Fatal("the change is still pending after the tool was asked")
	}
}

// A tool that does not answer is said, not guessed: with nothing cached the read
// fails with a gateway timeout, and its late answer is still withheld. Once it has
// reported, a later silence serves the last catalog it did report, marked stale, and
// asks again at the next read, which recovers.
func TestToolCommands_NoAnswerIsSaidAndTheLastCatalogIsKeptAsStale(t *testing.T) {
	oldWait, oldTTL, oldRetry := toolCommandsWait, toolCommandsTTL, toolCommandsRetry
	toolCommandsWait, toolCommandsTTL, toolCommandsRetry = 200*time.Millisecond, time.Hour, 0
	t.Cleanup(func() { toolCommandsWait, toolCommandsTTL, toolCommandsRetry = oldWait, oldTTL, oldRetry })
	fr := &fakeRunner{initSID: "sess-commands-silent"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)

	if _, err := m.runToolCommands(ctx, lr.tenant, ref); statusOf(err) != http.StatusGatewayTimeout {
		t.Fatalf("unanswered read with nothing cached = %v, want 504", err)
	}
	if waiting, abandoned := lr.commandWaitCount(); waiting != 0 || abandoned != 1 {
		t.Fatalf("after the unanswered read %d waiters and %d abandoned ids, want 0 and 1", waiting, abandoned)
	}

	var mu sync.Mutex
	answer := true
	startClaudeCommandsStub(t, p, func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		if !answer {
			return "", "" // not a catalog: no commands key
		}
		return `[{"name":"kept","description":"stays (user)"}]`, "[]"
	})
	first, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil || first.Stale {
		t.Fatalf("answered read = %+v, %v, want a fresh catalog", first, err)
	}

	// The tool says its catalog changed, then stops giving a catalog.
	mu.Lock()
	answer = false
	mu.Unlock()
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"commands_changed","commands":[]}`)}
	waitFor(t, "the change is noted", func() bool { return lr.commands.dirty.Load() })
	got, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if c := commandByName(t, got, "kept"); c.Source != "user" || !got.Stale || !got.Reported {
		t.Fatalf("stale read = %+v (kept %+v), want the last catalog marked stale", got, c)
	}

	// It recovers; the cache window is an hour, so only the failed ask can make this read ask again.
	mu.Lock()
	answer = true
	mu.Unlock()
	got, err = m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil || got.Stale {
		t.Fatalf("recovered read = %+v, %v, want fresh and not stale", got, err)
	}
}

// A callback that outlives its caller: the tool answers after the request left (a closed
// menu, a slow tool). The engine's own conversation still does not reach the attach
// stream or the recorded I/O.
func TestToolCommands_ALateAnswerIsStillWithheld(t *testing.T) {
	oldWait, oldRetry := toolCommandsWait, toolCommandsRetry
	toolCommandsWait, toolCommandsRetry = 100*time.Millisecond, 0
	t.Cleanup(func() { toolCommandsWait, toolCommandsRetry = oldWait, oldRetry })
	fr := &workControlRunner{}
	recorder := &capturingRecorder{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithRecorder(recorder), WithLaunchGate(&recordingLaunchGate{dec: LaunchDecision{Allowed: true, RecordIO: true}}))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	ref := dto.RunRef
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("the run is not live")
	}
	p := fr.lastProc(t)
	if _, err := m.runToolCommands(ctx, lr.tenant, ref); statusOf(err) != http.StatusGatewayTimeout {
		t.Fatalf("unanswered read = %v, want 504", err)
	}
	// And a caller that gives up while the tool has not answered.
	leaving, leave := context.WithCancel(ctx)
	t.Cleanup(leave)
	// Only the first read exercises the answer timeout. Keep it out of the
	// cancellation case, even if scheduling pauses after the write.
	toolCommandsWait = time.Hour
	p.setAfterSend(leave)
	if _, err := m.runToolCommands(leaving, lr.tenant, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller that left = %v, want context.Canceled", err)
	}

	var ids []string
	p.fakeProc.mu.Lock()
	for _, line := range p.sent {
		var req struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(line, &req) == nil && strings.HasPrefix(req.RequestID, commandsRequestPrefix) {
			ids = append(ids, req.RequestID)
		}
	}
	p.fakeProc.mu.Unlock()
	if len(ids) != 2 {
		t.Fatalf("%d requests reached the child, want 2: the timed-out one and the one whose caller left after the write", len(ids))
	}
	if waiting, abandoned := lr.commandWaitCount(); waiting != 0 || abandoned != 2 {
		t.Fatalf("before the late answers %d waiters and %d abandoned ids, want 0 and 2", waiting, abandoned)
	}
	for _, id := range ids {
		p.out <- OutputFrame{Stream: streamStdout, Data: claudeInitializeAnswer(id, catalogCommands, catalogAgents)}
	}
	marker := []byte(`{"type":"system","subtype":"marker"}`)
	p.out <- OutputFrame{Stream: streamStdout, Data: marker}
	waitFor(t, "the frames before the marker are handled", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if bytes.Equal(f.Data, marker) {
				return true
			}
		}
		return false
	})
	for _, f := range lr.ring.readFrom(0).frames {
		if bytes.Contains(f.Data, []byte(commandsRequestPrefix)) {
			t.Fatalf("a late answer reached the attach stream: %.100s", f.Data)
		}
	}
	// The marker is recorded after both answers have passed through the bridge.
	waitFor(t, "the marker is recorded", func() bool {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		for _, f := range recorder.frames {
			if bytes.Equal(f.Data, marker) {
				return true
			}
		}
		return false
	})
	recorder.mu.Lock()
	frames := append([]RecordedFrame(nil), recorder.frames...)
	recorder.mu.Unlock()
	for _, f := range frames {
		if bytes.Contains(f.Data, []byte(commandsRequestPrefix)) || bytes.Contains(f.Data, []byte(`"commands"`)) {
			t.Fatalf("a late answer reached recorded I/O: %.100s", f.Data)
		}
	}
	if waiting, abandoned := lr.commandWaitCount(); waiting != 0 || abandoned != 0 {
		t.Fatalf("%d waiters and %d abandoned ids are left after the late answers", waiting, abandoned)
	}
}

// A tool that never answers costs one wait, not one per caller: the callers behind it
// get its failure at once, and the tool is not written to again inside the window.
func TestToolCommands_AFailedAskIsNotRepeatedForEveryCaller(t *testing.T) {
	oldWait, oldTTL, oldRetry := toolCommandsWait, toolCommandsTTL, toolCommandsRetry
	toolCommandsWait, toolCommandsTTL, toolCommandsRetry = 300*time.Millisecond, time.Millisecond, time.Hour
	t.Cleanup(func() { toolCommandsWait, toolCommandsTTL, toolCommandsRetry = oldWait, oldTTL, oldRetry })
	fr := &fakeRunner{initSID: "sess-commands-backoff"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)

	var wg sync.WaitGroup
	statuses := make([]int, 6)
	begin := time.Now()
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.runToolCommands(ctx, lr.tenant, ref)
			statuses[i] = statusOf(err)
		}()
	}
	wg.Wait()
	for i, s := range statuses {
		if s != http.StatusGatewayTimeout {
			t.Errorf("caller %d = %d, want 504", i, s)
		}
	}
	if elapsed := time.Since(begin); elapsed > 3*toolCommandsWait {
		t.Fatalf("six callers took %v behind a silent tool, want about one wait (%v)", elapsed, toolCommandsWait)
	}
	p.mu.Lock()
	asked := 0
	for _, line := range p.sent {
		if bytes.Contains(line, []byte(commandsRequestPrefix)) {
			asked++
		}
	}
	p.mu.Unlock()
	if asked != 1 {
		t.Fatalf("the silent tool was written to %d times, want 1", asked)
	}
}

// Many menus opening at once ask the tool once.
func TestToolCommands_ConcurrentReadsAskTheToolOnce(t *testing.T) {
	old := toolCommandsTTL
	toolCommandsTTL = time.Hour
	t.Cleanup(func() { toolCommandsTTL = old })
	fr := &fakeRunner{initSID: "sess-commands-concurrent"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	stub := startClaudeCommandsStub(t, p, func() (string, string) { return catalogCommands, catalogAgents })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := m.runToolCommands(ctx, lr.tenant, ref); err != nil || len(got.Commands) == 0 {
				t.Errorf("concurrent read = %d commands, %v", len(got.Commands), err)
			}
		}()
	}
	wg.Wait()
	if stub.requests() != 1 {
		t.Fatalf("the tool was asked %d times by 8 concurrent reads, want 1", stub.requests())
	}
}

// ctxAwareProc refuses a write for a context that is already over, as the real process
// runner does (procrunner.go Send): nothing reaches the child.
type ctxAwareProc struct{ *fakeProc }

func (p ctxAwareProc) Send(ctx context.Context, line []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.fakeProc.Send(ctx, line)
}

// A caller that is already gone does not take the request back: the write belongs to the
// engine's own wait (a write cancelled halfway would break the session's stdin for good),
// and the request is kept so that its answer is still withheld.
func TestToolCommands_ACancelledCallerStillWritesTheRequest(t *testing.T) {
	fp := &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{})}
	lr := &liveRun{proc: ctxAwareProc{fp}}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Module{}).askClaudeCommands(gone, lr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ask for a caller that is gone = %v, want context.Canceled", err)
	}
	fp.mu.Lock()
	written := len(fp.sent)
	fp.mu.Unlock()
	if waiting, abandoned := lr.commandWaitCount(); written != 1 || waiting != 0 || abandoned != 1 {
		t.Fatalf("%d requests written, %d waiters, %d abandoned; want 1, 0, 1", written, waiting, abandoned)
	}
}

// A request that cannot be written is a bad gateway. Nobody waits for it, but its id is
// kept: a write that failed at its deadline may still have reached the child.
func TestToolCommands_AClosedStdinIsABadGateway(t *testing.T) {
	proc := &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{}), done: true}
	lr := &liveRun{proc: proc}
	_, err := (&Module{}).askClaudeCommands(context.Background(), lr)
	if statusOf(err) != http.StatusBadGateway {
		t.Fatalf("ask on a closed stdin = %v, want 502", err)
	}
	if waiting, abandoned := lr.commandWaitCount(); waiting != 0 || abandoned != 1 {
		t.Fatalf("%d waiters and %d kept ids after a failed write, want 0 and 1", waiting, abandoned)
	}
}

// The ids kept to withhold late answers are bounded, and the bound refuses to ask again:
// an id the engine issued must never lose its cover, because its answer carries the tool's
// account block and tens of kilobytes that belong neither on the attach stream nor in the
// recorded I/O. A tool that answers none of its requests is not written to any more.
func TestToolCommands_ATiresomeToolIsNotAskedAgainAndEveryAnswerIsWithheld(t *testing.T) {
	old := toolCommandsWait
	toolCommandsWait = 5 * time.Millisecond
	t.Cleanup(func() { toolCommandsWait = old })
	fp := &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{})}
	lr := &liveRun{proc: fp}
	m := &Module{}
	for i := 0; i < maxAbandonedCommandRequests; i++ {
		if _, err := m.askClaudeCommands(context.Background(), lr); statusOf(err) != http.StatusGatewayTimeout {
			t.Fatalf("ask %d = %v, want 504", i, err)
		}
	}
	fp.mu.Lock()
	written := len(fp.sent)
	fp.mu.Unlock()
	if _, err := m.askClaudeCommands(context.Background(), lr); statusOf(err) != http.StatusGatewayTimeout {
		t.Fatalf("ask past the bound = %v, want 504 without a write", err)
	}
	fp.mu.Lock()
	after := len(fp.sent)
	var ids []string
	for _, line := range fp.sent {
		var req struct {
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(line, &req)
		ids = append(ids, req.RequestID)
	}
	fp.mu.Unlock()
	if after != written {
		t.Fatalf("%d requests written, then %d: a tool that left %d unanswered was written to again", written, after, maxAbandonedCommandRequests)
	}
	if waiting, kept := lr.commandWaitCount(); waiting != 0 || kept != maxAbandonedCommandRequests {
		t.Fatalf("%d waiters and %d kept ids, want 0 and the bound %d", waiting, kept, maxAbandonedCommandRequests)
	}
	for _, id := range ids {
		if !lr.takeCommandsResponse(claudeInitializeAnswer(id, "[]", "[]")) {
			t.Fatalf("the late answer of %s was not withheld: it would reach the ring and the recorder", id)
		}
	}
	// Once the tool answers, it is asked again.
	if waiting, kept := lr.commandWaitCount(); waiting != 0 || kept != 0 {
		t.Fatalf("%d waiters and %d kept ids after every answer, want 0 and 0", waiting, kept)
	}
}

// A caller that leaves is not the tool's failure: the catalog is not marked stale and the
// next read still asks.
func TestToolCommands_ACallerThatLeavesIsNotTheToolsFailure(t *testing.T) {
	oldWait, oldTTL, oldRetry := toolCommandsWait, toolCommandsTTL, toolCommandsRetry
	toolCommandsWait, toolCommandsTTL, toolCommandsRetry = time.Second, time.Hour, time.Hour
	t.Cleanup(func() { toolCommandsWait, toolCommandsTTL, toolCommandsRetry = oldWait, oldTTL, oldRetry })
	fr := &fakeRunner{initSID: "sess-commands-leaving"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	stub := startClaudeCommandsStub(t, p, func() (string, string) { return `[{"name":"held","description":"x (user)"}]`, "[]" })
	if _, err := m.runToolCommands(ctx, lr.tenant, ref); err != nil {
		t.Fatalf("first read: %v", err)
	}
	stub.halt() // the tool goes quiet
	lr.commands.dirty.Store(true)
	leaving, leave := context.WithCancel(ctx)
	time.AfterFunc(30*time.Millisecond, leave)
	if _, err := m.runToolCommands(leaving, lr.tenant, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller that left = %v, want context.Canceled", err)
	}
	if lr.commands.stale || !lr.commands.dirty.Load() || lr.commands.failure != nil {
		t.Fatalf("a caller leaving marked the catalog stale=%v dirty=%v failure=%v; want fresh state and still dirty",
			lr.commands.stale, lr.commands.dirty.Load(), lr.commands.failure)
	}
}

// A caller queued behind a slow read can leave at once; it does not wait for the read.
func TestToolCommands_AQueuedCallerCanLeave(t *testing.T) {
	oldWait, oldRetry := toolCommandsWait, toolCommandsRetry
	toolCommandsWait, toolCommandsRetry = 2*time.Second, 0
	t.Cleanup(func() { toolCommandsWait, toolCommandsRetry = oldWait, oldRetry })
	fr := &fakeRunner{initSID: "sess-commands-queued"}
	m, ctx, ref, lr, _ := startCommandsRun(t, fr) // a silent tool
	first := make(chan error, 1)
	go func() { _, err := m.runToolCommands(ctx, lr.tenant, ref); first <- err }()
	waitFor(t, "the first read is waiting for the tool", func() bool { w, _ := lr.commandWaitCount(); return w == 1 })

	leaving, leave := context.WithCancel(ctx)
	time.AfterFunc(30*time.Millisecond, leave)
	begin := time.Now()
	if _, err := m.runToolCommands(leaving, lr.tenant, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("a queued caller that left = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("the queued caller took %v to leave, want about 30ms: it waited for the read in front of it", elapsed)
	}
	if err := <-first; statusOf(err) != http.StatusGatewayTimeout {
		t.Fatalf("the first read = %v, want its own 504", err)
	}
}

// Claude Code answering "error" is not a catalog either.
func TestToolCommands_ARefusedInitializeIsABadGateway(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-refused"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	go func() {
		for i := 0; i < 400; i++ {
			p.mu.Lock()
			sent := append([][]byte(nil), p.sent...)
			p.mu.Unlock()
			for _, line := range sent {
				var req struct {
					RequestID string `json:"request_id"`
					Request   struct {
						Subtype string `json:"subtype"`
					} `json:"request"`
				}
				if json.Unmarshal(line, &req) == nil && req.Request.Subtype == "initialize" {
					p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"control_response","response":{"subtype":"error","request_id":"` + req.RequestID + `","error":"not ready"}}`)}
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if _, err := m.runToolCommands(ctx, lr.tenant, ref); statusOf(err) != http.StatusBadGateway {
		t.Fatalf("refused initialize = %v, want 502", err)
	}
}

// A session that is not live on this node has no tool to ask: the answer is "the
// tool reported nothing", which the console says in one line.
func TestToolCommands_ASessionThatIsNotLiveReportsNothing(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-ended"}
	m, ctx, ref, lr, _ := startCommandsRun(t, fr)
	m.rt.dropLive(lr.tenant, ref)
	got, err := m.runToolCommands(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("runToolCommands: %v", err)
	}
	if got.Reported || len(got.Commands) != 0 || got.Commands == nil {
		t.Fatalf("catalog = %+v, want reported=false and an empty (non-null) list", got)
	}
}

func TestParseClaudeCommands_BoundsAndShapes(t *testing.T) {
	t.Run("no commands key is not a catalog", func(t *testing.T) {
		if _, _, err := parseClaudeCommands([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"x","response":{"models":[]}}}`)); err == nil {
			t.Fatal("a response without commands parsed as a catalog")
		}
	})
	t.Run("entries beyond the bound are dropped, not the whole catalog", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < maxToolCommands+50; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"c-` + strconv.Itoa(i) + `","description":"d"}`)
		}
		b.WriteString("]")
		got, dropped, err := parseClaudeCommands(claudeInitializeAnswer("x", b.String(), "[]"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(got) != maxToolCommands || dropped != 50 {
			t.Fatalf("kept %d commands and dropped %d, want the bound %d and 50 counted", len(got), dropped, maxToolCommands)
		}
	})
	t.Run("text is clipped to a line of visible characters", func(t *testing.T) {
		long := strings.Repeat("\u00e9", 5000)
		got, dropped, err := parseClaudeCommands(claudeInitializeAnswer("x", `[{"name":"a","description":"`+long+`\u0007 (project)","argumentHint":"`+long+`"}]`, "[]"))
		if err != nil || len(got) != 1 || dropped != 0 {
			t.Fatalf("parse = %v, %d, %v", got, dropped, err)
		}
		want := strings.Repeat("\u00e9", maxToolCommandText) + "…"
		if got[0].Description != want || got[0].ArgumentHint != want {
			t.Fatalf("description %d runes, hint %d runes, want both clipped to %d plus the ellipsis",
				len([]rune(got[0].Description)), len([]rune(got[0].ArgumentHint)), maxToolCommandText)
		}
		if got[0].Source != "project" {
			t.Fatalf("source = %q, want project: it is read from the tool's text before that text is clipped", got[0].Source)
		}
	})
	t.Run("control and invisible format characters never reach the console", func(t *testing.T) {
		got, _, err := parseClaudeCommands(claudeInitializeAnswer("x",
			`[{"name":"safe","description":"Run\u202e this\u200b now\u0007 (user)"},{"name":"bi\u202ddi","description":"hidden joiner in the name"}]`, "[]"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(got) != 1 || got[0].Name != "safe" || got[0].Description != "Run this now" || got[0].Source != "user" {
			t.Fatalf("catalog = %+v, want one entry \"safe\", \"Run this now\", user: the name with a format character is dropped", got)
		}
	})
	t.Run("one entry that is not a command does not cost the others", func(t *testing.T) {
		got, dropped, err := parseClaudeCommands(claudeInitializeAnswer("x",
			`[{"name":"fine","description":"ok"},{"name":"odd","description":"x","argumentHint":["not","a","string"]},"not an object",{"name":"also-fine","description":"ok"}]`,
			`[{"name":"agent-a","description":"ok"},{"name":7}]`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		names := []string{}
		for _, c := range got {
			names = append(names, c.Kind+":"+c.Name)
		}
		if strings.Join(names, ",") != "command:fine,command:also-fine,agent:agent-a" || dropped != 3 {
			t.Fatalf("kept %v and dropped %d, want the three good entries and 3 dropped", names, dropped)
		}
	})
	t.Run("the tool's refusal text is clipped like any other cause", func(t *testing.T) {
		_, _, err := parseClaudeCommands([]byte(`{"type":"control_response","response":{"subtype":"error","request_id":"x","error":"` + strings.Repeat("e", 5000) + `"}}`))
		if err == nil || len(err.Error()) > 1000 {
			t.Fatalf("error = %.80v (%d bytes), want a clipped cause", err, len(fmt.Sprint(err)))
		}
	})
}

// #1801: asking writes the tool's stdin, so it is fenced like an interrupt. A launch whose
// durable Claim moved to a successor is not asked, and nothing crosses to its child.
func TestToolCommands_ALaunchWhoseClaimMovedIsNotAsked(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-fence"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	stub := startClaudeCommandsStub(t, p, func() (string, string) { return catalogCommands, catalogAgents })
	reapOwnedChild(t, lr)
	takeOverClaim(t, m, lr.tenant, lr, "commands-successor")

	got, err := m.runToolCommands(ctx, lr.tenant, ref)
	if status := statusOf(err); status != http.StatusForbidden && status != http.StatusConflict {
		t.Fatalf("read after the claim moved = %+v, %v; want a 403 or 409 refusal", got, err)
	}
	if got.Reported || len(got.Commands) != 0 {
		t.Fatalf("a launch that lost its authority served a catalog: %+v", got)
	}
	p.mu.Lock()
	var initialize int
	for _, line := range p.sent {
		if bytes.Contains(line, []byte(`"subtype":"initialize"`)) {
			initialize++
		}
	}
	p.mu.Unlock()
	if initialize != 0 || stub.requests() != 0 {
		t.Fatalf("%d initialize requests crossed to the child of a launch that lost the claim", initialize)
	}
}

// A catalog whose every entry was dropped is not "the tool lists nothing": the shape is not
// the one this reads, and saying "reported" would replace a good catalog with an empty one.
func TestParseClaudeCommands_AnAnswerWithNoUsableEntryIsNotACatalog(t *testing.T) {
	_, dropped, err := parseClaudeCommands(claudeInitializeAnswer("x", `[{"nombre":"a"},{"name":"two words"},"text"]`, "[]"))
	if err == nil || statusOf(err) != http.StatusBadGateway {
		t.Fatalf("parse = dropped %d, %v; want a 502 error", dropped, err)
	}
	// An honest empty list stays a catalog.
	items, dropped, err := parseClaudeCommands(claudeInitializeAnswer("x", "[]", "[]"))
	if err != nil || len(items) != 0 || dropped != 0 {
		t.Fatalf("empty list = %v, %d, %v; want a reported empty catalog", items, dropped, err)
	}
}

// A session that is stopping, or whose row says it is not running, has no tool to ask:
// nothing is written to a stdin the runtime is closing, and nothing old is served.
func TestToolCommands_ASessionThatIsStoppingOrNotRunningIsNotWritten(t *testing.T) {
	written := func(p *fakeProc) int {
		p.mu.Lock()
		defer p.mu.Unlock()
		n := 0
		for _, line := range p.sent {
			if bytes.Contains(line, []byte(`"subtype":"initialize"`)) {
				n++
			}
		}
		return n
	}
	t.Run("a stop is in progress", func(t *testing.T) {
		fr := &fakeRunner{initSID: "sess-commands-stopping"}
		m, ctx, ref, lr, p := startCommandsRun(t, fr)
		lr.mu.Lock()
		lr.stopRequested = true
		lr.mu.Unlock()
		got, err := m.runToolCommands(ctx, lr.tenant, ref)
		if err != nil || got.Reported || len(got.Commands) != 0 || written(p) != 0 {
			t.Fatalf("read while stopping = %+v, %v after %d initialize request(s); want reported=false and nothing written", got, err, written(p))
		}
	})
	t.Run("the row is not running", func(t *testing.T) {
		fr := &fakeRunner{initSID: "sess-commands-notrunning"}
		m, ctx, ref, lr, p := startCommandsRun(t, fr)
		if err := mutateRunForWorkTest(m, lr.tenant, ref, func(row model.Record) { row[colState] = stateStopped }); err != nil {
			t.Fatalf("mark the run: %v", err)
		}
		got, err := m.runToolCommands(ctx, lr.tenant, ref)
		if err != nil || got.Reported || len(got.Commands) != 0 || written(p) != 0 {
			t.Fatalf("read of a stopped row = %+v, %v after %d initialize request(s); want reported=false and nothing written", got, err, written(p))
		}
	})
}

// The turn to ask can wait behind another read, and the session may be stopping when it
// comes: the state is read again then, and nothing is written.
func TestToolCommands_AStopRequestedWhileTheReadWaitedIsNotWritten(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-stop-while-queued"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr)
	rec, err := m.loadRun(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("loadRun: %v", err)
	}
	lr.mu.Lock()
	lr.stopRequested = true
	lr.mu.Unlock()
	if err := m.refreshToolCommands(ctx, lr, rec, &lr.commands); !errors.Is(err, errToolEnded) {
		t.Fatalf("refresh of a stopping session = %v, want errToolEnded", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range p.sent {
		if bytes.Contains(line, []byte(`"subtype":"initialize"`)) {
			t.Fatal("an initialize request was written to a session that was stopping")
		}
	}
}

// A tool that left its bound of requests unanswered is not written to, and is not worth
// the authority transaction either: the bound answers before the Claim is read.
func TestToolCommands_TheBoundAnswersBeforeTheAuthorityTransaction(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-commands-bound-first"}
	m, ctx, ref, lr, _ := startCommandsRun(t, fr)
	rec, err := m.loadRun(ctx, lr.tenant, ref)
	if err != nil {
		t.Fatalf("loadRun: %v", err)
	}
	takeOverClaim(t, m, lr.tenant, lr, "bound-successor") // the guard would refuse with 403
	lr.commands.waitMu.Lock()
	lr.commands.waits = map[string]chan []byte{}
	for i := 0; i < maxAbandonedCommandRequests; i++ {
		lr.commands.waits["olv-commands-"+strconv.Itoa(i)] = nil
	}
	lr.commands.waitMu.Unlock()
	err = m.refreshToolCommands(ctx, lr, rec, &lr.commands)
	if statusOf(err) != http.StatusGatewayTimeout {
		t.Fatalf("refresh at the bound = %v, want 504 and not the authority's 403", err)
	}
}

// The tool says its catalog changed while the engine asks; the caller then leaves. The
// notice is not undone by the caller's departure: the next read asks again.
func TestToolCommands_ACommandsChangedDuringAnAskSurvivesTheCallerLeaving(t *testing.T) {
	oldWait, oldTTL := toolCommandsWait, toolCommandsTTL
	toolCommandsWait, toolCommandsTTL = 5*time.Second, time.Hour
	t.Cleanup(func() { toolCommandsWait, toolCommandsTTL = oldWait, oldTTL })
	fr := &fakeRunner{initSID: "sess-commands-changed-leaving"}
	m, ctx, ref, lr, p := startCommandsRun(t, fr) // a silent tool
	leaving, leave := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := m.runToolCommands(leaving, lr.tenant, ref); done <- err }()
	waitFor(t, "the read waits for the tool", func() bool { w, _ := lr.commandWaitCount(); return w == 1 })
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"commands_changed","commands":[]}`)}
	waitFor(t, "the engine noted the change", func() bool { return lr.commands.dirty.Load() })
	leave()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller that left = %v, want context.Canceled", err)
	}
	if !lr.commands.dirty.Load() {
		t.Fatal("the tool's change notice was cleared by a caller that left: the next read trusts an out-of-date catalog")
	}
}
