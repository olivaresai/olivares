// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionActivityShowsCodexContextWindow(t *testing.T) {
	var out bytes.Buffer
	if err := printSessionActivity(&out, json.RawMessage(`{"driver":"codex","context_tokens":1250,"context_window":258400}`), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1250 / 258400 tokens") {
		t.Fatalf("context window missing: %s", out.String())
	}
}

// #1861: `session show --activity` prints what the session's tool is doing, from the
// same engine answer the console's side panel reads; -o json is that answer.
func TestSessionShowActivityPrintsTheEngineAnswer(t *testing.T) {
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) { testSessionShowActivityPrintsTheEngineAnswer(t, driver) })
	}
}

func testSessionShowActivityPrintsTheEngineAnswer(t *testing.T, driver string) {
	t.Helper()
	const fixture = `{"driver":"claude","model":"claude-haiku-5-5","effort":"medium","context_tokens":78582,
"compactions":[{"trigger":"auto"}],
"agents":[{"tool_use_id":"u1","agent_id":"a442c29bf1c636f28","type":"general-purpose","description":"\u001b]52;c;aGk=\u0007Write agent-a.txt","state":"completed","duration_ms":5059}],
"tasks":[{"tool_use_id":"u2","task_id":"blu934u8g","kind":"shell","description":"Run sleep 5","command":"sleep 5; echo finished","state":"running"},{"tool_use_id":"w1","kind":"workflow","description":"Build workflow","state":"running","workflow":{"run_id":"wf_1","name":"Build","agents_total":2,"agents_done":1,"phases":[{"index":1,"title":"Prepare","state":"completed","agents_total":1,"agents_done":1},{"index":2,"title":"\u001b]52;c;aGk=\u0007Finish","detail":"Run final checks","state":"running","agents_total":1,"agents_done":0}]}}],
"files":[{"path":"/home/dev/demo/one.txt","added":1,"removed":1,"edits":1},{"path":"/home/dev/demo/three.txt","added":1,"removed":0,"created":true,"edits":1},{"path":"/elsewhere/x.txt","added":2,"removed":0,"edits":1}],
"todos":[{"content":"Edit the files","status":"completed"},{"content":"Wait for the job","status":"in_progress"}],
"skills":["tdd-workflow"],"mcp_servers":["github"]}`
	model := "claude-haiku-5-5"
	if driver == "codex" {
		model = "gpt-5.5"
	}
	activity := strings.ReplaceAll(strings.ReplaceAll(fixture, `"claude"`, `"`+driver+`"`), "claude-haiku-5-5", model)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": driver, "workspace_path": "/home/dev/demo"}}
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == sessionRunsPath+"/r1/activity" {
			asked++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(activity))
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)

	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "show", "notes", "--activity"}, sessionCreds(srv.URL)...)...)
	if err != nil {
		t.Fatalf("show --activity: %v\n%s", err, errb)
	}
	for _, want := range []string{
		model + " · effort medium", "78582 tokens · compacted 1×", "tdd-workflow", "github",
		"general-purpose", "Write agent-a.txt", "5.1s", "a442c29bf1c636f28",
		"Run sleep 5", "one.txt", "+1 -1", "three.txt", "+1 -0 new", "/elsewhere/x.txt",
		"[x] Edit the files", "[>] Wait for the job",
		"Prepare", "Finish", "Run final checks", "1 of 2 agents done", "0 of 1 agents done",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("activity lacks %q:\n%s", want, out)
		}
	}
	// What the agent wrote never drives the terminal (an OSC 52 would set the clipboard).
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("activity output carries a terminal control sequence: %q", out)
	}
	if strings.Contains(out, "/home/dev/demo/one.txt") {
		t.Fatalf("a file inside the session's folder is not named by its place in it:\n%s", out)
	}

	out, errb, err = execSessionCLI(t, nil, append([]string{"session", "show", "notes", "--activity", "-o", "json"}, sessionCreds(srv.URL)...)...)
	if err != nil {
		t.Fatalf("show --activity -o json: %v\n%s", err, errb)
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	_ = json.Unmarshal([]byte(activity), &want)
	gotRaw, _ := json.Marshal(got)
	wantRaw, _ := json.Marshal(want)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("-o json is not the engine's answer:\n%s\nwant\n%s", gotRaw, wantRaw)
	}
	if asked != 2 {
		t.Fatalf("activity asked %d times, want 2", asked)
	}

	// Without the flag, show is what it was: the run, and no activity read.
	if _, _, err = execSessionCLI(t, nil, append([]string{"session", "show", "notes"}, sessionCreds(srv.URL)...)...); err != nil || asked != 2 {
		t.Fatalf("plain show read the activity (asked %d) or failed: %v", asked, err)
	}
}

func TestSessionActivityWorkflowCountsWithoutPhases(t *testing.T) {
	var out bytes.Buffer
	err := printSessionActivity(&out, json.RawMessage(`{"tasks":[{"kind":"workflow","state":"running","workflow":{"name":"Unphased","agents_total":2,"agents_done":1,"phases":[]}}]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 of 2 agents done") {
		t.Fatalf("unphased workflow lost its progress: %s", out.String())
	}
}

func TestSessionActivityContextWindow(t *testing.T) {
	for _, tc := range []struct{ metadata, want string }{
		{`,"context_window":200000,"context_percent":39.291`, "78582 tokens · 39.3% of 200000"},
		{`,"context_window":1000000,"context_percent":7.8582`, "78582 tokens · 7.9% of 1000000"},
		{`,"context_window":null`, "78582 tokens · window unknown"},
		{"", "78582 tokens · window unknown"},
	} {
		var out bytes.Buffer
		if err := printSessionActivity(&out, json.RawMessage(`{"context_tokens":78582`+tc.metadata+`}`), ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("want %q in %s", tc.want, out.String())
		}
	}
}
