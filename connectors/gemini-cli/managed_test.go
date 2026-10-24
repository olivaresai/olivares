// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package geminicli

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSettingsJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"governed keys", `{"security":{"disableYoloMode":true},"tools":{"exclude":["run_shell_command"]},"mcp":{"allowed":["a"]},"general":{"defaultApprovalMode":"default"},"telemetry":{"target":"local"}}`, ""},
		{"unknown keys pass", `{"ui":{"theme":"x"}}`, ""},
		{"empty", "", "document is empty"},
		{"not an object", `["a"]`, "must be a JSON object"},
		{"wrong type", `{"security":{"disableYoloMode":"yes"}}`, "wrong type"},
		{"unknown approval mode", `{"general":{"defaultApprovalMode":"yolo"}}`, "defaultApprovalMode must be default, auto_edit or plan"},
		{"unknown telemetry target", `{"telemetry":{"target":"s3"}}`, "telemetry.target must be gcp or local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := ValidateSettingsJSON([]byte(tt.content))
			switch {
			case tt.want == "" && len(issues) != 0:
				t.Fatalf("issues = %v, want none", issues)
			case tt.want != "" && (len(issues) != 1 || !strings.Contains(issues[0], tt.want)):
				t.Fatalf("issues = %v, want one containing %q", issues, tt.want)
			}
		})
	}
}

func TestVerifyDriftJSONFindsAWeakenedHost(t *testing.T) {
	authored := `{"security":{"disableYoloMode":true},"tools":{"exclude":["run_shell_command"]}}`
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	got, err := VerifyDriftJSON("host-1", []byte(authored), []byte(`{"security":{"disableYoloMode":false},"tools":{"exclude":["run_shell_command"]},"ui":{}}`), at)
	if err != nil || len(got) != 1 || got[0].Title != "Gemini CLI: security.disableYoloMode differs from the published policy" || got[0].SubjectKind != "gemini_cli_managed_config" {
		t.Fatalf("yolo re-enabled on the host: err=%v findings=%+v", err, got)
	}
	if got, err := VerifyDriftJSON("host-1", []byte(authored), nil, at); err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "absent on the host") {
		t.Fatalf("no file on the host: err=%v findings=%+v", err, got)
	}
	if _, err := VerifyDriftJSON("host-1", []byte(`{"general":{"defaultApprovalMode":"yolo"}}`), nil, at); err == nil {
		t.Fatal("an invalid authored document must be an error")
	}
}

func TestVerifyDriftJSONOrderMattersOnlyWhereItIsASequence(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	authored := `{"tools":{"exclude":["run_shell_command","write_file"]},"mcpServers":{"x":{"command":"npx","args":["-y","pkg"]}}}`
	reorderedAllowlist := `{"tools":{"exclude":["write_file","run_shell_command"]},"mcpServers":{"x":{"command":"npx","args":["-y","pkg"]}}}`
	if got, err := VerifyDriftJSON("h", []byte(authored), []byte(reorderedAllowlist), at); err != nil || len(got) != 0 {
		t.Fatalf("a reordered tool list is not drift: err=%v findings=%+v", err, got)
	}
	reorderedArgs := `{"tools":{"exclude":["run_shell_command","write_file"]},"mcpServers":{"x":{"command":"npx","args":["pkg","-y"]}}}`
	got, err := VerifyDriftJSON("h", []byte(authored), []byte(reorderedArgs), at)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "mcpServers.x.args differs") {
		t.Fatalf("reordered args are a different command: err=%v findings=%+v", err, got)
	}
}
