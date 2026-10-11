// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type conversationRecordingRunner struct {
	Runner
	mu    sync.Mutex
	specs []LaunchSpec
}

type delayedConversationRunner struct {
	Runner
	reads atomic.Int64
}

func (r *delayedConversationRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.NetworkPolicy != nil && spec.NetworkPolicy.Offline {
		r.reads.Add(1)
		select {
		case <-time.After(750 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.Runner.Launch(ctx, spec)
}

// Exercise the actual non-detached CLI against the module HTTP API and a native
// tool. A slow native history reader must not move an old completed turn into
// the reply to newly accepted input.
func TestCodexNativeCLISendWithDelayedConversationHistory(t *testing.T) {
	native, cli := os.Getenv("OLIVARES_TEST_CODEX_PROGRAM"), os.Getenv("OLIVARES_TEST_CLI_PROGRAM")
	if native == "" || cli == "" {
		t.Skip("set OLIVARES_TEST_CODEX_PROGRAM and OLIVARES_TEST_CLI_PROGRAM to native executables")
	}
	if !filepath.IsAbs(native) || !filepath.IsAbs(cli) {
		t.Fatal("native executable paths must be absolute")
	}
	var requests atomic.Int64
	fixture := codexPrivacyEndpoint(t, &requests, nil)
	var newReply atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		fixture.Config.Handler.ServeHTTP(recorder, r)
		answer := "OLD REPLY"
		if newReply.Load() {
			answer = "NEW REPLY"
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write([]byte(strings.ReplaceAll(recorder.Body.String(), "fixture answer", answer)))
	}))
	t.Cleanup(endpoint.Close)
	runner := &delayedConversationRunner{Runner: NewProcRunner()}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithConfinement([]string{t.TempDir()}, true),
		WithRunner(runner), WithProviderDriver(NewCodexDriver()), WithDriverProgram(providerDriverCodex, native),
		WithProviderSecretVault(newFakeVault()), WithProviderProbe(&fakeProbe{}),
		WithDriverTimeouts(20*time.Second, 2*time.Second))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "delayed-history-cli")
	m.UseExecutionEnvironmentRef(testEnvRef)
	record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, DisplayName: "Labelled loopback CLI provider",
		BaseURL: endpoint.URL + "/v1", APIKey: testProviderKey,
	})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "Delayed history CLI", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		ProviderProfileRef: profile.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, Model: "fixture-model",
		PermissionMode: permModeBypass, MayRunUnrestricted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.sendTextInput(ctx, tenant, run.RunRef, "old prompt"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "old native turn completed", func() bool {
		lr, ok := m.rt.getLive(tenant, run.RunRef)
		return ok && lr.session.ActiveTurn() == ""
	})
	newReply.Store(true)
	server := httptest.NewServer(h.srv.Handler())
	t.Cleanup(server.Close)
	command := exec.CommandContext(ctx, cli, "session", "send", run.RunRef, "new prompt")
	// The test bearer stays in the child environment, never argv or diagnostics.
	command.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "TERM=dumb",
		"OLIVARES_SERVER_URL=" + server.URL, "OLIVARES_TOKEN=" + admin, "OLIVARES_TENANT=" + string(tenant)}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "NEW REPLY") || strings.Contains(string(output), "OLD REPLY") {
		t.Fatalf("CLI send returned err=%v output=%q; want only the new turn's reply", err, output)
	}
	if runner.reads.Load() != 0 {
		t.Fatal("legacy CLI attach launched a native history reader")
	}
}

func (r *conversationRecordingRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.Runner.Launch(ctx, spec)
}

