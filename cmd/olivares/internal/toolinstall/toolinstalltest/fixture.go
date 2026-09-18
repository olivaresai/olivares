// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package toolinstalltest provides the disposable fixtures the toolinstall tests
// and the CLI tests share: a throwaway OpenPGP signing key made with the real
// gpg, an HTTP server with the vendor's release layout and request counters,
// and harmless executables that report a version. Nothing here is linked into
// the product binary; only tests import it.
package toolinstalltest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// SigningKey is a fixture-only OpenPGP key. Its private half lives in Home,
// which is removed when the test ends.
type SigningKey struct {
	Home        string
	Fingerprint string
	Public      []byte
}

// RequireGPG fails the test when gpg is missing. It does not skip: the
// acceptance of this installer is a REAL signature verification, and a silent
// skip would report that acceptance as met on a machine that never measured it.
func RequireGPG(t testing.TB) string {
	t.Helper()
	path, err := exec.LookPath("gpg")
	if err != nil {
		t.Fatalf("gpg is required to exercise the real signature verification and is not on PATH: %v", err)
	}
	return path
}

func gpg(t testing.TB, home string, stdin []byte, args ...string) []byte {
	t.Helper()
	base := []string{"--homedir", home, "--batch", "--no-tty", "--no-options", "--pinentry-mode", "loopback", "--passphrase", ""}
	// #nosec G204 -- test-only fixture: nothing outside *_test.go imports this package, so it is not linked into the product binary. The program is the literal "gpg" and exec.Command passes an argv ARRAY, never a shell; every variadic argument comes from this file's own four call sites — fixed gpg flags, the uid literal the test names, the fingerprint this file reads from gpg's own colon listing and length-checks (40) before use, and a t.TempDir() path.
	cmd := exec.Command("gpg", append(base, args...)...)
	cmd.Env = []string{"GNUPGHOME=" + home, "HOME=" + home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("gpg %v: %v\n%s", args, err, errb.String())
	}
	return out.Bytes()
}

// GenerateKey creates an ed25519 signing key in a short temporary GNUPGHOME
// (gpg-agent sockets need a short path) and exports its armored public half.
func GenerateKey(t testing.TB, uid string) *SigningKey {
	t.Helper()
	RequireGPG(t)
	home, err := os.MkdirTemp("", "ogk")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if gpgconf, err := exec.LookPath("gpgconf"); err == nil {
			// #nosec G204 -- test-only fixture: gpgconf is exec.LookPath's own resolution against the test process's PATH, its four arguments are three literals and the os.MkdirTemp home this function created, and there is no shell.
			_ = exec.Command(gpgconf, "--homedir", home, "--kill", "gpg-agent").Run()
		}
		_ = os.RemoveAll(home)
	})
	gpg(t, home, nil, "--quick-generate-key", uid, "ed25519", "sign", "never")
	listing := string(gpg(t, home, nil, "--with-colons", "--list-keys"))
	var fpr string
	afterPub := false
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Split(line, ":")
		switch f[0] {
		case "pub":
			afterPub = true
		case "fpr":
			if afterPub && len(f) > 9 {
				fpr = f[9]
				afterPub = false
			}
		}
	}
	if len(fpr) != 40 {
		t.Fatalf("no primary fingerprint in gpg listing:\n%s", listing)
	}
	pub := gpg(t, home, nil, "--armor", "--export", fpr)
	if !bytes.Contains(pub, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
		t.Fatalf("export did not produce an armored key:\n%s", pub)
	}
	return &SigningKey{Home: home, Fingerprint: fpr, Public: pub}
}

// Sign returns a binary detached signature over data.
func (k *SigningKey) Sign(t testing.TB, data []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "data")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sig := gpg(t, k.Home, nil, "--detach-sign", "--output", "-", in)
	if len(sig) == 0 {
		t.Fatal("empty detached signature")
	}
	return sig
}

// Server serves a Claude-shaped release layout under its URL:
// /<version>/manifest.json, /<version>/manifest.json.sig, /<version>/<platform>/claude,
// /latest and /stable. Every request is counted by path.
type Server struct {
	*httptest.Server
	mu     sync.Mutex
	files  map[string][]byte
	counts map[string]int
	// Chunked paths are sent without Content-Length so the streaming size check
	// is what catches a short or long body.
	chunked map[string]bool
	// Gate, when set for a path, blocks the response body until the channel is
	// closed. It models a slow artifact download for concurrency tests.
	gates map[string]chan struct{}
}

