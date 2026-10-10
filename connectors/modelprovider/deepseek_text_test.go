// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
)

func TestDeepSeekTextRequestUsesTheDocumentedNonThinkingContract(t *testing.T) {
	prepared, err := PrepareDeepSeekTextRequest(ChatTextRequest{
		Model: "deepseek-flash", Input: "  hello\n", MaxCompletionTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["max_tokens"]) != "64" || string(body["stream"]) != "false" ||
		string(body["thinking"]) != `{"type":"disabled"}` {
		t.Fatalf("wrong DeepSeek request: %s", prepared.Bytes())
	}
	for _, key := range []string{"max_completion_tokens", "n", "store", "tools"} {
		if _, ok := body[key]; ok {
			t.Fatalf("unsupported field %s", key)
		}
	}
	if string(body["messages"]) != `[{"role":"user","content":"  hello\n"}]` {
		t.Fatalf("input changed: %s", body["messages"])
	}
	if prepared.Digest() != sha256.Sum256(prepared.Bytes()) {
		t.Fatal("digest is not the sent body")
	}
	for _, limit := range []int64{0, -1, 393217} {
		p, err := PrepareDeepSeekTextRequest(ChatTextRequest{Model: "deepseek-flash", Input: "hello", MaxCompletionTokens: limit})
		if err == nil || !p.IsZero() {
			t.Fatalf("invalid limit %d accepted", limit)
		}
	}
}
