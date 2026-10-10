// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Runs the real CLI through the production runner with a synthetic provider.
// Neither the caller's home nor any vendor account is exposed to the child.
func TestRealClaudeBoundNetworkNamespace(t *testing.T) {
	bin := os.Getenv("OLIVARES_TEST_CLAUDE_BIN")
	if bin == "" {
		t.Skip("OLIVARES_TEST_CLAUDE_BIN is not set")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	if _, err := launchBound(m, tenant, prof); err != nil {
		t.Fatal(err)
	}
	spec := runner.lastSpec()
	provider := newFakeAnthropic(t)
	endpoint := provider.url
	if external := os.Getenv("OLIVARES_TEST_CLAUDE_TLS_ENDPOINT"); external != "" {
		endpoint = external
	}
	if spec.NetworkPolicy == nil {
		t.Fatal("a record-bound Claude launch is not network-confined")
	}
	spec.Program, spec.Dir, spec.BoundProvider.Endpoint = bin, t.TempDir(), endpoint
	spec.NetworkPolicy.Providers = []string{endpoint}
	spec.Confinement = &confine.Policy{ReadWrite: []string{spec.Dir, configHome, userHome}, ReadOnly: []string{filepath.Dir(bin)}}
	spec.ConfinementRequired = true
	for i := range spec.Env {
		if spec.Env[i].Name == "ANTHROPIC_BASE_URL" {
			spec.Env[i].Value = endpoint
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	p, err := NewProcRunner().Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	msg := []byte(`{"type":"user","message":{"role":"user","content":"Say hi."}}`)
	if err := p.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal("real CLI did not complete the synthetic turn")
		case frame, ok := <-p.Output():
			if !ok {
				code, err := p.Wait()
				t.Fatalf("real CLI exited before answering: %d %v", code, err)
			}
			var event struct {
				Type    string `json:"type"`
				IsError bool   `json:"is_error"`
			}
			if json.Unmarshal(frame.Data, &event) == nil && event.Type == "result" {
				if event.IsError || endpoint == provider.url && provider.count() == 0 {
					t.Fatal("real CLI did not use the bound provider")
				}
				if endpoint == provider.url {
					t.Logf("real Claude completed a turn through the namespace; bound requests=%d", provider.count())
				} else {
					t.Log("real Claude completed the externally counted TLS turn through the namespace")
				}
				return
			}
		}
	}
}

// The engine trusts the synthetic provider; Claude trusts only the distinct
// per-session proxy certificate injected by the runner. No vendor calls/keys.
func TestRealClaudeTLSBoundNetworkNamespace(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_CLAUDE_BIN") == "" {
		t.Skip("OLIVARES_TEST_CLAUDE_BIN is not set")
	}
	provider := newFakeAnthropic(t)
	target, err := url.Parse(provider.url)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRealClaudeBoundNetworkNamespace$", "-test.v")
	cmd.Env = append(os.Environ(), "OLIVARES_TEST_CLAUDE_TLS_ENDPOINT="+server.URL, "SSL_CERT_FILE="+root, "SSL_CERT_DIR="+filepath.Dir(root))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real Claude TLS turn: %v\n%s", err, output)
	}
	if provider.count() == 0 {
		t.Fatal("bound TLS provider received no request")
	}
	t.Logf("real Claude completed verified TLS through the namespace; bound requests=%d", provider.count())
}

