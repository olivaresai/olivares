// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"cmp"
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

// Handing a conversation off to another tool (#1845).
//
// No other tool can resume Claude Code's conversation by id, so the honest way to
// continue it elsewhere is a NEW session, linked to the original, that starts from:
//
//   - a summary the person reviews and may edit before anything starts, generated
//     from the conversation the tool stored and by no model (a second provider
//     would otherwise see the conversation before the person chose it);
//   - the transcript, rendered as plain text (messages in full up to a limit, tool
//     calls and results shortened) and written under the target profile's
//     configuration home, the one place a confined session can always read, and
//     named in the first turn.
//
// The original must be stopped and is only read. Its ledger gains one event naming
// the new session, and the new run row names the original in handoff_of.

// handoffAttachmentDir is the directory, under the target profile's configuration
// home, that holds the transcript a handoff attaches.
const handoffAttachmentDir = "olivares-handoff"

var (
	errHandoffRecordTooLarge = &runErr{http.StatusUnprocessableEntity,
		"the stored conversation holds a record too large to summarize; continue it with a move, or start a new session"}
	errHandoffTooLarge = &runErr{http.StatusUnprocessableEntity,
		"the stored conversation is too large to hand off; continue it with a move, or start a new session"}
	errHandoffRunning = &runErr{http.StatusConflict,
		"stop the session first: a handoff reads its stored conversation, which a running session is still writing"}
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
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
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
	b.WriteString("This session continues work from an earlier Claude Code session. " +
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
	b.WriteString("# Transcript of an earlier Claude Code session\n")
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

// handoffSource is a session's stored conversation, read from the home the session
// ran under after that home is proven to be the one its profile registered. The
// session must be stopped: a running one is still writing the file, and a summary of
// a half-written record would claim the conversation ended where it did not.
func (m *Module) handoffSource(ctx context.Context, tenant model.TenantID, rec model.Record) (handoffConversation, ProviderHomeSnapshot, error) {
	src := storedProfileSnapshot(rec)
	if src.ProfileID == "" {
		return handoffConversation{}, src, conflictErr("legacy session has no proven provider home and cannot be handed off")
	}
	if src.Driver != providerDriverClaude {
		return handoffConversation{}, src, &runErr{http.StatusUnprocessableEntity,
			"a handoff reads Claude Code conversations only; this session's tool keeps its conversation another way"}
	}
	if state := rec.String(colState); state != stateStopped && state != stateFailed {
		return handoffConversation{}, src, errHandoffRunning
	}
	// What the session printed may hold what its vault secrets and repository
	// credential were; that cannot be proven away after the fact, and a handoff sends
	// the conversation to another provider.
	if err := refuseSecretBearing(rec, "handed"); err != nil {
		return handoffConversation{}, src, err
	}
	tr, err := m.openClaudeTranscript(ctx, tenant, rec, src)
	switch {
	case errors.Is(err, errNoClaudeSessionID):
		return handoffConversation{}, src, conflictErr("this session has no stored conversation to hand off yet")
	case errors.Is(err, errClaudeTranscriptMissing):
		return handoffConversation{}, src, conflictErr("this session's conversation was not found in its account home, so there is nothing to hand off")
	case err != nil:
		return handoffConversation{}, src, err
	}
	defer tr.Close()
	size := tr.info.Size()
	if size > handoffFileMax {
		return handoffConversation{}, src, errHandoffTooLarge
	}
	conv, err := readClaudeConversation(ctx, io.LimitReader(tr.f, size), size)
	return conv, src, err
}

// previewHandoff generates what a handoff of runRef would start from.
func (m *Module) previewHandoff(ctx context.Context, tenant model.TenantID, runRef string) (handoffPreviewDTO, error) {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return handoffPreviewDTO{}, err
	}
	conv, src, err := m.handoffSource(ctx, tenant, rec)
	if err != nil {
		return handoffPreviewDTO{}, err
	}
	return handoffPreviewDTO{
		Summary: conv.summary(), SourceDriver: src.Driver,
		Turns: len(conv.texts("user")), TranscriptBytes: conv.bytes,
	}, nil
}

// handoffRun starts the linked session. The attachment is written before anything
// starts, under a name of its own, so a refusal or a failed launch leaves no session
// behind and removes only what this call wrote. The first turn is sent only once the
// new tool is ready for it, and what became of it is reported as it is: delivered,
// not delivered, unknown (it crossed and the answer did not come back) or waiting
// for an approval that has to come first.
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
	conv, _, err := m.handoffSource(ctx, tenant, rec)
	if err != nil {
		return handoffResultDTO{}, err
	}
	// The target is refused here, before a byte is written, with the checks a launch
	// under it gets; createRun repeats them.
	dst, _, err := m.resolveLaunchProfile(ctx, tenant, targetRef)
	if err != nil {
		return handoffResultDTO{}, err
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = conv.summary()
	}
	attachment, attachmentFile, err := writeHandoffAttachment(dst.ConfigHome, runRef, conv.transcript())
	if err != nil {
		return handoffResultDTO{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Handoff from " + cmp.Or(rec.String(colRunName), runRef)
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
		return handoffResultDTO{}, err
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
	crossed, err := m.sendTextInputLoaded(ctx, tenant, ref, text, rec)
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
