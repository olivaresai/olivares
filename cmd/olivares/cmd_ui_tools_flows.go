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
	"sync"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// The three actions of the tools screens. Each runs the engine work on its own
// goroutine and shows the answer through Host.Post, so a screen is only ever
// changed on the loop. Each calls the function of the command it names
// (installTool, startToolSignIn, addProvider), which is why the audit ledger
// cannot tell the screen from the command.

// endsByItself is what is said of a login the engine did not end when asked.
const endsByItself = "It ends by itself within 15 minutes."

// couldNotEnd is the notice for a login the person left that stays open.
const couldNotEnd = "Could not end the sign-in on the engine. " + endsByItself

// mayHaveStarted is what is said of a login whose start the engine may have
// accepted: nothing here knows its id, so nothing here can end it.
const mayHaveStarted = "The sign-in may have started on the engine. " + endsByItself

// showResult turns a panel into a page that only says something: its work is
// over, so Enter takes the person back.
func showResult(p *tui.Panel, role termrender.Role, footer string, lines ...string) {
	p.Busy, p.Submit = false, nil
	p.SetFields(nil)
	p.Lines, p.Footer, p.FooterRole = lines, footer, role
}

// settle is the end of an action's work, decided on the loop, where the answer
// is checked against what the person did meanwhile. Done work closes its panel
// and the list behind it re-reads. Failed work is said on the panel while the
// person is still on it, and as a notice on the list when they have left.
func settle(h *tui.Host, p *tui.Panel, reload func(string), failed bool, text string) {
	switch {
	case !failed:
		h.Close(p)
		reload(text)
	case h.OnTop(p):
		showResult(p, termrender.RoleFail, "Enter goes back.", "  "+text)
	default:
		reload(text)
	}
}

// install is `olivares tool install <tool>`: plan, run and wait for the job.
func (u *uiTools) install(h *tui.Host, driver string, reload func(string)) string {
	name := toolName(driver)
	ctx, cancel := context.WithCancel(context.Background())
	left, planned := false, false // set and read on the loop only
	installing := func() {}       // says the install goes on, until the wait has its answer; set before the answer is posted
	p := &tui.Panel{
		Heading: "Install " + name,
		Lines: []string{"  Asking the engine for the signed release…", "",
			"  Esc stops waiting here. The install keeps going on the engine."},
		Footer:  "Installing…",
		Command: "olivares tool install " + driver,
		Busy:    true,
		Cancel:  func() { left = true; cancel() },
		OnClose: func() { reload("") },
	}
	h.PushPanel(p)
	cfg := u.installClient()
	go func() {
		defer cancel()
		request := map[string]any{"driver": driver, "version": installRequestVersion(driver, "latest", false)}
		_, job, err := cfg.installTool(ctx, request, func(plan toolInstallPlan) {
			// Kept here, not on the loop: a quit that comes before the loop reaches a
			// posted function drops it, and the request to start the job may be on its way.
			installing = u.pending(name + " may still be installing on the engine. Check olivares tool ls.")
			h.Post(func() {
				planned = true
				p.Lines[0] = fmt.Sprintf("  Installing %s %s (verified: %s)…", name, oneRow(plan.Version), oneRow(plan.Verification))
			})
		})
		h.Post(func() {
			installing()
			switch {
			case err != nil && left && job.ID != "": // the wait ended because the person left
				reload(name + " is still installing on the engine.")
			case err != nil && left && planned: // the request to start it may have been accepted
				reload(name + " may be installing on the engine.")
			case err != nil && left:
			case err != nil && job.ID != "": // accepted, then lost track of: it may be running
				settle(h, p, reload, true, oneRow(fmt.Sprintf("%s may still be installing on the engine (%s). Check olivares tool ls.", name, err)))
			case err != nil && planned && !engineRefused(err): // the start may have been accepted
				settle(h, p, reload, true, oneRow(fmt.Sprintf("%s may have started installing on the engine (%s). Check olivares tool ls.", name, err)))
			case err != nil:
				settle(h, p, reload, true, oneRow(err.Error()))
			case job.State != "succeeded":
				settle(h, p, reload, true, oneRow(fmt.Sprintf("%s was not installed: %s", name, job.failure())))
			default:
				settle(h, p, reload, false, fmt.Sprintf("Installed %s %s.", name, oneRow(job.Version)))
			}
		})
	}()
	return ""
}

