// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// sessionview.go renders a session's output frames for a person, by the rules of the
// console's conversation view (web/src/features/sessions/conversation-frames.ts):
// assistant text as text, a tool call as one line, the turn result as a footer,
// quiet system lines, and a line it cannot name kept rather than dropped. `-o json`
// bypasses it and prints the frames as they are.

// sessionSummaryCap is the width of a one-line summary, as in the console.
const sessionSummaryCap = 96

type sessionView struct {
	r *termrender.Renderer
	// driver is the run's tool, so a sign-in hint can name its command.
	driver string
	// engineSupplied is set when the engine supplies the run's credential: signing the tool
	// in would not fix its refusal.
	engineSupplied bool
	// lastText is the last assistant text shown, so a result that only repeats it
	// (an API error arrives as both) is not printed twice.
	lastText string
	// failed is set when the last turn ended in an error; signedOut when the tool
	// said it is not signed in.
	failed, signedOut bool
	// readyShown: Claude repeats its init frame at every turn, so the ready line
	// is shown once (and never by send, whose start line already said it).
	readyShown bool
	// tools are the tool calls already on screen, so their progress stays quiet.
	tools map[string]bool
	// acpText gathers an ACP message's chunks (acpKind, acpMessage) until it ends.
	acpText             strings.Builder
	acpKind, acpMessage string
	// retries are the API retry causes already on screen, so a cause is said once.
	retries map[string]bool
}

func newSessionView(w io.Writer) *sessionView {
	return &sessionView{r: renderTo(w)}
}

// say writes one line of untrusted session text: control sequences are removed
// first (termSafe), then this CLI's own colour is applied.
func (v *sessionView) say(s string, role termrender.Role) { v.r.Line(v.r.Paint(termSafe(s), role)) }

func (v *sessionView) quiet(s string) { v.say(s, termrender.RoleMuted) }

// render shows one output line and reports whether it ended a turn.
func (v *sessionView) render(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return false
	}
	var frame map[string]any
	if json.Unmarshal([]byte(line), &frame) != nil {
		// Not a frame: the tool wrote plain text (stderr, a warning). Show it.
		v.say(line, termrender.RoleNone)
		return false
	}
	switch str(frame, "type") {
	case "system":
		v.system(frame)
	case "assistant":
		v.assistant(frame)
	case "user":
		v.toolErrors(frame)
	case "result":
		v.result(frame)
		return true
	case "stream_event":
		// Partial text; the complete assistant message follows and is shown then.
	case "tool_progress":
		v.toolProgress(frame)
	case "control_response":
		// The answer to a control request the engine sent (an interrupt); the turn's
		// own frames say what happened.
	case "rate_limit_event":
		if info, _ := frame["rate_limit_info"].(map[string]any); str(info, "status") != "allowed" {
			v.quiet("· rate limit: " + strings.ReplaceAll(str(info, "status"), "_", " "))
		}
	case "":
		// Codex, OpenCode and Grok Build: JSON-RPC peers with no "type", a method or an id.
		if ended, ok := v.acp(frame); ok {
			return ended
		}
		return v.codex(frame, line)
	default:
		if t := str(frame, "type"); t != "" {
			v.quiet("· " + truncateSummary(t))
		} else {
			v.quiet("· " + truncateSummary(line))
		}
	}
	return false
}

