// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockOffsite is an in-memory S3-compatible store for CLI offsite tests. It requires
// a SigV4 Authorization header on every request, so the whole signed-request path is
// exercised, and speaks path-style addressing (/{bucket}/{key}).
type mockOffsite struct {
	mu      sync.Mutex
	objects map[string][]byte
	bucket  string
	t       *testing.T
}

func (m *mockOffsite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=") {
		m.t.Errorf("request without SigV4 auth: %s %s", r.Method, r.URL)
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"+m.bucket), "/")
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			m.serveList(w, r)
			return
		}
		m.mu.Lock()
		b, ok := m.objects[key]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		_, _ = w.Write(b)
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.objects[key] = b
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		m.mu.Lock()
		delete(m.objects, key)
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (m *mockOffsite) serveList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	type entry struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	}
	var res struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		IsTruncated bool     `xml:"IsTruncated"`
		Contents    []entry  `xml:"Contents"`
	}
	m.mu.Lock()
	for k, v := range m.objects {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			res.Contents = append(res.Contents, entry{Key: k, Size: int64(len(v)), LastModified: "2026-07-09T10:00:00.000Z"})
		}
	}
	m.mu.Unlock()
	_ = xml.NewEncoder(w).Encode(&res)
}

func newMockOffsite(t *testing.T) (*httptest.Server, *mockOffsite, []string) {
	t.Helper()
	mock := &mockOffsite{objects: map[string][]byte{}, bucket: "dr-bucket", t: t}
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)
	akf := filepath.Join(t.TempDir(), "akid")
	skf := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(akf, []byte("AKIAEXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skf, []byte("secretexample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The offsite flag block, credentials by reference.
	flags := []string{
		"--offsite-endpoint", srv.URL,
		"--offsite-bucket", "dr-bucket",
		"--offsite-region", "auto",
		"--offsite-access-key-id-file", akf,
		"--offsite-secret-access-key-file", skf,
	}
	return srv, mock, flags
}

func sha(b []byte) string { s := sha256.Sum256(b); return fmt.Sprintf("%x", s) }

// TestDROffsitePushPullList exercises push → list --offsite → pull, round-tripping a
// real bundle through the signed client and the CLI.
func TestDROffsitePushPullList(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := filepath.Join(t.TempDir(), "pass")
	_ = os.WriteFile(pf, []byte("a strong DR passphrase"), 0o600)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	orig, _ := os.ReadFile(bundle)

	_, _, flags := newMockOffsite(t)

	if out, err := runDR(append([]string{"push", "--in", bundle}, flags...)...); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	out, err := runDR(append([]string{"list", "--offsite"}, flags...)...)
	if err != nil {
		t.Fatalf("list --offsite: %v\n%s", err, out)
	}
	if !strings.Contains(out, "estate.drbundle") {
		t.Fatalf("offsite list missing the pushed bundle:\n%s", out)
	}

	pulled := filepath.Join(t.TempDir(), "pulled.drbundle")
	if out, err := runDR(append([]string{"pull", "--name", "estate.drbundle", "--out", pulled}, flags...)...); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(pulled)
	if sha(got) != sha(orig) {
		t.Fatalf("pulled bundle differs from the pushed one (%d vs %d bytes)", len(got), len(orig))
	}
}

// TestDRBackupOffsiteReplication proves `dr backup --offsite-*` replicates the
// just-written bundle off-box in one shot.
func TestDRBackupOffsiteReplication(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := filepath.Join(t.TempDir(), "pass")
	_ = os.WriteFile(pf, []byte("a strong DR passphrase"), 0o600)
	bundle := filepath.Join(t.TempDir(), "olivares-dr-2026.drbundle")

	_, mock, flags := newMockOffsite(t)
	args := append([]string{"backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf}, flags...)
	out, err := runDR(args...)
	if err != nil {
		t.Fatalf("backup --offsite: %v\n%s", err, out)
	}
	if !strings.Contains(out, "replicated offsite") {
		t.Fatalf("expected offsite replication note:\n%s", out)
	}
	mock.mu.Lock()
	_, ok := mock.objects["olivares-dr-2026.drbundle"]
	mock.mu.Unlock()
	if !ok {
		t.Fatalf("bundle was not replicated to the offsite target; keys: %v", offsiteKeys(mock))
	}
}

func offsiteKeys(m *mockOffsite) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		out = append(out, k)
	}
	return out
}

// TestDRRestoreInPlace proves the staged, atomic, self-preserving in-place restore:
// it replaces a LIVE data dir, verifies continuity, and leaves the previous state as
// *.pre-restore-<ts>.
func TestDRRestoreInPlace(t *testing.T) {
	dir := t.TempDir()
	seedDataDir(t, dir)
	pf := filepath.Join(t.TempDir(), "pass")
	_ = os.WriteFile(pf, []byte("a strong DR passphrase"), 0o600)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	if out, err := runDR("backup", "--data-dir", dir, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	// Restore in place over the SAME live dir. Replacing a live estate now carries
	// a declared operator and reason; everything this test asserts about the
	// staged, atomic, self-preserving promotion is unchanged by that.
	out, err := runDR("restore", "--in", bundle, "--data-dir", dir, "--engine", "sqlite", "--passphrase-file", pf, "--in-place",
		"--operator", "ops@x.io", "--reason", "in-place restore drill")
	if err != nil {
		t.Fatalf("in-place restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "promoted in place") {
		t.Fatalf("in-place restore output unexpected:\n%s", out)
	}
	// The live store + keys are present, and a pre-restore copy was made.
	for _, f := range []string{"olivares.db", "audit-signing.key", "catalog-signing.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("live %s missing after in-place restore: %v", f, err)
		}
	}
	pre, _ := filepath.Glob(filepath.Join(dir, "olivares.db.pre-restore-*"))
	if len(pre) == 0 {
		t.Fatalf("in-place restore did not preserve the previous store as *.pre-restore-*")
	}
	// No leftover staging dir.
	if st, _ := filepath.Glob(filepath.Join(dir, ".dr-staging-*")); len(st) != 0 {
		t.Fatalf("staging dir leaked: %v", st)
	}
}

// TestDRRestoreInPlaceWrongPassphraseLeavesLiveIntact is the safety property: a
// failed in-place restore must not touch the live data dir.
func TestDRRestoreInPlaceWrongPassphraseLeavesLiveIntact(t *testing.T) {
	dir := t.TempDir()
	seedDataDir(t, dir)
	pf := filepath.Join(t.TempDir(), "pass")
	wrong := filepath.Join(t.TempDir(), "wrong")
	_ = os.WriteFile(pf, []byte("right one"), 0o600)
	_ = os.WriteFile(wrong, []byte("nope"), 0o600)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	if out, err := runDR("backup", "--data-dir", dir, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	before, _ := os.ReadFile(filepath.Join(dir, "olivares.db"))
	keyBefore, _ := os.ReadFile(filepath.Join(dir, "audit-signing.key"))

	// The declaration is supplied so this test still fails where it says it fails:
	// on the PASSPHRASE. Without it the command would now refuse earlier, for the
	// missing operator, and this test would go green having never decrypted
	// anything — a fixture passing for the wrong reason.
	if _, err := runDR("restore", "--in", bundle, "--data-dir", dir, "--engine", "sqlite", "--passphrase-file", wrong, "--in-place",
		"--operator", "ops@x.io", "--reason", "wrong-passphrase safety drill"); err == nil {
		t.Fatal("in-place restore with the wrong passphrase should fail")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "olivares.db"))
	keyAfter, _ := os.ReadFile(filepath.Join(dir, "audit-signing.key"))
	if sha(before) != sha(after) {
		t.Fatal("live store was modified by a FAILED in-place restore (safety violation)")
	}
	if sha(keyBefore) != sha(keyAfter) {
		t.Fatal("live signing key was modified by a FAILED in-place restore (safety violation)")
	}
	if pre, _ := filepath.Glob(filepath.Join(dir, "*.pre-restore-*")); len(pre) != 0 {
		t.Fatalf("a failed restore should not preserve/rename anything, found: %v", pre)
	}
}

// TestDRDrillCLI runs the full round-trip drill and asserts it passes with a
// measured RTO — the reproducible drill (docs/DR-RUNBOOK.md §8), run here.
func TestDRDrillCLI(t *testing.T) {
	out, err := runDR("drill", "--events", "40")
	if err != nil {
		t.Fatalf("drill: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DR drill PASSED") {
		t.Fatalf("drill did not pass:\n%s", out)
	}
	if !strings.Contains(out, "measured RTO") {
		t.Fatalf("drill did not report a measured RTO:\n%s", out)
	}
}

func TestDRDrillZeroEventReportDoesNotPass(t *testing.T) {
	var out strings.Builder
	err := reportDRDrillSuccess(&out, 0, time.Second)
	if err == nil || !strings.Contains(err.Error(), "nothing to verify") {
		t.Fatalf("zero-event drill error = %v, want nothing-to-verify failure", err)
	}
	if strings.Contains(out.String(), "DR drill PASSED") {
		t.Fatalf("zero-event drill printed success: %q", out.String())
	}
}
