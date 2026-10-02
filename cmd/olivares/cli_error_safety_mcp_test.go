// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCLIErrorSafetyMCPDriftFormatterRetainsRequestGuard(t *testing.T) {
	const token = "fixture-mcp-approval-bearer"
	t.Setenv("OLIVARES_TOKEN", token)
	for _, tool := range []string{"stable-tool", token, cliSafetyUnicode(token)} {
		for _, mode := range []string{"text", "json"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				var contacted atomic.Bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != mcpToolPinsPath+"/approve" {
						t.Error("approval fixture did not receive its request context")
					}
					contacted.Store(true)
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, `{"error":"test failure"}`)
				}))
				defer srv.Close()
				_, _, err := execSessionCLI(t, nil, "mcp", "pins", "approve", tool, "--from-drift", "--server", srv.URL, "--tenant", "fixture-tenant", "-o", mode)
				if !contacted.Load() || exitcode.From(err) != exitcode.Conflict {
					t.Fatal("approval lost its conflict classification or fixture contact")
				}
				if tool == "stable-tool" {
					if !strings.Contains(err.Error(), "no current drift") {
						t.Fatal("the guarded reader bypassed the operation-specific drift formatter")
					}
				} else if err.Error() != "response details withheld (HTTP 409)" {
					t.Fatal("the drift formatter released a literal or escaped request credential")
				}
				// The operation-specific formatter must also retain credentials for
				// a subsequent outer formatter and the last output check.
				outer := fmt.Errorf("outer formatter: %s: %w", cliSafetyUnicode(token), err)
				var out bytes.Buffer
				if writeErr := printCLIError(&out, outer); writeErr != nil || out.String() != "Error: response details withheld (HTTP 409)\n" {
					t.Fatal("the drift formatter lost request context before the final write")
				}
			})
		}
	}
}

func TestCLIErrorSafetyRedactedCauseRetainsFinalWriteCredentials(t *testing.T) {
	const firstKey = "fixture-first-request-key"
	const secondKey = "fixture-second-request-key"
	cause := guardCLIRefusalError(httpErr(http.StatusConflict, []byte("ordinary refusal")), http.StatusConflict, []string{firstKey, secondKey})
	outer := fmt.Errorf("outer: %s %s: %w", firstKey, cliSafetyUnicode(secondKey), cause)
	safe := redactCoded(outer, firstKey)
	var refusal *cliRefusalError
	if !errors.Is(safe, cause) || !errors.As(safe, &refusal) || exitcode.From(safe) != exitcode.Conflict {
		t.Fatal("scrubbed error lost its cause/type/classification")
	}
	for unwrapped := safe; unwrapped != nil; unwrapped = errors.Unwrap(unwrapped) {
		if strings.Contains(unwrapped.Error(), firstKey) {
			t.Fatal("scrubbed error exposed the original credential through Unwrap")
		}
	}
	var out bytes.Buffer
	if err := printCLIError(&out, safe); err != nil || out.String() != "Error: response details withheld (HTTP 409)\n" {
		t.Fatal("closing public Unwrap lost the final guard's second request credential")
	}
}
