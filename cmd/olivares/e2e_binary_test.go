// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

package main

// Black-box binary smoke: build the real single binary, boot `serve`, and drive
// the install→setup→login path a sysadmin walks ("instalable en un
// comando"). Gated behind the `e2e` build tag because it compiles + execs the
// binary; the in-process suite (default `go test`) is the fast, hermetic path.
// Mirrors scripts/web-e2e.sh.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var setupTokenRE = regexp.MustCompile(`olst_[A-Z0-9]+`)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestE2EBinary_InstallSetupLogin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "olivares")

	// Build the single binary (the web bundle's committed placeholder lets it link).
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "serve", "--insecure",
		"--listen", addr, "--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--data-dir", filepath.Join(dir, "data"))
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	base := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}

	// Wait for the server to answer /healthz.
	healthy := false
	for i := 0; i < 100; i++ {
		if resp, err := client.Get(base + "/healthz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("server never became healthy; stdout:\n%s", stdout.String())
	}

	// The one-time setup token is printed to stdout on first boot.
	var token string
	for i := 0; i < 50 && token == ""; i++ {
		token = setupTokenRE.FindString(stdout.String())
		if token == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if token == "" {
		t.Fatalf("no setup token on stdout:\n%s", stdout.String())
	}

	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		resp, err := client.Post(base+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return resp.StatusCode, m
	}

	if code, _ := post("/v1/setup", map[string]any{"token": token, "email": "admin@bin.test", "password": "supersecret-bin"}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	code, body := post("/v1/auth/login", map[string]any{"email": "admin@bin.test", "password": "supersecret-bin"})
	if code != http.StatusOK {
		t.Fatalf("login = %d", code)
	}
	bearer, _ := body["token"].(string)
	if bearer == "" {
		t.Fatal("no bearer token from login")
	}

	// whoami round-trips the bearer; the superadmin is recognized.
	req, _ := http.NewRequest("GET", base+"/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami = %d", resp.StatusCode)
	}

	// The embedded SPA is served on the same origin (go:embed bundle present).
	root, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer root.Body.Close()
	html, _ := io.ReadAll(root.Body)
	if root.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(string(html)), "<!doctype html") && !strings.Contains(string(html), "<html") {
		t.Errorf("embedded SPA not served at / (status %d, %d bytes)", root.StatusCode, len(html))
	}
}
