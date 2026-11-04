// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// pullPlane stands in for the Business control plane: it signs the artifact the way the
// plane does (a detached Ed25519 signature over the domain-separated hash) and records
// the check-in a workstation sends after it has written the file.
type pullPlane struct {
	*lot3Server
	pub      ed25519.PublicKey
	mu       sync.Mutex
	checkin  map[string]any
	artifact map[string]any
	// reply is the plane's answer to a check-in; status 0 means 200.
	reply  string
	status int
	// artifactQuery is the query string of the last artifact request, artifactPath its path.
	artifactQuery string
	artifactPath  string
}

func newPullPlane(t *testing.T, tenant, surface string, revision int64, content string, mutate func(map[string]any)) *pullPlane {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	contentSHA := hex.EncodeToString(sum[:])
	digest := sha256.Sum256([]byte("olivares.policy.artifact.v1|" + tenant + "|" + surface + "|" + strconv.FormatInt(revision, 10) + "|" + contentSHA))
	fp := sha256.Sum256(pub)
	p := &pullPlane{pub: pub, reply: `{"verified":true,"drift":[]}`, artifact: map[string]any{
		"surface": surface, "revision": revision, "content": content, "artifact_sha256": contentSHA,
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest[:])),
		"pubkey":    base64.StdEncoding.EncodeToString(pub), "key_fingerprint": hex.EncodeToString(fp[:])[:16],
		"algorithm": "ed25519",
	}}
	if mutate != nil {
		mutate(p.artifact)
	}
	p.lot3Server = newLot3Server(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifact"):
			p.mu.Lock()
			p.artifactQuery = r.URL.RawQuery
			p.artifactPath = r.URL.EscapedPath()
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(p.artifact)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/checkin"):
			var in map[string]any // newLot3Server has already read the request body
			_ = json.Unmarshal([]byte(p.lastBody()), &in)
			p.mu.Lock()
			p.checkin = in
			p.mu.Unlock()
			p.mu.Lock()
			reply, status := p.reply, p.status
			p.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
			}
			_, _ = w.Write([]byte(reply))
		default:
			http.NotFound(w, r)
		}
	})
	return p
}

func (p *pullPlane) fingerprint() string { return p.artifact["key_fingerprint"].(string) }

func TestClaudePolicyPullWritesTheVerifiedFileAndAttestsIt(t *testing.T) {
	const doc = "[sandbox]\nprofile = \"strict\"\n"
	p := newPullPlane(t, "tenant-a", "grok-requirements", 3, doc, nil)
	out := filepath.Join(t.TempDir(), "requirements.toml")

	stdout, _, err := execRoot(t, lot3Args(p.URL, "claude-policy", "pull", "grok-requirements",
		"--out", out, "--scope", "host-7", "--pin-key-fingerprint", p.fingerprint())...)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, stdout)
	}
	if got, _ := os.ReadFile(out); string(got) != doc {
		t.Fatalf("file on disk = %q, want the published document", got)
	}
	info, _ := os.Stat(out)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644 (managed files are world-readable by design)", info.Mode().Perm())
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkin == nil {
		t.Fatal("the workstation did not check in after writing the file")
	}
	if p.checkin["scope"] != "host-7" || p.checkin["revision"] != float64(3) ||
		p.checkin["artifact_sha256"] != p.artifact["artifact_sha256"] || p.checkin["key_fingerprint"] != p.fingerprint() {
		t.Errorf("attestation = %v", p.checkin)
	}
	if p.checkin["observed_content"] != doc {
		t.Errorf("observed_content = %q, want the bytes read back from the file", p.checkin["observed_content"])
	}
}

