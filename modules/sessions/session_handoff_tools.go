// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The stored conversations a handoff reads besides Claude Code's (session_handoff.go).
// Each is read from where its tool keeps it, in the form its tool writes; none is
// launched except OpenCode, whose only stable form is its own export.

// ---------------------------------------------------------------------------
// Codex: an internal design note (not shipped)<timestamp>-<thread>.jsonl, found as the
// activity of a session is (session_activity_codex.go).

func (m *Module) readCodexHandoff(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (handoffConversation, error) {
	tr, err := m.openCodexActivityTranscript(ctx, tenant, rec, src)
	if err != nil {
		return handoffConversation{}, err
	}
	defer tr.Close()
	return readCodexConversationFile(ctx, io.LimitReader(tr.f, tr.info.Size()), rec.String(colClaudeSessionID), tr.info.Size())
}

// readCodexConversationFile reads a rollout: the messages of the person and of the
// tool, and a line for each tool call and result. What Codex gives its model as
// context is not a message of the person (injectedMessage). Reasoning and the
// transient events are not carried.
func readCodexConversationFile(ctx context.Context, r io.Reader, sid string, size int64) (handoffConversation, error) {
	conv := handoffConversation{bytes: size}
	br := bufio.NewReaderSize(r, 64<<10)
	pending, err := codexRolloutRoot(br, sid)
	if pending {
		return conv, errNoClaudeSessionID
	}
	if err != nil {
		return conv, err
	}
	err = scanJSONLines(ctx, br, &conv, conv.addCodex)
	return conv, err
}

// scanJSONLines calls add for every complete record of a JSON-lines transcript. A
// last record that is not valid (a tool still writing it, or killed while it did) is
// counted as unreadable, so the transcript says it leaves it out; one past the line
// bound refuses the read, because a conversation with a record missing would claim to
// be whole.
func scanJSONLines(ctx context.Context, br *bufio.Reader, conv *handoffConversation, add func([]byte)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, tooLong, err := readRecord(br, handoffLineMax)
		if tooLong {
			return errHandoffRecordTooLarge
		}
		switch {
		case errors.Is(err, io.EOF):
			switch {
			case len(line) == 0:
			case json.Valid(line):
				add(line)
			default:
				conv.unreadable++
			}
			return nil
		case err != nil:
			return err
		case len(line) > 0:
			if !json.Valid(line) {
				conv.unreadable++
				continue
			}
			add(line)
		}
	}
}

func (c *handoffConversation) addCodex(line []byte) {
	var rec codexActivityRecord
	if json.Unmarshal(line, &rec) != nil {
		c.unreadable++
		return
	}
	if rec.Type != "response_item" {
		return
	}
	var item struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Output    json.RawMessage `json:"output"`
	}
	if json.Unmarshal(rec.Payload, &item) != nil {
		c.unreadable++
		return
	}
	switch item.Type {
	case "message":
		role, text, bad := codexMessage(rec.Payload)
		c.unreadable += bad
		c.push(role, text)
	case "function_call":
		c.push("tool", strings.TrimSpace(item.Name+" "+clip(compactJSON(json.RawMessage(item.Arguments)), handoffToolInputMax)))
	case "custom_tool_call":
		c.push("tool", strings.TrimSpace(item.Name+" "+clip(item.Input, handoffToolInputMax)))
	case "function_call_output", "custom_tool_call_output":
		c.push("result", clip(blockText(item.Output), handoffResultMax))
	}
}

// ---------------------------------------------------------------------------
// Gemini CLI: <config home>/tmp/<project>/chats/session-<time>-<first 8 of id>.jsonl.
//
// The file is a log, not a list: its first record is the header, a record with an id
// that was written before replaces the earlier one (a tool call gains its result), a
// {"$set": ...} record changes the header's fields, and {"$rewindTo": id} drops that
// message and every message after it, as the tool does when a turn is undone or fails.

const geminiChatsDir = "chats"

