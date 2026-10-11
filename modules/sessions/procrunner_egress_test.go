// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexsession "github.com/olivaresai/olivares/connectors/codex/session"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
	"golang.org/x/sys/unix"
)

func hostUnixSocketFixture(t *testing.T) net.Listener {
	t.Helper()
	// t.TempDir includes TMPDIR and the test name, which can exceed sun_path.
	// Only this socket uses /tmp; build scratch continues to use TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "olivares-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove host Unix socket directory: %v", err)
		}
	})
	path := filepath.Join(dir, "host.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close host Unix socket: %v", err)
		}
	})
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return listener
}

func TestHostUnixSocketFixtureWithLongTMPDIR(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 108))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	var path string
	t.Run("listen", func(t *testing.T) {
		listener := hostUnixSocketFixture(t)
		path = listener.Addr().String()
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err != nil {
			t.Fatalf("host socket is not reachable before confinement: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s permissions = %o, want %o", name, got, want)
			}
		}
	})
	if path != "" {
		for _, name := range []string{path, filepath.Dir(path)} {
			if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("fixture cleanup left %s: %v", name, err)
			}
		}
	}
}

func TestProcRunnerBoundProviderNetworkIsolation(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_EGRESS_CHILD") == "1" {
		client := &http.Client{Timeout: 2 * time.Second}
		for _, name := range []string{"OLIVARES_TEST_EGRESS_ALLOWED", "OLIVARES_TEST_EGRESS_FORBIDDEN"} {
			resp, err := client.Get(os.Getenv(name))
			if err != nil {
				fmt.Println(name + "=denied")
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			fmt.Printf("%s=%d\n", name, resp.StatusCode)
		}
		proxyURL, err := url.Parse(os.Getenv("HTTP_PROXY"))
		if err != nil || proxyURL.Host == "" {
			t.Fatal("no enforced proxy")
		}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		proxied := &http.Client{Transport: transport, Timeout: 2 * time.Second}
		resp, err := proxied.Get(os.Getenv("OLIVARES_TEST_EGRESS_FORBIDDEN"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("proxy allowed other host: %d", resp.StatusCode)
		}
		if c, err := net.DialTimeout("unix", os.Getenv("OLIVARES_TEST_EGRESS_UNIX"), time.Second); err == nil {
			_ = c.Close()
			t.Fatal("host Unix socket reachable")
		}
		if pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0); err != unix.EPERM {
			if err == nil {
				_ = unix.Close(pair[0])
				_ = unix.Close(pair[1])
			}
			t.Fatalf("datagram socketpair permits host sendto: %v", err)
		}
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("anonymous stream IPC unavailable: %v", err)
		}
		defer unix.Close(pair[0])
		defer unix.Close(pair[1])
		if err := unix.Connect(pair[0], &unix.SockaddrUnix{Name: os.Getenv("OLIVARES_TEST_EGRESS_UNIX")}); err == nil {
			t.Fatal("stream socketpair reconnected to host")
		}
		if _, err := unix.Write(pair[0], []byte("x")); err != nil {
			t.Fatal(err)
		}
		if n, err := unix.Read(pair[1], make([]byte, 1)); err != nil || n != 1 {
			t.Fatalf("stream IPC: %d %v", n, err)
		}
		if fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0); err != unix.EPERM {
			if err == nil {
				_ = unix.Close(fd)
			}
			t.Fatalf("non-IP socket not denied by seccomp: %v", err)
		}
		if err := unix.Unshare(unix.CLONE_NEWNET); err != unix.EPERM {
			t.Fatalf("namespace creation not denied: %v", err)
		}
		if os.Getenv("OLIVARES_TEST_EGRESS_GRANDCHILD") != "1" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcRunnerBoundProviderNetworkIsolation$")
			cmd.Env = append(os.Environ(), "OLIVARES_TEST_EGRESS_GRANDCHILD=1")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("subprocess lost confinement: %v\n%s", err, output)
			}
		}
		return
	}
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	var forbiddenRequests atomic.Int32
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer allowed.Close()
	_, port, err := net.SplitHostPort(allowed.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forbiddenRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	_ = forbidden.Listener.Close()
	forbidden.Listener = listener
	forbidden.Start()
	defer forbidden.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	unixListener := hostUnixSocketFixture(t)
	unixPath := unixListener.Addr().String()
	folder := filepath.Dir(unixPath)
	proc, err := NewProcRunner().Launch(ctx, LaunchSpec{
		Program: self, Args: []string{"-test.run=^TestProcRunnerBoundProviderNetworkIsolation$"},
		Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
		NetworkPolicy: &egress.Policy{Providers: []string{allowed.URL}},
		Env: []EnvVar{
			{Name: "OLIVARES_TEST_EGRESS_CHILD", Value: "1"},
			{Name: "OLIVARES_TEST_EGRESS_ALLOWED", Value: allowed.URL},
			{Name: "OLIVARES_TEST_EGRESS_FORBIDDEN", Value: forbidden.URL},
			{Name: "OLIVARES_TEST_EGRESS_UNIX", Value: unixPath},
		},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	var output strings.Builder
	for frame := range proc.Output() {
		output.Write(frame.Data)
		output.WriteByte('\n')
	}
	if code, err := proc.Wait(); err != nil || code != 0 {
		t.Fatalf("child exited %d: %v\n%s", code, err, output.String())
	}
	if !strings.Contains(output.String(), "OLIVARES_TEST_EGRESS_ALLOWED=200") {
		t.Fatalf("bound provider was unreachable:\n%s", output.String())
	}
	if requests := forbiddenRequests.Load(); requests != 0 {
		t.Fatalf("session bound to %s reached non-bound host %s on the same port (%d requests):\n%s", allowed.URL, forbidden.URL, requests, output.String())
	}
}

func TestProcRunnerNetworkSupervisorPreservesGracefulStop(t *testing.T) {
	folder := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", "trap 'sleep 0.1; echo flushed > final; exit 0' TERM; echo ready; while :; do sleep 1; done"},
		Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
		NetworkPolicy: &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}, WaitDelay: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for frame := range proc.Output() {
		if strings.Contains(string(frame.Data), "ready") {
			break
		}
	}
	if err := proc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(folder, "final")); err != nil || string(body) != "flushed\n" {
		t.Fatalf("graceful transcript lost: %q, %v", body, err)
	}
}

