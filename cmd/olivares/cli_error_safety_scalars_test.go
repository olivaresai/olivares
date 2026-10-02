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

// The provider key is a string in the request. A refusal may echo its literal
// bytes as a JSON number, without any encoding or reconstruction.
func TestSRCLIRequestKeyCannotEscapeRedactionAsJSONNumber(t *testing.T) {
	const key = "3141592653589793"
	req, err := http.NewRequest(http.MethodPost, "http://fixture.invalid/provider", strings.NewReader(`{"api_key":"`+key+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	input := `{"error":{"message":` + key + `},"sequence":9007199254740993}`
	raw, err := readCLIResponse(&http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(input))}, req, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	refused := httpErr(http.StatusForbidden, raw)
	if exitcode.From(refused) != exitcode.Auth {
		t.Fatal("lost refusal exit classification")
	}
	if strings.Contains(refused.Error(), key) {
		t.Fatal("a known request api_key survived as a literal JSON number in the refusal")
	}
}

func TestSRCLIActualProviderAddCannotPrintNumericRequestKey(t *testing.T) {
	const key = "3141592653589793"
	const bearer = "sr-effective-synthetic-admin-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			var contacted atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["api_key"] != key || r.Header.Get("Authorization") != "Bearer "+bearer {
					t.Error("fixture did not receive the effective string key and bearer")
				}
				contacted.Store(true)
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":{"message":`+key+`},"sequence":9007199254740993}`)
			}))
			defer srv.Close()
			out, stderr, err := execSessionCLI(t, strings.NewReader(key), "provider", "add", "--kind", "openai_compatible", "--name", "sr-fixture", "--base-url", "http://fixture.invalid", "--server", srv.URL, "--tenant", "sr-own-tenant", "-o", mode)
			if !contacted.Load() || exitcode.From(err) != exitcode.Auth {
				t.Fatal("actual provider command did not reach/refuse the owned fixture")
			}
			if strings.Contains(out+stderr+err.Error(), key) {
				t.Fatal("actual provider add printed the full request API key as a JSON number")
			}
		})
	}
}

// The formatter joins individually harmless fields into a reversible credential.
// Scanning only the serialized response cannot see this final diagnostic.
func TestCLIErrorSafetyFinalFormatterCannotAssembleAnEscapedCredential(t *testing.T) {
	const bearer = "fixture-hold  matter fixture-matter  scope fixture-scope"
	const reflected = `\u0066ixture-hold  matter fixture-matter  scope fixture-scope`
	t.Setenv("OLIVARES_TOKEN", bearer)
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			var contacted atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+bearer {
					t.Error("fixture did not receive the effective bearer")
				}
				contacted.Store(true)
				w.WriteHeader(http.StatusLocked)
				_, _ = io.WriteString(w, `{"error":{"code":"legal_hold","holds":[{"id":"\\u0066ixture-hold","matter_ref":"fixture-matter","scope_kind":"fixture-scope"}]}}`)
			}))
			defer srv.Close()
			out, stderr, err := execSessionCLI(t, nil, "compliance", "erasure", "execute", "er-1", "--yes", "--server", srv.URL, "--tenant", "fixture-tenant", "-o", mode)
			if !contacted.Load() || exitcode.From(err) != exitcode.Conflict {
				t.Fatal("owned 423 fixture lost its contact or conflict classification")
			}
			if strings.Contains(out+stderr+err.Error(), reflected) || strings.Contains(out+stderr+err.Error(), bearer) {
				t.Fatal("the final refusal formatter assembled a recoverable request credential")
			}
		})
	}
}