func (m *Module) readGeminiHandoff(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (handoffConversation, error) {
	sid := rec.String(colClaudeSessionID)
	if sid == "" || !claudeSessionIDPattern.MatchString(sid) {
		return handoffConversation{}, errNoClaudeSessionID
	}
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return handoffConversation{}, err
	}
	home, err := os.OpenRoot(src.ConfigHome)
	if err != nil {
		return handoffConversation{}, err
	}
	defer home.Close()
	tr, err := findGeminiChat(ctx, home, sid)
	if err != nil {
		return handoffConversation{}, err
	}
	defer tr.Close()
	return readGeminiConversation(ctx, io.LimitReader(tr.f, tr.info.Size()), tr.info.Size())
}

// findGeminiChat opens the one chat file of sid under home/tmp/*/chats. Every
// directory is held open, so swapping a name cannot redirect the read; a file counts
// only when its own header names the id, and two files that do are refused rather than
// guessed between.
func findGeminiChat(ctx context.Context, home *os.Root, sid string) (claudeTranscript, error) {
	tmp, err := openRealDir(home, "tmp")
	if errors.Is(err, fs.ErrNotExist) {
		return claudeTranscript{}, errClaudeTranscriptMissing
	}
	if err != nil {
		return claudeTranscript{}, err
	}
	defer tmp.Close()
	projects, err := readDirBounded(ctx, tmp, claudeStateMaxFiles)
	if err != nil {
		return claudeTranscript{}, err
	}
	short := sid[:min(len(sid), 8)]
	var found claudeTranscript
	fail := func(err error) (claudeTranscript, error) {
		if found.f != nil {
			found.Close()
		}
		return claudeTranscript{}, err
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		proj, err := openRealDir(tmp, project.Name())
		if err != nil {
			return fail(err)
		}
		chats, err := openRealDir(proj, geminiChatsDir)
		proj.Close()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		names, err := readDirBounded(ctx, chats, claudeStateMaxFiles)
		if err != nil {
			chats.Close()
			return fail(err)
		}
		kept := false
		for _, e := range names {
			name := e.Name()
			if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, "-"+short+".jsonl") {
				continue
			}
			f, info, err := openGeminiChatFile(chats, name, sid)
			if err != nil {
				chats.Close()
				return fail(err)
			}
			if f == nil {
				continue // another conversation whose id starts alike
			}
			if found.f != nil {
				f.Close()
				if found.home != chats {
					chats.Close()
				}
				return fail(conflictErr("this session has more than one stored conversation file; the current one cannot be chosen safely"))
			}
			found, kept = claudeTranscript{home: chats, rel: name, f: f, info: info}, true
		}
		if !kept {
			chats.Close()
		}
	}
	if found.f == nil {
		return claudeTranscript{}, errClaudeTranscriptMissing
	}
	return found, nil
}

