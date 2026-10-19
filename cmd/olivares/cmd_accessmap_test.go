// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// The help text documented `--mode r|rw`, but the engine's wire values are read,
// write and readwrite, so the documented command filtered on a value no edge has
// and answered "no access edges match". Both spellings must reach the engine as
// the wire value.
func TestAccessMapModeFlagSendsTheWireValue(t *testing.T) {
	for _, verb := range []string{"graph", "drift"} {
		for flag, want := range map[string]string{
			"r": "read", "w": "write", "rw": "readwrite",
			"read": "read", "write": "write", "readwrite": "readwrite",
		} {
			spy := newObserveSpy(t, http.StatusOK, `{"nodes":[],"edges":[],"unexpected_accesses":[],"unused_grants":[]}`)
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", verb, "--mode", flag)...); err != nil {
				t.Fatalf("accessmap %s --mode %s failed: %v", verb, flag, err)
			}
			if got := spy.last(t).query.Get("mode"); got != want {
				t.Errorf("accessmap %s --mode %s sent mode=%q, want %q", verb, flag, got, want)
			}
		}
	}
}

// drift reconciles the whole window; the engine never read --limit or --cursor, so
// a script that paged on them looped on the same answer. The flags stay accepted
// (published) but are deprecated, say so, and are no longer sent.
func TestAccessMapDriftPagingFlagsAreDeprecatedNoOps(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"unexpected_accesses":[],"unused_grants":[]}`)
	out, errOut, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "drift", "--limit", "5", "--cursor", "abc")...)
	errOut += out // cobra prints a deprecated-flag notice through the command's output writer
	if err != nil {
		t.Fatalf("a published flag must keep working: %v", err)
	}
	q := spy.last(t).query
	if q.Has("limit") || q.Has("cursor") {
		t.Errorf("drift sent paging parameters the engine ignores: %v", q)
	}
	if !strings.Contains(errOut, "--limit") || !strings.Contains(errOut, "deprecated") {
		t.Errorf("the ignored flag must say so on stderr, got %q", errOut)
	}
}

// A summary or path list the engine cut short must say so; "0" and "no paths found"
// from a capped read are a measurement that was not made.
func TestAccessMapAttackPathAnswersShowTruncation(t *testing.T) {
	t.Run("summary", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"total_agents":1,"reachable_paths":1000,"escalation_paths":2,"exfil_routes":0,"truncated":true}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "attack-paths", "summary")...)
		if err != nil || !strings.Contains(out, "TRUNCATED") {
			t.Errorf("a truncated summary must be labeled, got %v:\n%s", err, out)
		}
	})
	t.Run("paths", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"paths":[],"truncated":true}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "attack-paths", "escalation", "--agent-id", "ag-7")...)
		if err != nil || !strings.Contains(out, "TRUNCATED") {
			t.Errorf("a truncated path list must be labeled, got %v:\n%s", err, out)
		}
		if !strings.Contains(out, "PARTIAL") {
			t.Errorf("a cut-short answer must carry the partial-result warning:\n%s", out)
		}
	})
}

// Paging stays on the views whose engine route reads it: only drift stopped sending it.
func TestAccessMapGraphStillSendsPaging(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"nodes":[],"edges":[]}`)
	if _, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "graph", "--limit", "7", "--cursor", "abc")...); err != nil {
		t.Fatal(err)
	}
	q := spy.last(t).query
	if q.Get("limit") != "7" || q.Get("cursor") != "abc" {
		t.Errorf("graph must keep sending limit and cursor, got %v", q)
	}
}

// A truncated list that does hold paths still ends with the label, and the counts the
// engine sent are printed as sent.
func TestAccessMapPathListAndSummaryKeepTheirNumbersWhenTruncated(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"paths":[{"kind":"escalation","steps":[{"node_kind":"agent","node_id":"a","node_name":"planner"},{"node_kind":"agent","node_id":"b","node_name":"deployer"}],"attribution":"firm","min_confidence":"attributed"}],"truncated":true}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "attack-paths", "escalation", "--agent-id", "a")...)
	if err != nil || !strings.Contains(out, "planner -> deployer") || !strings.Contains(out, "TRUNCATED") {
		t.Errorf("a partial list must show its rows and the label: %v\n%s", err, out)
	}

	spy = newObserveSpy(t, http.StatusOK, `{"total_agents":4,"reachable_paths":9,"escalation_paths":3,"exfil_routes":2,"critical_agents":1,"sensitive_targets":5,"truncated":true}`)
	out, _, err = execRoot(t, observeArgs(spy.srv.URL, "accessmap", "attack-paths", "summary")...)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{"escalation paths", "exfil routes"} {
		if !strings.Contains(out, row) {
			t.Errorf("summary lost its %q row:\n%s", row, out)
		}
	}
	if !strings.Contains(out, "3") || !strings.Contains(out, "2") || !strings.Contains(out, "TRUNCATED") {
		t.Errorf("summary must print the engine's counts and the label:\n%s", out)
	}
}

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

// Published descriptions must explain the answers rather than restating a route
// template. Read the actual offline OpenAPI document, as an integrator does.
func TestAccessMapAttackPathDescriptionsExplainTheAnswer(t *testing.T) {
	doc, err := moduleOpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var published struct {
		Paths map[string]map[string]struct {
			Description string `json:"description"`
			Summary     string `json:"summary"`
		}
	}
	if err := json.Unmarshal(encoded, &published); err != nil {
		t.Fatal(err)
	}
	for _, analysis := range []string{"reachability", "escalation", "exfil", "summary"} {
		path := "/v1/m/accessmap/attack-paths/" + analysis
		op := published.Paths[path]["get"]
		for _, description := range []string{op.Summary, op.Description} {
			if description == "" || strings.HasPrefix(description, "Serves GET") || strings.Contains(description, "/attack-paths/") {
				t.Errorf("%s summary/description does not explain its answer: %q", path, description)
			}
		}
	}
}
