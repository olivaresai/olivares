// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The messages of a session (#2129): its index, its search and its time anchor, read
// from the transcript the tool keeps, as its activity is (session_activity.go). The
// engine stores nothing of it, the same read serves the console and the CLI, and a
// person reaches only the sessions they may read. A message has one id, its position
// among the session's messages, which a transcript that only grows never changes.

const (
	messagesPageDefault = 50
	// A page is an index, not the conversation: the first line of each message.
	messagesPageMax     = 200
	messageFirstLineMax = 160 // runes
	// One message read whole by id is bounded as a handoff turn is.
	messageTextMax  = 64 << 10 // bytes
	messageQueryMax = 256      // bytes
)

const (
	messageRoleUser      = "user"
	messageRoleAssistant = "assistant"
)

// errMessagesPending marks a Codex rollout the tool has not written a record to yet.
var errMessagesPending = errors.New("sessions: the rollout has no record yet")

var errMessageNone = &runErr{http.StatusNotFound, "this session has no message with that id"}

// messageDTO is one message of the index. Text and LaterSame are only present when one
// message is read whole by id: LaterSame counts the later messages of the same sender
// with the same first line, which is how a reader that holds only the newest part of a
// conversation tells which of several equal rows is this one.
type messageDTO struct {
	ID        int    `json:"id"`
	Role      string `json:"role"`
	At        string `json:"at,omitempty"`
	FirstLine string `json:"first_line"`
	Text      string `json:"text,omitempty"`
	LaterSame int    `json:"later_same,omitempty"`
}

// messagesDTO is what GET /runs/{ref}/messages answers. Items are always oldest
// first. Total counts every message that matches, not only the page; when one message
// is read by id it is the number of messages of the session, as no filter applies. Next
// is the cursor of the page that continues in the order asked; absent at the end.
type messagesDTO struct {
	Driver     string                    `json:"driver"`
	Transcript string                    `json:"transcript"`
	Total      int                       `json:"total"`
	Items      api.JSONArray[messageDTO] `json:"items"`
	Next       string                    `json:"next,omitempty"`
	// Unreadable counts records the read could not decode, so a list says what it
	// leaves out instead of silently being shorter.
	Unreadable int `json:"unreadable,omitempty"`
}

// scannedMessage is one message as the transcript has it.
type scannedMessage struct {
	id         int
	role, text string
	at         string
}

// messageQuery is the parsed request. Filters apply to the list and the search; id
// reads one message whole and ignores them.
type messageQuery struct {
	all    bool
	needle string // lower-cased
	at     time.Time
	desc   bool
	cursor int // asc: ids after it; desc: ids before it (0 = from the end)
	limit  int
	id     int
}

func parseMessageQuery(v url.Values) (messageQuery, error) {
	q := messageQuery{limit: messagesPageDefault}
	switch v.Get("role") {
	case "", messageRoleUser:
	case "all":
		q.all = true
	default:
		return q, badRequest("role must be user or all")
	}
	switch v.Get("order") {
	case "", "asc":
	case "desc":
		q.desc = true
	default:
		return q, badRequest("order must be asc or desc")
	}
	s := v.Get("q")
	if len(s) > messageQueryMax {
		return q, badRequest("q is longer than " + strconv.Itoa(messageQueryMax) + " bytes")
	}
	q.needle = strings.ToLower(strings.TrimSpace(s))
	if s := v.Get("at"); s != "" {
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, badRequest("at must be a time such as 2026-10-14T10:30:00Z")
		}
		q.at = at
	}
	for _, p := range []struct {
		name string
		dst  *int
		min  int
	}{{"limit", &q.limit, 1}, {"cursor", &q.cursor, 0}, {"id", &q.id, 1}} {
		s := v.Get(p.name)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < p.min {
			return q, badRequest(p.name + " must be a whole number of at least " + strconv.Itoa(p.min))
		}
		*p.dst = n
	}
	q.limit = min(q.limit, messagesPageMax)
	return q, nil
}

// matches is whether a message belongs to the list this query asks for.
func (q messageQuery) matches(m scannedMessage) bool {
	if !q.all && m.role != messageRoleUser {
		return false
	}
	if !q.at.IsZero() {
		// A message with no readable time cannot be at or after one.
		at, err := time.Parse(time.RFC3339, m.at)
		if err != nil || at.Before(q.at) {
			return false
		}
	}
	return q.needle == "" || strings.Contains(strings.ToLower(m.text), q.needle)
}

// messageFirstLine is the first line of a message that has words, clipped for an index.
func messageFirstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if utf8.RuneCountInString(line) > messageFirstLineMax {
				return string([]rune(line)[:messageFirstLineMax]) + "…"
			}
			return line
		}
	}
	return ""
}

