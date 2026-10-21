// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// Use a real native child and the real bridge: handing a frame straight to the
// waiter would bypass the line cap that caused #1871.
func TestToolCommands_OversizedInitializeKeepsNativeSessionAlive(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "first catalog", true: "stale catalog"}[cached], func(t *testing.T) {
			first := ""
			if cached {
				first = `if [ "$asked" = 1 ]; then
printf '{"type":"control_response","response":{"request_id":"%s","subtype":"success","response":{"commands":[{"name":"kept","description":"Previous catalog"}]}}}\n' "$id"
continue
fi
`
			}
			script := `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-overflow"}'
asked=0
while IFS= read -r line; do
case "$line" in
*'"subtype":"initialize"'*)
asked=$((asked+1))
id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
` + first + `printf '{"type":"control_response","response":{"request_id":"%s","subtype":"success","response":{"commands":[],"padding":"' "$id"
head -c 1100000 /dev/zero | tr '\000' x
printf '"}}}\n'
;;
*) printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"still alive"}]}}' ;;
esac
done
`
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(NewProcRunner()), WithProgram(writeOwnedChild(t, script)),
				WithCredentialSource(staticCred()), WithStopWaitDelay(100*time.Millisecond))
			ctx := context.Background()
			dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
			})
			if err != nil {
				t.Fatal(err)
			}
			lr, ok := m.rt.getLive(tenant, dto.RunRef)
			if !ok {
				t.Fatal("no live session")
			}
			waitFor(t, "native init", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID != "" })
			if cached {
				if got, err := m.runToolCommands(ctx, tenant, dto.RunRef); err != nil || !got.Reported || len(got.Commands) != 1 {
					t.Fatalf("first catalog = %+v, %v", got, err)
				}
				lr.commands.dirty.Store(true)
			}
			route := chi.NewRouteContext()
			route.URLParams.Add("ref", dto.RunRef)
			request := httptest.NewRequest(http.MethodGet, "/runs/"+dto.RunRef+"/commands", nil)
			request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
			response := httptest.NewRecorder()
			m.handleRunCommands(response, request, api.ModuleContext{Tenant: tenant})
			if cached {
				var got toolCommandsDTO
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || !got.Reported || !got.Stale || len(got.Commands) != 1 || got.Commands[0].Name != "kept" {
					t.Fatalf("stale catalog = %d %s", response.Code, response.Body)
				}
			} else if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "output line limit") {
				t.Fatalf("oversized catalog = %d %s, want explicit 502 line-limit error", response.Code, response.Body)
			}
			if err := lr.proc.Send(ctx, []byte(`{"type":"user","text":"continue"}`)); err != nil {
				t.Fatalf("session cannot continue: %v", err)
			}
			waitFor(t, "output after oversized catalog", func() bool {
				for _, frame := range lr.ring.readFrom(0).frames {
					if bytes.Contains(frame.Data, []byte("still alive")) {
						return true
					}
				}
				return false
			})
			if lr.toolEnded() {
				t.Fatal("catalog ended the session")
			}
			if waiting, abandoned := lr.commandWaitCount(); waiting != 0 || abandoned != 0 {
				t.Fatalf("catalog waits = %d/%d", waiting, abandoned)
			}
			for _, frame := range lr.ring.readFrom(0).frames {
				if bytes.Contains(frame.Data, []byte(commandsRequestPrefix)) || bytes.Contains(frame.Data, []byte("padding")) {
					t.Fatal("catalog reached attach output")
				}
			}
		})
	}
}

// launchCodexWithSkills starts the real runtime on a real child that answers skills/list as fixture says.
func launchCodexWithSkills(t *testing.T, fixture codexFixture) (*Module, model.TenantID, runDTO, *liveRun) {
	t.Helper()
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	fixture.ThreadID, fixture.Account = "thread-skills", "apikey"
	setCodexFixture(t, prof, fixture)
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("no live session")
	}
	return m, tenant, dto, lr
}

func assertNoSkillsInAttach(t *testing.T, lr *liveRun) {
	t.Helper()
	for _, frame := range lr.ring.readFrom(0).frames {
		if bytes.Contains(frame.Data, []byte("xxxxxxxx")) || bytes.Contains(frame.Data, []byte("fixture-skill")) {
			t.Fatal("the skills answer reached attach output")
		}
	}
}

