// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

type consoleErrorTracing struct {
	enabled bool
}

func (s consoleErrorTracing) TracingSettings(context.Context) (api.TracingStatus, error) {
	return api.TracingStatus{Effective: obstrace.Settings{Enabled: s.enabled}}, nil
}

func (s consoleErrorTracing) SaveTracingSettings(context.Context, auth.Principal, obstrace.Settings) (api.TracingStatus, error) {
	panic("unexpected settings write")
}

func newConsoleErrorHarness(t *testing.T, telemetryEnabled bool) (*harness, string) {
	t.Helper()
	broker := api.NewLogBroker(slog.NewTextHandler(io.Discard, nil), 32, nil)
	h := newHarnessOpts(t, func(opts *api.Options) {
		opts.Logger = slog.New(broker)
		opts.LogBroker = broker
		opts.TracingSettings = consoleErrorTracing{enabled: telemetryEnabled}
	})
	return h, h.adminLogin()
}

func consoleErrorLogCount(t *testing.T, h *harness, admin string) int {
	t.Helper()
	r := h.do(http.MethodGet, "/v1/console/logs/buffer", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("log buffer = %d %s", r.code, r.raw)
	}
	count := 0
	for _, item := range responseLogItems(t, r) {
		entry := item.(map[string]any)
		attrs, _ := entry["attrs"].(map[string]any)
		if entry["message"] != "console view render error" || attrs["error_id"] != "deadbeef" {
			continue
		}
		count++
		if attrs["error_id"] != "deadbeef" || attrs["error"] != "render boom" ||
			attrs["component_stack"] != "\n    at BrokenView" || attrs["route_path"] != "/audit" {
			t.Fatalf("logged attrs = %#v", attrs)
		}
	}
	return count
}

func TestConsoleErrorReportLogsOnceWhenTelemetryIsEnabled(t *testing.T) {
	h, admin := newConsoleErrorHarness(t, true)
	r := h.do(http.MethodPost, "/v1/console/errors", admin, map[string]any{
		"error_id":        "deadbeef",
		"error":           "render boom",
		"component_stack": "\n    at BrokenView",
		"route_path":      "/audit",
	}, nil)
	if r.code != http.StatusNoContent || r.raw != "" {
		t.Fatalf("report = %d %s, want empty 204", r.code, r.raw)
	}
	if got := consoleErrorLogCount(t, h, admin); got != 1 {
		t.Fatalf("render error log count = %d, want 1", got)
	}
}

func TestConsoleErrorReportDoesNotLogWhenTelemetryIsDisabled(t *testing.T) {
	h, admin := newConsoleErrorHarness(t, false)
	r := h.do(http.MethodPost, "/v1/console/errors", admin, map[string]any{
		"error_id": "deadbeef",
		"error":    "render boom",
	}, nil)
	if r.code != http.StatusNoContent {
		t.Fatalf("report = %d %s, want 204", r.code, r.raw)
	}
	if got := consoleErrorLogCount(t, h, admin); got != 0 {
		t.Fatalf("render error log count = %d, want 0", got)
	}
}

func TestConsoleErrorReportRequiresAuthenticationAndBoundedInput(t *testing.T) {
	h, admin := newConsoleErrorHarness(t, true)
	valid := map[string]any{"error_id": "deadbeef", "error": "render boom"}
	if r := h.do(http.MethodPost, "/v1/console/errors", "", valid, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous report = %d %s, want 401", r.code, r.raw)
	}
	// Only a person's browser session reports: an API token is refused.
	it := h.do(http.MethodPost, "/v1/tokens", admin, map[string]any{"name": "ci", "superadmin": true}, nil)
	if it.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", it.code, it.raw)
	}
	if r := h.do(http.MethodPost, "/v1/console/errors", it.body["token"].(string), valid, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("API token report = %d %s, want 401", r.code, r.raw)
	}
	for name, body := range map[string]map[string]any{
		"short id":      {"error_id": "short", "error": "boom"},
		"bad id":        {"error_id": "bad\nid99", "error": "boom"},
		"missing error": {"error_id": "deadbeef"},
	} {
		t.Run(name, func(t *testing.T) {
			if r := h.do(http.MethodPost, "/v1/console/errors", admin, body, nil); r.code != http.StatusBadRequest {
				t.Fatalf("report = %d %s, want 400", r.code, r.raw)
			}
		})
	}
	if got := consoleErrorLogCount(t, h, admin); got != 0 {
		t.Fatalf("invalid reports logged %d entries, want 0", got)
	}
}

// A report over the limits is logged shortened, never refused, and the cut never splits a
// character. The console ring clamps every value again (log_redact.go); the terminal or
// file sink gets the record as the handler wrote it, so that is what this reads.
func TestConsoleErrorReportLogsALongReportShortened(t *testing.T) {
	var sink bytes.Buffer
	broker := api.NewLogBroker(slog.NewTextHandler(&sink, nil), 32, nil)
	h := newHarnessOpts(t, func(opts *api.Options) {
		opts.Logger = slog.New(broker)
		opts.LogBroker = broker
		opts.TracingSettings = consoleErrorTracing{enabled: true}
	})
	admin := h.adminLogin()
	r := h.do(http.MethodPost, "/v1/console/errors", admin, map[string]any{
		"error_id":        "cafebabe",
		"error":           strings.Repeat("€", 6000),  // 3 bytes each: 18000 bytes
		"component_stack": strings.Repeat("§", 40000), // 2 bytes each: 80000 bytes
	}, nil)
	if r.code != http.StatusNoContent {
		t.Fatalf("long report = %d %s, want 204", r.code, r.raw)
	}
	out := sink.String()
	// 16 KiB holds 5461 whole euro signs, 64 KiB exactly 32768 section signs.
	if got := strings.Count(out, "€"); got != 5461 {
		t.Errorf("logged %d euro signs, want 5461 (16 KiB without a split character)", got)
	}
	if got := strings.Count(out, "§"); got != 32768 {
		t.Errorf("logged %d section signs, want 32768 (64 KiB)", got)
	}
	if !utf8.ValidString(out) {
		t.Error("the log line holds a split character")
	}
}