func NewServer(t testing.TB) *Server {
	t.Helper()
	s := &Server{files: map[string][]byte{}, counts: map[string]int{}, chunked: map[string]bool{}, gates: map[string]chan struct{}{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	body, ok := s.files[r.URL.Path]
	s.counts[r.URL.Path]++
	chunked := s.chunked[r.URL.Path]
	gate := s.gates[r.URL.Path]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if gate != nil {
		<-gate
	}
	if chunked {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// Set stores body at path (leading slash added when missing).
func (s *Server) Set(path string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[normalize(path)] = body
}

// Delete removes a path so it answers 404.
func (s *Server) Delete(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, normalize(path))
}

// Get returns the stored body.
func (s *Server) Get(path string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files[normalize(path)]
}

// Count returns how many requests path received.
func (s *Server) Count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[normalize(path)]
}

// SetChunked makes path stream without Content-Length.
func (s *Server) SetChunked(path string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunked[normalize(path)] = on
}

// Gate returns a channel; until it is closed, responses for path block after
// the request is counted.
func (s *Server) Gate(path string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{})
	s.gates[normalize(path)] = ch
	return ch
}

func normalize(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// Publish writes a signed manifest for version naming each artifact by vendor
// platform key, and serves the artifacts. It returns the manifest bytes.
func (s *Server) Publish(t testing.TB, key *SigningKey, version string, artifacts map[string][]byte) []byte {
	t.Helper()
	platforms := map[string]map[string]any{}
	for plat, b := range artifacts {
		sum := sha256.Sum256(b)
		platforms[plat] = map[string]any{"binary": "claude", "checksum": hex.EncodeToString(sum[:]), "size": len(b)}
		s.Set(fmt.Sprintf("/%s/%s/claude", version, plat), b)
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"version": version, "manifestSignatureEnforcement": "flag", "commit": "fixture", "buildDate": "2026-09-05T00:00:00Z",
		"platforms": platforms, "sdkCompat": map[string]any{"harnessSchema": 1},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	s.Set("/"+version+"/manifest.json", manifest)
	s.Set("/"+version+"/manifest.json.sig", key.Sign(t, manifest))
	return manifest
}

// SetPointer publishes /latest or /stable.
func (s *Server) SetPointer(name, version string) { s.Set("/"+name, []byte(version+"\n")) }

// ArtifactPath is the request path of one artifact.
func ArtifactPath(version, plat string) string { return fmt.Sprintf("/%s/%s/claude", version, plat) }

// Executable returns a harmless POSIX shell program that prints "<version>
// (Claude Code)" like the real tool and, when marker is non-empty, appends a
// line to that file each time it runs, so a test can prove it never ran.
func Executable(version, marker string) []byte {
	return VersionReporter("Claude Code", version, marker)
}

// VersionReporter returns a harmless POSIX shell program that prints
// "<version> (<name>)" like a provider CLI --version line.
func VersionReporter(name, version, marker string) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# olivares toolinstall fixture: harmless version reporter\n")
	if marker != "" {
		fmt.Fprintf(&b, "printf 'ran\\n' >> '%s'\n", marker)
	}
	fmt.Fprintf(&b, "printf '%%s (%s)\\n' '%s'\n", name, version)
	return []byte(b.String())
}

// StubbornExecutable ignores SIGTERM, records its own pid and a helper's pid
// into pidfile, and waits; only SIGKILL to the process group ends it.
func StubbornExecutable(pidfile string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\ntrap '' TERM\necho $$ > '%s'\nsleep 300 &\necho $! >> '%s'\nwait\n", pidfile, pidfile))
}

// ExecCapableDir returns a fresh directory whose files can be executed; TMPDIR
// may be mounted noexec on hardened hosts. It fails when none is found rather
// than skipping, because every install assertion needs to run the fixture.
func ExecCapableDir(t testing.TB) string {
	t.Helper()
	try := func(dir string) bool {
		p := filepath.Join(dir, "execprobe")
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			return false
		}
		defer func() { _ = os.Remove(p) }()
		// #nosec G204 -- test-only fixture: p is the "execprobe" file this closure just wrote with os.WriteFile, inside the t.TempDir() or $HOME-rooted os.MkdirTemp directory ExecCapableDir is probing. Running this owned constant-shebang probe with no arguments measures whether that filesystem is mounted noexec; the kernel invokes /bin/sh for its constant body.
		return exec.Command(p).Run() == nil
	}
	if d := t.TempDir(); try(d) {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if d, err := os.MkdirTemp(home, ".olivares-toolinstall-test-*"); err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(d) })
			if try(d) {
				return d
			}
		}
	}
	t.Fatalf("no exec-capable directory: t.TempDir() (TMPDIR=%q) and $HOME both refuse to execute a file; point TMPDIR at an exec-capable filesystem", os.Getenv("TMPDIR"))
	return ""
}

// NameFirstVersionReporter returns a harmless POSIX shell program that prints
// "<name> <version> (<build>)", the line shape the Grok Build and Codex CLIs
// print. It exists because a fixture that prints the version first hides a
// parser that reads the first field as the version: measured 2026-09-18
// against Grok Build 1.0.34 from the official origin.
func NameFirstVersionReporter(name, version, build string) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# olivares toolinstall fixture: harmless version reporter\n")
	fmt.Fprintf(&b, "printf '%s %%s (%s)\\n' '%s'\n", name, build, version)
	return []byte(b.String())
}
