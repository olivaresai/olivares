// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

package main

// C-21 of an internal design note (not shipped), executed rather than described:
// "clean install → the `olivares` CLI creates token/user/tenant → the API answers
// authenticated · finishes 0 without opening the console".
//
// WHAT MAKES THIS DIFFERENT FROM e2e_binary_test.go. That test walks the same
// install→setup→login path with a raw http.Client, which is exactly why it could
// stay green while the product had no browser-free path at all: it proved the API
// works, never that an OPERATOR can reach it. Here every state-changing step is an
// `olivares` subcommand, executed as the real binary with its real exit code. The
// only non-CLI HTTP in this file is the readiness probe, and it is a probe, not a
// console: a kubelet does the same thing.
//
// The negative control is built in. `olivares tokens ls` against the engine with
// NO credential must fail (exit 2) before any of this, and the same command must
// succeed with the token the walkthrough minted. A green run therefore says both
// "the door was shut" and "the key opens it".

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cliRun is one invocation of the real binary, with the CLI's own client config
// redirected into the test's temp dir so the walkthrough cannot read (or corrupt)
// the developer's ~/.config/olivares/config.yaml.
type cliRunner struct {
	bin        string
	configPath string
	base       string
}

func (r cliRunner) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = append(os.Environ(),
		"OLIVARES_CLI_CONFIG="+r.configPath,
		"OLIVARES_SERVER_URL=",
		"OLIVARES_TOKEN=",
		"OLIVARES_TENANT=",
	)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("olivares %s: %v", strings.Join(args, " "), err)
		}
	}
	return out.String(), errb.String(), code
}

