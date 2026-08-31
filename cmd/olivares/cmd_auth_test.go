// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestAuthLoginValidatesAndPersistsContextWithoutPrintingToken(t *testing.T) {
	const token = "olvk_super-secret-abcd"
	var gotAuth, gotTenant, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Olivares-Tenant")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:cli", "grants": []map[string]string{{"tenant": "tenant-a", "role": "owner"}},
		})
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "olivares", "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	out, stderr, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", token, "--tenant", "tenant-a", "--context", "local")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if gotPath != "/v1/auth/whoami" || gotAuth != "Bearer "+token || gotTenant != "tenant-a" {
		t.Fatalf("whoami request path=%q auth=%q tenant=%q", gotPath, gotAuth, gotTenant)
	}
	if strings.Contains(out+stderr, token) {
		t.Fatalf("token leaked in CLI output: stdout=%q stderr=%q", out, stderr)
	}

	cfg, err := readCLIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "local" || len(cfg.Contexts) != 1 {
		t.Fatalf("persisted config = %#v", cfg)
	}
	ctx := cfg.Contexts[0]
	if ctx.Server != srv.URL || ctx.Token != token || ctx.Tenant != "tenant-a" {
		t.Fatalf("persisted context = %#v", ctx)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %#o", info.Mode().Perm())
	}
}

func TestAuthLoginRejectsUnauthorizedWithoutPersisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	_, stderr, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", "do-not-print")
	if err == nil {
		t.Fatal("unauthorized login must fail")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Auth)
	}
	if strings.Contains(stderr+err.Error(), "do-not-print") {
		t.Fatalf("token leaked in auth error: %q / %q", stderr, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed login created config: %v", statErr)
	}
}

func TestAuthLoginUsesPinnedTLSAndDefaultsContextToHostname(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:pinned", "grants": []map[string]string{{"tenant": "tenant-pinned", "role": "viewer"}},
		})
	}))
	t.Cleanup(srv.Close)
	spki := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(spki[:])
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)

	if out, stderr, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", "olvk_pinned-secret", "--pin-sha256", pin); err != nil {
		t.Fatalf("pinned login: %v\nstdout=%s\nstderr=%s", err, out, stderr)
	}
	cfg, err := readCLIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != u.Hostname() || len(cfg.Contexts) != 1 {
		t.Fatalf("default context = %#v, want hostname %q", cfg, u.Hostname())
	}
	ctx := cfg.Contexts[0]
	if len(ctx.PinSHA256) != 1 || ctx.PinSHA256[0] != pin || ctx.Tenant != "tenant-pinned" {
		t.Fatalf("pinned context = %#v", ctx)
	}
}

func TestAuthStatusPrintsEffectiveIdentityAndRedactedToken(t *testing.T) {
	const token = "olvk_effective-secret-abcd"
	var gotAuth, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Olivares-Tenant")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:status", "grants": []map[string]string{{"tenant": "effective-tenant", "role": "admin"}},
		})
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	if err := writeCLIConfig(path, cliConfig{
		CurrentContext: "saved",
		Contexts:       []cliContext{{Name: "saved", Server: "https://unused.example.test", Token: "saved-token", Tenant: "saved-tenant"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_SERVER_URL", "https://also-unused.example.test")
	t.Setenv("OLIVARES_TOKEN", "env-token")
	t.Setenv("OLIVARES_TENANT", "env-tenant")
	out, stderr, err := execRoot(t, "auth", "status", "--server", srv.URL, "--token", token, "--tenant", "effective-tenant")
	if err != nil {
		t.Fatalf("auth status: %v\n%s", err, stderr)
	}
	if gotAuth != "Bearer "+token || gotTenant != "effective-tenant" {
		t.Fatalf("effective headers auth=%q tenant=%q", gotAuth, gotTenant)
	}
	for _, want := range []string{"token:status", "effective-tenant", "admin", srv.URL, "saved", "olvk_…abcd"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+stderr, token) {
		t.Fatalf("token leaked in status output: stdout=%q stderr=%q", out, stderr)
	}
}

func TestAuthStatusMapsForbiddenToAuthExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	_, _, err := execRoot(t, "auth", "status", "--server", srv.URL, "--token", "hidden-token")
	if err == nil || exitcode.From(err) != exitcode.Auth {
		t.Fatalf("error = %v, exit code = %d; want auth rejection", err, exitcode.From(err))
	}
	if strings.Contains(err.Error(), "hidden-token") {
		t.Fatalf("token leaked in error: %q", err)
	}
}

func TestAuthLogoutAndUseContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	initial := cliConfig{
		CurrentContext: "one",
		Contexts: []cliContext{
			{Name: "one", Server: "https://one.example.test", Token: "token-one", Tenant: "tenant-one", CACert: "one.pem", PinSHA256: []string{"pin-one"}},
			{Name: "two", Server: "https://two.example.test", Token: "token-two", Tenant: "tenant-two"},
		},
	}
	if err := writeCLIConfig(path, initial); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := execRoot(t, "auth", "logout"); err != nil {
		t.Fatalf("logout: %v\n%s", err, stderr)
	}
	cfg, err := readCLIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := cfg.context("one")
	if one.Token != "" || one.Server != initial.Contexts[0].Server || one.Tenant != "tenant-one" || one.CACert != "one.pem" || len(one.PinSHA256) != 1 {
		t.Fatalf("logout context = %#v", one)
	}

	if _, stderr, err := execRoot(t, "auth", "use-context", "two"); err != nil {
		t.Fatalf("use-context: %v\n%s", err, stderr)
	}
	cfg, err = readCLIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "two" {
		t.Fatalf("current context = %q, want two", cfg.CurrentContext)
	}

	if _, stderr, err := execRoot(t, "auth", "logout", "--context", "two", "--purge"); err != nil {
		t.Fatalf("purge: %v\n%s", err, stderr)
	}
	cfg, err = readCLIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "" || len(cfg.Contexts) != 1 || cfg.Contexts[0].Name != "one" {
		t.Fatalf("purged config = %#v", cfg)
	}
}

func TestAuthHelpDocumentsPrecedenceAndConfigCollision(t *testing.T) {
	out, _, err := execRoot(t, "auth", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"explicit flag", "OLIVARES_SERVER_URL", "current context", "engine configuration", "use-context"} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth help missing %q:\n%s", want, out)
		}
	}
}

