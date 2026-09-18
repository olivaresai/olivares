// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// agentToolFixture stands up the real verifier, a throwaway signing key and a
// release server, and points the CLI's engine seam at them. The CLI is invoked
// through the real root command, so every flag, exit code and output path is
// the one an operator gets.
type agentToolFixture struct {
	t      *testing.T
	key    *toolinstalltest.SigningKey
	srv    *toolinstalltest.Server
	root   string
	marker string
}

func newAgentToolFixture(t *testing.T) *agentToolFixture {
	t.Helper()
	toolinstalltest.RequireGPG(t)
	verifier, err := toolinstall.NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	key := toolinstalltest.GenerateKey(t, "Olivares CLI Fixture <cli-fixture@olivares.invalid>")
	srv := toolinstalltest.NewServer(t)
	dir := toolinstalltest.ExecCapableDir(t)
	f := &agentToolFixture{t: t, key: key, srv: srv, root: filepath.Join(dir, "tools"), marker: filepath.Join(dir, "ran.marker")}
	prev := toolInstallEngine
	toolInstallEngine = func(context.Context) *toolinstall.Engine {
		claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: srv.URL, Verifier: verifier, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond}, key.Public, key.Fingerprint)
		return toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{InstallerVersion: "test"})
	}
	t.Cleanup(func() { toolInstallEngine = prev })
	// A data dir nobody else uses, so the default root is deterministic.
	t.Setenv("OLIVARES_DATA_DIR", filepath.Join(dir, "data"))
	return f
}

func (f *agentToolFixture) publish(version string) {
	f.t.Helper()
	f.srv.Publish(f.t, f.key, version, map[string][]byte{"linux-x64": toolinstalltest.Executable(version, f.marker)})
	f.srv.SetPointer("latest", version)
}

func (f *agentToolFixture) ran() int {
	b, _ := os.ReadFile(f.marker)
	return strings.Count(string(b), "ran\n")
}

// run executes the CLI in-process with a non-interactive stdin and returns the
// process exit code the binary would use.
func (f *agentToolFixture) run(args ...string) (code int, stdout, stderr string) {
	f.t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	_, err := root.ExecuteC()
	if err == nil {
		return exitcode.OK, out.String(), errb.String()
	}
	// The root silences cobra's own printing and runMain writes the error to
	// stderr; mirror that so assertions see what the operator sees.
	return exitcode.From(err), out.String(), errb.String() + "Error: " + err.Error() + "\n"
}

func (f *agentToolFixture) installArgs(extra ...string) []string {
	args := []string{"agent", "tool", "install", "--driver", "claude", "--platform", "linux-x64", "--root", f.root, "--source", f.srv.URL}
	return append(args, extra...)
}

