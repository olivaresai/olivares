// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__relay_accept_failure" {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			os.Exit(125)
		}
		defer listener.Close()
		client, err := net.Dial("tcp4", listener.Addr().String())
		if err != nil {
			os.Exit(125)
		}
		defer client.Close()
		// Only this subprocess loses descriptor capacity. Accept must allocate
		// a real connected socket and fails with EMFILE when the limit is zero.
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{}); err != nil {
			os.Exit(125)
		}
		log.SetOutput(os.Stdout)
		relayAccept(listener, "", "")
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == namespaceArg {
		// Force a real setup failure without changing the host's network.
		runtime.LockOSThread()
		data := [2]unix.CapUserData{}
		if err := unix.Capset(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &data[0]); err != nil {
			os.Exit(125)
		}
		os.Exit(RunHelper(os.Args[1:], nil))
	}
	os.Exit(m.Run())
}

func TestNamespaceSetupFailureWritesReasonBeforeClose(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	payload, err := json.Marshal(helperSpec{ReadyFD: 3, Args: []string{"engine", "__olivares_confine", "--"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], namespaceArg, string(payload))
	cmd.ExtraFiles = []*os.File{writer}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, readErr := io.ReadAll(reader)
	waitErr := cmd.Wait()
	var exited *exec.ExitError
	if !errors.As(waitErr, &exited) || exited.ExitCode() != 126 || readErr != nil {
		t.Fatalf("helper failure: %v, %v, %s", waitErr, readErr, stderr.String())
	}
	want := strings.TrimPrefix(stderr.String(), "olivares: session ")
	if !strings.HasPrefix(want, "network setup failed:") || string(response) != want {
		t.Fatalf("readiness reason=%q; actual failure=%q", response, stderr.String())
	}
}

func wrappedFailureCommand(t *testing.T, endpoints ...string) (*exec.Cmd, func() error) {
	t.Helper()
	cmd := exec.Command("/bin/true", "__olivares_confine", "--")
	wait, release, _, err := Wrap(t.Context(), cmd, Policy{Providers: endpoints})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return cmd, wait
}

func TestBoundaryReadFailuresKeepTheirReason(t *testing.T) {
	for _, tc := range []struct{ name, response, want string }{
		{"setup", "network setup failed: operation not permitted\n", "operation not permitted"},
		{"exec", "ready\nfilesystem confinement failed: no such file or directory\n", "no such file or directory"},
		{"empty", "", "helper exited before reporting readiness"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, wait := wrappedFailureCommand(t, "http://127.0.0.1:11434")
			if _, err := cmd.ExtraFiles[0].WriteString(tc.response); err != nil {
				t.Fatal(err)
			}
			if err := wait(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("launch reason=%v; want %q", err, tc.want)
			}
		})
	}
}

func TestBoundaryReadTimeoutKeepsItsCause(t *testing.T) {
	cmd, wait := wrappedFailureCommand(t, "http://127.0.0.1:11434")
	fd, err := unix.Dup(int(cmd.ExtraFiles[0].Fd()))
	if err != nil {
		t.Fatal(err)
	}
	held := os.NewFile(uintptr(fd), "held-ready-writer")
	defer held.Close()
	if _, err := held.WriteString("network setup incomplete\n"); err != nil {
		t.Fatal(err)
	}
	err = wait()
	if !errors.Is(err, os.ErrDeadlineExceeded) || !strings.Contains(err.Error(), "network setup incomplete") {
		t.Fatalf("timeout or partial setup reason lost: %v", err)
	}
}

