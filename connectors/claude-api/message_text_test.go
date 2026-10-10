// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProxyTextContentShorthandReachesGovernance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		batch bool
		allow bool
	}{
		{"message_allow", false, true},
		{"message_deny", false, false},
		{"batch_allow", true, true},
		{"batch_deny", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := &proxyStubDoer{status: http.StatusOK, body: okMessageJSON}
			dec := &fakeDecider{
				decision:      ProxyDecision{Allow: tc.allow, Status: http.StatusForbidden},
				batchDecision: ProxyBatchDecision{Allow: tc.allow, Status: http.StatusForbidden},
				batchDenyAt:   -1,
			}
			proxy := newProxy(t, doer, dec, nil)
			body := `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"SHORTHAND-CANARY"}]}`
			response := postMessages
			if tc.batch {
				body = `{"requests":[{"custom_id":"fixture","params":` + body + `}]}`
				doer.body = upstreamBatchJSON
				response = postBatch
			}
			got := response(t, proxy, body, "CALLER-FIXTURE")
			want := http.StatusForbidden
			if tc.allow {
				want = http.StatusOK
			}
			if got.Code != want {
				t.Fatalf("status=%d, want %d: %s", got.Code, want, got.Body.String())
			}
			req := dec.gotReq
			if tc.batch {
				if len(dec.gotBatch) != 1 {
					t.Fatal("shorthand did not reach per-entry governance")
				}
				req = dec.gotBatch[0].Params
			}
			if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Type != "text" || req.Messages[0].Content[0].Text != "SHORTHAND-CANARY" {
				t.Fatal("governance did not receive the shorthand as text content")
			}
			if !tc.allow {
				if doer.calls != 0 || dec.finalized || dec.batchFinalized {
					t.Fatal("denied shorthand reached the upstream effect")
				}
				return
			}
			if doer.calls != 1 || doer.gotAPIKey != "OPERATOR-KEY" || !strings.Contains(string(doer.gotBody), `"content":[{"type":"text","text":"SHORTHAND-CANARY"}]`) {
				t.Fatal("shorthand did not forward its governed text with the operator credential")
			}
		})
	}
}

func TestMessageTextShorthandKeepsExistingContentForms(t *testing.T) {
	const raw = `{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"fixture","content":[{"type":"text","text":"opaque fixture"}],"is_error":true}]}`
	var message Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(message)
	if err != nil || string(encoded) != raw {
		t.Fatalf("existing opaque block changed: %s, %v", encoded, err)
	}
	if err := json.Unmarshal([]byte(`{"role":"user"}`), &message); err != nil || len(message.Content) != 1 {
		t.Fatal("omitted content changed existing decoder state")
	}
	if err := json.Unmarshal([]byte(`{"content":null}`), &message); err != nil || message.Content != nil {
		t.Fatal("explicit null no longer clears content")
	}
	for _, invalid := range []string{`{"content":1}`, `{"content":{}}`} {
		if json.Unmarshal([]byte(invalid), &message) == nil {
			t.Fatal("unsupported content form admitted")
		}
	}
}
