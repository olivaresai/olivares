// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

// The commands, skills and agents a live session's tool offers, as the TOOL reports
// them (the console's "/" menu). Olivares keeps no list of its own: a skill added to
// the user's or the project's folder is in the next answer, with no release.
//
//	GET /runs/{ref}/commands   {tool, reported, commands:[{name, description, argument_hint, source, kind}]}
//
// Claude Code reports its catalog to the live session's stdin protocol: the SDK's
// `initialize` control request, answered with every slash command AND skill (the
// tool's built-ins, the user's, the project's and each plugin's) and every agent,
// each with a description. It is asked of the session's own process, not of a
// second one: only the live process knows the plugins, settings and folders THIS
// session runs with (a second process started with no setting sources lists 48
// built-ins and none of the user's or the project's skills, measured on Claude
// Code 2.1.296). The request carries no prompt and starts no turn.
//
// The answer is the engine's own conversation with the tool. The bridge hands it to
// the waiter and publishes nothing: it holds the tool's account block and tens of
// kilobytes of text that belong neither on the attach stream nor in the recorded I/O.
// An id this engine issued is withheld when its answer arrives, also when the caller
// has left or the tool answered late; any other frame is the child's output.
//
// Asking writes a control line to the child's stdin, as an interrupt does, so the route
// needs the run's write permission and not only its read, and a read that has to ask the
// tool gets the authority checks of any other write to it (guardToolAsk): a launch whose
// Claim moved, whose profile was retired or whose work lease needs a fence is not asked.
//
// Codex, Gemini CLI and OpenCode report their catalogs through their own interfaces;
// until each is read here, their sessions answer reported=false and the console says so.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/model"
)

const (
	// commandsRequestPrefix marks the control requests this feature issues; the bridge
	// looks at a frame only when it carries it.
	commandsRequestPrefix = "olv-commands-"
	// maxToolCommands bounds one catalog (commands and agents together).
	maxToolCommands = 2000
	// maxToolCommandName bounds a command name in bytes; a name that cannot be typed
	// as one word is not a command.
	maxToolCommandName = 128
	// maxToolCommandText bounds a description or an argument hint in runes.
	maxToolCommandText = 1024
)

// Variables so tests stay fast.
var (
	// toolCommandsTTL is how long a catalog the tool reported is served without asking
	// again: the menu opens on every "/", the tool is asked at most this often.
	toolCommandsTTL = 5 * time.Second
	// toolCommandsWait bounds how long the tool may take to answer.
	toolCommandsWait = 10 * time.Second
	// toolCommandsRetry is how long after a failed ask the tool is not asked again, so
	// a silent tool costs one wait and not one per caller.
	toolCommandsRetry = 2 * time.Second
)

// maxAbandonedCommandRequests bounds the ids kept to withhold late answers. At the bound
// the tool is not asked again until it answers one: an issued id is never forgotten,
// because its late answer carries the tool's account block.
const maxAbandonedCommandRequests = 16

// toolCommand is one entry of the menu. Kind is "command" (a slash command or a
// skill: the tool types both as /name) or "agent". Source is "built-in", "user",
// "project" or "plugin" when the tool says so and "" when it does not.
type toolCommand struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argument_hint,omitempty"`
	Source       string `json:"source"`
	Kind         string `json:"kind"`
}

// toolCommandsDTO is the answer. Reported is false when the tool reported nothing:
// the session is not live on this node, or its tool has no reader here yet.
type toolCommandsDTO struct {
	Tool     string `json:"tool"`
	Reported bool   `json:"reported"`
	// Stale is true when the last ask of the tool failed and Commands are what it
	// reported before.
	Stale    bool          `json:"stale,omitempty"`
	Commands []toolCommand `json:"commands"`
}

// toolCommandCache is one live session's catalog and the requests waiting for the
// tool's answer. Its lifetime is the process's: a resumed session asks again.
type toolCommandCache struct {
	// asking holds one read of the tool at a time per session and guards the fields
	// below. A caller waits for it only while it is still there to read the answer.
	asking     chan struct{}
	askingOnce sync.Once
	items      []toolCommand
	at         time.Time
	ok         bool
	// stale: the last ask failed, items are the answer before it.
	stale bool
	// failure and failedAt are the last failed ask, for toolCommandsRetry.
	failure  error
	failedAt time.Time
	// dirty is set when the tool said its catalog changed: the next read asks again,
	// whatever the cache's age.
	dirty atomic.Bool

	// waits maps each request id still to be answered to its waiter; a nil waiter is a
	// request whose caller left (or whose write may have reached the child): its answer
	// is still withheld when it arrives.
	waitMu sync.Mutex
	waits  map[string]chan []byte
}