// codex reads a Codex app-server frame by the console's rules (FH 634d6d00): an
// agentMessage item is the reply, a tool item (command, file change, MCP or
// dynamic tool, web search) one line, turn/completed the footer. The driver's own
// handshake responses and streaming deltas stay quiet; an error is shown. A
// notification it cannot name is one quiet line, never dropped. HU 016: follow
// printed these frames raw.
func (v *sessionView) codex(frame map[string]any, line string) bool {
	method := str(frame, "method")
	params, _ := frame["params"].(map[string]any)
	if method == "" {
		if e, ok := frame["error"].(map[string]any); ok {
			v.say("✗ "+str(e, "message"), termrender.RoleFail)
		} else if _, response := frame["result"]; !response {
			v.quiet("· " + truncateSummary(line))
		}
		return false
	}
	switch {
	case method == "item/completed":
		item, _ := params["item"].(map[string]any)
		switch str(item, "type") {
		case "agentMessage":
			if text := strings.TrimSpace(str(item, "text")); text != "" {
				v.say(text, termrender.RoleNone)
				v.lastText = text
			}
		case "commandExecution":
			v.say("→ command "+truncateSummary(str(item, "command")), termrender.RoleMuted)
			if code, ok := item["exitCode"].(float64); ok && code != 0 {
				v.say(fmt.Sprintf("  ✗ exit %.0f", code), termrender.RoleFail)
			}
		case "fileChange":
			changes, _ := item["changes"].([]any)
			for _, ch := range changes {
				if c, ok := ch.(map[string]any); ok {
					v.say("→ edit "+truncateSummary(str(c, "path")), termrender.RoleMuted)
				}
			}
		case "mcpToolCall", "dynamicToolCall":
			name := str(item, "tool")
			if server := str(item, "server"); server != "" {
				name = server + "." + name
			}
			line := "→ " + name
			if args := summariseArgs(item["arguments"]); args != "" {
				line += " " + args
			}
			v.say(line, termrender.RoleMuted)
			if e, ok := item["error"].(map[string]any); ok {
				v.say("  ✗ "+truncateSummary(str(e, "message")), termrender.RoleFail)
			}
		case "webSearch":
			v.say("→ web search "+truncateSummary(str(item, "query")), termrender.RoleMuted)
		case "userMessage", "reasoning", "hookPrompt":
			// The operator's own turn and the model's private reasoning are not shown.
		default:
			v.quiet("· " + truncateSummary(str(item, "type")))
		}
	case method == "turn/completed":
		turn, _ := params["turn"].(map[string]any)
		status := str(turn, "status")
		failed := status == "failed"
		if e, ok := turn["error"].(map[string]any); ok && failed && str(e, "message") != v.lastText {
			v.say(str(e, "message"), termrender.RoleFail)
		}
		footer, role := "— turn finished", termrender.RoleMuted
		switch status {
		case "failed":
			footer, role = "— turn failed", termrender.RoleFail
		case "interrupted":
			footer = "— turn interrupted"
		}
		v.say(footer, role)
		v.failed, v.lastText = failed, ""
		return true
	case method == "error":
		if e, ok := params["error"].(map[string]any); ok && params["willRetry"] == true {
			v.say("! "+str(e, "message")+" (retrying)", termrender.RoleWarn)
		} else if ok {
			v.say(str(e, "message"), termrender.RoleFail)
			v.lastText = str(e, "message")
		}
	case method == "configWarning", method == "deprecationNotice":
		v.say("! "+str(params, "summary"), termrender.RoleWarn)
	case method == "warning", method == "guardianWarning":
		v.say("! "+str(params, "message"), termrender.RoleWarn)
	case strings.HasSuffix(method, "/requestApproval"), method == "execCommandApproval", method == "applyPatchApproval":
		// The engine's approval authority answers it; the operator sees that it waits.
		what := "Codex is waiting for approval"
		if cmd := str(params, "command"); cmd != "" {
			what += " to run " + truncateSummary(cmd)
		}
		v.say("! "+what, termrender.RoleWarn)
	case codexProgress(method):
		// Progress the completed item and the turn's end already tell.
	default:
		v.quiet("· " + truncateSummary(method))
	}
	return false
}

// acp reads an Agent Client Protocol frame (OpenCode, Grok Build) by the same rules:
// the agent_message_chunk texts of one message joined into the reply, a tool call one
// line, a failed tool call its error, a permission request the line that says the
// session waits, and the session/prompt result (stopReason, usage) the turn's footer.
// The person's own message (user_message_chunk, sent when a conversation is replayed)
// is shown after "›". Private reasoning, command lists, mode, configuration and plan
// updates stay quiet; an update it cannot name is one quiet line. HU2-01: follow
// printed only "· session/update" and the reply was never shown.
func (v *sessionView) acp(frame map[string]any) (ended, handled bool) {
	method := str(frame, "method")
	params, _ := frame["params"].(map[string]any)
	switch {
	case method == "session/update":
		v.acpUpdate(params["update"])
		return false, true
	case method == "session/request_permission":
		v.flushACP()
		what := " is waiting for approval"
		if call, _ := params["toolCall"].(map[string]any); str(call, "title") != "" {
			what += " to run " + truncateSummary(str(call, "title"))
		}
		v.say("! "+toolName(v.driver)+what, termrender.RoleWarn)
		return false, true
	case method != "":
		return false, false
	}
	if e, ok := frame["error"].(map[string]any); ok && (v.driver == "opencode" || v.driver == "grok") {
		// An ACP prompt that fails is answered with an error instead of a stopReason.
		v.flushACP()
		v.say("✗ "+str(e, "message"), termrender.RoleFail)
		v.say("— turn failed", termrender.RoleFail)
		v.failed, v.lastText = true, ""
		return true, true
	}
	result, _ := frame["result"].(map[string]any)
	reason := str(result, "stopReason")
	if reason == "" {
		return false, false
	}
	v.flushACP()
	footer, role := "— turn finished", termrender.RoleMuted
	switch reason {
	case "end_turn":
	case "cancelled":
		footer = "— turn interrupted"
	case "refusal":
		footer, role = "— turn refused by the model", termrender.RoleFail
	default:
		footer, role = "— turn stopped: "+strings.ReplaceAll(reason, "_", " "), termrender.RoleWarn
	}
	if usage, _ := result["usage"].(map[string]any); usage != nil {
		if n, ok := usage["totalTokens"].(float64); ok && n > 0 {
			footer += fmt.Sprintf(" · %.0f tokens", n)
		}
	}
	v.say(footer, role)
	v.failed, v.lastText = reason == "refusal", ""
	return true, true
}

