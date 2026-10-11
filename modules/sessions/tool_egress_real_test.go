// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// ONE REAL TURN OF EACH INSTALLED TOOL ON A RECORD BOUND TO A SYNTHETIC TLS PROVIDER.
//
// It runs the product's launch (record, profile, driver, runner) with the installed
// tool and a loopback provider that answers over TLS from a test CA. No vendor
// account or key is used. The turn must settle with the provider's answer, and a
// key-bound tool must present the record's key to the provider.
//
// Report mode (default) keeps the host network, even for a tool whose boundary is on,
// and gives the tool the boundary's own proxy and trust variables (proxyEnvironment),
// with the provider's CA. The proxy refuses and records every request, and the TCP
// sockets of the test's process tree are read from /proc every 50 ms until the session
// stops, so an authority other than the bound endpoint fails the test. That is the
// tool's endpoint inventory. It does not see UDP or connections shorter than a poll.
//
// Enforce mode (OLIVARES_TEST_EGRESS_ENFORCE=1) runs the same turn with the network
// boundary the product's launch spec carries (a user/network namespace and the
// per-session proxy) on a host that allows unprivileged user and network namespaces.
// The tool trusts only the per-session CA. Before the tool starts, under the same
// launch, the proxy must refuse a second loopback host and a public host (curl exit 56)
// and a direct connection must fail (curl exit 7). A tool the product launches without
// the boundary reaches the second host, and the turn fails. The proxy and /proc
// observers see nothing in another namespace, so this mode does not use them.
//
// Binaries: OLIVARES_TEST_CODEX_PROGRAM, OLIVARES_TEST_GROK_BIN,
// OLIVARES_TEST_GEMINI_BIN, OLIVARES_TEST_OPENCODE_BIN.
func TestRealToolTLSBoundNetwork(t *testing.T) {
	for _, tc := range []struct{ driver, kind, binEnv string }{
		{providerDriverCodex, ProviderKindOpenAI, "OLIVARES_TEST_CODEX_PROGRAM"},
		{providerDriverGrok, ProviderKindXAI, "OLIVARES_TEST_GROK_BIN"},
		{providerDriverGemini, ProviderKindGemini, "OLIVARES_TEST_GEMINI_BIN"},
		// OpenCode reaches a key only at the vendor's own address; a local model is
		// the binding a synthetic provider can stand in for.
		{providerDriverOpenCode, ProviderKindOllama, "OLIVARES_TEST_OPENCODE_BIN"},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			bin := os.Getenv(tc.binEnv)
			if bin == "" {
				t.Skip(tc.binEnv + " is not set")
			}
			bin, err := filepath.EvalSymlinks(bin)
			if err != nil {
				t.Fatal(err)
			}
			version, err := exec.Command(bin, "--version").Output()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s %s", tc.driver, strings.TrimSpace(string(version)))
			enforce := os.Getenv("OLIVARES_TEST_EGRESS_ENFORCE") == "1"
			pki := os.Getenv("OLIVARES_TEST_EGRESS_PKI")
			if pki == "" {
				pki = writeToolEgressPKI(t)
				if enforce {
					// The engine's proxy verifies the provider with the process trust
					// roots, which Go reads once: run the turn in a child that has them.
					ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
					defer cancel()
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRealToolTLSBoundNetwork$/^"+tc.driver+"$", "-test.v", "-test.count=1")
					cmd.Env = append(os.Environ(), "OLIVARES_TEST_EGRESS_PKI="+pki, "SSL_CERT_FILE="+filepath.Join(pki, "ca.pem"))
					output, err := cmd.CombinedOutput()
					t.Logf("%s", output)
					if err != nil || !bytes.Contains(output, []byte("--- PASS: TestRealToolTLSBoundNetwork/"+tc.driver)) {
						t.Fatalf("enforced %s turn: %v", tc.driver, err)
					}
					return
				}
			}
			realToolTLSTurn(t, tc.driver, tc.kind, bin, pki, enforce)
		})
	}
}