// THE REAL CLAUDE CODE ON A KEY, SAVED SETTINGS AGAINST IT, ZERO REQUESTS TO ANY OTHER HOST.
//
// It takes the argv and environment the product builds for a launch bound to an Anthropic key
// (a real createRun, captured by the fake runner) and runs them with the installed Claude Code
// (OLIVARES_TEST_CLAUDE_BIN; 2.1.288 is the version the product installs), from a profile whose
// saved user settings clear both quiet switches. Only the endpoint's
// value is changed, to a loopback Anthropic API: up (it answers), then down (a closed port) for
// a resume and a fresh start. Every host is counted as in
// TestRealOpenCodeBoundSessionReachesNoOtherHost: a refusing proxy and the process tree's sockets.
func TestRealClaudeKeySessionReachesNoOtherHost(t *testing.T) {
	bin := os.Getenv("OLIVARES_TEST_CLAUDE_BIN")
	if bin == "" {
		t.Skip("OLIVARES_TEST_CLAUDE_BIN is not set: this regression runs the installed Claude Code")
	}
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	if _, err := launchBound(m, tenant, prof); err != nil {
		t.Fatalf("launch: %v", err)
	}
	spec := runner.lastSpec()
	saved := `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"","CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL":""}}`
	if err := os.WriteFile(filepath.Join(configHome, "settings.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := newRefusingProxy(t)
	up := newFakeAnthropic(t)
	down := "http://" + closedLoopbackPort(t)
	direct := map[string]bool{}
	sid := runRealClaude(t, bin, spec, up.url, "", proxy, direct)
	if up.count() == 0 || sid == "" {
		t.Fatalf("the answering endpoint got %d requests, session %q", up.count(), sid)
	}
	runRealClaude(t, bin, spec, down, sid, proxy, direct)
	runRealClaude(t, bin, spec, down, "", proxy, direct)
	t.Logf("bound endpoint answered %d requests; proxied %v; direct %v", up.count(), proxy.hosts(), direct)
	for host, n := range proxy.hosts() {
		t.Errorf("%d request(s) to %s, a host other than the bound endpoint", n, host)
	}
	for addr := range direct {
		t.Errorf("a direct connection to %s (not loopback, not through the proxy)", addr)
	}
}

// The governed hook launch (one settings file, no other setting sources), real Claude Code:
// a key-bound session, endpoint up then down, start then resume, reaches only its endpoint.
// It does not exercise managed settings: Claude Code reads those only from the system
// directory, which a test cannot write (SR2C 071).
func TestRealClaudeHookLaunchReachesOnlyItsEndpoint(t *testing.T) {
	bin := os.Getenv("OLIVARES_TEST_CLAUDE_BIN")
	if bin == "" {
		t.Skip("OLIVARES_TEST_CLAUDE_BIN is not set: this regression runs the installed Claude Code")
	}
	useClaudeManagedSettingsDir(t, t.TempDir())
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	if _, err := launchBound(m, tenant, prof); err != nil {
		t.Fatalf("launch: %v", err)
	}
	spec := runner.lastSpec()
	spec.Env = append(spec.Env, EnvVar{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:1/"}, EnvVar{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "test-only"})
	if err := ConfigureClaudeHookPEP(&spec, t.TempDir(), "run-real", "/opt/olivares"); err != nil {
		t.Fatal(err)
	}
	proxy, up, direct := newRefusingProxy(t), newFakeAnthropic(t), map[string]bool{}
	sid := runRealClaude(t, bin, spec, up.url, "", proxy, direct)
	runRealClaude(t, bin, spec, "http://"+closedLoopbackPort(t), sid, proxy, direct)
	t.Logf("bound endpoint answered %d; proxied %v; direct %v", up.count(), proxy.hosts(), direct)
	if up.count() == 0 || len(proxy.hosts()) != 0 || len(direct) != 0 {
		t.Fatalf("answered %d, proxied %v, direct %v; want the bound endpoint only", up.count(), proxy.hosts(), direct)
	}
}

// runRealClaude runs the product's launch spec with the installed Claude Code for one turn (a
// resume when sid is set), the endpoint's value set to endpoint, waits for the background work
// after it, and returns the session id Claude Code reported.
func runRealClaude(t *testing.T, bin string, spec LaunchSpec, endpoint, sid string, proxy *refusingProxy, direct map[string]bool) string {
	t.Helper()
	dir := spec.Dir
	if dir == "" {
		dir = t.TempDir()
	}
	args := append([]string(nil), spec.Args...)
	if sid != "" {
		args = append(args, "--resume", sid)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Stderr = dir, io.Discard
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost"}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		cmd.Env = append(cmd.Env, name+"="+proxy.url)
	}
	for _, item := range spec.Env {
		if item.Name == "ANTHROPIC_BASE_URL" {
			item.Value = endpoint
		}
		cmd.Env = append(cmd.Env, item.Name+"="+item.Value)
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Claude Code: %v", err)
	}
	done := make(chan struct{})
	var mu sync.Mutex
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			for addr := range processTreeRemotes(cmd.Process.Pid) {
				mu.Lock()
				direct[addr] = true
				mu.Unlock()
			}
		}
	}()
	msg, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Say hi."}})
	_, _ = stdin.Write(append(msg, '\n'))
	result := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			frame := map[string]any{}
			if json.Unmarshal(sc.Bytes(), &frame) == nil && frame["type"] == "result" {
				id, _ := frame["session_id"].(string)
				result <- id
				return
			}
		}
		result <- ""
	}()
	var got string
	select {
	case got = <-result:
	case <-time.After(120 * time.Second):
	}
	_ = stdin.Close()
	time.Sleep(12 * time.Second) // the background work after a turn
	close(done)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return got
}

// fakeAnthropic is a loopback Anthropic Messages API that answers "hi".
type fakeAnthropic struct {
	url string
	mu  sync.Mutex
	n   int
}

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.n++
		f.mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/v1/messages") || strings.HasPrefix(r.URL.Path, "/v1/messages/count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"input_tokens":1}`)
			return
		}
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		msg := map[string]any{"id": "msg_fh", "type": "message", "role": "assistant", "model": body.Model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
		if !body.Stream {
			msg["content"], msg["stop_reason"] = []any{map[string]string{"type": "text", "text": "hi"}}, "end_turn"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(msg)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []struct {
			name string
			data any
		}{
			{"message_start", map[string]any{"type": "message_start", "message": msg}},
			{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}}},
			{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "hi"}}},
			{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
			{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}}},
			{"message_stop", map[string]any{"type": "message_stop"}},
		} {
			raw, _ := json.Marshal(ev.data)
			_, _ = io.WriteString(w, "event: "+ev.name+"\ndata: "+string(raw)+"\n\n")
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	f.url = "http://" + l.Addr().String()
	return f
}

func (f *fakeAnthropic) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}
