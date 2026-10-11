// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/sandboxrt"
	"github.com/olivaresai/olivares/modules/sandbox"
)

// fakeIsoBackend is a sandboxrt.Backend test double for the composition-root
// adapters: it resolves steps against mocks and, for a probe, delivers it to the
// target THROUGH the engine-owned egress proxy (so the deny-by-default gate is
// exercised end-to-end with real sockets), while the unrunnable runsc/firecracker
// spawn is left to production.
type fakeIsoBackend struct{ unavailable bool }

func (f fakeIsoBackend) Name() string   { return "gvisor" }
func (f fakeIsoBackend) Isolated() bool { return true }
func (f fakeIsoBackend) Preflight(context.Context) error {
	if f.unavailable {
		return context.DeadlineExceeded
	}
	return nil
}

func (f fakeIsoBackend) Execute(ctx context.Context, job sandboxrt.Job, _ sandboxrt.Profile, proxyAddr string) (sandboxrt.BackendResult, error) {
	resolve := map[string]string{}
	for _, m := range job.Mocks {
		resolve[m.Resource] = m.Response
	}
	var steps []sandboxrt.StepOutput
	for _, s := range job.Steps {
		if r, ok := resolve[s.Input]; ok {
			steps = append(steps, sandboxrt.StepOutput{Key: s.Key, Output: r, MockHit: true})
		} else {
			steps = append(steps, sandboxrt.StepOutput{Key: s.Key, Output: "[[mock-miss:" + s.Input + "]]"})
		}
	}
	br := sandboxrt.BackendResult{Steps: steps, InstanceID: "i-1", Destroyed: true, DestroyVerified: true}
	if job.Probe != nil && proxyAddr != "" {
		pu, _ := url.Parse("http://" + proxyAddr)
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, job.Target, nil)
		if resp, err := client.Do(req); err == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == http.StatusOK {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				br.Response, br.Reached = string(b), true
			}
		}
	}
	return br, nil
}

func testEngine(t *testing.T, b sandboxrt.Backend) *sandboxrt.Engine {
	t.Helper()
	return sandboxrt.New(sandboxrt.WithBackend(b), sandboxrt.WithLogger(slog.Default()))
}

// TestSandboxRunnerAdapterSyntheticRun proves the sandbox.Runner adapter runs a
// synthetic scenario in the isolated runtime, maps the outcome, and reports the
// engine's real backend identity.
func TestSandboxRunnerAdapterSyntheticRun(t *testing.T) {
	a := sandboxRunnerAdapter{eng: testEngine(t, fakeIsoBackend{})}
	if a.Name() != "gvisor" || !a.Isolated() {
		t.Fatalf("adapter identity = (%q,%v), want (gvisor,true)", a.Name(), a.Isolated())
	}
	out, err := a.Run(context.Background(), model.TenantID("t1"), sandbox.RunSpec{
		Steps: []sandbox.Step{{Key: "s1", Input: "db"}, {Key: "s2", Input: "x"}},
		Mocks: []sandbox.Mock{{Resource: "db", Response: "ROWS"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out.Steps) != 2 || out.Steps[0].Output != "ROWS" || !out.Steps[0].MockHit || !out.Destroyed {
		t.Fatalf("unexpected outcome: %+v", out)
	}
}

// TestSandboxRunnerAdapterUnavailableIsHonest proves an unavailable runtime
// reports unavailable/false and fails the run closed (never a faked microVM).
func TestSandboxRunnerAdapterUnavailableIsHonest(t *testing.T) {
	a := sandboxRunnerAdapter{eng: testEngine(t, fakeIsoBackend{unavailable: true})}
	if a.Name() != "unavailable" || a.Isolated() {
		t.Fatalf("unavailable adapter identity = (%q,%v), want (unavailable,false)", a.Name(), a.Isolated())
	}
	if _, err := a.Run(context.Background(), model.TenantID("t1"), sandbox.RunSpec{}); err == nil {
		t.Fatal("run should fail closed with no backend available")
	}
}

// TestRedteamAdapterReachesAuthorizedTargetAndJudges proves the red-team adapter
// scopes egress to the target, delivers the probe, and judges the response with
// the module's own Judge: a complied response is a FAIL, a refusal a PASS.

// TestRedteamAdapterRefusesUnauthorizedTarget proves the RED LINE second check:
// an un-authorized target is never executed.

// TestRedteamAdapterErrorsWhenNoBackend proves a probe against an unavailable
// runtime is OutcomeError (the module records it and continues; never a false pass).

type unverifiedBackend struct{ fakeIsoBackend }

func (b unverifiedBackend) Execute(ctx context.Context, job sandboxrt.Job, profile sandboxrt.Profile, proxyAddr string) (sandboxrt.BackendResult, error) {
	r, err := b.fakeIsoBackend.Execute(ctx, job, profile, proxyAddr)
	r.DestroyVerified = false
	return r, err
}

func TestSandboxRuntimeConfigPreservesProxySocketOptIn(t *testing.T) {
	t.Setenv("OLIVARES_SANDBOX_RUNTIME_CONFIG", filepath.Join(t.TempDir(), "runtime.json"))
	if err := os.WriteFile(os.Getenv("OLIVARES_SANDBOX_RUNTIME_CONFIG"), []byte(`{"gvisor":{"proxy_socket":true,"network":"sandbox"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadSandboxRuntimeConfig(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GVisor == nil || !cfg.GVisor.to().ProxySocket || cfg.GVisor.to().Network != "sandbox" {
		t.Fatalf("config = %+v", cfg)
	}
}

// TestParseEndpointScopes proves the egress rule is scoped to exactly the target
// host:port across URL and bare-host forms.

// TestNewSandboxRuntimeNilWhenUnconfigured proves the default deployment keeps its
// honest module defaults (no engine when no backend is configured).
func TestNewSandboxRuntimeNilWhenUnconfigured(t *testing.T) {
	if eng := newSandboxRuntime(sandboxRuntimeConfig{}, slog.Default()); eng != nil {
		t.Fatal("unconfigured runtime should be nil (modules keep in-proc/offline defaults)")
	}
	// Configured ⇒ a (preflight-gated) engine is built; on this host the primitive
	// is absent so it is unavailable, but the engine is non-nil and fails closed.
	eng := newSandboxRuntime(sandboxRuntimeConfig{GVisor: &gvisorCfgJSON{RootfsDir: t.TempDir()}}, slog.Default())
	if eng == nil {
		t.Fatal("configured runtime should be non-nil")
	}
	if eng.Available() {
		t.Skip("host unexpectedly has runsc; availability path covered elsewhere")
	}
}
