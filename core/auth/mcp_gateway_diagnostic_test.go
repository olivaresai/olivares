// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"encoding/json"
	"testing"
)

func TestMCPGatewayProbeRejectsMultilineProcessDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"one_line", `{"state":"unreachable","tools":[],"reason":"process_exit","detail":"permission denied reading /fixture/server.py"}`, true},
		{"newline", `{"state":"unreachable","tools":[],"reason":"process_exit","detail":"first\nsecond"}`, false},
		{"terminal_control", `{"state":"unreachable","tools":[],"reason":"process_start","detail":"\u001b[31merror"}`, false},
		{"successful_probe", `{"state":"ok","tools":[],"detail":"stale failure"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probe MCPGatewayProbe
			if err := json.Unmarshal([]byte(tc.raw), &probe); err != nil {
				t.Fatal(err)
			}
			if validMCPGatewayProbe(probe, false) != tc.want {
				t.Fatal("probe did not enforce a bounded one-line process diagnosis")
			}
		})
	}
}
