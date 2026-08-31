// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact_test

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/redact"
)

const (
	secretInput = "model=public; token=abcdef123456"
	cleanInput  = "model=claude-sonnet path=/v1/messages"
)

func TestCleanRedactsSecretAndLeavesCleanInputAlone(t *testing.T) {
	t.Run("redacts", func(t *testing.T) {
		got := redact.Clean(secretInput)
		if got != "model=public; token=[REDACTED]" {
			t.Fatalf("Clean() = %q, want the token replaced", got)
		}
		if strings.Contains(got, "abcdef123456") {
			t.Fatalf("Clean() leaked the token: %q", got)
		}
	})

	t.Run("no-fire", func(t *testing.T) {
		if got := redact.Clean(cleanInput); got != cleanInput {
			t.Fatalf("Clean() changed non-secret input to %q, want %q", got, cleanInput)
		}
	})
}

func TestScrubReportsOnlyActualRedaction(t *testing.T) {
	t.Run("redacts", func(t *testing.T) {
		got, redacted := redact.Scrub(secretInput)
		if !redacted {
			t.Fatal("Scrub() redacted = false for an input containing a token")
		}
		if got != "model=public; token=[REDACTED]" {
			t.Fatalf("Scrub() = %q, want the token replaced", got)
		}
	})

	t.Run("no-fire", func(t *testing.T) {
		got, redacted := redact.Scrub(cleanInput)
		if redacted {
			t.Fatalf("Scrub() redacted = true for non-secret input; output %q", got)
		}
		if got != cleanInput {
			t.Fatalf("Scrub() changed non-secret input to %q, want %q", got, cleanInput)
		}
	})
}

func TestContainsSecretDistinguishesSecretFromCleanInput(t *testing.T) {
	if !redact.ContainsSecret(secretInput) {
		t.Fatal("ContainsSecret() = false for an input containing a token")
	}
	if redact.ContainsSecret(cleanInput) {
		t.Fatal("ContainsSecret() = true for non-secret input")
	}
}
