// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestActivity_WorkflowLiveSnapshotsFromTheRealStream(t *testing.T) {
	f := newMoveFixture(t)
	workflowFixtureHome(t, f)
	lr, ok := f.m.rt.getLive(f.tenant, f.runRef)
	if !ok {
		t.Fatal("the fixture session is not live")
	}
	b, err := os.ReadFile("testdata/claude-2.1.296-workflow-progress.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		// The native events name this probe's task. Bind the same shapes to the
		// other real probe whose launch our home fixture contains.
		event["task_id"] = "wser0ji6z"
		event["tool_use_id"] = "toolu_01K8CjAFacEgg9prBcrzp5Ra"
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lr.ring.append(streamStdout, raw, time.Now())
		w := workflowAnswer(t, f)
		wantTotal, wantDone := 1, 0
		if i >= 2 {
			wantTotal = 2
		}
		if i >= 1 {
			wantDone = 1
		}
		if i == 3 {
			wantDone = 2
		}
		if w["agents_total"] != float64(wantTotal) || w["agents_done"] != float64(wantDone) {
			t.Fatalf("snapshot %d counts = %+v", i, w)
		}
		phases := w["phases"].([]any)
		if len(phases) != 2 {
			t.Fatalf("snapshot %d phases = %+v", i, phases)
		}
		if i == 0 && phases[0].(map[string]any)["state"] != activityRunning {
			t.Fatalf("Prepare is not visibly running: %+v", phases)
		}
		if i == 2 && (phases[0].(map[string]any)["state"] != nil || phases[1].(map[string]any)["state"] != activityRunning) {
			t.Fatalf("the live phase did not advance to Finish: %+v", phases)
		}
		if i == 1 || i == 3 {
			// The measured script waits after the agent returns. Agent completion
			// cannot tell us whether the script has left this phase.
			if phases[i/2].(map[string]any)["state"] != nil {
				t.Fatalf("agent completion invented a phase ending: %+v", phases)
			}
		}
		// A second browser/read must not double-count the same native snapshot.
		if again := workflowAnswer(t, f); !reflect.DeepEqual(w, again) {
			t.Fatalf("a reload changed snapshot %d: %+v", i, again)
		}
	}
}

