// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// B1: every present serve-family listener is prepared and acquired BEFORE the
// first-account announcement, and nothing serves before that announcement was
// accepted by its writer.
//
// These tests drive the real runEngine against a real SQLite engine in a temporary
// data directory and real loopback sockets. Their oracles are causal facts, never an
// elapsed time alone:
//
//   - whether the announce callback ran (a counter it increments itself);
//   - whether setup.token exists in the data directory (Ensure persists its hash
//     before it returns the plaintext);
//   - whether a listener answered a real HTTP request while runEngine was in flight
//     (a response is proof Serve ran; a kernel-queued connection is not);
//   - whether every address the run was given can be bound again after it returned;
//   - the error value runEngine returned, through errors.Is.
//
// The token plaintext is never printed: failures report booleans and counts only.
// No t.Parallel: some cases set process environment and every case boots an engine.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	bindAnnounceRunBound   = 120 * time.Second
	bindAnnounceProbeEvery = 50 * time.Millisecond
	bindAnnounceProbeWait  = 300 * time.Millisecond
)

var (
	errBindAnnounceFixture = errors.New("bind-announce: fixture-injected failure")
	bindAnnounceTokenRE    = regexp.MustCompile(`olst_[A-Z0-9]+`)
)

// bindAnnounceFreeAddr returns a loopback address that was free a moment ago. The
// reuse window is a fixture limitation; an unrelated collision fails loudly as a bind
// error naming the address, never as a silent pass.
func bindAnnounceFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind-announce: reserve a loopback port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("bind-announce: release the reserved port %s: %v", addr, err)
	}
	return addr
}

// bindAnnounceOccupy holds a plain listener on a fresh loopback port for the test.
func bindAnnounceOccupy(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind-announce: occupy a loopback port: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, ln.Addr().String()
}

// requireBindAnnounceRebindable proves nothing from the run still holds addr.
func requireBindAnnounceRebindable(t *testing.T, what string, addrs ...string) {
	t.Helper()
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("%s: %s is not bindable after runEngine returned, so a listener from the run is still held: %v", what, addr, err)
			continue
		}
		if err := ln.Close(); err != nil {
			t.Errorf("%s: close the rebind probe on %s: %v", what, addr, err)
		}
	}
}

func bindAnnounceTokenFile(dataDir string) string { return filepath.Join(dataDir, "setup.token") }

func bindAnnounceTokenExists(t *testing.T, dataDir string) bool {
	t.Helper()
	_, err := os.Stat(bindAnnounceTokenFile(dataDir))
	switch {
	case err == nil:
		return true
	case errors.Is(err, os.ErrNotExist):
		return false
	default:
		t.Fatalf("bind-announce: stat setup.token: %v", err)
		return false
	}
}

// bindAnnounceBuffer is a concurrency-safe output sink; its content is inspected with
// booleans only.
type bindAnnounceBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bindAnnounceBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bindAnnounceBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// bindAnnounceRun is one owned runEngine invocation.
type bindAnnounceRun struct {
	cancel    context.CancelFunc
	announced atomic.Int32
	answered  atomic.Bool
}

type bindAnnounceResult struct {
	err       error
	announced int
	// answered is true when the run's main HTTP listener returned a real HTTP response
	// to the probe while runEngine was still in flight.
	answered bool
}

func bindAnnounceBaseURL(opts serveOptions) string {
	if opts.insecure {
		return "http://" + opts.listen
	}
	return "https://" + opts.listen
}

func bindAnnounceClient(timeout time.Duration) (*http.Client, *http.Transport) {
	tr := &http.Transport{
		DisableKeepAlives: true,
		// Loopback fixture only: the probe asks whether OUR engine answers, not whether
		// its self-signed certificate is trusted.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback test probe
	}
	return &http.Client{Transport: tr, Timeout: timeout}, tr
}

