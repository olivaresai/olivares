// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui_tools.go is the Get started and AI tools screens of CLI-CONCEPT §5.2 and
// §5.3. They read what `olivares tool ls`, `provider ls` and `provider account
// ls` read, and every action calls the client function of the command it
// names (cmd_ui_tools_flows.go), so the audit ledger holds the same events.

// uiTools is the engine the two screens talk to. cfg is resolved once, as a
// command resolves it.
type uiTools struct {
	cfg agentClientConfig

	// kept are sentences for the command to say after the screens close: a
	// sign-in that could not be ended cannot be said on a list that is gone.
	mu   sync.Mutex
	kept []*string
}

// note keeps text to say after the screens close.
func (u *uiTools) note(text string) { u.keep(text) }

// keep adds one entry for text and returns it, so a caller can drop that entry
// and no other.
func (u *uiTools) keep(text string) *string {
	u.mu.Lock()
	defer u.mu.Unlock()
	entry := &text
	u.kept = append(u.kept, entry)
	return entry
}

// pending keeps text to say after the screens close until done is called. A
// request still on its way when the person quits is never answered on a screen,
// so what it may have done is said by the command; one that answered is not. Each
// request has its own entry: the answer to an earlier one with the same words
// does not drop the note of one still on its way.
func (u *uiTools) pending(text string) (done func()) {
	entry := u.keep(text)
	return func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.kept = slices.DeleteFunc(u.kept, func(kept *string) bool { return kept == entry })
	}
}

// sayKept writes what the screens kept to w, each sentence once. u may be nil: no
// screens were wired.
func (u *uiTools) sayKept(w io.Writer) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	var said []string
	for _, text := range u.kept {
		if !slices.Contains(said, *text) {
			said = append(said, *text)
			_, _ = fmt.Fprintln(w, *text)
		}
	}
}

// uiToolsData is everything the screens show, read at one moment. A part that
// could not be read is left empty and named in warn; the tool rows are
// required, so their failure is an error instead.
type uiToolsData struct {
	rows      []toolRow
	running   map[string]int
	providers []map[string]any
	accounts  []map[string]any
	// ready is the engine's own answer that some tool can start a session now.
	ready bool
	// refused is the engine's sentence for a key its provider refused, the reason
	// no tool is ready when there is one.
	refused string
	warn    string
}

// getStartedSteps are the steps Get started counts: the engine answers, a tool
// is installed, a tool can run a session.
const getStartedSteps = 3

// maxLinkLen is the longest sign-in link shown: a real authorize link is about
// 400 characters, and one that does not fit the screen beside its other lines at
// 80 x 24 would be drawn in part, which is a link nobody can use.
const maxLinkLen = 800

// safeLink is whether a sign-in link may be shown: the engine's text, opened by a
// person, so only a plain https address with a host, no credentials and no
// character a terminal or a browser could read differently. url.Parse decodes a
// percent-encoded host, so the host is checked as decoded: a browser would read
// it as a name other than the text the person sees.
func safeLink(link string) bool {
	u, err := url.Parse(link)
	return err == nil && len(link) <= maxLinkLen && u.Scheme == "https" && u.Hostname() != "" &&
		u.User == nil && !strings.ContainsFunc(u.Host, func(r rune) bool { return r > '~' }) &&
		!strings.ContainsFunc(link, func(r rune) bool { return r <= ' ' || r > '~' })
}

// readFailedFrame is the screen when the engine could not be read. An action that
// just finished still did: its sentence stays above the error, which is about the
// re-read and not about the action.
func readFailedFrame(heading, notice string, err error) tui.Frame {
	footer := "Could not read the engine: " + err.Error()
	if notice != "" {
		footer = notice + " " + footer
	}
	return tui.Frame{Heading: heading, Footer: oneRow(footer), FooterRole: termrender.RoleFail}
}

// linkRefused is whether a sign-in's link is one the screen will not draw. A
// login that finished draws nothing, so a stale link in its answer is not judged.
func linkRefused(s toolSignIn) bool {
	return s.State != "signed_in" && s.URL != "" && !safeLink(s.URL)
}

// userCodeText is what a device code looks like: the code a person types where
// the link leads, not a sentence.
var userCodeText = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

