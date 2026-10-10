// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestArtifactPrivilegedLoopbackStartsOrNamesCause(t *testing.T) {
	binary := os.Getenv("OLIVARES_TEST_ENGINE_BIN")
	if binary == "" {
		t.Skip("OLIVARES_TEST_ENGINE_BIN is not set")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("engine sha256=%x", sha256.Sum256(body))
	listener, err := net.Listen("tcp4", "127.0.0.1:1023")
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "bound") }))
	_ = provider.Listener.Close()
	provider.Listener = listener
	provider.Start()
	defer provider.Close()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd, _, err := confine.Command(ctx, confine.Policy{ReadWrite: []string{dir}}, "/usr/bin/curl", "--silent", "--show-error", "--fail", "--max-time", "5", provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Path, cmd.Args[0] = binary, binary
	cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	wait, release, _, err := Wrap(ctx, cmd, Policy{Providers: []string{provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := cmd.Start(); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Logf("privileged loopback provider refused by host before launch: %v", err)
			return
		}
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_, cause, named := strings.Cut(err.Error(), "failed:")
		cause, _, _ = strings.Cut(cause, "\"")
		if !named || strings.TrimSpace(cause) == "" {
			t.Fatalf("unnamed low-port boundary refusal: %v", err)
		}
		t.Logf("privileged loopback provider refused with setup reason: %v", err)
		return
	}
	if err := cmd.Wait(); err != nil || output.String() != "bound" {
		t.Fatalf("privileged loopback provider: %v, output=%q", err, output.String())
	}
}

func TestEndpointRelayStreamsAuthenticatedMCP(t *testing.T) {
	finish := make(chan struct{})
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.URL.Path != "/session/mcp" {
			t.Error("relay changed authenticated endpoint")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
	}))
	defer control.Close()
	defer close(finish)
	ds, err := destinations(Policy{Providers: []string{"http://127.0.0.1:1"}, Controls: []string{control.URL + "/session/mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	relay := httptest.NewServer(endpointRelay(p.socket, ds[1]))
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", relay.URL+"/session/mcp", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: ready\n" {
		t.Fatalf("MCP stream was buffered: %q %v", line, err)
	}
}

// Exercise helper dispatch in the built engine, not the test executable. This
// supplements (and does not replace) installed-platform session qualification.
func TestArtifactBoundSessionNetwork(t *testing.T) {
	binary := os.Getenv("OLIVARES_TEST_ENGINE_BIN")
	if binary == "" {
		t.Skip("OLIVARES_TEST_ENGINE_BIN is not set")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatal(err)
	}
	var forbidden atomic.Int32
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "bound") }))
	defer allowed.Close()
	_, port, _ := net.SplitHostPort(allowed.Listener.Addr().String())
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forbidden.Add(1); w.WriteHeader(http.StatusOK) }))
	_ = other.Listener.Close()
	other.Listener = listener
	other.Start()
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	script := `set -eu
"$1" --silent --show-error --fail --max-time 2 "$2"
if "$1" --silent --fail --max-time 2 "$3"; then exit 90; fi
if "$1" --silent --fail --max-time 2 --noproxy '' --proxy "$HTTP_PROXY" "$3"; then exit 91; fi
/bin/sh -c '"$1" --silent --fail --max-time 2 "$2"' sh "$1" "$2"
`
	cmd, _, err := confine.Command(ctx, confine.Policy{ReadWrite: []string{dir}}, "/bin/sh", "-c", script, "sh", curl, allowed.URL, other.URL)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Path, cmd.Args[0] = binary, binary
	cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	ready, release, _, err := Wrap(ctx, cmd, Policy{Providers: []string{allowed.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := ready(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("engine boundary: %v: %s", err, output.String())
	}
	if output.String() != "boundbound" || forbidden.Load() != 0 {
		t.Fatalf("output=%q foreign requests=%d", output.String(), forbidden.Load())
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("engine sha256=%x; bound requests from tool and subprocess passed; foreign direct/proxied requests=0", sha256.Sum256(body))
}
