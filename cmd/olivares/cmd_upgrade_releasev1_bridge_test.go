// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// THE WORKER-TO-CLI BRIDGE ACCEPTANCE (lot 1 addendum §6 group 7, completed on 2026-09-05).
//
// The REAL licence Worker — src/index.ts routed through the real D1Store over node:sqlite with the
// real migrations and a controlled R2 — is hosted on a loopback listener by
// commercial/license-worker/test/bridge/release-v1-worker-bridge.ts, and the REAL `olivares
// upgrade` command is driven against it over an owned executable target. Manifests are signed by a
// throwaway Ed25519 key generated here (the Worker never verifies them; the CLI does, against
// --pubkey), tokens are minted by the Worker's own minting code under fakeEnv's test secret, and the
// bridge's stdin control channel moves grants, licences, deployments and publications BETWEEN the
// CLI's requests — the only deterministic way to exercise the multi-request guarantees.
//
// WHAT A GREEN HERE MEANS, AND DOES NOT: the authenticated journey works end to end on this SHA
// through the actual router and the actual command. It is not Cloudflare, not D1-the-service, not
// R2, not a real signing ceremony, and it sends no email. Skips are reported as skips.

type bridgeToken struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type bridgeDeployment struct {
	ID       string `json:"id"`
	Serial   string `json:"serial"`
	IssueSeq int    `json:"issueSeq"`
}

type bridgeOtaToken struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	DeploymentID string `json:"deploymentId"`
	Serial       string `json:"serial"`
	IssueSeq     int    `json:"issueSeq"`
}

type bridgeSpec struct {
	Holder      string             `json:"holder"`
	Codes       []string           `json:"codes"`
	Objects     map[string]string  `json:"objects"`
	Tokens      []bridgeToken      `json:"tokens,omitempty"`
	Deployments []bridgeDeployment `json:"deployments,omitempty"`
	OtaTokens   []bridgeOtaToken   `json:"otaTokens,omitempty"`
}

type bridgeServed struct {
	Path   string  `json:"path"`
	Kind   *string `json:"kind"`
	Status int     `json:"status"`
}

type workerBridge struct {
	t      *testing.T
	dir    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	stderr *bytes.Buffer
	port   int
	tokens map[string]string
	files  int
}

func bridgeManifestKey(channel, set string) string {
	return "enterprise/" + channel + "/" + set + "/manifest.json"
}

func bridgeArtifactKey(version, set string) string {
	return fmt.Sprintf("enterprise/%s/%s/olivares_%s_linux_amd64.tar.gz", version, set, version)
}

// workerBridgeDir locates the Worker checkout relative to this package, or skips.
func workerBridgeDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "commercial", "license-worker"))
	if err != nil {
		t.Skip("cannot resolve the license-worker directory: " + err.Error())
	}
	if _, err := os.Stat(filepath.Join(dir, "test", "bridge", "release-v1-worker-bridge.ts")); err != nil {
		t.Skip("license-worker bridge script not present: " + err.Error())
	}
	return dir
}

// startWorkerBridge writes the fixture directory and launches the real Worker over loopback.
func startWorkerBridge(t *testing.T, spec bridgeSpec, files map[string][]byte) *workerBridge {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available for the Worker bridge: " + err.Error())
	}
	workerDir := workerBridgeDir(t)
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), specBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "test/bridge/release-v1-worker-bridge.ts", "--fixture", dir)
	cmd.Dir = workerDir
	cmd.Env = append(os.Environ(), "NODE_NO_WARNINGS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	b := &workerBridge{t: t, dir: dir, cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 1<<20), stderr: stderr}
	t.Cleanup(func() {
		_, _ = io.WriteString(stdin, `{"op":"exit"}`+"\n")
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() && stderr.Len() > 0 {
			t.Logf("bridge stderr:\n%s", stderr.String())
		}
	})
	var ready struct {
		Ready  bool              `json:"ready"`
		Port   int               `json:"port"`
		Tokens map[string]string `json:"tokens"`
	}
	line := b.readLine(60 * time.Second)
	if err := json.Unmarshal([]byte(line), &ready); err != nil || !ready.Ready || ready.Port == 0 {
		t.Fatalf("bridge did not become ready: %q (%v)\nstderr:\n%s", line, err, stderr.String())
	}
	b.port = ready.Port
	b.tokens = ready.Tokens
	return b
}

