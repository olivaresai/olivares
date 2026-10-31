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
	"os"
	"path/filepath"
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

// A run that never had input has no stored conversation to read: attaching to
// it says nothing, rather than the read failure the issue reported. A run whose
// input state is unknown (older rows) is still read, and still refused here
// because its earlier launch values cannot be proven redacted.
func TestAttachHTTPConversationOfAStoppedRunBeforeItsFirstInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		attempted any
		warns     bool
	}{
		{"never had input", false, false},
		{"input state unknown", nil, true},
		{"had input", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
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
				rec[colRunGitRead] = "approved-binding:owner/repository"
				rec[colRunCodexInputAttempted] = test.attempted
				_, err = repo.Update(t.Context(), rec)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			res := h.do(http.MethodGet, "/v1/m/sessions/runs/"+ref+"/attach?from=0&history=1", admin, tenantHdr(tenant))
			if res.code != http.StatusOK {
				t.Fatalf("attach status=%d %s", res.code, res.raw)
			}
			if warned := strings.Contains(res.raw, "olivares_notice"); warned != test.warns {
				t.Fatalf("history notice=%v; want %v: %s", warned, test.warns, res.raw)
			}
		})
	}
}

type conversationPeer struct {
	*fakeProc
	t         *testing.T
	thread    any
	methods   []string
	readError *rpcError
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
		if request.Params.IncludeTurns && p.readError != nil {
			data, err := json.Marshal(map[string]any{"id": request.ID, "error": p.readError})
			if err != nil {
				return err
			}
			p.out <- OutputFrame{Stream: streamStdout, Data: data}
			return nil
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
		// A package registry token: the stored thread is the tool's own, unredacted.
		{colRunRegistrySecrets: `[{"env":"env/npm","secret":"env/npm"}]`},
		{colRunRegistrySecrets: `invalid`},
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

// fixtureHistoryRunner starts the test binary's Codex peer in place of the tool the
// module resolved, through the real pump and with the launch's own flags. The network
// and confinement of the launch are not under test and need user namespaces.
type fixtureHistoryRunner struct {
	inner Runner
	exe   string
}

func (r fixtureHistoryRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	spec.Program, spec.Args = r.exe, []string{"app-server"}
	spec.NetworkPolicy, spec.Confinement = nil, nil
	return r.inner.Launch(ctx, spec)
}

// The stored history of a conversation that was sent a screenshot, read the way an
// attach asks for it: through the module, the real pump and a real child. The
// answer lists the picture as one line far over the runner's 1 MiB (a 25 MiB image
// here, over the pump's 24 MiB overflow ceiling too), and the reader must neither end
// for it nor hand the picture's bytes to the console.
func TestConversationHistoryOfAThreadWithAnImageIsReadThroughTheRealPump(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m, st, _ := openProfiledRuntime(t, profileBackends(t)[0], WithRunner(fixtureHistoryRunner{inner: NewProcRunner(), exe: exe}), WithProviderDriver(NewCodexDriver()), WithDriverProgram(providerDriverCodex, exe))
	defer st.Close()
	tenant := ensureTenant(t, st, "history-with-image")
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	raw, err := json.Marshal(codexFixture{StoredImageBytes: 25 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.ConfigHome, codexFixtureConfigFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := model.Record{colRunProfileID: profile.Ref, colRunProfileDriver: profile.Driver, colRunProfileEnvRef: profile.EnvironmentRef, colRunProfileConfigHome: profile.ConfigHome, colRunProfileUserHome: profile.UserHome, colClaudeSessionID: "fixture-thread"}
	line, err := m.conversationHistory(t.Context(), tenant, rec)
	if err != nil {
		t.Fatalf("a thread that holds a 25 MiB image could not be read: %v", err)
	}
	var history struct {
		Turns []struct {
			ID    string `json:"id"`
			Items []struct {
				Text    string `json:"text"`
				Content []struct {
					Text string `json:"text"`
					URL  string `json:"url"`
				} `json:"content"`
			} `json:"items"`
		} `json:"turns"`
	}
	if err := json.Unmarshal([]byte(line), &history); err != nil || len(history.Turns) != 2 {
		t.Fatalf("history = %.300s (%v)", line, err)
	}
	first := history.Turns[0].Items[0].Content
	if first[0].Text != "what is in this picture" || first[1].URL != "data:image/png;base64," || history.Turns[1].Items[0].Text != "a cat on a sofa" || len(line) > 4096 {
		t.Fatalf("history = %.400s (%d bytes)", line, len(line))
	}
}

func TestConversationHistoryReadFailureUsesUserWords(t *testing.T) {
	peer := &conversationPeer{fakeProc: &fakeProc{out: make(chan OutputFrame, 4), stopped: make(chan struct{})}, t: t, readError: &rpcError{Code: -32603, Message: "private read error"}}
	m, _, _ := openProfiledRuntime(t, profileBackends(t)[0], WithRunner(&pinnedHistoryRunner{proc: peer}), WithProviderDriver(NewCodexDriver()))
	tenant := model.TenantID(model.NewID())
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	rec := model.Record{colRunProfileID: profile.Ref, colRunProfileDriver: profile.Driver, colRunProfileEnvRef: profile.EnvironmentRef, colRunProfileConfigHome: profile.ConfigHome, colRunProfileUserHome: profile.UserHome, colClaudeSessionID: "root"}
	line, err := m.conversationHistory(t.Context(), tenant, rec)
	if line != "" || err == nil || err.Error() != "the saved conversation could not be read; live output is still available while the session is running" {
		t.Fatalf("line=%q error=%v", line, err)
	}
}

func TestConversationHistoryRequiresProofBeforeTreatingMissingHistoryAsEmpty(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		empty bool
	}{
		{"never attempted input", false, true},
		{"attempted input", true, false},
		{"legacy unknown", nil, false},
		{"invalid proof", "false", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(WithRunner(&fakeRunner{}), WithProviderDriver(NewCodexDriver()))
			rec := model.Record{colRunProfileDriver: providerDriverCodex, colClaudeSessionID: "root", colRunCodexInputAttempted: test.value}
			line, err := m.conversationHistory(t.Context(), model.TenantID(model.NewID()), rec)
			if line != "" || (err == nil) != test.empty {
				t.Fatalf("line=%q error=%v; empty=%v", line, err, test.empty)
			}
		})
	}
}

func TestCodexConversationInputAttemptIsDurable(t *testing.T) {
	m, _, tenant, profile := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, profile, codexFixture{ThreadID: "durable-input", Account: "apikey"})
	run, err := codexLaunch(t, m, tenant, profile)
	if err != nil {
		t.Fatal(err)
	}
	rec := codexRunRecord(t, m, tenant, run.RunRef)
	if value, ok := rec[colRunCodexInputAttempted].(bool); !ok || value {
		t.Fatalf("new run input fact=%v", rec[colRunCodexInputAttempted])
	}
	if err := m.sendTextInput(t.Context(), tenant, run.RunRef, "first message"); err != nil {
		t.Fatal(err)
	}
	if rec := codexRunRecord(t, m, tenant, run.RunRef); !rec.Bool(colRunCodexInputAttempted) {
		t.Fatal("input attempt not persisted")
	}
}

func TestCodexConversationInputRefusesFailedProofWrite(t *testing.T) {
	m, _, tenant, profile := codexHarness(t, AuthSourceAccountHome)
	fault := &failNthMutateData{inner: m.Data, err: errors.New("input proof persistence unavailable")}
	m.UseData(fault)
	path := setCodexFixture(t, profile, codexFixture{ThreadID: "failed-input-proof", Account: "apikey"})
	run, err := codexLaunch(t, m, tenant, profile)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := m.rt.getLive(tenant, run.RunRef)
	fault.mu.Lock()
	fault.calls, fault.failAt = 0, 2
	fault.mu.Unlock()
	attempted, err := m.driverInputImages(t.Context(), live, "must not be sent", nil)

	if attempted || err == nil {
		t.Fatalf("attempted=%v error=%v", attempted, err)
	}
	for _, method := range readFixtureRecord(t, path).Methods {
		if method == "turn/start" || method == "turn/steer" {
			t.Fatal("input crossed a failed durable proof write")
		}
	}
	if rec := codexRunRecord(t, m, tenant, run.RunRef); rec.Bool(colRunCodexInputAttempted) {
		t.Fatal("failed write changed input proof")
	}
}

type beforeCodexInputRegistry struct{ store.ExtensionRegistry }

func (r beforeCodexInputRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == runKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, field := range d.Fields {
			if field.Name != colRunCodexInputAttempted {
				fields = append(fields, field)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func TestCodexInputAttemptNullableUpgradeBothBackends(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			old := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error { return old.RegisterSchema(beforeCodexInputRegistry{reg}) })
			tenant := ensureTenant(t, st, "codex-input-upgrade")
			ref := model.NewID().String()
			if err := m.Data.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(t.Context(), model.Record{colRunRef: ref, colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateStopped, colLastEventSeq: int64(0)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			if err := m.Data.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := findRunRec(t.Context(), repo, ref)
				if err != nil {
					return err
				}
				if !rec.IsNull(colRunCodexInputAttempted) {
					t.Fatal("legacy input state was backfilled")
				}
				rec[colRunCodexInputAttempted] = true
				_, err = repo.Update(t.Context(), rec)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rec, err := m.loadRun(t.Context(), tenant, ref)
			if err != nil || !rec.Bool(colRunCodexInputAttempted) {
				t.Fatalf("upgraded input fact did not persist: %v", err)
			}
		})
	}
}