// readDirBounded lists dir, at most limit entries.
func readDirBounded(ctx context.Context, dir *os.Root, limit int) ([]fs.DirEntry, error) {
	d, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	var out []fs.DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := d.ReadDir(256)
		out = append(out, entries...)
		if len(out) > limit {
			return nil, errActivityTooLarge
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// openGeminiChatFile opens name for reading when it is a regular file of one name
// whose header record names sid. It returns a nil file for a chat of another id.
func openGeminiChatFile(dir *os.Root, name, sid string) (*os.File, fs.FileInfo, error) {
	f, err := openStateFile(dir, name)
	if err != nil {
		return nil, nil, errClaudeStateSpecial
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || !singleLink(info)) {
		err = errClaudeStateSpecial
	}
	var header struct {
		SessionID string `json:"sessionId"`
	}
	if err == nil {
		line, _ := readBoundedLine(bufio.NewReaderSize(io.LimitReader(f, 1<<16), 1<<16), 1<<16)
		_ = json.Unmarshal(line, &header)
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if header.SessionID != sid {
		f.Close()
		return nil, nil, nil
	}
	if info.Size() > handoffFileMax {
		f.Close()
		return nil, nil, errHandoffTooLarge
	}
	return f, info, nil
}

// geminiRecord is the part of a chat record a handoff reads. Unknown records and
// fields are ignored, so a newer tool version adds nothing that breaks the read.
type geminiRecord struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		Name   string          `json:"name"`
		Args   json.RawMessage `json:"args"`
		Result json.RawMessage `json:"result"`
	} `json:"toolCalls"`
	RewindTo *string `json:"$rewindTo"`
}

// geminiMessage is what one message id is worth in the conversation.
type geminiMessage struct {
	id    string
	turns []handoffTurn
	// bad counts what the message holds that could not be read.
	bad int
}

func readGeminiConversation(ctx context.Context, r io.Reader, size int64) (handoffConversation, error) {
	conv := handoffConversation{bytes: size}
	var messages []geminiMessage
	at := map[string]int{} // message id -> its place in messages
	err := scanJSONLines(ctx, bufio.NewReaderSize(r, 64<<10), &conv, func(line []byte) {
		var rec geminiRecord
		if json.Unmarshal(line, &rec) != nil {
			conv.unreadable++
			return
		}
		switch {
		case rec.RewindTo != nil:
			// The message and every message after it are dropped. An id the log does
			// not hold drops nothing, as in the tool.
			if i, ok := at[*rec.RewindTo]; ok {
				for _, dropped := range messages[i:] {
					delete(at, dropped.id)
				}
				messages = messages[:i]
			}
		case rec.ID != "": // the header and the {"$set"} records carry none
			msg := geminiMessage{id: rec.ID}
			msg.turns, msg.bad = geminiTurns(rec)
			if i, ok := at[rec.ID]; ok {
				messages[i] = msg
				return
			}
			at[rec.ID] = len(messages)
			messages = append(messages, msg)
		}
	})
	for _, msg := range messages {
		conv.turns = append(conv.turns, msg.turns...)
		conv.unreadable += msg.bad
	}
	return conv, err
}

// geminiTurns reads one message: what the person wrote, what the tool answered, and a
// line for each tool call and its result. What the tool gives its model as context is
// not a message of the person, and its own function-response records repeat the
// results the call already holds. bad counts what could not be read.
func geminiTurns(rec geminiRecord) (turns []handoffTurn, bad int) {
	var out handoffConversation
	switch rec.Type {
	case "user":
		var plain string
		if json.Unmarshal(rec.Content, &plain) == nil {
			if !injectedMessage(plain) {
				out.push("user", plain)
			}
			break
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(rec.Content, &parts) != nil {
			return nil, 1
		}
		for _, p := range parts {
			if !injectedMessage(p.Text) {
				out.push("user", p.Text)
			}
		}
	case "gemini":
		var text string
		if len(rec.Content) > 0 && json.Unmarshal(rec.Content, &text) != nil {
			bad++
		}
		out.push("assistant", text)
		for _, call := range rec.ToolCalls {
			out.push("tool", strings.TrimSpace(call.Name+" "+clip(compactJSON(call.Args), handoffToolInputMax)))
			out.push("result", clip(geminiResultText(call.Result), handoffResultMax))
		}
	}
	return out.turns, bad
}

// geminiResultText is the text of a tool call's result: the output the tool returned,
// or its error.
func geminiResultText(raw json.RawMessage) string {
	var results []struct {
		FunctionResponse struct {
			Response struct {
				Output string `json:"output"`
				Error  string `json:"error"`
			} `json:"response"`
		} `json:"functionResponse"`
	}
	if json.Unmarshal(raw, &results) != nil {
		return ""
	}
	var parts []string
	for _, r := range results {
		parts = append(parts, r.FunctionResponse.Response.Output, r.FunctionResponse.Response.Error)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// ---------------------------------------------------------------------------
// OpenCode keeps its conversations in a database whose layout changes between its
// versions and is named by its channel, so the conversation is read the one stable
// way the tool offers: `opencode --pure export <session>`, run offline under the
// session's own profile, for this read only (withStoredReader). --pure is not
// optional: without it the export loads the plugins the profile's configuration names
// and runs their code (measured with OpenCode 1.19.2), outside any governed session.

const (
	openCodeExportTimeout = 30 * time.Second
	// The export is indented JSON, one short line per field. A real conversation is far
	// below the bound the activity of a session is read with.
	openCodeExportMax = activityFileMax
)

var (
	errOpenCodeExport = &runErr{http.StatusBadGateway,
		"OpenCode did not export this session's conversation; it may have been deleted from its account home"}
	errOpenCodeTimeout = &runErr{http.StatusGatewayTimeout,
		"OpenCode did not export this session's conversation in time; try again"}
	errOpenCodeUnavailable = &runErr{http.StatusBadGateway,
		"OpenCode could not be started to read this session's conversation; install it or clear its version selection"}
)

func (m *Module) readOpenCodeHandoff(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (handoffConversation, error) {
	sid := rec.String(colClaudeSessionID)
	if sid == "" || !claudeSessionIDPattern.MatchString(sid) {
		return handoffConversation{}, errNoClaudeSessionID
	}
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return handoffConversation{}, err
	}
	var export []byte
	err := m.withStoredReader(ctx, tenant, rec, src, []string{"--pure", "export", sid}, openCodeExportTimeout,
		func(ctx context.Context, proc Process) (err error) {
			export, err = collectStdout(ctx, proc, openCodeExportMax)
			return err
		})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return handoffConversation{}, errOpenCodeTimeout
	case errors.Is(err, errHandoffRecordTooLarge), errors.Is(err, errHandoffTooLarge), errors.Is(err, errOpenCodeExport):
		return handoffConversation{}, err
	case errors.Is(err, errStoredReaderVersion), errors.Is(err, errStoredReaderStart), errors.Is(err, errStoredReaderStop):
		return handoffConversation{}, errOpenCodeUnavailable
	case err != nil:
		return handoffConversation{}, err
	}
	// The home is still the one the profile registered after the tool ran in it.
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return handoffConversation{}, err
	}
	return readOpenCodeExport(export)
}

// collectStdout reads the process's standard output to its end, at most limit bytes,
// and answers with the process's own verdict: a non-zero exit is a refusal, not a
// conversation. A line past the pump's bound ends the child and is a record too large.
func collectStdout(ctx context.Context, proc Process, limit int) ([]byte, error) {
	var out bytes.Buffer
	for out.Len() <= limit {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case frame, ok := <-proc.Output():
			if !ok {
				return out.Bytes(), waitExit(ctx, proc)
			}
			if frame.Stream == streamStdout {
				out.Write(frame.Data)
				out.WriteByte('\n')
			}
		}
	}
	return nil, errHandoffTooLarge
}

// waitExit waits for the process to exit, no longer than ctx allows, and says whether
// it exited cleanly.
func waitExit(ctx context.Context, proc Process) error {
	var code int
	var err error
	done := make(chan struct{})
	go func() { code, err = proc.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err() // the stop that follows ends the wait
	case <-done:
	}
	switch {
	case errors.Is(err, ErrOutputLineTooLong):
		return errHandoffRecordTooLarge
	case err != nil || code != 0:
		return errOpenCodeExport
	}
	return nil
}

// openCodeExport is the part of `opencode export` a handoff reads.
type openCodeExport struct {
	Messages []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Synthetic bool   `json:"synthetic"`
			Ignored   bool   `json:"ignored"`
			Tool      string `json:"tool"`
			State     struct {
				Status string          `json:"status"`
				Input  json.RawMessage `json:"input"`
				Output string          `json:"output"`
				Error  string          `json:"error"`
			} `json:"state"`
		} `json:"parts"`
	} `json:"messages"`
}

func readOpenCodeExport(data []byte) (handoffConversation, error) {
	conv := handoffConversation{bytes: int64(len(data))}
	var export openCodeExport
	// A session with no messages exports an empty list; an answer without one is not an
	// export of this tool, and an empty conversation must not stand for it.
	if json.Unmarshal(data, &export) != nil || export.Messages == nil {
		return conv, errOpenCodeExport
	}
	for _, msg := range export.Messages {
		for _, p := range msg.Parts {
			switch {
			case p.Type == "text" && !p.Synthetic && !p.Ignored && (msg.Info.Role == "user" || msg.Info.Role == "assistant"):
				conv.push(msg.Info.Role, p.Text)
			case p.Type == "tool":
				conv.push("tool", strings.TrimSpace(p.Tool+" "+clip(compactJSON(p.State.Input), handoffToolInputMax)))
				conv.push("result", clip(p.State.Output+p.State.Error, handoffResultMax))
			}
		}
	}
	return conv, nil
}
