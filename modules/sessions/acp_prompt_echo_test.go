// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// An ACP agent does not echo the person's prompt, so the console and
// `session follow` had the reply and never the question. Each accepted prompt is one
// user_message_chunk frame in the run's stream, ahead of that turn's reply, through
// the real OpenCode driver and fixture child; an agent that echoes it says it once.
func TestACPPromptIsInTheStreamOnceAheadOfItsReply(t *testing.T) {
	for _, echo := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent echoes %v", echo), func(t *testing.T) {
			m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
			setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-echo", EchoPrompt: echo})
			ctx := context.Background()
			dto, err := openCodeLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
			lr, ok := m.rt.getLive(tenant, dto.RunRef)
			if !ok {
				t.Fatal("launched run has no live handle")
			}
			prompts := []string{"first message", "second\nmessage {\"type\":\"user\"}"}
			for i, text := range prompts {
				waitFor(t, "an idle conversation", func() bool { return lr.session.ActiveTurn() == "" })
				if err := m.sendTextInput(ctx, tenant, dto.RunRef, text); err != nil {
					t.Fatalf("input %d: %v", i, err)
				}
				waitFor(t, "the reply", func() bool { return countACPUpdates(lr, "agent_message_chunk", "live output") == i+1 })
			}
			var kinds []string
			for _, f := range lr.ring.readFrom(0).frames {
				var frame struct {
					Params struct {
						SessionID string `json:"sessionId"`
						Update    struct {
							SessionUpdate string                `json:"sessionUpdate"`
							Content       struct{ Text string } `json:"content"`
							Meta          map[string]any        `json:"_meta"`
						} `json:"update"`
					} `json:"params"`
				}
				if json.Unmarshal(f.Data, &frame) != nil || frame.Params.Update.SessionUpdate == "" {
					continue
				}
				u := frame.Params.Update
				switch u.SessionUpdate {
				case "user_message_chunk":
					if frame.Params.SessionID != "ses-echo" {
						t.Fatalf("prompt frame names conversation %q", frame.Params.SessionID)
					}
					fromEngine := u.Meta["olivares.ai/origin"] == "accepted_input"
					if fromEngine == echo {
						t.Fatalf("prompt frame written by the engine=%v with an echoing agent=%v: %s", fromEngine, echo, f.Data)
					}
					kinds = append(kinds, "you: "+u.Content.Text)
				case "agent_message_chunk":
					kinds = append(kinds, "agent: "+u.Content.Text)
				}
			}
			want := []string{"you: " + prompts[0], "agent: live output", "you: " + prompts[1], "agent: live output"}
			if fmt.Sprint(kinds) != fmt.Sprint(want) {
				t.Fatalf("stream = %q, want each prompt once, ahead of its reply: %q", kinds, want)
			}
		})
	}
}

func countACPUpdates(lr *liveRun, kind, text string) int {
	n := 0
	for _, f := range lr.ring.readFrom(0).frames {
		if t, ok := acpUpdateText(f.Data, kind); ok && t == text {
			n++
		}
	}
	return n
}

func acpUpdateText(data []byte, kind string) (string, bool) {
	var frame struct {
		Params struct {
			Update struct {
				SessionUpdate string                `json:"sessionUpdate"`
				Content       struct{ Text string } `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Params.Update.SessionUpdate != kind {
		return "", false
	}
	return frame.Params.Update.Content.Text, true
}