// `-o json` HAS TO PRODUCE JSON (2026-08-05). newAuthStatusCmd used to write its
// six KEY<TAB>value lines with fmt.Fprintf and return, ignoring the global output
// flag entirely: `auth status -o json` emitted output byte-identical to the text
// form and exited 0, so a caller piping it to jq got a parse error from a command
// that had just reported success. It was the only surface in the CLI sweep where
// the JSON form was not JSON.
func TestAuthStatusJSONIsActuallyJSONAndStillRedactsTheToken(t *testing.T) {
	const token = "olvk_effective-secret-abcd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:status",
			"grants": []map[string]string{{"tenant": "effective-tenant", "role": "admin"}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))

	out, stderr, err := execRoot(t, "auth", "status", "--server", srv.URL,
		"--token", token, "--tenant", "effective-tenant", "-o", "json")
	if err != nil {
		t.Fatalf("auth status -o json: %v\n%s", err, stderr)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("-o json did not produce JSON (%v):\n%s", err, out)
	}
	if decoded["actor"] != "token:status" || decoded["tenant"] != "effective-tenant" || decoded["role"] != "admin" {
		t.Fatalf("json payload lost the identity it was asked for: %#v", decoded)
	}
	// Redaction is a property of the VALUE, not of the text formatter, so it must
	// survive the format switch.
	if decoded["token"] != "olvk_…abcd" {
		t.Fatalf("token field = %q, want the redacted form", decoded["token"])
	}
	if strings.Contains(out+stderr, token) {
		t.Fatalf("the -o json path leaked the bearer token: %q", out)
	}
}

// TestAuthLoginWarnsThatInsecureIsNotPersisted pins the one thing `auth login
// --insecure` used to leave unsaid: it writes a context that no later command can
// use. Measured 2026-08-09 against a real `olivares quickstart` engine —
// login printed "login validated" and exited 0, and the very next `olivares
// status` died with x509 "certificate signed by unknown authority" and exit 6,
// with nothing in either message connecting the two.
//
// The assertion is on WHAT THE OPERATOR IS TOLD, not on a phrasing: the note must
// say the flag is not saved AND name the way out. It also pins the negative that
// makes the whole thing safe — --insecure must still never reach the config file.
func TestAuthLoginWarnsThatInsecureIsNotPersisted(t *testing.T) {
	const token = "olvk_no-trust-material-token"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:cli", "grants": []map[string]string{{"tenant": "tenant-a", "role": "owner"}},
		})
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "olivares", "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")

	out, stderr, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", token,
		"--tenant", "tenant-a", "--context", "local", "--insecure")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "login validated") {
		t.Fatalf("expected the success line on stdout, got %q", out)
	}
	// It must say the flag did NOT stick, that the next command therefore fails,
	// and how to fix it for good. Any one of the three alone leaves the operator
	// exactly where the defect left them.
	for _, want := range []string{"not saved in the context", "fail TLS verification", "--ca-cert"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("the note must mention %q; stderr = %q", want, stderr)
		}
	}
	// The note is guidance, not a credential dump.
	if strings.Contains(out+stderr, token) {
		t.Fatalf("token leaked in CLI output: stdout=%q stderr=%q", out, stderr)
	}
	// And the reason the note has to exist at all: the flag never reaches the file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The YAML KEY, not the substring: the first version of this assertion grepped
	// for "insecure" and failed on a fixture token that happened to contain the
	// word. A test whose own data can satisfy its predicate proves nothing.
	if strings.Contains(string(raw), "insecure:") {
		t.Fatalf("--insecure must never be persisted; config = %q", raw)
	}
}

// TestAuthLoginStaysQuietWhenTheContextCarriesTrust is the other half, and without
// it the note above could fire on every login and still pass: a login that DOES
// store trust material has nothing to warn about, and a warning there would train
// operators to ignore the one that matters.
func TestAuthLoginStaysQuietWhenTheContextCarriesTrust(t *testing.T) {
	const token = "olvk_pinned-path-token"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:cli", "grants": []map[string]string{{"tenant": "tenant-a", "role": "owner"}},
		})
	}))
	t.Cleanup(srv.Close)

	pin := base64.StdEncoding.EncodeToString(func() []byte {
		sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
		return sum[:]
	}())

	path := filepath.Join(t.TempDir(), "olivares", "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")

	_, stderr, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", token,
		"--tenant", "tenant-a", "--context", "local", "--pin-sha256", pin)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "not saved in the context") {
		t.Fatalf("a pinned login carries its own trust and must not warn; stderr = %q", stderr)
	}
}
