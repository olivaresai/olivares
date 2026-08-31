// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestResourceFromTool(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		input    map[string]any
		wantKind string
		wantRef  string
		wantMode model.AccessMode
	}{
		{"read", "Read", map[string]any{"file_path": "/etc/hosts"}, resFile, "/etc/hosts", model.ModeRead},
		{"write", "Write", map[string]any{"file_path": "/tmp/out.txt"}, resFile, "/tmp/out.txt", model.ModeWrite},
		{"edit", "Edit", map[string]any{"file_path": "/src/app.go"}, resFile, "/src/app.go", model.ModeWrite},
		{"webfetch", "WebFetch", map[string]any{"url": "https://u:p@host/x?token=abc"}, resHTTP, "https://host/x", model.ModeRead},
		{"websearch-nofield", "WebSearch", map[string]any{"query": "secrets"}, resWeb, resWeb, model.ModeRead},
		{"bash-program-only", "Bash", map[string]any{"command": "FOO=bar psql 'postgres://u:pw@h/db'"}, resShell, "psql", model.ModeUnknown},
		{"mcp", "mcp__github__create_issue", nil, resMCP, "github/create_issue", model.ModeUnknown},
		{"unknown-tool-fallback", "OfficeExport", map[string]any{"x": "y"}, resTool, "OfficeExport", model.ModeUnknown},
		{"known-tool-no-detail", "Read", nil, resTool, "Read", model.ModeRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ref, mode := resourceFromTool(tc.tool, tc.input)
			if kind != tc.wantKind || ref != tc.wantRef || mode != tc.wantMode {
				t.Errorf("resourceFromTool(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tc.tool, kind, ref, mode, tc.wantKind, tc.wantRef, tc.wantMode)
			}
		})
	}
}

func TestShellProgramStripsArgsAndSecrets(t *testing.T) {
	got := shellProgram("AWS_SECRET=AKIAIOSFODNN7EXAMPLE aws s3 ls")
	if got != "aws" {
		t.Errorf("shellProgram = %q, want aws (env-assignment skipped, args dropped)", got)
	}
}

func TestIsHighRiskTool(t *testing.T) {
	cases := []struct {
		kind string
		mode model.AccessMode
		want bool
	}{
		{resFile, model.ModeWrite, true},
		{resMCP, model.ModeReadWrite, true},
		{resShell, model.ModeUnknown, true}, // a shell is high-risk even at unknown mode
		{resFile, model.ModeRead, false},
		{resMCP, model.ModeUnknown, false}, // an unknown-mode connector use is not assumed dangerous
		{resTool, model.ModeUnknown, false},
	}
	for _, tc := range cases {
		if got := isHighRiskTool(tc.kind, tc.mode); got != tc.want {
			t.Errorf("isHighRiskTool(%q,%q) = %v, want %v", tc.kind, tc.mode, got, tc.want)
		}
	}
}

func TestAutoApprovalClassification(t *testing.T) {
	for _, s := range []string{srcConfig, srcHook} {
		if !isAutoApproved(s) {
			t.Errorf("%q should be auto-approved", s)
		}
		if isManualDecision(s) {
			t.Errorf("%q should not be a manual decision", s)
		}
	}
	for _, s := range []string{"user_permanent", "user_temporary", "user_abort", "user_reject"} {
		if isAutoApproved(s) {
			t.Errorf("%q must NOT be auto-approved (it is a human decision)", s)
		}
		if !isManualDecision(s) {
			t.Errorf("%q should be a manual decision", s)
		}
	}
	if isAutoApproved("") {
		t.Error("an empty/unknown source must NOT be treated as auto-approved (fail-safe)")
	}
}
