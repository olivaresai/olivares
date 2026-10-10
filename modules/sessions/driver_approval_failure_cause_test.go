// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// An approval authority that cannot decide still refuses deny-closed, and the log now
// names why by class (here the launching person's access ended). The error's own text
// can carry paths or credentials and is never logged.
func TestApprovalAuthorityFailureLogsItsCause(t *testing.T) {
	const private = "/var/lib/olivares/private-store-path"
	failing := func(logged *[]string, mu *sync.Mutex) func(*DriverSessionConfig) {
		return func(c *DriverSessionConfig) {
			c.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				return ProviderApprovalDecision{}, fmt.Errorf("owner read at %s: %w", private, auth.ErrSessionAccessEnded)
			}
			c.Warn = func(msg string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				*logged = append(*logged, fmt.Sprintln(append([]any{msg}, args...)...))
			}
		}
	}
	options := []any{
		map[string]any{"optionId": "allow-once", "kind": "allow_once", "name": "Allow once"},
		map[string]any{"optionId": "reject-once", "kind": "reject_once", "name": "Reject"},
	}
	for _, tc := range []struct {
		driver string
		refuse func(t *testing.T, opt func(*DriverSessionConfig))
	}{
		{providerDriverCodex, func(t *testing.T, opt func(*DriverSessionConfig)) {
			peer := newCodexPeer(t, opt)
			if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			peer.session.(*codexSession).setActiveTurn("turn-1", "inProgress")
			peer.requestFromServer("refused", codexReqCommandApproval, map[string]any{"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1"})
			peer.awaitReplyTo("refused")
		}},
		{providerDriverGrok, func(t *testing.T, opt func(*DriverSessionConfig)) {
			peer := newGrokPeer(t, opt)
			if _, err := peer.answerHandshake("sess-1", grokAllAuthMethods()); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			if _, err := peer.session.Input(context.Background(), "run something"); err != nil {
				t.Fatalf("input: %v", err)
			}
			peer.nextRequest()
			peer.requestFromServer("srv-refused", acpReqRequestPermission, map[string]any{"sessionId": "sess-1", "toolCall": map[string]any{"toolCallId": "call-1", "kind": "execute"}, "options": options})
			peer.nextResponse()
		}},
		{providerDriverOpenCode, func(t *testing.T, opt func(*DriverSessionConfig)) {
			peer := newOpenCodePeer(t, opt)
			t.Cleanup(func() { peer.session.Close(nil) })
			if _, err := peer.answerHandshake("ses_1"); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			if _, err := peer.session.Input(context.Background(), "use the tool"); err != nil {
				t.Fatalf("input: %v", err)
			}
			peer.nextRequest()
			peer.requestFromServer("refused", acpReqRequestPermission, map[string]any{"sessionId": "ses_1", "toolCall": map[string]any{"toolCallId": "call-1", "kind": "execute"}, "options": options})
			peer.next()
		}},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			var logged []string
			var mu sync.Mutex
			tc.refuse(t, failing(&logged, &mu))
			mu.Lock()
			defer mu.Unlock()
			line := strings.Join(logged, "\n")
			if !strings.Contains(line, "refusing deny-closed") || !strings.Contains(line, "cause access_ended") || strings.Contains(line, private) {
				t.Fatalf("logged %q, want the deny-closed refusal with cause access_ended and no error text", line)
			}
		})
	}
}
