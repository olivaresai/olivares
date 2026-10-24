// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"strings"
	"testing"
	"time"
)

// The typed comparison skips what the connector does not model; the file comparison must not.
func TestFileComparisonCatchesWhatTheTypedModelDoesNot(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

	// A file that sets only a key the model does not hold: the typed comparison has
	// nothing to compare and reports nothing, even for a host with no file at all.
	only := "model = \"gpt-5\"\n"
	if typed := VerifyDriftTOML("h", Policy{}, nil, nil, at); len(typed) != 0 {
		t.Fatalf("premise: an empty typed policy compares nothing, got %d finding(s)", len(typed))
	}
	for name, observed := range map[string]string{"absent": "", "different": "model = \"o3\"\n", "unset": "approval_policy = \"never\"\n"} {
		got, err := VerifyManagedConfigFileTOML("h", []byte(only), []byte(observed), at)
		if err != nil || len(got) != 1 || got[0].SubjectKind != originManagedConfig {
			t.Errorf("%s host: err=%v findings=%+v, want one drift finding", name, err, got)
		}
	}
	if got, err := VerifyManagedConfigFileTOML("h", []byte(only), []byte("model = \"gpt-5\"\nextra = 1\n"), at); err != nil || len(got) != 0 {
		t.Errorf("a host that sets the key and adds its own: err=%v findings=%+v", err, got)
	}
}

func TestRequirementsFileComparisonTreatsTheMCPTableAsAnAllowlist(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	authored := "allowed_approval_policies = [\"on-request\", \"untrusted\"]\n[mcp_servers.files]\nidentity = { command = \"files\" }\n"
	reordered := "allowed_approval_policies = [\"untrusted\", \"on-request\"]\n[mcp_servers.files]\nidentity = { command = \"files\" }\n"
	if got, err := VerifyRequirementsFileTOML("h", []byte(authored), []byte(reordered), at); err != nil || len(got) != 0 {
		t.Fatalf("a reordered approval allowlist is not drift: err=%v %+v", err, got)
	}
	extra := reordered + "[mcp_servers.shell]\nidentity = { command = \"sh\" }\n"
	got, err := VerifyRequirementsFileTOML("h", []byte(authored), []byte(extra), at)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Title, "mcp_servers.shell is on the host but not in the published policy") {
		t.Fatalf("an MCP server the host adds widens the allowlist: err=%v %+v", err, got)
	}
}