// acquire waits for the session's turn to ask the tool, or for the caller to leave.
func (c *toolCommandCache) acquire(ctx context.Context) error {
	c.askingOnce.Do(func() { c.asking = make(chan struct{}, 1) })
	select {
	case c.asking <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *toolCommandCache) release() { <-c.asking }

// expect registers the request id the bridge should hand the answer for.
func (c *toolCommandCache) expect(id string) <-chan []byte {
	ch := make(chan []byte, 1)
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if c.waits == nil {
		c.waits = map[string]chan []byte{}
	}
	c.waits[id] = ch
	return ch
}

// forget drops a request that never reached the child.
func (c *toolCommandCache) forget(id string) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	delete(c.waits, id)
}

// abandon keeps a request whose caller left, whose tool is late or whose write may have
// reached the child, so that its answer, if it comes, is still withheld. Asks are one at a
// time per session and refused at the bound (askClaudeCommands), so a tool that never
// answers cannot grow the map and no issued id ever loses its cover.
func (c *toolCommandCache) abandon(id string) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if _, ok := c.waits[id]; ok { // absent: the answer arrived meanwhile
		c.waits[id] = nil
	}
}

// commandWaitCount is the number of requests a caller still waits for, and the number
// whose answer is only to be withheld.
func (lr *liveRun) commandWaitCount() (waiting, abandoned int) {
	lr.commands.waitMu.Lock()
	defer lr.commands.waitMu.Unlock()
	for _, ch := range lr.commands.waits {
		if ch == nil {
			abandoned++
		}
	}
	return len(lr.commands.waits) - abandoned, abandoned
}

// takeCommandsResponse hands the tool's answer to the request that waits for it (or
// drops it, when the caller left) and reports whether the frame was that answer. Any
// other frame, including one that merely claims an id this engine never issued, is the
// child's output and keeps its way to the ring.
func (lr *liveRun) takeCommandsResponse(data []byte) bool {
	if !bytes.Contains(data, []byte(`"`+commandsRequestPrefix)) {
		return false
	}
	var f struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &f) != nil || f.Type != "control_response" || !strings.HasPrefix(f.Response.RequestID, commandsRequestPrefix) {
		return false
	}
	c := &lr.commands
	c.waitMu.Lock()
	ch, ok := c.waits[f.Response.RequestID]
	delete(c.waits, f.Response.RequestID)
	c.waitMu.Unlock()
	if !ok {
		return false
	}
	if ch != nil {
		ch <- append([]byte(nil), data...)
	}
	return true
}

// oversizedResponse recognizes the header Claude writes before its catalog.
// Only an id still in the existing waiter map can recover from the line cap;
// the replacement follows the ordinary bridge and stale/error catalog paths.
// Nothing from the tool's oversized payload is published or kept.
func (c *toolCommandCache) oversizedResponse(prefix []byte) []byte {
	d := json.NewDecoder(bytes.NewReader(prefix))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	var kind, id string
	for d.More() {
		key, err := d.Token()
		if err != nil {
			break
		}
		if key == "type" {
			if d.Decode(&kind) != nil {
				return nil
			}
		} else if key == "response" {
			if token, err := d.Token(); err != nil || token != json.Delim('{') {
				return nil
			}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					break
				}
				if key == "request_id" {
					if d.Decode(&id) != nil {
						return nil
					}
				} else {
					var skip json.RawMessage
					if d.Decode(&skip) != nil {
						break // the catalog exceeds the retained prefix
					}
				}
			}
			break
		} else {
			var skip json.RawMessage
			if d.Decode(&skip) != nil {
				return nil
			}
		}
	}
	if kind != "control_response" || !strings.HasPrefix(id, commandsRequestPrefix) {
		return nil
	}
	c.waitMu.Lock()
	_, issued := c.waits[id]
	c.waitMu.Unlock()
	if !issued {
		return nil
	}
	// id was decoded as a string: quote it again rather than interpolating bytes
	// controlled by the child into a protocol record.
	quoted, _ := json.Marshal(id)
	return []byte(`{"type":"control_response","response":{"request_id":` + string(quoted) +
		`,"subtype":"error","error":"Claude Code command catalog exceeds the session output line limit"}}`)
}

// claudeFrameRun reports whether the live run is Claude Code driven by stream-json
// frames, as opposed to a provider with an owned protocol driver. A run with no
// profile driver recorded is a Claude run (launchDriverKey).
func claudeFrameRun(lr *liveRun, rec model.Record) bool {
	driver := rec.String(colRunProfileDriver)
	return lr.session == nil && Transport(rec.String(colTransport)) == TransportStreamJSON &&
		(driver == "" || driver == providerDriverClaude)
}

