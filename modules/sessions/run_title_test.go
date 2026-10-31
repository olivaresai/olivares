// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// A title is cut from the first message on this host: what the person wrote, trimmed
// to a short phrase, with nothing sent anywhere.
func TestTitleFromMessage(t *testing.T) {
	long := strings.Repeat("migrate ", 30)
	for _, tc := range []struct{ name, text, style, want string }{
		{"first line", "Plan the migration\nthen list the risks", "", "Plan the migration"},
		{"first sentence", "Fix the login bug. It fails on Safari.", "", "Fix the login bug"},
		{"a short first sentence does not end the title", "Hello. Please refactor the login flow", "", "Hello. Please refactor the login flow"},
		{"keeps a question mark", "why does this panic? it worked before", "", "why does this panic?"},
		{"heading marker", "  ## Add dark mode  ", "", "Add dark mode"},
		{"list marker", "1. Rename the module", "", "Rename the module"},
		{"emphasis and code marks", "**Bump** the `go` version", "", "Bump the go version"},
		{"skips a code block", "```go\nfmt.Println()\n```\nwhy does this panic", "", "why does this panic"},
		{"skips blank lines", "\n\n  \nHello there", "", "Hello there"},
		{"one space between words", "deploy\t the   api", "", "deploy the api"},
		{"drops invisible characters", "deploy​ prod‮ now", "", "deploy prod now"},
		{"version dots stay", "Upgrade to v1.2 today", "", "Upgrade to v1.2 today"},
		{"short cuts at a word", "Refactor the authentication middleware to use the new session store", titleStyleShort, "Refactor the authentication…"},
		{"normal cuts at a word", "Refactor the authentication middleware to use the new session store today please", titleStyleSentence, "Refactor the authentication middleware to use the new…"},
		{"long keeps more", "Refactor the authentication middleware to use the new session store today please", titleStyleLong, "Refactor the authentication middleware to use the new session store today please"},
		{"only code", "```\nrm -rf build\n```", "", ""},
		{"empty", "  \n ", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleFromMessage(tc.text, tc.style); got != tc.want {
				t.Fatalf("titleFromMessage(%q, %q) = %q, want %q", tc.text, tc.style, got, tc.want)
			}
		})
	}
	for style, limit := range titleLimits {
		got := titleFromMessage(long, style)
		if n := utf8.RuneCountInString(got); n == 0 || n > limit || checkRunName(got) != nil {
			t.Fatalf("style %s: %q is %d runes, limit %d", style, got, n, limit)
		}
	}
}

// titleTranscript opens with a message long enough for the styles to cut it differently.
const titleTranscript = `{"type":"user","timestamp":"2026-10-14T10:00:00.000Z","message":{"content":"Refactor the authentication middleware to use the new session store today please\nthen run the tests"}}
{"type":"assistant","timestamp":"2026-10-14T10:00:05.000Z","message":{"content":[{"type":"text","text":"On it."}]}}
{"type":"user","timestamp":"2026-10-14T10:05:00.000Z","message":{"content":"Second message, not the title"}}
`

