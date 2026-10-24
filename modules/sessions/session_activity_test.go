// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// #1861: the session's side panel shows what its tool is doing, read from the
// transcript Claude Code keeps. The fixture is the transcript of a real Claude Code
// 2.1.296 session (claude -p, Haiku) that spawned two subagents, ran a background
// shell command and changed three files. Only the records the activity reads are
// kept, paths are rewritten and the host's hook messages are neutralized.
const activityFixture = "testdata/claude-2.1.296-activity.jsonl"

// Records the fixture session could not produce: Claude Code gives TodoWrite only to
// its legacy models (a Claude 5 session keeps its list with TaskCreate and TaskUpdate,
// TestActivity_TheTaskToolsAreTheTaskList), and it neither compacted nor called a
// skill or an MCP tool. Their shapes are the ones Claude Code writes.
const activityExtra = `{"type":"assistant","isSidechain":false,"timestamp":"2026-10-12T22:12:02.000Z","message":{"model":"claude-haiku-5-5","content":[{"type":"tool_use","id":"toolu_todo1","name":"TodoWrite","input":{"todos":[{"content":"Edit the files","status":"completed","activeForm":"Editing the files"},{"content":"Wait for the job","status":"in_progress","activeForm":"Waiting for the job"}]}},{"type":"tool_use","id":"toolu_skill1","name":"Skill","input":{"skill":"tdd-workflow"}},{"type":"tool_use","id":"toolu_mcp1","name":"mcp__github__get_issue","input":{"number":1}}]}}
{"type":"user","isSidechain":false,"timestamp":"2026-10-12T22:12:02.100Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_todo1","content":"Todos have been modified successfully."}]},"toolUseResult":{"oldTodos":[],"newTodos":[]}}
{"type":"system","subtype":"compact_boundary","isSidechain":false,"timestamp":"2026-10-12T22:12:03.000Z","compactMetadata":{"trigger":"auto","preTokens":180000,"postTokens":21000}}
`

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(activityFixture)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixtureLines splits the fixture at its background-task notification.
func fixtureLines(t *testing.T) (beforeNotification []string, notification string) {
	t.Helper()
	for _, l := range strings.Split(strings.TrimSpace(string(readFixture(t))), "\n") {
		if strings.Contains(l, `"queued_command"`) {
			return beforeNotification, l
		}
		beforeNotification = append(beforeNotification, l)
	}
	t.Fatal("fixture lacks the notification")
	return nil, ""
}

func foldActivity(t *testing.T, transcript string, redact *secretRedactor) *activityFold {
	t.Helper()
	f := newActivityFold(false, redact)
	if err := f.read(context.Background(), strings.NewReader(transcript)); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestActivity_FoldsTheRealTranscript(t *testing.T) {
	got := foldActivity(t, string(readFixture(t))+activityExtra, nil).dto

	if got.Model != "claude-haiku-5-5" || got.Unreadable != 0 {
		t.Fatalf("model %q unreadable %d", got.Model, got.Unreadable)
	}
	if len(got.Agents) != 2 {
		t.Fatalf("agents = %+v", got.Agents)
	}
	for i, id := range []string{"a442c29bf1c636f28", "a27615e7428d6c5f6"} {
		a := got.Agents[i]
		if a.AgentID != id || a.Type != "general-purpose" || a.State != activityCompleted ||
			a.DurationMs <= 0 || a.Tokens <= 0 || a.ToolUses != 1 || a.Model != "claude-haiku-5-5" ||
			a.Summary != "done" || a.StartedAt == "" || a.EndedAt == "" || a.Description == "" {
			t.Fatalf("agent %d = %+v", i, a)
		}
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("tasks = %+v", got.Tasks)
	}
	task := got.Tasks[0]
	if task.Kind != "shell" || task.TaskID != "blu934u8g" || task.State != activityCompleted ||
		task.Command != "sleep 5; echo finished" || !strings.Contains(task.Summary, "exit code 0") {
		t.Fatalf("task = %+v", task)
	}
	// The refused first attempts changed nothing and are not counted.
	want := map[string]activityFile{
		"/home/dev/demo/one.txt":   {Added: 1, Removed: 1, Edits: 1},
		"/home/dev/demo/two.txt":   {Added: 1, Removed: 1, Edits: 1},
		"/home/dev/demo/three.txt": {Added: 1, Created: true, Edits: 1},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %+v", got.Files)
	}
	for _, file := range got.Files {
		w, ok := want[file.Path]
		if !ok || file.Added != w.Added || file.Removed != w.Removed || file.Created != w.Created || file.Edits != w.Edits {
			t.Fatalf("file %+v, want %+v", file, w)
		}
	}
	if len(got.Todos) != 2 || got.Todos[1] != (activityTodo{Content: "Wait for the job", Status: "in_progress", ActiveForm: "Waiting for the job"}) {
		t.Fatalf("todos = %+v", got.Todos)
	}
	if strings.Join(got.Skills, ",") != "tdd-workflow" || strings.Join(got.MCPServers, ",") != "github" {
		t.Fatalf("skills %v mcp %v", got.Skills, got.MCPServers)
	}
	if len(got.Compactions) != 1 || got.Compactions[0].PreTokens != 180000 || got.ContextTokens != 21000 {
		t.Fatalf("compactions %+v context %d", got.Compactions, got.ContextTokens)
	}
	// The API contract: every list is an array, never null.
	raw, _ := json.Marshal(foldActivity(t, "", nil).dto)
	for _, key := range []string{`"agents":[]`, `"tasks":[]`, `"files":[]`, `"todos":[]`, `"skills":[]`, `"mcp_servers":[]`, `"compactions":[]`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("empty activity lacks %s: %s", key, raw)
		}
	}
}

