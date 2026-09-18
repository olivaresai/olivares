// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// This file drives the whole driver contract — launch, attach, input, output,
// reconnect, stop with exit status, resume — against the REAL official CLI
// process on this host. The fake and the fixture peer prove the contract's
// shape; only this file proves that the vendor programs answer it.
//
// ⛔ EVERY HANDSHAKE HERE IS A PROTOCOL HANDSHAKE, SO NO MODEL TURN CAN BE
// SPENT. Claude Code answers a `control_request`, the Codex app-server and the
// Grok stdio agent answer a JSON-RPC `initialize`; none of the three reaches a
// model. The one leg that writes a conversation turn (the resume test, which
// needs the conversation the CLI itself nominates) runs ONLY after the CLI has
// reported, in that handshake, that it holds no credential — and it then proves
// the turn cost nothing.
//
// The launches are isolated besides: each gets a fresh HOME and a fresh vendor
// configuration home, and sanitizedEnv withholds every ANTHROPIC_*,
// CLAUDE_CODE_*, OPENAI_*, CODEX_*, GROK_* and XAI_* host variable
// (forbiddenInheritedEnvName), so the child has no credential to spend.

// liveCLI is one official CLI and the owned handshake that makes it answer.
type liveCLI struct {
	kind string
	// program is the vendor executable resolved on PATH.
	program string
	// handshake builds one input line the CLI answers without a model turn. n
	// distinguishes one handshake from the next on the same child.
	handshake func(n int) string
	// answers reports whether a frame is the answer to handshake(n).
	answers func(n int, data []byte) bool
}

func liveCLIs(t *testing.T) []liveCLI {
	t.Helper()
	all := []liveCLI{
		{
			kind: cliruntime.KindClaude,
			handshake: func(n int) string {
				return fmt.Sprintf(`{"type":"control_request","request_id":"olivares-%d","request":{"subtype":"initialize"}}`, n)
			},
			answers: func(n int, data []byte) bool {
				return strings.Contains(string(data), `"type":"control_response"`) &&
					strings.Contains(string(data), fmt.Sprintf(`"request_id":"olivares-%d"`, n))
			},
		},
		{
			kind: cliruntime.KindCodex,
			handshake: func(n int) string {
				return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"initialize","params":{"clientInfo":{"name":"olivares","title":"Olivares AI","version":"0"}}}`, n)
			},
			answers: answersJSONRPC,
		},
		{
			kind: cliruntime.KindGrok,
			handshake: func(n int) string {
				return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}`, n)
			},
			answers: answersJSONRPC,
		},
	}
	out := make([]liveCLI, 0, len(all))
	for _, c := range all {
		path, err := exec.LookPath(cliruntime.OfficialProgram(c.kind))
		if err != nil {
			t.Logf("LIVE %s: %s is not on PATH (%v); the fake and fixture-peer conformance still ran", c.kind, cliruntime.OfficialProgram(c.kind), err)
			continue
		}
		c.program = path
		out = append(out, c)
	}
	return out
}

