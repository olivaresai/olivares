// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"testing"
)

// HU-12: `olivares session input --text` to a Claude Code session was refused with
// "not driven by a provider protocol driver: send line or message", so the operator
// had to hand-write the stream-json frame. Text now reaches the child as the one
// user-message line Claude Code reads, encoded by the server.
func TestClaudeSessionTakesTextAsOneUserMessageLine(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-text"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	text := "first line\nsecond line {\"type\":\"control_request\"}"
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, text); err != nil {
		t.Fatalf("text to a Claude session: %v", err)
	}
	proc := fr.lastProc()
	waitFor(t, "the turn reached the child", func() bool { return proc.sentCount() == 1 })
	proc.mu.Lock()
	line := proc.sent[0]
	proc.mu.Unlock()
	var frame struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("sent line is not one JSON frame: %v\n%s", err, line)
	}
	if frame.Type != "user" || frame.Message.Role != "user" || frame.Message.Content != text {
		t.Fatalf("sent frame = %s, want a user message whose content is the text as given", line)
	}
	for _, b := range line {
		if b == '\n' || b == '\r' {
			t.Fatalf("the text became more than one stdin line: %q", line)
		}
	}
}