// An agent or a task is running from its call until its result or notification,
// and a session that no longer runs leaves nothing running.
func TestActivity_ItemsAreRunningUntilTheyEnd(t *testing.T) {
	head, _ := fixtureLines(t)
	before := foldActivity(t, strings.Join(head, "\n")+"\n", nil)
	if len(before.dto.Tasks) != 1 || before.dto.Tasks[0].State != activityRunning || before.dto.Tasks[0].EndedAt != "" {
		t.Fatalf("task before its notification = %+v", before.dto.Tasks)
	}
	agentCall := -1
	for i, l := range head {
		if strings.Contains(l, `"name":"Agent"`) {
			agentCall = i
			break
		}
	}
	early := foldActivity(t, strings.Join(head[:agentCall+1], "\n")+"\n", nil).dto
	if len(early.Agents) != 1 || early.Agents[0].State != activityRunning || early.Agents[0].AgentID != "" {
		t.Fatalf("agent before its result = %+v", early.Agents)
	}
	before.close()
	if before.dto.Tasks[0].State != activityStopped {
		t.Fatalf("task of a stopped session = %+v", before.dto.Tasks[0])
	}
}

// Background agents end by notification; a stop ends a task; a failed call fails.
func TestActivity_BackgroundAgentsStopsAndFailures(t *testing.T) {
	got := foldActivity(t, `{"type":"assistant","timestamp":"t1","message":{"content":[{"type":"tool_use","id":"u1","name":"Agent","input":{"description":"Scan","subagent_type":"Explore","run_in_background":true}},{"type":"tool_use","id":"u2","name":"Monitor","input":{"description":"Watch CI","command":"gh run watch"}},{"type":"tool_use","id":"u3","name":"Agent","input":{"description":"Broken"}}]}}
{"type":"user","timestamp":"t2","message":{"content":[{"type":"tool_result","tool_use_id":"u1","content":"launched"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"ag1"}}
{"type":"user","timestamp":"t2","message":{"content":[{"type":"tool_result","tool_use_id":"u2","content":"started"}]},"toolUseResult":{"taskId":"mon1","timeoutMs":1000,"persistent":false}}
{"type":"user","timestamp":"t2","message":{"content":[{"type":"tool_result","tool_use_id":"u3","is_error":true,"content":"agent type not found"}]},"toolUseResult":"Error: agent type not found"}
{"type":"assistant","timestamp":"t3","message":{"content":[{"type":"tool_use","id":"u4","name":"TaskStop","input":{"task_id":"mon1"}}]}}
{"type":"user","timestamp":"t4","message":{"content":[{"type":"tool_result","tool_use_id":"u4","content":"stopped"}]}}
{"type":"user","timestamp":"t5","message":{"content":"<task-notification>\n<task-id>ag1</task-id>\n<status>completed</status>\n<summary>Agent \"Scan\" completed</summary>\n</task-notification>"}}
`, nil).dto
	if len(got.Agents) != 2 || len(got.Tasks) != 1 {
		t.Fatalf("activity = %+v", got)
	}
	if a := got.Agents[0]; a.AgentID != "ag1" || !a.Background || a.State != activityCompleted || a.EndedAt != "t5" || a.Summary != `Agent "Scan" completed` {
		t.Fatalf("background agent = %+v", a)
	}
	if a := got.Agents[1]; a.State != activityFailed || a.Summary != "agent type not found" {
		t.Fatalf("failed agent = %+v", a)
	}
	if task := got.Tasks[0]; task.Kind != "monitor" || task.TaskID != "mon1" || task.State != activityStopped || task.EndedAt != "t4" {
		t.Fatalf("stopped monitor = %+v", task)
	}
}

// A record the tool is still writing waits for the next read; a record past the
// bound is skipped and counted, and what follows it is still read.
func TestActivity_PartialAndOversizedRecords(t *testing.T) {
	partial := foldActivity(t, string(readFixture(t))+`{"type":"assistant","message":{"model":"claude-opus`, nil).dto
	if partial.Unreadable != 0 || partial.Model != "claude-haiku-5-5" {
		t.Fatalf("a record being written was counted or read: %+v", partial)
	}
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", activityLineMax+10) + `"}}` + "\n"
	got := foldActivity(t, huge+string(readFixture(t)), nil).dto
	if got.Unreadable != 1 || len(got.Agents) != 2 || len(got.Files) != 3 {
		t.Fatalf("after an oversized record: unreadable %d agents %d files %d", got.Unreadable, len(got.Agents), len(got.Files))
	}
}

// A notification is read only where the tool delivers it, never from a prompt
// snapshot that merely mentions the format.
func TestActivity_NotificationsOnlyFromTheConversation(t *testing.T) {
	call := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"make","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"u1","content":"running"}]},"toolUseResult":{"backgroundTaskId":"b1"}}
`
	snapshot := `{"type":"attachment","attachment":{"type":"prompt_snapshot","prompt":"<task-notification><task-id>b1</task-id><status>failed</status></task-notification>"}}` + "\n"
	if got := foldActivity(t, call+snapshot, nil).dto; got.Tasks[0].State != activityRunning {
		t.Fatalf("a prompt snapshot ended a task: %+v", got.Tasks[0])
	}
}

