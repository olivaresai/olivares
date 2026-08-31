// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// discardHandler is a slog.Handler that discards all records.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

func testLevelVar(level slog.Level) *slog.LevelVar {
	v := &slog.LevelVar{}
	v.Set(level)
	return v
}

func testLogBroker(inner slog.Handler, capacity int) *LogBroker {
	return NewLogBroker(inner, capacity, testLevelVar(slog.LevelDebug))
}

type recordingHandler struct {
	min      slog.Level
	mu       sync.Mutex
	messages []string
}

func (h *recordingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.min
}

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	h.messages = append(h.messages, record.Message)
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.messages...)
}

func TestLogBroker_CapturesEntries(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	logger.Info("hello", "module", "api")
	logger.Warn("warning", "module", "dr")
	logger.Error("bad thing")

	entries, _ := broker.Buffer(LogFilter{}, 0)
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}

	if entries[0].Message != "hello" {
		t.Errorf("entries[0].Message = %q, want hello", entries[0].Message)
	}
	if entries[0].Level != "INFO" {
		t.Errorf("entries[0].Level = %q, want INFO", entries[0].Level)
	}
	if entries[0].Module != "api" {
		t.Errorf("entries[0].Module = %q, want api", entries[0].Module)
	}

	if entries[1].Level != "WARN" {
		t.Errorf("entries[1].Level = %q, want WARN", entries[1].Level)
	}
	if entries[2].Level != "ERROR" {
		t.Errorf("entries[2].Level = %q, want ERROR", entries[2].Level)
	}
}

func TestLogBroker_RingBufferWraps(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 5)
	logger := slog.New(broker)

	for i := 0; i < 10; i++ {
		logger.Info("msg", "i", i)
	}

	entries, _ := broker.Buffer(LogFilter{}, 0)
	if len(entries) != 5 {
		t.Fatalf("len(entries) = %d, want 5 (ring buffer capacity)", len(entries))
	}
	if entries[0].Attrs["i"] != int64(5) {
		t.Errorf("oldest entry i = %v, want 5", entries[0].Attrs["i"])
	}
	if entries[4].Attrs["i"] != int64(9) {
		t.Errorf("newest entry i = %v, want 9", entries[4].Attrs["i"])
	}
}

func TestLogBroker_FilterByLevel(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	logger.Debug("debug msg")
	logger.Info("info msg")
	logger.Warn("warn msg")
	logger.Error("error msg")

	minLevel := slog.LevelWarn
	warnAndAbove, _ := broker.Buffer(LogFilter{Min: &minLevel}, 0)
	if len(warnAndAbove) != 2 {
		t.Fatalf("len(warnAndAbove) = %d, want 2", len(warnAndAbove))
	}
	if warnAndAbove[0].Level != "WARN" {
		t.Errorf("first = %q, want WARN", warnAndAbove[0].Level)
	}
	if warnAndAbove[1].Level != "ERROR" {
		t.Errorf("second = %q, want ERROR", warnAndAbove[1].Level)
	}
}

func TestLogBroker_FilterByExactLevelSet(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	logger.Debug("debug msg")
	logger.Info("info msg")
	logger.Warn("warn msg")
	logger.Error("error msg")

	minLevel := slog.LevelError
	exact, _ := broker.Buffer(LogFilter{
		Levels: []slog.Level{slog.LevelDebug, slog.LevelError},
		Min:    &minLevel, // exact set wins over the conflicting threshold
	}, 0)
	if len(exact) != 2 {
		t.Fatalf("len(exact) = %d, want 2", len(exact))
	}
	if exact[0].Level != "DEBUG" || exact[1].Level != "ERROR" {
		t.Fatalf("exact levels = [%s %s], want [DEBUG ERROR]", exact[0].Level, exact[1].Level)
	}
}

func TestLogBroker_FilterPassesAllWhenEmpty(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	logger.Debug("debug msg")
	logger.Info("info msg")
	logger.Warn("warn msg")
	logger.Error("error msg")

	all, _ := broker.Buffer(LogFilter{}, 0)
	if len(all) != 4 {
		t.Fatalf("len(all) = %d, want 4", len(all))
	}
}

func TestLogBroker_FilterByModule(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	logger.Info("api request", "module", "api")
	logger.Info("dr backup", "module", "DisasterRecovery")
	logger.Info("store query", "module", "store")

	drOnly, _ := broker.Buffer(LogFilter{Module: "RECOVERY"}, 0)
	if len(drOnly) != 1 {
		t.Fatalf("len(drOnly) = %d, want 1", len(drOnly))
	}
	if drOnly[0].Message != "dr backup" {
		t.Errorf("message = %q, want 'dr backup'", drOnly[0].Message)
	}
}

