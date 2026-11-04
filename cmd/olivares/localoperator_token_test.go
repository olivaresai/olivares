// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// localOperatorFixture is a data directory as `serve` leaves it for its owner: the
// engine's origin and certificate, and the credential the engine minted.
func localOperatorFixture(t *testing.T, h *harness) (dir, token string) {
	t.Helper()
	dir = t.TempDir()
	// The engine makes its data directory private; a temp directory follows the umask.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(h.h)
	t.Cleanup(srv.Close)
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), crt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeConsoleState(dir, consoleState{
		Version: consoleStateVersion, Listen: strings.TrimPrefix(srv.URL, "https://"),
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_DATA_DIR", dir)
	for _, name := range []string{"OLIVARES_SERVER_URL", "OLIVARES_TOKEN", "OLIVARES_TENANT"} {
		t.Setenv(name, "")
	}
	if err := ensureLocalOperatorToken(context.Background(), h.authr, h.st, dir); err != nil {
		t.Fatalf("the engine could not leave the local operator's credential: %v", err)
	}
	return dir, readLocalOperatorToken(t, dir)
}

func readLocalOperatorToken(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, localOperatorTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := splitLocalOperatorFile(string(raw))
	return token
}

// The account that owns the engine's data directory is the local operator: with
// nothing configured (no flag, no environment, no saved sign-in) the CLI reaches
// the engine this host recorded, signed in. Measured on Docker install,
// inside the container as the engine's account: `olivares accessmap graph` said
// "Not signed in. Sign in first: olivares login".
//
// The whole path runs for real: the engine's own token mint, the file it leaves in
// the data directory, the CLI's resolution of engine, trust and credential, TLS,
// and the engine's bearer authentication on a governed route.
func TestLocalOperatorUsesTheCLIWithoutASeparateLogin(t *testing.T) {
	h := newHarness(t)
	dir, _ := localOperatorFixture(t, h)
	if info, err := os.Stat(filepath.Join(dir, localOperatorTokenFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the credential must be a file only its owner can read, got %v, %v", info, err)
	}

	out, errOut, err := execRoot(t, "accessmap", "graph", "-o", "json")
	if err != nil {
		t.Fatalf("accessmap graph with nothing configured must work for the data directory's owner: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "nodes") {
		t.Errorf("the engine's graph answer did not reach the terminal:\n%s", out)
	}
	// The local operator also runs the server-wide commands the product itself points
	// at (the engine says "Enable it from the CLI with olivares modules on accessmap").
	if out, errOut, err := execRoot(t, "auth", "totp-policy", "get"); err != nil {
		t.Errorf("auth totp-policy get needs system:admin, which the data directory's owner holds: %v\n%s%s", err, out, errOut)
	}
	// The credential is the superadmin of the install, so it also runs the commands that
	// change things; that is the intended model of the data directory's owner, not only reads.
	if out, errOut, err := execRoot(t, "tokens", "issue", "--name", "ci", "--superadmin"); err != nil {
		t.Errorf("tokens issue is a mutating command the data directory's owner runs too: %v\n%s%s", err, out, errOut)
	}
}

// Only the account's own data directory, and only a private file it owns, name the
// local operator's engine and credential. A cloned repository or unpacked archive
// holding olivares-data/{install-id, console.json, operator.token} in the working
// directory made every command talk to the engine it named, signed in with its token,
// so `olivares provider add` sent the user's key there.
func TestLocalOperatorCredentialComesOnlyFromTheAccountsPrivateFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"OLIVARES_DATA_DIR", "OLIVARES_SERVER_URL", "OLIVARES_TOKEN", "OLIVARES_TENANT", cliConfigOverrideEnv} {
		t.Setenv(name, "")
	}
	plant := func(dir string, perm os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "install-id"), []byte("planted\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeConsoleState(dir, consoleState{Version: consoleStateVersion, Listen: "evil.example:443"}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, localOperatorTokenFile)
		if err := os.WriteFile(path, []byte("olv_planted\nt_planted\n"), perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, perm); err != nil {
			t.Fatal(err)
		}
	}

	work := t.TempDir()
	plant(filepath.Join(work, legacyDataDirName), 0o600)
	t.Chdir(work)
	if got, err := resolveCLIConfig(cliResolutionOptions{}); err != nil || got.Server != "" || got.Token != "" {
		t.Fatalf("a data directory in the working directory chose the engine and the credential: server=%q token set=%t err=%v",
			got.Server, got.Token != "", err)
	}

	own := t.TempDir()
	plant(own, 0o644)
	t.Setenv("OLIVARES_DATA_DIR", own)
	if got, err := resolveCLIConfig(cliResolutionOptions{}); err != nil || got.Token != "" {
		t.Fatalf("a credential file other accounts can read is not the one the engine left: token set=%t err=%v", got.Token != "", err)
	}
}

// A replaced local-operator credential stops working. The engine minted a new
// superadmin token whenever the file was missing and left the old one live, so a
// copy in a backup or volume snapshot kept superadmin from any host.
func TestReplacedLocalOperatorCredentialIsRevoked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir, first := localOperatorFixture(t, h)

	if err := ensureLocalOperatorToken(ctx, h.authr, h.st, dir); err != nil {
		t.Fatal(err)
	}
	if again := readLocalOperatorToken(t, dir); again != first {
		t.Fatal("a restart replaced a credential that still works")
	}
	if err := os.Remove(filepath.Join(dir, localOperatorTokenFile)); err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalOperatorToken(ctx, h.authr, h.st, dir); err != nil {
		t.Fatal(err)
	}
	second := readLocalOperatorToken(t, dir)
	if second == "" || second == first {
		t.Fatal("the next start did not leave a fresh credential")
	}
	if _, err := h.authr.Authenticate(ctx, first); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("the replaced credential still authenticates: %v", err)
	}
	if _, err := h.authr.Authenticate(ctx, second); err != nil {
		t.Fatalf("the fresh credential does not authenticate: %v", err)
	}
}