// Native Codex owns the persisted conversation. Only the Responses provider is
// a labelled loopback stand-in; no fake tool or fabricated transcript is used.
func TestCodexNativeConversationAfterStoppedHandleIsGone(t *testing.T) {
	native := os.Getenv("OLIVARES_TEST_CODEX_PROGRAM")
	if native == "" {
		t.Skip("set OLIVARES_TEST_CODEX_PROGRAM to the native Codex executable")
	}
	if !filepath.IsAbs(native) {
		t.Fatal("native Codex path must be absolute")
	}
	var requests atomic.Int64
	runner := &conversationRecordingRunner{Runner: NewProcRunner()}
	endpoint := codexPrivacyEndpoint(t, &requests, nil)
	m := New(WithSessionWorkspaceRoot(t.TempDir()),
		WithConfinement([]string{t.TempDir()}, true),
		WithRunner(runner), WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, native),
		WithProviderSecretVault(newFakeVault()), WithProviderProbe(&fakeProbe{}),
		WithDriverTimeouts(20*time.Second, 2*time.Second))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "native-history")
	m.UseExecutionEnvironmentRef(testEnvRef)
	record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, DisplayName: "Labelled loopback history provider",
		BaseURL: endpoint.URL + "/v1", APIKey: testProviderKey,
	})
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "Native conversation", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		ProviderProfileRef: profile.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, Model: "fixture-model",
		PermissionMode: permModeBypass, MayRunUnrestricted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"initial CLI prompt", "earlier console prompt"} {
		if err := m.sendTextInput(ctx, tenant, run.RunRef, prompt); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "native turn finished", func() bool {
			lr, ok := m.rt.getLive(tenant, run.RunRef)
			return ok && lr.session.ActiveTurn() == ""
		})
	}
	lr, _ := m.rt.getLive(tenant, run.RunRef)
	if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if captures := os.Getenv("OLIVARES_TEST_CONVERSATION_CAPTURE"); captures != "" {
		var lines []string
		for _, frame := range lr.ring.readFrom(0).frames {
			lines = append(lines, string(frame.Data))
		}
		if err := os.WriteFile(captures+".attach", []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !m.rt.dropLiveIf(lr) {
		t.Fatal("could not reclaim stopped handle")
	}
	// Same persistent store, no live handle: the state after engine restart or
	// closed-ring expiry. A new browser has no in-memory sent-turn notes either.
	beforeRead := requests.Load()
	foreign := h.createOrg(admin, "foreign-history")
	foreignRes := h.do(http.MethodGet, "/v1/m/sessions/runs/"+run.RunRef+"/attach?from=0&history=1", admin, tenantHdr(foreign))
	if foreignRes.code != http.StatusNotFound {
		t.Fatalf("foreign history attach status=%d", foreignRes.code)
	}
	unauthenticated := h.do(http.MethodGet, "/v1/m/sessions/runs/"+run.RunRef+"/attach?from=0&history=1", "", tenantHdr(tenant))
	if unauthenticated.code != http.StatusUnauthorized {
		t.Fatalf("anonymous history attach status=%d", unauthenticated.code)
	}
	runner.mu.Lock()
	launchesBeforeRead := len(runner.specs)
	runner.mu.Unlock()
	for _, query := range []string{"from=0", "from=0&history=0", "from=0&history=true", "from=1&history=1"} {
		res := h.do(http.MethodGet, "/v1/m/sessions/runs/"+run.RunRef+"/attach?"+query, admin, tenantHdr(tenant))
		if res.code != http.StatusOK || strings.Contains(res.raw, "event: history") {
			t.Fatalf("attach without fresh history opt-in (%s)=%d %s", query, res.code, res.raw)
		}
	}
	runner.mu.Lock()
	launchesWithoutHistory := len(runner.specs)
	runner.mu.Unlock()
	if launchesWithoutHistory != launchesBeforeRead {
		t.Fatal("attach without fresh history opt-in launched a native reader")
	}
	res := h.do(http.MethodGet, "/v1/m/sessions/runs/"+run.RunRef+"/attach?from=0&history=1", admin, tenantHdr(tenant))
	if res.code != http.StatusOK {
		t.Fatalf("attach=%d %s", res.code, res.raw)
	}
	var history string
	for _, event := range parseSSE(res.raw) {
		if event.Event == "history" {
			var payload struct {
				Line string `json:"line"`
			}
			if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
				t.Fatal(err)
			}
			history = payload.Line
		}
	}
	for _, text := range []string{"initial CLI prompt", "earlier console prompt", "fixture answer"} {
		if !strings.Contains(history, text) {
			t.Errorf("stored native history lost %q", text)
		}
	}
	if requests.Load() != beforeRead {
		t.Error("history read contacted the model provider")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.specs) != launchesBeforeRead+1 {
		t.Fatalf("reader launch count=%d expected=%d", len(runner.specs), launchesBeforeRead+1)
	}
	spec := runner.specs[len(runner.specs)-1]
	if spec.NetworkPolicy == nil || !spec.NetworkPolicy.Offline || len(spec.NetworkPolicy.Providers)+len(spec.NetworkPolicy.Controls) != 0 {
		t.Fatal("history reader did not deny all network destinations")
	}
	if spec.Confinement == nil {
		t.Fatal("history reader omitted filesystem confinement")
	}
	for _, variable := range spec.Env {
		if variable.Name != "HOME" && variable.Name != "CODEX_HOME" {
			t.Errorf("history reader received extra variable %s", variable.Name)
		}
	}
	if !strings.Contains(res.raw, `"io_unavailable":"not_live_on_node"`) || strings.Contains(res.raw, "event: end") {
		t.Fatal("stored history changed unavailable I/O into a process end")
	}
	if captures := os.Getenv("OLIVARES_TEST_CONVERSATION_CAPTURE"); captures != "" && history != "" {
		if err := os.WriteFile(captures, []byte(history+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
