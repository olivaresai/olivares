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
	"fmt"
	"io"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// conversationHistory uses the tool's persisted conversation, not a second
// transcript store. The caller has already authorized and audited the attach.
// It never resumes a thread, sends a turn, or opens a provider credential.
func (m *Module) conversationHistory(ctx context.Context, tenant model.TenantID, rec model.Record) (line string, err error) {
	threadID := rec.String(colClaudeSessionID)
	if rec.String(colRunProfileDriver) != providerDriverCodex || threadID == "" {
		return "", nil
	}
	// False is written only for a new Codex run, and made true durably before
	// input can reach the tool. NULL on older runs is unknown, never empty.
	if attempted, known := rec[colRunCodexInputAttempted].(bool); known && !attempted {
		return "", nil
	}
	args := append([]string{"-c", "features.api_key_model_discovery=false"}, NewCodexDriver().LaunchArgs(DriverLaunch{})...)
	err = m.readStoredConversation(ctx, tenant, rec, args, 15*time.Second,
		func(ctx context.Context, proc Process) (err error) {
			line, err = readCodexConversation(ctx, proc, threadID, maxOutputLine)
			return err
		})
	if err != nil {
		return "", err
	}
	return line, nil
}

// openCodeHistory is an OpenCode session's conversation as the lines of the
// session/update frames OpenCode's own session/load replay sends for it (OpenCode
// 1.19.4), which the console already shows for a live OpenCode session: the person's
// and the tool's text under its message's id, and each tool call with its outcome.
// It is read from OpenCode's own export, as a handoff reads it. Text OpenCode adds as
// context (synthetic or ignored), reasoning and attachments are left out, as a handoff
// leaves them out.
func (m *Module) openCodeHistory(ctx context.Context, tenant model.TenantID, rec model.Record) (lines []string, err error) {
	sid := rec.String(colClaudeSessionID)
	if sid == "" {
		return nil, nil
	}
	if !claudeSessionIDPattern.MatchString(sid) {
		return nil, errHistoryUnreadable
	}
	err = m.readStoredConversation(ctx, tenant, rec, []string{"--pure", "export", sid}, openCodeExportTimeout,
		func(ctx context.Context, proc Process) error {
			export, err := collectStdout(ctx, proc, openCodeExportMax)
			if err != nil {
				return err
			}
			lines, err = openCodeFrames(sid, export)
			return err
		})
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// readStoredConversation runs read on the tool of the run's profile, run once and
// offline to read the conversation it stored (withStoredReader), behind the checks
// every stored conversation passes, and answers in the words the view shows.
func (m *Module) readStoredConversation(ctx context.Context, tenant model.TenantID, rec model.Record,
	args []string, timeout time.Duration, read func(context.Context, Process) error) error {
	if m.earlierLaunchValuesMayBeUnheld(ctx, tenant, rec) {
		// Current vault values cannot redact secrets from earlier generations
		// after rotation, nor expired Git read tokens from earlier launches, nor
		// a package registry token (the stored thread is the tool's own, so none
		// is withheld from it). The already-redacted live ring remains available.
		return errors.New("stored conversation is unavailable because historical secret redaction cannot be proven")
	}
	home, _, err := m.revalidateStoredProfile(ctx, tenant, rec)
	if err != nil {
		return errors.New("stored conversation is unavailable because its provider home is no longer authorized")
	}
	err = m.withStoredReader(ctx, tenant, rec, home, args, timeout, read)
	switch {
	case errors.Is(err, errStoredReaderVersion):
		return errors.New("stored conversation tool version is unavailable; install it or clear its version selection")
	case errors.Is(err, errStoredReaderStart):
		return errors.New("stored conversation reader could not be started")
	case errors.Is(err, errStoredReaderStop):
		return errors.New("stored conversation reader could not be stopped without error")
	case err != nil:
		return errHistoryUnreadable
	}
	if _, _, err = m.revalidateStoredProfile(ctx, tenant, rec); err != nil {
		return errors.New("stored conversation provider home authorization changed during the read")
	}
	return nil
}

var (
	// errHistoryWrite is a history the client stopped reading: the attach ends.
	errHistoryWrite = errors.New("the history could not be written to the client")
	// The transcript on disk is the tool's own, unredacted; the activity's rules decide
	// whether it may be read (session_activity.go), and these say so in the view.
	errHistorySecretBearing = errors.New("this session was given vault secrets, a repository credential or a managed credential; " +
		"its saved conversation cannot be proven free of them, so only its live output is shown")
	errHistoryRegistryToken = errors.New("this session used a package registry token; " +
		"its saved conversation cannot be proven free of one an earlier launch printed, so only its live output is shown")
	errHistoryMissing    = errors.New("the saved conversation was not found in the session's account home; only its live output is shown")
	errHistoryUnreadable = errors.New("the saved conversation could not be read; live output is still available while the session is running")
)

// noticeLine is the engine's own notice as one line of the conversation.
func noticeLine(message string) string {
	b, _ := json.Marshal(map[string]string{"type": "olivares_notice", "message": message})
	return string(b)
}

// claudeWindow is the uuids of the messages among frames. Claude Code prints a message
// with the uuid its transcript stores it under (measured on 2.1.316).
func claudeWindow(frames []seqFrame) map[string]bool {
	ids := map[string]bool{}
	for _, f := range frames {
		var frame struct {
			UUID string `json:"uuid"`
		}
		if bytes.Contains(f.Data, []byte(`"uuid"`)) && json.Unmarshal(f.Data, &frame) == nil && frame.UUID != "" {
			ids[frame.UUID] = true
		}
	}
	return ids
}

// claudeHistory sends through emit, one line each, the messages of a Claude Code
// session's transcript (the tool's store of record) that precede window, the messages
// the live ring will send itself. A turn the ring evicted (defaultRingFrames,
// defaultRingBytes), or that a restarted engine never held, is read from there. The
// transcript is read under the activity's rules (a proven home, no secret it cannot
// withhold) and through the same redactor. read says the transcript was read to the
// window, so the ring's older frames are not offered again. The error is a reason the
// view shows, or errHistoryWrite.
func (m *Module) claudeHistory(ctx context.Context, tenant model.TenantID, ref string, window map[string]bool,
	emit func(line []byte) error) (read bool, err error) {
	rec, tr, redact, live, err := m.activitySource(ctx, tenant, ref, false)
	switch {
	case errors.Is(err, errNoClaudeSessionID):
		return false, nil
	case errors.Is(err, errClaudeTranscriptMissing):
		// A running tool may not have written it yet; a stopped one should have.
		if live && activityRunState(rec.String(colState)) {
			return false, nil
		}
		return false, errHistoryMissing
	case errors.Is(err, errActivitySecretBearing):
		return false, errHistorySecretBearing
	case errors.Is(err, errActivityRegistryToken):
		return false, errHistoryRegistryToken
	case err != nil:
		return false, errHistoryUnreadable
	}
	defer tr.Close()
	withheld := 0
	done := func() (bool, error) {
		if withheld > 0 && emit([]byte(noticeLine(fmt.Sprintf(
			"%d records of the saved conversation are not shown: they could not be proven free of the session's secrets", withheld)))) != nil {
			return false, errHistoryWrite
		}
		return true, nil
	}
	br := bufio.NewReaderSize(io.LimitReader(tr.f, tr.info.Size()), 64<<10)
	for {
		if ctx.Err() != nil {
			return false, errHistoryWrite
		}
		line, tooLong, rerr := readRecord(br, activityLineMax)
		end := errors.Is(rerr, io.EOF)
		if rerr != nil && !end {
			return false, errHistoryUnreadable
		}
		// A last record without its newline is still being written: the ring has it.
		if tooLong || len(line) > 0 && (!end || json.Valid(line)) {
			var out []byte
			var stop bool
			if redacted := redact.apply(line); len(redacted) == 0 && !tooLong {
				withheld++
			} else {
				out, stop = claudeHistoryLine(redacted, tooLong, window)
			}
			if stop {
				return done()
			}
			if out != nil && emit(out) != nil {
				return false, errHistoryWrite
			}
		}
		if end {
			return done()
		}
	}
}

// claudeHistoryLine is one transcript record as the view reads a frame: a message of
// the main conversation with its uuid, its time and the tool's result beside it,
// without the folder, branch, version and account the record also carries, and without
// the bytes of an inline image (the attachment route serves those). Other records give
// nil. A record that cannot be shown whole, within the console's one-line bound
// (maxOutputLine), is a notice in its place. stop is the first message of window.
func claudeHistoryLine(line []byte, tooLong bool, window map[string]bool) (out []byte, stop bool) {
	if tooLong {
		return []byte(noticeLine(fmt.Sprintf("a record of the saved conversation is not shown: it is over the %d bytes the view reads", activityLineMax))), false
	}
	var in struct {
		Type             string          `json:"type"`
		UUID             string          `json:"uuid"`
		Timestamp        string          `json:"timestamp"`
		IsSidechain      bool            `json:"isSidechain"`
		IsMeta           bool            `json:"isMeta"`
		IsCompactSummary bool            `json:"isCompactSummary"`
		Message          json.RawMessage `json:"message"`
		ToolUseResult    json.RawMessage `json:"toolUseResult"`
	}
	if json.Unmarshal(line, &in) != nil {
		return []byte(noticeLine("a record of the saved conversation could not be read")), false
	}
	if in.UUID != "" && window[in.UUID] {
		return nil, true
	}
	if (in.Type != messageRoleUser && in.Type != messageRoleAssistant) || in.IsSidechain || in.IsMeta || in.IsCompactSummary || len(in.Message) == 0 {
		return nil, false
	}
	message, err := dropInlineImages(in.Message)
	if err == nil {
		in.ToolUseResult, err = dropInlineImages(in.ToolUseResult)
	}
	if err != nil {
		return []byte(noticeLine("a record of the saved conversation could not be read")), false
	}
	out, err = json.Marshal(struct {
		Type          string          `json:"type"`
		UUID          string          `json:"uuid,omitempty"`
		Timestamp     string          `json:"timestamp,omitempty"`
		Message       json.RawMessage `json:"message"`
		ToolUseResult json.RawMessage `json:"toolUseResult,omitempty"`
	}{in.Type, in.UUID, in.Timestamp, message, in.ToolUseResult})
	if err != nil || len(out) > maxOutputLine {
		return []byte(noticeLine(fmt.Sprintf("a message of the saved conversation is not shown: it is %d bytes, over the %d the view shows", len(out), maxOutputLine))), false
	}
	return out, false
}

// dropInlineImages removes the payload of every base64 source in raw (Claude's image
// and document blocks), keeping its media type: the view says an image was there.
func dropInlineImages(raw json.RawMessage) (json.RawMessage, error) {
	if !bytes.Contains(raw, []byte(`"base64"`)) {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if t["type"] == "base64" {
				delete(t, "data")
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	return json.Marshal(v)
}

var (
	errStoredReaderVersion = errors.New("the tool's program is unavailable")
	errStoredReaderStart   = errors.New("the tool could not be started")
	errStoredReaderStop    = errors.New("the tool could not be stopped without error")
)

// withStoredReader runs the tool of home's profile once, offline, to read the
// conversation it stored: its own program under the profile's homes, in the
// confinement a session of that profile gets, with the network closed. read gets
// the process; the process is stopped afterwards, and the tool stays admitted until
// the engine has confirmed that the child was reaped. A read that succeeds on a
// child that could not be stopped still fails, with errStoredReaderStop.
func (m *Module) withStoredReader(ctx context.Context, tenant model.TenantID, rec model.Record, home ProviderHomeSnapshot,
	args []string, timeout time.Duration, read func(context.Context, Process) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	m.rt.toolMu.RLock()
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			m.rt.toolMu.RUnlock()
		}
	}()
	program, err := m.resolveRunProgram(ctx, tenant, CreateRunParams{ProviderHome: &home, WorkspaceRef: rec.String(colWorkspaceRef), AgentRef: rec.String(colRunAgentRef)})
	if err != nil {
		return errStoredReaderVersion
	}
	program, _ = toolProgramIdentity(program)
	env := toSessionEnv(cliruntime.HomeEnv(home.Driver, home.UserHome, home.ConfigHome))
	if home.Driver == providerDriverOpenCode {
		// OpenCode keeps its data under XDG directories beside HOME, the mapping its
		// own sessions run under, and its own sessions run with its background
		// network work switched off: so does this read.
		env = append(env, openCodeXDGMapping(home.UserHome)...)
		env = append(env,
			EnvVar{Name: envOpenCodeDisableAutoUpdate, Value: "1"},
			EnvVar{Name: envOpenCodeDisableModelsFetch, Value: "1"},
			EnvVar{Name: envOpenCodeDisableLSPDownload, Value: "1"},
			EnvVar{Name: envNPMConfigOffline, Value: "true"},
			EnvVar{Name: envOpenCodeDisableShare, Value: "1"})
	}
	spec := LaunchSpec{Program: program, Args: args,
		Dir: home.UserHome, Env: env,
		Isolation: IsolationNative, WaitDelay: time.Second,
		// Its answer lists the images a person attached: see LaunchSpec.StripImageData.
		StripImageData: true,
		Confinement:    m.sessionConfinement("", &home, PresetNone), NetworkPolicy: &egress.Policy{Offline: true}}
	proc, err := m.rt.Runner.Launch(ctx, spec)
	if proc != nil {
		defer func() {
			stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			stopErr := proc.Stop(stopCtx)
			if !childWasReaped(stopErr) {
				// Keep custody after a partial launch or uncertain Stop. Removal
				// uses TryLock, so this hold never queues a writer ahead of launches.
				releaseAdmission = false
				go func() {
					_, waitErr := proc.Wait()
					if childWasReaped(waitErr) {
						m.rt.toolMu.RUnlock()
					} else {
						m.warnf("stored conversation reader collection remains unconfirmed")
					}
				}()
			}
			// A read that failed on a child that was reaped keeps its own error: the
			// teardown report then only says output was given up, which the read
			// already knows.
			if stopErr != nil && !(err != nil && childWasReaped(stopErr)) {
				err = errStoredReaderStop
			}
		}()
	}
	if err != nil || proc == nil {
		return errStoredReaderStart
	}
	return read(ctx, proc)
}

// readCodexConversation reads the stored conversation threadID, at most limit bytes
// as Codex sends it and as the console gets it.
func readCodexConversation(ctx context.Context, proc Process, threadID string, limit int) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	conn := newRPCConn(proc.Send, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				conn.close(ctx.Err())
				return
			case frame, ok := <-proc.Output():
				if !ok {
					conn.close(errors.New("conversation output ended"))
					return
				}
				if frame.Stream == streamStdout {
					conn.deliver(frame.Data)
				}
			}
		}
	}()
	defer func() { cancel(); conn.close(errors.New("conversation reader closed")); <-done }()
	if _, err := conn.call(ctx, codexMethodInitialize, codexInitializeParams{ClientInfo: codexClientInfo{Name: "olivares-conversation", Version: "1"}}, 10*time.Second); err != nil {
		return "", err
	}
	if err := conn.notify(ctx, codexMethodInitialized, nil); err != nil {
		return "", err
	}
	raw, err := conn.call(ctx, "thread/read", struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	}{threadID, true}, 10*time.Second)
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			codexThread
			Turns []json.RawMessage `json:"turns"`
		} `json:"thread"`
	}
	if len(raw) > limit || json.Unmarshal(raw, &response) != nil || response.Thread.ID != threadID || response.Thread.ParentThreadID != nil || response.Thread.AgentRole != nil || response.Thread.Turns == nil {
		return "", errors.New("invalid stored root conversation")
	}
	for _, rawTurn := range response.Thread.Turns {
		var turn struct {
			ID        string                         `json:"id"`
			Items     api.JSONArray[json.RawMessage] `json:"items"`
			ItemsView string                         `json:"itemsView"`
		}
		if json.Unmarshal(rawTurn, &turn) != nil || turn.ID == "" || turn.Items == nil || (turn.ItemsView != "" && turn.ItemsView != "full") {
			return "", errors.New("stored conversation is incomplete")
		}
	}
	// Exclude path, authentication, provider configuration and other thread
	// metadata. Only native turn items enter the existing conversation inspector.
	data, err := json.Marshal(struct {
		Type     string            `json:"type"`
		ThreadID string            `json:"thread_id"`
		Turns    []json.RawMessage `json:"turns"`
	}{"olivares_conversation", threadID, response.Thread.Turns})
	if len(data) > limit {
		return "", errors.New("stored conversation exceeded its display response bound")
	}
	return string(data), err
}

