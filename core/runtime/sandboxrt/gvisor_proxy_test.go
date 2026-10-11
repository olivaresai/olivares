// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// Only runsc is replaced: requests use real sockets and the engine's real proxy.
// The installed-artifact journey separately exercises runsc itself.
type socketGuest struct {
	fakeCmd
	t          *testing.T
	denied     string
	socketPath string
}

func (f *socketGuest) run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, int, error) {
	if !hasArg(args, "run") {
		return f.fakeCmd.run(ctx, dir, env, name, args...)
	}
	f.t.Helper()
	info, err := os.Stat(filepath.Join(dir, "job.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	if info.Mode().Perm()&0o004 == 0 {
		f.t.Fatal("non-root guest cannot read its bind-mounted job")
	}
	b, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var job harnessJob
	if err := json.Unmarshal(b, &job); err != nil {
		f.t.Fatal(err)
	}
	if job.ProxySocket != "/sandbox/proxy.sock" {
		f.t.Fatalf("guest proxy_socket = %q; host-loopback TCP is unreachable in the guest network namespace", job.ProxySocket)
	}
	b, err = os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var spec ociSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		f.t.Fatal(err)
	}
	for _, mount := range spec.Mounts {
		if mount.Destination == job.ProxySocket {
			f.socketPath = mount.Source
			if !hasOpt(mount.Options, "ro") {
				f.t.Fatal("socket mount is writable")
			}
		}
	}
	if f.socketPath == "" || !hasNamespace(spec.Linux.Namespaces, "network") || !hasArg(args, "--network=none") || !hasArg(args, "--host-uds=open") {
		f.t.Fatal("missing socket mount or guest network isolation")
	}
	if !hasArg(args, "--oci-seccomp") {
		f.t.Fatal("gVisor ignores the guest seccomp profile unless OCI seccomp is enabled")
	}
	proxy, err := url.Parse(job.ProxyURL)
	if err != nil {
		f.t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", f.socketPath)
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, attempt := range []struct {
		endpoint string
		status   int
	}{{job.Target, 200}, {f.denied, 403}} {
		req, err := http.NewRequestWithContext(ctx, "GET", attempt.endpoint, nil)
		if err != nil {
			f.t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			f.t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			f.t.Fatal(err)
		}
		if resp.StatusCode != attempt.status {
			f.t.Fatalf("response = %d, want %d", resp.StatusCode, attempt.status)
		}
		if attempt.status == 200 && string(body) != "refused" {
			f.t.Fatalf("response = %q", body)
		}
	}
	return []byte(`{"steps":[],"response":"refused","reached":true}`), 0, nil
}

func TestGVisorProxySocketReachesOnlyAuthorizedTargetWithoutNIC(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("refused")) }))
	defer target.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unlisted target was reached") }))
	defer denied.Close()
	// A short external TMPDIR is required on hosts with long checkout paths (AF_UNIX limit).
	base, err := os.MkdirTemp("", "uds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	f := &socketGuest{t: t, denied: denied.URL, fakeCmd: fakeCmd{stateExit: 1}}
	backend := newGVisorBackendWith(GVisorConfig{RootfsDir: t.TempDir(), BundleRoot: base, ProxySocket: true}, f)
	engine := New(WithBackend(backend))
	u, _ := url.Parse(target.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), Job{RunID: "target", Target: target.URL, Probe: &Probe{ID: "inj-01"},
		Egress: EgressPolicy{Allow: []EgressRule{{Host: u.Hostname(), Ports: []int{p}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reached || !result.Attestation.NoNIC || !result.Attestation.DestroyVerified || result.Attestation.EgressDenied != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(f.socketPath); !os.IsNotExist(err) {
		t.Fatalf("relay socket persists: %v", err)
	}
}

type syntheticGuest struct {
	fakeCmd
	t *testing.T
}

func (f *syntheticGuest) run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, int, error) {
	if hasArg(args, "run") {
		info, err := os.Stat(filepath.Join(dir, "job.json"))
		if err != nil || info.Mode().Perm()&0o004 == 0 {
			f.t.Fatal("non-root synthetic guest cannot read its job")
		}
		data, err := os.ReadFile(filepath.Join(dir, "job.json"))
		if err != nil {
			f.t.Fatal(err)
		}
		var job harnessJob
		if err := json.Unmarshal(data, &job); err != nil {
			f.t.Fatal(err)
		}
		if job.ProxySocket != "" || hasArg(args, "--host-uds=open") || !hasArg(args, "--network=none") {
			f.t.Fatal("deny-all synthetic job gained a proxy transport")
		}
		if !hasArg(args, "--oci-seccomp") {
			f.t.Fatal("synthetic guest seccomp filter is not enabled")
		}
	}
	return f.fakeCmd.run(ctx, dir, env, name, args...)
}

func TestGVisorProxySocketConfigPreservesSyntheticExecution(t *testing.T) {
	f := &syntheticGuest{t: t, fakeCmd: fakeCmd{runStdout: harnessOK, stateExit: 1}}
	backend := newGVisorBackendWith(GVisorConfig{RootfsDir: t.TempDir(), BundleRoot: t.TempDir(), ProxySocket: true}, f)
	result, err := New(WithBackend(backend)).Run(context.Background(), Job{RunID: "synthetic", Steps: []Step{{Key: "s1", Input: "db"}}, Mocks: []Mock{{Resource: "db", Response: "ROWS"}}})
	if err != nil || len(result.Steps) != 2 || !result.Attestation.NoNIC || !result.Attestation.DestroyVerified {
		t.Fatalf("synthetic execution: %+v, %v", result, err)
	}
}
