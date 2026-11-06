// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
)

// askEngine is the part of an engine `olivares ask` talks to: Ask's settings, a scripted turn
// reply, the decision route and the run route.
type askEngine struct {
	*httptest.Server
	mu        sync.Mutex
	enabled   bool
	reply     string
	turns     int
	decisions int
	runs      []string
}

func newAskEngine(t *testing.T, reply string) *askEngine {
	t.Helper()
	e := &askEngine{enabled: true, reply: reply}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/m/models/ask":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": e.enabled, "model": "pol-1", "tools": []map[string]any{
				{"name": "runs.list", "kind": "read", "permission": "sessions:run:read", "allowed": true},
				{"name": "run.start", "kind": "write", "permission": "sessions:run:write", "allowed": true},
			}})
		case "POST /v1/m/models/ask/turns":
			e.turns++
			io.WriteString(w, e.reply)
		case "POST /v1/m/governance/approvals/ap-1/decisions":
			e.decisions++
			io.WriteString(w, `{"id":"ap-1","status":"approved"}`)
		case "POST /v1/m/models/ask/proposals/ap-1/run":
			raw, _ := io.ReadAll(r.Body)
			e.runs = append(e.runs, string(raw))
			io.WriteString(w, `{"status":201,"command":"olivares session start --name fix-tests","approval_id":"ap-1","output":"{\"run_ref\":\"run-1\"}"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.Close)
	return e
}

const askProposalReply = `{"kind":"proposal","tool":"run.start","steps":[],"proposal":{"approval_id":"ap-1","status":"pending","tool":"run.start","permission":"sessions:run:write","args":{"name":"fix-tests"},"command":"olivares session start --name fix-tests"}}`

// The engine ran the reads: each one's equivalent command is printed, then the answer.
func TestAskPrintsEachReadAndTheAnswer(t *testing.T) {
	e := newAskEngine(t, `{"kind":"answer","text":"No sessions are running.","steps":[{"tool":"runs.list","command":"olivares session ls","status":200}]}`)
	out, errb, err := execSessionCLI(t, nil, append([]string{"ask", "which sessions are running?"}, sessionCreds(e.URL)...)...)
	if err != nil {
		t.Fatalf("ask: %v\n%s", err, errb)
	}
	if strings.TrimSpace(out) != "No sessions are running." || !strings.Contains(errb, "= olivares session ls") ||
		!strings.Contains(errb, "Ask can use: runs.list (read, sessions:run:read)") {
		t.Fatalf("out=%q err=%q", out, errb)
	}
}

// A write is only a proposal: without a terminal nothing is approved or run, the command to run
// it once approved is printed, and the exit code says a person has to decide.
func TestAskProposalWaitsForAPersonWithoutATerminal(t *testing.T) {
	e := newAskEngine(t, askProposalReply)
	out, _, err := execSessionCLI(t, nil, append([]string{"ask", "start fix-tests"}, sessionCreds(e.URL)...)...)
	if exitcode.From(err) != exitcode.ApprovalRequired || e.decisions != 0 || len(e.runs) != 0 {
		t.Fatalf("exit=%d decisions=%d runs=%d (%v)", exitcode.From(err), e.decisions, len(e.runs), err)
	}
	if !strings.Contains(out, `olivares ask --run ap-1 --tool run.start --args '{"name":"fix-tests"}'`) {
		t.Errorf("the run command is not shown: %q", out)
	}
}

// With Ask off the command says how an administrator turns it on, and asks nothing.
func TestAskOffSaysHowToTurnItOn(t *testing.T) {
	e := newAskEngine(t, askProposalReply)
	e.enabled = false
	_, _, err := execSessionCLI(t, nil, append([]string{"ask", "hello"}, sessionCreds(e.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || e.turns != 0 || !strings.Contains(err.Error(), "olivares models ask bind") {
		t.Fatalf("exit=%d turns=%d (%v)", exitcode.From(err), e.turns, err)
	}
}

// The Ask Olivares row of `olivares ui` asks each line as a question through the same runner:
// a yes at the terminal approves the proposal as the person and runs it once; an empty line ends.
func TestUIAskRowAsksQuestionsAndRunsAnApprovedProposal(t *testing.T) {
	if got := mainMenuLabels(mainMenuFrame(rootStatusReport{})); !strings.Contains(strings.Join(got, ","), "Ask Olivares") {
		t.Fatalf("main menu rows = %q", got)
	}
	e := newAskEngine(t, askProposalReply)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := agentClientConfig{server: e.URL, token: "tok", tenant: "t-1", timeout: askTurnTimeout}
	if err := cfg.resolve(); err != nil {
		t.Fatalf("resolve the client: %v", err)
	}
	var out bytes.Buffer
	if err := runAskShell(context.Background(), strings.NewReader("start fix-tests\ny\n\n"), &out, &cfg, true); err != nil {
		t.Fatalf("ask shell: %v\n%s", err, out.String())
	}
	if e.turns != 1 || e.decisions != 1 || len(e.runs) != 1 || e.runs[0] != `{"args":{"name":"fix-tests"},"tool":"run.start"}` {
		t.Fatalf("turns=%d decisions=%d runs=%q\n%s", e.turns, e.decisions, e.runs, out.String())
	}
	if !strings.Contains(out.String(), "= olivares session start --name fix-tests") || !strings.Contains(out.String(), "Done.") {
		t.Errorf("shell output:\n%s", out.String())
	}
}

// D3, T8: with the inference-egress module off no data-loss rules can be read, so no prompt
// leaves: the engine installs no rules port (Ask answers 503), or the port refuses.
func TestAskPortsWithoutDataLossRulesSendNothing(t *testing.T) {
	ports := askPorts(nil, nil, nil)
	if ports.PromptDenied == nil {
		return
	}
	denied, err := ports.PromptDenied(context.Background(), model.TenantID("acme"), "my key is ghp_0000")
	if err == nil && len(denied) == 0 {
		t.Fatal("a prompt was allowed out with no data-loss rules read")
	}
}