func (v *sessionView) acpUpdate(raw any) {
	u, _ := raw.(map[string]any)
	switch kind := str(u, "sessionUpdate"); kind {
	case "agent_message_chunk", "user_message_chunk":
		if id := str(u, "messageId"); kind != v.acpKind || id != v.acpMessage {
			v.flushACP()
			v.acpKind, v.acpMessage = kind, id
		}
		if content, _ := u["content"].(map[string]any); str(content, "type") == "text" {
			v.acpText.WriteString(str(content, "text"))
		}
	case "agent_thought_chunk", "plan", "available_commands_update", "current_mode_update",
		"config_option_update", "session_info_update", "usage_update":
		// Reasoning and the session's own bookkeeping: the reply and the turn's end tell it.
	case "tool_call":
		v.flushACP()
		v.seenTool(str(u, "toolCallId"))
		line := "→ " + acpToolTitle(u)
		if args := summariseArgs(u["rawInput"]); args != "" && !strings.Contains(line, args) {
			line += " " + args
		}
		v.say(line, termrender.RoleMuted)
		if str(u, "status") == "failed" {
			v.say("  ✗ failed", termrender.RoleFail)
		}
	case "tool_call_update":
		if str(u, "status") != "failed" {
			return
		}
		v.flushACP()
		msg := acpToolText(u["content"])
		if msg == "" {
			msg = acpToolTitle(u) + " failed"
		}
		v.say("  ✗ "+truncateSummary(msg), termrender.RoleFail)
	case "":
		v.flushACP()
		v.quiet("· session/update")
	default:
		v.flushACP()
		v.quiet("· " + truncateSummary(strings.ReplaceAll(kind, "_", " ")))
	}
}

// finish shows what the view still holds when the stream stops before a turn's end
// (it ended, failed, or the command was cancelled): an ACP reply already received is
// never dropped (SR2C on cfa80204).
func (v *sessionView) finish() { v.flushACP() }

// flushACP shows the ACP message gathered so far: the agent's reply as text, the
// person's own message after "›".
func (v *sessionView) flushACP() {
	text, kind := strings.TrimSpace(v.acpText.String()), v.acpKind
	v.acpText.Reset()
	v.acpKind, v.acpMessage = "", ""
	switch {
	case text == "":
	case kind == "user_message_chunk":
		v.say("› "+text, termrender.RoleMuted)
	default:
		v.say(text, termrender.RoleNone)
		v.lastText = text
	}
}

func acpToolTitle(u map[string]any) string {
	for _, k := range []string{"title", "kind", "toolCallId"} {
		if s := strings.TrimSpace(str(u, k)); s != "" {
			return truncateSummary(s)
		}
	}
	return "a tool"
}

