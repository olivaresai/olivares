// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCodexActivity_DiffCountsHunkContent(t *testing.T) {
	for _, source := range []string{"native diff", "patch input", "patch input without hunk marker"} {
		t.Run(source, func(t *testing.T) {
			fold := newCodexActivityFold(newActivityFold(false, nil))
			var records string
			if source == "native diff" {
				records = `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"edit","status":"completed","changes":{"counter.txt":{"type":"update","unified_diff":"--- counter.txt\n+++ counter.txt\n@@ -1,2 +1,2 @@\n---value\n--- value\n+++value\n+++ value\n"}}}}}`
			} else {
				patch := "*** Begin Patch\n*** Update File: counter.txt\n@@\n---value\n--- value\n+++value\n+++ value\n*** End Patch"
				if source == "patch input without hunk marker" {
					patch = strings.Replace(patch, "@@\n", "", 1)
				}
				input, _ := json.Marshal(patch)
				records = fmt.Sprintf(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"edit","input":%s}}
{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"edit","output":"Success. Updated the following files:\nM counter.txt"}}`, input)
			}
			if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+records+"\n"), moveTestSID); err != nil {
				t.Fatal(err)
			}
			if got := fold.dto.Files; len(got) != 2 || got[1].Added != 2 || got[1].Removed != 2 || got[1].Edits != 1 || fold.dto.Unreadable != 0 {
				t.Fatalf("hunk lines beginning with +++/--- = %+v; unreadable=%d", got, fold.dto.Unreadable)
			}
		})
	}
}

func TestCodexActivity_CollaborationOrderings(t *testing.T) {
	call := `{"timestamp":"2026-10-13T12:00:09Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn","arguments":"{\"message\":\"Check the file\",\"agent_type\":\"explorer\"}"}}`
	result := `{"timestamp":"2026-10-13T12:00:11Z","type":"response_item","payload":{"type":"function_call_output","call_id":"spawn","output":"{\"agent_id\":\"child\"}"}}`
	collab := `{"timestamp":"2026-10-13T12:00:10Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CollabAgentToolCall","id":"spawn","tool":"spawn_agent","prompt":"Check the file","model":"gpt-5.5","receiver_thread_ids":["child"],"agents_states":{"child":{"completed":"Looks good"}}}}}`
	for _, order := range []struct {
		name  string
		lines []string
	}{
		{"collab before output", []string{call, collab, result}},
		{"output before collab", []string{call, result, collab}},
		{"collab before call", []string{collab, call, result}},
	} {
		t.Run(order.name, func(t *testing.T) {
			fold := newCodexActivityFold(newActivityFold(false, nil))
			if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+strings.Join(order.lines, "\n")+"\n"), moveTestSID); err != nil {
				t.Fatal(err)
			}
			if got := fold.dto.Agents; len(got) != 1 || got[0].AgentID != "child" || got[0].ToolUseID != "spawn" || got[0].State != activityCompleted || got[0].Summary != "Looks good" || got[0].Description != "Check the file" || got[0].Type != "explorer" || got[0].Model != "gpt-5.5" || fold.dto.Unreadable != 0 {
				t.Fatalf("one agent across native views = %+v; unreadable=%d", got, fold.dto.Unreadable)
			}
		})
	}
}

func TestCodexActivity_PlanRetainsStateOnMissingOrNull(t *testing.T) {
	for _, source := range []string{"call result", "native event", "live notification"} {
		for _, plan := range []string{`{}`, `{"plan":null}`, `{"plan":[]}`} {
			t.Run(source+plan, func(t *testing.T) {
				var got []activityTodo
				want := []activityTodo{{Content: "Edit file", Status: "in_progress"}, {Content: "Check", Status: "pending"}}
				var unreadable int
				if source == "live notification" {
					want = []activityTodo{{Content: "Keep", Status: "pending"}}
					s := &codexSession{threadID: moveTestSID, turnID: "turn"}
					s.onNotification("turn/plan/updated", json.RawMessage(fmt.Sprintf(`{"threadId":%q,"turnId":"turn","plan":[{"step":"Keep","status":"pending"}]}`, moveTestSID)))
					var payload map[string]any
					if err := json.Unmarshal([]byte(plan), &payload); err != nil {
						t.Fatal(err)
					}
					payload["threadId"], payload["turnId"] = moveTestSID, "turn"
					raw, _ := json.Marshal(payload)
					s.onNotification("turn/plan/updated", raw)
					got, _, _, unreadable = s.activityPlanSnapshot(moveTestSID)
				} else {
					fold := newCodexActivityFold(newActivityFold(false, nil))
					var record string
					if source == "call result" {
						args, _ := json.Marshal(plan)
						record = fmt.Sprintf(`{"type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"plan2","arguments":%s}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"plan2","output":"Plan updated"}}`, args)
					} else {
						var payload map[string]any
						if err := json.Unmarshal([]byte(plan), &payload); err != nil {
							t.Fatal(err)
						}
						payload["type"] = "plan_update"
						raw, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": payload})
						record = string(raw)
					}
					if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+record+"\n"), moveTestSID); err != nil {
						t.Fatal(err)
					}
					got, unreadable = fold.dto.Todos, fold.dto.Unreadable
				}
				if plan == `{"plan":[]}` {
					if len(got) != 0 || unreadable != 0 {
						t.Fatalf("explicit empty plan = %+v; unreadable=%d", got, unreadable)
					}
				} else if !slices.Equal(got, want) || unreadable != 1 {
					t.Fatalf("malformed plan must retain todos = %+v; unreadable=%d", got, unreadable)
				}
			})
		}
	}
}

