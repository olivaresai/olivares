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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCLIErrorSafetyAgentSessionReflection(t *testing.T) {
	const bearer = "fixture-effective-bearer-403"
	t.Setenv("OLIVARES_TOKEN", bearer)
	for _, status := range []int{http.StatusForbidden, http.StatusCreated, http.StatusAccepted} {
		for _, raw := range []bool{false, true} {
			for _, mode := range []string{"text", "json"} {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					if token != bearer {
						t.Errorf("request used a different credential")
					}
					w.WriteHeader(status)
					if raw {
						_, _ = io.WriteString(w, "proxy reflected "+token)
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "forbidden", "message": "reflected " + token}})
					}
				}))
				out, stderr, err := execSessionCLI(t, nil, "agent", "session", "ls", "--server", srv.URL, "--tenant", "tenant-a", "-o", mode)
				srv.Close()
				want := exitcode.Err
				if status == http.StatusForbidden {
					want = exitcode.Auth
				}
				if exitcode.From(err) != want {
					t.Fatalf("HTTP %d exited %d, want %d: %v", status, exitcode.From(err), want, err)
				}
				if strings.Contains(out+stderr+err.Error(), bearer) {
					t.Fatalf("raw=%t, mode=%s: reflected bearer reached CLI output", raw, mode)
				}
			}
		}
	}
}

func TestCLIErrorSafetyProviderBodySecretReflection(t *testing.T) {
	const bearer = "fixture-effective-admin-bearer"
	const key = `fixture-provider-"quoted"-credential`
	t.Setenv("OLIVARES_TOKEN", bearer)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": string(raw)}})
	}))
	defer srv.Close()
	out, stderr, err := execSessionCLI(t, strings.NewReader(key), "provider", "add", "--kind", "anthropic", "--name", "fixture", "--server", srv.URL, "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.Auth {
		t.Fatalf("HTTP 403 exited %d, want 3: %v", exitcode.From(err), err)
	}
	escaped, _ := json.Marshal(key)
	if strings.Contains(out+stderr+err.Error(), key) || strings.Contains(out+stderr+err.Error(), string(escaped[1:len(escaped)-1])) {
		t.Fatal("request-body credential reached CLI output")
	}
}

func TestCLIErrorSafetyAgentTokenFileOverridesAmbientBearer(t *testing.T) {
	t.Setenv("OLIVARES_TOKEN", "fixture-stale-environment-token")
	const bearer = "fixture-private-token-file-value"
	file := filepath.Join(t.TempDir(), "bearer")
	if err := os.WriteFile(file, []byte(bearer+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			t.Error("token file did not override the ambient bearer")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "reflected "+bearer)
	}))
	defer srv.Close()
	out, stderr, err := execSessionCLI(t, nil, "agent", "session", "ls", "--server", srv.URL, "--tenant", "tenant-a", "--token-file", file)
	if exitcode.From(err) != exitcode.Auth {
		t.Fatalf("token-file request exited %d: %v", exitcode.From(err), err)
	}
	if strings.Contains(out+stderr+err.Error(), bearer) {
		t.Fatal("token-file bearer reached output")
	}
}