func TestPrivilegedLoopbackPortGetsOnlyRequiredCapability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endpoints  []string
		privileged bool
	}{
		{"IPv4 HTTP", []string{"http://127.0.0.1:80"}, true},
		{"IPv6 HTTPS default", []string{"https://[::1]"}, true},
		{"localhost HTTP default", []string{"http://localhost"}, true},
		{"IPv4 boundary low", []string{"http://127.0.0.1:1023"}, true},
		{"IPv4 boundary high", []string{"http://127.0.0.1:1024"}, false},
		{"IPv4 high", []string{"http://127.0.0.1:11434"}, false},
		{"remote HTTPS default", []string{"https://198.51.100.1"}, false},
		{"remote low and local high", []string{"https://198.51.100.1", "http://127.0.0.1:11434"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, wait := wrappedFailureCommand(t, tc.endpoints...)
			want := []uintptr{unix.CAP_NET_ADMIN, unix.CAP_SETPCAP}
			if tc.privileged {
				want = append(want, unix.CAP_NET_BIND_SERVICE)
			}
			if got := cmd.SysProcAttr.AmbientCaps; !slices.Equal(got, want) {
				t.Errorf("setup capabilities=%v; want %v", got, want)
			}
			_, _ = cmd.ExtraFiles[0].WriteString("ready\n")
			if err := wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func captureRelayLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func missingRelaySocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "relay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "missing.sock")
}

func TestRelayDialFailureLoggedOnce(t *testing.T) {
	output := captureRelayLog(t)
	client, peer := net.Pipe()
	defer peer.Close()
	relayConnection(client, missingRelaySocket(t), "")
	if got := output.String(); strings.Count(got, "relay") != 1 || !strings.Contains(got, "dial") || !strings.Contains(got, "no such file or directory") {
		t.Fatalf("dial failure log=%q", got)
	}
}

func TestRelayConnectFailureLoggedWithoutPeerSecrets(t *testing.T) {
	for _, tc := range []struct{ name, response, want string }{
		{"denied", "HTTP/1.1 403 secret-fixture\r\nContent-Length: 0\r\n\r\n", "403"},
		{"malformed", "secret-fixture\r\n\r\n", "response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureRelayLog(t)
			// Keep the Unix socket path within the kernel's 107-byte limit.
			dir, err := os.MkdirTemp("", "relay-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "p.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				upstream, err := listener.Accept()
				if err == nil {
					defer upstream.Close()
					_ = upstream.SetDeadline(time.Now().Add(5 * time.Second))
					buffer := make([]byte, 4096)
					_, err = upstream.Read(buffer)
					if err == nil {
						_, err = io.WriteString(upstream, tc.response)
					}
				}
				done <- err
			}()
			client, peer := net.Pipe()
			defer peer.Close()
			relayConnection(client, socket, "127.0.0.1:443")
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			got := output.String()
			if strings.Count(got, "relay") != 1 || !strings.Contains(got, tc.want) || strings.Contains(got, "secret-fixture") {
				t.Fatalf("CONNECT failure log=%q", got)
			}
		})
	}
}

func TestRelayAcceptFailureLoggedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body, err := exec.CommandContext(ctx, os.Args[0], "__relay_accept_failure").CombinedOutput()
	if err != nil {
		t.Fatalf("accept failure probe: %v: %s", err, body)
	}
	if got := string(body); strings.Count(got, "relay") != 1 || !strings.Contains(got, "too many open files") {
		t.Fatalf("accept failure log=%q", got)
	}
	output := captureRelayLog(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	relayAccept(l, "", "")
	if got := output.String(); got != "" {
		t.Fatalf("normal shutdown logged as failure: %q", got)
	}
}

func TestEndpointRelayFailureLoggedOnceWithoutRequestSecrets(t *testing.T) {
	output := captureRelayLog(t)
	d := destination{host: "127.0.0.1", port: "11434", scheme: "http"}
	handler := endpointRelay(missingRelaySocket(t), d)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:11434/?token=secret-fixture", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("relay status=%d", recorder.Code)
	}
	got := output.String()
	if strings.Count(got, "relay") != 1 || !strings.Contains(got, "no such file or directory") || strings.Contains(got, "secret-fixture") {
		t.Fatalf("HTTP relay failure log=%q", got)
	}
}