func realToolTLSTurn(t *testing.T, driver, kind, bin, pki string, enforce bool) {
	provider := newToolEgressProvider(t, pki)
	foreign := newToolEgressProvider(t, pki)
	caFile := filepath.Join(pki, "ca.pem")
	program := bin
	recording := newRefusingProxy(t)
	var configHome, userHome, probeLog string
	edit := func(spec *LaunchSpec) {
		if driver == providerDriverGemini {
			// TEST ONLY: keep the native Google wire but substitute the answering
			// TLS fixture. Production Gemini records accept no base_url override.
			spec.Env = append(spec.Env, EnvVar{Name: "GOOGLE_GEMINI_BASE_URL", Value: provider.url})
			if spec.NetworkPolicy != nil {
				spec.NetworkPolicy.Providers = []string{provider.url}
			}
		}

		if !enforce {
			// The boundary's own variables, the provider's CA in place of the session CA.
			// NO_PROXY keeps the loopback provider direct; the boundary relays it instead.
			spec.NetworkPolicy = nil
			for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
				spec.Env = append(spec.Env, EnvVar{Name: name, Value: recording.url})
			}
			spec.Env = append(spec.Env, EnvVar{Name: "NO_PROXY", Value: "127.0.0.1,localhost"}, EnvVar{Name: "no_proxy", Value: "127.0.0.1,localhost"})
			for _, name := range []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"} {
				spec.Env = append(spec.Env, EnvVar{Name: name, Value: caFile})
			}
			return
		}
		spec.Confinement = &confine.Policy{ReadWrite: []string{spec.Dir, configHome, userHome, filepath.Dir(probeLog)}, ReadOnly: []string{filepath.Dir(bin), filepath.Dir(program)}}
		spec.ConfinementRequired = true
	}
	if enforce {
		curl, err := exec.LookPath("curl")
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		program, probeLog = filepath.Join(dir, "tool"), filepath.Join(t.TempDir(), "probe.log")
		ca, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o600); err != nil {
			t.Fatal(err)
		}
		probe := func(want int, args ...string) string {
			cmd := codexPrivacyShellWord(curl) + " --silent --max-time 5 --cacert " + codexPrivacyShellWord(filepath.Join(dir, "ca.pem"))
			for _, arg := range args {
				cmd += " " + codexPrivacyShellWord(arg)
			}
			return fmt.Sprintf("%s >/dev/null 2>&1\nrc=$?\n[ $rc -eq %d ] || { echo \"second host %s: curl exit $rc, want %d\" >>%s; exit 97; }\n",
				cmd, want, args[len(args)-1], want, codexPrivacyShellWord(probeLog))
		}
		script := "#!/bin/sh\n" +
			probe(56, foreign.url+"/v1/models") + // refused by the proxy
			probe(56, "https://example.com/") + // a public host, refused by the proxy
			probe(7, "--noproxy", "*", foreign.url+"/v1/models") + // no direct route
			"exec " + codexPrivacyShellWord(bin) + " \"$@\"\n"
		if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	drivers := map[string]ProviderDriver{providerDriverCodex: NewCodexDriver(), providerDriverGrok: NewGrokDriver(), providerDriverOpenCode: NewOpenCodeDriver(), providerDriverGemini: NewGeminiDriver()}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(realToolRunner{NewProcRunner(), edit}), WithProviderDriver(drivers[driver]),
		WithDriverProgram(driver, program), WithProductVersion("test"),
		WithProviderSecretVault(newFakeVault()), WithProviderProbe(&fakeProbe{}),
		WithStopWaitDelay(2*time.Second), WithDriverTimeouts(60*time.Second, 2*time.Second),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true, RecordIO: true}}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	baseURL, key := provider.url+"/v1", testProviderKey
	if kind == ProviderKindOllama {
		baseURL, key = provider.url, ""
	}
	if kind == ProviderKindGemini {
		baseURL = ""
	}
	record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: kind, DisplayName: "Synthetic TLS provider", BaseURL: baseURL, APIKey: key})
	if _, err := m.recordProbeOutcome(t.Context(), tenant, record, ProbeOK, "accepted", []string{"fixture-model"}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	configHome, userHome = t.TempDir(), t.TempDir()
	if driver == providerDriverGemini {
		configHome = filepath.Join(configHome, ".gemini")
		if err := os.Mkdir(configHome, 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: driver, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "Egress inventory", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref})

	direct := map[string]bool{}
	var mu sync.Mutex
	done, polled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(polled)
		for {
			for addr := range processTreeRemotes(os.Getpid()) {
				mu.Lock()
				direct[addr] = true
				mu.Unlock()
			}
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	stopPolling := sync.OnceFunc(func() { close(done); <-polled })
	t.Cleanup(stopPolling)
	chosenModel := "fixture-model"
	if driver == providerDriverGemini {
		chosenModel = ""
	}
	run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{ProviderProfileRef: profile.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, Model: chosenModel, PermissionMode: permModeBypass, MayRunUnrestricted: true})
	if err != nil {
		t.Fatalf("%s launch: %v%s", driver, err, readProbeLog(probeLog))
	}
	stop := sync.OnceFunc(func() {
		if _, err := m.stopRun(context.Background(), tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(stop)
	if err := m.sendTextInput(t.Context(), tenant, run.RunRef, "Say hi."); err != nil {
		t.Fatalf("%s input: %v", driver, err)
	}
	answered, settled := false, false
	for deadline := time.Now().Add(150 * time.Second); !(answered && settled) && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		live, ok := m.rt.getLive(tenant, run.RunRef)
		if !ok {
			t.Fatalf("%s session ended before its turn settled", driver)
		}
		for _, frame := range live.ring.readFrom(0).frames {
			answered = answered || bytes.Contains(frame.Data, []byte("fixture answer"))
		}
		settled = live.session.ActiveTurn() == ""
	}
	time.Sleep(12 * time.Second) // the background work after a turn (titles, summaries, telemetry)
	stop()
	stopPolling()
	mu.Lock()
	defer mu.Unlock()
	requests, authorized := provider.seen()
	foreignRequests, _ := foreign.seen()
	t.Logf("%s: answered=%v settled=%v; bound requests %v (%d with the record's key); proxied %v; direct %v; second host requests %d",
		driver, answered, settled, requests, authorized, recording.hosts(), direct, len(foreignRequests))
	if !answered || !settled || len(requests) == 0 {
		t.Errorf("%s did not complete its turn on the bound provider%s", driver, readProbeLog(probeLog))
	}
	if key != "" && authorized == 0 {
		t.Errorf("%s never presented the record's key to the bound provider", driver)
	}
	if n := len(foreignRequests); n != 0 {
		t.Errorf("the second host received %d request(s)", n)
	}
	if enforce {
		return
	}
	for host, n := range recording.hosts() {
		t.Errorf("%d request(s) to %s, a host other than the bound endpoint", n, host)
	}
	for addr := range direct {
		t.Errorf("a direct connection to %s (not loopback, not through the proxy)", addr)
	}
}

// readProbeLog is the enforce-mode probe's verdict, empty when it passed.
func readProbeLog(path string) string {
	data, _ := os.ReadFile(path) // absent in report mode and after a passing probe
	if len(data) == 0 {
		return ""
	}
	return "; " + strings.TrimSpace(string(data))
}

// realToolRunner adjusts the product's launch spec for the measurement mode.
type realToolRunner struct {
	Runner
	edit func(*LaunchSpec)
}

func (r realToolRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	r.edit(&spec)
	return r.Runner.Launch(ctx, spec)
}

// writeToolEgressPKI writes a test CA (ca.pem) and a distinct 127.0.0.1 server leaf
// with its own key, whose chain includes that CA (leaf.pem, key.pem): native rustls
// clients refuse a CA certificate used as the server certificate, and OpenSSL takes a
// leaf with the CA's key and name for a self-signed one.
func writeToolEgressPKI(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tool egress test CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	root, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), BasicConstraintsValid: true, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	cert, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root})
	for name, data := range map[string][]byte{
		"ca.pem":   rootPEM,
		"leaf.pem": append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), rootPEM...),
		"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// toolEgressTLS is the server configuration for the leaf writeToolEgressPKI wrote.