func TestAgentToolInstallPositiveNoopSecondVersionAndList(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")

	// A non-interactive session without --yes is refused as usage, before any download.
	code, _, stderr := f.run(f.installArgs("--version", "2.1.261")...)
	if code != exitcode.Usage || !strings.Contains(stderr, "plan ") {
		t.Fatalf("no --yes: code %d stderr %q", code, stderr)
	}
	if f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64")) != 0 {
		t.Fatal("artifact fetched without confirmation")
	}

	code, stdout, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes", "-o", "json")...)
	if code != exitcode.OK {
		t.Fatalf("install: code %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	var result struct {
		Action  string              `json:"action"`
		Receipt toolinstall.Receipt `json:"receipt"`
		Plan    toolinstall.Plan    `json:"plan"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("install JSON: %v\n%s", err, stdout)
	}
	exe := filepath.Join(f.root, "claude", "2.1.261-linux-x64", "bin", "claude")
	if result.Action != "install" || result.Receipt.Destination.Executable != exe || result.Plan.Digest == "" {
		t.Fatalf("result %+v", result)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != result.Receipt.Artifact.SHA256 {
		t.Fatal("receipt digest differs from the installed bytes")
	}
	if !strings.HasPrefix(result.Receipt.Probe.Output, "2.1.261 (Claude Code)") || f.ran() != 1 {
		t.Fatalf("probe %q ran=%d", result.Receipt.Probe.Output, f.ran())
	}
	for _, phase := range []string{"manifest signature verified", "downloading", "probing the staged executable", "placed " + exe} {
		if !strings.Contains(stderr, phase) {
			t.Fatalf("progress lacks %q:\n%s", phase, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "claude", "2.1.261-linux-x64", toolinstall.ReceiptFile)); err != nil {
		t.Fatal(err)
	}

	// Identical second install: noop, same receipt, no artifact GET, no execution.
	gets := f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64"))
	code, stdout, stderr = f.run(f.installArgs("--version", "2.1.261", "--yes")...)
	if code != exitcode.OK || !strings.HasPrefix(stdout, "noop: claude 2.1.261 (linux-x64)") {
		t.Fatalf("noop: code %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64")) != gets || f.ran() != 1 {
		t.Fatal("noop fetched or executed")
	}

	// A second version lands beside the first.
	f.publish("2.1.262")
	code, stdout, stderr = f.run(f.installArgs("--version", "latest", "--yes")...)
	if code != exitcode.OK || !strings.HasPrefix(stdout, "install: claude 2.1.262 (linux-x64)") {
		t.Fatalf("second version: code %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("first release gone: %v", err)
	}

	// list shows both with matching digests, in text and JSON.
	code, stdout, _ = f.run("agent", "tool", "list", "--root", f.root, "-o", "json")
	if code != exitcode.OK {
		t.Fatalf("list: %d", code)
	}
	var inv toolinstall.Inventory
	if err := json.Unmarshal([]byte(stdout), &inv); err != nil {
		t.Fatal(err)
	}
	if len(inv.Installed) != 2 || inv.Installed[0].SHA256 != result.Receipt.Artifact.SHA256 || inv.Installed[0].State != "installed" || inv.Installed[1].Version != "2.1.262" {
		t.Fatalf("inventory %+v", inv.Installed)
	}
	code, stdout, _ = f.run("agent", "tool", "list", "--root", f.root)
	if code != exitcode.OK || !strings.Contains(stdout, "2.1.261") || !strings.Contains(stdout, "2.1.262") || !strings.Contains(stdout, "installed") {
		t.Fatalf("list text: %d\n%s", code, stdout)
	}

	// detect reports the managed releases as registered without executing them.
	ran := f.ran()
	code, stdout, _ = f.run("agent", "tool", "detect", "--root", f.root, "-o", "json")
	if code != exitcode.OK {
		t.Fatalf("detect: %d", code)
	}
	var cands []toolinstall.Candidate
	if err := json.Unmarshal([]byte(stdout), &cands); err != nil {
		t.Fatal(err)
	}
	registered := 0
	for _, c := range cands {
		if c.Origin == "managed" && c.Match == "registered" {
			registered++
		}
	}
	if registered != 2 || f.ran() != ran {
		t.Fatalf("detect registered=%d ran %d→%d: %+v", registered, ran, f.ran(), cands)
	}
}

func TestAgentToolPlanWritesFileAndInstallPlanRefusesChange(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	planFile := filepath.Join(t.TempDir(), "claude.plan.json")
	code, stdout, stderr := f.run("agent", "tool", "plan", "--driver", "claude", "--version", "2.1.261", "--platform", "linux-x64", "--root", f.root, "--source", f.srv.URL, "--out", planFile, "-o", "json")
	if code != exitcode.OK {
		t.Fatalf("plan: %d\n%s\n%s", code, stdout, stderr)
	}
	var plan toolinstall.Plan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Action != "install" || plan.Digest == "" || plan.Artifact.Size == 0 {
		t.Fatalf("plan %+v", plan)
	}
	if _, err := os.Stat(f.root); !os.IsNotExist(err) {
		t.Fatalf("plan created the root: %v", err)
	}
	if f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64")) != 0 || f.ran() != 0 {
		t.Fatal("plan fetched or executed")
	}
	// Text form names the essentials.
	code, stdout, _ = f.run("agent", "tool", "plan", "--version", "2.1.261", "--platform", "linux-x64", "--root", f.root, "--source", f.srv.URL)
	if code != exitcode.OK || !strings.Contains(stdout, plan.Artifact.SHA256) || !strings.Contains(stdout, "publisher-signed") || !strings.Contains(stdout, f.key.Fingerprint) {
		t.Fatalf("plan text: %d\n%s", code, stdout)
	}
	// --out refuses to overwrite.
	code, _, stderr = f.run("agent", "tool", "plan", "--version", "2.1.261", "--platform", "linux-x64", "--root", f.root, "--source", f.srv.URL, "--out", planFile)
	if code != exitcode.Usage || !strings.Contains(stderr, "not overwritten") {
		t.Fatalf("overwrite: %d %s", code, stderr)
	}

	// The vendor re-publishes the same version with other bytes: the approved plan is stale.
	f.srv.Publish(t, f.key, "2.1.261", map[string][]byte{"linux-x64": toolinstalltest.Executable("2.1.261", "")})
	code, _, stderr = f.run("agent", "tool", "install", "--plan", planFile)
	if code != exitcode.Conflict || !strings.Contains(stderr, "plan_changed") {
		t.Fatalf("stale plan: %d %s", code, stderr)
	}
	if f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64")) != 0 {
		t.Fatal("stale plan fetched the artifact")
	}
	// Contradicting flags are usage errors.
	f.publish("2.1.261")
	code, _, stderr = f.run("agent", "tool", "install", "--plan", planFile, "--version", "2.1.262")
	if code != exitcode.Usage || !strings.Contains(stderr, "contradicts") {
		t.Fatalf("contradiction: %d %s", code, stderr)
	}
	// The matching plan installs with no prompt: the file is the approval.
	code, stdout, stderr = f.run("agent", "tool", "install", "--plan", planFile)
	if code != exitcode.OK || !strings.HasPrefix(stdout, "install: claude 2.1.261") {
		t.Fatalf("install --plan: %d\n%s\n%s", code, stdout, stderr)
	}
	raw, _ := os.ReadFile(filepath.Join(f.root, "claude", "2.1.261-linux-x64", toolinstall.ReceiptFile))
	if !strings.Contains(string(raw), plan.Digest) {
		t.Fatal("receipt does not record the approved plan digest")
	}
	// The approval file carries no observation fields; injecting one with the
	// digest left intact is refused before anything is resolved or fetched.
	original := must(os.ReadFile(planFile))
	for _, k := range []string{`"action"`, `"existing"`, `"observed"`} {
		if bytes.Contains(original, []byte(k)) {
			t.Fatalf("approval file carries %s", k)
		}
	}
	gets := f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64"))
	manifestGETs := f.srv.Count("/2.1.261/manifest.json")
	injected := bytes.Replace(original, []byte(`"verified": [`), []byte("\"action\": \"noop\",\n  \"verified\": ["), 1)
	if err := os.WriteFile(planFile, injected, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = f.run("agent", "tool", "install", "--plan", planFile)
	if code != exitcode.Usage || !strings.Contains(stderr, "observation field") {
		t.Fatalf("injected action: %d %s", code, stderr)
	}
	if f.srv.Count(toolinstalltest.ArtifactPath("2.1.261", "linux-x64")) != gets || f.srv.Count("/2.1.261/manifest.json") != manifestGETs {
		t.Fatal("a refused approval file still reached the network")
	}
	// An edited selection is refused before anything is resolved.
	edited := bytes.Replace(original, []byte(`"size": `), []byte(`"size": 1`), 1)
	if err := os.WriteFile(planFile, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = f.run("agent", "tool", "install", "--plan", planFile)
	if code != exitcode.Usage || !strings.Contains(stderr, "does not match its content") {
		t.Fatalf("edited plan: %d %s", code, stderr)
	}
}

func TestAgentToolApprovalFileRefusesDecoderEquivalentObservationClaims(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	artifact := toolinstalltest.ArtifactPath("2.1.261", "linux-x64")
	manifest := "/2.1.261/manifest.json"
	sig := "/2.1.261/manifest.json.sig"

	writePlan := func(t *testing.T, root string) (planFile string, original []byte) {
		t.Helper()
		planFile = filepath.Join(t.TempDir(), "claude.plan.json")
		code, _, stderr := f.run("agent", "tool", "plan", "--driver", "claude", "--version", "2.1.261", "--platform", "linux-x64", "--root", root, "--source", f.srv.URL, "--out", planFile)
		if code != exitcode.OK {
			t.Fatalf("plan: %d %s", code, stderr)
		}
		original = must(os.ReadFile(planFile))
		for _, k := range []string{`"action"`, `"existing"`, `"observed"`} {
			if bytes.Contains(original, []byte(k)) {
				t.Fatalf("plan --out wrote observation key %s", k)
			}
		}
		return planFile, original
	}
	inject := func(original []byte, field string) []byte {
		edited := bytes.Replace(original, []byte(`"verified": [`), []byte(field+"\n  \"verified\": ["), 1)
		if bytes.Equal(edited, original) {
			t.Fatal("injection anchor missing")
		}
		return edited
	}
	counts := func() (int, int, int) {
		return f.srv.Count(manifest), f.srv.Count(sig), f.srv.Count(artifact)
	}

	cases := []struct {
		name  string
		field string
	}{
		{"lowercase action", `"action": "noop",`},
		{"title action", `"Action": "noop",`},
		{"upper action", `"ACTION": "noop",`},
		{"mixed action", `"aCtIoN": "noop",`},
		{"unicode-escaped title action", `"\u0041ction": "noop",`},
		{"unicode-escaped lowercase action", `"\u0061ction": "noop",`},
		{"lowercase observed", `"observed": [],`},
		{"title observed", `"Observed": [],`},
		{"upper observed", `"OBSERVED": [],`},
		{"mixed observed", `"ObSeRvEd": [],`},
		{"unicode-escaped title observed", `"\u004fbserved": [],`},
		{"long-s observed", `"ob\u017ferved": [],`},
		{"lowercase existing", `"existing": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
		{"title existing", `"Existing": {"release_dir": "/x", "receipt_present": true, "executable_present": true, "note": "already installed; this approval downloads nothing"},`},
		{"upper existing", `"EXISTING": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
		{"mixed existing", `"eXiStInG": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
		{"unicode-escaped title existing", `"\u0045xisting": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
		{"long-s existing", `"exi\u017fting": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
		{"F4 combo title claims", `"Action": "noop",` + "\n  " + `"Observed": [],` + "\n  " + `"Existing": {"release_dir": "/x", "receipt_present": true, "executable_present": true, "note": "already installed; this approval downloads nothing"},`},
		{"duplicate action spellings", `"Action": "noop",` + "\n  " + `"ACTION": "noop",`},
	}

	base := toolinstalltest.ExecCapableDir(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(base, "obs", strings.ReplaceAll(t.Name(), "/", "_"), "tools")
			planFile, original := writePlan(t, root)
			m0, s0, a0 := counts()
			if err := os.WriteFile(planFile, inject(original, tc.field), 0o644); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := f.run("agent", "tool", "install", "--plan", planFile)
			if code != exitcode.Usage || !strings.Contains(stderr, "observation field") {
				t.Fatalf("code %d stdout %s stderr %s", code, stdout, stderr)
			}
			if tc.name == "lowercase action" || tc.name == "unicode-escaped lowercase action" {
				if !strings.Contains(stderr, `observation field "action"`) {
					t.Fatalf("lowercase action diagnostic drifted: %s", stderr)
				}
			}
			m1, s1, a1 := counts()
			if m1 != m0 || s1 != s0 || a1 != a0 {
				t.Fatalf("refused approval still fetched: manifest %d→%d sig %d→%d artifact %d→%d", m0, m1, s0, s1, a0, a1)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("refused approval created the root: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "claude")); !os.IsNotExist(err) {
				t.Fatalf("refused approval created a release: %v", err)
			}
		})
	}

	root := filepath.Join(base, "positive", "tools")
	planFile, original := writePlan(t, root)

	t.Run("verified edit refused by digest", func(t *testing.T) {
		m0, s0, a0 := counts()
		edited := bytes.Replace(original, []byte(`"manifest_signature"`), []byte(`"everything"`), 1)
		if err := os.WriteFile(planFile+"-verified", edited, 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := f.run("agent", "tool", "install", "--plan", planFile+"-verified")
		if code != exitcode.Usage || !strings.Contains(stderr, "does not match its content") {
			t.Fatalf("verified edit: %d %s", code, stderr)
		}
		m1, s1, a1 := counts()
		if m1 != m0 || s1 != s0 || a1 != a0 {
			t.Fatal("verified edit still fetched")
		}
	})
	t.Run("selection edit refused by digest", func(t *testing.T) {
		m0, s0, a0 := counts()
		edited := bytes.Replace(original, []byte(`"size": `), []byte(`"size": 1`), 1)
		if err := os.WriteFile(planFile+"-size", edited, 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := f.run("agent", "tool", "install", "--plan", planFile+"-size")
		if code != exitcode.Usage || !strings.Contains(stderr, "does not match its content") {
			t.Fatalf("selection edit: %d %s", code, stderr)
		}
		m1, s1, a1 := counts()
		if m1 != m0 || s1 != s0 || a1 != a0 {
			t.Fatal("selection edit still fetched")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("selection edit created the root: %v", err)
		}
	})

	code, stdout, stderr := f.run("agent", "tool", "install", "--plan", planFile)
	if code != exitcode.OK || !strings.HasPrefix(stdout, "install: claude 2.1.261") {
		t.Fatalf("untouched approval: %d\n%s\n%s", code, stdout, stderr)
	}
	exe := filepath.Join(root, "claude", "2.1.261-linux-x64", "bin", "claude")
	sum := sha256OfFile(t, exe)
	receipt := must(os.ReadFile(filepath.Join(root, "claude", "2.1.261-linux-x64", toolinstall.ReceiptFile)))
	if !bytes.Contains(receipt, []byte(`"plan_digest"`)) {
		t.Fatal("receipt missing plan_digest")
	}

	// Identical-manifest re-signing keeps the approval valid; the second run is a
	// revalidated no-op and does not change the installed bytes.
	f.srv.Set(sig, f.key.Sign(t, f.srv.Get(manifest)))
	gets := f.srv.Count(artifact)
	ran := f.ran()
	code, stdout, stderr = f.run("agent", "tool", "install", "--plan", planFile)
	if code != exitcode.OK || !strings.HasPrefix(stdout, "noop: claude 2.1.261") {
		t.Fatalf("re-sign noop: %d\n%s\n%s", code, stdout, stderr)
	}
	if f.srv.Count(artifact) != gets || f.ran() != ran {
		t.Fatal("noop fetched or executed")
	}
	if sha256OfFile(t, exe) != sum {
		t.Fatal("noop changed installed bytes")
	}
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestAgentToolInstallNegativeControls(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	manifest := f.srv.Get("/2.1.261/manifest.json")
	artifact := toolinstalltest.ArtifactPath("2.1.261", "linux-x64")

	t.Run("wrong signature: zero artifact GETs, exit 1", func(t *testing.T) {
		other := toolinstalltest.GenerateKey(t, "Other <other@olivares.invalid>")
		f.srv.Set("/2.1.261/manifest.json.sig", other.Sign(t, manifest))
		defer f.publish("2.1.261")
		code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...)
		if code != exitcode.Err || !strings.Contains(stderr, "signature_invalid") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
		if f.srv.Count(artifact) != 0 || f.ran() != 0 {
			t.Fatal("bad signature led to a download or execution")
		}
	})
	t.Run("invalid hash: zero executions, exit 1, nothing left behind", func(t *testing.T) {
		good := f.srv.Get(artifact)
		bad := append([]byte(nil), good...)
		bad[len(bad)-2] ^= 1
		f.srv.Set(artifact, bad)
		defer f.srv.Set(artifact, good)
		code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...)
		if code != exitcode.Err || !strings.Contains(stderr, "digest_mismatch") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
		if f.ran() != 0 {
			t.Fatal("wrong bytes were executed")
		}
		if entries, _ := os.ReadDir(filepath.Join(f.root, "claude")); len(entries) != 0 {
			t.Fatalf("leftovers after refusal: %v", entries)
		}
	})
	t.Run("truncated body: exit 1", func(t *testing.T) {
		good := f.srv.Get(artifact)
		f.srv.Set(artifact, good[:len(good)-1])
		defer f.srv.Set(artifact, good)
		code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...)
		if code != exitcode.Err || !strings.Contains(stderr, "size_mismatch") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
	})
	t.Run("unsupported platform, driver and source: exit 2", func(t *testing.T) {
		gets := f.srv.Count(artifact)
		for _, args := range [][]string{
			{"agent", "tool", "install", "--platform", "win32-x64", "--root", f.root, "--source", f.srv.URL, "--yes"},
			{"agent", "tool", "install", "--driver", "not-a-cli", "--platform", "linux-x64", "--root", f.root, "--yes"},
			{"agent", "tool", "install", "--platform", "linux-x64", "--root", f.root, "--source", "ftp://mirror.invalid", "--yes"},
			{"agent", "tool", "install", "--platform", "linux-x64", "--root", "relative/tools", "--yes"},
			{"agent", "tool", "plan", "--platform", "riscv-x64", "--root", f.root},
		} {
			code, _, stderr := f.run(args...)
			if code != exitcode.Usage {
				t.Fatalf("%v: code %d stderr %s", args, code, stderr)
			}
		}
		if f.srv.Count(artifact) != gets {
			t.Fatal("a refused request fetched the artifact")
		}
	})
	t.Run("unknown version: exit 8", func(t *testing.T) {
		code, _, stderr := f.run(f.installArgs("--version", "9.9.9", "--yes")...)
		if code != exitcode.Indeterminate || !strings.Contains(stderr, "version_unknown") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
	})
	t.Run("gpg unavailable: exit 8, no download", func(t *testing.T) {
		prev := toolInstallEngine
		toolInstallEngine = func(context.Context) *toolinstall.Engine {
			_, err := toolinstall.NewGPGVerifier(context.Background(), func(string) (string, error) { return "", os.ErrNotExist })
			claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: f.srv.URL, Verifier: toolinstall.UnavailableVerifier{Err: err}}, f.key.Public, f.key.Fingerprint)
			return toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{})
		}
		defer func() { toolInstallEngine = prev }()
		gets := f.srv.Count(artifact)
		code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...)
		if code != exitcode.Indeterminate || !strings.Contains(stderr, "verification_unavailable") || !strings.Contains(stderr, "gpg") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
		if f.srv.Count(artifact) != gets {
			t.Fatal("download without a verifier")
		}
	})
	t.Run("concurrent installer holds the lock: exit 5, prior release intact", func(t *testing.T) {
		code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...)
		if code != exitcode.OK {
			t.Fatalf("seed install: %d %s", code, stderr)
		}
		exe := filepath.Join(f.root, "claude", "2.1.261-linux-x64", "bin", "claude")
		before, _ := os.ReadFile(exe)
		release, err := toolinstall.LockRoot(f.root)
		if err != nil {
			t.Fatal(err)
		}
		f.publish("2.1.262")
		code, _, stderr = f.run(f.installArgs("--version", "2.1.262", "--yes")...)
		release()
		if code != exitcode.Conflict || !strings.Contains(stderr, "locked") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
		after, _ := os.ReadFile(exe)
		if !bytes.Equal(before, after) {
			t.Fatal("prior release changed")
		}
	})
	t.Run("symlinked release path: exit 5, nothing written through it", func(t *testing.T) {
		elsewhere := t.TempDir()
		link := filepath.Join(f.root, "claude", "2.1.263-linux-x64")
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(link) }()
		f.publish("2.1.263")
		code, _, stderr := f.run(f.installArgs("--version", "2.1.263", "--yes")...)
		if code != exitcode.Conflict || !strings.Contains(stderr, "conflict") {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Fatalf("wrote through the symlink: %v", entries)
		}
	})
}

