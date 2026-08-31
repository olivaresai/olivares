// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHookPEPValidatePrintsServerVerdictAndMapsExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/governance/pdp/validate" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body struct {
			Engine string `json:"engine"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Engine != "cedar" {
			t.Errorf("engine = %q", body.Engine)
		}
		w.Header().Set("Content-Type", "application/json")
		if body.Source == "invalid" {
			_, _ = w.Write([]byte(`{"ok":false,"diagnostics":[{"message":"compile failed","severity":"error"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"diagnostics":[]}`))
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "validate", "--source", "valid", "--format", "json")
	if err != nil {
		t.Fatalf("valid policy returned error: %v (output %s)", err, output)
	}
	if !strings.Contains(output, `"ok": true`) {
		t.Fatalf("valid server verdict was not printed: %s", output)
	}

	err, output = runHookPEPCLI(t, "validate", "--source", "invalid", "--format", "json")
	if !errors.Is(err, errHookPEPValidationFailed) {
		t.Fatalf("invalid policy error = %v, want validation-failed sentinel", err)
	}
	if !strings.Contains(output, `"ok": false`) || !strings.Contains(output, "compile failed") {
		t.Fatalf("invalid server verdict was not printed: %s", output)
	}
}

func TestHookPEPDryRunDenyAndRollbackAreThinHTTPClients(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer policy-token" {
			t.Errorf("missing bearer header")
		}
		switch r.URL.Path {
		case "/v1/m/governance/pdp/dry-run":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode dry-run: %v", err)
			}
			if string(body["request"]) != `{"principal":{"kind":"token"},"permission":"agent:write","resource":{"kind":"agent"}}` {
				t.Errorf("request was not forwarded: %s", body["request"])
			}
			_, _ = w.Write([]byte(`{"allow":false,"engine":"cedar","reason":"forbid matched"}`))
		case "/v1/m/governance/pdp/rollback":
			var body struct {
				Engine   string `json:"engine"`
				Revision int64  `json:"revision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Engine != "cedar" || body.Revision != 3 {
				t.Errorf("rollback body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"engine":"cedar","from_revision":4,"to_revision":3,"active":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	request := `{"principal":{"kind":"token"},"permission":"agent:write","resource":{"kind":"agent"}}`
	err, output := runHookPEPCLI(t, "dry-run", "--source", "forbid policy", "--request", request)
	if !errors.Is(err, errHookPEPDenied) || !strings.Contains(output, "decision: deny") {
		t.Fatalf("deny mapping = err %v, output %q", err, output)
	}

	err, output = runHookPEPCLI(t, "rollback", "--revision", "3")
	if err != nil || !strings.Contains(output, "from=4 to=3 active=true") {
		t.Fatalf("rollback = err %v, output %q", err, output)
	}
}

func TestHookPEPPublishForwardsSourceAndRendersTextAndJSON(t *testing.T) {
	const response = `{"engine":"cedar","revision":8,"active":true,"note":"activated now"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/governance/pdp/publish" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body struct {
			Engine string `json:"engine"`
			Source string `json:"source"`
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode publish request: %v", err)
		}
		if body.Engine != "cedar" || body.Source != "permit policy" || body.Note != "approved" {
			t.Errorf("publish body = %+v", body)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "publish", "--source", "permit policy", "--note", "approved")
	if err != nil {
		t.Fatalf("publish text returned error: %v (output %q)", err, output)
	}
	if want := "publish: engine=cedar revision=8 active=true\nactivated now\n"; output != want {
		t.Fatalf("publish text output = %q, want %q", output, want)
	}

	err, output = runHookPEPCLI(t, "publish", "--source", "permit policy", "--note", "approved", "--format", "json")
	if err != nil {
		t.Fatalf("publish json returned error: %v (output %q)", err, output)
	}
	assertSameJSON(t, response, output)
}

func TestHookPEPVersionsUsesGETAndRendersTextAndJSON(t *testing.T) {
	const response = `{"items":[{"revision":3,"surface":"cedar","validated":true,"active":true},{"revision":2,"surface":"opa","validated":false}],"total":2}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/m/governance/pdp/versions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("GET Content-Type = %q, want empty", got)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "versions")
	if err != nil {
		t.Fatalf("versions text returned error: %v (output %q)", err, output)
	}
	wantText := "revision: engine=cedar revision=3 validated=true active=true\n" +
		"revision: engine=opa revision=2 validated=false active=false\n"
	if output != wantText {
		t.Fatalf("versions text output = %q, want %q", output, wantText)
	}

	err, output = runHookPEPCLI(t, "versions", "--format", "json")
	if err != nil {
		t.Fatalf("versions json returned error: %v (output %q)", err, output)
	}
	assertSameJSON(t, response, output)
}

func TestHookPEPTestsUsesGETQueryAndRendersTextAndJSON(t *testing.T) {
	const (
		compiledResponse  = `{"engine":"cedar","revision":9,"available":true,"passed":1,"failed":0,"total":1,"results":[{"name":"publish_compile_validate","passed":true}]}`
		unavailableResult = `{"engine":"opa","available":false,"passed":0,"failed":0,"total":0,"reason":"no stored artifact"}`
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/m/governance/pdp/tests" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch engine := r.URL.Query().Get("engine"); engine {
		case "cedar":
			if got := r.URL.Query().Get("revision"); got != "9" {
				t.Errorf("cedar revision = %q, want 9", got)
			}
			_, _ = w.Write([]byte(compiledResponse))
		case "opa":
			if _, present := r.URL.Query()["revision"]; present {
				t.Errorf("opa revision query should be omitted: %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(unavailableResult))
		default:
			t.Errorf("engine = %q", engine)
			http.Error(w, "bad engine", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "tests", "--engine", "cedar", "--revision", "9")
	if err != nil {
		t.Fatalf("tests text returned error: %v (output %q)", err, output)
	}
	if want := "tests: engine=cedar revision=9 available=true compiled=true\n"; output != want {
		t.Fatalf("tests text output = %q, want %q", output, want)
	}

	err, output = runHookPEPCLI(t, "tests", "--engine", "opa", "--format", "json")
	if err != nil {
		t.Fatalf("tests json returned error: %v (output %q)", err, output)
	}
	assertSameJSON(t, unavailableResult, output)
}

func runHookPEPCLI(t *testing.T, args ...string) (error, string) {
	t.Helper()
	cmd := newHookPEPCmd()
	var output, errOut bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	return cmd.Execute(), output.String()
}
