// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

func TestSessionViewShowsApprovalNoticeBeforeInterruptedTurn(t *testing.T) {
	var out strings.Builder
	v := newSessionView(&out)
	if v.render(`{"type":"olivares_notice","code":"approval_refused","cause":"access_ended","message":"Olivares refused the tool request because this session's access has ended."}`) {
		t.Fatal("notice ended turn")
	}
	if !v.render(`{"method":"turn/completed","params":{"turn":{"id":"t","status":"interrupted"}}}`) {
		t.Fatal("interruption did not end turn")
	}
	text := out.String()
	before, after := strings.Index(text, "this session's access has ended"), strings.Index(text, "turn interrupted")
	if before < 0 || after <= before {
		t.Fatalf("missing or misplaced notice: %q", text)
	}
}