// signInFlow is one run of `olivares tool login <tool>`. The goroutine owns the
// engine's answer; the panel is only changed through Post. mu guards what Cancel,
// which runs on the loop, and the goroutine both read.
type signInFlow struct {
	cfg    *agentClientConfig
	h      *tui.Host
	p      *tui.Panel
	driver string
	name   string
	cancel context.CancelFunc
	codes  chan string
	reload func(string)
	// note keeps a sentence for after the screens close, and pending one that is
	// dropped when its work answers; nil when nothing keeps them.
	note    func(string)
	pending func(string) func()
	// left and asked (what the page waits for: a code or a key) are set and read
	// on the loop only.
	left  bool
	asked string
	// methodsUnread is the sentence for a status that could not be read, so the
	// tool's default login runs. The goroutine sets and reads it.
	methodsUnread string

	// endMu makes ending the login one request: a second caller waits for it and
	// gets its answer. mu guards the fields Cancel (on the loop) and the goroutine share.
	endMu    sync.Mutex
	mu       sync.Mutex
	id       string
	signedIn bool
	ended    bool
	endOK    bool
}

// signIn starts the tool's own login on the engine and walks the person through
// it: the link, the code to enter or the code to paste, and the wait.
func (u *uiTools) signIn(h *tui.Host, driver string, reload func(string)) string {
	f := &signInFlow{cfg: &u.cfg, h: h, driver: driver, name: toolName(driver), codes: make(chan string, 1), reload: reload, note: u.note, pending: u.pending}
	ctx, cancel := context.WithTimeout(context.Background(), signInWindow)
	f.cancel = cancel
	f.p = &tui.Panel{
		Heading: "Sign in to " + f.name,
		Lines:   []string{"  Starting the " + f.name + " sign-in…"},
		Footer:  "Starting…",
		Command: "olivares tool login " + driver,
		Busy:    true,
		Submit:  f.sendCode,
		Cancel:  f.stop,
		OnClose: func() { reload("") },
	}
	h.PushPanel(f.p)
	go f.run(ctx)
	return ""
}

func (f *signInFlow) run(ctx context.Context) {
	defer f.cancel()
	var s toolSignIn
	// The command starts the first method the engine lists; so does the screen,
	// or the command shown beside the row would not be the one that runs.
	body := map[string]any{"driver": f.driver, "tenant_id": f.cfg.tenant}
	st, statusErr := f.cfg.readToolSignInStatus(ctx, f.driver, "")
	if ctx.Err() != nil {
		return // the person left before anything was started
	}
	if statusErr == nil && st.SignedIn {
		who := ""
		if st.Account != "" {
			who = " as " + oneRow(st.Account)
		}
		f.h.Post(func() { settle(f.h, f.p, f.reload, false, f.name+" is already signed in"+who+".") })
		return
	}
	if method := firstSignInMethod(st, statusErr); method != "" {
		body["method"] = method
	} else if facts, _ := driverfacts.Lookup(f.driver); statusErr != nil && len(facts.LoginMethods) > 1 {
		f.methodsUnread = "Could not read " + f.name + "'s login methods, so its default login runs."
	}
	answered := f.pend(mayHaveStarted) // said after a quit, until the engine answers the start
	err := f.cfg.startToolSignIn(ctx, body, &s)
	unknown := err != nil && !engineRefused(err) // a wait that ended or an answer that cannot be read
	if err == nil {
		// The id is known before the note about an unanswered start is dropped, so a
		// quit between the two ends the login instead of calling it ended.
		f.mu.Lock()
		f.id = s.ID
		f.mu.Unlock()
	}
	if !unknown {
		answered()
	}
	if err != nil {
		f.fail(ctx, err.Error(), unknown)
		return
	}
	if ctx.Err() != nil {
		f.endForLeaver() // the person left while the engine was starting it
		return
	}
	for {
		// The link is the engine's text and a person opens it: one that is not a
		// plain https address is not drawn, and the login ends.
		if linkRefused(s) {
			f.fail(ctx, "The sign-in link from the engine is not a plain https address, so it is not shown.", false)
			return
		}
		if textRefused(s) {
			f.fail(ctx, "The engine's sign-in text names another address, so it is not shown.", false)
			return
		}
		f.show(s)
		var err error
		switch {
		case s.State == "signed_in":
			f.finish()
			return
		case s.State == "failed", s.State != "starting" && s.State != "needs_key" && s.URL == "":
			f.fail(ctx, toolSignInFailed(f.driver, s).Error(), false)
			return
		case s.State == "needs_code", s.State == "needs_key":
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case code := <-f.codes:
				err = f.cfg.sendToolSignInCode(ctx, &s, code)
			}
		default:
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(2 * time.Second):
				err = f.cfg.readToolSignIn(ctx, &s)
			}
		}
		if err != nil {
			f.fail(ctx, err.Error(), false)
			return
		}
	}
}