// POST /runs/{ref}/title cuts the title from the session's first message. The name,
// the person's handle for the session, is never touched; the title is shown only. A
// regenerate follows the style asked and the ledger says a title was made without
// repeating it.
func TestRunTitleIsCutFromTheFirstMessage(t *testing.T) {
	f := messagesFixture(t, titleTranscript)
	path := "/v1/m/sessions/runs/" + f.runRef + "/title"
	post := func(body any) resp { return f.h.doJSON("POST", path, f.admin, body, f.hdr) }
	const normal = "Refactor the authentication middleware to use the new…"

	r := post(map[string]any{})
	if r.code != http.StatusOK || r.body["title"] != normal || r.body["run_ref"] != f.runRef {
		t.Fatalf("generate = %d %s", r.code, r.raw)
	}
	if _, named := r.body["name"]; named {
		t.Fatalf("a title must not become the session's name: %s", r.raw)
	}
	if got := f.h.do("GET", "/v1/m/sessions/runs/"+f.runRef, f.admin, f.hdr); got.body["title"] != normal {
		t.Fatalf("read after generate = %d %s", got.code, got.raw)
	}
	if r := post(map[string]any{"style": titleStyleShort}); r.code != http.StatusOK || r.body["title"] != "Refactor the authentication…" {
		t.Fatalf("regenerate short = %d %s", r.code, r.raw)
	}
	if r := post(map[string]any{"style": titleStyleLong}); r.code != http.StatusOK ||
		r.body["title"] != "Refactor the authentication middleware to use the new session store today please" {
		t.Fatalf("regenerate long = %d %s", r.code, r.raw)
	}
	// The same title again changes and records nothing.
	if r := post(map[string]any{"style": titleStyleLong}); r.code != http.StatusOK {
		t.Fatalf("repeat = %d %s", r.code, r.raw)
	}

	// A rename leaves the title, and a title leaves the name.
	if r := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.runRef+"/name", f.admin, map[string]any{"name": "migration"}, f.hdr); r.code != http.StatusOK || r.body["title"] == nil {
		t.Fatalf("rename = %d %s", r.code, r.raw)
	}
	if r := post(map[string]any{"style": titleStyleShort}); r.code != http.StatusOK || r.body["name"] != "migration" {
		t.Fatalf("regenerate on a named run = %d %s", r.code, r.raw)
	}

	for _, body := range []map[string]any{{"style": "poetic"}, {"extra": true}} {
		if r := post(body); r.code != http.StatusBadRequest {
			t.Fatalf("generate %v = %d %s, want 400", body, r.code, r.raw)
		}
	}
	viewer := f.h.viewerToken(f.admin, f.tenant, "v@title.io")
	if r := f.h.doJSON("POST", path, viewer, map[string]any{}, f.hdr); r.code != http.StatusForbidden {
		t.Fatalf("viewer generate = %d %s, want 403", r.code, r.raw)
	}
	if r := f.h.doJSON("POST", "/v1/m/sessions/runs/run-absent/title", f.admin, map[string]any{}, f.hdr); r.code != http.StatusNotFound {
		t.Fatalf("generate on an absent run = %d %s, want 404", r.code, r.raw)
	}

	events := runAuditEvents(t, f.h.st, f.tenant, "sessions.run.title_generated")
	if len(events) != 4 {
		t.Fatalf("title audit rows = %d, want 4 (normal, short, long, short again; the repeat of long records nothing)", len(events))
	}
	for _, e := range events {
		if e.TargetKind != runKind || !strings.Contains(e.meta, `"run_ref":"`+f.runRef+`"`) || !strings.HasPrefix(e.Actor, "user:") || e.ActorKind != model.ActorUser {
			t.Fatalf("audit row = %+v", e)
		}
		if strings.Contains(e.meta, "Refactor") {
			t.Fatalf("the audit row repeats the title: %s", e.meta)
		}
	}
}

// if_untitled makes a title only for a session that has neither name nor title, and
// answers for one that has either from its row, without reading its transcript.
func TestRunTitleIfUntitledLeavesAName(t *testing.T) {
	f := messagesFixture(t, titleTranscript)
	path := "/v1/m/sessions/runs/" + f.runRef + "/title"
	ask := func() resp {
		return f.h.doJSON("POST", path, f.admin, map[string]any{"if_untitled": true, "style": titleStyleShort}, f.hdr)
	}

	if r := ask(); r.code != http.StatusOK || r.body["title"] != "Refactor the authentication…" {
		t.Fatalf("if_untitled on an untitled run = %d %s", r.code, r.raw)
	}
	// The title is there now: a later style is not applied.
	if r := f.h.doJSON("POST", path, f.admin, map[string]any{"if_untitled": true, "style": titleStyleLong}, f.hdr); r.code != http.StatusOK || r.body["title"] != "Refactor the authentication…" {
		t.Fatalf("if_untitled on a titled run = %d %s", r.code, r.raw)
	}

	// A named run with no title, and no transcript to read: answered from the row.
	seedRunRows(t, f.h.st, f.tenant, stateRunning, "run-named")
	if r := f.h.doJSON("PUT", "/v1/m/sessions/runs/run-named/name", f.admin, map[string]any{"name": "deploy"}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("rename = %d %s", r.code, r.raw)
	}
	r := f.h.doJSON("POST", "/v1/m/sessions/runs/run-named/title", f.admin, map[string]any{"if_untitled": true}, f.hdr)
	if _, titled := r.body["title"]; r.code != http.StatusOK || r.body["name"] != "deploy" || titled {
		t.Fatalf("if_untitled on a named run = %d %s, want 200, named, no title", r.code, r.raw)
	}
	if got := len(runAuditEvents(t, f.h.st, f.tenant, "sessions.run.title_generated")); got != 1 {
		t.Fatalf("title audit rows = %d, want 1", got)
	}
}

