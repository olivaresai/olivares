// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares orchestration`. The lot-wide rules (credentials,
// exit codes, paging, PATCH semantics) live in cmd_agentexec_test.go; what is
// pinned here is what only this family decides.

// TestOrchestrationVerbsReachTheRoutesTheEngineRegisters walks every verb of the
// tree and asserts the method and path it puts on the wire.
//
// It exists because a thin client's whole job is the wire, and a wrong path is
// invisible from the inside: the command still renders, still exits 0 against a
// permissive stub, and only fails against a real engine — as a 404 somebody
// blames on their data.
func TestOrchestrationVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"orchestration", "graph"}, "GET", "/v1/m/orchestration/graph"},
		{[]string{"orchestration", "neighbors", "agent-a"}, "GET", "/v1/m/orchestration/graph/neighbors"},
		{[]string{"orchestration", "flows"}, "GET", "/v1/m/orchestration/flows"},
		{[]string{"orchestration", "timeline", "agent-a"}, "GET", "/v1/m/orchestration/timeline"},
		{[]string{"orchestration", "decisions"}, "GET", "/v1/m/orchestration/decisions"},
		{[]string{"orchestration", "schedules", "ls"}, "GET", "/v1/m/orchestration/schedules"},
		{[]string{"orchestration", "schedules", "get", "sc-1"}, "GET", "/v1/m/orchestration/schedules/sc-1"},
		{[]string{"orchestration", "schedules", "create", "--name", "n", "--subject-ref", "a"}, "POST", "/v1/m/orchestration/schedules"},
		{[]string{"orchestration", "schedules", "update", "sc-1", "--desired-status", "paused"}, "PATCH", "/v1/m/orchestration/schedules/sc-1"},
		{[]string{"orchestration", "schedules", "fire", "sc-1"}, "POST", "/v1/m/orchestration/schedules/sc-1/fire"},
		{[]string{"orchestration", "schedules", "decisions", "sc-1"}, "GET", "/v1/m/orchestration/schedules/sc-1/decisions"},
		{[]string{"orchestration", "schedules", "revisions", "sc-1"}, "GET", "/v1/m/orchestration/schedules/sc-1/revisions"},
		{[]string{"orchestration", "schedules", "restore", "sc-1", "--revision", "r-1"}, "POST", "/v1/m/orchestration/schedules/sc-1/restore"},
		{[]string{"orchestration", "workflows", "ls"}, "GET", "/v1/m/orchestration/workflows"},
		{[]string{"orchestration", "workflows", "get", "wf-1"}, "GET", "/v1/m/orchestration/workflows/wf-1"},
		{[]string{"orchestration", "workflows", "update", "wf-1", "--enabled=false"}, "PATCH", "/v1/m/orchestration/workflows/wf-1"},
		{[]string{"orchestration", "workflows", "revisions", "wf-1"}, "GET", "/v1/m/orchestration/workflows/wf-1/revisions"},
		{[]string{"orchestration", "workflows", "restore", "wf-1", "--revision", "r-1"}, "POST", "/v1/m/orchestration/workflows/wf-1/restore"},
		{[]string{"orchestration", "workflows", "dry-run", "wf-1"}, "POST", "/v1/m/orchestration/workflows/wf-1/dry-run"},
		{[]string{"orchestration", "workflows", "run", "wf-1"}, "POST", "/v1/m/orchestration/workflows/wf-1/run"},
		{[]string{"orchestration", "workflows", "runs", "ls", "wf-1"}, "GET", "/v1/m/orchestration/workflows/wf-1/runs"},
		{[]string{"orchestration", "workflows", "runs", "get", "wf-1", "run-7"}, "GET", "/v1/m/orchestration/workflows/wf-1/runs/run-7"},
	} {
		t.Run(strings.Join(tc.argv, "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"edges":[],"has_more":false,"id":"x"}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestOrchestrationWorkflowPatchNeverTouchesTheStepGraph. The graph is validated,
// hashed and approved as ONE UNIT: a metadata patch that also carried steps would
// let an approved plan hash drift under an edit nobody reviewed.
func TestOrchestrationWorkflowPatchNeverTouchesTheStepGraph(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"wf-1","enabled":false}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "workflows", "update", "wf-1", "--enabled=false")...); err != nil {
		t.Fatalf("the patch must succeed, got %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(srv.lastBody()), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, present := body["steps"]; present {
		t.Fatal("a metadata patch sent a step graph: an approved plan hash could drift under it")
	}
	if body["enabled"] != false {
		t.Fatalf("the typed flag did not travel: %s", srv.lastBody())
	}

	// THE CONTROL: set-steps DOES replace the graph, as a PUT.
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "workflows", "set-steps", "wf-1", "--steps-file", lot3WriteTempJSON(t, `[{"id":"s1"}]`))...); err != nil {
		t.Fatalf("set-steps must succeed, got %v", err)
	}
	if m, _ := srv.method.Load().(string); m != http.MethodPut {
		t.Errorf("set-steps used %s, want PUT — the graph is replaced as one unit", m)
	}
	if !strings.Contains(srv.lastBody(), `"s1"`) {
		t.Errorf("set-steps did not carry the graph: %s", srv.lastBody())
	}
}

