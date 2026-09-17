// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const inferenceProxyFirewallStatusPath = "/v1/m/inferenceproxy/content-firewall"

func newInferenceProxyFirewallStub(t *testing.T, status int, body string, requests *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInferenceProxyFirewallStatusRendersTheServedDocument(t *testing.T) {
	const body = `{"pep":"messages_proxy","state":"inspector_attached","note":"Startup attachment for this process. This state does not report listener health or per-request inspection."}`
	var want map[string]any
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatal(err)
	}

	prepareModelstackCLITest(t)
	var requests []string
	srv := newInferenceProxyFirewallStub(t, http.StatusOK, body, &requests)
	stdout, stderr, err := execRoot(t, "inference-proxy", "firewall", "status",
		"--server", srv.URL, "--token", "secret-token", "--tenant", "tenant-a", "-o", "json")
	if err != nil {
		t.Fatalf("firewall status -o json: %v (stderr %q)", err, stderr)
	}
	if !reflect.DeepEqual(requests, []string{http.MethodGet + " " + inferenceProxyFirewallStatusPath}) {
		t.Fatalf("requests = %v", requests)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not the served JSON document: %v\n%s", err, stdout)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stdout document = %v, want %v", got, want)
	}

	requests = nil
	stdout, stderr, err = execRoot(t, "inference-proxy", "firewall", "status",
		"--server", srv.URL, "--token", "secret-token", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("firewall status: %v (stderr %q)", err, stderr)
	}
	for _, fragment := range []string{"messages_proxy", "inspector_attached"} {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("human output lacks %q:\n%s", fragment, stdout)
		}
	}
}

func TestInferenceProxyFirewallStatusOnAnEngineWithoutTheRouteExitsNotFound(t *testing.T) {
	prepareModelstackCLITest(t)
	var requests []string
	srv := newInferenceProxyFirewallStub(t, http.StatusNotFound, `{"error":{"message":"not found"}}`, &requests)
	stdout, _, err := execRoot(t, "inference-proxy", "firewall", "status",
		"--server", srv.URL, "--token", "secret-token", "--tenant", "tenant-a", "-o", "json")
	assertExitCode(t, err, 4, "firewall status against an engine without the route")
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("a missing route printed a document: %q", stdout)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %v, want exactly one", requests)
	}
}

func TestInferenceProxyFirewallStatusHelpNamesTheDenyAllFallback(t *testing.T) {
	stdout, stderr, err := execRoot(t, "inference-proxy", "firewall", "status", "--help")
	if err != nil {
		t.Fatalf("help: %v (stderr %q)", err, stderr)
	}
	for _, fragment := range []string{
		"unobserved", "pep_not_composed", "inspector_absent", "inspector_attached",
		"deny-all fallback", "inspector_attached is not evidence",
		"listener health", "404",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("help lacks %q:\n%s", fragment, stdout)
		}
	}
}