func (b *workerBridge) readLine(timeout time.Duration) string {
	b.t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := b.out.ReadString('\n')
		ch <- res{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			b.t.Fatalf("bridge stdout: %v\nstderr:\n%s", r.err, b.stderr.String())
		}
		return strings.TrimSpace(r.line)
	case <-time.After(timeout):
		b.t.Fatalf("bridge did not answer within %s\nstderr:\n%s", timeout, b.stderr.String())
		return ""
	}
}

func (b *workerBridge) endpoint() string { return fmt.Sprintf("http://127.0.0.1:%d", b.port) }

// op sends one control command and returns its reply; a failed reply fails the test.
func (b *workerBridge) op(m map[string]any) map[string]any {
	b.t.Helper()
	line, err := json.Marshal(m)
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.stdin.Write(append(line, '\n')); err != nil {
		b.t.Fatalf("bridge stdin: %v", err)
	}
	var reply map[string]any
	raw := b.readLine(30 * time.Second)
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		b.t.Fatalf("bridge reply %q: %v", raw, err)
	}
	if ok, _ := reply["ok"].(bool); !ok {
		b.t.Fatalf("bridge op %v failed: %s", m, raw)
	}
	return reply
}

// putOp returns the control command that publishes data under key (writing the file first).
func (b *workerBridge) putOp(key string, data []byte) map[string]any {
	b.t.Helper()
	b.files++
	name := fmt.Sprintf("object-%d.bin", b.files)
	if err := os.WriteFile(filepath.Join(b.dir, name), data, 0o600); err != nil {
		b.t.Fatal(err)
	}
	return map[string]any{"op": "put", "key": key, "file": name}
}

func (b *workerBridge) publish(key string, data []byte) { b.op(b.putOp(key, data)) }

// after schedules ops to run right after the next served release-v1 request of the given kind
// ("manifest", "manifest.sig", "" for the artifact).
func (b *workerBridge) after(kind string, ops ...map[string]any) {
	b.op(map[string]any{"op": "after", "kind": kind, "ops": ops})
}

func (b *workerBridge) mint(name, version string) string {
	reply := b.op(map[string]any{"op": "mint", "name": name, "version": version})
	tok, _ := reply["token"].(string)
	if tok == "" {
		b.t.Fatalf("mint %s: no token in %v", name, reply)
	}
	b.tokens[name] = tok
	return tok
}

func (b *workerBridge) requests() []bridgeServed {
	reply := b.op(map[string]any{"op": "requests"})
	raw, _ := json.Marshal(reply["requests"])
	var out []bridgeServed
	_ = json.Unmarshal(raw, &out)
	return out
}