func TestLogBroker_Subscribe(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	ch, cancel := broker.Subscribe(LogFilter{})
	defer cancel()

	logger.Info("test message")

	select {
	case entry := <-ch:
		if entry.Message != "test message" {
			t.Errorf("message = %q, want 'test message'", entry.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for entry")
	}
}

func TestLogBroker_SubscribeLevelFilter(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	minLevel := slog.LevelWarn
	ch, cancel := broker.Subscribe(LogFilter{Min: &minLevel})
	defer cancel()

	logger.Info("should not arrive")
	logger.Warn("should arrive")

	select {
	case entry := <-ch:
		if entry.Level != "WARN" {
			t.Errorf("level = %q, want WARN", entry.Level)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for warn entry")
	}

	select {
	case <-ch:
		t.Fatal("info entry should not have been delivered")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLogBroker_SubscribeExactLevelSet(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	ch, cancel := broker.Subscribe(LogFilter{Levels: []slog.Level{slog.LevelDebug, slog.LevelError}})
	defer cancel()

	logger.Info("should not arrive")
	logger.Error("should arrive")

	select {
	case entry := <-ch:
		if entry.Level != "ERROR" {
			t.Fatalf("level = %q, want ERROR", entry.Level)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for exact-set entry")
	}
	select {
	case entry := <-ch:
		t.Fatalf("unexpected entry: %+v", entry)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLogBroker_SubscribeModuleSubstring(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	ch, cancel := broker.Subscribe(LogFilter{Module: "BASE"})
	defer cancel()

	logger.Info("filtered", "module", "api")
	logger.Info("matched", "module", "Database.Writer")

	select {
	case entry := <-ch:
		if entry.Message != "matched" {
			t.Fatalf("module-filtered entry = %+v, want matched", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for module substring match")
	}
	select {
	case entry := <-ch:
		t.Fatalf("unexpected non-matching module entry: %+v", entry)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLogBroker_Unsubscribe(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	ch, cancel := broker.Subscribe(LogFilter{})
	cancel()

	logger.Info("after cancel")

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed after cancel")
		}
	default:
	}
}

func TestLogBroker_ConcurrentWrites(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 1000)
	logger := slog.New(broker)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			logger.Info("concurrent", "n", n)
		}(i)
	}
	wg.Wait()

	entries, _ := broker.Buffer(LogFilter{}, 0)
	if len(entries) != 100 {
		t.Errorf("len(entries) = %d, want 100", len(entries))
	}
}

func TestLogBroker_WithGroup(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker).WithGroup("mymodule")

	logger.Info("grouped message")

	entries, _ := broker.Buffer(LogFilter{}, 0)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Module != "mymodule" {
		t.Errorf("module = %q, want mymodule", entries[0].Module)
	}
}

func TestLogBroker_BufferLimit(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)

	for i := 0; i < 50; i++ {
		logger.Info("msg")
	}

	limited, _ := broker.Buffer(LogFilter{}, 10)
	if len(limited) != 10 {
		t.Errorf("len = %d, want 10", len(limited))
	}
}

func TestLogBroker_CaptureLevelGatesRingAndSubscribers(t *testing.T) {
	// The inner terminal is pinned at WARN too, so INFO is refused by BOTH
	// sides — Enabled is the OR of the two decoupled floors.
	broker := NewLogBroker(&recordingHandler{min: slog.LevelWarn}, 100, testLevelVar(slog.LevelWarn))
	logger := slog.New(broker)
	ch, cancel := broker.Subscribe(LogFilter{})
	defer cancel()

	if broker.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("broker enabled INFO refused by both the WARN capture level and the WARN terminal")
	}
	logger.Info("below capture")
	// Handler implementations must remain safe even if a caller violates slog's
	// Enabled-before-Handle convention.
	_ = broker.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelDebug, "direct below capture", 0))
	logger.Warn("captured")

	entries, _ := broker.Buffer(LogFilter{}, 0)
	if len(entries) != 1 || entries[0].Message != "captured" {
		t.Fatalf("captured entries = %+v, want only WARN", entries)
	}
	select {
	case entry := <-ch:
		if entry.Message != "captured" {
			t.Fatalf("published entry = %+v, want captured WARN", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for captured WARN")
	}
	select {
	case entry := <-ch:
		t.Fatalf("record below capture level was published: %+v", entry)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLogBroker_InnerForwardingRespectsInnerEnabled(t *testing.T) {
	inner := &recordingHandler{min: slog.LevelWarn}
	broker := NewLogBroker(inner, 100, testLevelVar(slog.LevelDebug))
	logger := slog.New(broker)

	logger.Debug("captured debug")
	logger.Info("captured info")
	logger.Warn("forwarded warn")
	logger.Error("forwarded error")

	if entries, _ := broker.Buffer(LogFilter{}, 0); len(entries) != 4 {
		t.Fatalf("broker captured %d entries, want 4", len(entries))
	}
	got := inner.snapshot()
	if len(got) != 2 || got[0] != "forwarded warn" || got[1] != "forwarded error" {
		t.Fatalf("inner messages = %v, want WARN and ERROR only", got)
	}
}

// TestLogBroker_RaisedCaptureNeverSilencesTerminal pins the capture>inner
// direction: raising OLIVARES_LOG_LEVEL above the terminal's own level must
// not silence records the terminal would print — the two floors are decoupled
// (adversarial review: this direction was previously unpinned).
func TestLogBroker_RaisedCaptureNeverSilencesTerminal(t *testing.T) {
	inner := &recordingHandler{min: slog.LevelDebug}
	broker := NewLogBroker(inner, 100, testLevelVar(slog.LevelWarn))
	logger := slog.New(broker)

	logger.Info("terminal only")
	logger.Warn("both")

	entries, _ := broker.Buffer(LogFilter{}, 0)
	if len(entries) != 1 || entries[0].Message != "both" {
		t.Fatalf("ring = %+v, want only the WARN record", entries)
	}
	got := inner.snapshot()
	if len(got) != 2 || got[0] != "terminal only" || got[1] != "both" {
		t.Fatalf("terminal messages = %v, want INFO and WARN both delivered", got)
	}
}

func TestLevelFromString(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"Info", slog.LevelInfo},
		{"WARN", slog.LevelWarn},
		{"wArN", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"error", slog.LevelError},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}
	for _, tt := range tests {
		got := levelFromString(tt.input)
		if got != tt.want {
			t.Errorf("levelFromString(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
