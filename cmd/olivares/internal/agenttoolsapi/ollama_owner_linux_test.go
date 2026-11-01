// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package agenttoolsapi

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A process that binds the Ollama address after the engine's free-port probe answers
// the readiness check while the started child never listens. The appliance policy lets
// every installed tool bind the model runtime ports, so a 200 on the loopback address
// proves nothing about who serves it: the endpoint must not be registered, and the row
// must say why.
func TestOllamaForeignListenerOnTheAddressIsRefusedAndNotRegistered(t *testing.T) {
	call, f := newOllamaServer(t)
	// This `ollama serve` starts and stays, but never listens.
	quiet := "#!/bin/sh\n[ \"$1\" = serve ] || exit 2\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(f.bin, "ollama"), []byte(quiet), 0o755); err != nil {
		t.Fatal(err)
	}
	// A named tenant: the engine registers the endpoint for it once the address answers.
	org := seedOrg(t, f.store, "Second", "second")
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org.String()}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	// The start already probed the address and found it free: the foreign server binds now.
	l, err := net.Listen("tcp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.35.0"}`))
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = foreign.Serve(l) }()
	t.Cleanup(func() { _ = foreign.Close() })

	st := waitOllama(t, call, "failed")
	if msg, _ := st["message"].(string); !strings.Contains(msg, "could not confirm that the Ollama it started holds "+f.addr) {
		t.Fatalf("message = %q, want it to name the unconfirmed listener", msg)
	}
	f.mu.Lock()
	entered := f.entered
	f.mu.Unlock()
	if entered != 0 || f.registered() != "" {
		t.Fatalf("a foreign listener was registered as the managed Ollama: %d calls, endpoint %q", entered, f.registered())
	}
}

// listenerHeldBy against the real procfs: the test process plays the listener, and a
// sleeping child plays the started Ollama that does not hold it.
func TestListenerHeldByNamesOnlyTheProcessThatHoldsTheAddress(t *testing.T) {
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	listen := func(addr string) string {
		l, err := net.Listen("tcp4", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		return l.Addr().String()
	}
	loopback := listen("127.0.0.1:0")
	_, wildPort, _ := net.SplitHostPort(listen("0.0.0.0:0"))
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()
	type row struct {
		name string
		pid  int
		addr string
		want string // "" when held
	}
	rows := []row{
		{"its own listener", os.Getpid(), loopback, ""},
		{"another process's listener", child.Process.Pid, loopback, "another process listens on " + loopback},
		{"a wildcard listener of another process", child.Process.Pid, "127.0.0.1:" + wildPort, "another process listens on 127.0.0.1:" + wildPort},
		{"nothing listens", os.Getpid(), closed, "does not listen on " + closed},
		{"not an address", os.Getpid(), "localhost:11434", "is not an IP address and port"},
	}
	// A dual-stack [::] listener sits in /proc/net/tcp6 and takes IPv4 connections too.
	if l6, err := net.Listen("tcp", "[::]:0"); err == nil {
		t.Cleanup(func() { _ = l6.Close() })
		_, port6, _ := net.SplitHostPort(l6.Addr().String())
		rows = append(rows,
			row{"its own dual-stack listener", os.Getpid(), "127.0.0.1:" + port6, ""},
			row{"a dual-stack listener of another process", child.Process.Pid, "127.0.0.1:" + port6, "another process listens on 127.0.0.1:" + port6})
	} else {
		t.Logf("IPv6 is off, no tcp6 rows: %v", err)
	}
	for _, c := range rows {
		err := listenerHeldBy("/proc", c.pid, c.addr)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: listenerHeldBy(%d, %s) = %v, want %q", c.name, c.pid, c.addr, err, c.want)
		}
	}
}

// When the started child's descriptor links cannot be read (the policy refuses the
// engine, or another uid owns the child), the error says so: the listener that child
// holds must not be reported as another process's.
func TestListenerHeldByReportsUnreadableDescriptorsNotASquatter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory without search permission")
	}
	procfs := t.TempDir()
	fd := filepath.Join(procfs, "42", "fd")
	netDir := filepath.Join(procfs, "self", "net")
	for _, d := range []string{fd, netDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("socket:[777]", filepath.Join(fd, "3")); err != nil {
		t.Fatal(err)
	}
	// 127.0.0.1:11434 listening (0A) on socket inode 777.
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:2CAA 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 777 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(netDir, "tcp"), []byte(tcp), 0o644); err != nil {
		t.Fatal(err)
	}
	// The directory lists, but its links cannot be followed: readlink answers EACCES.
	if err := os.Chmod(fd, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fd, 0o755) })

	err := listenerHeldBy(procfs, 42, "127.0.0.1:11434")
	if err == nil || !strings.Contains(err.Error(), "the started Ollama's descriptors could not be read") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("listenerHeldBy = %v, want the unreadable descriptors named, not a squatter", err)
	}
}