func TestCodexActivity_PlanRejectsFinishedTurnsWhileIdle(t *testing.T) {
	for _, completionBeforeStart := range []bool{false, true} {
		t.Run(fmt.Sprint(completionBeforeStart), func(t *testing.T) {
			s := &codexSession{threadID: moveTestSID}
			notify := func(turn, step string) {
				s.onNotification("turn/plan/updated", json.RawMessage(fmt.Sprintf(`{"threadId":%q,"turnId":%q,"plan":[{"step":%q,"status":"pending"}]}`, moveTestSID, turn, step)))
			}
			if !completionBeforeStart {
				s.setActiveTurn("old-turn", "inProgress")
			}
			notify("old-turn", "Keep") // Allowed before the start caller records the turn.
			_, at, _, _ := s.activityPlanSnapshot(moveTestSID)
			s.onNotification(codexNotifyTurnCompleted, json.RawMessage(fmt.Sprintf(`{"threadId":%q,"turn":{"id":"old-turn","status":"completed"}}`, moveTestSID)))
			if completionBeforeStart {
				s.setActiveTurn("old-turn", "inProgress")
			}
			notify("old-turn", "Late stale update")
			got, after, _, unreadable := s.activityPlanSnapshot(moveTestSID)
			if len(got) != 1 || got[0].Content != "Keep" || !after.Equal(at) || unreadable != 0 || s.ActiveTurn() != "" {
				t.Fatalf("finished turn plan = %+v; unreadable=%d", got, unreadable)
			}
			notify("new-turn", "Start-response race")
			got, _, _, unreadable = s.activityPlanSnapshot(moveTestSID)
			if len(got) != 1 || got[0].Content != "Start-response race" || unreadable != 0 {
				t.Fatalf("new turn before start response = %+v; unreadable=%d", got, unreadable)
			}
		})
	}
}

func TestCodexActivity_CommandLearnsLaterNativeProcessID(t *testing.T) {
	for _, completion := range []string{"native interaction", "write_stdin"} {
		t.Run(completion, func(t *testing.T) {
			records := `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"exec2","arguments":"{\"cmd\":\"sleep 1\"}"}}
{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"exec2","command":["sleep","1"],"process_id":"77"}}
`
			if completion == "native interaction" {
				records += `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"interaction","source":"unified_exec_interaction","process_id":"77","command":["sleep","1"],"status":"completed","exit_code":0}}}`
			} else {
				records += `{"type":"response_item","payload":{"type":"function_call","name":"write_stdin","call_id":"stdin2","arguments":"{\"session_id\":77,\"chars\":\"\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"stdin2","output":"Process exited with code 0\nFinal output:\ndone"}}`
			}
			fold := newCodexActivityFold(newActivityFold(false, nil))
			if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+records+"\n"), moveTestSID); err != nil {
				t.Fatal(err)
			}
			if got := fold.dto.Tasks; len(got) != 2 || got[1].TaskID != "77" || got[1].ToolUseID != "exec2" || got[1].State != activityCompleted || fold.dto.Unreadable != 0 {
				t.Fatalf("command/process correlation = %+v; unreadable=%d", got, fold.dto.Unreadable)
			}
		})
	}
}

