// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

const grokFxVersion = "1.0.13"

type tlsMapServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	file map[string][]byte
}

func newTLSMap(t *testing.T) *tlsMapServer {
	t.Helper()
	s := &tlsMapServer{file: map[string][]byte{}}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		b, ok := s.file[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *tlsMapServer) put(path string, body []byte) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	s.mu.Lock()
	s.file[path] = body
	s.mu.Unlock()
}

func (s *tlsMapServer) client() *http.Client { return s.srv.Client() }
func (s *tlsMapServer) url() string          { return s.srv.URL }

func grokEngine(t *testing.T, s *tlsMapServer) (*Engine, *Grok, string) {
	t.Helper()
	dir := toolinstalltest.ExecCapableDir(t)
	g := NewGrok(GrokOptions{BaseURL: s.url(), Client: s.client(), ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	cat, err := NewCapabilityCatalog(NewCatalog(), g)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "tools")
	eng := NewEngineWithCapabilities(cat, EngineOptions{InstallerVersion: "test", Now: func() time.Time {
		return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	}})
	return eng, g, root
}

func TestGrokProbePresentAbsentOld(t *testing.T) {
	dir := toolinstalltest.ExecCapableDir(t)
	g := NewGrok(GrokOptions{ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	present := filepath.Join(dir, "grok")
	if err := os.WriteFile(present, toolinstalltest.VersionReporter("Grok", grokFxVersion, ""), 0o755); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(dir, "scratch")
	rep, err := g.Probe(context.Background(), present, scratch, grokFxVersion)
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if !strings.Contains(rep.Output, grokFxVersion) {
		t.Fatalf("output %q", rep.Output)
	}
	_, err = g.Probe(context.Background(), present, scratch, "9.9.9")
	if KindOf(err) != KindProbeMismatch {
		t.Fatalf("old version: %v", err)
	}
	_, err = g.Probe(context.Background(), filepath.Join(dir, "missing"), scratch, grokFxVersion)
	if KindOf(err) != KindProbeFailed {
		t.Fatalf("absent: %v", err)
	}
}

func TestGrokDetectEmptyAndPathAndAuth(t *testing.T) {
	s := newTLSMap(t)
	eng, _, root := grokEngine(t, s)
	home := t.TempDir()
	pathDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cands, err := eng.Detect(context.Background(), DetectOptions{Driver: DriverGrok, Root: root, Home: home, PathEnv: pathDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("empty host: %+v", cands)
	}
	exe := filepath.Join(pathDir, "grok")
	if err := os.WriteFile(exe, toolinstalltest.VersionReporter("Grok", grokFxVersion, ""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grok", "config.toml"), []byte("[sandbox]\nprofile = \"strict\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cands, err = eng.Detect(context.Background(), DetectOptions{Driver: DriverGrok, Root: root, Home: home, PathEnv: pathDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Match != MatchUnregisteredObserved {
		t.Fatalf("path candidate: %+v", cands)
	}
	if cands[0].Auth == nil || cands[0].Auth.State != AuthStatePresent {
		t.Fatalf("auth: %+v", cands[0].Auth)
	}
}

func TestGrokInstallProbeAndLatestRegistered(t *testing.T) {
	s := newTLSMap(t)
	body := toolinstalltest.VersionReporter("Grok", grokFxVersion, "")
	s.put("/latest", []byte(grokFxVersion+"\n"))
	s.put("/grok-"+grokFxVersion+"-linux-x86_64", body)
	eng, _, root := grokEngine(t, s)
	req := RequestV2{Driver: DriverGrok, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()}
	rec, plan, err := eng.InstallV2(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.VerificationKind != VerificationNoneOriginOnly {
		t.Fatalf("verification %s", rec.VerificationKind)
	}
	if rec.Version != grokFxVersion || plan.Selection.Verification.Kind != VerificationNoneOriginOnly {
		t.Fatalf("receipt %+v plan %+v", rec, plan.Selection)
	}
	sum := sha256.Sum256(body)
	if rec.FetchedObject.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("fetched digest %s", rec.FetchedObject.SHA256)
	}
	inv, err := eng.List(context.Background(), root)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != StateInstalled {
		t.Fatalf("list: %+v %v", inv, err)
	}
	got, ok, err := eng.LatestInstalled(context.Background(), root, DriverGrok)
	if err != nil || !ok || got.Executable != rec.Destination.Executable {
		t.Fatalf("latest: %+v %v %v", got, ok, err)
	}
	// no-op revalidate
	rec2, _, err := eng.InstallV2(context.Background(), req, nil, nil)
	if err != nil || rec2.Destination.Executable != rec.Destination.Executable {
		t.Fatalf("noop: %v %+v", err, rec2)
	}
}

func TestGrokUnknownVersion(t *testing.T) {
	s := newTLSMap(t)
	eng, _, root := grokEngine(t, s)
	_, err := eng.PlanV2(context.Background(), RequestV2{Driver: DriverGrok, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()})
	if KindOf(err) != KindVersionUnknown {
		t.Fatalf("missing latest: %v", err)
	}
}

// TestGrokProbeReadsTheVendorLineShape pins the line the official Grok Build
// CLI prints: the name comes first and the version second ("grok 1.0.34
// (5e9a58528b76)"). A probe that reads the first field as the version refuses
// every real release. Measured 2026-09-18: a live install of 1.0.34 from the
// official origin fetched 163035648 bytes and then refused with
// `probe_mismatch: ... reports "grok"; the plan says 1.0.34`.
func TestGrokProbeReadsTheVendorLineShape(t *testing.T) {
	dir := toolinstalltest.ExecCapableDir(t)
	g := NewGrok(GrokOptions{ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	exe := filepath.Join(dir, "grok")
	if err := os.WriteFile(exe, toolinstalltest.NameFirstVersionReporter("grok", "1.0.34", "5e9a58528b76"), 0o755); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(dir, "scratch")
	rep, err := g.Probe(context.Background(), exe, scratch, "1.0.34")
	if err != nil {
		t.Fatalf("vendor line shape: %v", err)
	}
	if !strings.Contains(rep.Output, "1.0.34") {
		t.Fatalf("output %q", rep.Output)
	}
	// The version still has to match: a different release is still a mismatch.
	if _, err := g.Probe(context.Background(), exe, scratch, "9.9.9"); KindOf(err) != KindProbeMismatch {
		t.Fatalf("other version: %v", err)
	}
	// A line that does not name Grok is still refused.
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, toolinstalltest.NameFirstVersionReporter("ripgrep", "1.0.34", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Probe(context.Background(), other, scratch, "1.0.34"); KindOf(err) != KindProbeMismatch {
		t.Fatalf("foreign tool: %v", err)
	}
}
