// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAttachHTTPConversationHistoryRequiresFreshOptIn(t *testing.T) {
	h, _, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	if _, err := h.m.stopRun(t.Context(), tenant, ref, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(t.Context(), repo, ref)
		if err != nil {
			return err
		}
		rec[colRunProfileDriver], rec[colClaudeSessionID] = providerDriverCodex, "root"
		// A history refusal is still a history event. It must not delay or
		// add events to a legacy attach, and must remain explicit for opt-in.
		rec[colRunGitRead] = "approved-binding:owner/repository"
		_, err = repo.Update(t.Context(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?from=0", "?from=0&history=0", "?from=0&history=true", "?from=1&history=1", "?from=0&history=1"} {
		res := h.do(http.MethodGet, "/v1/m/sessions/runs/"+ref+"/attach"+query, admin, tenantHdr(tenant))
		wantHistory := query == "?from=0&history=1"
		if res.code != http.StatusOK || strings.Contains(res.raw, "event: history") != wantHistory {
			t.Errorf("attach %q status=%d history=%v; want history=%v", query, res.code, strings.Contains(res.raw, "event: history"), wantHistory)
		}
		if wantHistory && !strings.Contains(res.raw, "historical secret redaction cannot be proven") {
			t.Error("opted-in history lost its explicit redaction refusal")
		}
	}
}

type conversationPeer struct {
	*fakeProc
	t       *testing.T
	thread  any
	methods []string
}

func (p *conversationPeer) Send(_ context.Context, line []byte) error {
	var request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			ThreadID     string `json:"threadId"`
			IncludeTurns bool   `json:"includeTurns"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		return err
	}
	p.methods = append(p.methods, request.Method)
	var result any = map[string]any{}
	switch request.Method {
	case "initialize":
	case "initialized":
		return nil
	case "thread/read":
		if request.Params.ThreadID != "root" || !request.Params.IncludeTurns {
			p.t.Error("reader changed thread identity or omitted turns")
		}
		result = map[string]any{"thread": p.thread}
	default:
		p.t.Errorf("conversation reader sent mutating or authentication method %q", request.Method)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false) // Native JSON does not use Go's HTML expansion.
	if err := encoder.Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
		return err
	}
	p.out <- OutputFrame{Stream: streamStdout, Data: data.Bytes()}
	return nil
}

func TestCodexConversationReaderRefusesUnprovenOrPartialHistory(t *testing.T) {
	for _, test := range []struct {
		name    string
		thread  map[string]any
		wantErr bool
	}{
		{"root", map[string]any{"id": "root", "turns": []any{}, "path": "private-thread-path", "provider": "private-provider"}, false},
		{"wrong thread", map[string]any{"id": "foreign", "turns": []any{}}, true},
		{"subagent", map[string]any{"id": "root", "parentThreadId": "parent", "turns": []any{}}, true},
		{"agent role", map[string]any{"id": "root", "agentRole": "worker", "turns": []any{}}, true},
		{"no turns", map[string]any{"id": "root"}, true},
		{"summary only", map[string]any{"id": "root", "turns": []any{map[string]any{"id": "turn", "items": []any{}, "itemsView": "summary"}}}, true},
		{"oversize", map[string]any{"id": "root", "turns": []any{}, "extra": strings.Repeat("x", maxOutputLine)}, true},
		{"HTML expansion", map[string]any{"id": "root", "turns": []any{map[string]any{"id": "turn", "items": []any{map[string]any{"type": "agentMessage", "text": strings.Repeat("<", maxOutputLine/5)}}}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := &conversationPeer{fakeProc: &fakeProc{out: make(chan OutputFrame, 4), stopped: make(chan struct{})}, t: t, thread: test.thread}
			line, err := readCodexConversation(t.Context(), peer, "root")
			if (err != nil) != test.wantErr {
				t.Fatalf("reader err=%v; want error=%v", err, test.wantErr)
			}
			if strings.Contains(line, "private-") {
				t.Fatal("reader disclosed thread metadata")
			}
			if !reflect.DeepEqual(peer.methods, []string{"initialize", "initialized", "thread/read"}) {
				t.Fatalf("methods=%v", peer.methods)
			}
		})
	}
}

func TestConversationHistoryDoesNotReadSecretsOrUnprovenHomes(t *testing.T) {
	runner := &fakeRunner{}
	m := New(WithRunner(runner), WithProviderDriver(NewCodexDriver()))
	for _, rec := range []model.Record{
		{colRunSecretEnv: `[{"env":"SECRET","secret":"env/old"}]`},
		{colRunSecretEnv: `invalid`},
		{colRunGitRead: "approved-binding:owner/repository"},
	} {
		rec[colRunProfileDriver], rec[colClaudeSessionID] = providerDriverCodex, "root"
		line, err := m.conversationHistory(t.Context(), model.TenantID(model.NewID()), rec)
		if err == nil || line != "" || !strings.Contains(err.Error(), "redaction") {
			t.Fatalf("line=%q err=%v", line, err)
		}
	}
	line, err := m.conversationHistory(t.Context(), model.TenantID(model.NewID()), model.Record{
		colRunProfileDriver: providerDriverCodex, colClaudeSessionID: "root",
	})
	if err == nil || line != "" {
		t.Fatalf("unproven profile read: line=%q err=%v", line, err)
	}
	if len(runner.procs) != 0 {
		t.Fatal("reader spawned before secret/home proof")
	}
}

// This uses the real scoped profile/pin store and the native protocol reader;
// the peer blocks Stop so removal must refuse for the entire reader lifetime.
type pinnedHistoryRunner struct {
	proc    Process
	program string
	err     error
}

func (r *pinnedHistoryRunner) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	r.program = spec.Program
	return r.proc, r.err
}

type stoppingConversationPeer struct {
	*conversationPeer
	entered, release chan struct{}
}

func (p *stoppingConversationPeer) Stop(ctx context.Context) error {
	close(p.entered)
	select {
	case <-p.release:
		return p.fakeProc.Stop(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestConversationHistoryUsesPinAndHoldsRemovalUntilCollected(t *testing.T) {
	peer := &stoppingConversationPeer{conversationPeer: &conversationPeer{fakeProc: &fakeProc{out: make(chan OutputFrame, 4), stopped: make(chan struct{})}, t: t, thread: map[string]any{"id": "root", "turns": []any{}}}, entered: make(chan struct{}), release: make(chan struct{})}
	runner := &pinnedHistoryRunner{proc: peer}
	m, st, _ := openProfiledRuntime(t, profileBackends(t)[0], WithRunner(runner), WithProviderDriver(NewCodexDriver()), WithProgramResolver(func(string) string { return "/latest/codex" }, func(_ context.Context, driver, version string) (string, error) {
		return "/pinned/" + driver + "/" + version, nil
	}))
	defer st.Close()
	tenant := ensureTenant(t, st, "pinned-history")
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{Name: "History", RootPath: t.TempDir(), Actor: "admin", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetToolVersionPin(t.Context(), tenant, ToolVersionPin{Driver: "codex", WorkspaceRef: ws.WorkspaceRef, Version: "0.1"}, "admin", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	rec := model.Record{colRunProfileID: profile.Ref, colRunProfileDriver: profile.Driver, colRunProfileEnvRef: profile.EnvironmentRef, colRunProfileConfigHome: profile.ConfigHome, colRunProfileUserHome: profile.UserHome, colClaudeSessionID: "root", colWorkspaceRef: ws.WorkspaceRef}
	done := make(chan error, 1)
	go func() { _, err := m.conversationHistory(t.Context(), tenant, rec); done <- err }()
	select {
	case <-peer.entered:
	case err := <-done:
		t.Fatalf("reader returned before stop: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not reach stop")
	}
	if runner.program != "/pinned/codex/0.1" {
		t.Errorf("history program=%s", runner.program)
	}
	if unlock, err := m.LockUnusedTool("/pinned/codex/0.1"); !errors.Is(err, ErrToolInUse) {
		if unlock != nil {
			unlock()
		}
		t.Errorf("removal admitted before reader collected: %v", err)
	}
	close(peer.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	unlock, err := m.LockUnusedTool("/pinned/codex/0.1")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

type historyCustodyPeer struct {
	Process
	stopErr, waitErr                  error
	stopped, waiting, release, waited chan struct{}
}

func (p *historyCustodyPeer) Stop(context.Context) error {
	close(p.stopped)
	return p.stopErr
}

func (p *historyCustodyPeer) Wait() (int, error) {
	close(p.waiting)
	<-p.release
	close(p.waited)
	return 0, p.waitErr
}

func TestConversationHistoryPartialLaunchKeepsUnconfirmedCustody(t *testing.T) {
	for _, tc := range []struct {
		name             string
		stopErr, waitErr error
	}{
		{"collected with abandoned output", ErrOutputAbandoned, nil},
		{"later collected", ErrChildNotReaped, nil},
		{"wait remains unconfirmed", ErrChildNotReaped, ErrChildNotReaped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &historyCustodyPeer{stopErr: tc.stopErr, waitErr: tc.waitErr, stopped: make(chan struct{}), waiting: make(chan struct{}), release: make(chan struct{}), waited: make(chan struct{})}
			defer func() {
				select {
				case <-peer.release:
				default:
					close(peer.release)
				}
			}()
			runner := &pinnedHistoryRunner{proc: peer, err: errors.New("partial launch")}
			m, st, _ := openProfiledRuntime(t, profileBackends(t)[0], WithRunner(runner), WithProviderDriver(NewCodexDriver()))
			defer st.Close()
			logs := &runtimeObserverLogs{}
			m.log = slog.New(logs)
			tenant := ensureTenant(t, st, "history-custody")
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
			rec := model.Record{colRunProfileID: profile.Ref, colRunProfileDriver: profile.Driver, colRunProfileEnvRef: profile.EnvironmentRef, colRunProfileConfigHome: profile.ConfigHome, colRunProfileUserHome: profile.UserHome, colClaudeSessionID: "root"}
			if line, err := m.conversationHistory(t.Context(), tenant, rec); err == nil || line != "" {
				t.Fatalf("partial launch history=%q err=%v", line, err)
			}
			select {
			case <-peer.stopped:
			default:
				t.Fatal("partial launch process escaped Stop")
			}
			if childWasReaped(tc.stopErr) {
				unlock, err := m.LockUnusedTool("codex")
				if err != nil {
					t.Fatal(err)
				}
				unlock()
				return
			}
			select {
			case <-peer.waiting:
			case <-time.After(time.Second):
				t.Fatal("uncollected reader escaped Wait")
			}
			unlock, err := m.LockUnusedTool("codex")
			if unlock != nil {
				unlock()
			}
			if !errors.Is(err, ErrToolInUse) {
				t.Fatalf("unconfirmed custody admitted removal: %v", err)
			}
			close(peer.release)
			<-peer.waited
			if !childWasReaped(tc.waitErr) {
				// Observe the collector's decision before asserting its retained
				// hold; Wait returning alone races that decision.
				for deadline := time.Now().Add(time.Second); ; {
					logs.mu.Lock()
					observed := false
					for _, record := range logs.records {
						observed = observed || record.Message == "stored conversation reader collection remains unconfirmed"
					}
					logs.mu.Unlock()
					if observed {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("unconfirmed reader collection was not retained and reported")
					}
					time.Sleep(time.Millisecond)
				}
				unlock, err := m.LockUnusedTool("codex")
				if unlock != nil {
					unlock()
				}
				if !errors.Is(err, ErrToolInUse) {
					t.Fatalf("unconfirmed Wait admitted removal: %v", err)
				}
				// A refused removal must not queue a writer and freeze new launches.
				if !m.rt.toolMu.TryRLock() {
					t.Fatal("new launch admission blocked")
				}
				m.rt.toolMu.RUnlock()
				return
			}
			deadline := time.Now().Add(time.Second)
			for {
				unlock, err := m.LockUnusedTool("codex")
				if err == nil {
					unlock()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("confirmed Wait did not release admission")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