func toolEgressTLS(t *testing.T, pki string) *tls.Config {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(pki, "leaf.pem"), filepath.Join(pki, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}}
}

// toolEgressProvider is a loopback TLS provider for the wire APIs the tools speak
// (OpenAI Responses, OpenAI chat completions, model listing). It records every
// request and answers each turn with "fixture answer".
type toolEgressProvider struct {
	url        string
	mu         sync.Mutex
	paths      []string
	authorized int
}

func newToolEgressProvider(t *testing.T, pki string) *toolEgressProvider {
	t.Helper()
	p := &toolEgressProvider{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.paths = append(p.paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") == "Bearer "+testProviderKey || r.Header.Get("x-goog-api-key") == testProviderKey {
			p.authorized++
		}
		p.mu.Unlock()
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // GET and unknown bodies take the model list below
		switch {
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"fixture answer\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"fixture answer"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "text/event-stream")
			message := map[string]any{"id": "msg_fixture", "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "fixture answer", "annotations": []any{}}}}
			for _, event := range []map[string]any{
				{"type": "response.created", "response": map[string]any{"id": "resp_fixture", "object": "response", "status": "in_progress", "output": []any{}}},
				{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_fixture", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}},
				{"type": "response.output_text.delta", "item_id": "msg_fixture", "output_index": 0, "content_index": 0, "delta": "fixture answer"},
				{"type": "response.output_item.done", "output_index": 0, "item": message},
				{"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "object": "response", "status": "completed", "model": "fixture-model",
					"output": []any{message}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
			} {
				data, _ := json.Marshal(event)
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
			}
		case strings.HasSuffix(r.URL.Path, "/chat/completions") && body.Stream:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range []string{
				`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"fixture-model","choices":[{"index":0,"delta":{"role":"assistant","content":"fixture answer"},"finish_reason":null}]}`,
				`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			} {
				_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":0,"model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"fixture-model","object":"model","created":0,"owned_by":"fixture"}],"models":[{"name":"fixture-model","model":"fixture-model"}]}`)
		}
	}))
	server.TLS = toolEgressTLS(t, pki)
	server.StartTLS()
	t.Cleanup(server.Close)
	p.url = server.URL
	return p
}

// seen returns the requests and how many carried the record's key.
func (p *toolEgressProvider) seen() ([]string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...), p.authorized
}
