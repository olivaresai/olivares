// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Handing a conversation off to another tool (#1845, #2243).
//
// No other tool can resume a conversation of Claude Code, Codex, Gemini CLI or
// OpenCode by id, so the honest way to continue it elsewhere is a NEW session,
// linked to the original, that starts from:
//
//   - a summary the person reviews and may edit before anything starts, generated
//     from the conversation the tool stored and by no model (a second provider
//     would otherwise see the conversation before the person chose it);
//   - the transcript, rendered as plain text (messages in full up to a limit, tool
//     calls and results shortened) and written under the target profile's
//     configuration home, the one place a confined session can always read, and
//     named in the first turn.
//
// The original is only read. A session that is still running is stopped first, the
// way a person would stop it, because its tool is still writing the conversation: the
// transcript is read once the child is gone. Its ledger gains one event naming the new
// session, and the new run row names the original in handoff_of.

// handoffAttachmentDir is the directory, under the target profile's configuration
// home, that holds the transcript a handoff attaches.
const handoffAttachmentDir = "olivares-handoff"

var (
	errHandoffRecordTooLarge = &runErr{http.StatusUnprocessableEntity,
		"the stored conversation holds a record too large to summarize; continue it with a move, or start a new session"}
	errHandoffTooLarge = &runErr{http.StatusUnprocessableEntity,
		"the stored conversation is too large to hand off; continue it with a move, or start a new session"}
	errHandoffNotStarted = &runErr{http.StatusConflict,
		"this session has not started yet; wait until it runs or stops, then hand it off"}
	errHandoffStillLive = &runErr{http.StatusConflict,
		"the session was asked to stop but its tool is still running; its conversation is not final yet, try again"}
)

const (
	// The largest stored conversation a handoff reads, and the longest record in it.
	// Large tool output is stored by the tool beside the transcript, so a real
	// conversation is far below both.
	handoffFileMax = 256 << 20
	handoffLineMax = 32 << 20
	// One message is kept to this many bytes, so a conversation of many large turns
	// is not held whole in memory to summarize it.
	handoffTurnMax = 256 << 10
	// The attachment stops here and says so. A real conversation is far below it.
	handoffRenderMax = 16 << 20
	// The caller's edited summary is a turn, not a document.
	handoffSummaryMax = 64 << 10

	handoffToolInputMax = 300
	handoffResultMax    = 1000
	handoffFirstMax     = 1500
	handoffRequestMax   = 300
	handoffAnswerMax    = 1500
	handoffLatestKeep   = 3

	// eventHandedOff is the ledger event the original session gains. Its detail names
	// the new session and the profile by opaque reference, like eventMoved.
	eventHandedOff = "handed_off"
)

// handoffTranscriptLine is the line of the first turn that names the attachment.
const handoffTranscriptLine = "Transcript of the earlier session (tool calls and results shortened): "

// handoffPreviewDTO is what GET /runs/{ref}/handoff answers: the text a handoff would
// start from, for the person to review. It starts nothing and writes nothing.
type handoffPreviewDTO struct {
	Summary         string `json:"summary"`
	SourceDriver    string `json:"source_driver"`
	Turns           int    `json:"turns"`
	TranscriptBytes int64  `json:"transcript_bytes"`
}

// handoffRunRequest is the POST /runs/{ref}/handoff body. The target is a profile
// reference, as everywhere; the summary is the reviewed text, generated when absent.
type handoffRunRequest struct {
	ProviderProfileRef string `json:"provider_profile_ref"`
	Summary            string `json:"summary"`
	Name               string `json:"name"`
}

// The states of the first turn of a handoff.
const (
	handoffDelivered        = "delivered"
	handoffNotDelivered     = "not_delivered"
	handoffDeliveryUnknown  = "unknown"
	handoffWaitsForApproval = "waits_for_approval"
)

// handoffResultDTO is the new session, plus what became of its first turn. Only
// "delivered" is a certainty that it arrived. Otherwise the text the engine would
// have sent is returned, so the person can send it: it names the transcript's path,
// which is why it is given only then.
type handoffResultDTO struct {
	runDTO
	SummaryState string `json:"summary_state"`
	FirstTurn    string `json:"first_turn,omitempty"`
}

// handoffTurn is one entry of the stored conversation: user, assistant, tool or
// result. Thinking and the structure of the tool's own records are not forwarded.
type handoffTurn struct {
	role string
	text string
}