// What a tool writes into a conversation in the person's name that the person did not
// write: a notification, a hook's output, a slash command, the context a tool gives its
// model. Such a record is not a message.
var injectedMessagePrefixes = []string{
	"<task-notification", "<system-reminder", "<session_context>", "<local-command-", "<command-name>", "<command-message>",
	"<user-prompt-submit-hook", "<environment_context", "<user_instructions", "<permissions instructions",
	"<turn_aborted", "<subagent_notification", "# AGENTS.md instructions", "[Request interrupted by user",
	"Caveat: The messages below were generated by the user while running local commands",
}

func injectedMessage(text string) bool {
	text = strings.TrimSpace(text)
	for _, p := range injectedMessagePrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// claudeMessage reads the message of one Claude Code record, if it holds one. Records
// of a subagent, of the tool itself, of a compaction, and tool results carry none.
func claudeMessage(rec claudeLine) (role, text string, unreadable int) {
	if rec.IsSidechain || rec.IsMeta || rec.IsCompactSummary || len(rec.Message.Content) == 0 ||
		(rec.Type != messageRoleUser && rec.Type != messageRoleAssistant) {
		return "", "", 0
	}
	// What the tool injects is dropped block by block: a person's words that follow a
	// system reminder in the same record are still the person's message.
	user := rec.Type == messageRoleUser
	var parts []string
	var plain string
	if json.Unmarshal(rec.Message.Content, &plain) == nil {
		parts = append(parts, plain)
	} else {
		var raws []json.RawMessage
		if json.Unmarshal(rec.Message.Content, &raws) != nil {
			return "", "", 1
		}
		for _, raw := range raws {
			var b claudeBlock
			if json.Unmarshal(raw, &b) != nil {
				unreadable++
			} else if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
	}
	kept := parts[:0]
	for _, part := range parts {
		if !(user && injectedMessage(part)) {
			kept = append(kept, part)
		}
	}
	text = strings.TrimSpace(strings.Join(kept, "\n"))
	if text == "" {
		return "", "", unreadable
	}
	return rec.Type, text, unreadable
}

// codexMessage reads the message of one Codex rollout record: a response item of type
// message. What the tool gives its model as context is a user item too; only the
// blocks the person wrote are the message.
func codexMessage(payload json.RawMessage) (role, text string, unreadable int) {
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &item) != nil {
		return "", "", 1
	}
	if item.Type != "message" || (item.Role != messageRoleUser && item.Role != messageRoleAssistant) {
		return "", "", 0
	}
	var parts []string
	for _, b := range item.Content {
		if (b.Type == "input_text" || b.Type == "output_text") && !(item.Role == messageRoleUser && injectedMessage(b.Text)) {
			parts = append(parts, b.Text)
		}
	}
	return item.Role, strings.TrimSpace(strings.Join(parts, "\n")), 0
}

// scanMessages reads a transcript once, in order, and calls yield for each message
// with its id. It holds no message: the caller keeps what it needs. The session's
// secrets are withheld from each record before it is read, as the attach stream does.
// A record the tool is still writing waits for the next read; one past the bound is
// skipped and counted, and the records after it are read.
func scanMessages(ctx context.Context, r io.Reader, driver, sid string, redact *secretRedactor, yield func(scannedMessage)) (unreadable int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	if driver == providerDriverCodex {
		pending, err := codexRolloutRoot(br, sid)
		if pending {
			return 0, errMessagesPending
		}
		if err != nil {
			return 0, err
		}
	}
	id := 0
	record := func(line []byte) {
		if line = redact.apply(line); len(line) == 0 {
			// A redactor that cannot prove a record safe withholds all of it.
			unreadable++
			return
		}
		var role, text, at string
		var bad int
		if driver == providerDriverCodex {
			var rec codexActivityRecord
			if json.Unmarshal(line, &rec) != nil {
				unreadable++
				return
			}
			if rec.Type != "response_item" {
				return
			}
			role, text, bad = codexMessage(rec.Payload)
			at = rec.Timestamp
		} else {
			var rec claudeLine
			if json.Unmarshal(line, &rec) != nil {
				unreadable++
				return
			}
			role, text, bad = claudeMessage(rec)
			at = rec.Timestamp
		}
		unreadable += bad
		if role == "" || text == "" {
			return
		}
		id++
		yield(scannedMessage{id: id, role: role, text: text, at: at})
	}
	for {
		if err := ctx.Err(); err != nil {
			return unreadable, err
		}
		line, tooLong, err := readRecord(br, activityLineMax)
		if tooLong {
			unreadable++
		}
		switch {
		case errors.Is(err, io.EOF):
			// A last record without its newline is still being written.
			if len(line) > 0 && json.Valid(line) {
				record(line)
			}
			return unreadable, nil
		case err != nil:
			return unreadable, err
		case len(line) > 0:
			record(line)
		}
	}
}

// readRecord reads one line of at most limit bytes. A longer line is read to its end
// without keeping it and reported with tooLong, so the next call starts at the next
// record. (readBoundedLine stops inside such a line or just after it, and the caller
// cannot tell which.)
func readRecord(br *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	for {
		fragment, err := br.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(fragment) > limit+2 {
				line, tooLong = nil, true
			} else {
				line = append(line, fragment...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return trimCRLF(line), tooLong, err
	}
}

// runMessages answers one request for the session's messages.
func (m *Module) runMessages(ctx context.Context, tenant model.TenantID, runRef string, q messageQuery) (messagesDTO, error) {
	rec, tr, redact, live, err := m.activitySource(ctx, tenant, runRef, false)
	out := messagesDTO{Driver: providerDriverClaude, Transcript: activityTranscriptRead, Items: []messageDTO{}}
	if rec.String(colRunProfileDriver) == providerDriverCodex {
		out.Driver = providerDriverCodex
	}
	switch {
	case errors.Is(err, errNoClaudeSessionID):
		out.Transcript = activityTranscriptPending
		return out, nil
	case errors.Is(err, errClaudeTranscriptMissing):
		// A running tool may not have written it yet; a stopped one should have.
		out.Transcript = activityTranscriptMissing
		if activityRunState(rec.String(colState)) && live {
			out.Transcript = activityTranscriptPending
		}
		return out, nil
	case err != nil:
		return messagesDTO{}, err
	}
	defer tr.Close()
	if tr.info.Size() > activityFileMax {
		return messagesDTO{}, errActivityTooLarge
	}

	var one *messageDTO
	var more bool
	// ponytail: each read scans the whole transcript (bounded at 64 MiB) to count and
	// to find the page; keep an index per run and read only the bytes appended since
	// when sessions that large are common.
	out.Unreadable, err = scanMessages(ctx, io.LimitReader(tr.f, tr.info.Size()), out.Driver,
		rec.String(colClaudeSessionID), redact, func(msg scannedMessage) {
			if q.id != 0 {
				out.Total++
				switch {
				case msg.id == q.id:
					one = &messageDTO{ID: msg.id, Role: msg.role, At: msg.at, FirstLine: messageFirstLine(msg.text), Text: clipBytes(msg.text, messageTextMax)}
				case one != nil && msg.role == one.Role && messageFirstLine(msg.text) == one.FirstLine:
					one.LaterSame++
				}
				return
			}
			if !q.matches(msg) {
				return
			}
			out.Total++
			item := messageDTO{ID: msg.id, Role: msg.role, At: msg.at, FirstLine: messageFirstLine(msg.text)}
			switch {
			case q.desc && (q.cursor == 0 || msg.id < q.cursor):
				// Keep the newest `limit` below the cursor; what falls off is "more".
				if out.Items = append(out.Items, item); len(out.Items) > q.limit {
					out.Items, more = out.Items[1:], true
				}
			case !q.desc && msg.id > q.cursor:
				if len(out.Items) < q.limit {
					out.Items = append(out.Items, item)
				} else {
					more = true
				}
			}
		})
	switch {
	case errors.Is(err, errMessagesPending):
		// The activity's rule: a tool that is not running will not write it now.
		out.Transcript, out.Items = activityTranscriptPending, []messageDTO{}
		if !live || !activityRunState(rec.String(colState)) {
			out.Transcript = activityTranscriptMissing
		}
		return out, nil
	case err != nil:
		return messagesDTO{}, err
	}
	if q.id != 0 {
		if one == nil {
			return messagesDTO{}, errMessageNone
		}
		out.Items = []messageDTO{*one}
		return out, nil
	}
	if more {
		next := out.Items[len(out.Items)-1].ID
		if q.desc {
			next = out.Items[0].ID
		}
		out.Next = strconv.Itoa(next)
	}
	return out, nil
}

func (m *Module) handleRunMessages(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	q, err := parseMessageQuery(r.URL.Query())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	dto, err := m.runMessages(r.Context(), mc.Tenant, ref, q)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	// A message is conversation, so a read of it is recorded like the activity's.
	m.auditActivityRead(r, mc, ref, "sessions.run.messages")
	writeJSON(w, http.StatusOK, dto)
}