// bareHost is a host name or a machine's address followed by what continues it
// (a path, a port, a query), as "claude-login.example:8443/auth" or "10.0.0.7/x".
var bareHost = regexp.MustCompile(`[a-z0-9-]+\.(xn--[a-z0-9-]+|[a-z]{2,})[/:?#\\]|\b\d{1,3}(\.\d{1,3}){3}\b`)

// namesAddress is whether engine text carries an address a person could open.
func namesAddress(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "://") || strings.Contains(text, "www.") || bareHost.MatchString(text)
}

// textRefused is whether the code or the message drawn beside a sign-in link is
// one the screen will not draw. The link passed safeLink and is the one address
// the page carries; text that names another, or a code that is not a code, would
// send the person somewhere nobody checked. A failed login keeps its reason and a
// login that finished draws nothing, so neither is judged.
func textRefused(s toolSignIn) bool {
	if s.State == "signed_in" || s.State == "failed" {
		return false
	}
	return namesAddress(s.Message) || namesAddress(s.UserCode) || (s.UserCode != "" && !userCodeText.MatchString(s.UserCode))
}

// installClient is the config an install runs with: the request limit of the
// command, because the engine takes up to 45 seconds to resolve a release.
func (u *uiTools) installClient() agentClientConfig {
	cfg := u.cfg
	cfg.timeout = toolInstallTimeout
	return cfg
}

// read asks the engine what `tool ls`, `provider ls`, `provider account ls`,
// `session ls --state running` and the readiness behind `olivares` would show.
func (u *uiTools) read(ctx context.Context) (uiToolsData, error) {
	rows, err := u.cfg.toolRows(ctx)
	if err != nil {
		return uiToolsData{}, err
	}
	// A driver id is the engine's text, and it names a command and a request:
	// one that would draw on a terminal is not a tool this screen can show.
	total := len(rows)
	rows = slices.DeleteFunc(rows, func(r toolRow) bool { return !plainName.MatchString(r.Driver) })
	d := uiToolsData{rows: rows, running: map[string]int{}}
	var warned []string
	if len(rows) < total {
		warned = append(warned, "a tool with an unusual name")
	}
	if slices.ContainsFunc(rows, unreadSignIn) {
		warned = append(warned, "whether a tool is signed in")
	}
	if d.providers, err = u.cfg.listItems(ctx, providersPath); err != nil {
		warned = append(warned, "the API keys")
	}
	if d.accounts, err = u.cfg.listItems(ctx, providerAccountsPath); err != nil {
		warned = append(warned, "the accounts")
	}
	if runs, err := u.cfg.listRuns(ctx, url.Values{"state": {"running"}}); err != nil {
		warned = append(warned, "the running sessions")
	} else {
		for _, run := range runs {
			if str(run, "state") == "running" {
				d.running[sessionDriver(run)]++
			}
		}
	}
	tool, refused, err := u.cfg.readyTool(ctx)
	if err != nil {
		warned = append(warned, "whether a tool is ready")
	}
	d.ready, d.refused = tool != "", refused
	if len(warned) > 0 {
		d.warn = "Could not read " + strings.Join(warned, ", ") + ". Run olivares doctor."
	}
	return d, nil
}

// refreshMainMenu puts the step the tools now name on the main menu's Get started
// row, which was written when the menu opened: after an install or a sign-in it is
// no longer the next step. root is nil when there is no list under the screen.
func refreshMainMenu(root *tui.Menu, rows []toolRow) {
	if root == nil {
		return
	}
	frame := root.Frame()
	_, next := nextToolStep(rows)
	for i := range frame.Items {
		if frame.Items[i].Label == "Get started" {
			frame.Items[i].Detail, frame.Items[i].Command = "Next: "+oneRow(next), oneRow(next)
		}
	}
	root.SetFrame(frame)
}

