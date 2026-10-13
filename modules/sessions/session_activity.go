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
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// What the session's tool is doing (#1861): the agents it spawned, its background
// tasks, the files it changed, its own task list and what its context holds. Each
// item is a record Claude Code wrote in the transcript it keeps for the session;
// the engine stores nothing of it and infers nothing beyond it, and reads it again
// whenever it is asked. The state therefore lives on the server, survives a reload
// and is the same for the console, the API and the CLI.

const (
	// A read repeats whenever the session writes, so its bounds are tighter than the
	// handoff's one-off read; a real session is far below all of them. Large tool
	// output is stored by the tool beside the transcript, not in it.
	activityFileMax   = 64 << 20
	activityLineMax   = 8 << 20
	activityAgentsMax = 256
	// One summary is a line on a panel, not the agent's report.
	activitySummaryMax = 300
	// A read of one session by one person is recorded once per window: the console
	// reads again whenever the session writes, and the ledger records who read what,
	// not how often a panel refreshed.
	activityAuditWindow = 15 * time.Minute
)

// The states of an agent or a background task.
const (
	activityRunning   = "running"
	activityCompleted = "completed"
	activityFailed    = "failed"
	activityStopped   = "stopped"
	activityScheduled = "scheduled"
)

// What the activity was read from. A session that has not reported its conversation
// yet, or whose tool has not written it yet, is pending; a transcript that should be
// there and is not is missing, which the panel says instead of showing nothing.
const (
	activityTranscriptRead    = "read"
	activityTranscriptPending = "pending"
	activityTranscriptMissing = "missing"
)

var (
	errActivityOtherTool = &runErr{http.StatusUnprocessableEntity,
		"activity is read from Claude Code's transcript; this session's tool keeps its record another way"}
	errActivityTooLarge = &runErr{http.StatusUnprocessableEntity,
		"the session's transcript is larger than the activity reads"}
	errActivityNoAgent = &runErr{http.StatusNotFound, "this session has no subagent transcript by that id"}
	// The transcript on disk is the tool's own, unredacted. Current vault values cannot
	// redact a value an earlier generation of the session printed, so, as for a handoff
	// and a stored Codex conversation, it is not shown; the attach stream still is.
	errActivitySecretBearing = conflictErr("this session was given vault secrets or a repository credential; " +
		"its transcript cannot be proven free of them, so its activity is not shown (its live output is)")
)

// activityDTO is what GET /runs/{ref}/activity answers.
type activityDTO struct {
	Driver string `json:"driver"`
	// Transcript is read, pending or missing.
	Transcript string `json:"transcript"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	// ContextTokens is what the latest request of the main conversation sent:
	// input plus cached input, as the provider counted it.
	ContextTokens int64                `json:"context_tokens"`
	Compactions   []activityCompaction `json:"compactions"`
	Agents        []activityAgent      `json:"agents"`
	Tasks         []activityTask       `json:"tasks"`
	Files         []activityFile       `json:"files"`
	Todos         []activityTodo       `json:"todos"`
	Skills        []string             `json:"skills"`
	MCPServers    []string             `json:"mcp_servers"`
	// Unreadable counts records the read could not decode, so a panel says what it
	// leaves out instead of silently being shorter.
	Unreadable int    `json:"unreadable,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type activityCompaction struct {
	At         string `json:"at,omitempty"`
	Trigger    string `json:"trigger,omitempty"`
	PreTokens  int64  `json:"pre_tokens,omitempty"`
	PostTokens int64  `json:"post_tokens,omitempty"`
}

type activityAgent struct {
	ToolUseID   string `json:"tool_use_id"`
	AgentID     string `json:"agent_id,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	Background  bool   `json:"background,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	EndedAt     string `json:"ended_at,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	Tokens      int64  `json:"tokens,omitempty"`
	ToolUses    int64  `json:"tool_uses,omitempty"`
	Model       string `json:"model,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// activityTask is work the tool left running beside the conversation: a shell
// command, a monitor, a workflow, or a wakeup it scheduled.
type activityTask struct {
	ToolUseID   string `json:"tool_use_id"`
	TaskID      string `json:"task_id,omitempty"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	State       string `json:"state"`
	StartedAt   string `json:"started_at,omitempty"`
	EndedAt     string `json:"ended_at,omitempty"`
	DueAt       string `json:"due_at,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// activityFile is a file the tool's own edit tools changed, with the lines the
// tool reported adding and removing. A file changed another way (a shell command)
// is in the session's Changes, which read the folder itself.
type activityFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Created bool   `json:"created,omitempty"`
	Edits   int    `json:"edits"`
	LastAt  string `json:"last_at,omitempty"`
}

type activityTodo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"`
}

// activityAgentDTO is GET /runs/{ref}/activity/agents/{agent}: what one subagent
// was asked, what it did and what it answered, in the handoff's plain form.
type activityAgentDTO struct {
	AgentID    string            `json:"agent_id"`
	Turns      []activityTurnDTO `json:"turns"`
	Unreadable int               `json:"unreadable,omitempty"`
}