// These are the launch and final metadata of a real Claude Code 2.1.296
// two-phase Workflow. Only the fields the Activity reader consumes remain.
func workflowFixtureHome(t *testing.T, f *moveFixture) string {
	t.Helper()
	project := filepath.Join(f.homeA, "projects", moveTestProject)
	b, err := os.ReadFile("testdata/claude-2.1.296-workflow.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(project, moveTestSID+".jsonl"), string(b))
	dir := filepath.Join(project, moveTestSID, "workflows")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func workflowAnswer(t *testing.T, f *moveFixture) map[string]any {
	t.Helper()
	_, r := f.activity(t)
	if r.code != http.StatusOK {
		t.Fatalf("activity = %d %s", r.code, r.raw)
	}
	var answer struct {
		Tasks []struct {
			Workflow map[string]any `json:"workflow"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(r.raw), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Tasks) != 1 || answer.Tasks[0].Workflow == nil {
		t.Fatalf("workflow metadata is absent: %s", r.raw)
	}
	return answer.Tasks[0].Workflow
}

func TestActivity_WorkflowFinalSummaryFromTheRealFile(t *testing.T) {
	f := newMoveFixture(t)
	dir := workflowFixtureHome(t, f)
	b, err := os.ReadFile("testdata/claude-2.1.296-workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "wf_557a7af4-74b.json"), string(b))
	if got, r := f.activity(t); r.code != http.StatusOK || got.Tasks[0].State != activityCompleted || got.Tasks[0].EndedAt != "2026-10-13T22:03:25.463Z" {
		t.Fatalf("the completed file did not end the task: %d %s", r.code, r.raw)
	}
	w := workflowAnswer(t, f)
	if w["run_id"] != "wf_557a7af4-74b" || w["name"] != "activity-probe" || w["duration_ms"] != float64(40015) {
		t.Fatalf("workflow = %+v", w)
	}
	phases, ok := w["phases"].([]any)
	if !ok || len(phases) != 2 {
		t.Fatalf("phases = %+v", w["phases"])
	}
	for i, title := range []string{"Prepare", "Finish"} {
		p := phases[i].(map[string]any)
		if p["title"] != title || p["detail"] == "" {
			t.Fatalf("phase %d = %+v", i, p)
		}
	}
	// Re-reading the same source, including after stop, gives the same phases.
	f.stop(t)
	if got := workflowAnswer(t, f); !reflect.DeepEqual(got, w) {
		t.Fatalf("after stop = %+v", got)
	}
}

func TestActivity_WorkflowDirectoryIsNotALink(t *testing.T) {
	f := newMoveFixture(t)
	dir := workflowFixtureHome(t, f)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, r := f.activity(t); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("linked workflows = %d %s", r.code, r.raw)
	}
}

func TestActivity_WorkflowFileRefusalsAreCounted(t *testing.T) {
	for _, variant := range []string{"symlink", "hardlink", "oversized", "invalid-json", "other-run", "other-task", "running-file", "unknown-status", "large-summary", "invalid-timestamp", "long-valid-timestamp"} {
		t.Run(variant, func(t *testing.T) {
			f := newMoveFixture(t)
			dir := workflowFixtureHome(t, f)
			path := filepath.Join(dir, "wf_557a7af4-74b.json")
			valid := `{"runId":"wf_557a7af4-74b","taskId":"wser0ji6z","phases":[{"title":"Wrong session"}],"status":"completed"}`
			switch variant {
			case "symlink", "hardlink":
				other := filepath.Join(t.TempDir(), "other.json")
				writeFile(t, other, valid)
				var err error
				if variant == "symlink" {
					err = os.Symlink(other, path)
				} else {
					err = os.Link(other, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "oversized":
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate(activityFileMax + 1)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "large-summary":
				writeFile(t, path, valid+strings.Repeat(" ", activityLineMax))
			case "invalid-timestamp":
				writeFile(t, path, strings.TrimSuffix(valid, "}")+`,"timestamp":"`+strings.Repeat("x", activitySummaryMax+1)+`"}`)
			case "long-valid-timestamp":
				stamp := "2026-10-13T22:03:25." + strings.Repeat("0", activitySummaryMax) + "Z"
				if _, err := time.Parse(time.RFC3339, stamp); err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, strings.TrimSuffix(valid, "}")+`,"timestamp":"`+stamp+`"}`)
			case "invalid-json":
				writeFile(t, path, "{")
			case "other-run":
				writeFile(t, path, `{"runId":"wf_other","taskId":"wser0ji6z","status":"completed","phases":[{"title":"Wrong session"}]}`)
			case "other-task":
				writeFile(t, path, `{"runId":"wf_557a7af4-74b","taskId":"other-task","status":"completed","phases":[{"title":"Wrong session"}]}`)
			case "running-file", "unknown-status":
				status := "running"
				if variant == "unknown-status" {
					status = "future-state"
				}
				writeFile(t, path, `{"runId":"wf_557a7af4-74b","taskId":"wser0ji6z","status":"`+status+`","phases":[{"title":"Wrong session"}]}`)
			}
			got, r := f.activity(t)
			if r.code != http.StatusOK || got.Unreadable != 1 {
				t.Fatalf("%s = %d %s", variant, r.code, r.raw)
			}
			if strings.Contains(r.raw, "Wrong session") {
				t.Fatalf("%s exposed the refused metadata: %s", variant, r.raw)
			}
			if len(got.Tasks) != 1 || got.Tasks[0].State != activityRunning {
				t.Fatalf("bad final metadata changed the running task: %+v", got.Tasks)
			}
		})
	}
}

func appendWorkflowSnapshot(t *testing.T, f *moveFixture, snapshot int, agentState string) {
	t.Helper()
	b, err := os.ReadFile("testdata/claude-2.1.296-workflow-progress.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(b)), "\n")[snapshot]), &event); err != nil {
		t.Fatal(err)
	}
	event["task_id"] = "wser0ji6z"
	event["tool_use_id"] = "toolu_01K8CjAFacEgg9prBcrzp5Ra"
	if agentState != "" {
		event["workflow_progress"].([]any)[2].(map[string]any)["state"] = agentState
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := f.m.rt.getLive(f.tenant, f.runRef)
	if !ok {
		t.Fatal("the fixture session is not live")
	}
	lr.ring.append(streamStdout, raw, time.Now())
}

func TestActivity_WorkflowLiveThenFinalAndInvalidSummary(t *testing.T) {
	f := newMoveFixture(t)
	dir := workflowFixtureHome(t, f)
	appendWorkflowSnapshot(t, f, 2, "")
	live := workflowAnswer(t, f)
	path := filepath.Join(dir, "wf_557a7af4-74b.json")
	writeFile(t, path, "{")
	if got := workflowAnswer(t, f); !reflect.DeepEqual(got, live) {
		t.Fatalf("invalid summary discarded live progress: %+v", got)
	}
	if got, _ := f.activity(t); got.Unreadable != 1 {
		t.Fatalf("invalid summary is not counted: %+v", got)
	}
	b, err := os.ReadFile("testdata/claude-2.1.296-workflow-agents.json")
	if err != nil {
		t.Fatal(err)
	}
	var final map[string]any
	if err := json.Unmarshal(b, &final); err != nil {
		t.Fatal(err)
	}
	// Bind the measured final record to the same launch fixture as its snapshots.
	final["runId"], final["taskId"] = "wf_557a7af4-74b", "wser0ji6z"
	b, err = json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
	w := workflowAnswer(t, f)
	if w["agents_total"] != float64(2) || w["agents_done"] != float64(2) || w["duration_ms"] != float64(32124) {
		t.Fatalf("final summary did not replace live progress: %+v", w)
	}
	for _, phase := range w["phases"].([]any) {
		p := phase.(map[string]any)
		if p["state"] != activityCompleted || p["detail"] == nil {
			t.Fatalf("final phase = %+v", p)
		}
	}
}

func TestActivity_WorkflowStoppedLiveAndFailedAgents(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "failed-agent"}[failed], func(t *testing.T) {
			f := newMoveFixture(t)
			workflowFixtureHome(t, f)
			state := ""
			if failed {
				state = "error"
			}
			appendWorkflowSnapshot(t, f, 0, state)
			if failed {
				w := workflowAnswer(t, f)
				if w["agents_failed"] != float64(1) || w["agents_done"] != float64(0) {
					t.Fatalf("failed agent progress = %+v", w)
				}
			} else {
				f.stop(t)
				w := workflowAnswer(t, f)
				if w["agents_total"] != float64(1) || w["phases"].([]any)[0].(map[string]any)["state"] != activityStopped {
					t.Fatalf("stopped progress = %+v", w)
				}
			}
		})
	}
}

func TestActivity_WorkflowRepeatedPhaseTitlesKeepTheirDetails(t *testing.T) {
	cases := []struct {
		name   string
		titles []string
	}{
		{"repeated", []string{"Repeat", "Repeat"}},
		{"clipped", []string{strings.Repeat("x", activitySummaryMax) + " first", strings.Repeat("x", activitySummaryMax) + " second"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t)
			dir := workflowFixtureHome(t, f)
			final := map[string]any{
				"runId": "wf_557a7af4-74b", "taskId": "wser0ji6z", "status": "completed",
				"phases":           []any{map[string]any{"title": tc.titles[0], "detail": "First detail"}, map[string]any{"title": tc.titles[1], "detail": "Second detail"}},
				"workflowProgress": []any{map[string]any{"type": "workflow_phase", "index": 1, "title": tc.titles[0]}, map[string]any{"type": "workflow_phase", "index": 2, "title": tc.titles[1]}},
			}
			b, err := json.Marshal(final)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "wf_557a7af4-74b.json"), string(b))
			phases := workflowAnswer(t, f)["phases"].([]any)
			if len(phases) != 2 || phases[0].(map[string]any)["detail"] != "First detail" || phases[1].(map[string]any)["detail"] != "Second detail" {
				t.Fatalf("repeated titles lost phase identity: %+v", phases)
			}
		})
	}
}

func TestActivity_WorkflowSummaryReadsHaveAnAggregateBudget(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fold := newActivityFold(false, nil)
	// Every valid record fits the per-summary ceiling. Their combined payload
	// exceeds the request budget, and the last live task must remain intact.
	n := activityFileMax/activityLineMax + 1
	for i := 0; i < n; i++ {
		id, task := fmt.Sprintf("wf_%d", i), fmt.Sprintf("task%d", i)
		fold.dto.Tasks = append(fold.dto.Tasks, activityTask{TaskID: task, Kind: "workflow", State: activityRunning, Workflow: &activityWorkflow{RunID: id}})
		b, err := json.Marshal(map[string]any{"runId": id, "taskId": task, "status": "completed", "workflowName": "Accepted", "padding": strings.Repeat("x", activityLineMax-1024)})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, id+".json"), string(b))
	}
	if err := fold.workflowFiles(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if fold.dto.Unreadable != 1 || fold.dto.Tasks[0].State != activityCompleted || fold.dto.Tasks[n-1].State != activityRunning {
		t.Fatalf("aggregate read budget was not enforced: unreadable=%d first=%s last=%s", fold.dto.Unreadable, fold.dto.Tasks[0].State, fold.dto.Tasks[n-1].State)
	}
}

func TestActivity_WorkflowSummaryHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeFile(t, filepath.Join(dir, "wf_1.json"), `{"runId":"wf_1","taskId":"task1","status":"completed"}`)
	fold := newActivityFold(false, nil)
	fold.dto.Tasks = []activityTask{{TaskID: "task1", Kind: "workflow", State: activityRunning, Workflow: &activityWorkflow{RunID: "wf_1"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fold.workflowFiles(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled summary read = %v", err)
	}
	if fold.dto.Tasks[0].State != activityRunning {
		t.Fatal("canceled summary mutated live progress")
	}
}

func TestActivity_WorkflowDirectoryDoesNotHideAnAgentTranscript(t *testing.T) {
	f := newMoveFixture(t)
	dir := workflowFixtureHome(t, f)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID, "subagents", "agent-a1.jsonl")
	writeFile(t, agent, `{"type":"user","isSidechain":true,"message":{"content":"OWN AGENT"}}`+"\n")
	got, err := f.m.runAgentTranscript(context.Background(), f.tenant, f.runRef, "a1")
	if err != nil || len(got.Turns) != 1 || got.Turns[0].Text != "OWN AGENT" {
		t.Fatalf("unrelated workflow directory hid the agent transcript: %+v %v", got, err)
	}
}

func TestActivity_WorkflowAgentProgressWithoutPhases(t *testing.T) {
	f := newMoveFixture(t)
	workflowFixtureHome(t, f)
	lr, ok := f.m.rt.getLive(f.tenant, f.runRef)
	if !ok {
		t.Fatal("fixture is not live")
	}
	lr.ring.append(streamStdout, []byte(`{"type":"system","subtype":"task_progress","task_id":"wser0ji6z","tool_use_id":"toolu_01K8CjAFacEgg9prBcrzp5Ra","workflow_progress":[{"type":"workflow_agent","index":1,"phaseIndex":0,"state":"done"},{"type":"workflow_agent","index":2,"phaseIndex":0,"state":"start"}]}`), time.Now())
	w := workflowAnswer(t, f)
	if w["agents_total"] != float64(2) || w["agents_done"] != float64(1) || len(w["phases"].([]any)) != 0 {
		t.Fatalf("unphased native agent progress = %+v", w)
	}
}

func TestActivity_WorkflowInvalidProgressPreservesTheLastValidSnapshot(t *testing.T) {
	f := newMoveFixture(t)
	workflowFixtureHome(t, f)
	appendWorkflowSnapshot(t, f, 0, "")
	w := workflowAnswer(t, f)
	appendWorkflowSnapshot(t, f, 0, "future-state")
	if got := workflowAnswer(t, f); !reflect.DeepEqual(got, w) {
		t.Fatalf("invalid native progress erased the last valid snapshot: %+v", got)
	}
	if got, _ := f.activity(t); got.Unreadable != 1 {
		t.Fatalf("invalid progress was not counted: %+v", got)
	}
}