// toolsFrame is the list of CLI-CONCEPT §5.3: each tool with its state and the
// action it offers, then the accounts and keys, then the way to add a key. With
// getStarted it is the Get started screen: the same rows, a count of the steps
// done, and the cursor on the row that is the next step. reload re-reads the
// screen after an action and shows its notice.
func (u *uiTools) toolsFrame(d uiToolsData, heading string, getStarted bool, reload func(notice string)) (tui.Frame, int) {
	frame := tui.Frame{
		Heading: heading,
		Hints:   "↑↓ move  Enter open  / search  ? help  Esc back",
	}
	cursor := 0
	installed, signedIn := 0, 0
	for _, row := range d.rows {
		frame.Items = append(frame.Items, u.toolItem(row, d.running[row.Driver], reload))
		if row.Installed {
			installed++
		}
		if row.SignedIn != nil && *row.SignedIn {
			signedIn++
		}
	}
	for _, rec := range d.accounts {
		frame.Items = append(frame.Items, accountItem(rec))
	}
	for _, rec := range d.providers {
		frame.Items = append(frame.Items, keyItem(rec))
	}
	frame.Items = append(frame.Items, tui.MenuItem{
		Label:   "Add an API key",
		Detail:  "[ Add ]  seal a key in the engine and test it",
		Command: "olivares provider add",
		Open:    func(h *tui.Host) string { return u.addKey(h, reload) },
	})
	frame.Footer = fmt.Sprintf("%d installed · %d signed in", installed, signedIn)
	if getStarted {
		done := 1
		if installed > 0 {
			done++
		}
		// The step the main menu and `olivares` name for the same tools.
		driver, next := nextToolStep(d.rows)
		cursor = max(slices.IndexFunc(d.rows, func(r toolRow) bool { return r.Driver == driver }), 0)
		if d.ready {
			done++
			next, cursor = "olivares session start <folder>", 0
		}
		frame.Footer = fmt.Sprintf("%d of %d done. Next: %s", done, getStartedSteps, next)
		frame.FooterRole = termrender.RoleWarn
		if d.ready {
			frame.FooterRole = termrender.RoleOK
		}
		if !d.ready && d.refused != "" {
			frame.Footer = d.refused
		}
	} else {
		frame.Right, cursor = fmt.Sprint(len(d.rows)), 0
	}
	if d.warn != "" {
		frame.Footer, frame.FooterRole = d.warn, termrender.RoleWarn
	}
	frame.Heading, frame.Right, frame.Footer = oneRow(frame.Heading), oneRow(frame.Right), oneRow(frame.Footer)
	return frame, cursor
}

// toolItem is one tool's row.
func (u *uiTools) toolItem(row toolRow, running int, reload func(string)) tui.MenuItem {
	driver, name := row.Driver, toolName(row.Driver)
	item := tui.MenuItem{Label: oneRow(name), Command: "olivares tool ls"}
	switch {
	case !row.Installed:
		item.Detail = "Not installed  [ Install ]"
		item.Command = "olivares tool install " + driver
		item.Open = func(h *tui.Host) string { return u.install(h, driver, reload) }
	case row.SignedIn != nil && !*row.SignedIn:
		item.Detail = "Installed · not signed in  [ Sign in ]"
		item.Command = "olivares tool login " + driver
		item.Open = func(h *tui.Host) string { return u.signIn(h, driver, reload) }
	case row.SignedIn != nil:
		item.Detail = "Signed in"
		if row.Account != "" {
			item.Detail += " · " + oneRow(row.Account)
		}
	case unreadSignIn(row):
		item.Detail = "Installed · sign-in not readable  [ Sign in ]"
		item.Command = "olivares tool login " + driver
		item.Open = func(h *tui.Host) string { return u.signIn(h, driver, reload) }
	default:
		item.Detail = "Installed"
	}
	if running > 0 {
		item.Detail += " · " + sessionsRunning(running)
	}
	return item
}

// unreadSignIn is an installed tool with its own sign-in whose state the engine
// did not give: toolRows leaves it unset when the status read fails, and also
// when the probe answered installed:false for a tool the inventory lists. The
// check-in's toolRow.probeFailed flags only the failed read: keep the two in step.
func unreadSignIn(row toolRow) bool {
	return row.Installed && row.SignedIn == nil && toolSignsIn(row.Driver)
}

func sessionsRunning(n int) string {
	if n == 1 {
		return "1 session running"
	}
	return fmt.Sprintf("%d sessions running", n)
}

