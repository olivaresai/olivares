// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func newLogHandlerHarness(t *testing.T, captureLevel slog.Level, capacity int) (*harness, *slog.Logger, string) {
	t.Helper()
	level := &slog.LevelVar{}
	level.Set(captureLevel)
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})
	broker := api.NewLogBroker(inner, capacity, level)
	h := newHarnessOpts(t, func(opts *api.Options) {
		opts.LogBroker = broker
	})
	return h, slog.New(broker), h.adminLogin()
}

func responseLogItems(t *testing.T, response resp) []any {
	t.Helper()
	items, ok := response.body["items"].([]any)
	if !ok {
		t.Fatalf("log response has no items array: %s", response.raw)
	}
	return items
}

func TestLogBufferLevelsExactSet(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warn")
	logger.Error("error")

	// levels wins over the conflicting legacy threshold and is case-insensitive.
	r := h.do("GET", "/v1/console/logs/buffer?levels=DeBuG,ERROR&level=warn", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("buffer = %d %s", r.code, r.raw)
	}
	items := responseLogItems(t, r)
	if len(items) != 2 {
		t.Fatalf("items = %d, want DEBUG and ERROR: %s", len(items), r.raw)
	}
	if items[0].(map[string]any)["level"] != "DEBUG" || items[1].(map[string]any)["level"] != "ERROR" {
		t.Fatalf("exact-set levels = %v, want DEBUG and ERROR", items)
	}
}

func TestLogBufferLegacyLevelThreshold(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warn")
	logger.Error("error")

	r := h.do("GET", "/v1/console/logs/buffer?level=warn", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("legacy threshold = %d %s", r.code, r.raw)
	}
	items := responseLogItems(t, r)
	if len(items) != 2 || items[0].(map[string]any)["level"] != "WARN" ||
		items[1].(map[string]any)["level"] != "ERROR" {
		t.Fatalf("legacy WARN threshold returned %v", items)
	}

	// Present-but-unrecognized legacy values retain the historical INFO default.
	r = h.do("GET", "/v1/console/logs/buffer?level=trace", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("unknown legacy threshold = %d %s", r.code, r.raw)
	}
	items = responseLogItems(t, r)
	if len(items) != 3 || items[0].(map[string]any)["level"] != "INFO" {
		t.Fatalf("unknown legacy threshold returned %v, want INFO and above", items)
	}
}

func TestLogBufferRejectsUnknownExactLevel(t *testing.T) {
	h, _, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	r := h.do("GET", "/v1/console/logs/buffer?levels=debug,trace", admin, nil, nil)
	if r.code != http.StatusBadRequest {
		t.Fatalf("unknown exact level = %d %s, want 400", r.code, r.raw)
	}

	r = h.do("GET", "/v1/console/logs/stream?levels=trace", admin, nil, nil)
	if r.code != http.StatusBadRequest {
		t.Fatalf("unknown stream exact level = %d %s, want 400", r.code, r.raw)
	}
}

func TestLogBufferModuleSubstringCaseInsensitive(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	logger.Info("database", "module", "Database.Writer")
	logger.Info("api", "module", "api")

	r := h.do("GET", "/v1/console/logs/buffer?module=BASE", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("module substring = %d %s", r.code, r.raw)
	}
	items := responseLogItems(t, r)
	if len(items) != 1 || items[0].(map[string]any)["message"] != "database" {
		t.Fatalf("module substring returned %v, want database entry", items)
	}
}

func TestLogBufferNoLevelFilterAndCaptureLevel(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	logger.Debug("captured debug")

	r := h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("unfiltered buffer = %d %s", r.code, r.raw)
	}
	items := responseLogItems(t, r)
	if len(items) != 1 || items[0].(map[string]any)["level"] != "DEBUG" {
		t.Fatalf("unfiltered buffer omitted DEBUG: %v", items)
	}
	if r.body["capture_level"] != "debug" {
		t.Fatalf("capture_level = %v, want debug", r.body["capture_level"])
	}
}

// TestLogBufferEmptyLevelsMeansNoFilter pins the cleared-filter semantics: an
// empty ?levels= (a client clearing its selection) passes ALL captured levels
// — never a 400 (only an unknown NON-empty value is a hard 400).
func TestLogBufferEmptyLevelsMeansNoFilter(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	logger.Debug("debug")
	logger.Warn("warn")

	r := h.do("GET", "/v1/console/logs/buffer?levels=", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("empty levels= must not 400: %d %s", r.code, r.raw)
	}
	if items := responseLogItems(t, r); len(items) != 2 {
		t.Fatalf("empty levels= must pass all captured levels, got %d: %s", len(items), r.raw)
	}
	if r := h.do("GET", "/v1/console/logs/buffer?levels=debug,,warn", admin, nil, nil); r.code != http.StatusOK {
		t.Fatalf("empty CSV segment must be skipped, got %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/console/logs/buffer?levels=bogus", admin, nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("unknown level must stay 400, got %d", r.code)
	}
}

func TestLogBufferLimitClampsAtMaximum(t *testing.T) {
	const entries = 10001
	h, logger, admin := newLogHandlerHarness(t, slog.LevelInfo, entries)
	for i := 0; i < entries; i++ {
		logger.Info("entry")
	}

	r := h.do("GET", "/v1/console/logs/buffer?limit=999999", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("clamped buffer = %d %s", r.code, r.raw)
	}
	if got := len(responseLogItems(t, r)); got != 10000 {
		t.Fatalf("clamped items = %d, want 10000", got)
	}
	// `total` USED to be len(items) and this assertion pinned that: with 10001 entries in
	// the ring it demanded the answer 10000, i.e. it demanded that the response describe
	// its own page and call it the set. A client reading `total == limit` cannot tell a
	// buffer holding exactly that many from one holding ten thousand more, which is the
	// same class as reporting a page size as a count anywhere else in this API. `total` is
	// now the number of entries that matched across the whole ring, `returned` is the page,
	// and `truncated` says plainly that older matches were left out.
	if r.body["total"] != float64(entries) {
		t.Fatalf("total = %v, want %d (every matching entry, not the page)", r.body["total"], entries)
	}
	if r.body["returned"] != float64(10000) {
		t.Fatalf("returned = %v, want 10000 (the clamped page)", r.body["returned"])
	}
	if r.body["truncated"] != true {
		t.Fatalf("truncated = %v, want true: %d matched and 10000 were sent", r.body["truncated"], entries)
	}
}

// ...and the other direction, because a `truncated` that is always true is the same lie
// with a different sign: a buffer that fits must say so.
func TestLogBufferIsNotTruncatedWhenEverythingFits(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelInfo, 100)
	for i := 0; i < 5; i++ {
		logger.Info("entry")
	}
	r := h.do("GET", "/v1/console/logs/buffer?limit=50", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("buffer = %d %s", r.code, r.raw)
	}
	if r.body["total"] != float64(5) || r.body["returned"] != float64(5) {
		t.Fatalf("total=%v returned=%v, want 5 and 5", r.body["total"], r.body["returned"])
	}
	if r.body["truncated"] != false {
		t.Fatalf("truncated = %v, want false: everything matching was sent", r.body["truncated"])
	}
}