// answersJSONRPC recognizes a JSON-RPC answer to request n, whether the CLI
// answers with a result or with a named error. Both are answers; neither is a
// hang.
func answersJSONRPC(n int, data []byte) bool {
	var obj struct {
		ID     *json.RawMessage `json:"id"`
		Result *json.RawMessage `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &obj) != nil || obj.ID == nil {
		return false
	}
	if string(*obj.ID) != fmt.Sprint(n) {
		return false
	}
	return obj.Result != nil || obj.Error != nil
}

// liveRequest is the isolated launch context. Fresh homes are what make a
// credential — and therefore a model turn — impossible.
func liveRequest(t *testing.T) cliruntime.LaunchRequest {
	t.Helper()
	return cliruntime.LaunchRequest{
		WorkDir:    t.TempDir(),
		UserHome:   t.TempDir(),
		ConfigHome: t.TempDir(),
	}
}

// liveLaunch launches on runCtx, which is the PER-RUN context the runner
// documents: it must outlive the request that started the child, so it is never
// a bounded request context.
func liveLaunch(t *testing.T, runCtx context.Context, c liveCLI, req cliruntime.LaunchRequest) (cliruntime.Resumer, cliruntime.Session) {
	t.Helper()
	d, err := NewOfficialLocal(c.kind, c.program)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := d.(cliruntime.Resumer)
	if !ok {
		t.Fatalf("live %s driver does not implement Resumer", c.kind)
	}
	sess, err := r.Launch(runCtx, req)
	if err != nil {
		t.Fatalf("launch live %s: %v", c.kind, err)
	}
	if sess.PID() <= 0 {
		t.Fatalf("live %s pid = %d", c.kind, sess.PID())
	}
	t.Logf("LIVE %s: launched %s pid=%d ref=%s gen=%d on the %s transport",
		c.kind, c.program, sess.PID(), sess.Ref(), sess.Generation(), cliruntime.LaunchTransport(c.kind))
	return r, sess
}

// TestOfficialCLI_LiveLaunchInputOutputStop launches each official CLI on PATH,
// attaches BEFORE any input, writes the handshake, reads the answer, and stops
// with an observed exit status.
func TestOfficialCLI_LiveLaunchInputOutputStop(t *testing.T) {
	for _, c := range liveCLIs(t) {
		t.Run(c.kind, func(t *testing.T) {
			runCtx, endRun := context.WithCancel(context.Background())
			defer endRun()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, sess := liveLaunch(t, runCtx, c, liveRequest(t))
			defer func() { _, _ = sess.Stop(context.Background()) }()

			// Attach before the first byte of input: a subscriber that was
			// already there must receive everything the child says.
			frames, unsub := sess.Attach(1)
			defer unsub()

			if err := sess.Send(ctx, []byte(c.handshake(1))); err != nil {
				t.Fatalf("live %s send: %v", c.kind, err)
			}
			answer, ok := liveWaitFrame(frames, 45*time.Second, func(data []byte) bool {
				return c.answers(1, data)
			})
			if !ok {
				t.Fatalf("live %s gave no answer to its handshake in 45s (a named refusal is a pass; a hang is not)", c.kind)
			}
			t.Logf("LIVE %s: answered on %s seq=%d: %s", c.kind, answer.Stream, answer.Seq, liveRedact(answer.Data, 360))

			res, err := sess.Stop(ctx)
			if err != nil {
				t.Fatalf("live %s stop: %v", c.kind, err)
			}
			if !res.ProcessExited {
				t.Fatalf("live %s stop did not observe the process exit", c.kind)
			}
			t.Logf("LIVE %s: stop observed exit=%d generation=%d", c.kind, res.ExitCode, res.Generation)
		})
	}
}

// TestOfficialCLI_LiveReconnectDeliversTheBufferOnceInOrder kills the engine's
// client side while the CLI keeps running, writes more input with nobody
// attached, reconnects, and proves the whole buffer is delivered once and in
// order. It then stops the child and proves reconnect names the process loss
// instead of launching a replacement.
func TestOfficialCLI_LiveReconnectDeliversTheBufferOnceInOrder(t *testing.T) {
	for _, c := range liveCLIs(t) {
		t.Run(c.kind, func(t *testing.T) {
			runCtx, endRun := context.WithCancel(context.Background())
			defer endRun()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, sess := liveLaunch(t, runCtx, c, liveRequest(t))
			defer func() { _, _ = sess.Stop(context.Background()) }()

			frames, unsub := sess.Attach(1)
			if err := sess.Send(ctx, []byte(c.handshake(1))); err != nil {
				t.Fatalf("live %s send: %v", c.kind, err)
			}
			first := liveDrain(frames, 45*time.Second, func(data []byte) bool { return c.answers(1, data) })
			if len(first) == 0 {
				t.Fatalf("live %s delivered nothing to the first client", c.kind)
			}
			t.Logf("LIVE %s: the first client saw %d frame(s), seq %d..%d",
				c.kind, len(first), first[0].Seq, first[len(first)-1].Seq)

			// Kill the engine's client side. The child keeps running and keeps
			// answering; its output lands in the buffer with nobody subscribed.
			unsub()
			if err := sess.Send(ctx, []byte(c.handshake(2))); err != nil {
				t.Fatalf("live %s send while detached: %v", c.kind, err)
			}

			replay, unsub2, err := sess.Reconnect(1)
			if err != nil {
				t.Fatalf("live %s reconnect on a live process: %v", c.kind, err)
			}
			defer unsub2()
			got := liveDrain(replay, 45*time.Second, func(data []byte) bool { return c.answers(2, data) })
			liveAssertOnceInOrder(t, c.kind, got)
			liveAssertReplays(t, c.kind, first, got)
			if len(got) <= len(first) {
				t.Fatalf("live %s: reconnect delivered %d frame(s), no more than the %d the killed client had already seen, so nothing written while detached was buffered",
					c.kind, len(got), len(first))
			}
			if !c.answers(2, got[len(got)-1].Data) {
				t.Fatalf("live %s: reconnect did not deliver the answer written while no client was attached", c.kind)
			}
			t.Logf("LIVE %s: reconnect delivered %d frame(s), seq %d..%d — once, in order, including the %d buffered while detached",
				c.kind, len(got), got[0].Seq, got[len(got)-1].Seq, len(got)-len(first))

			res, err := sess.Stop(ctx)
			if err != nil {
				t.Fatalf("live %s stop: %v", c.kind, err)
			}
			t.Logf("LIVE %s: stop after reconnect observed exit=%d", c.kind, res.ExitCode)
			if _, _, err := sess.Reconnect(1); !errors.Is(err, cliruntime.ErrProcessGone) {
				t.Fatalf("live %s reconnect after the process left = %v, want ErrProcessGone (it must not launch a replacement)", c.kind, err)
			}
			t.Logf("LIVE %s: the process is gone, so reconnect answers by name instead of launching a replacement", c.kind)
			after, unsub3 := sess.Attach(1)
			defer unsub3()
			outlived := liveDrain(after, 15*time.Second, func([]byte) bool { return false })
			liveAssertOnceInOrder(t, c.kind, outlived)
			liveAssertReplays(t, c.kind, first, outlived)
			t.Logf("LIVE %s: the buffer outlived the process — attach replayed %d frame(s), once and in order",
				c.kind, len(outlived))
		})
	}
}

// TestOfficialCLI_LiveResumeCarriesTheConversation stops a live generation and
// resumes it. Resume must start a NEW generation that carries the SAME
// conversation, and it must refuse without one.
func TestOfficialCLI_LiveResumeCarriesTheConversation(t *testing.T) {
	for _, c := range liveCLIs(t) {
		t.Run(c.kind, func(t *testing.T) {
			runCtx, endRun := context.WithCancel(context.Background())
			defer endRun()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			req := liveRequest(t)
			// The homes are shared across the two generations on purpose: a
			// resume continues a conversation the first generation stored.
			r, first := liveLaunch(t, runCtx, c, req)
			if _, err := r.Resume(runCtx, cliruntime.ResumeRequest{}); !errors.Is(err, cliruntime.ErrResumeRequired) {
				t.Fatalf("live %s resume without a conversation = %v, want ErrResumeRequired", c.kind, err)
			}
			frames, unsub := first.Attach(1)
			conv := liveConversation(t, c, first, frames)
			unsub()
			gen1 := first.Generation()
			if _, err := first.Stop(ctx); err != nil {
				t.Fatalf("live %s stop: %v", c.kind, err)
			}

			second, err := r.Resume(runCtx, cliruntime.ResumeRequest{
				LaunchRequest:  req,
				ConversationID: conv,
			})
			if err != nil {
				t.Fatalf("live %s resume: %v", c.kind, err)
			}
			defer func() { _, _ = second.Stop(context.Background()) }()
			if second.ConversationID() != conv {
				t.Fatalf("live %s resumed conversation %q != %q (it must not start a different conversation)", c.kind, second.ConversationID(), conv)
			}
			if second.Generation() == gen1 {
				t.Fatalf("live %s resume kept generation %d", c.kind, gen1)
			}
			if second.PID() <= 0 {
				t.Fatalf("live %s resume pid = %d", c.kind, second.PID())
			}
			t.Logf("LIVE %s: resume carried conversation %s from generation %d into generation %d (pid=%d)",
				c.kind, conv, gen1, second.Generation(), second.PID())
		})
	}
}

// liveConversation returns the conversation id of the first generation: the one
// the CLI itself nominates when it nominates one, and the id the engine stored
// otherwise. It never reaches a model — see liveClaudeConversation.
func liveConversation(t *testing.T, c liveCLI, sess cliruntime.Session, frames <-chan cliruntime.Frame) string {
	t.Helper()
	if c.kind == cliruntime.KindClaude {
		if conv := liveClaudeConversation(t, c, sess, frames); conv != "" {
			return conv
		}
	} else {
		// Codex and Grok nominate no conversation in their handshake: the engine
		// carries the id it stored for that run. Said as such, not inferred.
		t.Logf("LIVE %s: the handshake nominates no conversation; the stored id is what resume carries", c.kind)
	}
	return "stored-" + c.kind + "-conversation"
}

// liveClaudeConversation obtains the conversation Claude Code itself nominates,
// and it is the one place in this file that writes a conversation turn.
//
// ⛔ THE TURN IS WRITTEN ONLY AFTER THE CLI HAS SAID IT HAS NO CREDENTIAL, and
// that report costs nothing: the control handshake answers with
// `account.tokenSource`. When it is anything but "none" this returns "" and the
// resume leg falls back to the stored id, so a credentialed host spends nothing
// either. When it is "none" the CLI answers the turn itself, from a synthetic
// model, and the assertion below proves the turn cost zero.
func liveClaudeConversation(t *testing.T, c liveCLI, sess cliruntime.Session, frames <-chan cliruntime.Frame) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := sess.Send(ctx, []byte(c.handshake(1))); err != nil {
		t.Fatalf("live claude send: %v", err)
	}
	control, ok := liveWaitFrame(frames, 45*time.Second, func(data []byte) bool { return c.answers(1, data) })
	if !ok {
		t.Fatal("live claude gave no control answer in 45s")
	}
	var reply struct {
		Response struct {
			Response struct {
				Account struct {
					TokenSource string `json:"tokenSource"`
				} `json:"account"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(control.Data, &reply); err != nil {
		t.Fatalf("live claude control answer is not JSON: %v", err)
	}
	source := reply.Response.Response.Account.TokenSource
	if source != "none" {
		t.Logf("LIVE claude: the CLI reports account.tokenSource=%q, so no conversation turn is written and the stored id is what resume carries", source)
		return ""
	}
	t.Log("LIVE claude: the CLI reports account.tokenSource=\"none\", so the conversation turn below cannot reach a model")
	if err := sess.Send(ctx, []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"ping"}]}}`)); err != nil {
		t.Fatalf("live claude send turn: %v", err)
	}
	if _, ok := liveWaitFrame(frames, 45*time.Second, func(data []byte) bool {
		return strings.Contains(string(data), `"subtype":"init"`)
	}); !ok {
		t.Fatal("live claude printed no init frame in 45s")
	}
	conv := sess.ConversationID()
	if conv == "" {
		t.Fatal("live claude nominated no conversation in its init frame")
	}
	result, ok := liveWaitFrame(frames, 45*time.Second, func(data []byte) bool {
		return strings.Contains(string(data), `"type":"result"`)
	})
	if !ok {
		t.Fatal("live claude gave no result frame in 45s")
	}
	var outcome struct {
		IsError bool    `json:"is_error"`
		Result  string  `json:"result"`
		Cost    float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(result.Data, &outcome); err != nil {
		t.Fatalf("live claude result frame is not JSON: %v", err)
	}
	if outcome.Cost != 0 {
		t.Fatalf("live claude turn reported total_cost_usd=%v: a model turn was spent and this battery must never spend one", outcome.Cost)
	}
	t.Logf("LIVE claude: it nominated conversation %s and answered the turn itself (is_error=%v, cost=%v, %q) — a named refusal, not a hang",
		conv, outcome.IsError, outcome.Cost, outcome.Result)
	return conv
}

// liveWaitFrame reads until a frame matches or the bound expires.
func liveWaitFrame(ch <-chan cliruntime.Frame, d time.Duration, ok func([]byte) bool) (cliruntime.Frame, bool) {
	deadline := time.After(d)
	for {
		select {
		case f, open := <-ch:
			if !open {
				return cliruntime.Frame{}, false
			}
			if ok(f.Data) {
				return f, true
			}
		case <-deadline:
			return cliruntime.Frame{}, false
		}
	}
}

// liveDrain collects frames until `last` matches, the channel closes, or the
// stream goes idle. The idle bound is what lets a drain end on a CLI that is
// still running and has nothing more to say.
//
// ⛔ THE IDLE BOUND STARTS AT THE FIRST FRAME, NOT AT THE CALL. An official CLI
// takes seconds to reach its first byte (Claude Code took 2.5 s to answer a
// control request on this host), so a drain that begins its idle countdown
// immediately returns empty and reports the CLI as silent when it was only
// starting.
func liveDrain(ch <-chan cliruntime.Frame, d time.Duration, last func([]byte) bool) []cliruntime.Frame {
	var out []cliruntime.Frame
	deadline := time.After(d)
	for {
		idle := d
		if len(out) > 0 {
			idle = time.Second
		}
		select {
		case f, open := <-ch:
			if !open {
				return out
			}
			out = append(out, f)
			if last(f.Data) {
				return out
			}
		case <-time.After(idle):
			return out
		case <-deadline:
			return out
		}
	}
}

// liveAssertOnceInOrder proves the delivery property the operator depends on:
// every frame arrives once, in ascending sequence, with no hole.
func liveAssertOnceInOrder(t *testing.T, kind string, got []cliruntime.Frame) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("live %s: the replay delivered nothing", kind)
	}
	if got[0].Seq != 1 {
		t.Fatalf("live %s: the replay starts at seq %d, not 1", kind, got[0].Seq)
	}
	seen := make(map[int64]bool, len(got))
	for i, f := range got {
		if seen[f.Seq] {
			t.Fatalf("live %s: seq %d was delivered twice", kind, f.Seq)
		}
		seen[f.Seq] = true
		if i > 0 && f.Seq != got[i-1].Seq+1 {
			t.Fatalf("live %s: seq jumped from %d to %d", kind, got[i-1].Seq, f.Seq)
		}
	}
}

// liveAssertReplays proves the reconnected client sees what the killed client
// saw, byte for byte and in the same places.
func liveAssertReplays(t *testing.T, kind string, first, got []cliruntime.Frame) {
	t.Helper()
	if len(got) < len(first) {
		t.Fatalf("live %s: the replay has %d frame(s), fewer than the %d the first client saw", kind, len(got), len(first))
	}
	for i, want := range first {
		if got[i].Seq != want.Seq {
			t.Fatalf("live %s: replay frame %d has seq %d, want %d", kind, i, got[i].Seq, want.Seq)
		}
		if got[i].Stream != want.Stream {
			t.Fatalf("live %s: replay frame %d is on %s, want %s", kind, i, got[i].Stream, want.Stream)
		}
		if string(got[i].Data) != string(want.Data) {
			t.Fatalf("live %s: replay frame %d differs from what the first client saw", kind, i)
		}
	}
}

// liveRedact bounds a logged frame and removes the host name a vendor CLI
// echoes back. A receipt carries what the runtime observed, not where it ran.
func liveRedact(data []byte, max int) string {
	s := string(data)
	if host, err := os.Hostname(); err == nil && host != "" {
		s = strings.ReplaceAll(s, host, "HOST")
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
