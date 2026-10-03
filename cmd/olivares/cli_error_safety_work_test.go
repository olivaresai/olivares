// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// SR's complete 82 KiB refusal exposes one more escape on every decode pass.
// An inconclusive scan must withhold details rather than print or skip them.
func TestSRCLIRefusalEscapeScanHasBoundedWorkOnSmallResponse(t *testing.T) {
	const token = "sr-synthetic-request-bearer-without-reflection"
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	input := `{"error":{"message":"\\` + strings.Repeat("u005c", 16384) + `unrelated"}}`
	raw, err := readCLIResponse(&http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(input))}, req, 8<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "response details withheld" {
		t.Fatalf("an escape chain requiring 16384 decode passes remained printable (%d bytes)", len(raw))
	}
	withhold, scan := scanCLIOutputCredentials(input, cliRequestSecrets(req))
	if !withhold || !scan.limited || scan.decodePasses == 0 || scan.decodePasses > 8 || scan.bytesProcessed > uint64(len(input))*64 {
		t.Fatalf("escape work was not bounded: withhold=%t scan=%+v", withhold, scan)
	}
	t.Logf("complete refusal bytes=%d decode passes=%d charged bytes=%d budget=%d", len(input), scan.decodePasses, scan.bytesProcessed, scan.maxBytes)
	// The same lower-trust bytes on an accepted response bypass refusal scanning.
	accepted, err := readCLIResponse(&http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(input))}, req, 8<<20, true)
	if err != nil || string(accepted) != input {
		t.Fatal("bounded refusal scanning changed accepted raw bytes")
	}
}

func TestCLIErrorSafetyEscapeDepthAndWorkLimitsWithhold(t *testing.T) {
	const key = "fixture-request-key"
	deep := `\` + strings.Repeat("u005c", 32) + `u0066ixture-request-key`
	t.Run("depth", func(t *testing.T) {
		withhold, scan := scanCLIOutputCredentials(deep, []string{key})
		if !withhold || !scan.limited || scan.decodePasses != 8 || scan.bytesProcessed > uint64(len(deep))*64 {
			t.Fatalf("depth limit did not withhold a still-recoverable credential: %+v", scan)
		}
	})
	t.Run("work", func(t *testing.T) {
		const input = "unrelated integer 9007199254740993"
		secrets := make([]string, 64)
		for i := range secrets {
			secrets[i] = fmt.Sprintf("fixture-other-request-key-%d", i)
		}
		withhold, scan := scanCLIOutputCredentials(input, secrets)
		if !withhold || !scan.limited || scan.decodePasses != 0 || scan.bytesProcessed != uint64(len(input))*64 {
			t.Fatalf("work budget did not stop full-string searches: %+v", scan)
		}
	})
	for _, control := range []struct {
		name, output string
		withhold     bool
	}{
		{"credential-on-last-pass", `\` + strings.Repeat("u005c", 7) + `u0066ixture-request-key`, true},
		{"clean-on-last-pass", `\` + strings.Repeat("u005c", 7) + `u0066ixture-evidence`, false},
	} {
		t.Run(control.name, func(t *testing.T) {
			withhold, scan := scanCLIOutputCredentials(control.output, []string{key})
			if withhold != control.withhold || scan.limited || scan.decodePasses != 8 || scan.bytesProcessed > uint64(len(control.output))*64 {
				t.Fatalf("last permitted decode pass lost its credential/clean check: %+v", scan)
			}
		})
	}
	t.Run("no-request-credentials", func(t *testing.T) {
		withhold, scan := scanCLIOutputCredentials(deep, nil)
		if withhold || scan.limited || scan.bytesProcessed != 0 || scan.decodePasses != 0 {
			t.Fatal("uncredentialed details entered the credential decoder")
		}
	})
}

type cliWorkWriteRecorder struct {
	bytes.Buffer
	writes int
}

func (w *cliWorkWriteRecorder) Write(body []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(body)
}

func TestCLIErrorSafetyFinalWriteWithholdsOnEscapeLimits(t *testing.T) {
	const key = "fixture-request-key"
	deep := `\` + strings.Repeat("u005c", 16384) + `u0066ixture-request-key`
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	for _, limit := range []struct {
		name    string
		secrets []string
	}{
		{"depth", []string{key}},
		{"work", cliRequestSecrets(req)},
	} {
		for _, mode := range []string{"text", "json"} {
			t.Run(limit.name+"/"+mode, func(t *testing.T) {
				cause := exitcode.New(exitcode.Auth, fmt.Errorf("fixture refusal: %w", context.Canceled))
				refusal := guardCLIRefusalError(cause, http.StatusForbidden, limit.secrets)
				// Only the outer formatter introduces the lower-trust escape chain.
				outer := fmt.Errorf("outer formatter: %s: %w", deep, refusal)
				var printable error = outer
				if mode == "json" {
					encoded, _ := json.Marshal(map[string]string{"error": outer.Error()})
					printable = &cliSafeError{message: string(encoded), cause: outer}
				}
				final := string(fmt.Appendln(nil, "Error:", printable))
				withhold, scan := scanCLIOutputCredentials(final, limit.secrets)
				if !withhold || !scan.limited || scan.bytesProcessed > uint64(len(final))*64 || scan.decodePasses > 8 {
					t.Fatalf("outer diagnostic did not exhaust a bounded scan: %+v", scan)
				}
				if limit.name == "depth" && scan.decodePasses != 8 || limit.name == "work" && scan.decodePasses >= 8 {
					t.Fatalf("did not independently exercise the %s limit: %+v", limit.name, scan)
				}
				var out cliWorkWriteRecorder
				if err := printCLIError(&out, printable); err != nil {
					t.Fatal(err)
				}
				if out.String() != "Error: response details withheld (HTTP 403)\n" || out.writes != 1 ||
					!errors.Is(printable, cause) || !errors.Is(printable, context.Canceled) || exitcode.From(printable) != exitcode.Auth {
					t.Fatal("budget exhaustion changed final-write/status/cause custody")
				}
			})
		}
	}
}
