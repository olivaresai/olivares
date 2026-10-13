// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The --mode filter is matched by the store against the wire values of
// sdkmodel.AccessMode. A help line or example that names any other spelling sends
// the operator to a command that exits 0 with "no access edges match this query".
func TestAccessMapModeHelpNamesTheWireValues(t *testing.T) {
	root := newAccessMapCmd()
	modeArg := regexp.MustCompile(`--mode[ =](\S+)`)
	for _, name := range []string{"graph", "drift"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Fatalf("accessmap %s not found: %v", name, err)
		}
		flag := cmd.Flags().Lookup("mode")
		if flag == nil {
			t.Fatalf("accessmap %s has no --mode flag", name)
		}
		// Whole tokens: "read" and "write" are both substrings of "readwrite".
		listed := map[string]bool{}
		if m := regexp.MustCompile(`\(([^)]*)\)`).FindStringSubmatch(flag.Usage); m != nil {
			for _, v := range strings.Split(m[1], ",") {
				listed[strings.TrimSpace(v)] = true
			}
		}
		for _, want := range []sdkmodel.AccessMode{sdkmodel.ModeRead, sdkmodel.ModeWrite, sdkmodel.ModeReadWrite, sdkmodel.ModeUnknown} {
			if !listed[string(want)] {
				t.Errorf("accessmap %s --mode help %q does not name the wire value %q", name, flag.Usage, want)
			}
		}
		for v := range listed {
			if !sdkmodel.AccessMode(v).Valid() {
				t.Errorf("accessmap %s --mode help names %q, which is not an access mode", name, v)
			}
		}
		for _, m := range modeArg.FindAllStringSubmatch(cmd.Example, -1) {
			if !sdkmodel.AccessMode(m[1]).Valid() {
				t.Errorf("accessmap %s example uses --mode %s, which is not an access mode the store matches", name, m[1])
			}
		}
	}
}

// The engine marks an analysis it capped. The CLI must say so beside the answer,
// including an empty one: an under-count of attack paths reads as good news.
func TestAccessMapAttackPathsSaysWhenTheEngineCappedTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		args      []string
		wantNote  bool
		wantInOut string
	}{
		{"empty and capped", `{"paths":[],"truncated":true}`, []string{"accessmap", "attack-paths", "escalation", "--agent-id", "ag-7"}, true, "no escalation paths found"},
		{"empty and complete", `{"paths":[]}`, []string{"accessmap", "attack-paths", "escalation", "--agent-id", "ag-7"}, false, "no escalation paths found"},
		{"summary capped", `{"total_agents":2,"escalation_paths":100,"truncated":true}`, []string{"accessmap", "attack-paths", "summary"}, true, "escalation paths"},
		{"summary complete", `{"total_agents":2,"escalation_paths":1}`, []string{"accessmap", "attack-paths", "summary"}, false, "escalation paths"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, tc.body)
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			if err != nil {
				t.Fatalf("command failed: %v", err)
			}
			if !strings.Contains(out, tc.wantInOut) {
				t.Errorf("output %q does not contain %q", out, tc.wantInOut)
			}
			if got := strings.Contains(out, "TRUNCATED"); got != tc.wantNote {
				t.Errorf("TRUNCATED note present = %v, want %v in %q", got, tc.wantNote, out)
			}
		})
	}
}