// Anything that does not verify is refused before the file is touched and before any
// attestation is sent: the host keeps the file it had and the plane learns nothing false.
func TestClaudePolicyPullRefusesWhatDoesNotVerify(t *testing.T) {
	const doc = "{\"permission\":{\"edit\":\"deny\"}}"
	tests := []struct {
		name    string
		tenant  string // the plane signs for this tenant; the CLI is tenant-a
		mutate  func(a map[string]any)
		pin     func(p *pullPlane) string
		wantErr string
	}{
		{"content changed after signing", "tenant-a", func(a map[string]any) { a["content"] = doc + " " }, nil, "does not match artifact_sha256"},
		{"hash replaced to match changed content", "tenant-a", func(a map[string]any) {
			a["content"] = "{}"
			sum := sha256.Sum256([]byte("{}"))
			a["artifact_sha256"] = hex.EncodeToString(sum[:])
		}, nil, "signature does not verify"},
		{"signed for another tenant", "tenant-b", nil, nil, "signature does not verify"},
		{"signed for another revision", "tenant-a", func(a map[string]any) { a["revision"] = int64(4) }, nil, "signature does not verify"},
		{"signer is not the pinned key", "tenant-a", nil, func(*pullPlane) string { return "0123456789abcdef" }, "does not match the pinned key fingerprint"},
		{"fingerprint field lies about the key", "tenant-a", func(a map[string]any) { a["key_fingerprint"] = "0123456789abcdef" }, func(*pullPlane) string { return "0123456789abcdef" }, "is not the fingerprint of the public key"},
		{"unknown algorithm", "tenant-a", func(a map[string]any) { a["algorithm"] = "rsa" }, nil, "algorithm"},
		{"surface served is not the one asked", "tenant-a", func(a map[string]any) { a["surface"] = "sandbox" }, nil, `served surface "sandbox"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPullPlane(t, tt.tenant, "opencode-config", 3, doc, tt.mutate)
			pin := p.fingerprint()
			if tt.pin != nil {
				pin = tt.pin(p)
			}
			out := filepath.Join(t.TempDir(), "opencode.json")
			if err := os.WriteFile(out, []byte("previous"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := execRoot(t, lot3Args(p.URL, "claude-policy", "pull", "opencode-config",
				"--out", out, "--scope", "host-7", "--pin-key-fingerprint", pin)...)
			if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v (exit %d), want exit %d containing %q", err, exitcode.From(err), exitcode.Degraded, tt.wantErr)
			}
			if got, _ := os.ReadFile(out); string(got) != "previous" {
				t.Errorf("the host file was changed to %q by an artifact that did not verify", got)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.checkin != nil {
				t.Errorf("an attestation %v was sent for an artifact that did not verify", p.checkin)
			}
		})
	}
}

func TestClaudePolicyPullNeedsAPinAndAScope(t *testing.T) {
	p := newPullPlane(t, "tenant-a", "sandbox", 1, "{}", nil)
	out := filepath.Join(t.TempDir(), "f")
	for _, argv := range [][]string{
		{"claude-policy", "pull", "sandbox", "--out", out, "--scope", "h"},
		{"claude-policy", "pull", "sandbox", "--out", out, "--pin-key-fingerprint", p.fingerprint()},
		{"claude-policy", "pull", "sandbox", "--scope", "h", "--pin-key-fingerprint", p.fingerprint()},
	} {
		if _, _, err := execRoot(t, lot3Args(p.URL, argv...)...); err == nil {
			t.Errorf("%v: want a usage error", argv)
		}
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("%d request(s) were sent without a pin, scope and path", n)
	}
}

func TestClaudePolicyAcceptsEveryManagedSurface(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[]}`))
	surfaces := []string{"managed-settings", "hooks", "managed-mcp", "sandbox",
		"codex-requirements", "codex-managed-config", "grok-requirements", "opencode-config", "gemini-cli-settings"}
	for _, surface := range surfaces {
		if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "versions", "ls", surface)...); err != nil {
			t.Fatalf("surface %s must be accepted, got %v", surface, err)
		}
	}
	if n := srv.calls.Load(); int(n) != len(surfaces) {
		t.Fatalf("%d surfaces made %d requests", len(surfaces), n)
	}
}