func TestAgentToolDetectCorroboratesWithSignedManifestOnly(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	sigPath := filepath.Join(dir, "manifest.json.sig")
	if err := os.WriteFile(manifestPath, f.srv.Get("/2.1.261/manifest.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, f.srv.Get("/2.1.261/manifest.json.sig"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A vendor-layout copy of the artifact under a private HOME.
	home := t.TempDir()
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versions, "2.1.261"), f.srv.Get(toolinstalltest.ArtifactPath("2.1.261", "linux-x64")), 0o755); err != nil {
		t.Fatal(err)
	}
	// Signed by another key, prepared while gpg is still on PATH: the verifier
	// resolved gpg's absolute path when the fixture was built, but the fixture's
	// signer looks it up per call.
	other := toolinstalltest.GenerateKey(t, "Other <other@olivares.invalid>")
	otherSig := other.Sign(t, f.srv.Get("/2.1.261/manifest.json"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/nonexistent-for-this-test")
	code, stdout, stderr := f.run("agent", "tool", "detect", "--root", f.root, "--manifest", manifestPath, "--manifest-sig", sigPath, "-o", "json")
	if code != exitcode.OK {
		t.Fatalf("detect: %d %s", code, stderr)
	}
	var cands []toolinstall.Candidate
	if err := json.Unmarshal([]byte(stdout), &cands); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range cands {
		if c.Path == filepath.Join(versions, "2.1.261") {
			found = true
			if c.Match != "manifest-corroborated" || c.Version != "2.1.261" || c.Probe != nil {
				t.Fatalf("candidate %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("vendor-layout candidate missing: %+v", cands)
	}
	if f.ran() != 0 {
		t.Fatal("detect executed without --probe")
	}
	// One half of the pair is a usage error; a signature by another key is refused.
	code, _, _ = f.run("agent", "tool", "detect", "--root", f.root, "--manifest", manifestPath)
	if code != exitcode.Usage {
		t.Fatalf("half pair: %d", code)
	}
	if err := os.WriteFile(sigPath, otherSig, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--manifest", manifestPath, "--manifest-sig", sigPath)
	if code != exitcode.Err || !strings.Contains(stderr, "signature_invalid") {
		t.Fatalf("other key: %d %s", code, stderr)
	}
	// Bare --probe without a corroborating manifest runs nothing here: the
	// vendor-layout copy is unregistered-observed and is skipped with the way to
	// name it.
	code, stdout, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--probe")
	if code != exitcode.OK || strings.Contains(stdout, "probe: ") || !strings.Contains(stdout, "probe skipped") || !strings.Contains(stdout, "--probe-path") || f.ran() != 0 {
		t.Fatalf("bare probe: %d ran=%d\n%s\n%s", code, f.ran(), stdout, stderr)
	}
	// With the verified manifest the same candidate is corroborated and runs.
	if err := os.WriteFile(sigPath, f.srv.Get("/2.1.261/manifest.json.sig"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--manifest", manifestPath, "--manifest-sig", sigPath, "--probe")
	if code != exitcode.OK || !strings.Contains(stdout, "probe: 2.1.261 (Claude Code)") || f.ran() != 1 {
		t.Fatalf("corroborated probe: %d ran=%d\n%s\n%s", code, f.ran(), stdout, stderr)
	}
	// Naming the exact path runs it without any manifest.
	vendorBin := filepath.Join(versions, "2.1.261")
	code, stdout, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--probe-path", vendorBin)
	if code != exitcode.OK || !strings.Contains(stdout, "probe: 2.1.261 (Claude Code)") || f.ran() != 2 {
		t.Fatalf("named probe: %d ran=%d\n%s\n%s", code, f.ran(), stdout, stderr)
	}
	// A relative name is a usage error; a missing named path exits 1 and says why.
	code, _, _ = f.run("agent", "tool", "detect", "--root", f.root, "--probe-path", "relative/claude")
	if code != exitcode.Usage {
		t.Fatalf("relative probe path: %d", code)
	}
	code, stdout, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--probe-path", "/nonexistent-for-this-test/claude")
	if code != exitcode.Err || !strings.Contains(stdout, "no such file") || !strings.Contains(stderr, "probe_failed") || f.ran() != 2 {
		t.Fatalf("missing probe path: %d ran=%d\n%s\n%s", code, f.ran(), stdout, stderr)
	}
}

func TestAgentToolDamagedReleaseIsNeverProbedAndListSaysWhy(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...)
	if code != exitcode.OK {
		t.Fatalf("install: %d %s", code, stderr)
	}
	release := filepath.Join(f.root, "claude", "2.1.261-linux-x64")
	exe := filepath.Join(release, "bin", "claude")
	listState := func() (state, reason string, provenance bool) {
		code, stdout, stderr := f.run("agent", "tool", "list", "--root", f.root, "-o", "json")
		if code != exitcode.OK {
			t.Fatalf("list: %d %s", code, stderr)
		}
		var inv struct {
			Installed []struct {
				State      string          `json:"state"`
				Reason     string          `json:"reason"`
				Provenance json.RawMessage `json:"provenance"`
			} `json:"installed"`
		}
		if err := json.Unmarshal([]byte(stdout), &inv); err != nil || len(inv.Installed) != 1 {
			t.Fatalf("list JSON: %v\n%s", err, stdout)
		}
		return inv.Installed[0].State, inv.Installed[0].Reason, len(inv.Installed[0].Provenance) > 0
	}
	if state, _, prov := listState(); state != "installed" || !prov {
		t.Fatalf("fresh install: %s provenance=%t", state, prov)
	}
	// A retained signature replaced by prose: list and detect stop crediting it.
	sigPath := filepath.Join(release, toolinstall.RetainedSignature)
	goodSig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, []byte("this is not a signature at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if state, reason, prov := listState(); state != "damaged" || !strings.Contains(reason, "does not verify") || prov {
		t.Fatalf("prose signature: %s %q provenance=%t", state, reason, prov)
	}
	code, stdout, _ := f.run("agent", "tool", "detect", "--root", f.root)
	if code != exitcode.OK || !strings.Contains(stdout, "damaged") {
		t.Fatalf("detect after prose signature: %d\n%s", code, stdout)
	}
	if err := os.WriteFile(sigPath, goodSig, 0o644); err != nil {
		t.Fatal(err)
	}
	if state, _, prov := listState(); state != "installed" || !prov {
		t.Fatalf("restored signature: %s provenance=%t", state, prov)
	}
	// Replaced bytes: damaged, and never executed, whether swept or named.
	if err := os.WriteFile(exe, toolinstalltest.Executable("9.9.9", f.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	base := f.ran()
	code, stdout, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--probe")
	if code != exitcode.OK || !strings.Contains(stdout, "probe skipped: never executed") || f.ran() != base {
		t.Fatalf("bare probe on damaged: %d ran=%d\n%s\n%s", code, f.ran()-base, stdout, stderr)
	}
	code, stdout, stderr = f.run("agent", "tool", "detect", "--root", f.root, "--probe-path", exe)
	if code != exitcode.Err || !strings.Contains(stderr, "probe_failed") || !strings.Contains(stdout, "damaged") || f.ran() != base {
		t.Fatalf("named probe on damaged: %d ran=%d\n%s\n%s", code, f.ran()-base, stdout, stderr)
	}
	if state, reason, _ := listState(); state != "damaged" || !strings.Contains(reason, "receipt records") {
		t.Fatalf("list after byte replacement: %s %q", state, reason)
	}
}

func TestAgentToolDefaultRootFollowsDataDir(t *testing.T) {
	f := newAgentToolFixture(t)
	code, stdout, _ := f.run("agent", "tool", "list")
	want := filepath.Join(os.Getenv("OLIVARES_DATA_DIR"), "tools")
	if code != exitcode.OK || !strings.Contains(stdout, "no tools root at "+want) {
		t.Fatalf("default root: %d %s", code, stdout)
	}
	root, err := resolveAgentToolRoot("")
	if err != nil || root != want {
		t.Fatalf("resolveAgentToolRoot = %q, %v", root, err)
	}
	if _, err := resolveAgentToolRoot("relative"); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("relative root accepted: %v", err)
	}
}

// TestAgentToolProductionWiringTrustsOnlyTheEmbeddedKey pins the seam: the
// fixture constructor is reachable from tests only, never from the command file
// or any flag, so no production path can relax the pinned release key.
func TestAgentToolProductionWiringTrustsOnlyTheEmbeddedKey(t *testing.T) {
	src, err := os.ReadFile("cmd_agent_tool.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(src, []byte("NewClaudeWithTrust")) {
		t.Fatal("cmd_agent_tool.go must construct the Claude adapter with NewClaude only")
	}
	if !bytes.Contains(src, []byte("toolinstall.NewClaude(")) {
		t.Fatal("cmd_agent_tool.go no longer wires the production adapter")
	}
	for _, flag := range []string{`"key"`, `"fingerprint"`, `"trust"`, `"insecure"`} {
		if bytes.Contains(src, []byte("Flags().StringVar(&"+flag)) || bytes.Contains(src, []byte(", "+flag+", ")) {
			t.Fatalf("a %s flag on agent tool would let a caller relax the pinned key", flag)
		}
	}
	// The production engine builds without touching the network or the disk.
	eng := toolInstallEngine(context.Background())
	if keys := eng.Catalog().Keys(); len(keys) != 1 || keys[0] != "claude" {
		t.Fatalf("v1 catalog %v", keys)
	}
	got := strings.Join(eng.DriverKeys(), ",")
	if got != "claude,codex,grok" {
		t.Fatalf("driver keys %q", got)
	}
	if bytes.Contains(src, []byte("HashMatchingVerifier")) {
		t.Fatal("cmd_agent_tool.go must not wire the test-only HashMatchingVerifier")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
