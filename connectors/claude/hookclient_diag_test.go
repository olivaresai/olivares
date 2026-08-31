// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDenyClosedCauseReachesDiagAndNeverTheAgent pins both halves of the split. The decision the
// agent enforces is a CONTRACT — widening its reason to carry transport detail would leak engine
// internals to the agent, which the loopback design exists to prevent — while an operator with no
// cause at all cannot tell a closed port from a certificate that does not verify, and the remedy
// for each is different. Measured 2026-08-19: client.Do's error was discarded entirely.
func TestDenyClosedCauseReachesDiagAndNeverTheAgent(t *testing.T) {
	var out, diag bytes.Buffer
	cfg := HookClientConfig{Endpoint: "http://127.0.0.1:1/", Diag: &diag}
	if err := RunHookClient(context.Background(), strings.NewReader("{}"), &out, cfg); err != nil {
		t.Fatalf("the client reports an error only when the WRITE fails, got %v", err)
	}
	if !strings.Contains(out.String(), "deny") {
		t.Fatalf("an unreachable PEP must deny closed, got %s", out.String())
	}
	if strings.Contains(out.String(), "connection refused") || strings.Contains(out.String(), "127.0.0.1:1") {
		t.Fatalf("the AGENT must never receive transport detail, got %s", out.String())
	}
	if !strings.Contains(diag.String(), "connection refused") {
		t.Fatalf("the OPERATOR must receive the cause, got %q", diag.String())
	}
}

// TestDiagDistinguishesTheFailureItReports is the case the feature exists for: two different
// failures used to produce one identical message.
func TestDiagDistinguishesTheFailureItReports(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	var out, diag bytes.Buffer
	cfg := HookClientConfig{Endpoint: srv.URL, Diag: &diag}
	if err := RunHookClient(context.Background(), strings.NewReader("{}"), &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diag.String(), "HTTP 418") {
		t.Fatalf("an error STATUS must be reported as such, not as «unreachable»: %q", diag.String())
	}
	if strings.Contains(diag.String(), "unreachable") {
		t.Fatalf("a reachable PEP that answered 418 is not unreachable: %q", diag.String())
	}
}

// TestNilDiagStaysSilent keeps every existing caller unchanged: a nil sink must not panic.
func TestNilDiagStaysSilent(t *testing.T) {
	var out bytes.Buffer
	cfg := HookClientConfig{Endpoint: "http://127.0.0.1:1/"}
	if err := RunHookClient(context.Background(), strings.NewReader("{}"), &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deny") {
		t.Fatalf("still deny-closed with no diag sink, got %s", out.String())
	}
}