func TestCodexActivity_CloseAgentReportsMalformedResult(t *testing.T) {
	for _, output := range []string{"invalid JSON", `{}`, `{"previous_status":null}`, `{"previous_status":{}}`, `{"previous_status":7}`, `{"previous_status":{"running":42}}`, `{"previous_status":"future"}`} {
		t.Run(output, func(t *testing.T) {
			result, _ := json.Marshal(output)
			records := fmt.Sprintf(`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn","arguments":"{\"message\":\"Check\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"spawn","output":"{\"agent_id\":\"child\"}"}}
{"type":"response_item","payload":{"type":"function_call","name":"close_agent","call_id":"close","arguments":"{\"id\":\"child\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"close","output":%s}}`, result)
			fold := newCodexActivityFold(newActivityFold(false, nil))
			if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+records+"\n"), moveTestSID); err != nil {
				t.Fatal(err)
			}
			if got := fold.dto.Agents; len(got) != 1 || got[0].State != activityRunning || fold.dto.Unreadable != 1 {
				t.Fatalf("unreadable close must preserve prior state = %+v; unreadable=%d", got, fold.dto.Unreadable)
			}
		})
	}
}

// Native rollout shapes from Codex 0.162.1's protocol and persistence policy.
// The tool persists calls/results even when transient exec/plan events are omitted.
const codexActivityRecords = `{"timestamp":"2026-10-13T12:00:00Z","type":"session_meta","payload":{"id":"5b0c1a6e-1845-4a2b-9c3d-000000000001","source":"cli"}}
{"timestamp":"2026-10-13T12:00:01Z","type":"turn_context","payload":{"model":"gpt-5.4","effort":"high"}}
{"timestamp":"2026-10-13T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"plan1","arguments":"{\"plan\":[{\"step\":\"Edit file\",\"status\":\"in_progress\"},{\"step\":\"Check\",\"status\":\"pending\"}]}"}}
{"timestamp":"2026-10-13T12:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"plan1","output":"Plan updated"}}
{"timestamp":"2026-10-13T12:00:04Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"exec1","arguments":"{\"cmd\":\"sleep 1; echo done\",\"yield_time_ms\":1}"}}
{"timestamp":"2026-10-13T12:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"exec1","output":"Process running with session ID 42"}}
{"timestamp":"2026-10-13T12:00:06Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"patch1","input":"*** Begin Patch\n*** Add File: one.txt\n+one\n+two\n*** End Patch"}}
{"timestamp":"2026-10-13T12:00:07Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"patch1","success":true,"changes":{"one.txt":{"type":"add","content":"one\ntwo\n"}}}}
{"timestamp":"2026-10-13T12:00:07Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"patch1","output":"Success. Updated the following files:\nA one.txt"}}
{"timestamp":"2026-10-13T12:00:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":99000},"last_token_usage":{"input_tokens":1200,"cached_input_tokens":900,"output_tokens":50,"total_tokens":1250},"model_context_window":258400}}}
`

