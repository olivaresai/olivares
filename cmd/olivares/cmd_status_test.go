// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestStatusCommandPrintsKnowledgePosture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"degraded",
			"timestamp":"2026-07-08T00:00:00Z",
				"embedder_kind":"local-hash",
				"retrieval_semantic":false,
				"knowledge_status_reason":"embeddings_provider_missing",
				"guard_profile":"public_only",
				"guard_warning":"knowledge_guard_public_only_active",
				"guard_downgrade_count":1,
				"components":[
					{"name":"api","status":"operational"},
					{"name":"knowledge","status":"degraded","embedder_kind":"local-hash","retrieval_semantic":false,"reason":"embeddings_provider_missing","guard_profile":"public_only","guard_warning":"knowledge_guard_public_only_active","guard_downgrade_count":1}
				]
			}`))
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	cmd := newStatusCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--server", srv.URL})
	// This fixture reports a DEGRADED posture, so the exit contract
	// makes the command return the silent degraded code after printing.
	if err := cmd.Execute(); exitcode.From(err) != exitcode.Degraded || !exitcode.Silent(err) {
		t.Fatalf("status on a degraded engine: want silent degraded exit, got %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"EMBEDDER_KIND",
		"local-hash",
		"RETRIEVAL_SEMANTIC",
		"false",
		"KNOWLEDGE_REASON",
		"embeddings_provider_missing",
		"GUARD_PROFILE",
		"public_only",
		"GUARD_DOWNGRADES",
		"KNOWLEDGE_GUARD_WARNING",
		"knowledge_guard_public_only_active",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
}

// A correct install whose only gap is an unprovisioned OPTIONAL capability must
// exit 0 — a fresh install failing its own health check is how exit 7 stops
// meaning anything — while still NAMING what is not provisioned, up front and in
// the component table.
func TestStatusCommandNotConfiguredExitsZeroAndNamesIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"not_configured",
			"timestamp":"2026-08-05T00:00:00Z",
			"embedder_kind":"local-hash",
			"retrieval_semantic":false,
			"knowledge_status_reason":"embeddings_provider_missing",
			"guard_profile":"acl_aware",
			"components":[
				{"name":"api","status":"operational"},
				{"name":"knowledge","status":"not_configured","embedder_kind":"local-hash","retrieval_semantic":false,"reason":"embeddings_provider_missing","guard_profile":"acl_aware"},
				{"name":"store","status":"operational"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	cmd := newStatusCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--server", srv.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status on a healthy-but-unconfigured engine: want exit 0, got code %d (%v)\n%s", exitcode.From(err), err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"NOT_CONFIGURED",
		"knowledge",
		"not_configured",
		"embeddings_provider_missing",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q — exiting 0 must never hide what is unprovisioned:\n%s", want, got)
		}
	}
}

// Deny-closed: a status word this build does not know (an older CLI against a
// newer engine) is a FAULT, never a silent success.
func TestStatusCommandUnknownStatusExitsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"quarantined","timestamp":"2026-08-05T00:00:00Z","components":[]}`))
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	cmd := newStatusCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--server", srv.URL})
	if err := cmd.Execute(); exitcode.From(err) != exitcode.Degraded || !exitcode.Silent(err) {
		t.Fatalf("status on an unrecognized verdict: want silent degraded exit, got %v\n%s", err, out.String())
	}
}

// E5a: the audit-spool posture rides the public status components,
// so `olivares status` renders it like any other health section.
func TestStatusCommandPrintsAuditSpoolComponent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"degraded",
			"timestamp":"2026-07-18T00:00:00Z",
			"embedder_kind":"semantic",
			"retrieval_semantic":true,
			"guard_profile":"acl_aware",
			"components":[
				{"name":"api","status":"operational"},
				{"name":"audit_spool","status":"degraded","reason":"audit spool budget engaged; evidence is dropping under the declared policy (mode=degrade)"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	cmd := newStatusCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--server", srv.URL})
	// This fixture reports a DEGRADED posture, so the exit contract
	// makes the command return the silent degraded code after printing.
	if err := cmd.Execute(); exitcode.From(err) != exitcode.Degraded || !exitcode.Silent(err) {
		t.Fatalf("status on a degraded engine: want silent degraded exit, got %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"audit_spool", "degraded", "audit spool budget engaged"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
}

func TestStatusCommandUsesContextCAAndExplicitPin(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"operational","timestamp":"2026-07-20T00:00:00Z","components":[]}`))
	}))
	t.Cleanup(srv.Close)
	cert := srv.Certificate()
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", configPath)
	t.Setenv("OLIVARES_SERVER_URL", "")
	if err := writeCLIConfig(configPath, cliConfig{
		CurrentContext: "tls-test",
		Contexts:       []cliContext{{Name: "tls-test", Server: srv.URL, CACert: caPath}},
	}); err != nil {
		t.Fatal(err)
	}

	if out, stderr, err := execRoot(t, "status"); err != nil {
		t.Fatalf("status with context CA: %v\nstdout=%s\nstderr=%s", err, out, stderr)
	}

	spki := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(spki[:])
	if out, stderr, err := execRoot(t, "status", "--server", srv.URL, "--ca-cert=", "--pin-sha256", pin); err != nil {
		t.Fatalf("status with explicit pin: %v\nstdout=%s\nstderr=%s", err, out, stderr)
	}
}

// The adversarial review found `status` shipping the ACTIVE CONTEXT's
// bearer token to whatever --server the operator pointed at — /status is
// public and unauthenticated, so no credential may ever ride along.
func TestStatusCommandNeverSendsCredentials(t *testing.T) {
	var gotAuth, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Olivares-Tenant")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"operational","components":[]}`))
	}))
	t.Cleanup(srv.Close)

	// An active context with a token for a DIFFERENT server, plus env token:
	// neither may reach the divergent --server target.
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", configPath)
	t.Setenv("OLIVARES_TOKEN", "olvk_env_secret")
	t.Setenv("OLIVARES_TENANT", "acme")
	if err := writeCLIConfig(configPath, cliConfig{
		CurrentContext: "prod",
		Contexts:       []cliContext{{Name: "prod", Server: "https://prod.example.com", Token: "olvk_prod_secret", Tenant: "acme"}},
	}); err != nil {
		t.Fatal(err)
	}

	if out, stderr, err := execRoot(t, "status", "--server", srv.URL); err != nil {
		t.Fatalf("status: %v\nstdout=%s\nstderr=%s", err, out, stderr)
	}
	if gotAuth != "" {
		t.Fatalf("status leaked an Authorization header to an arbitrary server: %q", gotAuth)
	}
	if gotTenant != "" {
		t.Fatalf("status leaked a tenant header to an arbitrary server: %q", gotTenant)
	}
}

func TestStatusCommandInsecureEmitsWarning(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"operational","components":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	_, stderr, err := execRoot(t, "status", "--server", srv.URL, "--insecure")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "TLS verification disabled — never use against production") {
		t.Fatalf("insecure warning missing: %q", stderr)
	}
}
