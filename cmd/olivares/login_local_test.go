// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// loginEngine is a TLS engine that accepts one password and reports one tenant,
// recorded in a data directory the way `quickstart` records it: console.json with
// its bind, and tls.crt with the certificate it serves.
func loginEngine(t *testing.T) (srv *httptest.Server, bodies chan map[string]any) {
	t.Helper()
	bodies = make(chan map[string]any, 4)
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case authLoginPath:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies <- body
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvs_session-for-test", "expires_at": "2026-10-01T22:00:00Z"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kind": "user", "actor": "user:ana", "grants": []map[string]string{{"tenant": "tenant-a", "role": "owner"}},
			})
		}
	}))
	t.Cleanup(srv.Close)
	dataDir := t.TempDir()
	u, _ := url.Parse(srv.URL)
	if err := writeConsoleState(dataDir, consoleState{Version: 1, Listen: u.Host}); err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dataDir, "tls.crt"), crt, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_DATA_DIR", dataDir)
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	return srv, bodies
}

func TestLoginOnTheEnginesHostNeedsNoServerAndNoCertificateFlag(t *testing.T) {
	srv, bodies := loginEngine(t)
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := execRoot(t, "login", "--email", "ana@example.com", "--password-file", pw)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	u, _ := url.Parse(srv.URL)
	if !strings.Contains(stderr, "Using the engine on this machine: https://"+u.Host) {
		t.Fatalf("login must say which engine it used; stderr = %q", stderr)
	}
	if got := <-bodies; got["email"] != "ana@example.com" || got["password"] != "correct horse" {
		t.Fatalf("login body = %v", got)
	}
	cfg, _, err := loadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	ctx, ok := cfg.context(cfg.CurrentContext)
	if !ok || ctx.Server != "https://"+u.Host || !strings.HasSuffix(ctx.CACert, "tls.crt") || ctx.Tenant != "tenant-a" {
		t.Fatalf("saved context = %+v, want the local engine, its tls.crt and the one tenant", ctx)
	}
}

func TestLoginAsksAPersonForEmailAndPassword(t *testing.T) {
	_, bodies := loginEngine(t)
	prevTTY, prevHidden := interactiveStdin, readHiddenInput
	t.Cleanup(func() { interactiveStdin, readHiddenInput = prevTTY, prevHidden })
	interactiveStdin = func(io.Reader) bool { return true }
	readHiddenInput = func(_ io.Reader, r *bufio.Reader) (string, error) {
		line, err := r.ReadString('\n')
		return strings.TrimSpace(line), err
	}
	root := newRootCmd()
	var out, errb strings.Builder
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader("ana@example.com\nsecret pw\n"))
	root.SetArgs([]string{"login"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\n%s", err, errb.String())
	}
	if !strings.Contains(errb.String(), "Email: ") || !strings.Contains(errb.String(), "Password: ") {
		t.Fatalf("prompts missing; stderr = %q", errb.String())
	}
	if strings.Contains(errb.String(), "--password is visible") {
		t.Fatal("a prompted password is not a --password flag and must not be warned about as one")
	}
	if got := <-bodies; got["email"] != "ana@example.com" || got["password"] != "secret pw" {
		t.Fatalf("login body = %v", got)
	}
}

func TestNotSignedInNamesTheLoginCommand(t *testing.T) {
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	_, _, err := execRoot(t, "session", "ls")
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "Sign in first: olivares login") {
		t.Fatalf("err = %v, want a usage error that names olivares login", err)
	}
}