// show puts the engine's state on the panel.
func (f *signInFlow) show(s toolSignIn) {
	lines, footer, needsCode := signInLines(f.driver, s)
	if f.methodsUnread != "" {
		lines = append(lines, "", "  "+f.methodsUnread)
	}
	f.h.Post(func() {
		f.p.Lines, f.p.Footer, f.p.FooterRole = lines, footer, termrender.RoleNone
		f.p.Busy = !needsCode
		if needsCode {
			f.asked = "code"
			if s.State == "needs_key" {
				f.asked = "key"
			}
			f.p.SetFields([]tui.Field{{Label: strings.ToUpper(f.asked[:1]) + f.asked[1:], Secret: true}})
		} else {
			f.p.SetFields(nil)
		}
	})
}

// signInLines is what the person reads for one state of the login: the link, the
// code to enter there (Codex, Grok Build, OpenCode) and, for Claude Code and
// Gemini CLI, a field for the code the page shows. A login that waits for a key
// (OpenCode's own provider) names the page that issues it and asks for the key.
func signInLines(driver string, s toolSignIn) (lines []string, footer string, needsCode bool) {
	switch {
	case s.State == "failed" || s.State == "signed_in":
		// The login is over: its link and code are of no use, and are not drawn.
		return []string{"  Finishing…"}, "Finishing…", false
	case s.State == "needs_key" && s.URL == "":
		return []string{"  Paste the API key the tool waits for.", ""}, "Paste the key, then press Enter.", true
	case s.State == "needs_key":
		return []string{"  Create an API key here, then paste it below:", "", "  " + oneRow(s.URL), ""}, "Paste the key, then press Enter.", true
	case s.State == "starting" || s.URL == "":
		return []string{"  Starting the " + toolName(driver) + " sign-in…"}, "Starting…", false
	}
	lines = []string{"  Open this link and sign in:", "", "  " + oneRow(s.URL), ""}
	if driver != "claude" && s.UserCode != "" {
		lines = append(lines, "  Enter this code there:", "", "    "+oneRow(s.UserCode), "")
	}
	if s.State == "needs_code" {
		if s.Message != "" {
			lines = append(lines, "  "+oneRow(s.Message), "")
		}
		return lines, "Paste the code the page shows, then press Enter.", true
	}
	return lines, "Waiting for you to finish on the sign-in page. It works for 15 minutes.", false
}

// sendCode is the person pressing Enter on the code. The field is emptied the
// moment the code is handed over.
func (f *signInFlow) sendCode(values []string) {
	if len(values) == 0 {
		return
	}
	code := strings.TrimSpace(values[0])
	if code == "" {
		return
	}
	f.p.ClearFields()
	f.p.SetFields(nil)
	f.p.Busy, f.p.Footer = true, "Checking the "+f.asked+"…"
	f.codes <- code
}

