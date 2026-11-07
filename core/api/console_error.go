// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"unicode/utf8"
)

const (
	maxConsoleErrorText  = 16 * 1024
	maxConsoleErrorStack = 64 * 1024
	maxConsoleErrorRoute = 2048
)

type consoleErrorReport struct {
	ErrorID        string `json:"error_id"`
	Error          string `json:"error"`
	ComponentStack string `json:"component_stack"`
	RoutePath      string `json:"route_path"`
}

func validConsoleErrorID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

// truncateUTF8 cuts s to at most n bytes without splitting a character, so a long
// report is kept, shortened, instead of refused.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// handleConsoleError records a browser render failure only when deployment
// telemetry is enabled. Authentication identifies the reporter; no admin read of
// tracing settings or screen data is needed. Only a person's browser session may
// report: the console is the one sender, so API and machine tokens get 401.
func (s *Server) handleConsoleError(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var in consoleErrorReport
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid console error report")
		return
	}
	if !validConsoleErrorID(in.ErrorID) || in.Error == "" {
		s.badRequest(w, r, "invalid console error report")
		return
	}
	if s.tracingSettings == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	status, err := s.tracingSettings.TracingSettings(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !status.Effective.Enabled {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.log.With("module", "console").Error("console view render error",
		"error_id", in.ErrorID,
		"error", truncateUTF8(in.Error, maxConsoleErrorText),
		"component_stack", truncateUTF8(in.ComponentStack, maxConsoleErrorStack),
		"route_path", truncateUTF8(in.RoutePath, maxConsoleErrorRoute),
		"actor", mc.Principal.Actor(),
	)
	w.WriteHeader(http.StatusNoContent)
}