// acpToolText is the first text of a tool call's content ({"type":"content",
// "content":{"type":"text",...}}).
func acpToolText(raw any) string {
	items, _ := raw.([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		inner, _ := m["content"].(map[string]any)
		if str(inner, "type") == "text" {
			if s := strings.TrimSpace(str(inner, "text")); s != "" {
				return s
			}
		}
	}
	return ""
}

// codexProgressPrefixes are the Codex 0.153.4 notifications (ServerNotification
// schema) that report progress or host state the reply and the turn's end already
// tell, or that the operator does not act on.
var codexProgressPrefixes = []string{
	"item/started", "item/autoApprovalReview/", "item/commandExecution/terminalInteraction",
	"item/fileChange/patchUpdated", "item/mcpToolCall/progress", "item/reasoning/",
	"turn/started", "turn/diff/", "turn/plan/", "turn/moderationMetadata",
	"thread/", "account/", "remoteControl/", "hook/", "mcpServer/", "model/", "modelProvider/",
	"process/", "fs/", "project/", "skills/", "app/", "serverRequest/", "fuzzyFileSearch/",
	"externalAgentConfig/", "windowsSandbox/",
}

func codexProgress(method string) bool {
	if strings.HasSuffix(strings.ToLower(method), "delta") {
		return true
	}
	for _, p := range codexProgressPrefixes {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}

func (v *sessionView) system(frame map[string]any) {
	switch sub := str(frame, "subtype"); {
	case sub == "init":
		if v.readyShown {
			return
		}
		v.readyShown = true
		parts := []string{"● session ready"}
		for _, k := range []string{"model", "cwd"} {
			if s := str(frame, k); s != "" {
				parts = append(parts, s)
			}
		}
		v.quiet(strings.Join(parts, " · "))
	case strings.HasPrefix(sub, "hook_"), sub == "task_started", sub == "task_progress":
		// Hook and task bookkeeping: the tool call is already on screen.
	case sub == "task_notification":
		if status := str(frame, "status"); status != "" {
			v.quiet("· task " + strings.ReplaceAll(status, "_", " "))
		}
	case sub == "api_retry":
		v.apiRetry(frame)
	case sub != "":
		v.quiet("· " + strings.ReplaceAll(sub, "_", " "))
	}
}

// apiRetry says why the tool retries its provider call, once per cause (HU2-13): a
// refused API key printed "· api retry" up to ten times and never the 401 every frame
// carried. A refused credential is a failure with what fixes it; another cause is one
// warning with its status.
func (v *sessionView) apiRetry(frame map[string]any) {
	cause := str(frame, "error")
	status, _ := frame["error_status"].(float64)
	key := fmt.Sprintf("%s/%.0f", cause, status)
	if v.retries[key] {
		return
	}
	if v.retries == nil {
		v.retries = map[string]bool{}
	}
	v.retries[key] = true
	name := toolName(v.driver)
	if name == "" {
		name = "The tool"
	}
	if cause == "authentication_failed" || status == 401 || status == 403 {
		v.say(fmt.Sprintf("✗ %s's provider refused its credential (HTTP %.0f). Replace the API key in AI tools › API keys, "+
			"or sign it in: olivares tool login %s", name, status, v.driver), termrender.RoleFail)
		return
	}
	what := strings.ReplaceAll(cause, "_", " ")
	if what == "" {
		what = "the provider did not answer"
	}
	if status > 0 {
		what += fmt.Sprintf(" (HTTP %.0f)", status)
	}
	if limit, ok := frame["max_retries"].(float64); ok && limit > 0 {
		what += fmt.Sprintf("; retrying, up to %.0f times", limit)
	}
	v.say("! "+name+": "+what, termrender.RoleWarn)
}

func (v *sessionView) assistant(frame map[string]any) {
	if str(frame, "error") == "authentication_failed" {
		v.signedOut = true
	}
	for _, block := range contentBlocks(frame["message"]) {
		switch str(block, "type") {
		case "text":
			if text := strings.TrimSpace(str(block, "text")); text != "" {
				v.say(text, termrender.RoleNone)
				v.lastText = text
			}
		case "tool_use":
			v.seenTool(str(block, "id"))
			line := "→ " + str(block, "name")
			if args := summariseArgs(block["input"]); args != "" {
				line += " " + args
			}
			v.say(line, termrender.RoleMuted)
		}
	}
}

// toolProgress is Claude Code's clock for a running tool (every few seconds). A tool
// already on screen stays quiet; one this view has not seen (a sub-agent's) gets one
// line, once.
func (v *sessionView) toolProgress(frame map[string]any) {
	id := str(frame, "tool_use_id")
	if id != "" && v.tools[id] {
		return
	}
	v.seenTool(id)
	name := str(frame, "tool_name")
	if name == "" {
		name = "a tool"
	}
	v.quiet("· " + truncateSummary(name) + " is running")
}

func (v *sessionView) seenTool(id string) {
	if id == "" {
		return
	}
	if v.tools == nil {
		v.tools = map[string]bool{}
	}
	v.tools[id] = true
}

// toolErrors shows a tool result only when it failed: a successful result is the
// tool's raw output, which the assistant's next message describes.
func (v *sessionView) toolErrors(frame map[string]any) {
	for _, block := range contentBlocks(frame["message"]) {
		if str(block, "type") == "tool_result" && block["is_error"] == true {
			v.say("  ✗ "+truncateSummary(textOf(block["content"])), termrender.RoleFail)
		}
	}
}

func (v *sessionView) result(frame map[string]any) {
	failed := frame["is_error"] == true || strings.HasPrefix(str(frame, "subtype"), "error")
	if text := strings.TrimSpace(str(frame, "result")); failed && text != "" && text != v.lastText {
		v.say(text, termrender.RoleFail)
	}
	parts := []string{"turn finished"}
	if failed {
		parts = []string{"turn failed"}
	}
	if cost, ok := frame["total_cost_usd"].(float64); ok && cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", cost))
	}
	if ms, ok := frame["duration_ms"].(float64); ok && ms >= 1000 {
		parts = append(parts, fmt.Sprintf("%.1fs", ms/1000))
	} else if ok && ms > 0 {
		parts = append(parts, fmt.Sprintf("%.0fms", ms))
	}
	role := termrender.RoleMuted
	if failed {
		role = termrender.RoleFail
	}
	v.r.Line(v.r.Paint("— "+strings.Join(parts, " · "), role))
	if failed && v.signedOut {
		switch {
		case v.engineSupplied:
			v.r.Line("The credential the engine supplies for this session was refused by the provider. Ask the engine's operator to replace it (a key from Providers is replaced in Providers), then send again.")
		case toolSignsIn(v.driver):
			v.r.Line(fmt.Sprintf("%s is not signed in. Sign it in: olivares tool login %s", toolName(v.driver), v.driver))
		default:
			v.r.Line("The tool is not signed in on the engine's host: sign it in there, then send again.")
		}
	}
	v.failed, v.signedOut, v.lastText = failed, false, ""
}

// contentBlocks returns a message's content blocks (a string content is one text).
func contentBlocks(message any) []map[string]any {
	m, _ := message.(map[string]any)
	switch c := m["content"].(type) {
	case string:
		return []map[string]any{{"type": "text", "text": c}}
	case []any:
		out := make([]map[string]any, 0, len(c))
		for _, b := range c {
			if bm, ok := b.(map[string]any); ok {
				out = append(out, bm)
			}
		}
		return out
	}
	return nil
}

func textOf(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if bm, ok := b.(map[string]any); ok && str(bm, "type") == "text" {
				parts = append(parts, str(bm, "text"))
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// summariseArgs is a tool call's input on one line: the value alone when there is
// one key (a command, a path), else compact JSON.
func summariseArgs(v any) string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return ""
	}
	if len(m) == 1 {
		for _, val := range m {
			if s, ok := val.(string); ok {
				return truncateSummary(s)
			}
		}
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if s, ok := m[k].(string); ok && s != "" {
			return truncateSummary(s)
		}
	}
	b, _ := json.Marshal(m)
	return truncateSummary(string(b))
}

func truncateSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > sessionSummaryCap {
		return strings.TrimRight(string(r[:sessionSummaryCap-1]), " ") + "…"
	}
	return s
}

// scanSSE reads a server-sent-events body and calls fn with each event's name and
// data until fn returns false or the body ends.
func scanSSE(body io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if !fn(event, strings.TrimPrefix(line, "data: ")) {
				return nil
			}
		}
	}
	return sc.Err()
}

// termSafe removes what a terminal would act on instead of print: escape
// sequences (CSI, and OSC/DCS/SOS/PM/APC up to their terminator — an OSC 52 would
// write the clipboard) and every other C0/C1 control except newline and tab.
// Session output, names and paths come from the engine and the agent, so none of
// them may drive the operator's terminal (SR2 on 36e31097).
func termSafe(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }) {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == 0x1b:
			i = escapeEnd(rs, i)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeEnd returns the index of the last rune of the escape sequence at rs[i].
func escapeEnd(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i
	}
	switch rs[i+1] {
	case '[':
		j := i + 2
		for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
			j++
		}
		return min(j, len(rs)-1)
	case ']', 'P', 'X', '^', '_':
		for j := i + 2; j < len(rs); j++ {
			if rs[j] == 0x07 {
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
		return len(rs) - 1
	}
	return i + 1
}
