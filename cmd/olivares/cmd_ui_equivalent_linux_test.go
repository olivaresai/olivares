// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// This journey drives the menus, tools and security screens on one demo estate
// and captures their equivalent-command lines. It runs each line on a matching
// estate, comparing writes and read results. The captured lines must match the
// literal execution table exactly; terminal output is never executed as code.

// demoEstate is one engine with both planes the screens speak to: the tools
// routes of uiEngine and the governance and audit routes of secEngine. `olivares
// ui` walks them through one address, as it does one real engine.
type demoEstate struct {
	*httptest.Server
	tools *uiEngine
	sec   *secEngine
}

// newDemoEstate is the estate the journey works on: Claude Code and Codex
// installed but signed out, three tools not installed, a named account, one
// running session, two requests waiting for a person, the kill switch off and a
// ledger with three events. The device sign-in is one the person finishes on the
// page, so the screen action and the command both complete it.
func newDemoEstate(t *testing.T) *demoEstate {
	t.Helper()
	tools := &uiEngine{
		installed: map[string]bool{"claude": true, "codex": true},
		signedIn:  map[string]bool{},
		deleted:   make(chan struct{}), jobPolled: make(chan struct{}),
		deviceState: "signed_in",
		accounts:    []map[string]any{{"name": "claude-a", "driver": "claude", "state": "active"}},
		runs: []map[string]any{{
			"run_ref": "run_0192_storefront", "name": "storefront cart fix", "state": "running",
			"provider_driver": "claude", "workspace_path": "/srv/storefront",
			"created_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		}},
	}
	sec := &secEngine{stateStatus: http.StatusOK}
	sec.approvals = []map[string]any{
		secApproval("apr_01", "pending", "Turn on the issue tracker MCP server"),
		secApproval("apr_02", "pending", "Push cart-totals from storefront-web"),
	}
	now := time.Now()
	sec.events = []map[string]any{
		secEvent(1, now.Add(-3*time.Minute), "system", "checkpoint signed", "ledger", "height 48213"),
		secEvent(2, now.Add(-2*time.Minute), "johan@acme.example", "approval requested", "approval", "apr_01"),
		secEvent(3, now.Add(-time.Minute), "rehema@acme.example", "session started", "session", "run_0192_storefront"),
	}
	e := &demoEstate{tools: tools, sec: sec}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	return e
}

// serve answers from the tools plane first; a route it does not know goes to the
// governance plane, as one engine routes internally. `olivares provider get`
// reads one registered key, a route neither fake plane has, so the estate serves
// it from the tools plane's own records.
func (e *demoEstate) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == providersPath+"/prv_01" {
		e.tools.mu.Lock()
		defer e.tools.mu.Unlock()
		if len(e.tools.providers) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(e.tools.providers[0])
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	held := httptest.NewRecorder()
	e.tools.serve(held, r)
	if held.Code == http.StatusNotFound && bytes.Contains(held.Body.Bytes(), []byte("no route")) {
		e.sec.serve(w, r)
		return
	}
	for name, values := range held.Header() {
		w.Header()[name] = values
	}
	w.WriteHeader(held.Code)
	_, _ = w.Write(held.Body.Bytes())
}

// ledgerStatusWord is what the Verify now panel says the ledger is.
var ledgerStatusWord = regexp.MustCompile(`Ledger status: ([a-z]+)`)

// esc presses Esc and waits out the decoder's escape window, so the next key
// cannot be glued to the lone Esc as one sequence (two Escs in a row would
// otherwise arrive as one).
func (j *uiJourney) esc() {
	j.press(keyEscBytes)
	time.Sleep(50 * time.Millisecond)
}

func TestUIEveryShownLineRunsOnADemoEstate(t *testing.T) {
	// The ledger Verify now reads is the one on this computer: a real one, with a
	// signed checkpoint, so both the screen and the command report the same word.
	ledgerDir, ledgerTenant := t.TempDir(), uiTestTenant
	if auditVerificationAvailable {
		ledgerDir, ledgerTenant = signedLedger(t)
	}
	estate := newDemoEstate(t)
	j, wait := startUIJourneyAt(t, estate.URL, uiTestToken, ledgerTenant, "OLIVARES_DATA_DIR="+ledgerDir)

	// The main menu: every row selected once, so every line it can show is shown.
	// The menu and the selected row's line arrive in one frame; the harvest at
	// the end proves the line was there.
	j.see("= olivares tool login claude")
	j.press("\x1b[B")
	j.see("= olivares tool ls")
	j.press("\x1b[B")
	j.see("= olivares session ls")
	j.press("\x1b[B")
	j.see("= olivares governance approvals ls")
	j.press("\x1b[B")
	j.see("= olivares config")
	j.press("\x1b[B")
	j.see("= olivares --help")
	j.press("\x1b[A\x1b[A\x1b[A\x1b[A") // back to AI tools, the row with the actions

	// AI tools: every action row is acted on, top to bottom, so the lines are seen
	// in the order the rows show them.
	j.press("\r")
	j.see("Not installed  [ Install ]")
	j.press("\r") // Claude Code, signed out: [ Sign in ]
	j.see("Paste the code the page shows")
	j.typeText(uiTestCode)
	j.press("\r")
	j.see("Claude Code is signed in.")
	j.press("\x1b[B")
	j.see("= olivares tool login codex") // the row, before Enter acts on it
	j.press("\r")                        // the device sign-in the person finishes on the page
	j.see("Waiting for you to finish on the sign-in page")
	j.see("Codex is signed in.")
	j.press("\x1b[B")
	j.see("= olivares tool install grok")
	j.press("\r")
	j.see("Installed Grok Build 2.1.286.")
	j.press("\x1b[B")
	j.see("= olivares tool install opencode")
	j.press("\r")
	j.see("Installed OpenCode 2.1.286.")
	j.press("\x1b[B")
	j.see("= olivares tool install gemini-cli")
	j.press("\r")
	j.see("Installed Gemini CLI 2.1.286.")
	j.press("\x1b[B")
	j.see("= olivares provider account ls") // the named account
	j.press("\x1b[B")
	j.see("= olivares provider add") // the row; the page names the redirection
	j.press("\r")
	j.see("The key is never shown")
	j.press("\r") // the provider, kept as Anthropic
	j.typeText(uiTestKey)
	j.see("hidden · 41 characters")
	j.press("\r") // the name, left empty
	j.press("\r") // send
	j.sampleArgv()
	j.see("Connection test: works.")
	j.press("\r") // back to the list, which now holds the key, and the cursor is on it
	j.see("API key · Anthropic")
	j.esc() // the main menu
	// Read the menu before the next keys: a lone Esc glued to a following arrow
	// would lose its escape byte to the decoder's sequence wait.
	j.see("· version")

	// Security: the approvals, the kill switch and the ledger.
	j.press("\x1b[B\x1b[B\r")
	j.see("= olivares governance approvals ls") // the menu's Approvals row
	j.press("\x1b[B")
	j.see("= olivares governance killswitch state")
	j.press("\x1b[B")
	j.see("= olivares audit ls")                        // the menu's Audit ledger row
	j.press("\x1b[A\x1b[A\r")                           // Approvals
	j.see("= olivares governance approvals get apr_01") // the queue, its first row selected
	j.press("\x1b[B")
	j.see("= olivares governance approvals get apr_02")
	j.press("\x1b[A\r") // review the first request
	j.see("Asked by")   // the review's own row; the queue's rows already name the requester
	j.esc()
	j.see("Security › Approvals")
	j.esc()
	j.see("recorded in the audit ledger")
	j.press("\x1b[B\r") // the kill switch
	j.see("The kill switch is off for the estate.")
	j.esc()
	j.see("recorded in the audit ledger")
	j.press("\x1b[B\x1b[B\r") // the audit ledger
	// "Newest first" is the ledger list's own footer: the words of this wait are
	// in no Security menu frame, where the Audit ledger row shows its line first.
	j.see("Newest first")
	if auditVerificationAvailable {
		j.press("\x1b[B")
		j.see("= olivares audit verify")
		j.press("\r")
		j.see("Ledger status: ")
		j.press("\r") // back to the list, which keeps the result
		j.see("last verify")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
	j.assertNeverShown(uiTestKey)
	j.assertNotOnDisk(uiTestKey)
	// The data dir the Verify now screen reads is outside the journey's home, and
	// this journey is the one that pointed the screens at it.
	if path := fileHoldingSecret(t, ledgerDir, uiTestKey); path != "" {
		t.Errorf("part of the key was written to the data dir: %s", path)
	}

	// Every line the screens showed, in the order each was first drawn.
	shown := shownCommandLines(j.transcript.String())
	want := []struct {
		line   string
		stdin  string // what a person types into it, where it asks
		action bool   // Enter on the row did work on the engine
	}{
		{"olivares tool login claude", uiTestCode + "\n", true},
		{"olivares tool ls", "", false},
		{"olivares session ls", "", false},
		{"olivares governance approvals ls", "", false},
		{"olivares config", "", false},
		{"olivares --help", "", false},
		{"olivares tool login codex", "", true},
		{"olivares tool install grok", "", true},
		{"olivares tool install opencode", "", true},
		{"olivares tool install gemini-cli", "", true},
		{"olivares provider account ls", "", false},
		// The row names the command; the page names it with the redirection that
		// feeds it. Both run: the exact form is the one whose writes are compared,
		// and the row's form takes the key the way a pipe would.
		{"olivares provider add", uiTestKey + "\n", false},
		{"olivares provider add < key.txt", "", true},
		{"olivares provider get prv_01", "", false},
		{"olivares governance killswitch state", "", false},
		{"olivares audit ls", "", false},
		{"olivares governance approvals get apr_01", "", false},
		{"olivares governance approvals get apr_02", "", false},
	}
	if auditVerificationAvailable {
		want = append(want, struct {
			line   string
			stdin  string
			action bool
		}{"olivares audit verify", "", false})
	}
	var wantLines []string
	for _, w := range want {
		wantLines = append(wantLines, w.line)
	}
	if !reflect.DeepEqual(shown, wantLines) {
		t.Errorf("the lines the screens showed are\n%s\nwant\n%s", strings.Join(shown, "\n"), strings.Join(wantLines, "\n"))
	}

	// The status Verify now reported, for the line that must report it too.
	verifyStatus := ""
	if auditVerificationAvailable {
		word := ledgerStatusWord.FindStringSubmatch(termrender.StripANSI(j.transcript.String()))
		if word == nil {
			t.Fatalf("the ledger screen never said its status:\n%s", termrender.StripANSI(j.transcript.String()))
		}
		verifyStatus = word[1]
	}

	// The twin: the same estate, and every line of the pin — which the harvest
	// above had to equal exactly — run as a person would paste it, through a
	// shell (the key line redirects its key.txt) with `olivares` on PATH as this
	// binary. Actions first, in the order the screens did them, so their writes
	// can be compared as a sequence; then the reads.
	twin := newDemoEstate(t)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	if err := os.Symlink(exe, filepath.Join(bin, "olivares")); err != nil {
		t.Fatalf("put olivares on PATH: %v", err)
	}
	keyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(keyDir, "key.txt"), []byte(uiTestKey+"\n"), 0o600); err != nil {
		t.Fatalf("write key.txt: %v", err)
	}
	// The twin's home is this test's own dir, so it can be scanned for the key
	// once the lines have run; without these the children would write under a
	// throwaway home the test cannot name.
	twinHome := t.TempDir()
	env := cliProcessEnv(t,
		"OLIVARES_SERVER_URL="+twin.URL, "OLIVARES_TOKEN="+uiTestToken, "OLIVARES_TENANT="+ledgerTenant,
		"OLIVARES_DATA_DIR="+ledgerDir, "NO_COLOR=1",
		"HOME="+twinHome, "XDG_CONFIG_HOME="+filepath.Join(twinHome, ".config"),
		"XDG_DATA_HOME="+filepath.Join(twinHome, ".local", "share"), "TMPDIR="+filepath.Join(twinHome, "tmp"))
	if err := os.MkdirAll(filepath.Join(twinHome, "tmp"), 0o700); err != nil {
		t.Fatalf("make the twin's temp dir: %v", err)
	}
	for i, kv := range env {
		if old, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + bin + string(os.PathListSeparator) + old
		}
	}
	for _, w := range want {
		if !w.action {
			continue
		}
		if out, rc := runShownLine(t, keyDir, env, w.stdin, w.line); rc != 0 {
			t.Errorf("%s exited %d, want 0:\n%s", w.line, rc, out)
		}
	}
	screenLedger := estate.tools.ledger()
	if got := twin.tools.ledger(); !reflect.DeepEqual(got, screenLedger) {
		t.Errorf("the lines left\n%q\nthe screens left\n%q", got, screenLedger)
	}
	if writes := estate.sec.writes(); len(writes) != 0 || len(twin.sec.writes()) != 0 {
		t.Errorf("a governance write was made: screens %+v, lines %+v", estate.sec.writes(), twin.sec.writes())
	}
	// The engine saw the one key the screens sent, and nothing mangled in flight.
	if got, want := estate.tools.receivedKeys(), []string{uiTestKey}; !reflect.DeepEqual(got, want) {
		t.Errorf("the screens sent the keys %q, want the one that was typed", got)
	}

	// The reads: each line prints what its screen showed. They leave the twin as
	// the actions left it, except the row's own add form, which adds its key.
	for _, w := range want {
		if w.action {
			continue
		}
		out, rc := runShownLine(t, keyDir, env, w.stdin, w.line)
		if rc != 0 {
			t.Errorf("%s exited %d, want 0:\n%s", w.line, rc, out)
			continue
		}
		switch w.line {
		case "olivares audit verify":
			var report struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &report); err != nil {
				t.Errorf("olivares audit verify printed no report to read: %v\n%s", err, out)
				continue
			}
			if report.Status != verifyStatus {
				t.Errorf("olivares audit verify says %q, want the %q the screen said:\n%s", report.Status, verifyStatus, out)
			}
			continue
		}
		var facts []string
		switch w.line {
		case "olivares tool ls":
			facts = []string{"Claude Code", "signed in as ana@example.com", "Codex", "Grok Build", "OpenCode", "Gemini CLI"}
		case "olivares session ls":
			facts = []string{"storefront cart fix", "running"}
		case "olivares provider account ls":
			facts = []string{"claude-a"}
		case "olivares provider add":
			facts = []string{"Anthropic", "…wxyz"}
		case "olivares provider get prv_01":
			facts = []string{"Anthropic", "…wxyz"}
		case "olivares governance approvals ls":
			facts = []string{"apr_01", "apr_02", "pending", "rehema@acme.example"}
		case "olivares governance approvals get apr_01":
			facts = []string{"rehema@acme.example", "sessions.run.launch", "Turn on the issue tracker MCP server"}
		case "olivares governance approvals get apr_02":
			facts = []string{"rehema@acme.example", "sessions.run.launch", "Push cart-totals from storefront-web"}
		case "olivares governance killswitch state":
			// The screen said "off for the estate"; these are the command's words
			// for the state both read.
			facts = []string{"estate running", "no kill switch is active"}
		case "olivares audit ls":
			facts = []string{"checkpoint signed", "approval requested", "session started", "rehema@acme.example"}
		case "olivares config", "olivares --help":
			facts = []string{"Usage:"}
		default:
			t.Errorf("no facts are checked for %q", w.line)
		}
		for _, fact := range facts {
			if !strings.Contains(out, fact) {
				t.Errorf("%s does not say %q:\n%s", w.line, fact, out)
			}
		}
	}
	extra := twin.tools.ledger()[len(screenLedger):]
	wantExtra := []string{
		`POST /v1/m/sessions/providers {"api_key":"<api_key>","base_url":"","display_name":"Anthropic","kind":"anthropic"}`,
		`POST /v1/m/sessions/providers/prv_01/test {}`,
	}
	if !reflect.DeepEqual(extra, wantExtra) {
		t.Errorf("the reads left %q on the twin, want only the row's own add form\n%q", extra, wantExtra)
	}
	if got, wantKeys := twin.tools.receivedKeys(), []string{uiTestKey, uiTestKey}; !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("the lines sent the keys %q, want the typed one, once per form of the add", got)
	}
	if path := fileHoldingSecret(t, twinHome, uiTestKey); path != "" {
		t.Errorf("part of the key was written under the twin's home: %s", path)
	}
}

