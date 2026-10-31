// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepareContainerRetainsEndpointAndCredentialPin(t *testing.T) {
	var hits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = io.WriteString(w, "bound") }))
	defer provider.Close()
	policy := Policy{Providers: []string{provider.URL}, CredentialSHA256: sha256.Sum256([]byte("bound-key"))}
	args, dir, ready, release, err := PrepareContainer(t.Context(), policy, []string{"tool"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var req helperSpec
	if err := json.Unmarshal([]byte(args[2]), &req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(args[2], "CredentialSHA256") || !req.Container {
		t.Fatal("helper carries the credential digest or lost container mode")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", req.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second * 5}
	for _, tc := range []struct {
		endpoint, key string
		want          int
	}{{provider.URL, "bound-key", 200}, {provider.URL, "wrong-key", 403}, {"http://127.0.0.1:1", "bound-key", 403}} {
		request, _ := http.NewRequest("GET", tc.endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+tc.key)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("status=%d want=%d", response.StatusCode, tc.want)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("provider requests=%d", hits.Load())
	}
	result := make(chan error, 1)
	go func() { result <- ready() }()
	c, err := net.Dial("unix", filepath.Join(dir, "ready.sock"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(c, "ready\n")
	_ = c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("proxy directory not removed: %v", err)
	}
}

func TestPrepareContainerRefusesEmptyPolicy(t *testing.T) {
	if _, _, _, _, err := PrepareContainer(t.Context(), Policy{}, []string{"tool"}); err == nil {
		t.Fatal("empty policy accepted")
	}
}

func TestContainerHelperDisablesDumping(t *testing.T) {
	cmd := exec.Command(os.Args[0], "__container_dumpable")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dumpable relay: %v %s", err, out)
	}
}

func TestPrepareContainerReadinessReason(t *testing.T) {
	_, dir, ready, release, err := PrepareContainer(t.Context(), Policy{Providers: []string{"https://example.com"}}, []string{"tool"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	c, err := net.Dial("unix", filepath.Join(dir, "ready.sock"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "socket restrictions failed: test reason\n")
	c.Close()
	if err := ready(); err == nil || !strings.Contains(err.Error(), "test reason") {
		t.Fatalf("lost reason: %v", err)
	}
}

func TestPrepareContainerReadinessCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	_, _, ready, release, err := PrepareContainer(ctx, Policy{Providers: []string{"https://example.com"}}, []string{"tool"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancel()
	result := make(chan error, 1)
	go func() { result <- ready() }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("accepted canceled launch")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt readiness")
	}
}

func TestContainerHelperReportsSetupFailure(t *testing.T) {
	args, _, ready, release, err := PrepareContainer(t.Context(), Policy{Providers: []string{"https://example.com"}}, []string{"tool"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var req helperSpec
	if err := json.Unmarshal([]byte(args[2]), &req); err != nil {
		t.Fatal(err)
	}
	req.Policy = Policy{}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "__olivares_confine", containerArg, string(payload))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("invalid policy accepted")
	}
	if err := ready(); err == nil || !strings.Contains(err.Error(), "bounded provider endpoint list") {
		t.Fatalf("lost setup failure: %v; stderr=%s", err, out)
	}
}