func TestProcRunnerNetworkBoundaryRejectsFailedExec(t *testing.T) {
	folder := t.TempDir()
	program := filepath.Join(folder, "missing-interpreter")
	if err := os.WriteFile(program, []byte("#!/olivares-fixture-no-such-interpreter\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := NewProcRunner().Launch(ctx, LaunchSpec{
		Program: program, Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
		NetworkPolicy: &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}},
	})
	if err == nil {
		_ = p.Stop(ctx)
		t.Fatal("failed exec was reported ready")
	}
	if !strings.Contains(err.Error(), "boundary") {
		t.Fatalf("no actionable boundary failure: %v", err)
	}
}

func TestProcRunnerNetworkSupervisorPreservesSignalExit(t *testing.T) {
	for _, signal := range []string{"TERM", "KILL", "ABRT"} {
		t.Run(signal, func(t *testing.T) {
			folder := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			proc, err := NewProcRunner().Launch(ctx, LaunchSpec{
				Program: "/bin/sh", Args: []string{"-c", "kill -" + signal + " $$"},
				Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
				NetworkPolicy: &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for range proc.Output() {
			}
			if code, err := proc.Wait(); err != nil || code != -1 {
				t.Fatalf("signal exit = (%d, %v), want (-1, nil)", code, err)
			}
		})
	}
}

// Use a separate engine process so its system trust cache contains only this
// synthetic provider root. The tool must read its distinct session CA inside
// Landlock and complete verified TLS through the loopback bridge.
func TestProcRunnerNetworkTLSBoundProvider(t *testing.T) {
	switch os.Getenv("OLIVARES_TEST_TLS_STAGE") {
	case "tool":
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(os.Getenv("OLIVARES_TEST_TLS_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "bound TLS provider" {
			t.Fatalf("TLS response %q: %v", body, err)
		}
		return
	case "engine":
		folder := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewProcRunner().Launch(ctx, LaunchSpec{
			Program: os.Args[0], Args: []string{"-test.run=^TestProcRunnerNetworkTLSBoundProvider$"},
			Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
			NetworkPolicy: &egress.Policy{Providers: []string{os.Getenv("OLIVARES_TEST_TLS_URL")}},
			Env:           []EnvVar{{Name: "OLIVARES_TEST_TLS_STAGE", Value: "tool"}, {Name: "OLIVARES_TEST_TLS_URL", Value: os.Getenv("OLIVARES_TEST_TLS_URL")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for frame := range proc.Output() {
			output.Write(frame.Data)
		}
		if code, err := proc.Wait(); code != 0 || err != nil {
			t.Fatalf("TLS child (%d,%v): %s", code, err, output.String())
		}
		return
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "bound TLS provider") }))
	defer provider.Close()
	roots := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(roots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcRunnerNetworkTLSBoundProvider$")
	cmd.Env = append(os.Environ(), "OLIVARES_TEST_TLS_STAGE=engine", "OLIVARES_TEST_TLS_URL="+provider.URL, "SSL_CERT_FILE="+roots, "SSL_CERT_DIR="+filepath.Dir(roots))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("TLS engine: %v\n%s", err, output)
	}
}

// The boundary's own reason reaches the run's failure instead of the generic
// "launch failed" text, so an operator can act on it.
func TestProcRunnerNetworkFailureKeepsItsReason(t *testing.T) {
	folder := t.TempDir()
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	_, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: "/bin/true", Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
		NetworkPolicy: &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}},
	})
	if msg := launchFailedErr("tool process launch", err).msg; !strings.Contains(msg, "Unix socket limit") {
		t.Fatalf("network boundary reason lost: %q (from %v)", msg, err)
	}
}

