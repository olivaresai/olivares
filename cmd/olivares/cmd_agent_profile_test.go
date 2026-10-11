// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// These tests drive the real Cobra tree for `agent session create` against a
// controlled HTTP listener. They pin transport: the selected provider_profile_ref,
// bearer, tenant and status mapping. They do not grant authority, spawn a runtime
// or infer a profile from the environment.

type createProbe struct {
	*httptest.Server
	calls  atomic.Int64
	method atomic.Value
	path   atomic.Value
	auth   atomic.Value
	tenant atomic.Value
	ctype  atomic.Value
	body   atomic.Value
}

func newCreateProbe(t *testing.T, status int, payload string) *createProbe {
	t.Helper()
	p := &createProbe{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		p.method.Store(r.Method)
		p.path.Store(r.URL.EscapedPath())
		p.auth.Store(r.Header.Get("Authorization"))
		p.tenant.Store(r.Header.Get("X-Olivares-Tenant"))
		p.ctype.Store(r.Header.Get("Content-Type"))
		raw, _ := io.ReadAll(r.Body)
		p.body.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *createProbe) lastBody() string   { s, _ := p.body.Load().(string); return s }
func (p *createProbe) lastAuth() string   { s, _ := p.auth.Load().(string); return s }
func (p *createProbe) lastTenant() string { s, _ := p.tenant.Load().(string); return s }
func (p *createProbe) lastMethod() string { s, _ := p.method.Load().(string); return s }
func (p *createProbe) lastPath() string   { s, _ := p.path.Load().(string); return s }

const createdRunJSON = `{"run_ref":"run-lab-1","state":"pending","transport":"stream-json","isolation":"native"}`

func createArgs(server string, extra ...string) []string {
	base := []string{
		"agent", "session", "create",
		"--name", "feature-work",
		"--workspace", "ws-123",
		"--server", server,
		"--token", "test-token",
		"--tenant", "tenant-a",
	}
	return append(base, extra...)
}

func decodeCreateBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("create body is not JSON: %v\n%s", err, raw)
	}
	return got
}