type activityTurnDTO struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// activitySource is the transcript of a session the activity may read, and the
// redactor its records pass through: the live session's, which also withholds the
// registry tokens no row records. A session given vault secrets or a repository
// credential is refused.
func (m *Module) activitySource(ctx context.Context, tenant model.TenantID, runRef string) (model.Record, activitySession, *secretRedactor, bool, error) {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return rec, activitySession{}, nil, false, err
	}
	src := storedProfileSnapshot(rec)
	if src.ProfileID == "" {
		return rec, activitySession{}, nil, false, conflictErr("legacy session has no proven provider home; its activity cannot be read")
	}
	if src.Driver != providerDriverClaude {
		return rec, activitySession{}, nil, false, errActivityOtherTool
	}
	if refuseSecretBearing(rec, "shown") != nil {
		return rec, activitySession{}, nil, false, errActivitySecretBearing
	}
	lr, live := m.rt.getLive(tenant, runRef)
	var redact *secretRedactor
	if live {
		redact = lr.redact
	}
	tr, err := m.openClaudeTranscript(ctx, tenant, rec, src)
	if err != nil {
		return rec, activitySession{}, redact, live, err
	}
	agents, err := openAgentsDir(tr.home, tr.rel)
	if err != nil {
		tr.Close()
		return rec, activitySession{}, redact, live, err
	}
	return rec, activitySession{claudeTranscript: tr, agents: agents}, redact, live, nil
}

// activitySession is a session's open transcript and its subagents' directory.
type activitySession struct {
	claudeTranscript
	agents *os.Root // nil: the session spawned no subagent
}

func (s activitySession) Close() {
	if s.agents != nil {
		s.agents.Close()
	}
	s.claudeTranscript.Close()
}

// openAgentsDir opens the directory of the session's subagents once, through the
// session's own directory beside the transcript. Both are opened only as real
// directories: a link there would hand this session another one's records. An
// absent one is not refused: a session that spawned no subagent has none.
func openAgentsDir(home *os.Root, transcript string) (*os.Root, error) {
	session, err := openRealDir(home, activitySessionDir(transcript))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer session.Close()
	agents, err := openRealDir(session, "subagents")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return agents, err
}

// openRealDir opens the directory name in root as a root of its own and refuses a
// link. What it opens must be the directory Lstat saw, so a link swapped in between
// is refused too, and every later read goes through the opened directory rather
// than resolving the path again.
func openRealDir(root *os.Root, name string) (*os.Root, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errClaudeStateSpecial
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := dir.Stat(".")
	if err == nil && !os.SameFile(fi, opened) {
		err = errClaudeStateSpecial
	}
	if err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}

// runActivity reads the activity of a session.
func (m *Module) runActivity(ctx context.Context, tenant model.TenantID, runRef string) (activityDTO, error) {
	rec, tr, redact, live, err := m.activitySource(ctx, tenant, runRef)
	fold := newActivityFold(false, redact)
	switch {
	case errors.Is(err, errNoClaudeSessionID):
		fold.dto.Transcript = activityTranscriptPending
		return fold.dto, nil
	case errors.Is(err, errClaudeTranscriptMissing):
		// A running tool may not have written it yet; a stopped one should have.
		fold.dto.Transcript = activityTranscriptMissing
		if activityRunState(rec.String(colState)) && live {
			fold.dto.Transcript = activityTranscriptPending
		}
		return fold.dto, nil
	case err != nil:
		return activityDTO{}, err
	}
	defer tr.Close()
	if err := readActivityFile(ctx, fold, tr.f, tr.info); err != nil {
		return activityDTO{}, err
	}
	// The files its subagents changed are the session's too. One subagent transcript
	// that cannot be read costs its own files, counted, not the whole panel.
	var entries []fs.DirEntry
	if tr.agents != nil {
		if entries, err = fs.ReadDir(tr.agents.FS(), "."); err != nil {
			return activityDTO{}, err
		}
	}
	read := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "agent-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if read++; read > activityAgentsMax || !e.Type().IsRegular() {
			fold.dto.Unreadable++
			continue
		}
		sub := newActivityFold(true, redact)
		if err := readActivityState(ctx, tr.agents, e.Name(), sub.read); err != nil {
			if ctx.Err() != nil {
				return activityDTO{}, ctx.Err()
			}
			fold.dto.Unreadable++
			continue
		}
		fold.mergeFiles(sub.dto.Files)
		fold.dto.Unreadable += sub.dto.Unreadable
	}
	if !live || !activityRunState(rec.String(colState)) {
		fold.close()
	}
	// ponytail: each read folds the whole transcript again (bounded at 64 MiB); keep
	// the fold per run and read only the bytes appended since when sessions that
	// large are common.
	return fold.dto, nil
}

// activityRunState is whether a session in state may still be at work.
func activityRunState(state string) bool {
	switch state {
	case statePending, stateRunning, stateIdle, stateWaitingApproval:
		return true
	}
	return false
}