// `olivares login` saves a sign-in of its own, never a copy of the local operator's
// credential: the copy outranked the engine's file and outlived its replacement.
func TestLoginDoesNotSaveTheLocalOperatorCredential(t *testing.T) {
	h := newHarness(t)
	_, token := localOperatorFixture(t, h)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, cfg)

	out, errOut, err := execRootStdin(t, "", "login")
	raw, _ := os.ReadFile(cfg)
	if strings.Contains(string(raw), token) {
		t.Fatalf("login saved the local operator's credential into %s:\n%s%s", cfg, out, errOut)
	}
	if err == nil {
		t.Fatalf("login with no credential must ask for one, not succeed:\n%s%s", out, errOut)
	}
}

func localOperatorResolved(t *testing.T) cliResolvedConfig {
	t.Helper()
	got, err := resolveCLIConfig(cliResolutionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The engine's endpoint, its certificate and the bearer sent to them are trusted only
// when the data directory is private to the account that runs the CLI. On a directory
// other accounts can write, one of them replaces console.json and tls.crt with its own
// endpoint and CA, and the next zero-config command hands it the superadmin token.
func TestLocalOperatorTrustsOnlyAPrivateDataDirectory(t *testing.T) {
	h := newHarness(t)
	dir, token := localOperatorFixture(t, h)
	if got := localOperatorResolved(t); got.Token != token {
		t.Fatalf("the engine's own private data directory must resolve: token set=%t", got.Token != "")
	}
	for _, c := range []struct {
		name string
		path string
		bad  os.FileMode
		good os.FileMode
	}{
		{"a data directory other accounts can write", dir, 0o777, 0o700},
		{"a console state other accounts can write", filepath.Join(dir, consoleStateFile), 0o666, 0o600},
		{"a certificate other accounts can write", filepath.Join(dir, "tls.crt"), 0o666, 0o644},
	} {
		if err := os.Chmod(c.path, c.bad); err != nil {
			t.Fatal(err)
		}
		if got := localOperatorResolved(t); got.Token != "" || got.Server != "" {
			t.Errorf("%s still names the engine and the credential: server=%q token set=%t", c.name, got.Server, got.Token != "")
		}
		if err := os.Chmod(c.path, c.good); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(dir, consoleStateFile)
	if err := os.Rename(state, state+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(state+".real", state); err != nil {
		t.Fatal(err)
	}
	if got := localOperatorResolved(t); got.Token != "" {
		t.Error("a symlink in place of the console state still names the engine and the credential")
	}
}

// The engine keeps only a credential file the CLI would accept. A file other accounts
// can read, or a symlink, was kept and used by the engine but refused by the CLI; it is
// replaced, and the token in it, which may have been copied, is revoked.
func TestUnsafeLocalOperatorFileIsReplacedAndRevoked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir, first := localOperatorFixture(t, h)
	path := filepath.Join(dir, localOperatorTokenFile)

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalOperatorToken(ctx, h.authr, h.st, dir); err != nil {
		t.Fatal(err)
	}
	second := readLocalOperatorToken(t, dir)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 || second == first {
		t.Fatalf("a file other accounts can read was kept: mode=%v same token=%t err=%v", info, second == first, err)
	}
	if _, err := h.authr.Authenticate(ctx, first); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("the token of the readable file still authenticates: %v", err)
	}

	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalOperatorToken(ctx, h.authr, h.st, dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("a symlink was kept in place of the credential file: %v, %v", info, err)
	}
	if _, err := h.authr.Authenticate(ctx, second); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("the token behind the symlink still authenticates: %v", err)
	}
}

// A restart that finds the file kept still finishes the revocation an earlier start
// did not (a crash or a store failure between the new file and the revoke), so a
// predecessor never stays a live superadmin token.
func TestKeptLocalOperatorCredentialRevokesItsPredecessors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir, kept := localOperatorFixture(t, h)
	actor, err := auth.NewSystemOperator("test/local-operator", "a predecessor the earlier start did not revoke")
	if err != nil {
		t.Fatal(err)
	}
	stray, _, err := h.authr.IssueToken(ctx, actor, auth.TokenSpec{Name: localOperatorPrefix(dir) + "earlier", Superadmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalOperatorToken(ctx, h.authr, h.st, dir); err != nil {
		t.Fatal(err)
	}
	if got := readLocalOperatorToken(t, dir); got != kept {
		t.Fatal("the restart replaced a credential that still works")
	}
	if _, err := h.authr.Authenticate(ctx, stray); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("the predecessor still authenticates after a restart: %v", err)
	}
}

// The default PostgreSQL install (application and owner roles, no BYPASSRLS admin pool)
// cannot enumerate every organization, and the credential was never left there: the CLI
// stayed signed out after setup and after every restart.
func TestLocalOperatorCredentialIsLeftOnDefaultPostgres(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner,
		Version: version, Logger: slog.New(&loopLog{}), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodPost, "/v1/setup", "", "",
		map[string]any{"token": setup, "email": "pg@olivares.ai", "password": "fixture-password-2026!"}); code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	path := filepath.Join(dir, localOperatorTokenFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("first-boot setup on the default PostgreSQL install left no credential: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalOperatorToken(t.Context(), eng.authr, eng.store, dir); err != nil {
		t.Fatalf("a start on the default PostgreSQL install could not leave the credential: %v", err)
	}
	if _, err := eng.authr.Authenticate(t.Context(), readLocalOperatorToken(t, dir)); err != nil {
		t.Fatalf("the credential left on PostgreSQL does not authenticate: %v", err)
	}
}

// An API token, the local operator's included, never satisfies the administrative
// step-up under any policy; only a person's session does. The refusal says so and
// names the way (`olivares login`), not "it is off by default", which sent the local
// operator's first `olivares modules on accessmap` into a dead end.
func TestStepUpRefusalTellsTheTokenHolderToSignIn(t *testing.T) {
	err := bootstrapHTTPError(http.StatusForbidden, []byte(`{"error":{"code":"step_up_required","message":"auth: step-up required"}}`))
	if err == nil || !strings.Contains(err.Error(), "olivares login") || strings.Contains(err.Error(), "off by default") {
		t.Fatalf("the step-up refusal must send a token holder to `olivares login`, got: %v", err)
	}
}

// loginDiscoveryFixture is an engine `olivares login` may find through a data
// directory: record writes its console state, certificate and install id into dir,
// and login runs `olivares login --email --password-file` with no --server and
// returns how many requests, each carrying the typed password, reached that engine.
func loginDiscoveryFixture(t *testing.T) (record func(dir string), login func() int64) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	for _, name := range []string{"OLIVARES_DATA_DIR", "OLIVARES_SERVER_URL", "OLIVARES_TOKEN", "OLIVARES_TENANT", cliConfigOverrideEnv} {
		t.Setenv(name, "")
	}
	var hits atomic.Int64
	engine := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"code":"unauthenticated","message":"no"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(engine.Close)
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: engine.Certificate().Raw})
	record = func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "install-id"), []byte("recorded\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tlsCertFile), crt, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeConsoleState(dir, consoleState{Version: consoleStateVersion, Listen: strings.TrimPrefix(engine.URL, "https://")}); err != nil {
			t.Fatal(err)
		}
	}
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("typed-password-2026!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	login = func() int64 {
		t.Helper()
		before := hits.Load()
		_, _, _ = execRoot(t, "login", "--email", "ana@example.com", "--password-file", pw)
		return hits.Load() - before
	}
	return record, login
}