// finish is a login that completed: the screen behind it re-reads the tools.
func (f *signInFlow) finish() {
	f.mu.Lock()
	f.signedIn = true
	f.mu.Unlock()
	f.h.Post(func() { settle(f.h, f.p, f.reload, false, f.name+" is signed in.") })
}

// fail ends the login on the engine and says why, unless the person left. Only
// the login's own 15 minutes make it a timeout: a slow request has its own error.
// A login the engine did not end is said on this page, because closing it
// re-reads the list and clears a notice put there. startUnknown is a start that
// ended without the engine refusing it: the login may be open, with no id to end.
func (f *signInFlow) fail(ctx context.Context, msg string, startUnknown bool) {
	ended := f.endOnEngine()
	text := oneRow(msg)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut {
		text = "The sign-in timed out after 15 minutes. Start it again."
	}
	f.mu.Lock()
	started := f.id != ""
	f.mu.Unlock()
	if started && !ended && !timedOut {
		text += " It could not be ended on the engine. " + endsByItself
		f.keep(couldNotEnd)
	}
	f.h.Post(func() {
		switch {
		case !f.left && startUnknown:
			f.keep(mayHaveStarted)
			settle(f.h, f.p, f.reload, true, text+" "+mayHaveStarted)
		case !f.left:
			settle(f.h, f.p, f.reload, true, text)
		case !started: // the person left before the engine answered the start
			f.reload(mayHaveStarted)
		}
	})
}

// stop is the person leaving or quitting: the wait ends and so does the login on
// the engine, as Ctrl-C ends it in `olivares tool login`. It waits for the
// engine's answer (five seconds at most), which a quit needs.
//
// When the engine does not confirm the end, the list says so and so does the
// command once the terminal is restored: the login ends by itself within 15
// minutes, as `olivares tool login` says in the same case.
func (f *signInFlow) stop() {
	f.left = true
	f.cancel()
	f.endForLeaver()
}

// endForLeaver ends the login for a person who left, and says so when the engine
// does not confirm it: on the list, and after the screens close.
func (f *signInFlow) endForLeaver() {
	if !f.endOnEngine() {
		f.keep(couldNotEnd)
		go f.h.Post(func() { f.reload(couldNotEnd) })
	}
}

// pend is f.pending, or nothing to drop when no screens keep it.
func (f *signInFlow) pend(text string) (done func()) {
	if f.pending == nil {
		return func() {}
	}
	return f.pending(text)
}

// keep has the command say text after the screens close.
func (f *signInFlow) keep(text string) {
	if f.note != nil {
		f.note(text)
	}
}

// endOnEngine ends the engine's login once, unless it signed in, and says whether
// it is ended. It is one request: a caller that comes while the request is on its
// way (the loop on a quit, while the flow's own goroutine ends it after the wait
// was canceled) waits for it and gets its answer, so a process does not exit with
// the end still in flight.
func (f *signInFlow) endOnEngine() bool {
	f.endMu.Lock()
	defer f.endMu.Unlock()
	f.mu.Lock()
	switch {
	case f.id == "" || f.signedIn:
		f.mu.Unlock()
		return true
	case f.ended:
		ended := f.endOK
		f.mu.Unlock()
		return ended
	}
	f.ended = true
	id := f.id
	f.mu.Unlock()
	ended := f.cfg.cancelToolSignIn(id)
	f.mu.Lock()
	f.endOK = ended
	f.mu.Unlock()
	return ended
}