// runToolCommands is the catalog of one session's tool. A session that is not live
// on this node, or whose tool is not read here, answers reported=false with an
// empty list. A live Claude Code session that does not answer is 504, and one that
// refuses is 502, unless it reported before: its last catalog is served then, marked
// stale. It does not take the per-run operation lock (lockRunContext) that stop,
// resume, interrupt and input hold: that lock would keep Stop waiting behind a silent
// tool's ten-second answer. toolEnded and the authority check cover those cases.
func (m *Module) runToolCommands(ctx context.Context, tenant model.TenantID, runRef string) (toolCommandsDTO, error) {
	none := toolCommandsDTO{Commands: []toolCommand{}}
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		return none, nil
	}
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return none, err
	}
	if !claudeFrameRun(lr, rec) {
		none.Tool = rec.String(colRunProfileDriver)
		return none, nil
	}
	none.Tool = providerDriverClaude
	if lr.toolEnded() || rec.String(colState) != stateRunning {
		return none, nil
	}
	c := &lr.commands
	if err := c.acquire(ctx); err != nil {
		return none, err
	}
	defer c.release()
	if err := m.refreshToolCommands(ctx, lr, rec, c); err != nil {
		if errors.Is(err, errToolEnded) {
			return none, nil
		}
		return none, err
	}
	return toolCommandsDTO{
		Tool: providerDriverClaude, Reported: true, Stale: c.stale, Commands: append([]toolCommand{}, c.items...),
	}, nil
}

// toolEnded reports that the session has no tool left to ask: its process finished or
// never started, or a stop was requested. A finished process stays registered for a
// while so its output can be replayed; nothing it said before is current.
func (lr *liveRun) toolEnded() bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.finalized || lr.launchFailed || lr.stopRequested
}

// commandAsksExhausted: the tool left maxAbandonedCommandRequests requests unanswered, so
// it is not written to again until it answers one.
func (lr *liveRun) commandAsksExhausted() bool {
	_, abandoned := lr.commandWaitCount()
	return abandoned >= maxAbandonedCommandRequests
}

// refreshToolCommands asks the tool when the cache is empty, old or marked changed,
// and keeps the cache honest about it. The caller holds the session's turn to ask
// (c.acquire). After a failed ask the
// tool is not asked again for toolCommandsRetry: the callers queued behind a silent
// tool get its failure, or the last catalog, at once instead of one full wait each.
// A caller that left is not the tool's fault and is not recorded as a failure.
func (m *Module) refreshToolCommands(ctx context.Context, lr *liveRun, rec model.Record, c *toolCommandCache) error {
	changed := c.dirty.Swap(false)
	if c.ok && !changed && time.Since(c.at) < toolCommandsTTL {
		return nil
	}
	// A change notice the tool sent while this ask ran is not undone by a read that ends
	// early: only the notice this read consumed is put back, never a newer one erased.
	keepNotice := func() {
		if changed {
			c.dirty.Store(true)
		}
	}
	err := c.failure
	if err == nil || time.Since(c.failedAt) >= toolCommandsRetry {
		// The turn to ask may have waited behind another read, and the session may be
		// stopping by now.
		if lr.toolEnded() {
			keepNotice()
			return errToolEnded
		}
		// A refusal is a decision, not a failure of the tool: nothing is served from a
		// launch that lost its authority, and nothing is recorded against the tool. A tool
		// that is not to be written to anyway is not worth the authority transaction.
		if !lr.commandAsksExhausted() {
			if gerr := m.guardToolAsk(ctx, lr, rec); gerr != nil {
				keepNotice()
				return gerr
			}
		}
		var items []toolCommand
		if items, err = m.askClaudeCommands(ctx, lr); err == nil {
			c.items, c.at, c.ok, c.stale, c.failure = items, time.Now(), true, false, nil
			return nil
		}
		if ctx.Err() != nil {
			// The caller left: nothing is known about the tool, and the next read asks.
			keepNotice()
			return err
		}
		if errors.Is(err, errToolEnded) {
			return err
		}
		c.failure, c.failedAt = err, time.Now()
		m.warnf("session tool commands not refreshed", "run_ref", lr.runRef, "cause", clipCause(err.Error()))
	}
	if !c.ok {
		keepNotice()
		return err
	}
	// The catalog held is served, marked stale, and the next read tries again.
	c.stale = true
	c.dirty.Store(true)
	return nil
}

