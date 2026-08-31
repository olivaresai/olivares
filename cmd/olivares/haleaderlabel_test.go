// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// fakeAPIServer records the label patches an engine sends to the Kubernetes API.
type fakeAPIServer struct {
	*httptest.Server
	mu       sync.Mutex
	roles    []string
	paths    []string
	tokens   []string
	ctypes   []string
	failNext int
}

func newFakeAPIServer(t *testing.T) *fakeAPIServer {
	t.Helper()
	f := &fakeAPIServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var patch struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(body, &patch)
		f.mu.Lock()
		if f.failNext > 0 {
			f.failNext--
			f.mu.Unlock()
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.ctypes = append(f.ctypes, r.Header.Get("Content-Type"))
		f.roles = append(f.roles, patch.Metadata.Labels[haRoleLabelKey])
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"Pod"}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPIServer) published() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.roles...)
}

// testPublisher builds a publisher pointed at the fake API server, with a token
// file on disk (the projected ServiceAccount token the operator mounts).
func testPublisher(t *testing.T, f *fakeAPIServer) *haLeaderPublisher {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &haLeaderPublisher{
		base:      f.URL,
		pod:       "cp-1",
		namespace: "olivares",
		tokenFile: tokenFile,
		client:    f.Client(),
		poll:      10 * time.Millisecond,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestHALeaderPublisherPatchesOwnPod pins the wire contract the operator's RBAC
// authorizes: a merge-patch of the caller's OWN pod with the role label, carrying
// the projected ServiceAccount bearer token (re-read per call, so a rotated
// projected token is picked up without a restart).
func TestHALeaderPublisherPatchesOwnPod(t *testing.T) {
	f := newFakeAPIServer(t)
	p := testPublisher(t, f)

	if err := p.publish(context.Background(), haRoleLeader); err != nil {
		t.Fatalf("publish: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, want := f.paths[0], "PATCH /api/v1/namespaces/olivares/pods/cp-1"; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
	if f.ctypes[0] != "application/merge-patch+json" {
		t.Errorf("content-type = %q, want application/merge-patch+json", f.ctypes[0])
	}
	if f.tokens[0] != "Bearer sa-token-v1" {
		t.Errorf("authorization = %q, want the projected ServiceAccount token", f.tokens[0])
	}
	if f.roles[0] != haRoleLeader {
		t.Errorf("published role = %q, want %q", f.roles[0], haRoleLeader)
	}
}

// TestHALeaderPublisherFollowsLeadership is the failover contract at the label
// surface: the publisher converges the pod label onto the CURRENT leadership state
// — it publishes standby while following, leader on promotion, and standby again
// on demotion — so the leader-selecting Service always resolves to the one active
// writer. It also proves the publisher does not re-patch an unchanged role.
func TestHALeaderPublisherFollowsLeadership(t *testing.T) {
	f := newFakeAPIServer(t)
	p := testPublisher(t, f)

	var leader sync.Map
	leader.Store("v", false)
	isLeader := func() bool { v, _ := leader.Load("v"); return v.(bool) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.run(ctx, isLeader) }()

	waitFor(t, func() bool { return len(f.published()) >= 1 })
	leader.Store("v", true)
	waitFor(t, func() bool {
		pub := f.published()
		return len(pub) >= 2 && pub[len(pub)-1] == haRoleLeader
	})
	leader.Store("v", false)
	waitFor(t, func() bool {
		pub := f.published()
		return len(pub) >= 3 && pub[len(pub)-1] == haRoleStandby
	})
	cancel()
	<-done

	pub := f.published()
	if pub[0] != haRoleStandby {
		t.Fatalf("first published role = %q, want %q (a restarted pod must never keep a stale leader label)", pub[0], haRoleStandby)
	}
	// Steady state is quiet: one patch per TRANSITION, not one per poll tick.
	if len(pub) > 6 {
		t.Errorf("published %d patches for 3 transitions (%v); the publisher must not re-patch an unchanged role", len(pub), pub)
	}
}

// TestHALeaderPublisherRetriesAfterAPIFailure proves a transient Kubernetes API
// error does not leave the label permanently stale: the resync loop republishes on
// the next tick. A publication failure never grants or removes leadership — the
// Postgres lock is the sole authority (design §B.1 failure modes).
func TestHALeaderPublisherRetriesAfterAPIFailure(t *testing.T) {
	f := newFakeAPIServer(t)
	f.mu.Lock()
	f.failNext = 2
	f.mu.Unlock()
	p := testPublisher(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx, func() bool { return true })

	waitFor(t, func() bool {
		pub := f.published()
		return len(pub) >= 1 && pub[len(pub)-1] == haRoleLeader
	})
}

// TestHALeaderPublisherBacksOff pins the retry pacing: a persistent failure (a
// revoked RoleBinding, an apiserver outage) must not turn every replica into a
// once-a-second hammer against a struggling apiserver — it backs off to a bounded
// interval and keeps retrying there.
func TestHALeaderPublisherBacksOff(t *testing.T) {
	p := &haLeaderPublisher{poll: time.Second, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if got := p.backoff(); got != time.Second {
		t.Errorf("healthy backoff = %s, want the poll interval %s", got, time.Second)
	}
	p.fails = 3
	if got, want := p.backoff(), 8*time.Second; got != want {
		t.Errorf("backoff after 3 failures = %s, want %s", got, want)
	}
	p.fails = 50
	if got := p.backoff(); got != haBackoffMax {
		t.Errorf("backoff after 50 failures = %s, want the %s cap", got, haBackoffMax)
	}
}

// TestLeaderOnlyHandler covers the AUXILIARY listeners (HITL receiver, voice
// webhook, agent gateway, hook PEP, inference proxy). They are separate
// http.Servers outside core/api's middleware chain, so in the leader-routing layout
// — where every replica is Ready and therefore dialable — a standby would otherwise
// run a governed hook decision or proxy an inference call while another node is the
// writer. Probes and metrics must still answer on every replica.
func TestLeaderOnlyHandler(t *testing.T) {
	served := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})

	ctx := context.Background()
	base, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	// Standby: the application path is refused with the retryable not_leader shape.
	h := leaderOnlyHandler(inner, standbyStore{Store: base})
	rr := do(h, http.MethodPost, "/hooks/pre-tool-use")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("auxiliary application path on a standby = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "not_leader") {
		t.Errorf("body = %q, want the not_leader error shape", rr.Body.String())
	}
	if served != 0 {
		t.Error("the wrapped handler ran on a standby; the side effect must not happen at all")
	}
	// …but the probes are not application traffic.
	for _, p := range []string{"/healthz", "/livez", "/readyz", "/pod-readyz", "/metrics"} {
		if rr := do(h, http.MethodGet, p); rr.Code != http.StatusOK {
			t.Errorf("%s on a standby auxiliary listener = %d, want 200", p, rr.Code)
		}
	}

	// Leader: unchanged behavior.
	served = 0
	if rr := do(leaderOnlyHandler(inner, base), http.MethodPost, "/hooks/pre-tool-use"); rr.Code != http.StatusOK || served != 1 {
		t.Fatalf("auxiliary path on the leader = %d (served %d), want 200 once", rr.Code, served)
	}
}

// TestLoadHALeaderConfig covers the fail-closed env contract: label publishing
// requires the downward-API pod identity, it implies the route gate (a Ready
// standby that is routed by label MUST refuse application traffic), and the gate
// alone is valid for non-Kubernetes HA deployments.
func TestLoadHALeaderConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	cfg, err := loadHALeaderConfig(env(map[string]string{
		"OLIVARES_HA_LEADER_LABEL": "1",
		"POD_NAME":                 "cp-0",
		"POD_NAMESPACE":            "olivares",
		"KUBERNETES_SERVICE_HOST":  "10.0.0.1",
		"KUBERNETES_SERVICE_PORT":  "443",
	}))
	if err != nil {
		t.Fatalf("valid label config: %v", err)
	}
	if !cfg.PublishLabel || !cfg.Gate {
		t.Errorf("label publishing must imply the route gate; got %+v", cfg)
	}
	if cfg.APIServer != "https://10.0.0.1:443" {
		t.Errorf("api server = %q, want https://10.0.0.1:443", cfg.APIServer)
	}

	if _, err := loadHALeaderConfig(env(map[string]string{"OLIVARES_HA_LEADER_LABEL": "1"})); err == nil {
		t.Error("label publishing without POD_NAME/POD_NAMESPACE must fail closed, not publish nothing silently")
	}

	cfg, err = loadHALeaderConfig(env(map[string]string{"OLIVARES_HA_LEADER_GATE": "true"}))
	if err != nil {
		t.Fatalf("gate-only config: %v", err)
	}
	if !cfg.Gate || cfg.PublishLabel {
		t.Errorf("gate-only config = %+v, want Gate only (non-Kubernetes HA)", cfg)
	}

	cfg, err = loadHALeaderConfig(env(map[string]string{}))
	if err != nil || cfg.Gate || cfg.PublishLabel {
		t.Errorf("unset config = %+v, %v; want the legacy layout (both off)", cfg, err)
	}

	if _, err := loadHALeaderConfig(env(map[string]string{"OLIVARES_HA_LEADER_GATE": "yes-please"})); err == nil {
		t.Error("an unparseable boolean must fail loudly rather than silently disabling the gate")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}