// accountItem is a named provider account: a tool's own sign-in kept apart
// under a name.
func accountItem(rec map[string]any) tui.MenuItem {
	detail := "Account · " + oneRow(toolName(str(rec, "driver")))
	if state := str(rec, "state"); state != "" && state != "active" {
		detail += " · " + oneRow(state)
	}
	return tui.MenuItem{Label: oneRow(str(rec, "name")), Detail: detail, Command: "olivares provider account ls"}
}

// keyItem is a registered API key. The connection test is said in a word, never
// only in a color.
func keyItem(rec map[string]any) tui.MenuItem {
	detail := "API key · " + oneRow(keyKindLabel(str(rec, "kind")))
	if hint := str(rec, "key_hint"); hint != "" {
		detail += " · " + oneRow(hint)
	}
	detail += " · " + connectionWords(str(rec, "probe_state"))
	if state := str(rec, "state"); state != "" && state != "active" {
		detail += " · " + oneRow(state)
	}
	// A reference that is not a plain name is not put into a command to paste.
	command := "olivares provider ls"
	if ref := str(rec, "provider_ref"); plainName.MatchString(ref) {
		command = "olivares provider get " + ref
	}
	return tui.MenuItem{Label: oneRow(str(rec, "display_name")), Detail: detail, Command: command}
}

// connectionWords is what a key's last connection test found.
func connectionWords(probe string) string {
	switch probe {
	case "ok":
		return "works"
	case "refused":
		return "refused by the provider"
	case "unreachable":
		return "provider not reachable"
	default:
		return "not tested"
	}
}

// keyKindLabels are the providers the screen adds a key for, in the order shown;
// keyKinds maps each to the kind the engine takes. An OpenAI-compatible endpoint
// needs a base URL: `olivares provider add --base-url` does that one.
var (
	keyKindLabels = []string{"Anthropic", "OpenAI", "xAI", "Gemini"}
	keyKinds      = map[string]string{"Anthropic": "anthropic", "OpenAI": "openai", "xAI": "xai", "Gemini": "gemini"}
)

// geminiKeyPrefix starts every Google API key. providerKindFromKey does not name
// it, because `provider add` takes Gemini through --kind only.
const geminiKeyPrefix = "AIza"

// keyForAnotherProvider is the sentence for a key whose own prefix names a
// provider other than kind, or "" when it does not. The engine tests a key with
// the provider chosen, so a key that belongs to another is not sent: it would be
// given to a vendor it is not for.
func keyForAnotherProvider(kind, key string) string {
	own := providerKindFromKey(key, "")
	if strings.HasPrefix(key, geminiKeyPrefix) {
		own = "gemini"
	}
	if own == "" || own == kind {
		return ""
	}
	return "That key is for " + keyKindLabel(own) + ", not " + keyKindLabel(kind) + ". Go up to Provider and change it."
}

// keyKindLabel is a kind in the words of keyKindLabels, else providerKindLabel's.
func keyKindLabel(kind string) string {
	for label, k := range keyKinds {
		if k == kind {
			return label
		}
	}
	return providerKindLabel(kind)
}

// open is the Open of the Get started and AI tools rows of the main menu: it
// pushes the screen, reads the engine without blocking the keys, and shows the
// answer. A read that fails says why in the footer, in the engine's words.
func (u *uiTools) open(heading string, getStarted bool) func(*tui.Host) string {
	return func(h *tui.Host) string {
		menu := h.PushMenu(tui.Frame{Heading: heading, Footer: "Reading the engine…"})
		var reload func(notice string)
		latest := 0 // the newest read; set and read on the loop only
		reload = func(notice string) {
			if notice != "" && !h.Holds(menu) {
				u.note(notice) // the person left this list: the command says it when the screens close
			}
			latest++
			mine := latest
			go func() {
				d, err := u.read(context.Background())
				h.Post(func() {
					if mine != latest {
						return // a newer read answers for the screen
					}
					if err != nil {
						menu.SetFrame(readFailedFrame(heading, notice, err))
						return
					}
					frame, cursor := u.toolsFrame(d, heading, getStarted, reload)
					menu.SetFrame(frame)
					if getStarted {
						menu.Select(cursor)
					}
					menu.SetNotice(notice)
					refreshMainMenu(h.Root(), d.rows)
				})
			}()
		}
		reload("")
		return ""
	}
}