// runBindAnnounce runs runEngine to completion. While it is in flight a probe asks
// the main HTTP address for /healthz; the first real response marks the run as
// answered, runs onServing (if any) and then cancels the run so it drains through the
// unchanged DS1 path. A run that never serves returns on its own.
func runBindAnnounce(t *testing.T, opts serveOptions, out io.Writer, announce func(*bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error, onServing func(base string)) bindAnnounceResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &bindAnnounceRun{cancel: cancel}
	done := make(chan error, 1)
	go func() { done <- runEngine(ctx, out, opts, announce(run)) }()

	client, tr := bindAnnounceClient(bindAnnounceProbeWait)
	defer tr.CloseIdleConnections()
	base := bindAnnounceBaseURL(opts)
	deadline := time.After(bindAnnounceRunBound)
	ticker := time.NewTicker(bindAnnounceProbeEvery)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return bindAnnounceResult{err: err, announced: int(run.announced.Load()), answered: run.answered.Load()}
		case <-deadline:
			cancel()
			select {
			case err := <-done:
				t.Fatalf("UNRESOLVED: runEngine did not return within %s (returned %v only after the fixture canceled it)", bindAnnounceRunBound, err)
			case <-time.After(30 * time.Second):
				t.Fatalf("UNRESOLVED: runEngine did not return within %s, nor 30s after cancellation", bindAnnounceRunBound)
			}
			return bindAnnounceResult{}
		case <-ticker.C:
			if run.answered.Load() {
				continue
			}
			resp, err := client.Get(base + "/healthz")
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			run.answered.Store(true)
			if onServing != nil {
				onServing(base)
			}
			cancel()
		}
	}
}

