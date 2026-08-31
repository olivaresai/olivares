// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errLogBrokerUnavailable = fmt.Errorf("log broker unavailable")

const (
	defaultLogBufferLimit = 1000
	maxLogBufferLimit     = 10000
)

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.logBroker == nil {
		s.writeError(w, r, errLogBrokerUnavailable)
		return
	}

	filter, err := parseLogFilter(r)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.logBroker.Subscribe(filter)
	defer cancel()

	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if writeFrame(rc, w, fmt.Sprintf("event: log\ndata: %s\n\n", payload)) != nil {
				return
			}
		case <-ticker.C:
			if writeFrame(rc, w, ": ping\n\n") != nil {
				return
			}
		}
	}
}

func (s *Server) handleLogBuffer(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.logBroker == nil {
		s.writeError(w, r, errLogBrokerUnavailable)
		return
	}

	filter, err := parseLogFilter(r)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	limit := defaultLogBufferLimit
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if n, parseErr := strconv.Atoi(ls); parseErr == nil && n > 0 {
			limit = min(n, maxLogBufferLimit)
		}
	}

	entries, matched := s.logBroker.Buffer(filter, limit)
	// `total` used to be len(entries) — the size of the page this response carries, under a
	// name that promises the size of the set. A client could not tell a buffer holding
	// exactly `limit` entries from one holding ten thousand, so it could not know it was
	// looking at a window at all. `total` is now the number of entries in the ring that
	// matched the filter, and `truncated` says plainly that older matches were left out.
	writeJSON(w, http.StatusOK, map[string]any{
		"items":         entries,
		"total":         matched,
		"returned":      len(entries),
		"truncated":     matched > len(entries),
		"capture_level": strings.ToLower(s.logBroker.CaptureLevel().String()),
	})
}

func parseLogFilter(r *http.Request) (LogFilter, error) {
	values := r.URL.Query()
	filter := LogFilter{Module: values.Get("module")}

	// The exact-set parameter is authoritative whenever it carries a value,
	// including when a legacy threshold is also supplied. An EMPTY levels= (a
	// client clearing its filter) means "no level filter", never a 400; empty
	// CSV segments are skipped for the same reason. Unknown non-empty values
	// stay a hard 400 — a typo must not silently widen a filter.
	if strings.TrimSpace(values.Get("levels")) != "" {
		seen := make(map[slog.Level]struct{})
		for _, raw := range strings.Split(values.Get("levels"), ",") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			level, ok := namedLogLevel(raw)
			if !ok {
				return LogFilter{}, fmt.Errorf("unknown log level %q", strings.TrimSpace(raw))
			}
			if _, duplicate := seen[level]; duplicate {
				continue
			}
			seen[level] = struct{}{}
			filter.Levels = append(filter.Levels, level)
		}
		return filter, nil
	}

	if values.Has("level") {
		minLevel := parseLevelParam(values.Get("level"))
		filter.Min = &minLevel
	}
	return filter, nil
}

func parseLevelParam(s string) slog.Level {
	switch s {
	case "DEBUG", "debug":
		return slog.LevelDebug
	case "WARN", "warn":
		return slog.LevelWarn
	case "ERROR", "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
