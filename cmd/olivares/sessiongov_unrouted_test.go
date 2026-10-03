// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func plainLaunch() sessions.LaunchIntent {
	return sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "default"}
}

// ARCH CORE-B B4b (Root, 2026-10-01): whether session inference goes through the
// Olivares inference proxy is a property of the deployment, not of a launch. A
// launch is never blocked by it and publishes nothing about it; the fact is stated
// once, at boot and in doctor. Until 26.10.1 every launch raised the same finding.
func TestSessionLaunchGate_UnroutedLaunchIsAllowedAndPublishesNothing(t *testing.T) {
	// The gate holds no routing posture and no bus: there is nothing a launch could publish.
	g := &sessionLaunchGate{recordAvailable: true, log: slog.Default()}
	for _, tenant := range []string{"tenant-a", "tenant-b", "tenant-a"} {
		dec, err := g.Authorize(context.Background(), model.TenantID(tenant), plainLaunch())
		if err != nil || !dec.Allowed {
			t.Fatalf("launch must be allowed, got allowed=%v err=%v", dec.Allowed, err)
		}
	}
}

func TestInferenceRoutingPostureIsOneFactFromTheRuntimeSetting(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == envSessionBaseURL {
				return v
			}
			return ""
		}
	}
	if got := inferenceRoutingPosture(env("")); got != inferenceNotRouted {
		t.Fatalf("no base URL = %q, want %q", got, inferenceNotRouted)
	}
	if got := inferenceRoutingPosture(env("  ")); got != inferenceNotRouted {
		t.Fatalf("blank base URL = %q, want %q", got, inferenceNotRouted)
	}
	if got := inferenceRoutingPosture(env("https://127.0.0.1:8443/v1/inference")); got != inferenceRouted {
		t.Fatalf("base URL set = %q, want %q", got, inferenceRouted)
	}
}

// The boot states the fact once, at INFO, with its name, and says what it means
// when inference is not routed. It is not a warning: nothing is broken.
func TestInferenceRoutingPostureIsLoggedOnceAtBoot(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logInferenceRoutingPosture(log, inferenceNotRouted)
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "level=INFO") {
		t.Fatalf("want one INFO line, got %q", out)
	}
	if !strings.Contains(out, inferenceRoutingFact+"="+inferenceNotRouted) {
		t.Fatalf("the line does not name the fact %s=%s: %q", inferenceRoutingFact, inferenceNotRouted, out)
	}
}