// With no message of the person's to cut from, there is no title to make: 409, and the
// run keeps none.
func TestRunTitleNeedsAFirstMessage(t *testing.T) {
	f := messagesFixture(t, `{"type":"assistant","timestamp":"2026-10-14T10:00:05.000Z","message":{"content":[{"type":"text","text":"Ready."}]}}`+"\n")
	r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+f.runRef+"/title", f.admin, map[string]any{}, f.hdr)
	if r.code != http.StatusConflict {
		t.Fatalf("generate with no message = %d %s, want 409", r.code, r.raw)
	}
	if got := f.h.do("GET", "/v1/m/sessions/runs/"+f.runRef, f.admin, f.hdr); got.body["title"] != nil {
		t.Fatalf("a refused generate left a title: %s", got.raw)
	}
}

// The title is cut from the conversation, so it is refused wherever the conversation is
// not read: a session that could carry a secret (409) and a tool whose record the engine
// does not read (422). Neither keeps a title.
func TestRunTitleIsRefusedWhereTheConversationIsNotRead(t *testing.T) {
	f := messagesFixture(t, titleTranscript)
	path := "/v1/m/sessions/runs/" + f.runRef + "/title"
	untitled := func() {
		t.Helper()
		if got := f.h.do("GET", "/v1/m/sessions/runs/"+f.runRef, f.admin, f.hdr); got.body["title"] != nil {
			t.Fatalf("a refused title left one: %s", got.raw)
		}
	}

	setRunColumn(t, f, colRunGitRead, "binding:owner/name")
	if r := f.h.doJSON("POST", path, f.admin, map[string]any{}, f.hdr); r.code != http.StatusConflict || !strings.Contains(r.raw, "repository credential") {
		t.Fatalf("secret-bearing session = %d %s, want 409", r.code, r.raw)
	}
	untitled()
	setRunColumn(t, f, colRunGitRead, "")
	setRunColumn(t, f, colRunProfileDriver, "opencode")
	if r := f.h.doJSON("POST", path, f.admin, map[string]any{}, f.hdr); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("another tool's session = %d %s, want 422", r.code, r.raw)
	}
	untitled()
	if n := len(runAuditEvents(t, f.h.st, f.tenant, "sessions.run.title_generated")); n != 0 {
		t.Fatalf("refused titles recorded %d audit rows, want 0", n)
	}
}

// The title is made from the conversation, so the caller must be allowed to read it as
// well as to write the session: a context the permission door cannot vouch for is
// refused before anything is read.
func TestRunTitleRefusesWithoutOutputPermission(t *testing.T) {
	m := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/runs/run-1/title", strings.NewReader(`{}`))
	m.handleGenerateRunTitle(rec, req, api.ModuleContext{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("generate without the output permission = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// The title is the person's own first words, so a run read shows it only to a caller who
// may read the conversation it was cut from: the detail and each list row, decided for
// that run. A viewer reads the run row (run:read) but not its output, and gets no title.
func TestRunTitleIsShownOnlyToReadersOfTheOutput(t *testing.T) {
	f := messagesFixture(t, titleTranscript)
	if r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+f.runRef+"/title", f.admin, map[string]any{}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("generate = %d %s", r.code, r.raw)
	}
	viewer := f.h.viewerToken(f.admin, f.tenant, "v@title.io")
	rows := func(who string) []any {
		t.Helper()
		list := f.h.do("GET", "/v1/m/sessions/runs", who, f.hdr)
		items, _ := list.body["items"].([]any)
		if list.code != http.StatusOK || len(items) == 0 {
			t.Fatalf("list = %d %s, want the run among the rows", list.code, list.raw)
		}
		return items
	}
	for who, want := range map[string]bool{f.admin: true, viewer: false} {
		detail := f.h.do("GET", "/v1/m/sessions/runs/"+f.runRef, who, f.hdr)
		if detail.code != http.StatusOK {
			t.Fatalf("detail = %d %s", detail.code, detail.raw)
		}
		if _, got := detail.body["title"]; got != want {
			t.Fatalf("detail carries title = %v, want %v: %s", got, want, detail.raw)
		}
		for _, row := range rows(who) {
			if _, got := row.(map[string]any)["title"]; got != want {
				t.Fatalf("list row carries title = %v, want %v: %v", got, want, row)
			}
		}
	}
}
