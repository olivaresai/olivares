// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestApprovalGetShowsTheWholeReasonInText: a reviewer must read the full command before
// approving. The text form of `governance approvals get` dropped Reason (JSON kept it).
// It is printed whole: its own lines kept, long lines folded only at a space, internal
// spacing unchanged, terminal controls removed.
func TestApprovalGetShowsTheWholeReasonInText(t *testing.T) {
	reason := "Run in /srv/app: rm -rf ./build &&  printf 'a  b' > out.txt && " +
		strings.Repeat("make release-candidate ", 6) + "\x1b[31mDONE\x1b[0m\nthen: git push origin main --force-with-lease"
	body, _ := json.Marshal(map[string]any{"id": "apr-1", "status": "pending", "risk_tier": "high",
		"required_approvals": 1, "action": "sessions.launch", "requested_by": "user:ana", "reason": reason})
	spy := newObserveSpy(t, http.StatusOK, string(body))
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "governance", "approvals", "get", "apr-1", "-o", "text")...)
	if err != nil {
		t.Fatalf("approvals get: %v", err)
	}
	i := strings.Index(out, "REASON")
	if i < 0 {
		t.Fatalf("no reason in the text form:\n%s", out)
	}
	block := out[i:]
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("the reason reached the terminal with its control sequences:\n%q", out)
	}
	if !strings.Contains(block, "printf 'a  b'") || !strings.Contains(block, "&&  printf") {
		t.Fatalf("the reason's own spacing was changed:\n%s", block)
	}
	if !strings.Contains(block, "\n  then: git push origin main --force-with-lease") {
		t.Fatalf("the reason's second line is not its own line:\n%s", block)
	}
	rest := block
	for _, word := range strings.Fields(termSafe(reason)) {
		j := strings.Index(rest, word)
		if j < 0 {
			t.Fatalf("the reason lost %q:\n%s", word, block)
		}
		rest = rest[j+len(word):]
	}
	for _, line := range strings.Split(block, "\n") {
		if len([]rune(line)) > 80 {
			t.Fatalf("a reason line is wider than 80 columns: %q", line)
		}
	}
}
