// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTurnGLM53ThinkingDisabled(t *testing.T) {
	raw := fixtureBytes(t, "glm-5.3-thinking-disabled.json")
	if !bytes.Contains(raw, []byte(`"disabled"`)) {
		t.Fatal("fixture is not a thinking.type disabled request")
	}
	assertTurnRefused(t, "glm-5.3-thinking-disabled.json", raw)
}

func TestTurnKimiK3AssistantComplete(t *testing.T) {
	assistant := fixtureBytes(t, "kimi-k3-assistant-complete.json")
	if !bytes.Contains(assistant, []byte(`"reasoning_content"`)) || !bytes.Contains(assistant, []byte(`"tool_calls"`)) {
		t.Fatal("fixture is missing reasoning_content or tool_calls")
	}
	// The file stays the assistant sample. The request body is built here.
	var body bytes.Buffer
	body.WriteString(`{"model":"kimi-k3","max_tokens":16,"messages":[`)
	body.WriteString(`{"role":"user","content":"Tell me three random numbers."},`)
	body.Write(bytes.TrimSpace(assistant))
	body.WriteString(`,{"role":"user","content":"What are the other two numbers you have in mind?"}]}`)
	doer, driver := captureDriver(t)
	_, err := OfferText(context.Background(), driver, body.Bytes())
	// This can fail when the assistant turn is rewritten to content only.
	stripped := []byte(`{"role":"assistant","content":"473, 921, 235"}`)
	if bytes.Contains(doer.buf.Bytes(), stripped) {
		t.Fatal("assistant message was rewritten to content only")
	}
	if doer.n != 0 || doer.buf.Len() != 0 {
		t.Fatalf("outbound buffer not empty: requests=%d bytes=%d", doer.n, doer.buf.Len())
	}
	wantNotImplemented(t, err)
	if stringsContainSecret(err) {
		t.Fatalf("refusal leaked fixture bytes: %v", err)
	}
	after := fixtureBytes(t, "kimi-k3-assistant-complete.json")
	if !bytes.Equal(assistant, after) {
		t.Fatal("kimi fixture changed on disk")
	}
}

func TestTurnGrok47ReasoningItem(t *testing.T) {
	item := fixtureBytes(t, "grok-4.7-reasoning-item.json")
	if !bytes.Contains(item, []byte(`"encrypted_content"`)) || !bytes.Contains(item, []byte(`"reasoning"`)) {
		t.Fatal("fixture is not one reasoning item")
	}
	var body bytes.Buffer
	body.WriteString(`{"model":"grok-4.7","input":[`)
	body.Write(bytes.TrimSpace(item))
	body.WriteString(`]}`)
	doer, driver := captureDriver(t)
	_, err := OfferText(context.Background(), driver, body.Bytes())
	mutated, merr := encodeOpenAI(MessageRequest{
		Model:     "grok-4.7",
		MaxTokens: 16,
		Messages:  []Message{{Role: "user", Content: "opaque-ciphertext"}},
	}, false)
	if merr != nil {
		t.Fatal(merr)
	}
	enc, merr := json.Marshal(mutated)
	if merr != nil {
		t.Fatal(merr)
	}
	if bytes.Equal(doer.buf.Bytes(), enc) {
		t.Fatal("chat encoder emitted a mutated copy of the reasoning item")
	}
	if bytes.Contains(doer.buf.Bytes(), []byte("opaque-ciphertext")) {
		t.Fatal("ciphertext was copied into the outbound body")
	}
	if doer.n != 0 || doer.buf.Len() != 0 {
		t.Fatalf("outbound buffer not empty: requests=%d bytes=%d", doer.n, doer.buf.Len())
	}
	wantNotImplemented(t, err)
	if stringsContainSecret(err) {
		t.Fatalf("refusal leaked fixture bytes: %v", err)
	}
	after := fixtureBytes(t, "grok-4.7-reasoning-item.json")
	if !bytes.Equal(item, after) {
		t.Fatal("grok fixture changed on disk")
	}
}

// assertTurnRefused refuses raw, leaves the fixture file unchanged, and
// leaves the fake doer's outbound buffer empty.
func assertTurnRefused(t *testing.T, name string, before []byte) *captureDoer {
	t.Helper()
	doer, d := captureDriver(t)
	_, err := OfferText(context.Background(), d, before)
	wantNotImplemented(t, err)
	if doer.n != 0 || doer.buf.Len() != 0 {
		t.Fatalf("outbound buffer not empty: requests=%d bytes=%d", doer.n, doer.buf.Len())
	}
	if stringsContainSecret(err) {
		t.Fatalf("refusal leaked fixture bytes: %v", err)
	}
	after, rerr := os.ReadFile(filepath.Join("testdata", name))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("fixture %s changed on disk", name)
	}
	return doer
}

func stringsContainSecret(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, secret := range []string{"opaque-ciphertext", "reasoning_content", "473, 921, 235"} {
		if bytes.Contains([]byte(s), []byte(secret)) {
			return true
		}
	}
	return false
}