// mustRun fails the walkthrough at the first step that does not finish 0, naming
// the step — "finishes 0" is the criterion, so a non-zero exit is the result.
func (r cliRunner) mustRun(t *testing.T, what, stdin string, args ...string) string {
	t.Helper()
	out, errb, code := r.run(t, stdin, args...)
	if code != 0 {
		t.Fatalf("%s: exit %d\nargs: olivares %s\nstdout:\n%s\nstderr:\n%s",
			what, code, strings.Join(args, " "), out, errb)
	}
	return out
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func TestE2ECLIBootstrapReachesOperationalWithoutABrowser(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "olivares")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// ── A CLEAN INSTALL ──────────────────────────────────────────────────────
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	base := "http://" + addr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serve := exec.CommandContext(ctx, bin, "serve", "--insecure",
		"--listen", addr, "--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--data-dir", filepath.Join(dir, "data"))
	var serveOut bytes.Buffer
	serve.Stdout = &serveOut
	serve.Stderr = io.Discard
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() { _ = serve.Process.Kill(); _, _ = serve.Process.Wait() })

	cli := cliRunner{bin: bin, configPath: filepath.Join(dir, "cli-config.yaml"), base: base}

	// Readiness, driven by the CLI itself: `status` reads the unauthenticated
	// /status page. Exit 6 is "not reachable yet"; anything else means the engine
	// answered (0 operational, 7 degraded — a fresh install may legitimately
	// report optional capabilities unconfigured, and that is not a failure here).
	ready := false
	for i := 0; i < 150 && !ready; i++ {
		_, _, code := cli.run(t, "", "status", "--server", base)
		if code != exitcode.Server {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("the engine never answered `olivares status`; serve stdout:\n%s", serveOut.String())
	}

	// ── NEGATIVE CONTROL, BEFORE ANYTHING EXISTS ─────────────────────────────
	// Without a credential the first-run families must refuse. If this passed,
	// every "it works" below would be measuring an open door.
	if out, _, code := cli.run(t, "", "tokens", "ls", "--server", base); code != exitcode.Usage {
		t.Fatalf("`tokens ls` with no credential exited %d, want %d (usage); stdout:\n%s",
			code, exitcode.Usage, out)
	}

	// ── STEP 1 · redeem the one-time first-boot token, no console ─────────────
	var setupToken string
	for i := 0; i < 100 && setupToken == ""; i++ {
		setupToken = setupTokenRE.FindString(serveOut.String())
		if setupToken == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if setupToken == "" {
		t.Fatalf("no one-time setup token on serve's stdout:\n%s", serveOut.String())
	}
	tokenFile := filepath.Join(dir, "setup.token")
	if err := os.WriteFile(tokenFile, []byte(setupToken), 0o600); err != nil {
		t.Fatal(err)
	}
	const adminPassword = "e2e-first-admin-password"

	bootstrapOut := cli.mustRun(t, "auth bootstrap", adminPassword,
		"auth", "bootstrap", "--server", base,
		"--setup-token-file", tokenFile,
		"--email", "admin@e2e.test", "--password-file", "-",
		"--organization", "E2E Org", "-o", "json")
	var setupResult struct {
		ID           string `json:"id"`
		Organization struct {
			TenantID string `json:"tenant_id"`
		} `json:"organization"`
	}
	if err := json.Unmarshal([]byte(bootstrapOut), &setupResult); err != nil {
		t.Fatalf("auth bootstrap -o json is not JSON: %v\n%s", err, bootstrapOut)
	}
	firstTenant := setupResult.Organization.TenantID
	if firstTenant == "" || setupResult.ID == "" {
		t.Fatalf("setup did not report the first tenant and superadmin: %s", bootstrapOut)
	}

	// The token is single-use: a second redemption must fail. This is what proves
	// the first one CONSUMED it rather than merely being accepted.
	if _, _, code := cli.run(t, adminPassword, "auth", "bootstrap", "--server", base,
		"--setup-token-file", tokenFile, "--email", "second@e2e.test", "--password-file", "-"); code == 0 {
		t.Fatal("the one-time setup token was accepted twice")
	}

	// ── STEP 2 · sign in with the account just created ───────────────────────
	cli.mustRun(t, "auth login", adminPassword,
		"auth", "login", "--server", base, "--email", "admin@e2e.test", "--password-file", "-")

	// From here the saved client context supplies server and credential: this is
	// the state a real operator is in after step 2.
	// The saved context must resolve to the tenant setup created and to the role
	// that owns it. `actor` is the principal id, not the email — assert the FACTS
	// the next steps depend on, not a string the engine never promised.
	whoami := cli.mustRun(t, "auth status", "", "auth", "status", "-o", "json")
	if !strings.Contains(whoami, firstTenant) {
		t.Fatalf("`auth status` does not resolve to the tenant setup created (%s):\n%s", firstTenant, whoami)
	}
	if !strings.Contains(whoami, `"role": "owner"`) {
		t.Fatalf("`auth status` does not report the first superadmin as owner of its tenant:\n%s", whoami)
	}
	if strings.Contains(whoami, adminPassword) {
		t.Fatalf("`auth status` echoed the password:\n%s", whoami)
	}

	// ── STEP 3 · create a TENANT ─────────────────────────────────────────────
	tenantOut := cli.mustRun(t, "tenants create", "",
		"tenants", "create", "--name", "Second Org", "--slug", "second", "-o", "json")
	var tenant struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal([]byte(tenantOut), &tenant); err != nil || tenant.TenantID == "" {
		t.Fatalf("tenants create did not return a tenant id: %v\n%s", err, tenantOut)
	}
	if list := cli.mustRun(t, "tenants ls", "", "tenants", "ls"); !strings.Contains(list, tenant.TenantID) {
		t.Fatalf("the tenant just created is not in `tenants ls`:\n%s", list)
	}

	// ── STEP 4 · create a USER ───────────────────────────────────────────────
	userOut := cli.mustRun(t, "users create", "e2e-member-password",
		"users", "create", "--email", "member@e2e.test", "--display-name", "Member",
		"--password-file", "-", "-o", "json")
	var user struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(userOut), &user); err != nil || user.ID == "" {
		t.Fatalf("users create did not return a user id: %v\n%s", err, userOut)
	}

	// ── STEP 5 · give it a MEMBERSHIP in the new tenant ──────────────────────
	cli.mustRun(t, "members grant", "",
		"members", "grant", "--tenant", tenant.TenantID, "--user", user.ID, "--role", "admin")
	roster := cli.mustRun(t, "members ls", "", "members", "ls", "--tenant", tenant.TenantID)
	if !strings.Contains(roster, "member@e2e.test") {
		t.Fatalf("the account just granted is not on the tenant roster:\n%s", roster)
	}

	// ── STEP 6 · mint an API TOKEN for that tenant ───────────────────────────
	tokenOut := cli.mustRun(t, "tokens issue", "",
		"tokens", "issue", "--name", "e2e-ci", "--tenant", tenant.TenantID, "--role", "admin", "-o", "json")
	var issued struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal([]byte(tokenOut), &issued); err != nil || issued.Token == "" {
		t.Fatalf("tokens issue did not return a secret: %v\n%s", err, tokenOut)
	}

	// ── STEP 7 · the API ANSWERS AUTHENTICATED to that token ─────────────────
	// The whole point of C-21: a credential produced entirely by the CLI, used by
	// the CLI, against the running engine.
	status := cli.mustRun(t, "auth status with the minted token", "",
		"auth", "status", "--server", base, "--token", issued.Token, "--tenant", tenant.TenantID, "-o", "json")
	if !strings.Contains(status, tenant.TenantID) {
		t.Fatalf("the minted token did not authenticate into its tenant:\n%s", status)
	}
	if strings.Contains(status, issued.Token) {
		t.Fatalf("`auth status` printed the bearer in full:\n%s", status)
	}
	listed := cli.mustRun(t, "tokens ls with the minted token", "",
		"tokens", "ls", "--server", base, "--token", issued.Token, "--tenant", tenant.TenantID)
	if !strings.Contains(listed, "e2e-ci") {
		t.Fatalf("the minted token cannot list its own tenant's tokens:\n%s", listed)
	}

	// ── CLOSING CONTROL · revoking it shuts the door again ───────────────────
	cli.mustRun(t, "tokens revoke", "",
		"tokens", "revoke", issued.ID, "--yes", "--server", base, "--token", issued.Token)
	if _, _, code := cli.run(t, "", "tokens", "ls", "--server", base,
		"--token", issued.Token, "--tenant", tenant.TenantID); code != exitcode.Auth {
		t.Fatalf("a revoked token still authenticates (exit %d, want %d)", code, exitcode.Auth)
	}
}