func TestAgentSessionCreateSendsProviderProfileRefOnExplicitFlag(t *testing.T) {
	p := newCreateProbe(t, http.StatusCreated, createdRunJSON)
	out, errb, err := execRoot(t, createArgs(p.URL, "--provider-profile", "prof-fixture-1")...)
	if err != nil {
		t.Fatalf("explicit profile must succeed against HTTP 201, err=%v stderr=%s stdout=%s", err, errb, out)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", p.calls.Load())
	}
	if p.lastMethod() != http.MethodPost {
		t.Fatalf("method=%q, want POST", p.lastMethod())
	}
	if p.lastPath() != "/v1/m/sessions/runs" {
		t.Fatalf("path=%q, want /v1/m/sessions/runs", p.lastPath())
	}
	if p.lastAuth() != "Bearer test-token" {
		t.Fatalf("Authorization header was not the configured bearer")
	}
	if p.lastTenant() != "tenant-a" {
		t.Fatalf("X-Olivares-Tenant=%q, want tenant-a", p.lastTenant())
	}
	if !strings.HasPrefix(p.ctype.Load().(string), "application/json") {
		t.Fatalf("Content-Type=%q, want application/json", p.ctype.Load())
	}
	got := decodeCreateBody(t, p.lastBody())
	if got["provider_profile_ref"] != "prof-fixture-1" {
		t.Fatalf("provider_profile_ref=%v, want prof-fixture-1", got["provider_profile_ref"])
	}
	if got["workspace_ref"] != "ws-123" {
		t.Fatalf("workspace_ref=%v, want ws-123", got["workspace_ref"])
	}
	if _, ok := got["user_home"]; ok {
		t.Fatalf("body must not send user_home: %v", got)
	}
	if _, ok := got["config_home"]; ok {
		t.Fatalf("body must not send config_home: %v", got)
	}
	if _, ok := got["auth_source"]; ok {
		t.Fatalf("body must not send auth_source: %v", got)
	}
	if _, ok := got["environment_ref"]; ok {
		t.Fatalf("body must not send environment_ref: %v", got)
	}
	if !strings.Contains(out, "run-lab-1") {
		t.Fatalf("stdout must name the created run, got %q", out)
	}
	for _, tc := range []struct{ name, payload string }{
		{"non_json", `queued test-token`},
		{"array", `["reflected test-token"]`},
		{"string", `"queued test-token"`},
		{"empty_array", `[]`},
	} {
		t.Run(tc.name+"_202_stays_a_guarded_refusal", func(t *testing.T) {
			p := newCreateProbe(t, http.StatusAccepted, tc.payload)
			out, stderr, err := execRoot(t, createArgs(p.URL, "--provider-profile", "prof-fixture-1", "-o", "json")...)
			if err == nil || out != "" {
				t.Fatalf("a non-approval 202 must refuse without stdout: err=%v stdout=%s", err, out)
			}
			if p.calls.Load() != 1 || p.lastMethod() != http.MethodPost || p.lastPath() != "/v1/m/sessions/runs" {
				t.Fatalf("refused create made another request: calls=%d method=%s path=%s", p.calls.Load(), p.lastMethod(), p.lastPath())
			}
			var renderedJSON, renderedText strings.Builder
			if err := printCLIErrorAs(&renderedJSON, err, true); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Error struct {
					Status int `json:"status"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(renderedJSON.String()), &got); err != nil || got.Error.Status != http.StatusAccepted {
				t.Errorf("JSON refusal must keep status 202: %s (decode=%v)", renderedJSON.String(), err)
			}
			if err := printCLIErrorAs(&renderedText, err, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(renderedText.String(), "HTTP 202") {
				t.Errorf("text refusal must keep HTTP 202: %s", renderedText.String())
			}
			if strings.Contains(out+stderr+err.Error()+renderedJSON.String()+renderedText.String(), "test-token") {
				t.Fatal("non-approval refusal disclosed the request credential")
			}
		})
	}
	t.Run("non_approval_202_stays_a_redacted_refusal", func(t *testing.T) {
		p := newCreateProbe(t, http.StatusAccepted, `{"error":{"message":"reflected test-token"}}`)
		out, stderr, err := execRoot(t, createArgs(p.URL, "--provider-profile", "prof-fixture-1")...)
		if err == nil || strings.TrimSpace(out) != "" {
			t.Fatalf("a non-approval 202 must remain a refusal: err=%v stdout=%s", err, out)
		}
		if strings.Contains(out+stderr+err.Error(), "test-token") {
			t.Fatal("non-approval refusal disclosed the request credential")
		}
		if p.calls.Load() != 1 {
			t.Fatalf("non-approval refusal retried: calls=%d", p.calls.Load())
		}
	})
	for _, mode := range []string{"text", "json", "legacy-json"} {
		t.Run("waiting_approval_"+mode, func(t *testing.T) {
			const payload = `{"run_ref":"run-lab-approval","name":"feature-work","state":"waiting_approval","driver":"claude","transport":"stream-json","isolation":"native","workspace_path":"/workspace/fixture","approval_ref":"approval-fixture","extra":"future-field"}`
			p := newCreateProbe(t, http.StatusAccepted, payload)
			args := createArgs(p.URL, "--provider-profile", "prof-fixture-1")
			if mode == "json" {
				args = append(args, "-o", "json")
			} else if mode == "legacy-json" {
				args = append(args, "--json")
			}
			out, stderr, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("pending approval must not be a refusal: err=%v stderr=%s stdout=%s", err, stderr, out)
			}
			if p.calls.Load() != 1 || p.lastMethod() != http.MethodPost || p.lastPath() != "/v1/m/sessions/runs" {
				t.Fatalf("pending create made another request: calls=%d method=%s path=%s", p.calls.Load(), p.lastMethod(), p.lastPath())
			}
			if mode != "text" {
				got, want := decodeCreateBody(t, out), decodeCreateBody(t, payload)
				if len(got) != len(want) {
					t.Fatalf("pending JSON fields changed: %s", out)
				}
				for key, value := range want {
					if got[key] != value {
						t.Errorf("pending JSON %s = %v, want %v", key, got[key], value)
					}
				}
				return
			}
			if !strings.Contains(out, "waits for approval before it starts") || !strings.Contains(out, "approval-fixture") ||
				!strings.Contains(out, p.URL+"/permissions?tab=approvals") {
				t.Fatalf("pending create needs truthful status, approval reference and link: %s", out)
			}
			if strings.Contains(out, "Started ") || strings.Contains(out, "turn finished") {
				t.Fatalf("pending create claimed execution: %s", out)
			}
		})
	}
}

func TestAgentSessionCreateOmitsProviderProfileRefWhenFlagOmitted(t *testing.T) {
	p := newCreateProbe(t, http.StatusCreated, createdRunJSON)
	t.Setenv("OLIVARES_PROVIDER_PROFILE", "prof-from-env")
	t.Setenv("HOME", t.TempDir())
	_, _, err := execRoot(t, createArgs(p.URL)...)
	if err != nil {
		t.Fatalf("omitted flag must still transport against HTTP 201, err=%v", err)
	}
	got := decodeCreateBody(t, p.lastBody())
	if _, ok := got["provider_profile_ref"]; ok {
		t.Fatalf("omitted --provider-profile must keep the older body without provider_profile_ref, got %v", got)
	}
}

func TestAgentSessionCreatePropagatesBadRequest(t *testing.T) {
	p := newCreateProbe(t, http.StatusBadRequest,
		`{"error":{"message":"select a provider profile before launching a session"}}`)
	out, errb, err := execRoot(t, createArgs(p.URL)...)
	if err == nil {
		t.Fatal("HTTP 400 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Err {
		t.Fatalf("exit=%d, want %d (generic err): %v", got, exitcode.Err, err)
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("error must name HTTP 400, got %v", err)
	}
	if !strings.Contains(err.Error(), "select a provider profile before launching a session") {
		t.Fatalf("error must carry the server message, got %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a refused create must print nothing on stdout, got %q", out)
	}
	_ = errb
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", p.calls.Load())
	}
}

func TestAgentSessionCreatePropagatesForbidden(t *testing.T) {
	p := newCreateProbe(t, http.StatusForbidden, `{"error":{"message":"forbidden"}}`)
	out, _, err := execRoot(t, createArgs(p.URL, "--provider-profile", "prof-fixture-1")...)
	if err == nil {
		t.Fatal("HTTP 403 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Fatalf("exit=%d, want %d (auth): %v", got, exitcode.Auth, err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error must name HTTP 403, got %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a forbidden create must print nothing on stdout, got %q", out)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", p.calls.Load())
	}
}

func TestAgentSessionCreateUnknownFlagIsUsage(t *testing.T) {
	p := newCreateProbe(t, http.StatusCreated, createdRunJSON)
	_, _, err := execRoot(t, createArgs(p.URL, "--not-a-real-flag")...)
	if err == nil {
		t.Fatal("an unknown flag must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit=%d, want %d (usage): %v", got, exitcode.Usage, err)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("a usage error must not open a connection, calls=%d", p.calls.Load())
	}
}

// A profile that carries an account name shows it in the list and the detail, so
// a person can pick by name; -o json passes the field through untouched.
func TestAgentProfileShowsTheAccountName(t *testing.T) {
	named := map[string]any{"profile_ref": "ppf_a", "driver": "claude", "state": "active", "account_name": "claude-b", "display_name": "Claude Code"}
	plain := map[string]any{"profile_ref": "ppf_p", "driver": "claude", "state": "active", "display_name": "Claude Code"}
	var out strings.Builder
	if err := printProfileTable(&out, []map[string]any{named, plain}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if !strings.Contains(strings.ToLower(lines[0]), "account") {
		t.Fatalf("header = %q, want an account column", lines[0])
	}
	var namedLine, plainLine string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "ppf_a"):
			namedLine = l
		case strings.Contains(l, "ppf_p"):
			plainLine = l
		}
	}
	if !strings.Contains(namedLine, "claude-b") || strings.Contains(plainLine, "claude-b") {
		t.Fatalf("rows = %q / %q", namedLine, plainLine)
	}
	out.Reset()
	if err := printProfileRecord(&out, named); err != nil || !strings.Contains(out.String(), "claude-b") {
		t.Fatalf("detail = %q (%v), want the account name", out.String(), err)
	}
}