func pullArgs(p *pullPlane, surface, out string, extra ...string) []string {
	return lot3Args(p.URL, append([]string{"claude-policy", "pull", surface, "--out", out, "--scope", "host-7",
		"--pin-key-fingerprint", p.fingerprint()}, extra...)...)
}

// --revision names the revision the operator wants; a plane that serves another one (an older
// signed artifact replayed, or the newest when a pinned older one was asked) is refused.
func TestClaudePolicyPullRefusesARevisionThatWasNotAskedFor(t *testing.T) {
	p := newPullPlane(t, "tenant-a", "sandbox", 3, "{}", nil)
	out := filepath.Join(t.TempDir(), "sandbox.json")
	_, _, err := execRoot(t, pullArgs(p, "sandbox", out, "--revision", "2")...)
	if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "revision 3, not the 2 asked for") {
		t.Fatalf("err = %v (exit %d)", err, exitcode.From(err))
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Error("a file was written for a revision that was not asked for")
	}
	// The control: asking for the revision that is served works, and is sent to the plane.
	if _, _, err := execRoot(t, pullArgs(p, "sandbox", out, "--revision", "3")...); err != nil {
		t.Fatalf("revision 3 asked and served: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.artifactQuery != "revision=3" {
		t.Errorf("the plane was asked for %q, want revision=3", p.artifactQuery)
	}
}

// A pull is not done when the plane says a newer revision exists, or that the written file
// drifts: the file is on disk and attested, and the exit code says the host is not current.
func TestClaudePolicyPullIsNotCleanWhenStaleOrDrifting(t *testing.T) {
	for name, tc := range map[string]struct{ reply, want string }{
		"newer revision exists": {`{"verified":true,"latest_revision":5,"drift":[]}`, "STALE: wrote revision 3"},
		"plane reports drift":   {`{"verified":true,"latest_revision":3,"drift":[{"severity":"high"}]}`, "1 drift finding(s)"},
		"not verified":          {`{"verified":false,"drift":[]}`, "NOT VERIFIED"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPullPlane(t, "tenant-a", "sandbox", 3, "{}", nil)
			p.reply = tc.reply
			out := filepath.Join(t.TempDir(), "sandbox.json")
			_, _, err := execRoot(t, pullArgs(p, "sandbox", out)...)
			if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v (exit %d), want exit 7 containing %q", err, exitcode.From(err), tc.want)
			}
			if got, _ := os.ReadFile(out); string(got) != "{}" {
				t.Errorf("the verified artifact must still be on disk, got %q", got)
			}
		})
	}
	// The control: latest == written and no drift is a clean pull (the first test covers it
	// with the default reply); here a newer-than-latest reply must not trip the stale check.
	p := newPullPlane(t, "tenant-a", "sandbox", 3, "{}", nil)
	p.reply = `{"verified":true,"latest_revision":3,"drift":[]}`
	if _, _, err := execRoot(t, pullArgs(p, "sandbox", filepath.Join(t.TempDir(), "f"))...); err != nil {
		t.Fatalf("latest revision pulled: %v", err)
	}
}

// If the check-in cannot be made the file is already replaced; the error says so rather than
// reporting a pull that "failed" while the host has the new policy.
func TestClaudePolicyPullSaysWhenTheFileWasWrittenButTheCheckinFailed(t *testing.T) {
	p := newPullPlane(t, "tenant-a", "sandbox", 3, "{}", nil)
	p.status, p.reply = http.StatusInternalServerError, `{"error":{"message":"boom"}}`
	out := filepath.Join(t.TempDir(), "sandbox.json")
	_, _, err := execRoot(t, pullArgs(p, "sandbox", out)...)
	if err == nil || !strings.Contains(err.Error(), "revision 3 was written to") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "{}" {
		t.Errorf("file = %q", got)
	}
}

