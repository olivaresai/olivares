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

// Legal holds and erasure with key shred are Business (docs/editions.md). The published
// commands stay, a Community help does not list them, and the engine's refusal reaches a
// script as the Business sentence and exit 9.

func TestCommunityHelpHidesLegalHoldAndErasure(t *testing.T) {
	for _, path := range []string{"compliance holds", "compliance erasure", "compliance subject"} {
		command, _, err := newRootCmd().Find(strings.Fields(path))
		if err != nil || command == nil || command.Name() != strings.Fields(path)[1] {
			t.Fatalf("published command %s disappeared: %v", path, err)
		}
		if !command.Hidden {
			t.Errorf("Community advertises %s in help", path)
		}
	}
}

func TestCommunityLegalHoldAndErasureRefusalIsTheEditionExit(t *testing.T) {
	for _, tc := range []struct {
		feature string
		args    []string
	}{
		{"Legal holds", []string{"compliance", "holds", "ls"}},
		{"Legal holds", []string{"compliance", "holds", "get", "lh-1"}},
		{"Legal holds", []string{"compliance", "holds", "place", "--matter", "CASE-1", "--reason", "litigation", "--scope", "tenant"}},
		{"Erasure", []string{"compliance", "erasure", "ls"}},
		{"Erasure", []string{"compliance", "erasure", "request", "--subject-ref", "u-7", "--case-ref", "DSAR-1"}},
		{"Erasure", []string{"compliance", "subject", "status", "u-7"}},
		{"Erasure", []string{"compliance", "subject", "erase", "u-7", "--yes"}},
	} {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			server := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotImplemented)
				_, _ = w.Write([]byte(`{"error":{"code":"business_required","message":"Legal holds and erasure require Olivares Business."}}`))
			})
			_, _, err := execRoot(t, lot3Args(server.URL, tc.args...)...)
			want := tc.feature + " is a Business feature: " + pricingURL
			if err == nil || exitcode.From(err) != exitcode.Edition || err.Error() != want {
				t.Fatalf("refusal = %v (exit %d); want %q and exit %d", err, exitcode.From(err), want, exitcode.Edition)
			}
		})
	}
}
