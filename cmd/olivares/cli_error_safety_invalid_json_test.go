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
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func cliSafetyUnicode(s string) string {
	var out strings.Builder
	for _, r := range s {
		fmt.Fprintf(&out, `\u%04x`, r)
	}
	return out.String()
}

func TestSRCLIRefusalInvalidJSONCannotExposeEscapedRequestBearer(t *testing.T) {
	const token = "sr-synthetic-effective-bearer-01234567"
	escaped := cliSafetyUnicode(token)
	for _, tail := range []string{" malformed-tail", ` {"other":true}`} {
		t.Run(tail, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "http://fixture.invalid/v1/m/sessions/runs", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			input := `{"error":{"message":"` + escaped + `"}}` + tail
			raw, err := readCLIResponse(&http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(input))}, req, 4096, false)
			if err != nil {
				t.Fatal(err)
			}
			refused := httpErr(403, raw)
			if exitcode.From(refused) != exitcode.Auth {
				t.Fatal("lost HTTP 403 refusal classification")
			}
			// A complete escaped first JSON value is still recoverable in a printed error with a bad tail.
			start := strings.Index(refused.Error(), "{")
			if start < 0 {
				return
			}
			var value map[string]any
			if json.NewDecoder(strings.NewReader(refused.Error()[start:])).Decode(&value) == nil {
				normalized, _ := json.Marshal(value)
				if bytes.Contains(normalized, []byte(token)) {
					t.Fatal("403 CLI diagnostic contains a recoverable escaped request bearer")
				}
			}
		})
	}
}

func TestSRCLIHealthyBytesAndCancellationStayExact(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://fixture.invalid", nil)
	req.Header.Set("Authorization", "Bearer sr-synthetic-effective-bearer-01234567")
	raw := []byte("accepted\x00binary\xffbytes")
	got, err := readCLIResponse(&http.Response{Body: io.NopCloser(bytes.NewReader(raw))}, req, 4096, true)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("accepted bytes changed")
	}
	cause := fmt.Errorf("reflected sr-synthetic-effective-bearer-01234567: %w", context.Canceled)
	safe := redactCoded(cause, cliRequestSecrets(req)...)
	if !errors.Is(safe, context.Canceled) || strings.Contains(safe.Error(), "sr-synthetic-effective-bearer-01234567") {
		t.Fatal("redaction lost cancellation or bearer safety")
	}
}

func TestSRCLIActualSessionListRefusesEscapedBearerWithUnsafeTails(t *testing.T) {
	const token = "sr-synthetic-effective-bearer-01234567"
	t.Setenv("OLIVARES_TOKEN", token)
	for _, mode := range []string{"text", "json"} {
		for _, tail := range []string{" trailing-garbage", ` {"second":true}`} {
			t.Run(mode+tail, func(t *testing.T) {
				var contacted atomic.Bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					contacted.Store(true)
					if r.Header.Get("Authorization") != "Bearer "+token {
						t.Error("effective credential did not reach local fixture")
					}
					w.WriteHeader(403)
					io.WriteString(w, `{"error":{"code":"forbidden","message":"`+cliSafetyUnicode(token)+`"}}`+tail)
				}))
				defer srv.Close()
				out, stderr, err := execSessionCLI(t, nil, "agent", "session", "ls", "--server", srv.URL, "--tenant", "sr-own-tenant", "-o", mode)
				if !contacted.Load() || exitcode.From(err) != exitcode.Auth {
					t.Fatal("403 fixture was not reached/refused with its auth classification")
				}
				printed := out + stderr + err.Error()
				if strings.Contains(printed, cliSafetyUnicode(token)) || strings.Contains(printed, token) {
					t.Fatal("actual session ls printed the reversible escaped bearer")
				}
			})
		}
	}
}

func TestCLIErrorSafetyUnparseableRefusalCannotExposeEscapedBearer(t *testing.T) {
	const token = "fixture-malformed-response-bearer"
	escaped := cliSafetyUnicode(token)
	for _, input := range []string{
		`{"error":{"message":"` + escaped + `"}`,
		`proxy reflected "` + escaped + `"`,
	} {
		t.Run(input, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			raw, err := readCLIResponse(&http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(input))}, req, 4096, false)
			if err != nil {
				t.Fatal(err)
			}
			refused := httpErr(http.StatusForbidden, raw)
			if exitcode.From(refused) != exitcode.Auth || strings.Contains(refused.Error(), escaped) || strings.Contains(refused.Error(), token) {
				t.Fatal("unparseable refusal lost its error or exposed a reversible request bearer")
			}
		})
	}
}

func TestCLIErrorSafetyUncredentialedRefusalKeepsOriginalDetails(t *testing.T) {
	for _, input := range []string{"plain proxy refusal", `{"error":"refused"} trailing`, `{"error":"refused"} {"other":true}`} {
		req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
		raw, err := readCLIResponse(&http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(input))}, req, 4096, false)
		if err != nil || string(raw) != input {
			t.Fatalf("uncredentialed refusal details changed: %q, %v", raw, err)
		}
	}
}