// guardToolAsk is the authority of an effect on the tool's stdin, the same two checks the
// input route applies before it writes there: a run that is bound to an active work lease
// needs its fence, and the launch must still be the one the durable Claim and the current
// profile name. A read that is answered from the cache crosses nothing and needs neither.
func (m *Module) guardToolAsk(ctx context.Context, lr *liveRun, rec model.Record) error {
	if err := m.refuseUnfencedActiveWork(ctx, lr.tenant, rec); err != nil {
		return err
	}
	return m.assertRunAuthority(ctx, lr)
}

// askClaudeCommands sends Claude Code's `initialize` control request on the live
// session's stdin and waits for the answer the bridge hands over.
func (m *Module) askClaudeCommands(ctx context.Context, lr *liveRun) ([]toolCommand, error) {
	id := commandsRequestPrefix + model.NewID().String()
	line, err := json.Marshal(map[string]any{
		"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "initialize"},
	})
	if err != nil {
		return nil, err
	}
	if lr.commandAsksExhausted() {
		return nil, &runErr{http.StatusGatewayTimeout, "Claude Code has not answered its earlier requests for its commands"}
	}
	answer := lr.commands.expect(id)
	if p, ok := lr.proc.(*procProcess); ok {
		p.commandResponses.Store(&lr.commands)
	}
	// One deadline covers the write and the wait. The write is not the caller's to
	// cancel: a request cancelled mid-write leaves a half-written frame and breaks the
	// session's stdin for good (procrunner.go Send).
	deadline := time.Now().Add(toolCommandsWait)
	sendCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	err = lr.proc.Send(sendCtx, line)
	cancel()
	if err != nil {
		// A write that failed at its deadline may still have reached the child: its
		// answer, if it comes, is withheld like any other.
		lr.commands.abandon(id)
		// The process may echo launch credentials; only a fixed diagnostic escapes.
		m.warnf("session tool commands not written", "run_ref", lr.runRef)
		return nil, &runErr{http.StatusBadGateway, "the session could not be asked for its commands"}
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case raw := <-answer:
		items, dropped, err := parseClaudeCommands(raw)
		if err != nil {
			return nil, &runErr{http.StatusBadGateway, "Claude Code did not report its commands: " + clipCause(err.Error())}
		}
		if dropped > 0 {
			m.warnf("session tool commands dropped", "run_ref", lr.runRef, "dropped", dropped)
		}
		return items, nil
	case <-lr.finalizedCh:
		lr.commands.forget(id)
		return nil, errToolEnded
	case <-ctx.Done():
		lr.commands.abandon(id)
		return nil, ctx.Err()
	case <-timer.C:
		lr.commands.abandon(id)
		return nil, &runErr{http.StatusGatewayTimeout, "Claude Code did not report its commands in time"}
	}
}