// The session's vault secrets never reach the activity: each record passes the
// session's redactor first, as its attach stream does.
func TestActivity_RedactsTheSessionSecrets(t *testing.T) {
	const secret = "sk-live-activity-0123456789"
	redact := newSecretRedactor([]SecretEnvRef{{Env: "API_KEY", Secret: "deploy-key"}}, []EnvVar{{Name: "API_KEY", Value: secret}})
	got := foldActivity(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"curl -H 'k: `+secret+`' x","description":"Call `+secret+`","run_in_background":true}}]}}
`, redact).dto
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte(secret)) || !strings.Contains(got.Tasks[0].Command, "deploy-key") {
		t.Fatalf("activity = %s", raw)
	}
}

// activityFixtureHome replaces the move fixture's transcript with the real one up to
// the background command's end, and gives it a subagent transcript.
func activityFixtureHome(t *testing.T, f *moveFixture) (transcript string) {
	t.Helper()
	proj := filepath.Join(f.homeA, "projects", moveTestProject)
	transcript = filepath.Join(proj, moveTestSID+".jsonl")
	head, _ := fixtureLines(t)
	writeFile(t, transcript, strings.Join(head, "\n")+"\n")
	writeFile(t, filepath.Join(proj, moveTestSID, "subagents", "agent-a1.jsonl"),
		`{"type":"user","isSidechain":true,"agentId":"a1","message":{"role":"user","content":"Write agent-a.txt containing A"}}`+"\n"+
			`{"type":"assistant","isSidechain":true,"agentId":"a1","timestamp":"2026-10-12T22:11:45.000Z","message":{"content":[{"type":"tool_use","id":"s1","name":"Write","input":{"file_path":"/home/dev/demo/agent-a.txt","content":"A"}}]}}`+"\n"+
			`{"type":"user","isSidechain":true,"agentId":"a1","timestamp":"2026-10-12T22:11:45.100Z","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"File created"}]},"toolUseResult":{"type":"create","filePath":"/home/dev/demo/agent-a.txt","content":"A","structuredPatch":[]}}`+"\n"+
			`{"type":"assistant","isSidechain":true,"agentId":"a1","message":{"content":[{"type":"text","text":"done"}]}}`+"\n")
	return transcript
}

func (f *moveFixture) activity(t *testing.T) (activityDTO, resp) {
	t.Helper()
	r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/activity", f.admin, nil, f.hdr)
	var dto activityDTO
	if r.code == http.StatusOK {
		if err := json.Unmarshal([]byte(r.raw), &dto); err != nil {
			t.Fatal(err)
		}
	}
	return dto, r
}