// addKey is `olivares provider add`: the provider, the key, typed hidden, and an
// optional name. The key comes before the name so that the first field a person
// can type into, or paste into, is the hidden one. The field is emptied as soon
// as the key is sent; the key is never in argv, never drawn and never written
// anywhere.
func (u *uiTools) addKey(h *tui.Host, reload func(string)) string {
	sent := false // set and read on the loop only
	p := &tui.Panel{
		Heading: "Add an API key",
		Lines: []string{"  Pick the provider, type the key, then name it if you like.",
			"  The key is never shown. The engine seals it and tests it with one", "  model-list call; nothing is spent."},
		Command: "olivares provider add < key.txt",
		// Once the key was sent the engine may hold it, whatever the answer said.
		OnClose: func() {
			if sent {
				reload("")
			}
		},
	}
	p.SetFields([]tui.Field{
		{Label: "Provider", Value: keyKindLabels[0], Choices: keyKindLabels},
		{Label: "Key", Secret: true},
		{Label: "Name"},
	})
	p.Submit = func(values []string) {
		kind, key, name := keyKinds[values[0]], strings.TrimSpace(values[1]), strings.TrimSpace(values[2])
		if key == "" {
			p.Footer, p.FooterRole = "Type the key first.", termrender.RoleWarn
			return
		}
		if msg := keyForAnotherProvider(kind, key); msg != "" {
			p.Footer, p.FooterRole = msg, termrender.RoleWarn
			return
		}
		sent = true
		// If the person quits before the engine answers, the key may be sealed.
		answered := u.pending("A key sent as you left may have been added. Check olivares provider ls.")
		p.ClearFields()
		p.SetFields(nil)
		p.Busy, p.Footer, p.FooterRole = true, "Sealing the key and testing it…", termrender.RoleNone
		go func() {
			if name == "" {
				name = providerKindLabel(kind)
			}
			ctx := context.Background()
			rec, err := u.cfg.addProvider(ctx, map[string]any{"kind": kind, "display_name": name, "base_url": "", "api_key": key})
			if err == nil {
				// The reference names a route: only a plain name is asked for.
				if ref := str(rec, "provider_ref"); plainName.MatchString(ref) {
					rec = u.cfg.testAddedProvider(ctx, ref, rec)
				}
			}
			h.Post(func() {
				answered()
				switch {
				case err != nil:
					settle(h, p, reload, true, keyNotAdded(err))
				default:
					if h.OnTop(p) {
						showKeyResult(p, rec, name)
					} else {
						reload("Added " + oneRow(name) + ".")
					}
				}
			})
		}()
	}
	h.PushPanel(p)
	return ""
}

// keyNotAdded says what is known when the add did not come back as a success: an
// engine that refused it did not seal the key; a request that timed out, or a
// server error that may have followed the sealing, may have.
func keyNotAdded(err error) string {
	if engineRefused(err) {
		return "The key was not added: " + oneRow(err.Error())
	}
	return "The key may not have been added: " + oneRow(err.Error()) + " Check olivares provider ls."
}

// engineRefused is whether the engine answered the request with a refusal (a 4xx
// status): it did not act on it. Any other error, a 2xx that is not the answer
// expected included, leaves it unknown. The tool routes word a 403 and a 404 as a
// sentence of their own (toolHTTPErr) that carries the exit code of that status,
// and nothing else gives a request an Auth or NotFound code.
func engineRefused(err error) bool {
	var refusal *apiRefusal
	if errors.As(err, &refusal) {
		return refusal.status >= http.StatusBadRequest && refusal.status < http.StatusInternalServerError
	}
	code := exitcode.From(err)
	return code == exitcode.Auth || code == exitcode.NotFound
}

// showKeyResult is the page after a key was added and tested: what the test found,
// and the command that fixes what it found, as `provider add` prints it.
func showKeyResult(p *tui.Panel, rec map[string]any, name string) {
	ref, probe := str(rec, "provider_ref"), str(rec, "probe_state")
	lines := []string{
		"  Added " + oneRow(name) + " (" + oneRow(str(rec, "key_hint")) + ").",
		"  Connection test: " + connectionWords(probe) + ".",
	}
	switch {
	case probe == "ok":
	case !plainName.MatchString(ref):
		// The reference is the engine's text: not a name to put into a command.
		lines = append(lines, "", "  Its reference is not a plain name, so no command is given for it. List the keys:", "    olivares provider ls")
	case probe == "refused":
		lines = append(lines, "", "  The provider rejected this key. Replace it:", "    olivares provider rotate "+ref+" < new-key.txt")
	default:
		lines = append(lines, "", "  Test it again:", "    olivares provider test "+ref)
	}
	showResult(p, probeRole(probe), "Enter goes back.", lines...)
}
