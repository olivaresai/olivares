// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHookClientSlowPEPReturnsExplicitDeny(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	err := RunHookClient(context.Background(), strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"printf should-not-run"}}`), &out, HookClientConfig{Endpoint: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("deadline must still emit a decision: %v", err)
	}
	var wire struct {
		Output struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Output.Decision != "deny" || !strings.Contains(wire.Output.Reason, "timed out") {
		t.Fatalf("late PEP did not deny explicitly: %s", out.Bytes())
	}
}

func TestHookClientIncompletePEPReturnsExplicitDeny(t *testing.T) {
	for _, response := range []string{"", "{}", "null", "{", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"invalid"}}`, `{"hookSpecificOutput":{"hookEventName":"OtherEvent","permissionDecision":"allow"}}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			wire := runClient(t, preToolUsePayload("Bash"), HookClientConfig{Endpoint: server.URL})
			output, _ := wire["hookSpecificOutput"].(map[string]any)
			if output["permissionDecision"] != "deny" {
				t.Fatalf("incomplete reply was not denied: %v", wire)
			}
		})
	}
}
