// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// The bridge withholds a resumed tool's replay through one question put to the
// session (replayingSession). Every ACP driver embeds acpSession, which answers it
// from the same state that classifies the replay as history.
var (
	_ replayingSession = (*geminiSession)(nil)
	_ replayingSession = (*openCodeSession)(nil)
	_ replayingSession = (*grokSession)(nil)
)

func replayUpdateFrame(t *testing.T, conversation, text string) OutputFrame {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": acpNotifySessionUpdate,
		"params": map[string]any{
			"sessionId": conversation,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
			},
		},
	})
	if err != nil {
		t.Fatalf("replayed frame: %v", err)
	}
	return OutputFrame{Stream: streamStdout, Data: frame}
}

// Gemini CLI has no fixture child that the runtime launches here, so its half of
// the rule is shown at the driver: from the moment session/load is sent until its
// correlated response, what the CLI sends may be a replay, and afterwards it is not.
func TestGeminiResumeIsReplayingUntilTheLoadIsAnswered(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	session := peer.session.(*geminiSession)
	if session.replaying() {
		t.Fatal("a session that has asked for nothing is replaying")
	}
	load := geminiSessionResultFor("")
	delete(load, "sessionId")

	var atLoad, afterReplay bool
	hs, _, err := peer.answerGemini(geminiInitializeResult(), load, map[string]func(int64){
		acpMethodSessionLoad: func(id int64) {
			atLoad = session.replaying()
			for _, text := range []string{"npm whoami //npm.acme.example/:_authToken=" + registryCanary, "history-1"} {
				session.Deliver(replayUpdateFrame(t, "ses_old", text))
			}
			afterReplay = session.replaying()
			peer.reply(id, load)
		},
	})
	if err != nil || hs.ConversationID != "ses_old" {
		t.Fatalf("resume = %+v %v", hs, err)
	}
	if !atLoad || !afterReplay {
		t.Fatalf("replaying while session/load is open = %v, after its replayed updates = %v; both must be true", atLoad, afterReplay)
	}
	if session.replaying() {
		t.Fatal("the session is still replaying after its load was answered")
	}
	if counts := session.updateCounts(); counts.history != 2 || counts.live != 0 {
		t.Fatalf("update counts = %+v, want the 2 replayed updates as history", counts)
	}
}

// A load that is refused, or that times out, ends the caller's wait; a tool that
// is slow has its replay in the pipe still. The hold lasts until the answer is
// read or the child is gone, never until the caller gave up.
func TestGeminiRefusedLoadStaysHeldUntilTheChildIsGone(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	session := peer.session.(*geminiSession)
	_, _, err := peer.answerGemini(geminiInitializeResult(), nil, map[string]func(int64){
		acpMethodSessionLoad: func(id int64) { peer.replyError(id, -32603, "No previous sessions found for this project.") },
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("err = %v, want the 409 refusal", err)
	}
	if !session.replaying() {
		t.Fatal("a refused load released the hold: the replay that is still in the pipe would be published")
	}
	if counts := session.updateCounts(); counts.history != 0 {
		t.Fatalf("history = %d before any replay arrived", counts.history)
	}
}

// withholdsReplay is judged on what a line is, on a live run whose session is
// replaying: only a well-formed response is published. It is judged on a Gemini
// session, which the runtime launches no fixture child for.
func TestWithholdsReplayPublishesOnlyAResponseWhileReplaying(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t)
	session := peer.session.(*geminiSession)
	lr := &liveRun{session: session}

	frames := map[string]struct {
		line string
		held bool
	}{
		"a response":                    {`{"jsonrpc":"2.0","id":3,"result":{}}`, false},
		"a response with a null result": {`{"jsonrpc":"2.0","id":3,"result":null}`, false},
		"an id and params only":         {`{"id":7,"params":{"text":"npm_token"}}`, true},
		"a history record with an id":   {`{"id":"msg_1","role":"user","content":"npm_token"}`, true},
		"an error response":             {`{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"x"}}`, false},
		"a session/update":              {`{"jsonrpc":"2.0","method":"session/update","params":{}}`, true},
		"an extension notification":     {`{"jsonrpc":"2.0","method":"_gemini/status","params":{}}`, true},
		"a request":                     {`{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{}}`, true},
		"a notification with a null id": {`{"jsonrpc":"2.0","id":null,"method":"session/update","params":{}}`, true},
		"a response with a null id":     {`{"jsonrpc":"2.0","id":null,"result":{}}`, true},
		"a line that is not JSON":       {`npm whoami //npm.acme.example/:_authToken=` + registryCanary, true},
		"a JSON array":                  {`[{"jsonrpc":"2.0","method":"session/update"}]`, true},
	}
	check := func(label string, replaying, covered bool) {
		t.Helper()
		lr.redactsEveryEarlierValue = covered
		for name, f := range frames {
			want := f.held && replaying && !covered
			if got := lr.withholdsReplay([]byte(f.line)); got != want {
				t.Errorf("%s: %s withheld = %v, want %v", label, name, got, want)
			}
		}
	}

	check("not replaying", false, false)

	session.mu.Lock()
	session.beginReplay("ses_old")
	session.mu.Unlock()
	check("replaying", true, false)
	check("replaying, redactor holds every token", true, true)

	// The caller giving up does not release the hold; the answer on the pump does.
	session.endReplay()
	check("after the caller gave up", true, false)
	session.answerReplay()
	check("after the answer", false, false)

	// A run with no protocol session (a stream-json Claude Code run) holds nothing.
	if (&liveRun{}).withholdsReplay([]byte(frames["a session/update"].line)) {
		t.Error("a run with no driver session withheld a line")
	}
}