// NetworkPolicy is the only network authority: a bound provider alone, as for a
// tool whose hosts are not measured yet, keeps today's network.
func TestProcRunnerBoundProviderAloneKeepsHostNetwork(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_NO_WRAP_CHILD") == "1" {
		fmt.Printf("proxy=%q\n", os.Getenv("HTTP_PROXY"))
		return
	}
	folder := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, LaunchSpec{
		Program: os.Args[0], Args: []string{"-test.run=^TestProcRunnerBoundProviderAloneKeepsHostNetwork$"},
		Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder}}, ConfinementRequired: true,
		BoundProvider: BoundProvider{Kind: "openai", Endpoint: "https://api.openai.com/v1"},
		Env:           []EnvVar{{Name: "OLIVARES_TEST_NO_WRAP_CHILD", Value: "1"}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	var output strings.Builder
	for frame := range proc.Output() {
		output.Write(frame.Data)
	}
	if code, err := proc.Wait(); err != nil || code != 0 {
		t.Fatalf("child exited %d: %v\n%s", code, err, output.String())
	}
	if !strings.Contains(output.String(), `proxy=""`) {
		t.Fatalf("a bound provider without a network policy was wrapped:\n%s", output.String())
	}
}

// A record-bound Codex session keeps its session MCP connection: the endpoint joins
// the launch's control relays beside the hook endpoint, and where the host allows the
// namespaces (OLIVARES_TEST_EGRESS_ENFORCE=1) a confined child reaches the listener
// through the relay.
func TestConfinedCodexSessionMCPReachesItsListener(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_CODEX_MCP_CHILD") == "1" {
		// As Codex does from mcp_servers.olivares.bearer_token_env_var.
		req, err := http.NewRequest(http.MethodPost, os.Getenv("OLIVARES_TEST_CODEX_MCP_URL"), strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv("OLIVARES_HOOK_PEP_TOKEN"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Println("MCP=unreachable")
			return
		}
		_ = resp.Body.Close()
		fmt.Printf("MCP=%d\n", resp.StatusCode)
		return
	}
	var hits atomic.Int32
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/mcp" {
			hits.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-session-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer mcp.Close()
	const hook = "http://127.0.0.1:8447/"
	endpoint := mcp.URL + "/session/mcp"
	m := New(WithConfinement([]string{"/engine-data"}, true))
	spec := m.childSpec(CreateRunParams{WorkspaceDir: t.TempDir(), ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex, AuthSource: AuthSourceManagedInjection}}, childDecision{cred: Credential{bound: BoundProvider{Kind: ProviderKindOpenAICompatible, Endpoint: "https://127.0.0.1:44399/v1"}}, gateEnv: []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: hook}, {Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "fixture-session-token"}}})
	cleanup, err := ConfigureSessionMCP(&spec, providerDriverCodex, t.TempDir(), "run-one", endpoint, "OLIVARES_HOOK_PEP_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if spec.NetworkPolicy == nil || strings.Join(spec.NetworkPolicy.Controls, " ") != hook+" "+endpoint {
		t.Fatalf("a confined Codex launch does not relay its hook and session MCP endpoints: %+v", spec.NetworkPolicy)
	}
	if !strings.Contains(strings.Join(spec.Args, " "), "mcp_servers.olivares.url="+strconv.Quote(endpoint)) {
		t.Fatalf("Codex is not pointed at the session MCP endpoint: %q", spec.Args)
	}
	if os.Getenv("OLIVARES_TEST_EGRESS_ENFORCE") != "1" {
		t.Skip("OLIVARES_TEST_EGRESS_ENFORCE=1 is not set: the relay needs user and network namespaces")
	}
	dir := t.TempDir()
	spec.Program, spec.Args, spec.Dir = os.Args[0], []string{"-test.run=^TestConfinedCodexSessionMCPReachesItsListener$"}, dir
	spec.Env = append(spec.Env, EnvVar{Name: "OLIVARES_TEST_CODEX_MCP_CHILD", Value: "1"}, EnvVar{Name: "OLIVARES_TEST_CODEX_MCP_URL", Value: endpoint})
	spec.Confinement = &confine.Policy{ReadWrite: []string{dir}, ReadOnly: []string{os.Args[0]}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for frame := range proc.Output() {
		out.Write(frame.Data)
		out.WriteByte('\n')
	}
	if _, err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "MCP=200") || hits.Load() != 1 {
		t.Fatalf("the confined child did not reach the session MCP listener (hits %d): %s", hits.Load(), out.String())
	}
}