func (b *workerBridge) reads() []string {
	reply := b.op(map[string]any{"op": "reads"})
	raw, _ := json.Marshal(reply["reads"])
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func (b *workerBridge) reset() { b.op(map[string]any{"op": "reset"}) }

func countServed(reqs []bridgeServed, kind string, status int) int {
	n := 0
	for _, r := range reqs {
		if r.Path == releaseV1Path && r.Kind != nil && *r.Kind == kind && r.Status == status {
			n++
		}
	}
	return n
}

func readsContain(reads []string, needle string) bool {
	for _, k := range reads {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// publishedRelease is one signed publication laid out the way the Worker's producer contract keys
// objects (contracts/publisher.gen.json): mutable channel/set metadata, version/set artifacts.
type publishedRelease struct {
	rel     *v1Release
	channel string
	set     string
}

func (p publishedRelease) files(prefix string) (map[string]string, map[string][]byte) {
	objects := map[string]string{
		bridgeManifestKey(p.channel, p.set):          prefix + "-manifest.json",
		bridgeManifestKey(p.channel, p.set) + ".sig": prefix + "-manifest.json.sig",
		bridgeArtifactKey(p.rel.version, p.set):      prefix + "-artifact.tar.gz",
	}
	files := map[string][]byte{
		prefix + "-manifest.json":     p.rel.manifest,
		prefix + "-manifest.json.sig": p.rel.sig,
		prefix + "-artifact.tar.gz":   p.rel.artifact,
	}
	return objects, files
}

func (b *workerBridge) publishRelease(p publishedRelease) {
	b.publish(bridgeManifestKey(p.channel, p.set), p.rel.manifest)
	b.publish(bridgeManifestKey(p.channel, p.set)+".sig", p.rel.sig)
	b.publish(bridgeArtifactKey(p.rel.version, p.set), p.rel.artifact)
}

func TestUpgradeThroughTheRealWorkerBridge(t *testing.T) {
	workerBridgeDir(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available for the Worker bridge")
	}
	old, desired := requireStubs(t)
	next := buildStub(t, "26.9.0")
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	art8 := tarGzBinary(t, desired)
	rel8 := newV1Release(t, priv, release.ChannelStable, "26.8.0", "", art8)
	rel9 := newV1Release(t, priv, release.ChannelStable, "26.9.0", "", tarGzBinary(t, next))
	const holder = "sub_bridge"

	run := func(t *testing.T, b *workerBridge, token, target, dataDir string, extra ...string) (string, error) {
		t.Helper()
		installDevLicense(t, dataDir)
		args := []string{
			"--enterprise", "--token", token, "--endpoint", b.endpoint(), "--pubkey", pubB64,
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir,
		}
		return runUpgradeCmd(t, append(args, extra...)...)
	}
	baseSpec := func(codes []string, published ...publishedRelease) (bridgeSpec, map[string][]byte) {
		objects := map[string]string{}
		files := map[string][]byte{}
		for i, p := range published {
			o, f := p.files(fmt.Sprintf("pub%d", i))
			for k, v := range o {
				objects[k] = v
			}
			for k, v := range f {
				files[k] = v
			}
		}
		return bridgeSpec{Holder: holder, Codes: codes, Objects: objects, Tokens: []bridgeToken{{Name: "link8", Version: "26.8.0"}}}, files
	}

	t.Run("the complete same-version authenticated journey installs the entitled set's bytes", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz", "reg"}, publishedRelease{rel8, "stable", "biz+reg"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		if _, err := run(t, b, b.tokens["link8"], target, t.TempDir()); err != nil {
			t.Fatalf("journey: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		reqs := b.requests()
		if countServed(reqs, "manifest", 200) != 1 || countServed(reqs, "manifest.sig", 200) != 1 || countServed(reqs, "", 200) != 1 {
			t.Fatalf("served requests: %+v", reqs)
		}
		reads := b.reads()
		if !readsContain(reads, bridgeArtifactKey("26.8.0", "biz+reg")) || readsContain(reads, "/biz/") {
			t.Fatalf("the Worker read %v; want the biz+reg artifact and no biz object", reads)
		}
	})

	t.Run("the OTA class through Authorization: a current deployment installs, a superseded lineage is refused before any read", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz"}, publishedRelease{rel8, "stable", "biz"})
		spec.Deployments = []bridgeDeployment{{ID: "dep_owned", Serial: "serial_owned", IssueSeq: 2}}
		spec.OtaTokens = []bridgeOtaToken{
			{Name: "ota8", Version: "26.8.0", DeploymentID: "dep_owned", Serial: "serial_owned", IssueSeq: 2},
			{Name: "ota_stale", Version: "26.8.0", DeploymentID: "dep_owned", Serial: "serial_owned", IssueSeq: 1},
		}
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		_, err := run(t, b, b.tokens["ota_stale"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "ota_lineage_refused") {
			t.Fatalf("superseded lineage: want ota_lineage_refused, got %v", err)
		}
		if len(b.reads()) != 0 {
			t.Fatalf("a refused lineage read objects: %v", b.reads())
		}
		assertTargetRuns(t, target, "26.7.0")
		if _, err := run(t, b, b.tokens["ota8"], target, t.TempDir()); err != nil {
			t.Fatalf("current lineage: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
	})

	t.Run("a signature that does not verify, or cannot be decoded, leaves the target intact", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz"}, publishedRelease{rel8, "stable", "biz"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		// Shape-valid, digest-consistent, cryptographically WRONG: only Ed25519 can catch it.
		wrong := []byte(base64.StdEncoding.EncodeToString(release.SignManifest([]byte("another manifest"), priv)))
		b.publish(bridgeManifestKey("stable", "biz")+".sig", wrong)
		_, err := run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "signature") {
			t.Fatalf("want a signature refusal, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if countServed(b.requests(), "", 200) != 0 {
			t.Fatal("artifact bytes were fetched for a manifest whose signature does not verify")
		}
		// A truncated signature object is not a publication: the Worker refuses it by name.
		b.publish(bridgeManifestKey("stable", "biz")+".sig", []byte("a"))
		_, err = run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "release_metadata_invalid") {
			t.Fatalf("want release_metadata_invalid, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
	})

	t.Run("a channel advance after mint is a named conflict; a properly reissued token installs the next version", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz", "reg"}, publishedRelease{rel8, "stable", "biz+reg"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		b.publishRelease(publishedRelease{rel9, "stable", "biz+reg"})
		_, err := run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), v1ErrTokenVersionStale) {
			t.Fatalf("want %s, got %v", v1ErrTokenVersionStale, err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if readsContain(b.reads(), "olivares_26.9.0") || readsContain(b.reads(), "olivares_26.8.0") {
			t.Fatalf("an artifact was read for a stale token: %v", b.reads())
		}
		// The Worker's own minting code issues a 26.9.0 token — the authenticated portal reissue —
		// and THAT bearer obtains the next version. The old one never did.
		tok9 := b.mint("link9", "26.9.0")
		if _, err := run(t, b, tok9, target, t.TempDir()); err != nil {
			t.Fatalf("reissued token: %v", err)
		}
		assertTargetRuns(t, target, "26.9.0")
	})

	t.Run("a grant change after resolution is a set conflict; one fresh resolution installs the same release for the new set", func(t *testing.T) {
		// biz+reg AND biz publish 26.8.0 with the SAME artifact bytes, so the re-resolved descriptor
		// is identical and the run may complete under the new set's own object.
		spec, files := baseSpec([]string{"biz", "reg"}, publishedRelease{rel8, "stable", "biz+reg"}, publishedRelease{rel8, "stable", "biz"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		b.after("manifest.sig", map[string]any{"op": "grants", "codes": []string{"biz"}})
		if _, err := run(t, b, b.tokens["link8"], target, t.TempDir()); err != nil {
			t.Fatalf("expected one fresh resolution to recover: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		reqs := b.requests()
		if countServed(reqs, "", 409) != 1 || countServed(reqs, "manifest", 200) != 2 || countServed(reqs, "manifest.sig", 200) != 2 || countServed(reqs, "", 200) != 1 {
			t.Fatalf("served: %+v (want one 409 artifact, then a full second resolution and one artifact)", reqs)
		}
		reads := b.reads()
		if readsContain(reads, bridgeArtifactKey("26.8.0", "biz+reg")) || !readsContain(reads, bridgeArtifactKey("26.8.0", "biz")) {
			t.Fatalf("artifact reads %v: the biz+reg object must not be served after the set changed", reads)
		}
	})

	t.Run("a grant change to a set with no publication ends in unavailability, target intact", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz", "reg"}, publishedRelease{rel8, "stable", "biz+reg"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		b.after("manifest.sig", map[string]any{"op": "grants", "codes": []string{"biz"}})
		_, err := run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "release_unavailable") {
			t.Fatalf("want release_unavailable after the fresh resolution, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if readsContain(b.reads(), "olivares_") {
			t.Fatalf("an artifact object was read: %v", b.reads())
		}
	})

	t.Run("revocation between the signature and the artifact denies before the artifact read", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz"}, publishedRelease{rel8, "stable", "biz"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		b.after("manifest.sig", map[string]any{"op": "revoke"})
		_, err := run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "license_not_live") {
			t.Fatalf("want license_not_live, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		if readsContain(b.reads(), "olivares_") {
			t.Fatalf("an artifact object was read after revocation: %v", b.reads())
		}
		if countServed(b.requests(), "manifest", 200) != 1 {
			t.Fatalf("a revocation must not be retried as a same-version conflict: %+v", b.requests())
		}
	})

	t.Run("a same-version metadata change after the signature is re-resolved once and installed", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz"}, publishedRelease{rel8, "stable", "biz"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		changed := newV1Release(t, priv, release.ChannelStable, "26.8.0", "notes changed, same binary", art8)
		b.after("manifest.sig",
			b.putOp(bridgeManifestKey("stable", "biz"), changed.manifest),
			b.putOp(bridgeManifestKey("stable", "biz")+".sig", changed.sig),
		)
		if _, err := run(t, b, b.tokens["link8"], target, t.TempDir()); err != nil {
			t.Fatalf("expected one fresh resolution to recover: %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		reqs := b.requests()
		if countServed(reqs, "", 409) != 1 || countServed(reqs, "manifest", 200) != 2 || countServed(reqs, "", 200) != 1 {
			t.Fatalf("served: %+v", reqs)
		}
	})

	t.Run("a publication that keeps changing terminates after the single fresh resolution, target intact", func(t *testing.T) {
		spec, files := baseSpec([]string{"biz"}, publishedRelease{rel8, "stable", "biz"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		second := newV1Release(t, priv, release.ChannelStable, "26.8.0", "second", art8)
		third := newV1Release(t, priv, release.ChannelStable, "26.8.0", "third", art8)
		b.after("manifest.sig", b.putOp(bridgeManifestKey("stable", "biz"), second.manifest), b.putOp(bridgeManifestKey("stable", "biz")+".sig", second.sig))
		b.after("manifest.sig", b.putOp(bridgeManifestKey("stable", "biz"), third.manifest), b.putOp(bridgeManifestKey("stable", "biz")+".sig", third.sig))
		_, err := run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "changed AGAIN") {
			t.Fatalf("want termination after one fresh resolution, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		reqs := b.requests()
		if countServed(reqs, "manifest", 200) != 2 || countServed(reqs, "", 409) != 2 || countServed(reqs, "", 200) != 0 {
			t.Fatalf("served: %+v (want exactly two resolutions, two artifact conflicts, no bytes)", reqs)
		}
	})

	t.Run("explicit rollback through a still-valid channel is audited, and refused without --force-rollback", func(t *testing.T) {
		rel7 := newV1Release(t, priv, release.ChannelSecurity, "26.7.0", "", tarGzBinary(t, old))
		spec, files := baseSpec([]string{"biz"}, publishedRelease{rel8, "stable", "biz"}, publishedRelease{rel7, "security", "biz"})
		spec.Tokens = append(spec.Tokens, bridgeToken{Name: "link7", Version: "26.7.0"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, desired) // running 26.8.0
		dataDir := t.TempDir()
		_, err := run(t, b, b.tokens["link7"], target, dataDir, "--channel", "security")
		if err == nil || !strings.Contains(err.Error(), "REFUSING to downgrade") {
			t.Fatalf("want the anti-rollback refusal, got %v", err)
		}
		assertTargetRuns(t, target, "26.8.0")
		if _, err := run(t, b, b.tokens["link7"], target, dataDir, "--channel", "security", "--force-rollback"); err != nil {
			t.Fatalf("forced rollback: %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		audit, err := os.ReadFile(filepath.Join(dataDir, "upgrade-audit.log"))
		if err != nil || !strings.Contains(string(audit), "force-rollback") || !strings.Contains(string(audit), "from=26.8.0") || !strings.Contains(string(audit), "to=26.7.0") {
			t.Fatalf("audit record missing or incomplete: %v\n%s", err, audit)
		}
	})

	t.Run("a binary that fails its post-swap probe is restored", func(t *testing.T) {
		sentinel := filepath.Join(t.TempDir(), "ran-once")
		flaky := newV1Release(t, priv, release.ChannelStable, "26.8.0", "", tarGzBinary(t, buildFlakyStub(t, sentinel)))
		spec, files := baseSpec([]string{"biz"}, publishedRelease{flaky, "stable", "biz"})
		b := startWorkerBridge(t, spec, files)
		target := writeTarget(t, old)
		_, err := run(t, b, b.tokens["link8"], target, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "rolled back") {
			t.Fatalf("want the automatic rollback, got %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
	})

	t.Run("the existing verified bundle path still performs an explicit, audited rollback", func(t *testing.T) {
		rel7 := newV1Release(t, priv, release.ChannelStable, "26.7.0", "", tarGzBinary(t, old))
		bundle := t.TempDir()
		for name, data := range map[string][]byte{
			"manifest.json": rel7.manifest, "manifest.json.sig": rel7.sig, "olivares_26.7.0_linux_amd64.tar.gz": rel7.artifact,
		} {
			if err := os.WriteFile(filepath.Join(bundle, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		target := writeTarget(t, desired)
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		if _, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", pubB64, "--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir); err == nil || !strings.Contains(err.Error(), "REFUSING to downgrade") {
			t.Fatalf("bundle without --force-rollback: want refusal, got %v", err)
		}
		if _, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", pubB64, "--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir, "--force-rollback"); err != nil {
			t.Fatalf("bundle rollback: %v", err)
		}
		assertTargetRuns(t, target, "26.7.0")
		audit, err := os.ReadFile(filepath.Join(dataDir, "upgrade-audit.log"))
		if err != nil || !strings.Contains(string(audit), "force-rollback") {
			t.Fatalf("bundle rollback audit missing: %v", err)
		}
	})
}