func countAudit(t *testing.T, f *moveFixture, action string) int {
	t.Helper()
	n := 0
	ctx := context.Background()
	if err := f.m.Data.View(ctx, f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == action {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// The endpoint reads the running session's transcript again on every request, so
// what the tool writes next is in the next answer, and a reload loses nothing.
func TestActivity_EndpointFollowsTheTranscript(t *testing.T) {
	f := newMoveFixture(t)
	transcript := activityFixtureHome(t, f)

	got, r := f.activity(t)
	if r.code != http.StatusOK {
		t.Fatalf("activity = %d %s", r.code, r.raw)
	}
	if len(got.Agents) != 2 || len(got.Tasks) != 1 || got.Tasks[0].State != activityRunning {
		t.Fatalf("activity = %s", r.raw)
	}
	paths := map[string]bool{}
	for _, file := range got.Files {
		paths[file.Path] = true
	}
	if len(got.Files) != 4 || !paths["/home/dev/demo/agent-a.txt"] || !paths["/home/dev/demo/three.txt"] {
		t.Fatalf("files (a subagent's included) = %+v", got.Files)
	}
	if strings.Contains(r.raw, f.homeA) || strings.Contains(r.raw, "tok-account-a") {
		t.Fatalf("activity leaks a home or a login: %s", r.raw)
	}

	// The tool reports the background command's end; the next read has it.
	_, notification := fixtureLines(t)
	fh, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(notification + "\n"); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if got, _ = f.activity(t); got.Tasks[0].State != activityCompleted {
		t.Fatalf("after the notification: %+v", got.Tasks[0])
	}
	// Every read is the same person on the same session: recorded once.
	if n := countAudit(t, f, "sessions.run.activity"); n != 1 {
		t.Fatalf("activity reads recorded %d times, want 1", n)
	}

	// The subagent's own transcript opens from its id.
	r = f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/activity/agents/a1", f.admin, nil, f.hdr)
	if r.code != http.StatusOK || !strings.Contains(r.raw, "Write agent-a.txt containing A") || !strings.Contains(r.raw, `"role":"tool"`) {
		t.Fatalf("agent transcript = %d %s", r.code, r.raw)
	}
	if countAudit(t, f, "sessions.run.activity_agent") != 1 {
		t.Fatal("the agent transcript read was not recorded")
	}
	for _, id := range []string{"missing", "..", "a1%2F..%2F..%2Fx"} {
		r = f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/activity/agents/"+id, f.admin, nil, f.hdr)
		if r.code != http.StatusNotFound {
			t.Fatalf("agent %q = %d %s", id, r.code, r.raw)
		}
	}

	// Once the session stops, nothing it started is running.
	head, _ := fixtureLines(t)
	agentCall := 0
	for i, l := range head {
		if strings.Contains(l, `"name":"Agent"`) {
			agentCall = i
			break
		}
	}
	writeFile(t, transcript, strings.Join(head[:agentCall+1], "\n")+"\n")
	if got, _ = f.activity(t); got.Agents[0].State != activityRunning {
		t.Fatalf("a running session's open agent = %+v", got.Agents[0])
	}
	f.stop(t)
	if got, r = f.activity(t); r.code != http.StatusOK || got.Agents[0].State != activityStopped {
		t.Fatalf("activity of a stopped session = %d %s", r.code, r.raw)
	}
}

func TestActivity_EndpointRefusals(t *testing.T) {
	f := newMoveFixture(t)

	// No transcript yet is no activity yet.
	if err := os.Remove(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if got, r := f.activity(t); r.code != http.StatusOK || len(got.Agents) != 0 || got.Driver != "claude" {
		t.Fatalf("no transcript = %d %s", r.code, r.raw)
	}
	activityFixtureHome(t, f)

	real := f.homeA
	setRunColumn(t, f, colRunProfileConfigHome, t.TempDir())
	if _, r := f.activity(t); r.code != http.StatusConflict || !strings.Contains(r.raw, "unproven home") {
		t.Fatalf("unproven home = %d %s", r.code, r.raw)
	}
	setRunColumn(t, f, colRunProfileConfigHome, real)

	setRunColumn(t, f, colRunProfileDriver, "opencode")
	if _, r := f.activity(t); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("another tool's session = %d %s", r.code, r.raw)
	}
	setRunColumn(t, f, colRunProfileDriver, "claude")

	// A session that held a repository credential or a managed credential is refused, running
	// or stopped: the transcript on disk is unredacted, and a value an earlier generation
	// printed cannot be withheld by the current one's redactor (the broker's withholding is
	// of what the plane stores, not of the tool's own transcript).
	givens := []struct{ column, value, said string }{
		{colRunGitRead, "binding:owner/name", "repository credential"},
		{colRunCredentials, `["prod-db"]`, "managed credential"},
	}
	for _, phase := range []string{"running", "stopped"} {
		if phase == "stopped" {
			f.stop(t)
		}
		for _, given := range givens {
			setRunColumn(t, f, given.column, given.value)
			if _, r := f.activity(t); r.code != http.StatusConflict || !strings.Contains(r.raw, given.said) {
				t.Fatalf("%s: secret-bearing %s session = %d %s", given.said, phase, r.code, r.raw)
			}
			if r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/messages", f.admin, nil, f.hdr); r.code != http.StatusConflict || !strings.Contains(r.raw, given.said) {
				t.Fatalf("%s: secret-bearing %s messages = %d %s", given.said, phase, r.code, r.raw)
			}
			if r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/activity/agents/a1", f.admin, nil, f.hdr); r.code != http.StatusConflict {
				t.Fatalf("%s: secret-bearing %s agent transcript = %d %s", given.said, phase, r.code, r.raw)
			}
			setRunColumn(t, f, given.column, "")
		}
	}
}

// What the transcript's state is, said instead of an empty panel.
func TestActivity_SaysWhetherTheTranscriptIsPendingOrMissing(t *testing.T) {
	f := newMoveFixture(t)
	if err := os.Remove(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.activity(t); got.Transcript != activityTranscriptPending {
		t.Fatalf("a running session without its transcript yet = %q", got.Transcript)
	}
	f.stop(t)
	if got, _ := f.activity(t); got.Transcript != activityTranscriptMissing {
		t.Fatalf("a stopped session without its transcript = %q", got.Transcript)
	}
	activityFixtureHome(t, f)
	if got, _ := f.activity(t); got.Transcript != activityTranscriptRead || len(got.Agents) != 2 {
		t.Fatalf("a session with its transcript = %+v", got)
	}
	setClaudeSessionID(t, f, "")
	if got, _ := f.activity(t); got.Transcript != activityTranscriptPending {
		t.Fatalf("a session that never reported its id = %q", got.Transcript)
	}
}

// A subagent id never leaves the session's subagents directory, and a link there is
// not followed into another session's.
func TestActivity_AgentTranscriptsStayInTheSession(t *testing.T) {
	f := newMoveFixture(t)
	activityFixtureHome(t, f)
	proj := filepath.Join(f.homeA, "projects", moveTestProject)
	agents := filepath.Join(proj, moveTestSID, "subagents")
	// What "../../x" would reach: agent-../../x.jsonl cleans to subagents/x.jsonl.
	writeFile(t, filepath.Join(agents, "x.jsonl"), `{"type":"user","isSidechain":true,"message":{"content":"PLANTED"}}`+"\n")
	if _, err := f.m.runAgentTranscript(context.Background(), f.tenant, f.runRef, "../../x"); err != errActivityNoAgent {
		t.Fatalf("a traversing agent id = %v", err)
	}

	// Another session's subagents, linked in place of this one's.
	other := filepath.Join(proj, "other-session", "subagents")
	writeFile(t, filepath.Join(other, "agent-b1.jsonl"),
		`{"type":"user","isSidechain":true,"message":{"content":"OTHER SESSION"}}`+"\n")
	if err := os.RemoveAll(agents); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, agents); err != nil {
		t.Fatal(err)
	}
	if _, r := f.activity(t); r.code != http.StatusUnprocessableEntity || strings.Contains(r.raw, "OTHER") {
		t.Fatalf("activity through a linked subagents directory = %d %s", r.code, r.raw)
	}
	r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/activity/agents/b1", f.admin, nil, f.hdr)
	if r.code == http.StatusOK || strings.Contains(r.raw, "OTHER SESSION") {
		t.Fatalf("agent transcript through a linked directory = %d %s", r.code, r.raw)
	}
}

// A subagent transcript that cannot be read costs its own files, counted.
func TestActivity_ABadSubagentFileIsCountedNotFatal(t *testing.T) {
	f := newMoveFixture(t)
	activityFixtureHome(t, f)
	agents := filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID, "subagents")
	if err := os.Link(filepath.Join(agents, "agent-a1.jsonl"), filepath.Join(agents, "agent-a2.jsonl")); err != nil {
		t.Fatal(err)
	}
	got, r := f.activity(t)
	if r.code != http.StatusOK || got.Unreadable != 2 || len(got.Agents) != 2 || len(got.Files) != 3 {
		t.Fatalf("with two hard-linked subagent files: %d unreadable %d agents %d files %d", r.code, got.Unreadable, len(got.Agents), len(got.Files))
	}
}