type handoffConversation struct {
	// tool is the name of the tool the conversation was stored by, as the summary and
	// the transcript say it.
	tool  string
	turns []handoffTurn
	// bytes is the size of the stored transcript this was read from.
	bytes int64
	// unreadable counts records or blocks that could not be decoded, so the
	// transcript says what it leaves out instead of silently being shorter.
	unreadable int
}

// claudeLine and claudeBlock are the parts of Claude Code's transcript records a
// handoff reads. Unknown record types and fields are ignored, so a newer tool
// version adds nothing that breaks the read.
type claudeLine struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	// IsCompactSummary marks the summary the tool writes in place of a compacted
	// conversation: it is not something the person sent.
	IsCompactSummary bool `json:"isCompactSummary"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// readClaudeConversation reads a stored Claude Code transcript.
func readClaudeConversation(ctx context.Context, r io.Reader, size int64) (handoffConversation, error) {
	conv := handoffConversation{bytes: size}
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return conv, err
		}
		line, err := readBoundedLine(br, handoffLineMax)
		if len(line) > 0 {
			conv.add(line)
		}
		switch {
		case errors.Is(err, io.EOF):
			return conv, nil
		case errors.Is(err, ErrOutputLineTooLong):
			// The reader stops inside the record, so nothing after it can be trusted
			// to start on a record boundary; a partial summary would claim the
			// conversation ended earlier than it did.
			return conv, errHandoffRecordTooLarge
		case err != nil:
			return conv, err
		}
	}
}

func (c *handoffConversation) add(line []byte) {
	var rec claudeLine
	if json.Unmarshal(line, &rec) != nil {
		c.unreadable++
		return
	}
	c.addRecord(rec, false)
}

// addRecord reads one record of the main conversation, or with sidechain one of a
// subagent's, whose transcript marks every record of its own as a side chain.
func (c *handoffConversation) addRecord(rec claudeLine, sidechain bool) {
	if rec.IsSidechain != sidechain || rec.IsMeta || (rec.Type != "user" && rec.Type != "assistant") ||
		len(rec.Message.Content) == 0 {
		return
	}
	var plain string
	if json.Unmarshal(rec.Message.Content, &plain) == nil {
		c.push(rec.Type, plain)
		return
	}
	var raws []json.RawMessage
	if json.Unmarshal(rec.Message.Content, &raws) != nil {
		c.unreadable++
		return
	}
	for _, raw := range raws {
		var b claudeBlock
		if json.Unmarshal(raw, &b) != nil {
			// One block the tool wrote in a shape this reader does not know costs that
			// block, not the record around it.
			c.unreadable++
			continue
		}
		switch b.Type {
		case "text":
			c.push(rec.Type, b.Text)
		case "tool_use":
			c.push("tool", strings.TrimSpace(b.Name+" "+clip(compactJSON(b.Input), handoffToolInputMax)))
		case "tool_result":
			c.push("result", clip(blockText(b.Content), handoffResultMax))
		}
	}
}

func (c *handoffConversation) push(role, text string) {
	if strings.TrimSpace(text) != "" {
		c.turns = append(c.turns, handoffTurn{role: role, text: clipBytes(text, handoffTurnMax)})
	}
}

// blockText is the text of a tool result, which the tool stores as a string or as a
// list of blocks.
func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var raws []json.RawMessage
	if json.Unmarshal(raw, &raws) != nil {
		return ""
	}
	var parts []string
	for _, r := range raws {
		var b claudeBlock
		if json.Unmarshal(r, &b) == nil && b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// clip shortens s to at most n runes, marking the cut.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + " [...]"
}

// clipBytes shortens s to at most n bytes on a rune boundary, marking the cut.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + " [...]"
}

// texts returns the text of every turn with the role.
func (c handoffConversation) texts(role string) []string {
	var out []string
	for _, t := range c.turns {
		if t.role == role {
			out = append(out, t.text)
		}
	}
	return out
}

// summary is the generated text a handoff starts from: the first request, the latest
// requests and the last answer, which together say what the work is and where it
// stopped. It is a starting point for review, and says so.
func (c handoffConversation) summary() string {
	var b strings.Builder
	b.WriteString("This session continues work from an earlier " + c.tool + " session. " +
		"This summary was generated from that session's stored conversation, without a model; " +
		"check it against the transcript before relying on it.\n")
	users, answers := c.texts("user"), c.texts("assistant")
	if len(users) > 0 {
		b.WriteString("\nThe task (the first request):\n" + clip(users[0], handoffFirstMax) + "\n")
	}
	if len(users) > 1 {
		start := max(1, len(users)-handoffLatestKeep)
		b.WriteString("\nThe latest requests:\n")
		for _, u := range users[start:] {
			b.WriteString("- " + clip(u, handoffRequestMax) + "\n")
		}
	}
	if len(answers) > 0 {
		b.WriteString("\nWhere it stopped (the last answer):\n" + clip(answers[len(answers)-1], handoffAnswerMax) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// transcript renders the whole conversation as plain text for the new tool to read.
// Tool calls and results are reduced to a line each; thinking is not carried.
func (c handoffConversation) transcript() []byte {
	var b bytes.Buffer
	b.WriteString("# Transcript of an earlier " + c.tool + " session\n")
	if c.unreadable > 0 {
		fmt.Fprintf(&b, "\n[%d records or blocks could not be read and are not shown]\n", c.unreadable)
	}
	for i, t := range c.turns {
		if b.Len() > handoffRenderMax {
			fmt.Fprintf(&b, "\n[transcript truncated here: %d more entries were not carried]\n", len(c.turns)-i)
			break
		}
		switch t.role {
		case "user":
			b.WriteString("\n## User\n" + t.text + "\n")
		case "assistant":
			b.WriteString("\n## Assistant\n" + t.text + "\n")
		case "tool":
			b.WriteString("\n- tool: " + t.text + "\n")
		case "result":
			b.WriteString("- result: " + t.text + "\n")
		}
	}
	return b.Bytes()
}

// handoffTool is a tool whose stored conversation a handoff reads: its name as the
// summary and the transcript say it, and the reader of where it keeps it.
type handoffTool struct {
	name string
	read func(*Module, context.Context, model.TenantID, model.Record, ProviderHomeSnapshot) (handoffConversation, error)
}

// handoffTools is every tool a handoff reads. Grok keeps its conversation where the
// engine has no stable reader for it, so it is not offered.
var handoffTools = map[string]handoffTool{
	providerDriverClaude:   {"Claude Code", (*Module).readClaudeHandoff},
	providerDriverCodex:    {"Codex", (*Module).readCodexHandoff},
	providerDriverGemini:   {"Gemini CLI", (*Module).readGeminiHandoff},
	providerDriverOpenCode: {"OpenCode", (*Module).readOpenCodeHandoff},
}

var errHandoffTool = &runErr{http.StatusUnprocessableEntity,
	"a handoff reads the stored conversation of Claude Code, Codex, Gemini CLI and OpenCode; this session's tool keeps its conversation another way"}

// checkHandoffSource proves, without reading the conversation, that rec may be handed
// off: it has a proven provider home, its tool is one whose conversation is read, it
// has reported its conversation id, it is stopped or running, and nothing it was given
// could be in what it printed. live says the tool is still running, which a handoff
// stops before it reads.
func (m *Module) checkHandoffSource(ctx context.Context, tenant model.TenantID, rec model.Record) (src ProviderHomeSnapshot, live bool, err error) {
	src = storedProfileSnapshot(rec)
	if src.ProfileID == "" {
		return src, false, conflictErr("legacy session has no proven provider home and cannot be handed off")
	}
	if _, ok := handoffTools[src.Driver]; !ok {
		return src, false, errHandoffTool
	}
	switch state := rec.String(colState); state {
	case stateStopped, stateFailed:
	case stateRunning, stateIdle:
		live = true
	case statePending, stateWaitingApproval:
		return src, false, errHandoffNotStarted
	default:
		return src, false, conflictErr("a " + state + " session has no conversation to hand off")
	}
	// Both are known from the row: a running session is not stopped for a handoff
	// that these would refuse afterwards.
	if sid := rec.String(colClaudeSessionID); sid == "" || !claudeSessionIDPattern.MatchString(sid) {
		return src, live, handoffReadErr(errNoClaudeSessionID)
	}
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return src, live, err
	}
	// What the session printed may hold what its vault secrets, repository
	// credential and package registry tokens were; that cannot be proven away after
	// the fact, and a handoff sends the conversation to another provider.
	return src, live, m.refuseSecretBearing(ctx, tenant, rec, "handed")
}

// readHandoffConversation reads the conversation the session's tool stored, from the
// home the session ran under after that home is proven to be the one its profile
// registered. The tool must not be writing it: a summary of a half-written record
// would claim the conversation ended where it did not.
func (m *Module) readHandoffConversation(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (handoffConversation, error) {
	tool, ok := handoffTools[src.Driver]
	if !ok {
		return handoffConversation{}, errHandoffTool
	}
	conv, err := tool.read(m, ctx, tenant, rec, src)
	conv.tool = tool.name
	return conv, handoffReadErr(err)
}

// handoffReadErr gives what every reader's open or read failed with the answer a
// person can act on; typed answers pass through unchanged.
func handoffReadErr(err error) error {
	switch {
	case errors.Is(err, errNoClaudeSessionID):
		return conflictErr("this session has no stored conversation to hand off yet")
	case errors.Is(err, errClaudeTranscriptMissing):
		return conflictErr("this session's conversation was not found in its account home, so there is nothing to hand off")
	case errors.Is(err, errActivityTooLarge):
		return errHandoffTooLarge
	}
	return err
}

func (m *Module) readClaudeHandoff(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (handoffConversation, error) {
	tr, err := m.openClaudeTranscript(ctx, tenant, rec, src)
	if err != nil {
		return handoffConversation{}, err
	}
	defer tr.Close()
	size := tr.info.Size()
	if size > handoffFileMax {
		return handoffConversation{}, errHandoffTooLarge
	}
	return readClaudeConversation(ctx, io.LimitReader(tr.f, size), size)
}

// previewHandoff generates what a handoff of runRef would start from. A running
// session is read as it is: the handoff itself stops it first.
func (m *Module) previewHandoff(ctx context.Context, tenant model.TenantID, runRef string) (handoffPreviewDTO, error) {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return handoffPreviewDTO{}, err
	}
	src, _, err := m.checkHandoffSource(ctx, tenant, rec)
	if err != nil {
		return handoffPreviewDTO{}, err
	}
	conv, err := m.readHandoffConversation(ctx, tenant, rec, src)
	if err != nil {
		return handoffPreviewDTO{}, err
	}
	return handoffPreviewDTO{
		Summary: conv.summary(), SourceDriver: src.Driver,
		Turns: len(conv.texts("user")), TranscriptBytes: conv.bytes,
	}, nil
}

// stopForHandoff stops a running source the way the stop route does (the same
// refusals for work-bound sessions, the same process stop) and returns the settled
// row. It answers only once the child is reaped: until then it may still be writing
// the conversation.
func (m *Module) stopForHandoff(ctx context.Context, mc api.ModuleContext, runRef string, rec model.Record) (model.Record, error) {
	if err := m.refuseUnfencedActiveWork(ctx, mc.Tenant, rec); err != nil {
		return nil, err
	}
	if _, _, err := m.stopRunLoaded(ctx, mc.Tenant, runRef, mc.Principal.Actor(), mc.Principal.ActorKind(), "stopped to hand it off", rec); err != nil {
		return nil, err
	}
	stopped, err := m.loadRun(ctx, mc.Tenant, runRef)
	if err != nil {
		return nil, err
	}
	state := stopped.String(colState)
	if state != stateStopped && state != stateFailed {
		return nil, errHandoffStillLive
	}
	if err := m.refuseUncollectedProcess(mc.Tenant, runRef, state, "handoff"); err != nil {
		return nil, afterStop(err)
	}
	return stopped, nil
}

// afterStop says, on a refusal that came after the source was stopped, that it was.
func afterStop(err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return &runErr{re.status, re.msg + "; the session was stopped and can be resumed"}
	}
	return err
}

// handoffName is the new session's name: the one the caller gave, or "Handoff from" the
// source's name. The source's name is cut so that the derived name stays within the limit.
func handoffName(given, sourceName, runRef string) string {
	if name := strings.TrimSpace(given); name != "" {
		return name
	}
	const prefix = "Handoff from "
	// A source named before the rule may hold characters it now refuses; they are left
	// out of the derived name, so the handoff is not refused for a name the caller never chose.
	var clean strings.Builder
	for _, r := range sourceName {
		if !invisibleInName(r) {
			clean.WriteRune(r)
		}
	}
	name := strings.TrimSpace(clean.String())
	if name == "" {
		name = runRef
	}
	if room := maxRunNameRunes - utf8.RuneCountInString(prefix); utf8.RuneCountInString(name) > room {
		name = strings.TrimSpace(string([]rune(name)[:room]))
	}
	return prefix + name
}

// handoffRun starts the linked session. The attachment is written before anything
// starts, under a name of its own, so a refusal or a failed launch leaves no session
// behind and removes only what this call wrote. A running source is stopped only after
// everything that can be refused without touching it has been, and the new tool is
// resolved before it is. The first turn is sent only once the new tool is ready for it,
// and what became of it is reported as it is: delivered, not delivered, unknown (it
// crossed and the answer did not come back) or waiting for an approval that has to
// come first.
func (m *Module) handoffRun(ctx context.Context, mc api.ModuleContext, runRef string, req handoffRunRequest) (handoffResultDTO, error) {
	targetRef := strings.TrimSpace(req.ProviderProfileRef)
	if !validProfileRef(targetRef) {
		return handoffResultDTO{}, badRequest("provider_profile_ref names the profile to continue in")
	}
	if len(req.Summary) > handoffSummaryMax {
		return handoffResultDTO{}, badRequest("summary is too long")
	}
	tenant := mc.Tenant
	// Serialized with resume, move, stop and cleanup of the original, so what is read
	// is what stays stopped while the new session is started.
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return handoffResultDTO{}, err
	}
	defer release()
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return handoffResultDTO{}, err
	}
	src, live, err := m.checkHandoffSource(ctx, tenant, rec)
	if err != nil {
		return handoffResultDTO{}, err
	}
	// The target is refused here, before a byte is written or a session is stopped,
	// with the checks a launch under it gets; createRun repeats them. The assignment is asked
	// first, as for a launch, so an unassigned caller learns nothing of the profile's state.
	if target := strings.TrimSpace(targetRef); validProfileRef(target) {
		if err := m.refuseUnassignedTarget(ctx, mc, target); err != nil {
			return handoffResultDTO{}, err
		}
	}
	dst, _, err := m.resolveLaunchProfile(ctx, tenant, targetRef)
	if err != nil {
		return handoffResultDTO{}, err
	}
	// The name is refused here too, before the source is stopped; createRun checks it again.
	name := handoffName(req.Name, rec.String(colRunName), runRef)
	if err := checkRunName(name); err != nil {
		return handoffResultDTO{}, err
	}
	if live {
		// A read that would be refused (no file, two files, too large, a record too
		// large) is refused now, while the session still runs. OpenCode's is a
		// process, read once: after the stop.
		if src.Driver != providerDriverOpenCode {
			if _, err := m.readHandoffConversation(ctx, tenant, rec, src); err != nil {
				return handoffResultDTO{}, err
			}
		}
	} else if err := m.refuseUncollectedProcess(tenant, runRef, rec.String(colState), "handoff"); err != nil {
		return handoffResultDTO{}, err
	}
	// From here the source is stopped, if it was running, and a refusal says so.
	refused := func(err error) (handoffResultDTO, error) {
		if live {
			err = afterStop(err)
		}
		return handoffResultDTO{}, err
	}
	if live {
		if rec, err = m.stopForHandoff(ctx, mc, runRef, rec); err != nil {
			return handoffResultDTO{}, err
		}
	}
	conv, err := m.readHandoffConversation(ctx, tenant, rec, src)
	if err != nil {
		return refused(err)
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = conv.summary()
	}
	attachment, attachmentFile, err := writeHandoffAttachment(dst.ConfigHome, runRef, conv.transcript())
	if err != nil {
		return refused(err)
	}
	credential, _ := auth.QueuedCredentialFrom(mc.Principal)
	params := CreateRunParams{
		queuedCredential: credential,
		Name:             name,
		// The workspace and template are the original's, so the new tool works on
		// the same folder under the same terms. A session that had a worktree of
		// its own continues from that branch in a worktree of its own.
		WorkspaceRef:       rec.String(colWorkspaceRef),
		TemplateID:         rec.String(colTemplateID),
		Isolation:          Isolation(rec.String(colIsolation)),
		ProviderProfileRef: targetRef,
		HandoffOf:          runRef,
		Actor:              mc.Principal.Actor(),
		ActorKind:          mc.Principal.ActorKind(),
		AgentRef:           mc.Principal.AgentIdentity,
		asks:               m.callerQuestions(ctx, mc),
	}
	if branch := rec.String(colRunWorktreeBranch); branch != "" && params.WorkspaceRef != "" {
		params.Worktree, params.WorktreeFrom = true, branch
	}
	dto, err := m.createRun(ctx, tenant, params)
	if err != nil {
		if rerr := removeHandoffAttachment(dst.ConfigHome, attachmentFile); rerr != nil {
			m.warnf("sessions: the attachment of a refused handoff was not removed", "run_ref", runRef, "err", redactErr(rerr))
		}
		return refused(err)
	}
	m.recordHandedOff(ctx, tenant, runRef, dto.RunRef, dst.ProfileID, mc.Principal.Actor(), mc.Principal.ActorKind())

	out := handoffResultDTO{runDTO: dto}
	first := summary + "\n\n" + handoffTranscriptLine + attachment
	switch dto.State {
	case stateRunning:
		out.SummaryState = m.deliverFirstTurn(ctx, tenant, dto.RunRef, first)
	case stateWaitingApproval:
		out.SummaryState = handoffWaitsForApproval
	default:
		out.SummaryState = handoffNotDelivered
	}
	if out.SummaryState != handoffDelivered {
		out.FirstTurn = first
	}
	return out, nil
}

// deliverFirstTurn sends the first turn of a new linked session and reports what is
// known. The runtime says whether the input crossed to the child; an error after it
// did is "unknown", never "not delivered", because sending it again would repeat it.
func (m *Module) deliverFirstTurn(ctx context.Context, tenant model.TenantID, ref, text string) string {
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, ref))
	if err != nil {
		return handoffNotDelivered
	}
	defer release()
	rec, err := m.loadRun(ctx, tenant, ref)
	if err != nil {
		return handoffNotDelivered
	}
	crossed, err := m.sendTextInputLoaded(ctx, tenant, ref, text, nil, rec)
	switch {
	case err == nil:
		return handoffDelivered
	case crossed:
		m.warnf("sessions: the handoff's first turn may have been delivered", "run_ref", ref, "err", redactErr(err))
		return handoffDeliveryUnknown
	}
	m.warnf("sessions: the handoff's first turn was not delivered", "run_ref", ref, "err", redactErr(err))
	return handoffNotDelivered
}

// recordHandedOff seals, on the ORIGINAL session, where it was handed off to. It is
// a record about a session that already exists: its failure is reported and does not
// undo the new session, whose handoff_of is the link of record.
func (m *Module) recordHandedOff(ctx context.Context, tenant model.TenantID, runRef, newRef, profileRef, actor, actorKind string) {
	if _, err := m.transition(ctx, tenant, runRef, transitionInput{
		event:      eventHandedOff,
		detail:     fmt.Sprintf("handed off to %s under profile %s", newRef, profileRef),
		actor:      actor,
		actorKind:  actorKind,
		keepReason: true,
	}); err != nil {
		m.warnf("sessions: the handoff could not be recorded on the original session", "run_ref", runRef, "err", redactErr(err))
	}
}

// writeHandoffAttachment writes the transcript under configHome, under a name of its
// own, and returns its path and file name. Two handoffs of one session to one
// profile never share a file, so removing one's can never remove the other's.
func writeHandoffAttachment(configHome, runRef string, content []byte) (full, file string, err error) {
	root, err := os.OpenRoot(configHome)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	if err := root.MkdirAll(handoffAttachmentDir, 0o700); err != nil {
		return "", "", err
	}
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", "", err
	}
	file = runRef + "-" + hex.EncodeToString(salt[:]) + ".md"
	name := path.Join(handoffAttachmentDir, file)
	tmp := name + claudeStateTmpMark
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	if _, err = f.Write(content); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return "", "", err
	}
	return filepath.Join(configHome, handoffAttachmentDir, file), file, nil
}

// removeHandoffAttachment removes an attachment this handoff wrote, after a launch
// that did not happen.
func removeHandoffAttachment(configHome, file string) error {
	root, err := os.OpenRoot(configHome)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(path.Join(handoffAttachmentDir, file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