// Codex's skills/list answer is as large as the person's skill set. The real
// runtime and a real child prove that a reply over the one-line limit fails the
// read, not the session.
func TestToolCommands_OversizedCodexSkillsKeepNativeSessionAlive(t *testing.T) {
	m, tenant, dto, lr := launchCodexWithSkills(t, codexFixture{SkillsPadBytes: maxOutputLine + 100_000})
	ctx := context.Background()
	pid := int(*dto.PID)
	got, err := m.runToolCommands(ctx, tenant, dto.RunRef)
	if statusOf(err) != http.StatusBadGateway || err.Error() != codexSkillsFailed {
		t.Fatalf("oversized skills = %+v, %v (session ended: %v); want the fixed 502", got, err, lr.toolEnded() || !processRunning(pid))
	}
	if lr.toolEnded() || !processRunning(pid) {
		t.Fatal("an oversized skills answer ended the session")
	}
	// The line was drained to its newline: the next answer is read from a clean stream.
	items, reported, err := lr.session.(*codexSession).commandCatalog(ctx)
	if err != nil || !reported || len(items) != 1 || items[0].Name != "fixture-skill" {
		t.Fatalf("catalog after the oversized answer = %+v %v %v", items, reported, err)
	}
	assertNoSkillsInAttach(t, lr)
}

// A catalog read before is served, marked stale, when the next answer is over the limit.
func TestToolCommands_OversizedCodexSkillsServeTheLastCatalogStale(t *testing.T) {
	m, tenant, dto, lr := launchCodexWithSkills(t, codexFixture{SkillsPadBytes: maxOutputLine + 100_000, SkillsPadRequest: 2})
	ctx := context.Background()
	if got, err := m.runToolCommands(ctx, tenant, dto.RunRef); err != nil || !got.Reported || got.Stale || len(got.Commands) != 1 {
		t.Fatalf("first catalog = %+v, %v", got, err)
	}
	lr.commands.dirty.Store(true)
	got, err := m.runToolCommands(ctx, tenant, dto.RunRef)
	if err != nil || !got.Reported || !got.Stale || len(got.Commands) != 1 || got.Commands[0].Name != "fixture-skill" {
		t.Fatalf("catalog after the oversized answer = %+v, %v; want the last one, stale", got, err)
	}
	if lr.toolEnded() || !processRunning(int(*dto.PID)) {
		t.Fatal("an oversized skills answer ended the session")
	}
	assertNoSkillsInAttach(t, lr)
}

// Recovery is for answers to the engine's own request only: a line over the limit
// under an id nobody issued still ends the session.
func TestToolCommands_OversizedCodexLineOfAnotherIDStillEndsTheSession(t *testing.T) {
	m, tenant, dto, lr := launchCodexWithSkills(t, codexFixture{SkillsPadBytes: maxOutputLine + 100_000, SkillsPadForeignID: true})
	pid := int(*dto.PID)
	// The read ends with the session; what it returns is not the point here.
	_, _ = m.runToolCommands(context.Background(), tenant, dto.RunRef)
	waitFor(t, "the over-long unissued line ended the session", func() bool { return !processRunning(pid) })
	assertNoSkillsInAttach(t, lr)
}

func TestToolCommands_OverflowRecoveryOnlyForIssuedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, prefix            string
		issued, abandoned, want bool
	}{
		{"issued", `{"type":"control_response","response":{"request_id":"olv-commands-owned","subtype":"success","response":{"commands":[`, true, false, true},
		{"abandoned", `{"type":"control_response","response":{"request_id":"olv-commands-owned","response":{"commands":[`, true, true, true},
		{"forged", `{"type":"control_response","response":{"request_id":"olv-commands-owned","response":{"commands":[`, false, false, false},
		{"assistant", `{"type":"assistant","response":{"request_id":"olv-commands-owned","response":{"commands":[`, true, false, false},
		{"nested id", `{"type":"control_response","response":{"response":{"request_id":"olv-commands-owned","commands":[`, true, false, false},
		{"broken id", `{"type":"control_response","response":{"request_id":42,"response":{"commands":[`, true, false, false},
		{"non JSON", `olv-commands-owned`, true, false, false},
		{"duplicate id", `{"type":"control_response","response":{"request_id":"olv-commands-foreign","request_id":"olv-commands-owned","response":{"commands":[`, true, false, false},
		{"duplicate type", `{"type":"assistant","type":"control_response","response":{"request_id":"olv-commands-owned","response":{"commands":[`, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lr := &liveRun{}
			if tc.issued {
				lr.commands.expect("olv-commands-owned")
			}
			if tc.abandoned {
				lr.commands.abandon("olv-commands-owned")
			}
			replacement := lr.commands.oversizedResponse([]byte(tc.prefix))
			if (replacement != nil) != tc.want {
				t.Fatalf("recovery = %v, want %v", replacement != nil, tc.want)
			}
			if tc.want && (!json.Valid(replacement) || !lr.takeCommandsResponse(replacement)) {
				t.Fatal("error did not take the ordinary answer path")
			}
		})
	}
}

