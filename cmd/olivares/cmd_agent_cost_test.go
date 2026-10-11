// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// The cost line prints the model the tool named; a name carrying terminal escapes
// must not reach the operator's terminal as escapes.
func TestRunCostLineKeepsEscapesOutOfTheTerminal(t *testing.T) {
	line := runCostLine(map[string]any{
		"input_tokens": float64(10), "output_tokens": float64(2), "cost_micro_usd": float64(1500),
		"usage_model_ref": "gpt\x1b]0;owned\x07\x1b[2J",
	})
	if strings.ContainsAny(line, "\x1b\x07") {
		t.Fatalf("cost line carries terminal escapes: %q", line)
	}
	if !strings.Contains(line, "an estimate, not an invoice") {
		t.Fatalf("cost line = %q", line)
	}
}

// A tool that reports tokens and no money (Codex on a server with no list price)
// has an unknown cost: the line says so, never "0.0000 USD", which reads as free.
func TestRunCostLineSaysAnUnreportedCostIsUnknown(t *testing.T) {
	line := runCostLine(map[string]any{
		"input_tokens": float64(2000), "output_tokens": float64(12), "usage_model_ref": "gpt-6-astra",
	})
	if want := "cost not reported (tokens in 2000, out 12) on gpt-6-astra"; line != want {
		t.Fatalf("cost line = %q, want %q", line, want)
	}
}