// A fresh host has no /etc/opencode yet: the first pull creates the directory and writes.
func TestClaudePolicyPullCreatesTheDirectoryOfAFreshHost(t *testing.T) {
	const doc = "{\"share\":\"disabled\"}"
	p := newPullPlane(t, "tenant-a", "opencode-config", 1, doc, nil)
	out := filepath.Join(t.TempDir(), "etc", "opencode", "opencode.json")
	if stdout, _, err := execRoot(t, pullArgs(p, "opencode-config", out)...); err != nil {
		t.Fatalf("pull into a missing directory: %v\n%s", err, stdout)
	}
	if got, _ := os.ReadFile(out); string(got) != doc {
		t.Fatalf("file on disk = %q, want the published document", got)
	}
}

// An empty --out is a usage error before any request, not a failed rename after verifying.
func TestClaudePolicyPullRefusesAnEmptyOut(t *testing.T) {
	p := newPullPlane(t, "tenant-a", "sandbox", 1, "{}", nil)
	_, _, err := execRoot(t, pullArgs(p, "sandbox", "")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("empty --out: want exit %d, got %v", exitcode.Usage, err)
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("%d request(s) were sent with no file to write", n)
	}
}

// The write replaces what is there, atomically, and leaves nothing behind.
func TestWriteFileAtomicReplacesTheFileAndLeavesNoTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("file = %q, want the new bytes", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the policy file", len(entries))
	}
	// A path that cannot be replaced keeps the old file and leaves no temporary.
	if err := writeFileAtomic(dir, []byte("x")); err == nil {
		t.Error("replacing a directory must fail")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a failed write left %d entries, want only the policy file", len(entries))
	}
}