// runAgentTranscript reads what one subagent of the session was asked and answered.
func (m *Module) runAgentTranscript(ctx context.Context, tenant model.TenantID, runRef, agentID string) (activityAgentDTO, error) {
	// The id becomes a file name: no separator, no dot.
	if !claudeSessionIDPattern.MatchString(agentID) {
		return activityAgentDTO{}, errActivityNoAgent
	}
	_, tr, redact, _, err := m.activitySource(ctx, tenant, runRef)
	switch {
	case errors.Is(err, errNoClaudeSessionID), errors.Is(err, errClaudeTranscriptMissing):
		return activityAgentDTO{}, errActivityNoAgent
	case err != nil:
		return activityAgentDTO{}, err
	}
	defer tr.Close()
	if tr.agents == nil {
		return activityAgentDTO{}, errActivityNoAgent
	}
	var conv handoffConversation
	err = readActivityState(ctx, tr.agents, "agent-"+agentID+".jsonl",
		func(ctx context.Context, r io.Reader) error {
			var err error
			conv, err = readClaudeAgentConversation(ctx, r, redact)
			return err
		})
	if errors.Is(err, fs.ErrNotExist) {
		return activityAgentDTO{}, errActivityNoAgent
	}
	if err != nil {
		return activityAgentDTO{}, err
	}
	out := activityAgentDTO{AgentID: agentID, Turns: make([]activityTurnDTO, 0, len(conv.turns)), Unreadable: conv.unreadable}
	for _, t := range conv.turns {
		out.Turns = append(out.Turns, activityTurnDTO{Role: t.role, Text: t.text})
	}
	return out, nil
}

// readClaudeAgentConversation reads a subagent's transcript in the handoff's form.
func readClaudeAgentConversation(ctx context.Context, r io.Reader, redact *secretRedactor) (handoffConversation, error) {
	var conv handoffConversation
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return conv, err
		}
		line, err := readBoundedLine(br, activityLineMax)
		if line = redact.apply(line); len(line) > 0 {
			var rec claudeLine
			if json.Unmarshal(line, &rec) == nil {
				conv.addRecord(rec, true)
			} else if err == nil {
				// A last record without its newline is still being written.
				conv.unreadable++
			}
		}
		switch {
		case errors.Is(err, io.EOF):
			return conv, nil
		case errors.Is(err, ErrOutputLineTooLong):
			conv.unreadable++
			if err := discardLine(br); errors.Is(err, io.EOF) {
				return conv, nil
			} else if err != nil {
				return conv, err
			}
		case err != nil:
			return conv, err
		}
	}
}

// activitySessionDir is the directory beside a transcript that holds the session's
// other state, its subagents' transcripts among them.
func activitySessionDir(transcript string) string {
	return strings.TrimSuffix(transcript, ".jsonl")
}

// readActivityState reads a file of the session's state under the transcript's
// rules: inside the home, not a link, one name, a regular file, within the bound.
func readActivityState(ctx context.Context, home *os.Root, rel string, read func(context.Context, io.Reader) error) error {
	f, err := openStateFile(home, rel)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !singleLink(info) {
		return errClaudeStateSpecial
	}
	if info.Size() > activityFileMax {
		return errActivityTooLarge
	}
	return read(ctx, io.LimitReader(f, info.Size()))
}

// readActivityFile folds an open transcript, up to the size it had when opened: a
// running session keeps writing, and the next read has the rest.
func readActivityFile(ctx context.Context, fold *activityFold, f *os.File, info fs.FileInfo) error {
	if info.Size() > activityFileMax {
		return errActivityTooLarge
	}
	return fold.read(ctx, io.LimitReader(f, info.Size()))
}

func (m *Module) handleRunActivity(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	dto, err := m.runActivity(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	m.auditActivityRead(r, mc, ref, "sessions.run.activity")
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleRunAgentTranscript(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	dto, err := m.runAgentTranscript(r.Context(), mc.Tenant, ref, chi.URLParam(r, "agent"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	// A subagent's transcript is conversation, recorded like the attach stream.
	m.auditRunRead(r, mc, ref, "sessions.run.activity_agent")
	writeJSON(w, http.StatusOK, dto)
}

// auditActivityRead records a read of the session's activity, which names what its
// conversation did, once per person, session and window. The window starts only
// once the record is written, so a failed write is tried again on the next read.
func (m *Module) auditActivityRead(r *http.Request, mc api.ModuleContext, ref, action string) {
	key := string(mc.Tenant) + "\x00" + ref + "\x00" + mc.Principal.Actor()
	now := time.Now()
	if last, ok := m.rt.activityAudits.Load(key); ok && now.Sub(last.(time.Time)) < activityAuditWindow {
		return
	}
	if err := m.appendRunReadAudit(r, mc, ref, action); err != nil {
		m.warnf("sessions: activity read audit failed", "run_ref", ref, "err", redactErr(err))
		return
	}
	// ponytail: one entry per person and session for the engine's lifetime, a few
	// bytes each; prune expired entries when an engine holds millions.
	m.rt.activityAudits.Store(key, now)
}