func TestToolCommands_StopDuringUnterminatedOversizedAnswer(t *testing.T) {
	script := `#!/bin/sh
IFS= read -r request
printf '%s' '{"type":"control_response","response":{"request_id":"olv-commands-owned","response":{"padding":"'
head -c 1100000 /dev/zero | tr '\000' x
printf '%s\n' 'overflow written' >&2
exec sleep 300
`
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Program: writeOwnedChild(t, script), WaitDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	p := proc.(*procProcess)
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	c := &toolCommandCache{}
	c.expect("olv-commands-owned")
	p.commandResponses.Store(c)
	if err := p.Send(context.Background(), []byte("start")); err != nil {
		t.Fatal(err)
	}
	written := make(chan struct{})
	go func() {
		for f := range p.Output() {
			if f.Stream == streamStderr && bytes.Contains(f.Data, []byte("overflow written")) {
				close(written)
			}
		}
	}()
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("native child did not write the oversized prefix")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		// An unterminated answer may consume the drain bound under contention.
		// The existing Stop contract reports that loss after reaping the child.
		if err != nil && (!errors.Is(err, ErrOutputAbandoned) || errors.Is(err, ErrChildNotReaped) || errors.Is(err, ErrOutputLineTooLong)) {
			t.Fatalf("stop during discard: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unterminated discard blocked Stop")
	}
	if !p.finishedWithin(time.Second) {
		t.Fatal("unterminated discard left the child or pumps running")
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("child collection did not complete: %v", err)
	}
}

func TestToolCommands_CodexOverflowRecoveryOnlyForIssuedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		want         bool
	}{
		{"issued", `{"id":7,"result":{"data":[{"cwd":"/private","skills":[`, true},
		{"issued error reply", `{"id":7,"error":{"code":-1,"message":"`, true},
		{"unissued id", `{"id":8,"result":{"data":[{"cwd":"/private","skills":[`, false},
		{"server request sharing the id", `{"id":7,"method":"item/tool/call","params":{"padding":"`, false},
		{"server request, method first", `{"method":"item/tool/call","id":7,"params":{"padding":"`, false},
		{"notification", `{"method":"skills/changed","params":{"padding":"`, false},
		{"nested id", `{"result":{"id":7,"data":[`, false},
		{"duplicate id", `{"id":7,"id":8,"result":{"data":[`, false},
		{"id inside text", `{"result":{"text":"{\"id\":7}"`, false},
		{"string id", `{"id":"7","result":{"data":[`, false},
		{"non JSON", `7`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCodexPeer(t).session.(*codexSession)
			s.catalogRequests = map[string]struct{}{"7": {}}
			replacement := s.oversizedCatalogResponse([]byte(tc.prefix))
			if (replacement != nil) != tc.want {
				t.Fatalf("recovery = %v, want %v", replacement != nil, tc.want)
			}
			if !tc.want {
				return
			}
			if !json.Valid(replacement) || bytes.Contains(replacement, []byte("private")) || bytes.Contains(replacement, []byte("padding")) {
				t.Fatalf("replacement = %s", replacement)
			}
			if !s.takeCatalogResponse(replacement) {
				t.Fatal("replacement did not take the ordinary catalog answer path")
			}
		})
	}
}

// Installing catalog recovery must not relax the shared stdout cap.
func TestToolCommands_OverflowRecoveryPreservesOtherLineLimits(t *testing.T) {
	c := &toolCommandCache{}
	c.expect("olv-commands-owned")
	for _, prefix := range []string{
		`{"type":"assistant","response":{"request_id":"olv-commands-owned","text":"`,
		`{"type":"control_response","response":{"request_id":"olv-commands-forged","text":"`,
	} {
		input := prefix + strings.Repeat("x", maxOutputLine) + `"}}` + "\n"
		line, err := readBoundedLineWithOverflow(bufio.NewReader(strings.NewReader(input)), maxOutputLine, c.oversizedResponse)
		if len(line) != 0 || !errors.Is(err, ErrOutputLineTooLong) {
			t.Fatalf("unrelated oversized output = %d bytes, %v", len(line), err)
		}
	}
}