// The context is what the latest real request sent: input plus cached input.
func TestActivity_ContextIsTheLatestRequest(t *testing.T) {
	fixture := string(readFixture(t))
	var want int64
	for _, l := range strings.Split(strings.TrimSpace(fixture), "\n") {
		var rec activityRecord
		if json.Unmarshal([]byte(l), &rec) == nil && rec.Type == "assistant" && rec.Message.Usage != nil {
			u := rec.Message.Usage
			want = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		}
	}
	if want == 0 {
		t.Fatal("fixture has no usage")
	}
	synthetic := `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error"}],"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n"
	got := foldActivity(t, fixture+synthetic, nil).dto
	if got.ContextTokens != want || got.Model != "claude-haiku-5-5" {
		t.Fatalf("context %d model %q, want %d", got.ContextTokens, got.Model, want)
	}
}

// The tool's reported window, not a model-family guess, supplies the denominator.
func TestActivity_ContextWindowFromTheTool(t *testing.T) {
	for _, tc := range []struct {
		name, models string
		window       int64
		percent      float64
	}{
		{"200k", `"claude-opus-5-5":{"inputTokens":1,"outputTokens":1,"contextWindow":200000}`, 200000, 41.793},
		{"1m", `"claude-opus-5-5[1m]":{"inputTokens":1,"outputTokens":1,"contextWindow":1000000}`, 1000000, 8.3586},
		{"other model", `"claude-other-model":{"inputTokens":1,"outputTokens":1,"contextWindow":1000000}`, 0, 0},
		{"missing window", `"claude-opus-5-5":{"inputTokens":1,"outputTokens":1}`, 0, 0},
		{"invalid window", `"claude-opus-5-5":{"inputTokens":1,"outputTokens":1,"contextWindow":-1}`, 0, 0},
		{"ambiguous variants", `"claude-opus-5-5":{"inputTokens":1,"outputTokens":1,"contextWindow":200000},"claude-opus-5-5[1m]":{"contextWindow":1000000}`, 0, 0},
		{"unknown variant", `"claude-opus-5-5":{"inputTokens":1,"outputTokens":1},"claude-opus-5-5[1m]":{"contextWindow":1000000}`, 0, 0},
		{"invalid variant", `"claude-opus-5-5":{"inputTokens":1,"outputTokens":1,"contextWindow":200000},"claude-opus-5-5[1m]":{"contextWindow":-1}`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t)
			transcript := activityFixtureHome(t, f)
			data, err := os.ReadFile(transcript)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, transcript, strings.ReplaceAll(string(data), "claude-haiku-5-5", "claude-opus-5-5"))
			frame := fmt.Sprintf(`{"type":"result","modelUsage":{%s}}`, tc.models)
			f.fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(frame)}
			waitFor(t, "result metering", func() bool {
				d, _ := f.m.getRun(context.Background(), f.tenant, f.runRef)
				return d.OutputTokens != nil && *d.OutputTokens == 1
			})
			_, response := f.activity(t)
			if response.code != http.StatusOK {
				t.Fatal(response.raw)
			}
			var got struct {
				Tokens  int64    `json:"context_tokens"`
				Window  *int64   `json:"context_window"`
				Percent *float64 `json:"context_percent"`
			}
			if err := json.Unmarshal([]byte(response.raw), &got); err != nil {
				t.Fatal(err)
			}
			if got.Tokens != 83586 {
				t.Fatalf("tokens = %d", got.Tokens)
			}
			if tc.window == 0 {
				if got.Window != nil || got.Percent != nil || !strings.Contains(response.raw, `"context_window":null`) {
					t.Fatalf("unknown window: %s", response.raw)
				}
			} else if got.Window == nil || *got.Window != tc.window || got.Percent == nil || math.Abs(*got.Percent-tc.percent) > 1e-9 {
				t.Fatalf("window/percent: %s; want %d / %g", response.raw, tc.window, tc.percent)
			}
			// A later result can report no window without adding another billed
			// token. The previous denominator must still become unknown.
			if tc.name == "200k" {
				f.fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(strings.ReplaceAll(frame, `"contextWindow":200000`, `"unknownWindow":200000`))}
				waitFor(t, "window becomes unknown on unchanged metering", func() bool {
					r, _ := f.m.loadRun(context.Background(), f.tenant, f.runRef)
					return r.String("context_windows") == `{"claude-opus-5-5":0}`
				})
				_, unknown := f.activity(t)
				if !strings.Contains(unknown.raw, `"context_window":null`) {
					t.Fatal(unknown.raw)
				}
			}
			// Read after stopping: the denominator is retained on the session row.
			if _, err := f.m.stopRun(context.Background(), f.tenant, f.runRef, "user:u1", model.ActorUser); err != nil {
				t.Fatal(err)
			}
			_, stopped := f.activity(t)
			if tc.name == "1m" && !strings.Contains(stopped.raw, fmt.Sprintf(`"context_window":%d`, tc.window)) {
				t.Fatal(stopped.raw)
			}
			if tc.name == "1m" {
				if r := f.post(t, "resume", nil); r.code != http.StatusOK {
					t.Fatal(r.raw)
				}
				_, resumed := f.activity(t)
				if !strings.Contains(resumed.raw, `"context_window":null`) {
					t.Fatal(resumed.raw)
				}
			}
		})
	}
}

// What the stored report means for a model, including the shapes the endpoint test
// cannot reach: equal sizes on both variants, no tokens yet, and a damaged column.
func TestActivityContextWindowReadsTheStoredReport(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		tokens       int64
		window       int64
		percent      float64
		unreadable   int
	}{
		{"both variants report the same size", `{"m":200000,"m[1m]":200000}`, 50000, 200000, 25, 0},
		{"no tokens is zero percent, not unknown", `{"m":200000}`, 0, 200000, 0, 0},
		{"tokens beyond the window are not clipped", `{"m":1000}`, 1500, 1000, 150, 0},
		{"an empty report is unknown", `{}`, 50000, 0, 0, 0},
		{"only another model is reported", `{"n":200000}`, 50000, 0, 0, 0},
		{"no report is unknown", ``, 50000, 0, 0, 0},
		{"a damaged column is counted as unreadable", `not-json`, 50000, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dto := activityDTO{Model: "m", ContextTokens: tc.tokens}
			activityContextWindow(&dto, model.Record{colRunContextWindows: tc.stored})
			if dto.Unreadable != tc.unreadable {
				t.Fatalf("unreadable = %d, want %d", dto.Unreadable, tc.unreadable)
			}
			if tc.window == 0 {
				if dto.ContextWindow != nil || dto.ContextPercent != nil {
					t.Fatalf("want unknown, got window set=%t percent set=%t", dto.ContextWindow != nil, dto.ContextPercent != nil)
				}
				return
			}
			if dto.ContextWindow == nil || *dto.ContextWindow != tc.window || dto.ContextPercent == nil || math.Abs(*dto.ContextPercent-tc.percent) > 1e-9 {
				t.Fatalf("want %d / %g, got %+v", tc.window, tc.percent, dto)
			}
		})
	}
}

func TestActivity_FileAccounting(t *testing.T) {
	got := foldActivity(t, `{"type":"assistant","timestamp":"t1","message":{"content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"/p/a.txt"}},{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/p/b.txt"}}]}}
{"type":"user","timestamp":"t2","message":{"content":[{"type":"tool_result","tool_use_id":"w1","content":"ok"}]},"toolUseResult":{"type":"create","filePath":"/p/a.txt","content":"one\ntwo\nthree\n","structuredPatch":[]}}
{"type":"user","timestamp":"t2","message":{"content":[{"type":"tool_result","tool_use_id":"e1","is_error":true,"content":"denied"}]},"toolUseResult":{"filePath":"/p/b.txt","structuredPatch":[{"lines":["-x","+y"]}]}}
{"type":"assistant","isSidechain":true,"timestamp":"t3","message":{"content":[{"type":"tool_use","id":"s1","name":"Write","input":{}}]}}
{"type":"user","isSidechain":true,"timestamp":"t3","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"ok"}]},"toolUseResult":{"type":"create","filePath":"/p/side.txt","content":"x"}}
`, nil)
	if len(got.dto.Files) != 1 {
		t.Fatalf("a refused edit or another conversation's record was counted: %+v", got.dto.Files)
	}
	if a := got.dto.Files[0]; a.Path != "/p/a.txt" || a.Added != 3 || !a.Created || a.Edits != 1 {
		t.Fatalf("created file = %+v", a)
	}
	// A subagent's change to the same file adds to it.
	got.mergeFiles([]activityFile{{Path: "/p/a.txt", Added: 2, Removed: 1, Edits: 1, LastAt: "t9"}, {Path: "/p/c.txt", Added: 1, Edits: 1}})
	if a := got.dto.Files[0]; a.Added != 5 || a.Removed != 1 || a.Edits != 2 || a.LastAt != "t9" || len(got.dto.Files) != 2 {
		t.Fatalf("merged = %+v", got.dto.Files)
	}
}

// A wakeup whose time came before the tool's latest record has fired; an item that
// ended keeps how it ended; an unknown ending is a stop, not "running" forever.
func TestActivity_EndingsAreFinal(t *testing.T) {
	got := foldActivity(t, `{"type":"assistant","timestamp":"2026-10-12T10:00:00Z","message":{"content":[{"type":"tool_use","id":"k1","name":"ScheduleWakeup","input":{"delaySeconds":60,"reason":"Check CI"}},{"type":"tool_use","id":"k2","name":"ScheduleWakeup","input":{"delaySeconds":3600,"reason":"Later"}},{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"make","run_in_background":true}},{"type":"tool_use","id":"b2","name":"Bash","input":{"command":"sleep 99","run_in_background":true}}]}}
{"type":"user","timestamp":"2026-10-12T10:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"b1","content":"bg"},{"type":"tool_result","tool_use_id":"b2","content":"bg"}]},"toolUseResult":{"backgroundTaskId":"t1"}}
{"type":"user","timestamp":"2026-10-12T10:00:30Z","message":{"content":"<task-notification><tool-use-id>b1</tool-use-id><status>completed</status></task-notification>"}}
{"type":"user","timestamp":"2026-10-12T10:00:40Z","message":{"content":"<task-notification><tool-use-id>b1</tool-use-id><status>killed</status></task-notification><task-notification><tool-use-id>b2</tool-use-id><status>timeout</status></task-notification>"}}
{"type":"assistant","timestamp":"2026-10-12T10:02:00Z","message":{"content":[{"type":"text","text":"woke"}]}}
{"type":"assistant","timestamp":"2026-10-12T10:02:00Z","message":{"content":[{"type":"tool_use","id":"k1","name":"ScheduleWakeup","input":{"delaySeconds":60,"reason":"Check CI"}}]}}
`, nil).dto
	if w := got.Tasks[0]; w.Kind != "wakeup" || w.State != activityCompleted || w.DueAt != "2026-10-12T10:01:00Z" {
		t.Fatalf("fired wakeup = %+v", w)
	}
	if w := got.Tasks[1]; w.State != activityScheduled {
		t.Fatalf("future wakeup = %+v", w)
	}
	if b := got.Tasks[2]; b.State != activityCompleted || b.EndedAt != "2026-10-12T10:00:30Z" {
		t.Fatalf("a late notification changed how a task ended: %+v", b)
	}
	if b := got.Tasks[3]; b.State != activityStopped {
		t.Fatalf("a timed-out task = %+v", b)
	}
}

// Claude Code 2.1.296 keeps the task list of a Claude 5 session with TaskCreate and
// TaskUpdate (TodoWrite is for its legacy models). The fixture is a real session
// that created three tasks, started and finished the first, started and renamed the
// second and deleted the third; only its task-tool records are kept.
func TestActivity_TheTaskToolsAreTheTaskList(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-2.1.296-tasks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSpace(string(b)), "\n")
	for _, step := range []struct {
		records int
		want    []activityTodo
	}{
		{6, []activityTodo{
			{Content: "Read the code", Status: "pending", ActiveForm: "Reading the code"},
			{Content: "Write the test", Status: "pending", ActiveForm: "Writing the test"},
			{Content: "Drop this one", Status: "pending", ActiveForm: "Dropping this one"},
		}},
		{8, []activityTodo{
			{Content: "Read the code", Status: "in_progress", ActiveForm: "Reading the code"},
			{Content: "Write the test", Status: "pending", ActiveForm: "Writing the test"},
			{Content: "Drop this one", Status: "pending", ActiveForm: "Dropping this one"},
		}},
		{len(lines), []activityTodo{
			{Content: "Read the code", Status: "completed", ActiveForm: "Reading the code"},
			{Content: "Write the failing test", Status: "in_progress", ActiveForm: "Writing the test"},
		}},
	} {
		got := foldActivity(t, strings.Join(lines[:step.records], ""), nil).dto
		if got.Unreadable != 0 || !slices.Equal(got.Todos, step.want) {
			t.Fatalf("after %d records: todos %+v unreadable %d, want %+v", step.records, got.Todos, got.Unreadable, step.want)
		}
	}
	// A refused update changes nothing, and a later TodoWrite list replaces the whole list.
	got := foldActivity(t, string(b)+`{"type":"assistant","timestamp":"2026-10-13T03:00:00Z","message":{"content":[{"type":"tool_use","id":"u9","name":"TaskUpdate","input":{"taskId":"1","status":"pending"}}]}}
{"type":"user","timestamp":"2026-10-13T03:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"u9","content":"Task not found","is_error":true}]},"toolUseResult":"Error: Task not found"}
`, nil).dto
	if len(got.Todos) != 2 || got.Todos[0].Status != "completed" {
		t.Fatalf("a refused update changed the list: %+v", got.Todos)
	}
	// Deleting an item before others keeps the later ones addressable; an update the
	// tool reports as unsuccessful, or for an id it never gave, changes nothing.
	got = foldActivity(t, string(b)+`{"type":"assistant","timestamp":"2026-10-13T03:00:00Z","message":{"content":[{"type":"tool_use","id":"c4","name":"TaskCreate","input":{"subject":"Ship it","activeForm":"Shipping it","description":"Ship it"}}]}}
{"type":"user","timestamp":"2026-10-13T03:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"c4","content":"Task #4 created successfully: Ship it"}]},"toolUseResult":{"task":{"id":"4","subject":"Ship it"}}}
{"type":"assistant","timestamp":"2026-10-13T03:00:02Z","message":{"content":[{"type":"tool_use","id":"d1","name":"TaskUpdate","input":{"taskId":"1","status":"deleted"}},{"type":"tool_use","id":"s4","name":"TaskUpdate","input":{"taskId":"4","status":"in_progress"}},{"type":"tool_use","id":"f2","name":"TaskUpdate","input":{"taskId":"2","status":"completed"}},{"type":"tool_use","id":"x9","name":"TaskUpdate","input":{"taskId":"9","status":"completed"}}]}}
{"type":"user","timestamp":"2026-10-13T03:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"d1","content":"Updated task #1 deleted"}]},"toolUseResult":{"success":true,"taskId":"1","updatedFields":["deleted"],"statusChange":{"from":"completed","to":"deleted"}}}
{"type":"user","timestamp":"2026-10-13T03:00:04Z","message":{"content":[{"type":"tool_result","tool_use_id":"s4","content":"Updated task #4 status"}]},"toolUseResult":{"success":true,"taskId":"4","updatedFields":["status"],"statusChange":{"from":"pending","to":"in_progress"}}}
{"type":"user","timestamp":"2026-10-13T03:00:05Z","message":{"content":[{"type":"tool_result","tool_use_id":"f2","content":"Task #2 is blocked"}]},"toolUseResult":{"success":false,"taskId":"2","updatedFields":[]}}
{"type":"user","timestamp":"2026-10-13T03:00:06Z","message":{"content":[{"type":"tool_result","tool_use_id":"x9","content":"Task #9 not found"}]},"toolUseResult":{"success":false,"taskId":"9","updatedFields":[]}}
`, nil).dto
	if want := []activityTodo{
		{Content: "Write the failing test", Status: "in_progress", ActiveForm: "Writing the test"},
		{Content: "Ship it", Status: "in_progress", ActiveForm: "Shipping it"},
	}; got.Unreadable != 0 || !slices.Equal(got.Todos, want) {
		t.Fatalf("after a delete and refused updates: %+v unreadable %d, want %+v", got.Todos, got.Unreadable, want)
	}
	// A record read twice is one item; an update of an item the list lacks, or a
	// create without an id, is counted rather than lost.
	firstCreate := lines[0] + lines[1]
	got = foldActivity(t, string(b)+firstCreate+`{"type":"assistant","timestamp":"2026-10-13T03:00:00Z","message":{"content":[{"type":"tool_use","id":"u7","name":"TaskUpdate","input":{"taskId":"7","status":"completed"}},{"type":"tool_use","id":"c8","name":"TaskCreate","input":{"subject":"Lost"}}]}}
{"type":"user","timestamp":"2026-10-13T03:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"u7","content":"Updated task #7 status"}]},"toolUseResult":{"success":true,"taskId":"7","updatedFields":["status"],"statusChange":{"from":"pending","to":"completed"}}}
{"type":"user","timestamp":"2026-10-13T03:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"c8","content":"Task created"}]},"toolUseResult":{}}
`, nil).dto
	if len(got.Todos) != 2 || got.Unreadable != 2 {
		t.Fatalf("a repeated create, an unknown update and an id-less create: %+v unreadable %d", got.Todos, got.Unreadable)
	}
	got = foldActivity(t, string(b)+activityExtra, nil).dto
	if len(got.Todos) != 2 || got.Todos[0].Content != "Edit the files" {
		t.Fatalf("TodoWrite after the task tools = %+v", got.Todos)
	}
}

// The subagents' directory is opened once: a link swapped in for it after the check
// does not redirect the reads to another session's records.
func TestActivity_SubagentsDirectoryIsOpenedOnce(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, "projects", "p", "sid", "subagents")
	other := filepath.Join(home, "projects", "p", "other", "subagents")
	writeFile(t, filepath.Join(agents, "agent-a1.jsonl"), "OWN\n")
	writeFile(t, filepath.Join(other, "agent-a1.jsonl"), "OTHER\n")
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dir, err := openAgentsDir(root, "projects/p/sid.jsonl")
	if err != nil || dir == nil {
		t.Fatalf("open = %v, %v", dir, err)
	}
	defer dir.Close()
	if err := os.Rename(agents, agents+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, agents); err != nil {
		t.Fatal(err)
	}
	var got string
	if _, err := readActivityState(context.Background(), dir, "agent-a1.jsonl", func(_ context.Context, r io.Reader) error {
		b, err := io.ReadAll(r)
		got = string(b)
		return err
	}); err != nil || got != "OWN\n" {
		t.Fatalf("read after the swap = %q, %v", got, err)
	}
	// A new open sees the link and refuses it; no subagents at all is no refusal.
	if _, err := openAgentsDir(root, "projects/p/sid.jsonl"); !errors.Is(err, errClaudeStateSpecial) {
		t.Fatalf("open through a link = %v", err)
	}
	if dir, err := openAgentsDir(root, "projects/p/none.jsonl"); dir != nil || err != nil {
		t.Fatalf("absent = %v, %v", dir, err)
	}
}
