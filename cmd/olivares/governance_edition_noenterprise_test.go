// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCommunityGovernanceHelpOmitsBreakGlass(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"governance", "--help"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\n  breakglass ") {
		t.Fatal("Community help offers break-glass")
	}
	for _, verb := range []string{"ls", "get", "uses"} {
		cmd, _, err := root.Find([]string{"governance", "breakglass", verb})
		if err != nil || cmd == nil || cmd.Name() != verb {
			t.Fatalf("published breakglass %s seam removed: %v", verb, err)
		}
	}
}

func TestCommunityGovernanceBreakGlassRefusesWithoutHTTP(t *testing.T) {
	for _, args := range [][]string{{}, {"ls"}, {"list", "--status", "active", "--limit", "5", "--cursor", "legacy"}, {"get", "legacy"}, {"uses", "legacy"}} {
		spy := newObserveSpy(t, http.StatusOK, "{}")
		_, _, err := execRoot(t, observeArgs(spy.srv.URL, append([]string{"governance", "breakglass"}, args...)...)...)
		if err == nil || exitcode.From(err) != exitcode.Edition {
			t.Errorf("%v refusal = %v, want edition exit", args, err)
		}
		if spy.count() != 0 {
			t.Errorf("%v spent an HTTP request in Community", args)
		}
	}
}

func TestCommunityGovernanceBreakGlassReferenceDescriptions(t *testing.T) {
	dump := buildCLIRefDump()
	for _, path := range []string{
		"olivares governance breakglass",
		"olivares governance breakglass get",
		"olivares governance breakglass ls",
		"olivares governance breakglass uses",
	} {
		t.Run(path, func(t *testing.T) {
			for _, cmd := range dump.Commands {
				if cmd.Path != path {
					continue
				}
				if strings.TrimSpace(cmd.Short) == "" {
					t.Error("preserved Community command has no reference summary")
				}
				if !strings.Contains(cmd.Short, "Business edition") {
					t.Errorf("summary %q must state Business edition availability", cmd.Short)
				}
				if cmd.Hidden != (path == "olivares governance breakglass") {
					t.Errorf("hidden state changed: %v", cmd.Hidden)
				}
				return
			}
			t.Fatal("published breakglass command is absent from the reference tree")
		})
	}
}