// serveAnnounce is the `serve` callback shape: the real announceSetup, counted.
func serveAnnounce(insecure bool) func(*bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error {
	return func(run *bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error {
		return func(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress) error {
			run.announced.Add(1)
			return announceSetup(ctx, out, eng, addr, insecure)
		}
	}
}

func bindAnnounceInsecureOpts(dataDir, listen, grpcListen string) serveOptions {
	return serveOptions{
		insecure: true, listen: listen, grpcListen: grpcListen, dataDir: dataDir,
		engine: "sqlite", checkpointInterval: 0,
	}
}

// writeBindAnnounceHITLConfig provisions the HITL receiver — one present auxiliary
// listener — on addr. The signing secret is a fixture value, not a credential.
func writeBindAnnounceHITLConfig(t *testing.T, addr string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"listen": addr,
		"providers": []map[string]any{{
			"name": "bind-announce", "kind": "webhook", "signing_secret": "bind-announce-fixture-not-a-credential",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hitl.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireNoAnnouncementEffects is the shared refusal oracle.
func requireNoAnnouncementEffects(t *testing.T, what string, res bindAnnounceResult, out *bindAnnounceBuffer, dataDir string) {
	t.Helper()
	if res.announced != 0 {
		t.Errorf("%s: the announcement ran %d time(s) although this invocation could not acquire every serve listener", what, res.announced)
	}
	if bindAnnounceTokenExists(t, dataDir) {
		t.Errorf("%s: setup.token was minted by an invocation that refused before serving", what)
	}
	got := out.String()
	if bindAnnounceTokenRE.MatchString(got) || strings.Contains(got, "FIRST-BOOT SETUP") {
		t.Errorf("%s: a first-boot banner or token-shaped value reached the output (content withheld)", what)
	}
	if res.answered {
		t.Errorf("%s: the main HTTP listener answered a request during a refused invocation", what)
	}
}

// TestServeBindAnnounceOccupiedListenerRefusesBeforeAnnouncement: an ordinary port
// conflict on the main HTTP, the gRPC, or a present auxiliary listener refuses the
// invocation before the announcement, mints nothing, and leaves every address free.
func TestServeBindAnnounceOccupiedListenerRefusesBeforeAnnouncement(t *testing.T) {
	for _, target := range []string{"http", "grpc", "auxiliary-hitl"} {
		t.Run(target, func(t *testing.T) {
			dataDir := t.TempDir()
			listen, grpcListen := bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t)
			occupier, occupied := bindAnnounceOccupy(t)
			switch target {
			case "http":
				listen = occupied
			case "grpc":
				grpcListen = occupied
			case "auxiliary-hitl":
				t.Setenv("OLIVARES_HITL_CONFIG", writeBindAnnounceHITLConfig(t, occupied))
			}
			opts := bindAnnounceInsecureOpts(dataDir, listen, grpcListen)
			out := &bindAnnounceBuffer{}
			res := runBindAnnounce(t, opts, out, serveAnnounce(true), nil)

			if res.err == nil {
				t.Fatalf("%s occupied: runEngine returned nil", target)
			}
			if !errors.Is(res.err, syscall.EADDRINUSE) {
				t.Errorf("%s occupied: the returned error does not carry EADDRINUSE through errors.Is: %v", target, res.err)
			}
			requireNoAnnouncementEffects(t, target+" occupied", res, out, dataDir)
			_ = occupier.Close()
			requireBindAnnounceRebindable(t, target+" occupied", listen, grpcListen, occupied)
		})
	}
}

// TestServeBindAnnouncePrepareFailureMintsNoToken: TLS material and auxiliary
// configuration are prepared before the announcement, so their failure mints nothing.
func TestServeBindAnnouncePrepareFailureMintsNoToken(t *testing.T) {
	t.Run("tls-material", func(t *testing.T) {
		dataDir := t.TempDir()
		material := t.TempDir()
		cert, key := filepath.Join(material, "tls.crt"), filepath.Join(material, "tls.key")
		for _, p := range []string{cert, key} {
			if err := os.WriteFile(p, []byte("not PEM material\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		opts := serveOptions{
			listen: bindAnnounceFreeAddr(t), grpcListen: bindAnnounceFreeAddr(t), dataDir: dataDir,
			engine: "sqlite", tlsCert: cert, tlsKey: key,
		}
		out := &bindAnnounceBuffer{}
		res := runBindAnnounce(t, opts, out, serveAnnounce(false), nil)
		if res.err == nil {
			t.Fatal("unusable TLS material: runEngine returned nil")
		}
		requireNoAnnouncementEffects(t, "unusable TLS material", res, out, dataDir)
		requireBindAnnounceRebindable(t, "unusable TLS material", opts.listen, opts.grpcListen)
	})
	t.Run("auxiliary-config", func(t *testing.T) {
		dataDir := t.TempDir()
		bad := filepath.Join(t.TempDir(), "hitl.json")
		if err := os.WriteFile(bad, []byte("{ not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OLIVARES_HITL_CONFIG", bad)
		opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
		out := &bindAnnounceBuffer{}
		res := runBindAnnounce(t, opts, out, serveAnnounce(true), nil)
		if res.err == nil {
			t.Fatal("invalid auxiliary config: runEngine returned nil")
		}
		requireNoAnnouncementEffects(t, "invalid auxiliary config", res, out, dataDir)
		requireBindAnnounceRebindable(t, "invalid auxiliary config", opts.listen, opts.grpcListen)
	})
}

// TestServeBindAnnounceListenersBoundBeforeAnnouncement: inside the announce callback
// the main HTTP and gRPC addresses already accept a TCP connection (bound), a request
// written there receives no response while the callback runs (Serve not launched),
// and the same held connection is answered once the callback has returned.
func TestServeBindAnnounceListenersBoundBeforeAnnouncement(t *testing.T) {
	dataDir := t.TempDir()
	opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
	out := &bindAnnounceBuffer{}

	var (
		held         net.Conn
		httpBound    bool
		grpcBound    bool
		earlyReply   bool
		earlyReadErr error
	)
	t.Cleanup(func() {
		if held != nil {
			_ = held.Close()
		}
	})
	announce := func(run *bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error {
		return func(ctx context.Context, w io.Writer, eng *engine, addr consoleAddress) error {
			run.announced.Add(1)
			if c, err := net.DialTimeout("tcp", opts.grpcListen, 2*time.Second); err == nil {
				grpcBound = true
				_ = c.Close()
			}
			c, err := net.DialTimeout("tcp", opts.listen, 2*time.Second)
			if err == nil {
				httpBound = true
				held = c
				_, _ = io.WriteString(c, "GET /healthz HTTP/1.1\r\nHost: bind-announce\r\nConnection: close\r\n\r\n")
				_ = c.SetReadDeadline(time.Now().Add(bindAnnounceProbeWait))
				n, rerr := c.Read(make([]byte, 1))
				earlyReply, earlyReadErr = n > 0, rerr
				_ = c.SetReadDeadline(time.Time{})
			}
			return announceSetup(ctx, w, eng, addr, true)
		}
	}
	res := runBindAnnounce(t, opts, out, announce, nil)

	if !httpBound {
		t.Error("the main HTTP address refused a connection inside the announcement: it was not bound before announcing")
	}
	if !grpcBound {
		t.Error("the gRPC address refused a connection inside the announcement: it was not bound before announcing")
	}
	if earlyReply {
		t.Error("a request on the main HTTP listener was answered while the announcement was still running")
	}
	var ne net.Error
	if httpBound && !earlyReply && !(errors.As(earlyReadErr, &ne) && ne.Timeout()) {
		t.Errorf("the in-announcement read ended with %v instead of its own deadline; the no-response observation is incomplete", earlyReadErr)
	}
	if res.err != nil {
		t.Errorf("a canceled, successfully launched run returned %v", res.err)
	}
	if res.announced != 1 || !res.answered {
		t.Errorf("announced=%d answered=%v, want 1/true", res.announced, res.answered)
	}
	if !strings.Contains(out.String(), "FIRST-BOOT SETUP") || !bindAnnounceTokenExists(t, dataDir) {
		t.Error("the successful run did not print the first-boot banner or persist its token hash")
	}
	if held != nil {
		_ = held.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := readBindAnnounceStatusLine(held)
		if err != nil || !strings.HasPrefix(line, "HTTP/1.1 ") {
			t.Errorf("the connection queued during the announcement was not answered after launch: line=%q err=%v", line, err)
		}
	}
	requireBindAnnounceRebindable(t, "successful run", opts.listen, opts.grpcListen)
}

func readBindAnnounceStatusLine(c net.Conn) (string, error) {
	var b strings.Builder
	one := make([]byte, 1)
	for b.Len() < 64 {
		n, err := c.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			b.WriteByte(one[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
	return strings.TrimRight(b.String(), "\r"), nil
}

// TestServeBindAnnounceRetryAfterRefusalMintsTheFirstToken: a refused invocation
// leaves the data directory without a token, so the next invocation in the SAME
// directory mints the first one, and that token completes setup (useful operation
// after recovery, not a banner assertion).
func TestServeBindAnnounceRetryAfterRefusalMintsTheFirstToken(t *testing.T) {
	dataDir := t.TempDir()
	occupier, occupied := bindAnnounceOccupy(t)
	grpcListen := bindAnnounceFreeAddr(t)

	first := bindAnnounceInsecureOpts(dataDir, occupied, grpcListen)
	out1 := &bindAnnounceBuffer{}
	res1 := runBindAnnounce(t, first, out1, serveAnnounce(true), nil)
	if res1.err == nil {
		t.Fatal("first invocation on an occupied port returned nil")
	}
	requireNoAnnouncementEffects(t, "first invocation", res1, out1, dataDir)
	_ = occupier.Close()

	second := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
	out2 := &bindAnnounceBuffer{}
	var setupStatus int
	var setupErr error
	res2 := runBindAnnounce(t, second, out2, serveAnnounce(true), func(base string) {
		token := bindAnnounceTokenRE.FindString(out2.String())
		if token == "" {
			setupErr = errors.New("no token-shaped value in the retry banner")
			return
		}
		body, _ := json.Marshal(map[string]string{
			"token": token, "email": "bind-announce@example.test", "password": "bind-announce-retry-password-42",
		})
		client, tr := bindAnnounceClient(10 * time.Second)
		defer tr.CloseIdleConnections()
		resp, err := client.Post(base+"/v1/setup", "application/json", bytes.NewReader(body))
		if err != nil {
			setupErr = fmt.Errorf("POST /v1/setup: %w", err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		setupStatus = resp.StatusCode
	})
	if res2.err != nil {
		t.Fatalf("retry invocation returned %v", res2.err)
	}
	banner := out2.String()
	if !strings.Contains(banner, "FIRST-BOOT SETUP") || strings.Contains(banner, "SETUP STILL PENDING") {
		t.Errorf("the retry did not mint the FIRST token (first-boot=%v pending=%v)",
			strings.Contains(banner, "FIRST-BOOT SETUP"), strings.Contains(banner, "SETUP STILL PENDING"))
	}
	if !res2.answered {
		t.Fatal("the retry never served")
	}
	if setupErr != nil || setupStatus != http.StatusCreated {
		t.Errorf("the retry's token did not complete setup: status=%d err=%v", setupStatus, setupErr)
	}
	requireBindAnnounceRebindable(t, "retry", occupied, grpcListen, second.listen, second.grpcListen)
}

// bindAnnounceFailingWriter rejects every write.
type bindAnnounceFailingWriter struct{}

func (bindAnnounceFailingWriter) Write([]byte) (int, error) { return 0, errBindAnnounceFixture }

// bindAnnounceShortWriter accepts all but the last byte of every write, with no error.
type bindAnnounceShortWriter struct{}

func (bindAnnounceShortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

// TestServeBindAnnounceOutputFailurePreservesToken: the real callbacks ignore their
// fmt.Fprintf results, so a rejected or short write must still stop the launch. The
// persisted hash stays (some bytes may have reached the operator), and the next boot
// prints the pending-setup recovery, not a new token.
func TestServeBindAnnounceOutputFailurePreservesToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  io.Writer
		want error
	}{
		{"write-error", bindAnnounceFailingWriter{}, errBindAnnounceFixture},
		{"short-write", bindAnnounceShortWriter{}, io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
			res := runBindAnnounce(t, opts, tc.out, serveAnnounce(true), nil)
			if res.err == nil || !errors.Is(res.err, tc.want) {
				t.Errorf("%s: runEngine returned %v, want an error carrying %v", tc.name, res.err, tc.want)
			}
			if res.answered {
				t.Errorf("%s: the engine served although its announcement output was not accepted", tc.name)
			}
			if !bindAnnounceTokenExists(t, dataDir) {
				t.Errorf("%s: the persisted setup token hash was removed after an unobserved delivery", tc.name)
			}
			requireBindAnnounceRebindable(t, tc.name, opts.listen, opts.grpcListen)

			next := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
			out := &bindAnnounceBuffer{}
			res2 := runBindAnnounce(t, next, out, serveAnnounce(true), nil)
			if res2.err != nil {
				t.Errorf("%s: next boot returned %v", tc.name, res2.err)
			}
			got := out.String()
			if !strings.Contains(got, "SETUP STILL PENDING") || bindAnnounceTokenRE.MatchString(got) {
				t.Errorf("%s: next boot did not print pending-setup recovery without a token (pending=%v token-shaped=%v)",
					tc.name, strings.Contains(got, "SETUP STILL PENDING"), bindAnnounceTokenRE.MatchString(got))
			}
			if !bindAnnounceTokenExists(t, dataDir) {
				t.Errorf("%s: the next boot removed the existing setup token", tc.name)
			}
			requireBindAnnounceRebindable(t, tc.name+" next boot", next.listen, next.grpcListen)
		})
	}
}

// TestServeBindAnnounceCallbackErrorUnwinds: a callback error is returned and nothing
// launches or stays bound.
func TestServeBindAnnounceCallbackErrorUnwinds(t *testing.T) {
	dataDir := t.TempDir()
	opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
	announce := func(run *bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error {
		return func(context.Context, io.Writer, *engine, consoleAddress) error {
			run.announced.Add(1)
			return errBindAnnounceFixture
		}
	}
	res := runBindAnnounce(t, opts, &bindAnnounceBuffer{}, announce, nil)
	if !errors.Is(res.err, errBindAnnounceFixture) {
		t.Errorf("runEngine returned %v, want the callback's error", res.err)
	}
	if res.answered {
		t.Error("the engine served after its announce callback failed")
	}
	requireBindAnnounceRebindable(t, "callback error", opts.listen, opts.grpcListen)
}

// TestServeBindAnnounceCancellationAfterCallbackDoesNotLaunch: a callback that cancels
// the context and returns nil must not launch any Serve.
func TestServeBindAnnounceCancellationAfterCallbackDoesNotLaunch(t *testing.T) {
	dataDir := t.TempDir()
	opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
	out := &bindAnnounceBuffer{}
	announce := func(run *bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error {
		return func(ctx context.Context, w io.Writer, eng *engine, addr consoleAddress) error {
			run.announced.Add(1)
			err := announceSetup(ctx, w, eng, addr, true)
			run.cancel()
			return err
		}
	}
	res := runBindAnnounce(t, opts, out, announce, nil)
	if res.err == nil || !errors.Is(res.err, context.Canceled) {
		t.Errorf("runEngine returned %v, want the observed cancellation", res.err)
	}
	if res.answered {
		t.Error("the engine served after cancellation was observed at the end of the announcement")
	}
	if !bindAnnounceTokenExists(t, dataDir) {
		t.Error("the announced token hash was removed")
	}
	requireBindAnnounceRebindable(t, "post-announce cancellation", opts.listen, opts.grpcListen)
}
