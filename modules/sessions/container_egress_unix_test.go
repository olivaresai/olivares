// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/egress"
)

func containerEgressProbe() int {
	client := &http.Client{Timeout: 3 * time.Second}
	for _, key := range []string{"bound-test-key", "foreign-test-key"} {
		req, _ := http.NewRequest("GET", os.Getenv("TEST_BOUND_URL"), nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			fmt.Println("request failed")
			return 1
		}
		_ = resp.Body.Close()
		want := 200
		if key == "foreign-test-key" {
			want = 403
		}
		if resp.StatusCode != want {
			fmt.Printf("status %d want %d\n", resp.StatusCode, want)
			return 1
		}
	}
	resp, err := client.Get(os.Getenv("TEST_OTHER_URL"))
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != 403 {
			return 2
		}
	}
	// Bypassing the proxy cannot reach the host listener or the public network.
	for _, address := range []string{os.Getenv("TEST_OTHER_ADDR"), "1.1.1.1:443"} {
		c, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			c.Close()
			return 3
		}
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		_ = syscall.Close(fd)
		return 4
	}
	if !errors.Is(err, syscall.EPERM) {
		return 5
	}

	// The unfiltered relay must not be repointed at a socket in a tool mount.
	var relay struct{ Socket string }
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", os.Getppid()))
	if err != nil {
		fmt.Println("relay argv:", err)
		return 6
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) < 4 || json.Unmarshal([]byte(parts[3]), &relay) != nil || relay.Socket == "" {
		return 7
	}
	if err := os.Remove(relay.Socket); !errors.Is(err, syscall.EROFS) {
		fmt.Println("proxy unlink:", err)
		return 8
	}
	if err := os.Symlink("/workspace/foreign.sock", filepath.Join(filepath.Dir(relay.Socket), "swap.sock")); !errors.Is(err, syscall.EROFS) {
		fmt.Println("proxy symlink:", err)
		return 9
	}
	// With ptrace_scope=0, a same-uid tool still cannot open the relay's memory.
	mem, err := os.OpenFile(fmt.Sprintf("/proc/%d/mem", os.Getppid()), os.O_RDWR, 0)
	if err == nil {
		mem.Close()
		fmt.Println("relay memory writable")
		return 10
	}
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		fmt.Println("relay memory:", err)
		return 11
	}
	fmt.Println("bound endpoint only; credential pin; direct network and Unix sockets denied")
	return 0
}

func TestContainerRunner_RealBoundEndpoint(t *testing.T) {
	runtime := os.Getenv("OLIVARES_TEST_CONTAINER_RUNTIME")
	if runtime == "" {
		t.Skip("OLIVARES_TEST_CONTAINER_RUNTIME is not set")
	}
	if _, err := exec.LookPath(runtime); err != nil {
		t.Fatal(err)
	}
	var bound, other atomic.Int32
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound.Add(1)
		if r.Header.Get("Authorization") != "Bearer bound-test-key" {
			t.Error("foreign credential reached provider")
		}
		_, _ = io.WriteString(w, "bound")
	}))
	defer allowed.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { other.Add(1); w.WriteHeader(200) }))
	defer denied.Close()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("OLIVARES_TEST_CONTAINER_IMAGE")
	if image == "" {
		image = "docker.io/library/debian:13-slim"
	}
	spec := LaunchSpec{Program: program, Args: []string{"fixture-container-egress"}, Isolation: IsolationContainer,
		NetworkPolicy: &egress.Policy{Providers: []string{allowed.URL}, CredentialSHA256: sha256.Sum256([]byte("bound-test-key"))},
		WaitDelay:     time.Second, Env: []EnvVar{{Name: "TEST_BOUND_URL", Value: allowed.URL}, {Name: "TEST_OTHER_URL", Value: denied.URL}, {Name: "TEST_OTHER_ADDR", Value: strings.TrimPrefix(denied.URL, "http://")}}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	p, err := NewContainerRunner(ContainerRuntime{Path: runtime, Image: image}).Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	var output strings.Builder
	for frame := range p.Output() {
		output.Write(frame.Data)
	}
	exit, waitErr := p.Wait()
	if waitErr != nil || exit != 0 || !strings.Contains(output.String(), "credential pin") {
		t.Fatalf("exit=%d err=%v output=%s", exit, waitErr, output.String())
	}
	if bound.Load() != 1 || other.Load() != 0 {
		t.Fatalf("requests: bound=%d other=%d", bound.Load(), other.Load())
	}
	t.Log(output.String())
}
