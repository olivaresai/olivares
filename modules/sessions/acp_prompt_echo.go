// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// An ACP agent (OpenCode, Grok Build) does not
// echo the person's prompt, so the console and `session follow` showed the reply and
// never what the person typed. A prompt the driver accepted becomes one session/update
// user_message_chunk frame in the run's output stream.
//
// The BRIDGE writes it, just before the child's next frame of that turn, never the
// input path: the ring's sequence and the governed recorder's chain are written by the
// run's one bridge goroutine, and a frame appended from the request would race that
// chain. So the prompt precedes the reply both in the stream and in the recording.
//
// SR2C on e418d66b: a prompt is published only once the driver ACCEPTED it (a refusal
// before the write publishes nothing); bookkeeping updates (commands, mode,
// configuration, usage) never publish it, so an agent's own echo that follows them is
// recognised; and an echo counts only in the run's own conversation. An agent that
// echoes the prompt says it once, and from then on this run adds nothing.
type acpPromptEcho struct {
	mu      sync.Mutex
	pending []*acpPrompt
	echoes  bool
}

// acpPrompt is one prompt on its way to the child: undecided until the driver's Input
// returns, then accepted (published by the bridge) or dropped.
type acpPrompt struct {
	sessionID, text string
	accepted        bool
	waited          bool
	decided         chan struct{}
	once            sync.Once
}

// acpDecisionWait bounds how long the bridge holds a turn's frame for the decision on
// a prompt whose write is in progress, so the prompt can still come first. It waits
// once per prompt; Input returns as soon as its frame is written.
const acpDecisionWait = 250 * time.Millisecond

// acpEchoDriver reports whether a driver speaks ACP, whose agents do not echo a prompt.
func acpEchoDriver(d ProviderDriver) bool {
	return d != nil && (d.Key() == providerDriverOpenCode || d.Key() == providerDriverGrok || d.Key() == providerDriverGemini)
}

// queue holds a prompt the driver is about to write; nil when nothing is to be added.
func (e *acpPromptEcho) queue(sessionID, text string) *acpPrompt {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.echoes || strings.TrimSpace(text) == "" {
		return nil
	}
	p := &acpPrompt{sessionID: sessionID, text: text, decided: make(chan struct{})}
	e.pending = append(e.pending, p)
	return p
}

// decide records the driver's answer: an accepted prompt is published before the
// turn's next frame; a refused or uncertain one is dropped.
func (e *acpPromptEcho) decide(p *acpPrompt, accepted bool) {
	e.mu.Lock()
	if accepted {
		p.accepted = true
	} else {
		for i, q := range e.pending {
			if q == p {
				e.pending = append(e.pending[:i], e.pending[i+1:]...)
				break
			}
		}
	}
	e.mu.Unlock()
	p.once.Do(func() { close(p.decided) })
}

// before returns the frames to write ahead of one child stdout frame: the accepted
// prompts at the head of the queue, when that frame belongs to their turn.
func (e *acpPromptEcho) before(frame []byte) [][]byte {
	e.mu.Lock()
	idle := len(e.pending) == 0
	e.mu.Unlock()
	if idle {
		return nil
	}
	f := readACPFrame(frame)
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pending) == 0 {
		return nil
	}
	head := e.pending[0]
	if f.update && f.sessionID == head.sessionID && f.kind == "user_message_chunk" &&
		f.text != "" && strings.HasPrefix(head.text, f.text) {
		// The agent's own echo of the prompt, in this conversation: it says it.
		e.echoes, e.pending = true, nil
		return nil
	}
	if f.update && (f.sessionID != head.sessionID || acpBookkeeping[f.kind]) {
		return nil
	}
	if !head.accepted && !head.waited {
		head.waited = true
		e.mu.Unlock()
		select {
		case <-head.decided:
		case <-time.After(acpDecisionWait):
		}
		e.mu.Lock()
	}
	var out [][]byte
	for len(e.pending) > 0 && e.pending[0].accepted {
		p := e.pending[0]
		e.pending = e.pending[1:]
		if b, err := acpUserMessageFrame(p.sessionID, p.text); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// acpBookkeeping are the session/update kinds about the session itself rather than a
// turn: they never place the prompt.
var acpBookkeeping = map[string]bool{
	"available_commands_update": true, "current_mode_update": true, "config_option_update": true,
	"session_info_update": true, "usage_update": true,
}

// acpFrame is what before reads of a child frame.
type acpFrame struct {
	update                bool
	sessionID, kind, text string
}

func readACPFrame(frame []byte) acpFrame {
	var f struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(frame, &f) != nil || f.Method != "session/update" {
		return acpFrame{}
	}
	out := acpFrame{update: true, sessionID: f.Params.SessionID, kind: f.Params.Update.SessionUpdate}
	if f.Params.Update.Content.Type == "text" {
		out.text = f.Params.Update.Content.Text
	}
	return out
}

// acpUserMessageFrame is the person's prompt as the ACP notification an agent uses for
// it. The _meta key says the engine wrote it, not the agent.
func acpUserMessageFrame(sessionID, text string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "user_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
				"_meta":         map[string]any{"olivares.ai/origin": "accepted_input"},
			},
		},
	})
}
