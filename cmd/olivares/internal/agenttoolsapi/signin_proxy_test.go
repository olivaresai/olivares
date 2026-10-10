// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secret"
)

// TestMain lets the test binary stand in for a tool that, like the real ones,
// reaches its vendor with the standard proxy and trust variables (#547).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "login" {
		os.Exit(proxiedCodex(os.Args[2:]))
	}
	os.Exit(m.Run())
}

const vendorPath = "/api/accounts/deviceauth/usercode"

// proxiedCodex is a Codex whose device login asks https://example.com (the
// stand-in vendor) for its code, and records the environment it was given.
func proxiedCodex(args []string) int {
	codexHome := os.Getenv("CODEX_HOME")
	if slices.Equal(args, []string{"status"}) {
		if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err == nil {
			fmt.Println("Logged in using ChatGPT")
			return 0
		}
		fmt.Println("Not logged in")
		return 1
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "env-seen"), []byte(strings.Join(os.Environ(), "\n")), 0o600); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Print("1. Open this link in your browser\n   https://auth.openai.com/codex/device\n2. Enter this one-time code\n   ABCD-12345\n")
	client := &http.Client{Timeout: 5 * time.Second} // the default transport reads the proxy variables
	resp, err := client.Get("https://example.com" + vendorPath)
	if err != nil {
		fmt.Println("Error logging in with device code:", err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Println("Error logging in with device code: status", resp.StatusCode)
		return 1
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		fmt.Println(err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

// TestSignInReachesTheVendorThroughTheEnginesProxy (#547): on a host that
// reaches the vendor only through an HTTPS proxy with its own CA, the engine's
// HTTPS_PROXY, no_proxy and SSL_CERT_FILE reach the login, and no engine secret.
func TestSignInReachesTheVendorThroughTheEnginesProxy(t *testing.T) {
	var reached atomic.Int32
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == vendorPath {
			reached.Add(1)
		}
	}))
	defer vendor.Close()
	var tunneled atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The host's only way out: CONNECT to the vendor's name, nothing else.
		if r.Method != http.MethodConnect || r.Host != "example.com:443" {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		upstream, err := net.Dial("tcp", vendor.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		client, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		tunneled.Add(1)
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, client) }()
		_, _ = io.Copy(client, upstream)
	}))
	defer proxy.Close()
	caFile := filepath.Join(t.TempDir(), "proxy-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: vendor.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("no_proxy", "internal.example")
	t.Setenv("SSL_CERT_FILE", caFile)
	for _, name := range []string{"OLIVARES_MASTER_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(name, "engine-secret-"+name)
	}
	// A proxy variable the engine resolved as an env: secret is the engine's,
	// not the host's way out. (The record is process-wide: no other test names it.)
	t.Setenv("ALL_PROXY", "http://user:engine-secret-proxy@corp.invalid:3128")
	if _, err := (secret.EnvHandler{}).Resolve(context.Background(), "ALL_PROXY"); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call, home := newSignInServer(t, func(m *Module, bin string) {
		m.SetProgramResolver(func(driver string) string {
			if driver == "codex" {
				return self
			}
			return filepath.Join(bin, driver)
		})
	})
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"})
	if code != 202 {
		t.Fatalf("start = %d %v", code, s)
	}
	end := pollSignInEnd(t, call, s["id"].(string))
	if end["state"] != "signed_in" || reached.Load() == 0 || tunneled.Load() == 0 {
		t.Fatalf("sign-in = %v, vendor reached %d times through %d tunnels: the login did not get the engine's proxy", end, reached.Load(), tunneled.Load())
	}
	raw, err := os.ReadFile(filepath.Join(home, "codex", "env-seen"))
	if err != nil {
		t.Fatal(err)
	}
	seen := strings.Split(string(raw), "\n")
	for _, want := range []string{"HTTPS_PROXY=" + proxy.URL, "no_proxy=internal.example", "SSL_CERT_FILE=" + caFile} {
		if !slices.Contains(seen, want) {
			t.Errorf("the login's environment lacks %s; it has %q", want, envNames(seen))
		}
	}
	for _, kv := range seen {
		if strings.Contains(kv, "engine-secret-") {
			t.Errorf("an engine secret reached the login in %s", envNames([]string{kv}))
		}
	}
}

// envNames is what a failure may print of an environment: its names, never a
// value, which can be a credential.
func envNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return names
}

// pollSignInEnd waits for a sign-in to end, whichever way.
func pollSignInEnd(t *testing.T, call func(string, string, any) (int, map[string]any), id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, s := call("GET", "/v1/m/agenttools/sign-in/"+id, nil)
		if code == 200 && s["state"] != "starting" && s["state"] != "waiting" && s["state"] != "checking" {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("sign-in %s never ended: %d %v", id, code, s)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