// An artifact the plane built badly is refused, never trusted: no revision, no key.
func TestVerifyPolicyArtifactRefusesAMalformedArtifact(t *testing.T) {
	good := newPullPlane(t, "tenant-a", "sandbox", 1, "{}", nil).artifact
	var a policyArtifact
	raw, _ := json.Marshal(good)
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	pin := a.KeyFingerprint
	if err := verifyPolicyArtifact(a, "sandbox", 0, "tenant-a", pin); err != nil {
		t.Fatalf("the good artifact: %v", err)
	}
	for name, mutate := range map[string]func(*policyArtifact){
		"no revision":       func(x *policyArtifact) { x.Revision = 0 },
		"short public key":  func(x *policyArtifact) { x.PublicKey = base64.StdEncoding.EncodeToString([]byte("short")) },
		"not a public key":  func(x *policyArtifact) { x.PublicKey = "%%%" },
		"not a signature":   func(x *policyArtifact) { x.Signature = "%%%" },
		"another algorithm": func(x *policyArtifact) { x.Algorithm = "rsa" },
	} {
		bad := a
		mutate(&bad)
		if err := verifyPolicyArtifact(bad, "sandbox", 0, "tenant-a", pin); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

// A plane or database that replays an older, validly signed revision as the newest passes every
// signature check. Only this host's own record of the highest revision it applied shows it is a
// rollback, so the pull refuses it before the write; going back on purpose needs --allow-rollback.
func TestClaudePolicyPullRefusesARevisionRecordThatHoldsNull(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sandbox.json")
	if err := os.WriteFile(policyHighWaterPath(out), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	plane := newPullPlane(t, "tenant-a", "sandbox", 5, `{"v":5}`, nil)
	_, _, err := execRoot(t, pullArgs(plane, "sandbox", out)...)
	if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "not a revision record") {
		t.Fatalf("err = %v (exit %d), want exit %d naming a damaged revision record", err, exitcode.From(err), exitcode.Degraded)
	}
	if _, serr := os.Stat(out); !os.IsNotExist(serr) {
		t.Fatalf("the host file was written over a damaged record: stat err = %v", serr)
	}
}

func TestClaudePolicyPullRefusesARevisionBelowWhatTheHostAlreadyApplied(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sandbox.json")
	newest := newPullPlane(t, "tenant-a", "sandbox", 5, `{"v":5}`, nil)
	if _, _, err := execRoot(t, pullArgs(newest, "sandbox", out)...); err != nil {
		t.Fatalf("revision 5: %v", err)
	}
	replay := newPullPlane(t, "tenant-a", "sandbox", 3, `{"v":3}`, nil)
	refused := func(args []string, wantFile string) {
		t.Helper()
		_, _, err := execRoot(t, args...)
		if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "already applied revision 5") {
			t.Fatalf("err = %v (exit %d), want exit %d naming the applied revision 5", err, exitcode.From(err), exitcode.Degraded)
		}
		if got, _ := os.ReadFile(out); string(got) != wantFile {
			t.Fatalf("the host file was changed to %q by a rollback, want %q", got, wantFile)
		}
	}
	refused(pullArgs(replay, "sandbox", out), `{"v":5}`)
	refused(pullArgs(replay, "sandbox", out, "--revision", "3"), `{"v":5}`) // asking for it is not enough
	replay.mu.Lock()
	if replay.checkin != nil {
		t.Errorf("an attestation %v was sent for a refused rollback", replay.checkin)
	}
	replay.mu.Unlock()

	if _, _, err := execRoot(t, pullArgs(replay, "sandbox", out, "--allow-rollback")...); err == nil || exitcode.From(err) != exitcode.Usage {
		t.Errorf("--allow-rollback without --revision: err = %v, want a usage error", err)
	}
	if _, _, err := execRoot(t, pullArgs(replay, "sandbox", out, "--revision", "3", "--allow-rollback")...); err != nil {
		t.Fatalf("an explicit rollback: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != `{"v":3}` {
		t.Fatalf("file = %q, want revision 3 after the explicit rollback", got)
	}
	// The record is the highest revision ever applied: a rollback does not lower it.
	refused(pullArgs(replay, "sandbox", out), `{"v":3}`)
	// The same revision again is not a rollback.
	if _, _, err := execRoot(t, pullArgs(newest, "sandbox", out)...); err != nil {
		t.Fatalf("revision 5 again: %v", err)
	}
}

// The short fingerprint is 64 bits of the key's SHA-256. The full digest is pinnable, the short
// form keeps working, and a pin that has the right start and the wrong rest is not the key.
func TestClaudePolicyPullAcceptsAFullLengthPin(t *testing.T) {
	p := newPullPlane(t, "tenant-a", "sandbox", 1, "{}", nil)
	sum := sha256.Sum256(p.pub)
	full := hex.EncodeToString(sum[:])
	pull := func(pin string) error {
		_, _, err := execRoot(t, lot3Args(p.URL, "claude-policy", "pull", "sandbox",
			"--out", filepath.Join(t.TempDir(), "sandbox.json"), "--scope", "host-7", "--pin-key-fingerprint", pin)...)
		return err
	}
	if err := pull(full); err != nil {
		t.Fatalf("the full 64-hex pin: %v", err)
	}
	if err := pull(full[:16]); err != nil {
		t.Fatalf("the short pin must keep working: %v", err)
	}
	err := pull(full[:16] + strings.Repeat("0", 48))
	if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "does not match the pinned key fingerprint") {
		t.Fatalf("a pin with a wrong tail: err = %v (exit %d)", err, exitcode.From(err))
	}
}

// A document with a credential in an MCP headers, env or http_headers entry would be written
// world-readable (0644) on the workstation: the pull refuses what the plane's
// redactInlineCredentials redacts (its token shapes and secret names), before anything reaches
// the disk.
func TestClaudePolicyPullRefusesADocumentThatCarriesACredential(t *testing.T) {
	fill := strings.Repeat("a1B2", 9)                       // fakes are built at run time, never literals
	key, literal := "sk-"+"ant-"+fill, "literal"+"value123" // a provider key; a value only its name marks
	for name, doc := range map[string]string{
		"json header":          `{"mcpServers":{"x":{"headers":{"Authorization":"Bearer ` + key + `"}}}}`,
		"json env":             `{"mcpServers":{"x":{"env":{"TOKEN":"` + key + `"}}}}`,
		"toml header":          "[mcp_servers.x.http_headers]\nAuthorization = \"Bearer " + key + "\"\n",
		"json github token":    `{"mcpServers":{"x":{"env":{"GITHUB_TOKEN":"` + "gh" + "p_" + fill + `"}}}}`,
		"json slack token":     `{"any":"` + "xox" + "b-" + fill + `"}`,
		"json google key":      `{"any":"` + "AI" + "za" + fill + `"}`,
		"json webhook secret":  `{"any":"` + "whs" + "ec_" + fill + `"}`,
		"json jwt":             `{"any":"` + "ey" + "J" + fill + "." + fill + "." + fill + `"}`,
		"json literal header":  `{"mcpServers":{"x":{"headers":{"Authorization":"Bearer ` + literal + `"}}}}`,
		"json token member":    `{"token":"` + literal + `"}`,
		"json apiKey member":   `{"provider":{"x":{"options":{"apiKey":"` + literal + `"}}}}`,
		"json credential":      `{"credential":"` + literal + `"}`,
		"toml access_key":      "[mcp_servers.x.env]\nAWS_ACCESS_KEY = \"" + literal + "\"\n",
		"toml api_key in list": "[[servers]]\napi_key = \"" + literal + "\"\n",
		// managed-instructions is Markdown: lines that are all headings are valid TOML comments,
		// and a parsed document must not hide what its raw text carries.
		"markdown headings only":  "# deployment\n# GITHUB_TOKEN=" + "gh" + "p_" + fill + "\n",
		"markdown provider key":   "# keys\n# " + key + "\n",
		"toml comment with a key": "model = \"gpt-5\"\n# " + key + "\n",
		"toml comment with a url": "model = \"gpt-5\"\n# postgres://app:" + literal + "@db/app\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := newPullPlane(t, "tenant-a", "sandbox", 1, doc, nil)
			out := filepath.Join(t.TempDir(), "sandbox.json")
			if err := os.WriteFile(out, []byte("previous"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := execRoot(t, pullArgs(p, "sandbox", out)...)
			if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "inline credential") {
				t.Fatalf("err = %v (exit %d), want exit %d naming the inline credential", err, exitcode.From(err), exitcode.Degraded)
			}
			if strings.Contains(err.Error(), fill) || strings.Contains(err.Error(), literal) {
				t.Error("the refusal printed the credential")
			}
			if got, _ := os.ReadFile(out); string(got) != "previous" {
				t.Errorf("the host file was changed to %q", got)
			}
			// Nothing else reached the disk either: no revision record, no temporary.
			if left, _ := os.ReadDir(filepath.Dir(out)); len(left) != 1 {
				t.Errorf("the refused pull left %v on the host", left)
			}
		})
	}
	// The controls: a value that points at a secret instead of carrying it is applied.
	for _, doc := range []string{
		`{"mcpServers":{"x":{"headers":{"Authorization":"Bearer ${MCP_TOKEN}"}}}}`,
		`{"mcpServers":{"x":{"env":{"GITHUB_TOKEN":"${GITHUB_TOKEN}"}}}}`,
		`{"provider":{"x":{"options":{"apiKey":"{env:OPENAI_API_KEY}"}}}}`,
		`{"apiKeyHelper":"get-key.sh","env":{"MAX_TOKENS":"4096"}}`,
		"[mcp_servers.x]\nbearer_token_env_var = \"MCP_TOKEN\"\n",
		"[mcp_servers.x]\nenv_http_headers = { \"X-Api-Token\" = \"MCP_TOKEN\" }\n",
	} {
		p := newPullPlane(t, "tenant-a", "sandbox", 1, doc, nil)
		if _, _, err := execRoot(t, pullArgs(p, "sandbox", filepath.Join(t.TempDir(), "f"))...); err != nil {
			t.Errorf("%s is not a credential: %v", doc, err)
		}
	}
}

