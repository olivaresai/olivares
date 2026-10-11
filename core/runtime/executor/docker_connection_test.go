// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortSocketDir returns a fresh directory with a short path. A unix socket path holds
// at most 108 bytes and t.TempDir() embeds the test and subtest names under TMPDIR, so a
// long runner TMPDIR fails bind with "invalid argument" before the code under test runs.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	for _, base := range []string{"/tmp", ""} {
		if dir, err := os.MkdirTemp(base, "dk"); err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
	}
	t.Fatal("no temporary directory for the daemon socket")
	return ""
}

func TestDockerConnectionTestIsReadOnly(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			socket := filepath.Join(shortSocketDir(t), "d.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			calls := make(chan string, 2)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.Method + " " + r.URL.Path
				if r.Header.Get("Authorization") != "" {
					t.Error("local socket received a bearer")
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte("OK"))
				} else {
					_, _ = w.Write([]byte("sensitive daemon error"))
				}
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			backend := NewDockerBackend(DockerConfig{SocketPath: socket})
			err = backend.TestConnection(context.Background())
			if status == http.StatusOK && err != nil {
				t.Fatal(err)
			}
			if status != http.StatusOK && err == nil {
				t.Fatal("failed daemon answered successful test")
			}
			if err != nil && strings.Contains(err.Error(), "sensitive daemon error") {
				t.Fatal("connection diagnostic exposed daemon response material")
			}
			if got := <-calls; got != "GET /_ping" {
				t.Fatalf("daemon request = %q", got)
			}
			select {
			case got := <-calls:
				t.Fatalf("unexpected daemon request %q", got)
			default:
			}
		})
	}
}
