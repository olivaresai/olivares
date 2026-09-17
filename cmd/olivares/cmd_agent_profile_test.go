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
