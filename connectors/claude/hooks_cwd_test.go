// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// hookBody is a PostToolUse hook input as Claude Code sends it, with its cwd.
func hookBody(t *testing.T, cwd string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id": "s", "hook_event_name": "PostToolUse", "tool_name": "Read",
		"tool_use_id": "tu_1", "tool_input": map[string]any{"file_path": "/etc/hosts"}, "cwd": cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Claude Code sends the working directory in every hook input (`cwd`, common
// fields, code.claude.com/docs/en/hooks). The connector keeps the folder's own name
// as the session's project and never the path: a path can carry a user name or a
// client's directory layout, and the list only needs the name to tell sessions apart.
func TestParseHookKeepsOnlyTheFolderOfCwd(t *testing.T) {
	cases := []struct{ name, cwd, want string }{
		{"absolute path", "/srv/work/billing-api", "billing-api"},
		{"trailing slash", "/srv/work/billing-api/", "billing-api"},
		{"doubled separators", "/srv//work//billing-api//", "billing-api"},
		{"windows path", `C:\work\billing-api`, "billing-api"},
		{"windows trailing backslash", `C:\work\billing-api\`, "billing-api"},
		{"root has no name", "/", ""},
		{"windows drive root", `C:\`, ""},
		{"bare drive", "C:", ""},
		{"absent", "", ""},
		{"control and bidi runes are dropped", "/srv/\u202eevil\nname\u2028", "evilname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := parseHook(hookBody(t, tc.cwd), testTime)
			if !ok {
				t.Fatal("hook did not parse")
			}
			if ev.project != tc.want {
				t.Errorf("project = %q, want %q", ev.project, tc.want)
			}
		})
	}
}

// A folder named like a credential is scrubbed like every other label.
func TestProjectOfScrubsASecretShapedFolder(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	ev, _ := parseHook(hookBody(t, "/srv/work/"+token), testTime)
	if ev.project == "" || strings.Contains(ev.project, token) {
		t.Errorf("project = %q: want a placeholder, never the token", ev.project)
	}
}

// The hook's own JSON reaches the edge, whichever side of the call arrives first, and
// when the hook is the only side. Without the link from the parsed hook to the signal
// every other test in this file would still pass.
func TestHookCwdReachesTheCorrelatedEdge(t *testing.T) {
	hookSignal := func(t *testing.T) toolSignal {
		ev, ok := parseHook(hookBody(t, "/srv/work/billing-api"), testTime)
		if !ok {
			t.Fatal("hook did not parse")
		}
		return toolSignalFromHook(ev)
	}
	otel := otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead)

	for _, tc := range []struct {
		name  string
		offer func(t *testing.T, cr *correlator)
	}{
		{"otel then hook", func(t *testing.T, cr *correlator) {
			cr.offer(otel, testTime)
			cr.offer(hookSignal(t), testTime)
		}},
		{"hook then otel", func(t *testing.T, cr *correlator) {
			cr.offer(hookSignal(t), testTime)
			cr.offer(otel, testTime)
		}},
		{"hook only, swept", func(t *testing.T, cr *correlator) {
			cr.offer(hookSignal(t), testTime)
			cr.sweep(testTime.Add(10 * time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &collect{}
			cr := newCorrelator(5*time.Second, c.emit)
			tc.offer(t, cr)
			edges := c.edges()
			if len(edges) != 1 {
				t.Fatalf("want one edge, got %d", len(edges))
			}
			if got := edges[0].Labels[model.LabelProjectFolder]; got != "billing-api" {
				t.Errorf("project label = %q, want billing-api", got)
			}
			if got := edges[0].Labels[model.LabelEngine]; got != model.EngineClaude {
				t.Errorf("engine label = %q, want %q", got, model.EngineClaude)
			}
		})
	}
}

func TestEdgeWithoutAHookProjectHasNoProjectLabel(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(time.Second, c.emit)
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.sweep(testTime.Add(2 * time.Second))
	edges := c.edges()
	if len(edges) != 1 {
		t.Fatalf("want one edge, got %d", len(edges))
	}
	if v, ok := edges[0].Labels[model.LabelProjectFolder]; ok {
		t.Errorf("an edge that learned no folder must not invent a project label, got %q", v)
	}
}

// An operator cannot assert the folder through the resource_labels allowlist: the hook is
// the only producer, so two sources never race for the same row (the operator's `project`
// stays a FinOps dimension on cost samples).
func TestOperatorCannotAllowlistTheFolderLabel(t *testing.T) {
	if !builtinAttrKeys[model.LabelProjectFolder] {
		t.Errorf("%q must be a built-in key an allowlist entry cannot collect", model.LabelProjectFolder)
	}
}

// The governed hook endpoint hands the decider the same folder the telemetry path reads.
func TestHookPEPPassesTheFolderToTheDecider(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(hookBody(t, "/srv/work/billing-api")))
	req.Header.Set("Content-Type", "application/json")
	serve(t, pep, req)
	if dec.gotInput.Project != "billing-api" {
		t.Errorf("decider saw project %q, want billing-api", dec.gotInput.Project)
	}
}