// shownCommandLines is every "= olivares …" line the terminal saw, in the order
// each was first drawn, without duplicates. A redraw puts the cursor home before
// it writes, so a frame begins a line there even though the stream carries no
// newline for it; the bottom line pads the gap to the key hints with spaces, and
// a command never holds two spaces in a row.
func shownCommandLines(raw string) []string {
	var lines []string
	seen := map[string]bool{}
	for _, row := range strings.Split(strings.ReplaceAll(raw, "\x1b[H", "\n"), "\n") {
		row = strings.TrimSpace(termrender.StripANSI(strings.TrimSuffix(row, "\r")))
		if !strings.HasPrefix(row, "= olivares ") {
			continue
		}
		if cut := strings.Index(row, "  "); cut >= 0 {
			row = strings.TrimRight(row[:cut], " ")
		}
		if !seen[row] {
			seen[row] = true
			lines = append(lines, strings.TrimPrefix(row, "= "))
		}
	}
	return lines
}

// shownLineWait bounds one line's run, as the pty waits bound the screens: a
// hung command is a failure with its name, not a stalled package.
const shownLineWait = 30 * time.Second

// runShownLine runs one shown line as the person reads it: through a shell in
// dir, so the line's own redirections work, and returns what it printed on both
// streams with its exit code. A line is one of the table's literals — the
// harvest is only ever compared, never run — and what it prints must hold no
// part of the key.
func runShownLine(t *testing.T, dir string, env []string, stdin, line string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), shownLineWait)
	defer cancel()
	// #nosec G204 -- line is a literal of the test's own table, not data from the run.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	rc := 0
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run %q: %v", line, err)
	}
	if errors.As(err, &exitErr) {
		rc = exitErr.ExitCode()
	}
	printed := out.String() + errOut.String()
	for i := 0; i+secretWindow <= len(uiTestKey); i++ {
		if strings.Contains(printed, uiTestKey[i:i+secretWindow]) {
			t.Errorf("%s printed part of the key", line)
			break
		}
	}
	return printed, rc
}
