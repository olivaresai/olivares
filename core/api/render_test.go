// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONWriterPreservesWireFormat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		value  any
		body   string
	}{
		{"object", 201, map[string]string{"name": "<job>"}, "{\"name\":\"\\u003cjob\\u003e\"}\n"},
		{"empty", 204, nil, ""},
		{"typed nil", 200, (*string)(nil), "null\n"},
		{"stable error", 403, map[string]any{"error": map[string]string{"code": "forbidden", "message": "forbidden"}}, "{\"error\":{\"code\":\"forbidden\",\"message\":\"forbidden\"}}\n"},
		{"structured conflict", 409, map[string]string{"id": "run-1", "state": "running"}, "{\"id\":\"run-1\",\"state\":\"running\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, tc.status, tc.value)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("response = %d %q; want %d %q", w.Code, w.Body.String(), tc.status, tc.body)
			}
			if w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("headers = %v", w.Header())
			}
		})
	}
}

func TestJSONWriterPreservesLegacyMediaTypeAndNull(t *testing.T) {
	for _, mediaType := range []string{"application/json", "application/scim+json"} {
		w := httptest.NewRecorder()
		WriteJSON(w, 200, json.RawMessage("null"), mediaType)
		if w.Header().Get("Content-Type") != mediaType || w.Body.String() != "null\n" {
			t.Fatalf("response = %v %q", w.Header(), w.Body.String())
		}
	}
}

type privateMarshalFailure struct{}

func (privateMarshalFailure) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private payload contents")
}

func TestJSONWriterReportsFailureWithoutLeakingPayload(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	w := httptest.NewRecorder()
	WriteJSON(w, 201, privateMarshalFailure{})
	if w.Code != 201 || w.Body.Len() != 0 {
		t.Fatalf("committed response changed: %d %q", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "JSON response write failed") || strings.Contains(logs.String(), "private payload") {
		t.Fatalf("unsafe or missing failure log: %s", logs.String())
	}
}
