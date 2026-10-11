// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
)

func TestFreshInstallLogsNoWarningsAcrossJobIntervals(t *testing.T) {
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF, haLabelGateEnv, haLabelPublishEnv} {
		t.Setenv(name, "")
	}
	const interval = 50 * time.Millisecond
	t.Setenv(gitpublishSweepIntervalEnv, interval.String())
	t.Setenv(workOutboxPumpIntervalEnv, interval.String())
	logs := &loopLog{}
	logger := slog.New(logs)
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: logger, ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if got := moduleStatus(eng, "olivares.gitpublish"); got != runtime.StatusDormant {
		t.Fatalf("unconfigured gitpublish = %s, want dormant", got)
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, _, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": setup, "email": "admin@example.test", "password": "fresh-install-test-password",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	// Keep one-time key backup and security posture notices, but catch job
	// failures even when the scheduler ran before first-admin setup finished.
	logs.mu.Lock()
	for _, r := range logs.recs {
		if strings.Contains(r.msg, "SQLite single-node") {
			t.Logf("level=%s msg=%q %v", r.level, r.msg, r.attrs)
		}
		if r.level >= slog.LevelWarn && (strings.Contains(r.msg, "SQLite single-node") || strings.Contains(r.msg, "gitpublish") || strings.Contains(r.msg, "runtime: periodic job")) {
			t.Errorf("level=%s msg=%q %v", r.level, r.msg, r.attrs)
		}
	}
	logs.recs = nil
	logs.mu.Unlock()
	// Reset only the pump's INFO-log throttle so each real pump pass has an
	// observable completion. No work is injected and the pump is never called here.
	for pass := 1; pass <= 2; pass++ {
		eng.communicationPump.mu.Lock()
		eng.communicationPump.lastReason = ""
		eng.communicationPump.mu.Unlock()
		deadline := time.After(5 * time.Second)
		tick := time.NewTicker(5 * time.Millisecond)
		for {
			logs.mu.Lock()
			completed := 0
			for _, r := range logs.recs {
				if r.msg == "sessions-work-outbox: K3 lane verdict" {
					completed++
				}
			}
			logs.mu.Unlock()
			if completed >= pass {
				break
			}
			select {
			case <-deadline:
				tick.Stop()
				t.Fatalf("scheduled pump did not complete pass %d", pass)
			case <-tick.C:
			}
		}
		tick.Stop()
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	t.Logf("observed two completed pump passes after setup (interval %s)", interval)
	for _, r := range logs.recs {
		if r.level >= slog.LevelWarn {
			t.Errorf("level=%s msg=%q %v", r.level, r.msg, r.attrs)
		}
	}
}