// TestOrchestrationNeighborsRejectsAnInvalidDirectionBeforeAnyRequest. The engine
// treats anything that is not "incoming"/"outgoing" as neither, so a typo would
// silently return an EMPTY subgraph — "this agent talks to nobody" is a dangerous
// thing to be told by accident.
func TestOrchestrationNeighborsRejectsAnInvalidDirectionBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"edges":[]}`))
	_, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "neighbors", "agent-a", "--direction", "sideways")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("an invalid --direction must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with an invalid --direction", n)
	}
	for _, ok := range []string{"incoming", "outgoing", "both"} {
		if _, _, err := execRoot(t, lot3Args(srv.URL,
			"orchestration", "neighbors", "agent-a", "--direction", ok)...); err != nil {
			t.Fatalf("--direction %s must be accepted, got %v", ok, err)
		}
	}
}

// TestOrchestrationGraphRendersEdgesAndNamesItsCursor: the graph route answers
// with `edges`, not the standard `items`, so a renderer written for the list
// envelope would print "no relations" over a populated graph.
func TestOrchestrationGraphRendersEdgesAndNamesItsCursor(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"edges":[{"supervisor_ref":"a","worker_ref":"b",
		"link_kind":"delegation","state":"active"}],"cursor":"c-2","has_more":true}`))
	out, errOut, err := execRoot(t, lot3Args(srv.URL, "orchestration", "graph")...)
	if err != nil {
		t.Fatalf("the graph read must succeed, got %v", err)
	}
	if !strings.Contains(out, "delegation") {
		t.Fatalf("the edge was not rendered:\n%s", out)
	}
	if !strings.Contains(errOut, "c-2") {
		t.Errorf("the cursor must be named on stderr, got:\n%s", errOut)
	}
	if strings.Contains(out, "c-2") {
		t.Errorf("the cursor leaked into stdout:\n%s", out)
	}

	// THE CONTROL: an empty graph says so rather than printing nothing.
	empty := newLot3Server(t, lot3OK(`{"edges":[],"has_more":false}`))
	eout, _, eerr := execRoot(t, lot3Args(empty.URL, "orchestration", "graph")...)
	if eerr != nil {
		t.Fatalf("an empty graph must exit 0, got %v", eerr)
	}
	if !strings.Contains(eout, "no agent relations") {
		t.Errorf("an empty graph must say so, got: %q", eout)
	}
}

// TestOrchestrationFireIsPhaseOneWithNoBody. The engine gates phase 1 on the
// ABSENCE OF BODY BYTES, so sending "{}" where nothing was meant would turn every
// approval request into a malformed phase 2.
func TestOrchestrationFireIsPhaseOneWithNoBody(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"op":"fire","op_status":"dispatched"}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL, "orchestration", "schedules", "fire", "sc-1")...); err != nil {
		t.Fatalf("phase 1 must succeed, got %v", err)
	}
	if body := srv.lastBody(); body != "" {
		t.Fatalf("phase 1 sent a body %q; the engine reads phase 1 as the absence of one", body)
	}

	// THE CONTROL: phase 2 DOES carry the approval reference.
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "schedules", "fire", "sc-1", "--approval-ref", "ap-9")...); err != nil {
		t.Fatalf("phase 2 must succeed, got %v", err)
	}
	if !strings.Contains(srv.lastBody(), "ap-9") {
		t.Fatalf("phase 2 did not carry the approval ref: %q", srv.lastBody())
	}
}

// TestOrchestrationCreateRejectsAMalformedStepFileBeforeAnyRequest: a typo in a
// step graph should cost exit 2 locally, not a 400 the operator has to decode.
func TestOrchestrationCreateRejectsAMalformedStepFileBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"wf-1"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "orchestration", "workflows", "create",
		"--name", "n", "--steps-file", lot3WriteTempJSON(t, `{"not":"an array"}`))...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("a step file that is not an array must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with a malformed step file", n)
	}

	// THE CONTROL: a well-formed array is accepted and reaches the engine.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "orchestration", "workflows", "create",
		"--name", "n", "--steps-file", lot3WriteTempJSON(t, `[{"id":"s1"}]`))...); err != nil {
		t.Fatalf("a valid step file must be accepted, got %v", err)
	}
	if !strings.Contains(srv.lastBody(), `"s1"`) {
		t.Fatalf("the steps did not reach the engine: %s", srv.lastBody())
	}
}