// A cloned repository (or an unpacked archive) with an olivares-data under the working
// directory chose the engine `olivares login` sent the typed password to, trusted
// through the certificate planted next to it.
func TestLoginIgnoresADataDirectoryUnderTheWorkingDirectory(t *testing.T) {
	record, login := loginDiscoveryFixture(t)
	work := t.TempDir()
	record(filepath.Join(work, legacyDataDirName))
	t.Chdir(work)
	if n := login(); n != 0 {
		t.Fatalf("a data directory a cloned repository put under the working directory chose the engine: %d requests carried the password", n)
	}
}

// In a data directory other accounts can write, one of them replaced console.json and
// tls.crt with its own endpoint and CA, and the next `olivares login` sent the typed
// password there. The owner's private directory still finds the engine.
func TestLoginIgnoresADataDirectoryOtherAccountsCanWrite(t *testing.T) {
	record, login := loginDiscoveryFixture(t)
	own := filepath.Join(t.TempDir(), "data")
	record(own)
	t.Setenv("OLIVARES_DATA_DIR", own)
	if n := login(); n == 0 {
		t.Fatal("the data directory's owner no longer finds the engine it recorded")
	}
	for _, c := range []struct {
		name      string
		path      string
		bad, good os.FileMode
	}{
		{"a data directory other accounts can write", own, 0o770, 0o700},
		{"a console state other accounts can write", filepath.Join(own, consoleStateFile), 0o666, 0o600},
		{"a certificate other accounts can write", filepath.Join(own, tlsCertFile), 0o666, 0o644},
	} {
		if err := os.Chmod(c.path, c.bad); err != nil {
			t.Fatal(err)
		}
		if n := login(); n != 0 {
			t.Errorf("%s chose the engine: %d requests carried the password", c.name, n)
		}
		if err := os.Chmod(c.path, c.good); err != nil {
			t.Fatal(err)
		}
	}
}