// Produced by a real Codex 0.162.1 process against a labelled loopback Responses
// stand-in. Codex ran the command, applied the patch and executed update_plan.
// Only activity records remain; the home/path and thread id are normalized.
func TestCodexActivity_RealNativeRollout(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-0.162.1-activity.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	fold := newCodexActivityFold(newActivityFold(false, nil))
	if err := fold.read(context.Background(), strings.NewReader(string(data)), moveTestSID); err != nil {
		t.Fatal(err)
	}
	got := fold.dto
	if got.Unreadable != 0 || got.Model != "gpt-5.5" || got.ContextTokens != 1256 || (got.ContextWindow == nil || *got.ContextWindow != 258400) || len(got.Tasks) != 1 || got.Tasks[0].State != activityCompleted || got.Tasks[0].Summary != "exit code 0" || len(got.Todos) != 2 || got.Todos[1].Status != "completed" {
		t.Fatalf("real native rollout = %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "/work/native.txt" || got.Files[0].Added != 2 || !got.Files[0].Created || got.Files[0].Edits != 1 {
		t.Fatalf("real native patch = %+v", got.Files)
	}
	f, file := newCodexActivityFixture(t)
	writeFile(t, file, string(data))
	if answer, r := f.activity(t); r.code != http.StatusOK || answer.ContextTokens != got.ContextTokens || len(answer.Files) != 1 {
		t.Fatalf("real native record through the API = %d %s", r.code, r.raw)
	} else if capture := os.Getenv("OLIVARES_ACTIVITY_CAPTURE"); capture != "" {
		if err := os.WriteFile(capture, []byte(r.raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func newCodexActivityFixture(t *testing.T) (*moveFixture, string) {
	t.Helper()
	f := newMoveFixture(t)
	home, user := t.TempDir(), t.TempDir()
	p := mustCreateProfile(t, f.m, f.tenant, CreateProfileInput{Driver: "codex", ConfigHome: home, UserHome: user, AuthSource: AuthSourceAccountHome})
	for col, val := range map[string]any{colRunProfileID: p.Ref, colRunProfileDriver: "codex", colRunProfileConfigHome: home, colRunProfileUserHome: user} {
		setRunColumn(t, f, col, val)
	}
	f.homeA = home
	file := filepath.Join(home, "sessions", "2026", "10", "13", "rollout-2026-10-13T12-00-00-"+moveTestSID+".jsonl")
	writeFile(t, file, codexActivityRecords)
	return f, file
}

func TestCodexActivity_AgentTranscriptRemainsUnsupported(t *testing.T) {
	f, file := newCodexActivityFixture(t)
	for range 2 {
		r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/activity/agents/a1", f.admin, nil, f.hdr)
		if r.code != http.StatusUnprocessableEntity {
			t.Fatalf("Codex child transcript refusal = %d %s", r.code, r.raw)
		}
		// The unsupported driver is refused before its native files are opened.
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func TestCodexActivity_EndpointReadsNativeRecordsAndRefreshes(t *testing.T) {
	f, file := newCodexActivityFixture(t)
	got, r := f.activity(t)
	if r.code != http.StatusOK {
		t.Fatalf("Codex activity = %d %s", r.code, r.raw)
	}
	if got.Driver != "codex" || got.Transcript != "read" || got.Model != "gpt-5.4" || got.Effort != "high" || got.ContextTokens != 1250 || r.body["context_window"] != float64(258400) {
		t.Fatalf("native context = %s", r.raw)
	}
	if len(got.Todos) != 2 || got.Todos[0].Content != "Edit file" || got.Todos[0].Status != "in_progress" {
		t.Fatalf("plan = %+v", got.Todos)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].State != "running" || got.Tasks[0].TaskID != "42" || got.Tasks[0].Command != "sleep 1; echo done" {
		t.Fatalf("background command = %+v", got.Tasks)
	}
	if len(got.Files) != 1 || got.Files[0].Added != 2 || got.Files[0].Edits != 1 || !got.Files[0].Created || got.Unreadable != 0 {
		t.Fatalf("successful patch counted once = %s", r.raw)
	}
	const end = `{"timestamp":"2026-10-13T12:00:09Z","type":"response_item","payload":{"type":"function_call","name":"write_stdin","call_id":"wait1","arguments":"{\"session_id\":42,\"chars\":\"\"}"}}
{"timestamp":"2026-10-13T12:00:10Z","type":"response_item","payload":{"type":"function_call_output","call_id":"wait1","output":"Process exited with code 0\nFinal output:\ndone"}}
{"timestamp":"2026-10-13T12:00:11Z","type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"plan2","arguments":"{\"plan\":[{\"step\":\"Edit file\",\"status\":\"completed\"},{\"step\":\"Check\",\"status\":\"completed\"}]}"}}
{"timestamp":"2026-10-13T12:00:12Z","type":"response_item","payload":{"type":"function_call_output","call_id":"plan2","output":"Plan updated"}}
`
	writeFile(t, file, codexActivityRecords+end)
	for range 2 { // A new request, including after a reload, reads the same native state.
		got, r = f.activity(t)
		if r.code != http.StatusOK || len(got.Tasks) != 1 || got.Tasks[0].State != "completed" || !strings.Contains(got.Tasks[0].Summary, "exit code 0") || got.Todos[1].Status != "completed" {
			t.Fatalf("after command and plan finish = %d %s", r.code, r.raw)
		}
	}
}

func TestCodexActivity_ReadRefusals(t *testing.T) {
	for _, scenario := range []string{"unproven home", "secret", "wrong id", "link", "hardlink", "linked sessions", "too large"} {
		t.Run(scenario, func(t *testing.T) {
			f, file := newCodexActivityFixture(t)
			want := http.StatusUnprocessableEntity
			switch scenario {
			case "unproven home":
				setRunColumn(t, f, colRunProfileConfigHome, t.TempDir())
				want = http.StatusConflict
			case "secret":
				setRunColumn(t, f, colRunGitRead, "binding:owner/name")
				want = http.StatusConflict
			case "wrong id":
				writeFile(t, file, strings.ReplaceAll(codexActivityRecords, moveTestSID, "another-thread"))
			case "link", "hardlink":
				other := filepath.Join(f.homeA, "not-a-rollout")
				if err := os.Rename(file, other); err != nil {
					t.Fatal(err)
				}
				var err error
				if scenario == "link" {
					err = os.Symlink(other, file)
				} else {
					err = os.Link(other, file)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "linked sessions":
				dir := filepath.Join(f.homeA, "sessions")
				other := filepath.Join(f.homeA, "other")
				if err := os.Rename(dir, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, dir); err != nil {
					t.Fatal(err)
				}
			case "too large":
				if err := os.Truncate(file, activityFileMax+1); err != nil {
					t.Fatal(err)
				}
			}
			if _, r := f.activity(t); r.code != want {
				t.Fatalf("%s = %d %s, want %d", scenario, r.code, r.raw, want)
			}
		})
	}
}

func TestCodexActivity_PendingMissingStoppedAndRedacted(t *testing.T) {
	f, file := newCodexActivityFixture(t)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if got, r := f.activity(t); r.code != http.StatusOK || got.Transcript != "pending" {
		t.Fatalf("pending = %d %s", r.code, r.raw)
	}
	setRunColumn(t, f, colState, stateStopped)
	if got, r := f.activity(t); r.code != http.StatusOK || got.Transcript != "missing" {
		t.Fatalf("missing = %d %s", r.code, r.raw)
	}
	writeFile(t, file, codexActivityRecords)
	if got, r := f.activity(t); r.code != http.StatusOK || got.Tasks[0].State != "stopped" {
		t.Fatalf("stopped = %d %s", r.code, r.raw)
	}
	// Read the live record only with the same redactor as the attach stream.
	f, file = newCodexActivityFixture(t)
	lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
	lr.redact = newSecretRedactor(nil, []EnvVar{{Name: "REGISTRY_TOKEN", Value: "PRIVATE-COMMAND"}})
	writeFile(t, file, strings.ReplaceAll(codexActivityRecords, "sleep 1; echo done", "PRIVATE-COMMAND"))
	if _, r := f.activity(t); r.code != http.StatusOK || strings.Contains(r.raw, "PRIVATE-COMMAND") {
		t.Fatalf("redaction = %d %s", r.code, r.raw)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.m.runActivity(ctx, f.tenant, f.runRef); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	var decoded map[string]any
	got, r := f.activity(t)
	raw, _ := json.Marshal(got)
	if json.Unmarshal(raw, &decoded) != nil || r.code != http.StatusOK {
		t.Fatal("not a DTO")
	}
}

func TestCodexActivity_FailedEditsPartialRecordsAndNativeItems(t *testing.T) {
	// Command output cannot impersonate native result metadata. Refused plans and
	// patches change nothing; completed native items and results are not two edits.
	const extra = `{"timestamp":"2026-10-13T12:00:09Z","type":"response_item","payload":{"type":"function_call","name":"shell_command","call_id":"failed","arguments":"{\"command\":\"exit 7\"}"}}
{"timestamp":"2026-10-13T12:00:10Z","type":"response_item","payload":{"type":"function_call_output","call_id":"failed","output":"Exit code: 7\nOutput:\nProcess exited with code 0"}}
{"timestamp":"2026-10-13T12:00:11Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"failed","command":["exit","7"],"status":"failed","exit_code":7}}}
{"timestamp":"2026-10-13T12:00:12Z","type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"bad-plan","arguments":"{\"plan\":[{\"step\":\"Do not show\",\"status\":\"completed\"}]}"}}
{"timestamp":"2026-10-13T12:00:13Z","type":"response_item","payload":{"type":"function_call_output","call_id":"bad-plan","output":"Error: invalid plan"}}
{"timestamp":"2026-10-13T12:00:14Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"bad-patch","input":"*** Begin Patch\n*** Add File: refused.txt\n+no\n*** End Patch"}}
{"timestamp":"2026-10-13T12:00:15Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"bad-patch","success":false,"changes":{"refused.txt":{"type":"add","content":"no\n"}}}}
{"timestamp":"2026-10-13T12:00:16Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"bad-patch","output":"Error: patch refused"}}
{"timestamp":"2026-10-13T12:00:17Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"edit","status":"completed","changes":{"one.txt":{"type":"update","unified_diff":"--- one.txt\n+++ one.txt\n@@ -1 +1 @@\n-one\n+three\n","move_path":"renamed.txt"}}}}}
`
	fold := newCodexActivityFold(newActivityFold(false, nil))
	if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+extra+`{"type":"event_msg",`), moveTestSID); err != nil {
		t.Fatal(err)
	}
	got := fold.dto
	if got.Unreadable != 0 || len(got.Tasks) != 2 || got.Tasks[1].State != "failed" || got.Tasks[1].Summary != "exit code 7" || len(got.Files) != 2 || len(got.Todos) != 2 {
		t.Fatalf("failed, partial or duplicated record = %+v", got)
	}
	if got.Files[1].Path != "renamed.txt" || got.Files[1].Added != 1 || got.Files[1].Removed != 1 {
		t.Fatalf("rename diff = %+v", got.Files[1])
	}
	// A successful custom patch with no redundant event is still read.
	withoutEvent := strings.Join(strings.Split(codexActivityRecords, "\n")[:7], "\n") + "\n" + strings.Split(codexActivityRecords, "\n")[8] + "\n"
	fold = newCodexActivityFold(newActivityFold(false, nil))
	if err := fold.read(context.Background(), strings.NewReader(withoutEvent), moveTestSID); err != nil {
		t.Fatal(err)
	}
	if len(fold.dto.Files) != 1 || fold.dto.Files[0].Added != 2 || fold.dto.Files[0].Edits != 1 {
		t.Fatalf("patch from durable call/result = %+v", fold.dto.Files)
	}
}

func TestCodexActivity_CollaborationAndUnreadableRecords(t *testing.T) {
	const agents = `{"timestamp":"2026-10-13T12:00:09Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn","arguments":"{\"message\":\"Check the file\",\"agent_type\":\"explorer\"}"}}
{"timestamp":"2026-10-13T12:00:10Z","type":"response_item","payload":{"type":"function_call_output","call_id":"spawn","output":"{\"agent_id\":\"child\"}"}}
{"timestamp":"2026-10-13T12:00:11Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CollabAgentToolCall","id":"spawn","tool":"spawn_agent","receiver_thread_ids":["child"],"agents_states":{"child":"running"}}}}
{"timestamp":"2026-10-13T12:00:12Z","type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"wait","arguments":"{\"ids\":[\"child\"]}"}}
{"timestamp":"2026-10-13T12:00:13Z","type":"response_item","payload":{"type":"function_call_output","call_id":"wait","output":"{\"status\":{\"child\":{\"completed\":\"Looks good\"}},\"timed_out\":false}"}}
`
	fold := newCodexActivityFold(newActivityFold(false, nil))
	if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+agents), moveTestSID); err != nil {
		t.Fatal(err)
	}
	if len(fold.dto.Agents) != 1 || fold.dto.Agents[0].AgentID != "child" || fold.dto.Agents[0].State != "completed" || fold.dto.Agents[0].Summary != "Looks good" || fold.dto.Agents[0].DurationMs != 4000 {
		t.Fatalf("native collaboration = %+v", fold.dto.Agents)
	}
	broken := "not JSON\n" + `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":"invalid"}}}}` + "\n"
	fold = newCodexActivityFold(newActivityFold(false, nil))
	if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+broken), moveTestSID); err != nil {
		t.Fatal(err)
	}
	if fold.dto.Unreadable != 2 || fold.dto.ContextTokens != 1250 {
		t.Fatalf("malformed records = %+v", fold.dto)
	}
}

func TestCodexActivity_ArchivedAmbiguousAndPartialHeader(t *testing.T) {
	f, file := newCodexActivityFixture(t)
	archive := filepath.Join(f.homeA, "archived_sessions", filepath.Base(file))
	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file, archive); err != nil {
		t.Fatal(err)
	}
	if got, r := f.activity(t); r.code != http.StatusOK || got.Transcript != "read" {
		t.Fatalf("archived = %d %s", r.code, r.raw)
	}
	writeFile(t, file, codexActivityRecords)
	if _, r := f.activity(t); r.code != http.StatusConflict {
		t.Fatalf("ambiguous = %d %s", r.code, r.raw)
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, `{"timestamp":"2026-10-13T12:00:00Z","type":"session_meta",`)
	if got, r := f.activity(t); r.code != http.StatusOK || got.Transcript != "pending" {
		t.Fatalf("header being written = %d %s", r.code, r.raw)
	}
}

func TestCodexActivity_CodeModePlanUsesNativeNotifications(t *testing.T) {
	f, _ := newCodexActivityFixture(t)
	lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
	session := &codexSession{threadID: moveTestSID, turnID: "live-turn"}
	previous := lr.session
	lr.session = session
	t.Cleanup(func() { lr.session = previous })
	notify := func(thread, turn, step, status string) {
		payload, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "plan": []map[string]string{{"step": step, "status": status}}})
		session.onNotification("turn/plan/updated", payload)
	}
	// Code Mode's nested call arguments need not be in the rollout. The native
	// app-server still reports the plan to the bridge, which already receives it.
	notify(moveTestSID, "live-turn", "Native live plan", "inProgress")
	for range 2 {
		got, r := f.activity(t)
		if r.code != http.StatusOK || len(got.Todos) != 1 || got.Todos[0].Content != "Native live plan" || got.Todos[0].Status != "in_progress" {
			t.Fatalf("native plan after refresh/reload = %d %s", r.code, r.raw)
		}
	}
	notify("another-thread", "live-turn", "Foreign", "completed")
	notify(moveTestSID, "old-turn", "Stale", "completed")
	if got, _ := f.activity(t); got.Todos[0].Content != "Native live plan" {
		t.Fatalf("foreign or stale plan = %+v", got.Todos)
	}
	lr.redact = newSecretRedactor(nil, []EnvVar{{Name: "REGISTRY_TOKEN", Value: "PRIVATE-PLAN"}})
	notify(moveTestSID, "live-turn", "PRIVATE-PLAN", "completed")
	if got, r := f.activity(t); r.code != http.StatusOK || len(got.Todos) != 1 || got.Todos[0].Status != "completed" || strings.Contains(r.raw, "PRIVATE-PLAN") {
		t.Fatalf("native plan redaction = %d %s", r.code, r.raw)
	}
}

