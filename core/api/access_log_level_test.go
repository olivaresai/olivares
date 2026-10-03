// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"log/slog"
	"net/http"
	"testing"
)

// HU2-11: the console's polling wrote one INFO line per read into olivares.log. A
// read that succeeded is DEBUG; a write, a refusal and an error stay INFO.
func TestAccessLogKeepsReadsOutOfTheInfoLog(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
		want   slog.Level
	}{
		{http.MethodGet, http.StatusOK, slog.LevelDebug},
		{http.MethodHead, http.StatusNoContent, slog.LevelDebug},
		{http.MethodGet, http.StatusNotModified, slog.LevelDebug},
		{http.MethodGet, http.StatusUnauthorized, slog.LevelInfo},
		{http.MethodGet, http.StatusConflict, slog.LevelInfo},
		{http.MethodGet, http.StatusInternalServerError, slog.LevelInfo},
		{http.MethodPost, http.StatusCreated, slog.LevelInfo},
		{http.MethodPut, http.StatusOK, slog.LevelInfo},
		{http.MethodDelete, http.StatusNoContent, slog.LevelInfo},
	} {
		if got := accessLogLevel(tc.method, tc.status); got != tc.want {
			t.Errorf("%s %d logged at %v, want %v", tc.method, tc.status, got, tc.want)
		}
	}
}
