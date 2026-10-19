// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func withEditionName(t *testing.T, name string) {
	t.Helper()
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = name
}

// Active-passive HA leader routing (the leader gate and the pod-label publisher) is a
// Business capability. A Community binary handed either switch refuses to start with the
// Business exit code and names the switch; it never runs unrouted and silent.
func TestCommunityRefusesHALeaderRouting(t *testing.T) {
	withEditionName(t, "community")
	for _, tc := range []struct {
		name string
		cfg  haLeaderConfig
		key  string
	}{
		{"gate", haLeaderConfig{Gate: true}, haLabelGateEnv},
		{"label", haLeaderConfig{Gate: true, PublishLabel: true}, haLabelPublishEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := haLeaderRoutingEdition(tc.cfg)
			if err == nil {
				t.Fatalf("Community accepted %s", tc.key)
			}
			if got := exitcode.From(err); got != exitcode.Edition {
				t.Errorf("exit code = %d, want %d (Edition)", got, exitcode.Edition)
			}
			if msg := err.Error(); !strings.Contains(msg, "Business") || !strings.Contains(msg, tc.key) {
				t.Errorf("error must name Business and %s: %v", tc.key, err)
			}
		})
	}
	if err := haLeaderRoutingEdition(haLeaderConfig{}); err != nil {
		t.Errorf("Community with both switches off must start: %v", err)
	}
}

func TestBusinessKeepsHALeaderRouting(t *testing.T) {
	withEditionName(t, "enterprise")
	for _, cfg := range []haLeaderConfig{{Gate: true}, {Gate: true, PublishLabel: true}} {
		if err := haLeaderRoutingEdition(cfg); err != nil {
			t.Errorf("Business refused %+v: %v", cfg, err)
		}
	}
}

// The refusal sits on the real boot path, before the election and before any
// apiserver call, so a Community pod fails at start instead of serving unrouted.
func TestBootCommunityRefusesHALeaderGate(t *testing.T) {
	withEditionName(t, "community")
	t.Setenv(haLabelGateEnv, "1")
	t.Setenv(haLabelPublishEnv, "")
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: slog.Default(), ServeMode: true})
	if err == nil {
		_ = eng.Close()
		t.Fatalf("Community booted with %s=1", haLabelGateEnv)
	}
	if got := exitcode.From(err); got != exitcode.Edition {
		t.Errorf("boot exit code = %d, want %d (Edition): %v", got, exitcode.Edition, err)
	}
	if !strings.Contains(err.Error(), "Business") {
		t.Errorf("boot error must name Business: %v", err)
	}
}

// The refusal comes before the publisher: a Community pod never dials the apiserver
// or publishes a role label. Without the check first, newHALeaderPublisher would fail
// on the missing in-cluster CA with exit code 1 instead.
func TestBootCommunityRefusesHALeaderLabelBeforePublishing(t *testing.T) {
	withEditionName(t, "community")
	t.Setenv(haLabelGateEnv, "")
	t.Setenv(haLabelPublishEnv, "1")
	t.Setenv("POD_NAME", "cp-0")
	t.Setenv("POD_NAMESPACE", "olivares")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: slog.Default(), ServeMode: true})
	if err == nil {
		_ = eng.Close()
		t.Fatalf("Community booted with %s=1", haLabelPublishEnv)
	}
	if got := exitcode.From(err); got != exitcode.Edition {
		t.Errorf("boot exit code = %d, want %d (Edition): %v", got, exitcode.Edition, err)
	}
	if !strings.Contains(err.Error(), haLabelPublishEnv) {
		t.Errorf("boot error must name %s: %v", haLabelPublishEnv, err)
	}
}

// A serve-only switch left in a pod's environment does not block the commands that
// boot the same engine without serving (audit, dr).
func TestBootCommunityNonServeIgnoresHALeaderGate(t *testing.T) {
	withEditionName(t, "community")
	t.Setenv(haLabelGateEnv, "1")
	t.Setenv(haLabelPublishEnv, "")
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("a non-serving Community boot refused %s=1: %v", haLabelGateEnv, err)
	}
	_ = eng.Close()
}

func TestBootBusinessKeepsHALeaderGate(t *testing.T) {
	withEditionName(t, "enterprise")
	t.Setenv(haLabelGateEnv, "1")
	t.Setenv(haLabelPublishEnv, "")
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: slog.Default(), ServeMode: true})
	if err != nil {
		t.Fatalf("Business refused %s=1: %v", haLabelGateEnv, err)
	}
	_ = eng.Close()
}
