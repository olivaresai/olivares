// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"
	"testing"
)

// `session start --from <github url>` reads the issue or pull request through the
// engine first, starts the session from it, and sends its title and body as the
// first message. A start without the flag is the request it always was.

const (
	fromIssueURL = "https://github.com/acme/widgets/issues/17"
	fromRunInput = sessionRunsPath + "/01a0f6dc-0000-7000-8000-000000000009/input"
)

func fromItem() map[string]any {
	return map[string]any{
		"url": fromIssueURL, "kind": "issue", "number": 17,
		"title": "Crash on save", "body": "Steps:\n1. open a file\n2. save",
	}
}

func TestSessionStartFromAnItemSendsItsTextAsTheFirstMessage(t *testing.T) {
	f := newFakeSessionEngine(t, func(f *fakeSessionEngine) { f.item = fromItem() })
	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--from", fromIssueURL + "#issuecomment-5", "--detach"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start --from: %v\n%s", err, errb)
	}
	if len(f.itemQueries) != 1 || f.itemQueries[0].Get("url") != fromIssueURL+"#issuecomment-5" || f.itemQueries[0].Get("workspace_ref") != "ws-new" {
		t.Fatalf("item reads = %v; want one for the page given, in the folder's workspace", f.itemQueries)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 || runs[0]["source_url"] != fromIssueURL+"#issuecomment-5" {
		t.Fatalf("launches = %v; want one carrying source_url", runs)
	}
	if _, present := runs[0]["repository_url"]; present {
		t.Fatalf("a start from an item sent repository_url: %v", runs[0])
	}
	in := f.postsTo(fromRunInput)
	if len(in) != 1 {
		t.Fatalf("%d messages sent; want the item's text once", len(in))
	}
	sent := fmt.Sprint(in[0])
	for _, want := range []string{"Crash on save", "Steps:", "2. save", fromIssueURL} {
		if !strings.Contains(sent, want) {
			t.Errorf("the first message %q does not carry %q", sent, want)
		}
	}
	if order := f.hits; indexOf(order, "GET "+sessionSourceItemsPath) > indexOf(order, "POST "+sessionRunsPath) {
		t.Fatalf("hits = %v; the item must be read before the session exists, so a failed read leaves none", order)
	}
}

// A prompt given as well follows the item's text in the same first message.
func TestSessionStartFromAnItemKeepsThePromptAfterIt(t *testing.T) {
	f := newFakeSessionEngine(t, func(f *fakeSessionEngine) { f.item = fromItem() })
	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "only touch the parser", "--from", fromIssueURL, "--detach"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start --from with a prompt: %v\n%s", err, errb)
	}
	in := f.postsTo(fromRunInput)
	if len(in) != 1 {
		t.Fatalf("%d messages sent; want one", len(in))
	}
	sent := fmt.Sprint(in[0])
	if i, j := strings.Index(sent, "Crash on save"), strings.Index(sent, "only touch the parser"); i < 0 || j < i {
		t.Fatalf("the first message %q must carry the item's text, then the prompt", sent)
	}
}

// What the engine cannot read is said in its own sentence, and no session is made.
func TestSessionStartFromAnItemStopsWhenTheItemCannotBeRead(t *testing.T) {
	const sentence = "no approved GitHub repository binding lists acme/widgets for this workspace"
	f := newFakeSessionEngine(t, func(f *fakeSessionEngine) { f.itemRefusal = sentence })
	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--from", fromIssueURL}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error()+errb, sentence) {
		t.Fatalf("start --from = %v / %q; want the engine's sentence", err, errb)
	}
	if n := len(f.postsTo(sessionRunsPath)); n != 0 {
		t.Fatalf("%d sessions were made after the item could not be read", n)
	}
}

// --from is a GitHub page, and it does not mix with an engine checkout.
func TestSessionStartFromRefusesWhatItCannotMean(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", "--repository", "https://github.com/acme/widgets", "--from", fromIssueURL}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("--from with --repository = %v; want a usage sentence naming --from", err)
	}
	if len(f.hits) != 0 {
		t.Fatalf("the engine was reached after a usage error: %v", f.hits)
	}
}

func TestSessionStartWithoutFromReadsNoItem(t *testing.T) {
	f := newFakeSessionEngine(t)
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir()}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("start: %v\n%s", err, errb)
	}
	if len(f.itemQueries) != 0 {
		t.Fatalf("a start without --from read an item: %v", f.itemQueries)
	}
	if runs := f.postsTo(sessionRunsPath); len(runs) != 1 {
		t.Fatalf("%d launches", len(runs))
	} else if _, present := runs[0]["source_url"]; present {
		t.Fatalf("a start without --from sent source_url: %v", runs[0])
	}
}

// session show names the page a session was started from, and only for such a session.
func TestSessionShowNamesTheItemASessionStartedFrom(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "fix", "state": "running", "provider_driver": "claude", "source_url": fromIssueURL},
		{"run_ref": "r2", "name": "plain", "state": "running", "provider_driver": "claude"},
	}
	show := func(name string) string {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append([]string{"session", "show", name}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("show %s: %v\n%s", name, err, errb)
		}
		return out
	}
	if out := show("fix"); !strings.Contains(out, "FROM") || !strings.Contains(out, fromIssueURL) {
		t.Fatalf("the page a session started from is missing:\n%s", out)
	}
	if out := show("plain"); strings.Contains(out, "FROM") {
		t.Fatalf("a session started any other way shows a FROM row:\n%s", out)
	}
}

func indexOf(hits []string, want string) int {
	for i, h := range hits {
		if h == want {
			return i
		}
	}
	return -1
}