// openCodeFrames is the export as the session/update frames of openCodeHistory.
func openCodeFrames(sid string, export []byte) ([]string, error) {
	var conv openCodeExport
	// A session with no messages exports an empty list; an answer without one is not
	// an export of this tool, and an empty conversation must not stand for it.
	if json.Unmarshal(export, &conv) != nil || conv.Messages == nil {
		return nil, errOpenCodeExport
	}
	var frames []string
	for _, msg := range conv.Messages {
		if msg.Info.Role != "user" && msg.Info.Role != "assistant" {
			continue
		}
		for _, p := range msg.Parts {
			var update any
			switch {
			case p.Type == "text" && p.Text != "" && !p.Synthetic && !p.Ignored:
				kind := "agent_message_chunk"
				if msg.Info.Role == "user" {
					kind = "user_message_chunk"
				}
				update = acpChunkUpdate{kind, msg.Info.ID, acpContentBlock{"text", p.Text}}
			case p.Type == "tool" && p.CallID != "":
				update = openCodeToolUpdate(p)
			default:
				continue
			}
			raw, err := json.Marshal(update)
			if err != nil {
				return nil, err
			}
			frame, err := json.Marshal(notificationOut{JSONRPC: "2.0", Method: acpNotifySessionUpdate, Params: acpSessionNotification{sid, raw}})
			if err != nil {
				return nil, err
			}
			frames = append(frames, string(frame))
		}
	}
	return frames, nil
}

type acpChunkUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	MessageID     string          `json:"messageId"`
	Content       acpContentBlock `json:"content"`
}

type acpToolContent struct {
	Type    string          `json:"type"`
	Content acpContentBlock `json:"content"`
}

type acpToolUpdate struct {
	SessionUpdate string           `json:"sessionUpdate"`
	ToolCallID    string           `json:"toolCallId"`
	Title         string           `json:"title"`
	Status        string           `json:"status"`
	RawInput      json.RawMessage  `json:"rawInput,omitempty"`
	Content       []acpToolContent `json:"content,omitempty"`
}

// openCodeToolUpdate is a stored tool call as one tool_call_update: the state the live
// view reaches for it, with its output, or its error when it failed.
func openCodeToolUpdate(p openCodePart) acpToolUpdate {
	u := acpToolUpdate{SessionUpdate: "tool_call_update", ToolCallID: p.CallID, Title: p.State.Title, Status: p.State.Status}
	if u.Title == "" {
		u.Title = p.Tool
	}
	if acpCapabilityPresent(p.State.Input) {
		u.RawInput = p.State.Input
	}
	text := ""
	switch p.State.Status {
	case "running":
		u.Status = "in_progress"
	case "completed":
		text = p.State.Output
	case "error":
		u.Status, text = "failed", p.State.Error
	}
	if text != "" {
		u.Content = []acpToolContent{{"content", acpContentBlock{"text", text}}}
	}
	return u
}