// When the disk does not hold what was written, the plane is told what the disk holds (so it
// records the drift) instead of learning nothing; the pull still exits 7.
func TestClaudePolicyPullTellsThePlaneWhenTheDiskChangedTheFile(t *testing.T) {
	p := newPullPlane(t, "tenant-a", "sandbox", 3, "{}", nil)
	altered := []byte(`{"changed":"by the disk"}`)
	prev := readPulledFile
	readPulledFile = func(string) ([]byte, error) { return altered, nil }
	t.Cleanup(func() { readPulledFile = prev })

	_, _, err := execRoot(t, pullArgs(p, "sandbox", filepath.Join(t.TempDir(), "sandbox.json"))...)
	if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "NOT ATTESTED") {
		t.Fatalf("err = %v (exit %d), want exit %d NOT ATTESTED", err, exitcode.From(err), exitcode.Degraded)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkin == nil {
		t.Fatal("the plane was not told that the disk holds other bytes")
	}
	sum := sha256.Sum256(altered)
	if p.checkin["artifact_sha256"] != hex.EncodeToString(sum[:]) || p.checkin["observed_content"] != string(altered) {
		t.Errorf("check-in = %v, want the hash and the bytes the disk holds", p.checkin)
	}
}

// Two pulls must not interleave read, check and write: a stale revision admitted from a stale read
// would replace the policy of a newer one that updated the record meanwhile. While another process
// holds the lock the pull waits, and then it sees the record that process left.
func TestClaudePolicyPullWaitsForTheLockAndThenSeesTheRecord(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sandbox.json")
	stale := newPullPlane(t, "tenant-a", "sandbox", 4, `{"v":4}`, nil)
	unlock, err := lockPolicyOut(out)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := execRoot(t, pullArgs(stale, "sandbox", out)...)
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("the pull ran while another pull held the lock: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	// What the other pull leaves behind before it lets go: revision 6 applied.
	if err := os.WriteFile(policyHighWaterPath(out), []byte(`{"tenant-a|sandbox":6}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case err := <-done:
		if err == nil || exitcode.From(err) != exitcode.Degraded || !strings.Contains(err.Error(), "already applied revision 6") {
			t.Fatalf("err = %v (exit %d), want exit %d naming the applied revision 6", err, exitcode.From(err), exitcode.Degraded)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pull did not run after the lock was released")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the stale revision replaced the policy")
	}
}

// A variable in the password slot of a URL is expanded by the tool that reads the document, so it
// carries no secret: a policy written that way is applied, and a written-out password is not.
func TestClaudePolicyPullAppliesAURLWhosePasswordIsAVariable(t *testing.T) {
	for doc, refused := range map[string]bool{
		`{"mcpServers":{"db":{"env":{"DATABASE_URL":"postgres://app:${DB_PASS}@db/app"}}}}`:          false,
		`{"mcpServers":{"db":{"env":{"DATABASE_URL":"postgres://app:$DB_PASS@db/app"}}}}`:            false,
		"[mcp_servers.db.env]\nDATABASE_URL = \"postgres://app:${DB_PASS}@db/app\"\n":                false,
		`{"mcpServers":{"db":{"env":{"DATABASE_URL":"postgres://app:` + "pass" + `word1@db/app"}}}}`: true,
		`{"mcpServers":{"db":{"env":{"DATABASE_URL":"postgres://app:${DB_PASS}x@db/app"}}}}`:         true,
	} {
		p := newPullPlane(t, "tenant-a", "sandbox", 1, doc, nil)
		_, _, err := execRoot(t, pullArgs(p, "sandbox", filepath.Join(t.TempDir(), "f"))...)
		if refused != (err != nil && exitcode.From(err) == exitcode.Degraded && strings.Contains(err.Error(), "inline credential")) {
			t.Errorf("%s: err = %v, want refused=%v", doc, err, refused)
		}
	}
}
