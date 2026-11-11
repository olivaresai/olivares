// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "testing"

// A result frame ends the turn and settles its row. A malformed is_error flag must not
// make the whole frame unreadable, or the turn never ends.
func TestResultFrameWithMalformedIsErrorStillEndsTheTurn(t *testing.T) {
	for line, wantErr := range map[string]bool{
		`{"type":"result","subtype":"success","is_error":true}`:  true,
		`{"type":"result","subtype":"success","is_error":false}`: false,
		`{"type":"result","subtype":"success","is_error":"yes"}`: false,
		`{"type":"result","subtype":"success","is_error":1}`:     false,
	} {
		frame, ok := parseStreamJSON([]byte(line))
		if !ok || !frame.isResult() {
			t.Fatalf("%s: frame not read as a result (ok=%v)", line, ok)
		}
		if bool(frame.IsError) != wantErr {
			t.Fatalf("%s: is_error=%v, want %v", line, frame.IsError, wantErr)
		}
	}
}