// A record-bound Codex session's governed hook keeps its PEP: the URL the hook
// command reads (OLIVARES_CODEX_HOOK_URL) joins the launch's control relays, and
// where the host allows the namespaces (OLIVARES_TEST_EGRESS_ENFORCE=1) the hook
// client in a confined child gets the listener's decision, not its own deny.
func TestConfinedCodexHookReachesItsListener(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_CODEX_HOOK_CHILD") == "1" {
		// As `olivares codex-hook` does from the session's environment.
		res := codexsession.RunClient(context.Background(), strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"true"}}`),
			codexsession.ClientConfig{Endpoint: os.Getenv("OLIVARES_CODEX_HOOK_URL"), Token: os.Getenv("OLIVARES_CODEX_HOOK_TOKEN")})
		fmt.Printf("HOOK=%s\n", res.Stdout)
		return
	}
	var hits atomic.Int32
	pep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-hook-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"listener":"codex-pep"}`)
	}))
	defer pep.Close()
	hook := pep.URL + "/"
	m := New(WithConfinement([]string{"/engine-data"}, true))
	spec := m.childSpec(CreateRunParams{WorkspaceDir: t.TempDir(), ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex, AuthSource: AuthSourceManagedInjection}}, childDecision{cred: Credential{bound: BoundProvider{Kind: ProviderKindOpenAICompatible, Endpoint: "https://127.0.0.1:44399/v1"}}, gateEnv: []EnvVar{{Name: "OLIVARES_CODEX_HOOK_URL", Value: hook}, {Name: "OLIVARES_CODEX_HOOK_TOKEN", Value: "fixture-hook-token"}}})
	if spec.NetworkPolicy == nil || !slices.Contains(spec.NetworkPolicy.Controls, hook) {
		t.Fatalf("a confined Codex launch does not relay its hook endpoint: %+v", spec.NetworkPolicy)
	}
	if os.Getenv("OLIVARES_TEST_EGRESS_ENFORCE") != "1" {
		t.Skip("OLIVARES_TEST_EGRESS_ENFORCE=1 is not set: the relay needs user and network namespaces")
	}
	dir := t.TempDir()
	spec.Program, spec.Args, spec.Dir = os.Args[0], []string{"-test.run=^TestConfinedCodexHookReachesItsListener$"}, dir
	spec.Env = append(spec.Env, EnvVar{Name: "OLIVARES_TEST_CODEX_HOOK_CHILD", Value: "1"})
	spec.Confinement = &confine.Policy{ReadWrite: []string{dir}, ReadOnly: []string{os.Args[0]}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for frame := range proc.Output() {
		out.Write(frame.Data)
		out.WriteByte('\n')
	}
	if _, err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `HOOK={"listener":"codex-pep"}`) || hits.Load() != 1 {
		t.Fatalf("the confined Codex hook did not reach its listener (hits %d): %s", hits.Load(), out.String())
	}
}
