// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// #2129: `session messages` lists, searches and anchors the messages of a session
// from the engine's read, the one the console's navigator uses.
func TestSessionMessagesAsksTheEngineAndPrintsThePage(t *testing.T) {
	const answer = `{"driver":"claude","transcript":"read","total":120,
"items":[{"id":1,"role":"user","at":"2026-10-14T10:00:00.000Z","first_line":"Plan the migration"},
{"id":3,"role":"user","at":"2026-10-14T11:30:00.000Z","first_line":"\u001b]52;c;aGk=\u0007Now the rollback"}],
"next":"3"}`
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "claude"}}
	var asked []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == sessionRunsPath+"/r1/messages" {
			asked = append(asked, r.URL.Query())
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(answer))
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, error) {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append(append([]string{"session", "messages", "notes"}, args...), sessionCreds(srv.URL)...)...)
		if err != nil {
			t.Logf("stderr: %s", errb)
		}
		return out, err
	}

	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04:05")
	for _, want := range []string{"ID", "Plan the migration", at, "user", "Now the rollback", "2 of 120 messages.", "More: --cursor 3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("page lacks %q:\n%s", want, out)
		}
	}
	// Going on from a page read from the newest end says so: the same cursor read the
	// other way would page forward.
	newestOut, err := run("--newest")
	if err != nil || !strings.Contains(newestOut, "More: --newest --cursor 3") {
		t.Fatalf("newest page hint = %v\n%s", err, newestOut)
	}
	// What the transcript holds never drives the terminal.
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("output carries a terminal control sequence: %q", out)
	}
	if len(asked) < 1 || len(asked[0]) != 0 {
		t.Fatalf("a plain list sent %v, want no parameters", asked)
	}

	// Every flag reaches the engine's read; a local time is sent as an instant.
	if _, err = run("--search", "roll back", "--at", "2026-10-14T10:30:00+02:00", "--all", "--newest", "--limit", "5", "--cursor", "40"); err != nil {
		t.Fatal(err)
	}
	got := asked[len(asked)-1]
	for k, want := range map[string]string{"q": "roll back", "at": "2026-10-14T08:30:00Z", "role": "all", "order": "desc", "limit": "5", "cursor": "40"} {
		if got.Get(k) != want {
			t.Fatalf("engine was asked %s=%q, want %q (all: %v)", k, got.Get(k), want, got)
		}
	}
	if _, err = run("--at", "2026-10-14 10:30"); err != nil {
		t.Fatal(err)
	}
	local := asked[len(asked)-1].Get("at")
	if want := time.Date(2026, 10, 14, 10, 30, 0, 0, time.Local).UTC().Format(time.RFC3339); local != want {
		t.Fatalf("a local time was sent as %q, want %q", local, want)
	}
	if _, err = run("--at", "2026-10-14"); err != nil {
		t.Fatal(err)
	}

	// -o json is the engine's answer.
	out, err = run("-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON map[string]any
	if err := json.Unmarshal([]byte(out), &gotJSON); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	_ = json.Unmarshal([]byte(answer), &wantJSON)
	gotRaw, _ := json.Marshal(gotJSON)
	wantRaw, _ := json.Marshal(wantJSON)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("-o json is not the engine's answer:\n%s\nwant\n%s", gotRaw, wantRaw)
	}
}

func TestSessionMessagesPrintsOneMessageWholeAndSaysWhenThereAreNone(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "claude"}}
	answers := map[string]struct {
		code int
		body string
	}{
		"id=7":     {200, `{"transcript":"read","total":9,"unreadable":2,"items":[{"id":7,"role":"user","at":"2026-10-14T10:00:00Z","first_line":"First","text":"First line\nand \u001b]52;c;aGk=\u0007the second line"}]}`},
		"id=99":    {404, `{"error":"this session has no message with that id"}`},
		"q=absent": {200, `{"transcript":"read","total":0,"items":[]}`},
		"pending":  {200, `{"transcript":"pending","total":0,"items":[]}`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != sessionRunsPath+"/r1/messages" {
			f.serve(w, r)
			return
		}
		key := r.URL.RawQuery
		if key == "" {
			key = "pending"
		}
		a, ok := answers[key]
		if !ok {
			t.Errorf("unexpected query %q", key)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.code)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, error) {
		out, _, err := execSessionCLI(t, nil, append(append([]string{"session", "messages", "notes"}, args...), sessionCreds(srv.URL)...)...)
		return out, err
	}

	out, err := run("--id", "7")
	if err != nil || !strings.Contains(out, "First line\nand the second line") || !strings.Contains(out, "7") {
		t.Fatalf("one message = %v\n%s", err, out)
	}
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "2 records of the transcript could not be read.") {
		t.Fatalf("one message whole must be terminal-safe and say what was left out: %q", out)
	}
	if _, err = run("--id", "99"); err == nil || exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("an unknown id = %v (exit %d), want not found", err, exitcode.From(err))
	}
	if out, err = run("--search", "absent"); err != nil || !strings.Contains(out, "No messages match.") {
		t.Fatalf("no match = %v\n%s", err, out)
	}
	if out, err = run(); err != nil || !strings.Contains(out, "has not written its transcript") {
		t.Fatalf("a transcript not there yet = %v\n%s", err, out)
	}
}

func TestSessionMessagesRefusesWhatItCannotAsk(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "claude"}}
	for _, args := range [][]string{
		{"--at", "yesterday"}, {"--limit", "0"}, {"--limit", "-3"}, {"--id", "0"}, {"--cursor", "-1"}, {"--limit", "many"},
		// One message whole takes no filter: the engine would ignore it silently.
		{"--id", "3", "--search", "x"}, {"--id", "3", "--all"}, {"--id", "3", "--limit", "5"},
	} {
		_, _, err := execSessionCLI(t, nil, append(append([]string{"session", "messages", "notes"}, args...), sessionCreds(f.URL)...)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%v = %v (exit %d), want a usage error", args, err, exitcode.From(err))
		}
	}
	for _, hit := range f.hits {
		if strings.HasSuffix(hit, "/messages") {
			t.Fatalf("a refused invocation still asked the engine: %v", f.hits)
		}
	}
}