func TestCLIErrorSafetyBufferedReadKeepsRawSuccessAndStatus(t *testing.T) {
	const bearer = "fixture-success-bearer"
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid/raw", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	payload := []byte("\x00raw-file:" + bearer + "\n")
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload))}
	raw, err := readCLIResponse(resp, req, 1024, resp.StatusCode == http.StatusOK)
	if err != nil || !bytes.Equal(raw, payload) {
		t.Fatal("successful raw file bytes changed")
	}
	for status, want := range map[int]int{401: 3, 403: 3, 404: 4, 409: 5, 500: 6} {
		resp := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"` + bearer + `"},"sequence":9007199254740993}`))}
		raw, err := readCLIResponse(resp, req, 1024, resp.StatusCode == http.StatusOK)
		if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) {
			t.Fatal("refusal read changed its numeric evidence")
		}
		refusal := httpErr(status, raw)
		if exitcode.From(refusal) != want || strings.Contains(refusal.Error(), bearer) {
			t.Fatalf("status %d lost classification/redaction", status)
		}
	}
}

type cliSafetyReadFailure struct{ failure error }

func (r cliSafetyReadFailure) Read(p []byte) (int, error) {
	return copy(p, "partial fixture-bearer-read-error"), r.failure
}
func TestCLIErrorSafetyBufferedReadFailureAndCancellation(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
	req.Header.Set("Authorization", "Bearer fixture-bearer-read-error")
	for _, failure := range []error{context.Canceled, errors.New("reflected fixture-bearer-read-error")} {
		resp := &http.Response{StatusCode: 403, Body: io.NopCloser(cliSafetyReadFailure{failure})}
		raw, err := readCLIResponse(resp, req, 1024, resp.StatusCode == http.StatusOK)
		if err == nil || len(raw) != 0 || strings.Contains(err.Error(), "fixture-bearer-read-error") {
			t.Fatal("partial failure exposed credential bytes")
		}
		if failure == context.Canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("caller cancellation lost its cause")
		}
	}
}

func TestCLIErrorSafetyCappedErrorWithholdsCredentialPrefixes(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
	req.Header.Set("Authorization", "Bearer fixture-long-bearer-value")
	resp := &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("proxy: fixture-long-bearer-value extra"))}
	raw, err := readCLIResponse(resp, req, 20, resp.StatusCode == http.StatusOK)
	if err != nil || bytes.Contains(raw, []byte("fixture-long")) {
		t.Fatal("capped error exposed a credential prefix")
	}
	if exitcode.From(httpErr(403, raw)) != 3 {
		t.Fatal("capped error lost HTTP status classification")
	}
}

func TestCLIErrorSafetyTokenFileUsesSavedContextAndStdin(t *testing.T) {
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TENANT", "")
	t.Setenv("OLIVARES_TOKEN", "fixture-stale-env-token")
	const bearer = "fixture-stdin-file-bearer"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bearer || r.Header.Get("X-Olivares-Tenant") != "tenant-fixture" {
			t.Error("file/context precedence changed")
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer srv.Close()
	config := filepath.Join(t.TempDir(), "client.yaml")
	t.Setenv(cliConfigOverrideEnv, config)
	if err := writeCLIConfig(config, cliConfig{CurrentContext: "fixture", Contexts: []cliContext{{Name: "fixture", Server: srv.URL, Token: "fixture-saved-token", Tenant: "tenant-fixture"}}}); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := execSessionCLI(t, strings.NewReader(bearer+"\n"), "agent", "session", "ls", "--token-file", "-")
	if err != nil || strings.Contains(out+stderr, bearer) {
		t.Fatalf("stdin file request: %v", err)
	}
	_, _, err = execSessionCLI(t, nil, "agent", "session", "ls", "--token", "")
	if exitcode.From(err) != exitcode.Usage {
		t.Fatal("explicit empty credential did not clear env/context")
	}
	_, _, err = execSessionCLI(t, strings.NewReader(bearer), "agent", "session", "ls", "--token", "fixture-argv-value", "--token-file", "-")
	if exitcode.From(err) != exitcode.Usage {
		t.Fatal("conflicting credential sources were not refused")
	}
}

func TestCLIErrorSafetyRedactionKeepsReflectedTransportCauses(t *testing.T) {
	const secret = "fixture-query-credential"
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		original := &url.Error{Op: "Get", URL: "https://fixture.invalid?token=" + secret, Err: cause}
		safe := redactCoded(exitcode.New(exitcode.Server, original), secret)
		var typed *url.Error
		if strings.Contains(safe.Error(), secret) || exitcode.From(safe) != exitcode.Server || !errors.Is(safe, cause) || !errors.As(safe, &typed) {
			t.Fatalf("redaction lost transport classification/cause: %v", safe)
		}
		req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp := &http.Response{StatusCode: 403, Body: io.NopCloser(cliSafetyReadFailure{fmt.Errorf("reflected %s: %w", secret, cause)})}
		_, err := readCLIResponse(resp, req, 1024, resp.StatusCode == http.StatusOK)
		if strings.Contains(err.Error(), secret) || !errors.Is(err, cause) {
			t.Fatalf("read redaction lost cause: %v", err)
		}
	}
}

func TestCLIErrorSafetyAcceptedMutationKeepsItsOriginalBody(t *testing.T) {
	const bearer = "fixture-accepted-bearer"
	req, _ := http.NewRequest(http.MethodPost, "http://fixture.invalid", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	for _, status := range []int{http.StatusCreated, http.StatusAccepted} {
		body := `{"receipt":"` + bearer + `"}`
		resp := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
		raw, err := readCLIResponse(resp, req, 1024, cliStatusAccepted(status, http.StatusCreated, http.StatusAccepted))
		if err != nil || string(raw) != body {
			t.Fatalf("accepted mutation bytes changed: %q, %v", raw, err)
		}
	}
}

func TestCLIErrorSafetyUninspectedLargeRequestWithholdsRefusal(t *testing.T) {
	const key = "fixture-body-key-after-large-field"
	body := `{"padding":"` + strings.Repeat("x", maxBootstrapCLIResponseSize) + `","api_key":"` + key + `"}`
	req, _ := http.NewRequest(http.MethodPost, "http://fixture.invalid", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("reflected " + key))}
	raw, err := readCLIResponse(resp, req, 1024, false)
	if err != nil || strings.Contains(string(raw), key) {
		t.Fatalf("uninspected body credential leaked: %q, %v", raw, err)
	}
}

func TestCLIErrorSafetyHALabelRefusalDoesNotEchoItsServiceBearer(t *testing.T) {
	const bearer = "fixture-service-account-token"
	file := filepath.Join(t.TempDir(), "service-token")
	if err := os.WriteFile(file, []byte(bearer), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			t.Error("service bearer changed")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "reflected "+bearer)
	}))
	defer srv.Close()
	p := &haLeaderPublisher{base: srv.URL, pod: "fixture-pod", namespace: "fixture", tokenFile: file, client: srv.Client()}
	err := p.publish(context.Background(), haRoleLeader)
	if err == nil || strings.Contains(err.Error(), bearer) || !strings.Contains(err.Error(), "403") {
		t.Fatalf("label refusal lost status or echoed its bearer: %v", err)
	}
}
