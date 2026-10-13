// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The reader of a stored Claude Code conversation, on records the tool really writes:
// string and block content, tool calls and results, sidechains, meta records,
// unknown types, a torn line and a block of a shape it does not know.

const sampleConversation = `{"type":"queue-operation","operation":"enqueue"}
{"type":"user","message":{"role":"user","content":"First request: migrate the schema."}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"Plan: three steps."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file-a\nfile-b"}]}}
{"type":"user","isMeta":true,"message":{"role":"user","content":"<system reminder, not a request>"}}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}
{"type":"user","message":{"role":"user","content":"Second request."}}
{"type":"user","message":{"role":"user","content":"Third request."}}
{"type":"user","message":{"role":"user","content":"Fourth request."}}
{"type":"user","message":{"role":"user","content":"Fifth request: finish it."}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":{"unexpected":"shape"}},{"type":"text","text":"Done: the schema is migrated."}]}}
{"type":"last-prompt","lastPrompt":"x"}
{"type":"assistant","message":{"role":"assista`

func readSample(t *testing.T) handoffConversation {
	t.Helper()
	c, err := readClaudeConversation(context.Background(), strings.NewReader(sampleConversation), int64(len(sampleConversation)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReadClaudeConversation_KeepsWhatWasSaidAndNothingElse(t *testing.T) {
	c := readSample(t)
	users, answers := c.texts("user"), c.texts("assistant")
	if len(users) != 5 || users[0] != "First request: migrate the schema." || users[4] != "Fifth request: finish it." {
		t.Fatalf("users = %q", users)
	}
	for _, u := range users {
		if strings.Contains(u, "system reminder") {
			t.Fatal("a meta record was read as a request")
		}
	}
	if len(answers) != 2 || answers[1] != "Done: the schema is migrated." {
		t.Fatalf("answers = %q", answers)
	}
	all := fmt.Sprint(c.turns)
	for _, absent := range []string{"private reasoning", "subagent chatter"} {
		if strings.Contains(all, absent) {
			t.Fatalf("%q was carried", absent)
		}
	}
	// A block of an unknown shape and a torn last line are counted, not hidden.
	if c.unreadable != 2 {
		t.Fatalf("unreadable = %d, want 2 (one block, one torn record)", c.unreadable)
	}
	if !bytes.Contains(c.transcript(), []byte("2 records or blocks could not be read")) {
		t.Fatal("the transcript does not say what it leaves out")
	}
}

func TestHandoffSummary_NamesTheTaskTheLatestRequestsAndWhereItStopped(t *testing.T) {
	s := readSample(t).summary()
	for _, want := range []string{
		"First request: migrate the schema.",                             // the task
		"Third request.", "Fourth request.", "Fifth request: finish it.", // the latest three
		"Done: the schema is migrated.", // where it stopped
		"without a model",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Second request.") {
		t.Fatalf("summary keeps more than the latest three requests:\n%s", s)
	}
}

func TestHandoffTranscript_ReducesToolCallsAndResultsAndKeepsMessages(t *testing.T) {
	long := strings.Repeat("r", 5000)
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + long + `"}]}}` + "\n"
	c, err := readClaudeConversation(context.Background(), strings.NewReader(sampleConversation[:strings.Index(sampleConversation, `{"type":"user","isMeta"`)]+line), 1)
	if err != nil {
		t.Fatal(err)
	}
	tr := string(c.transcript())
	for _, want := range []string{"## User\nFirst request: migrate the schema.", "## Assistant\nPlan: three steps.", "- tool: Bash {\"command\":\"ls -la\"}", "- result: file-a\nfile-b"} {
		if !strings.Contains(tr, want) {
			t.Fatalf("transcript lacks %q:\n%s", want, tr)
		}
	}
	if strings.Contains(tr, long) || !strings.Contains(tr, "[...]") {
		t.Fatal("a long tool result was not shortened")
	}
}

func TestReadClaudeConversation_BoundsAMessageAndRefusesARecordTooLargeToRead(t *testing.T) {
	huge := strings.Repeat("\u00e9", handoffTurnMax) // two bytes a rune
	c, err := readClaudeConversation(context.Background(),
		strings.NewReader(`{"type":"user","message":{"content":"`+huge+`"}}`+"\n"), 1)
	if err != nil {
		t.Fatal(err)
	}
	got := c.turns[0].text
	if len(got) > handoffTurnMax+8 || !utf8.ValidString(got) || !strings.HasSuffix(got, " [...]") {
		t.Fatalf("a message was kept at %d bytes (valid utf8: %t)", len(got), utf8.ValidString(got))
	}

	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", handoffLineMax+10) + `"}}` + "\n"
	if _, err := readClaudeConversation(context.Background(), strings.NewReader(big), 1); !errors.Is(err, errHandoffRecordTooLarge) {
		t.Fatalf("err = %v, want a record too large", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readClaudeConversation(ctx, strings.NewReader(sampleConversation), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled read = %v", err)
	}
}