func TestCLIErrorSafetyFinalWriteRunsAfterEveryRefusalFormatter(t *testing.T) {
	formatters := []struct {
		name   string
		status int
		code   int
		format func(int, []byte) error
	}{
		{"http", 403, exitcode.Auth, httpErr},
		{"bootstrap", 403, exitcode.Auth, bootstrapHTTPError},
		{"datalane", 403, exitcode.Auth, func(status int, body []byte) error { return datalaneHTTPError("knowledge", status, body) }},
		{"observe", 501, exitcode.Err, observeHTTPError},
		{"work", 422, exitcode.Conflict, workHTTPError},
		{"modelstack", 402, exitcode.Conflict, func(status int, body []byte) error {
			return modelstackHTTPError(modelstackResult{Status: status, Raw: body})
		}},
		{"agentexec", 422, exitcode.Usage, agentExecHTTPError},
		{"compliance", 423, exitcode.Conflict, complianceHTTPError},
		{"mcp", 501, exitcode.Edition, mcpPinsHTTPError}, // RU-02: a Business feature answers exit 9
		{"mirror", 403, exitcode.Err, func(status int, body []byte) error { return fmt.Errorf("gate returned %d: %s", status, body) }},
		{"ha", 403, exitcode.Err, func(status int, body []byte) error { return fmt.Errorf("patch pod label: %d: %s", status, body) }},
	}
	forms := []struct{ name, secret, output string }{
		{"literal-number", "3141592653589793", "3141592653589793"},
		{"json-string", `fixture-"quoted"-key`, `fixture-\"quoted\"-key`},
		{"unicode", "fixture-request-key", cliSafetyUnicode("fixture-request-key")},
		{"mixed-unicode", "fixture-request-key", `\u0066ixture-request-key`},
		{"uppercase-hex", "fixture-request-key", `fixture\u002Drequest-key`},
		{"surrogate-pair", "fixture-🐘-key", `fixture-\ud83d\udc18-key`},
		{"slash", "fixture/key/value", `fixture\/key\/value`},
		{"nested-json-string", "fixture-request-key", `\\u0066ixture-request-key`},
		{"invalid-escape-beside-key", "fixture-request-key", `unknown\q \u0066ixture-request-key`},
	}
	for _, formatter := range formatters {
		for _, form := range forms {
			for _, mode := range []string{"text", "json"} {
				t.Run(formatter.name+"/"+form.name+"/"+mode, func(t *testing.T) {
					request, _ := json.Marshal(map[string]string{"api_key": form.secret})
					req, _ := http.NewRequest(http.MethodPost, "http://fixture.invalid", bytes.NewReader(request))
					req.Header.Set("Content-Type", "application/json")
					resp := &http.Response{StatusCode: formatter.status, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unrelated detail"}}`))}
					_, refusal := readCLIHTTPResponse(resp, req, 4096, false, formatter.format)
					if refusal == nil || exitcode.From(refusal) != formatter.code {
						t.Fatal("refusal lost its original classification")
					}
					// The credential is introduced AFTER the request-aware refusal
					// formatter. Only checking the final write can stop this witness.
					outer := fmt.Errorf("outer formatter: %s: %w", form.output, refusal)
					var printable error = outer
					if mode == "json" {
						encoded, _ := json.Marshal(map[string]string{"error": outer.Error()})
						printable = &cliSafeError{message: string(encoded), cause: outer}
					}
					var out bytes.Buffer
					if err := printCLIError(&out, printable); err != nil {
						t.Fatal(err)
					}
					want := fmt.Sprintf("Error: response details withheld (HTTP %d)\n", formatter.status)
					if out.String() != want || !errors.Is(printable, refusal) || exitcode.From(printable) != formatter.code {
						t.Fatalf("final bytes or retained status/cause changed: %q", out.String())
					}
				})
			}
		}
	}
}

func TestCLIErrorSafetyFinalWriteChecksAllJoinedRequestCredentials(t *testing.T) {
	first := guardCLIRefusalError(httpErr(403, []byte("first refusal")), 403, []string{"fixture-first-key"})
	second := guardCLIRefusalError(httpErr(409, []byte("second refusal")), 409, []string{"fixture-second-key"})
	err := fmt.Errorf("final formatter: fixture-second-key: %w", errors.Join(first, second))
	var out bytes.Buffer
	if writeErr := printCLIError(&out, err); writeErr != nil {
		t.Fatal(writeErr)
	}
	if out.String() != "Error: response details withheld (HTTP 403)\n" || !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatal("the last output check skipped a joined request credential or flattened causes")
	}
}

func TestCLIErrorSafetyFinalScanPreservesUnrelatedIntegersAndAcceptedBytes(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://fixture.invalid", strings.NewReader(`{"api_key":"3141592653589793"}`))
	req.Header.Set("Content-Type", "application/json")
	input := `{"error":{"message":9007199254740993},"sequence":9007199254740993}`
	resp := &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(input))}
	raw, refusal := readCLIHTTPResponse(resp, req, 4096, false, httpErr)
	if refusal == nil || !bytes.Contains(raw, []byte("9007199254740993")) {
		t.Fatal("a credential-free integer was lost or rounded")
	}
	var out bytes.Buffer
	if err := printCLIError(&out, refusal); err != nil || !strings.Contains(out.String(), "9007199254740993") || strings.Contains(out.String(), "withheld") {
		t.Fatal("the final output guard changed unrelated integer evidence")
	}
	payload := []byte("accepted\x00binary\xff3141592653589793")
	resp = &http.Response{StatusCode: 201, Body: io.NopCloser(bytes.NewReader(payload))}
	raw, err := readCLIHTTPResponse(resp, req, 4096, true, func(int, []byte) error { t.Fatal("formatted an accepted response"); return nil })
	if err != nil || !bytes.Equal(raw, payload) {
		t.Fatal("the final refusal scan changed accepted raw bytes")
	}
	resp = &http.Response{StatusCode: 403, Body: io.NopCloser(cliSafetyReadFailure{failure: context.Canceled})}
	_, err = readCLIHTTPResponse(resp, req, 4096, false, httpErr)
	if !errors.Is(err, context.Canceled) || !errors.Is(wrapCLIResponseReadError(err, "read response"), context.Canceled) {
		t.Fatal("the request-aware seam lost a read cancellation cause")
	}
}

func TestCLIErrorSafetyMessageRefusalKeepsItsGenericExitClassification(t *testing.T) {
	t.Setenv("OLIVARES_COMMUNICATION_TOKEN", "fixture-communication-bearer")
	var contacted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-communication-bearer" {
			t.Error("message request changed its credential")
		}
		contacted.Store(true)
		w.WriteHeader(http.StatusRequestTimeout)
		_, _ = io.WriteString(w, `{"error":{"message":"request timeout"}}`)
	}))
	defer srv.Close()
	_, _, err := execSessionCLI(t, nil, "message", "get", testWorkItemID, "--server", srv.URL, "--tenant", "fixture-tenant")
	if !contacted.Load() || exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "408") {
		t.Fatalf("HTTP 408 message refusal lost its generic classification: %v", err)
	}
}
