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
	for _, args := range [][]string{{"ls"}, {"get", "legacy"}, {"uses", "legacy"}} {
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