func TestCodexActivity_CompactionUsesPersistedCheckpoint(t *testing.T) {
	// Codex persists the checkpoint, recomputes usage, then completes the UI
	// item. That completion is another view of the same compaction.
	records := `{"type":"compacted","payload":{}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100},"model_context_window":258400}}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"ContextCompaction","id":"compact-one"}}}
`
	fold := newCodexActivityFold(newActivityFold(false, nil))
	if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+records), moveTestSID); err != nil {
		t.Fatal(err)
	}
	if len(fold.dto.Compactions) != 1 || fold.dto.ContextTokens != 1100 {
		t.Fatalf("checkpoint and completed item = %+v", fold.dto)
	}
}

func TestCodexActivity_DurableVariants(t *testing.T) {
	cases := []struct {
		name, records string
		check         func(*testing.T, activityDTO)
	}{
		{"legacy shell", `{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"shell1","arguments":"{\"command\":[\"sh\",\"-c\",\"echo ok\"]}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"shell1","output":"{\"metadata\":{\"exit_code\":0},\"output\":\"ok\"}"}}
`, func(t *testing.T, d activityDTO) {
			if len(d.Tasks) != 2 || d.Tasks[1].State != "completed" || d.Tasks[1].Command != "sh -c echo ok" {
				t.Fatalf("legacy command = %+v", d.Tasks)
			}
		}},
		{"unreadable command", `{"type":"response_item","payload":{"type":"function_call","name":"shell_command","call_id":"shell1","arguments":"{\"command\":\"echo ok\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"shell1","output":"unrecognized metadata"}}
`, func(t *testing.T, d activityDTO) {
			if d.Unreadable != 1 || d.Tasks[1].State != "stopped" {
				t.Fatalf("unknown result = %+v", d)
			}
		}},
		{"failed plan", `{"type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"plan3","arguments":"{\"plan\":[{\"step\":\"Wrong\",\"status\":\"future\"}]}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"plan3","output":"Plan updated"}}
`, func(t *testing.T, d activityDTO) {
			if d.Unreadable != 1 || d.Todos[0].Content != "Edit file" {
				t.Fatalf("unknown status = %+v", d)
			}
		}},
		{"native delete", `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"delete","status":"completed","changes":{"gone.txt":{"type":"delete","content":"one\ntwo\n"}}}}}
`, func(t *testing.T, d activityDTO) {
			if len(d.Files) != 2 || d.Files[1].Removed != 2 || d.Files[1].Added != 0 {
				t.Fatalf("delete = %+v", d.Files)
			}
		}},
		{"native command events", `{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"command2","command":["false"],"process_id":"99"}}
{"type":"event_msg","payload":{"type":"exec_command_end","call_id":"command2","command":["false"],"process_id":"99","exit_code":1}}
`, func(t *testing.T, d activityDTO) {
			if len(d.Tasks) != 2 || d.Tasks[1].State != "failed" || d.Tasks[1].TaskID != "99" {
				t.Fatalf("native events = %+v", d.Tasks)
			}
		}},
		{"invalid tokens", `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1,"total_tokens":-1}}}}
`, func(t *testing.T, d activityDTO) {
			if d.Unreadable != 1 || d.ContextTokens != 1250 {
				t.Fatalf("invalid usage = %+v", d)
			}
		}},
		{"model change", `{"type":"turn_context","payload":{"model":"another-model","effort":"low"}}
`, func(t *testing.T, d activityDTO) {
			if d.Model != "another-model" || d.ContextTokens != 0 || d.ContextWindow != nil {
				t.Fatalf("model changed = %+v", d)
			}
		}},
		{"compaction", `{"type":"compacted","payload":{}}
`, func(t *testing.T, d activityDTO) {
			if len(d.Compactions) != 1 || d.ContextTokens != 0 {
				t.Fatalf("compaction = %+v", d)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fold := newCodexActivityFold(newActivityFold(false, nil))
			if err := fold.read(context.Background(), strings.NewReader(codexActivityRecords+c.records), moveTestSID); err != nil {
				t.Fatal(err)
			}
			c.check(t, fold.dto)
		})
	}
}
