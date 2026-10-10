// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
)

type fixturePolicy struct {
	policy egress.Policy
	err    error
}

func (p fixturePolicy) EgressPolicy(context.Context, model.TenantID) (egress.Policy, error) {
	return p.policy, p.err
}

type fixtureResolver struct{ ips []net.IP }

func (r fixtureResolver) LookupIP(context.Context, string) ([]net.IP, error) { return r.ips, nil }

type fixtureDialer struct {
	target    string
	mu        sync.Mutex
	addresses []string
}

func (d *fixtureDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addresses = append(d.addresses, address)
	d.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

func fixtureGit(t *testing.T, dir string, input []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %s: %v %s", args[0], err, out)
	}
	return out
}
func gitFixture(t *testing.T) (string, string, http.Handler) {
	t.Helper()
	repo := t.TempDir()
	fixtureGit(t, repo, nil, "init", "--quiet", ".")
	if err := os.Mkdir(filepath.Join(repo, "research"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "research", "SKILL.md"), []byte(harmless), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "research", "support.md"), []byte("exact fixture support"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, nil, "add", "research")
	fixtureGit(t, repo, nil, "commit", "--quiet", "-m", "fixture")
	commit := strings.TrimSpace(string(fixtureGit(t, repo, nil, "rev-parse", "HEAD")))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/fixture.git/info/refs":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, _ = io.WriteString(w, "001e# service=git-upload-pack\n0000")
			_, _ = w.Write(fixtureGit(t, repo, nil, "upload-pack", "--stateless-rpc", "--advertise-refs", repo))
		case r.Method == "POST" && r.URL.Path == "/fixture.git/git-upload-pack":
			data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			_, _ = w.Write(fixtureGit(t, repo, data, "upload-pack", "--stateless-rpc", repo))
		default:
			http.NotFound(w, r)
		}
	})
	return repo, commit, handler
}
func gitImporter(t *testing.T, handler http.Handler) (skills.GitImporter, *fixtureDialer) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dialer := &fixtureDialer{target: server.Listener.Addr().String()}
	trust := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	trust.ServerName = "example.com"
	trust.MinVersion = tls.VersionTLS12
	return skills.GitImporter{ScratchRoot: t.TempDir(), Policy: fixturePolicy{}, Resolver: fixtureResolver{[]net.IP{net.ParseIP("93.184.216.34")}}, Dialer: dialer, TLSConfig: trust}, dialer
}

func TestSkillsGitImportPinsCommitAndPreservesBytes(t *testing.T) {
	_, commit, handler := gitFixture(t)
	g, dialer := gitImporter(t, handler)
	pack, resolved, err := g.Import(context.Background(), model.SystemTenantID, "https://example.com/fixture.git", commit, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != commit {
		t.Fatalf("resolved %s, want %s", resolved, commit)
	}
	body, ok := pack.File("research/SKILL.md")
	if !ok || string(body) != harmless {
		t.Fatal("git import changed instruction bytes")
	}
	support, ok := pack.File("research/support.md")
	if !ok || string(support) != "exact fixture support" {
		t.Fatal("support file missing")
	}
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	if len(dialer.addresses) == 0 {
		t.Fatal("no actual transport dial observed")
	}
	for _, address := range dialer.addresses {
		if address != "93.184.216.34:443" {
			t.Fatalf("dial was not pinned: %s", address)
		}
	}
	entries, err := os.ReadDir(g.ScratchRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging survives: %v %v", entries, err)
	}
}

func TestSkillsGitSelectedFolderPreservesMember(t *testing.T) {
	_, commit, handler := gitFixture(t)
	g, _ := gitImporter(t, handler)
	pack, resolved, err := g.Import(t.Context(), model.SystemTenantID, "https://example.com/fixture.git", commit, "research")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != commit || len(pack.Members) != 1 || pack.Members[0].Directory != "research" {
		t.Fatalf("selected folder: %s %+v", resolved, pack.Members)
	}
	if support, ok := pack.File("research/support.md"); !ok || string(support) != "exact fixture support" {
		t.Fatalf("selected support: %q %v", support, ok)
	}
}
func TestSkillsGitRefusesUnsafeSourcesBeforeEffects(t *testing.T) {
	for _, source := range []string{"file:///tmp/repo", "ssh://example.com/repo", "https://user:password@example.com/repo", "https://example.com/repo?token=fixture", "https://example.com/%2e%2e/repo", "https://example.com/repo#fragment"} {
		t.Run(source, func(t *testing.T) {
			g := skills.GitImporter{ScratchRoot: filepath.Join(t.TempDir(), "absent")}
			pack, _, err := g.Import(context.Background(), model.SystemTenantID, source, "HEAD", "")
			var refusal *skills.ImportError
			if pack != nil || !errors.As(err, &refusal) || refusal.Code != "unsupported_source" {
				t.Fatalf("source not refused: %v", err)
			}
			if _, err := os.Stat(g.ScratchRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsafe source created staging")
			}
		})
	}
}
func TestSkillsGitEgressRefusesRebindingAndUnsafeRedirects(t *testing.T) {
	for _, kind := range []string{"reserved DNS", "deny policy", "policy unavailable", "HTTP redirect", "credential redirect"} {
		t.Run(kind, func(t *testing.T) {
			g, dialer := gitImporter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				dest := "http://example.com/refused"
				if kind == "credential redirect" {
					dest = "https://user:fixture@example.com/refused"
				}
				http.Redirect(w, r, dest, http.StatusFound)
			}))
			switch kind {
			case "reserved DNS":
				g.Resolver = fixtureResolver{[]net.IP{net.ParseIP("127.0.0.1")}}
			case "deny policy":
				g.Policy = fixturePolicy{policy: egress.Policy{InForce: true}}
			case "policy unavailable":
				g.Policy = fixturePolicy{err: errors.New("fixture policy failure")}
			}
			if pack, _, err := g.Import(context.Background(), model.SystemTenantID, "https://example.com/fixture.git", "HEAD", ""); pack != nil || err == nil {
				t.Fatal("unsafe transport published content")
			}
			dialer.mu.Lock()
			defer dialer.mu.Unlock()
			if !strings.Contains(kind, "redirect") && len(dialer.addresses) != 0 {
				t.Fatal("refused source was dialed")
			}
		})
	}
}
func TestSkillsGitCancellationDrainsTransportAndStaging(t *testing.T) {
	entered := make(chan struct{})
	done := make(chan struct{})
	g, _ := gitImporter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); defer close(done); <-r.Context().Done() }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := g.Import(ctx, model.SystemTenantID, "https://example.com/fixture.git", "HEAD", "")
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transport did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled transport did not drain")
	}
	entries, err := os.ReadDir(g.ScratchRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging survives: %v %v", entries, err)
	}
}