// parseClaudeCommands reads the commands and agents of an `initialize` answer. An entry
// that is not a command is dropped, and counted, not the catalog; a catalog with no
// command list at all is an error, because "the tool lists nothing" and "this is not a
// catalog" are different answers.
func parseClaudeCommands(raw []byte) (items []toolCommand, dropped int, err error) {
	var env struct {
		Response struct {
			Subtype  string `json:"subtype"`
			Error    string `json:"error"`
			Response struct {
				Commands *[]json.RawMessage `json:"commands"`
				Agents   []json.RawMessage  `json:"agents"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, 0, err
	}
	if env.Response.Subtype != "success" {
		return nil, 0, errNotACatalog(env.Response.Error)
	}
	if env.Response.Response.Commands == nil {
		return nil, 0, errNotACatalog("")
	}
	items = []toolCommand{}
	add := func(entry json.RawMessage, kind string) {
		var e struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			ArgumentHint string `json:"argumentHint"`
			Builtin      bool   `json:"builtin"`
		}
		if len(items) == maxToolCommands || json.Unmarshal(entry, &e) != nil || !validToolCommandName(e.Name) {
			dropped++
			return
		}
		c := toolCommand{Name: e.Name, Description: clipToolText(e.Description), Kind: kind}
		if kind == "command" {
			c.ArgumentHint = clipToolText(e.ArgumentHint)
			source, description := claudeCommandSource(e.Name, e.Description, e.Builtin)
			c.Source, c.Description = source, clipToolText(description)
		}
		items = append(items, c)
	}
	for _, entry := range *env.Response.Response.Commands {
		add(entry, "command")
	}
	for _, entry := range env.Response.Response.Agents {
		add(entry, "agent")
	}
	if dropped > 0 && len(items) == 0 {
		// Every entry failed: the shape is not the one this reads. An honest empty list
		// is a catalog; this is the tool changing under us, and "reported nothing" would
		// replace a good catalog and hide it.
		return nil, dropped, &runErr{http.StatusBadGateway, "none of the entries Claude Code reported is a command"}
	}
	return items, dropped, nil
}

// errToolEnded: the session ended while its tool was being asked; there is no tool left,
// so the read reports nothing rather than an old catalog as current.
var errToolEnded = &runErr{http.StatusConflict, "the session ended before Claude Code reported its commands"}

func errNotACatalog(cause string) error {
	if cause = clipCause(cause); cause != "" {
		return &runErr{http.StatusBadGateway, cause}
	}
	return &runErr{http.StatusBadGateway, "the answer lists no commands"}
}

// claudeCommandSource reads where a command comes from out of what Claude Code
// reports: a built-in says so, a user or project command ends its description in
// "(user)" or "(project)", and a plugin's is named "plugin:name" and starts its
// description with "(plugin)". Nothing else is a source; the description is left
// as the tool wrote it.
func claudeCommandSource(name, description string, builtin bool) (source, text string) {
	if builtin {
		return "built-in", description
	}
	for _, s := range []string{"user", "project"} {
		if t, ok := strings.CutSuffix(description, " ("+s+")"); ok {
			return s, t
		}
	}
	if plugin, _, ok := strings.Cut(name, ":"); ok && plugin != "" {
		if t, ok := strings.CutPrefix(description, "("+plugin+") "); ok {
			return "plugin", t
		}
	}
	return "", description
}

// validToolCommandName accepts what a person can type after "/" as one word.
func validToolCommandName(name string) bool {
	return name != "" && len(name) <= maxToolCommandName &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

// clipToolText makes the tool's text one line of printable characters, at most
// maxToolCommandText runes: a description is shown, never interpreted.
func clipToolText(s string) string {
	s = strings.Map(func(r rune) rune {
		// Control characters and invisible format characters (bidirectional overrides,
		// zero-width joiners) are not text a person reads, and can disguise a name.
		if (unicode.IsControl(r) && !unicode.IsSpace(r)) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxToolCommandText {
		return s
	}
	return string([]rune(s)[:maxToolCommandText]) + "…"
}

// handleRunCommands lists the commands, skills and agents the session's tool
// reports. It starts no turn and runs nothing: a command is run by typing it into
// the session, which is governed like any other message.
func (m *Module) handleRunCommands(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	got, err := m.runToolCommands(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		if r.Context().Err() == nil {
			writeRunErr(w, err)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, got)
}

// sessionsRunCommandsResponses publishes what GET /runs/{ref}/commands answers.
func sessionsRunCommandsResponses(method, pattern string) (map[string]any, bool) {
	if method != http.MethodGet || pattern != "/runs/{ref}/commands" {
		return nil, false
	}
	resp := api.GenericModuleResponses()
	str := func(description string) map[string]any { return oas.Obj("type", "string", "description", description) }
	resp["200"] = oas.Obj("description", "the commands, skills and agents the session's tool reports",
		"content", oas.Obj("application/json", oas.Obj("schema", oas.Obj(
			"type", "object", "additionalProperties", false, "required", oas.Enum("tool", "reported", "commands"),
			"properties", oas.Obj(
				"tool", str("The session's tool; empty when the session is not live on this node."),
				"reported", oas.Obj("type", "boolean", "description", "False when the tool reported nothing: the session is not live on this node, or its tool is not read yet."),
				"stale", oas.Obj("type", "boolean", "description", "True when the last ask of the tool failed and the commands are what it reported before."),
				"commands", oas.Obj("type", "array", "items", oas.Obj(
					"type", "object", "additionalProperties", false, "required", oas.Enum("name", "description", "source", "kind"),
					"properties", oas.Obj(
						"name", str("What follows the slash, without it."),
						"description", str("One line, as the tool wrote it."),
						"argument_hint", str("How the tool shows the arguments, when it does."),
						"source", oas.Obj("type", "string", "enum", oas.Enum("", "built-in", "user", "project", "plugin"),
							"description", "Where the command comes from, when the tool says so."),
						"kind", oas.Obj("type", "string", "enum", oas.Enum("command", "agent"),
							"description", "A command is a slash command or a skill; both are typed as /name."),
					))),
			),
		))))
	resp["409"] = oas.JSONResp("the session's launch is no longer its authority, or its work lease needs a fence")
	resp["502"] = oas.JSONResp("the tool refused or could not be asked for its commands")
	resp["504"] = oas.JSONResp("the tool did not report its commands in time")
	return resp, true
}