func TestTransportHintNamesTheNextStep(t *testing.T) {
	u, _ := url.Parse("https://10.0.0.5:8443/v1/auth/whoami")
	untrusted := &url.Error{Op: "Get", URL: u.String(), Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}
	var typed *url.Error
	if hinted := transportHint(u, untrusted); !errors.Is(hinted, untrusted) || !errors.As(hinted, &typed) {
		t.Fatal("certificate advice must preserve the transport cause")
	}
	if msg := transportHint(u, untrusted).Error(); !strings.Contains(msg, "does not trust the certificate of https://10.0.0.5:8443") ||
		!strings.Contains(msg, "olivares login") || !strings.Contains(msg, "--ca-cert") {
		t.Fatalf("untrusted certificate hint = %q", msg)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, refused := http.Get("http://" + addr + "/status")
	if refused == nil {
		t.Skip("the closed port answered")
	}
	ru, _ := url.Parse("http://" + addr + "/status")
	if hinted := transportHint(ru, refused); !errors.Is(hinted, syscall.ECONNREFUSED) || !errors.As(hinted, &typed) {
		t.Fatal("connection advice must preserve the transport cause")
	}
	if msg := transportHint(ru, refused).Error(); !strings.Contains(msg, "No engine answers at http://"+addr) {
		t.Fatalf("refused hint = %q", msg)
	}
	other := io.ErrUnexpectedEOF
	if transportHint(ru, other) != other {
		t.Fatal("an unrelated transport error must pass through unchanged")
	}
}

func TestLocalStoreCommandsTakeTheTenantOfTheLocalContext(t *testing.T) {
	srv, _ := loginEngine(t)
	u, _ := url.Parse(srv.URL)
	config := os.Getenv("OLIVARES_CLI_CONFIG")
	write := func(server string) {
		t.Helper()
		if err := writeCLIConfig(config, cliConfig{CurrentContext: "c", Contexts: []cliContext{{
			Name: "c", Server: server, Token: "t", Tenant: "tenant-local",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	write("https://localhost:" + u.Port())
	if got, err := resolveTenant(""); err != nil || got != "tenant-local" {
		t.Fatalf("resolveTenant = %q, %v; want the local context's tenant", got, err)
	}
	if got, _ := resolveTenant("explicit"); got != "explicit" {
		t.Fatalf("an explicit --tenant must win, got %q", got)
	}
	write("https://olivares.example.com:" + u.Port())
	if _, err := resolveTenant(""); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("a context for another engine must not supply this host's tenant, err = %v", err)
	}
}

func TestCLIBootLoggerKeepsTheEngineReportOffTheAnswer(t *testing.T) {
	t.Setenv(envLogLevel, "")
	prev, prevLog := cliProcess, slog.Default()
	t.Cleanup(func() { cliProcess = prev; slog.SetDefault(prevLog) })
	cliProcess = false
	if cliBootLogger(slog.LevelError) != slog.Default() {
		t.Fatal("outside the real process the default logger must be kept, so test captures see the boot")
	}
	cliProcess = true
	quiet := cliBootLogger(slog.LevelError)
	if quiet.Enabled(context.Background(), slog.LevelWarn) || !quiet.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("a read command must show the boot's errors only")
	}
	t.Setenv(envLogLevel, "info")
	if !cliBootLogger(slog.LevelError).Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("OLIVARES_LOG_LEVEL must still turn the report back on")
	}
}

// TestLoginWithEmailAsksForThePassword: the help's own example, `olivares login --server
// <address> --email ana@example.com`, failed at a terminal with "--email needs a password:
// pass --password-file". A person who gives the email is asked for the password only (not
// echoed); a script with no terminal still gets the refusal that names --password-file.
func TestLoginWithEmailAsksForThePassword(t *testing.T) {
	_, bodies := loginEngine(t)
	prevTTY, prevHidden := interactiveStdin, readHiddenInput
	t.Cleanup(func() { interactiveStdin, readHiddenInput = prevTTY, prevHidden })
	interactiveStdin = func(io.Reader) bool { return true }
	readHiddenInput = func(_ io.Reader, r *bufio.Reader) (string, error) {
		line, err := r.ReadString('\n')
		return strings.TrimSpace(line), err
	}
	root := newRootCmd()
	var out, errb strings.Builder
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader("secret pw\n"))
	root.SetArgs([]string{"login", "--email", "ana@example.com"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login --email: %v\n%s", err, errb.String())
	}
	if !strings.Contains(errb.String(), "Password: ") || strings.Contains(errb.String(), "Email: ") {
		t.Fatalf("want only the password prompt; stderr = %q", errb.String())
	}
	if got := <-bodies; got["email"] != "ana@example.com" || got["password"] != "secret pw" {
		t.Fatalf("login body = %v", got)
	}

	interactiveStdin = func(io.Reader) bool { return false }
	_, _, err := execRoot(t, "login", "--email", "ana@example.com")
	if exitcode.From(err) != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "--password-file") {
		t.Fatalf("without a terminal: err = %v (exit %d), want the refusal naming --password-file", err, exitcode.From(err))
	}
}
