// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package main

import (
	"bytes"
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"strings"
	"testing"
)

func TestCommunityOrchestrationCLIUnavailable(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "  orchestration ") {
		t.Fatal("Business orchestration advertised in Community help")
	}
	for _, args := range [][]string{{"orchestration", "graph"}, {"orchestration", "workflow", "run", "stored-id"}, {"orchestration", "schedules", "--json"}} {
		cmd := newRootCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); exitcode.From(err) != exitcode.Edition {
			t.Fatalf("%v: got %v, want edition exit", args, err)
		}
	}
}
