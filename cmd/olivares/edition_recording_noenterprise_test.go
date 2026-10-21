// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCommunityHelpHidesRecording(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"recording"})
	if err != nil || command == nil || command.Name() != "recording" {
		t.Fatalf("published command recording disappeared: %v", err)
	}
	if !command.Hidden {
		t.Error("Community advertises recording in help")
	}
}

func TestCommunityRecordingRefusalIsTheEditionExit(t *testing.T) {
	for _, args := range [][]string{
		{"recording", "notice"},
		{"recording", "sessions", "ls"},
		{"recording", "sessions", "export", "rs-1"},
		{"recording", "config", "get"},
	} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			server := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotImplemented)
				_, _ = w.Write([]byte(`{"error":{"code":"business_required","message":"Privileged-session recording requires Olivares Business."}}`))
			})
			_, _, err := execRoot(t, lot3Args(server.URL, args...)...)
			if exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), "Session recording is a Business feature") || !strings.Contains(err.Error(), pricingURL) {
				t.Fatalf("recording refusal = %v; want the Business sentence and exit %d", err, exitcode.Edition)
			}
		})
	}
}

// The Business summarizer answers 501 when no inference credential is wired. That is not an
// edition refusal and keeps its own wording and exit code.
func TestRecordingSummarizerUnwiredIsNotAnEditionRefusal(t *testing.T) {
	server := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":{"code":"module_error","message":"no summarizer configured"}}`))
	})
	_, _, err := execRoot(t, lot3Args(server.URL, "recording", "sessions", "summarize", "rs-1")...)
	if err == nil || exitcode.From(err) == exitcode.Edition || !strings.Contains(err.Error(), "no summarizer configured") {
		t.Fatalf("unwired summarizer = %v; want its own 501 text, not the Business exit", err)
	}
}
