// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// B1 seam tests for serve_listeners.go: the close-once authority, syscall.Conn
// preservation, acquisition unwind, cancellation at the final bind, the empty HTTP
// address rule, reuse-port sharing, the observed announcement writer, and the
// entry-deferred close of an HTTP adapter whose ServeTLS fails before net/http tracks
// its listener while the unchanged DS1 gRPC drain is still blocked. All sockets are
// real loopback sockets; unit fakes are named as fakes where they are used.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/serverhandover"
)

var (
	errServeListenerClose = errors.New("serve-listeners: fixture close failure")
	errServeListenerBind  = errors.New("serve-listeners: fixture bind failure")
)

func serveListenersLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeServeListener is a unit fake: it has no socket and no syscall.Conn.
type fakeServeListener struct {
	closes   atomic.Int32
	closeErr error
}

func (f *fakeServeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (f *fakeServeListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (f *fakeServeListener) Close() error {
	f.closes.Add(1)
	return f.closeErr
}

// recordingServeListener wraps a REAL listener and records each raw Close.
type recordingServeListener struct {
	net.Listener
	id       int
	closes   atomic.Int32
	closeErr error
	order    *[]int
	mu       *sync.Mutex
}

func (l *recordingServeListener) Close() error {
	l.closes.Add(1)
	if l.order != nil {
		l.mu.Lock()
		*l.order = append(*l.order, l.id)
		l.mu.Unlock()
	}
	err := l.Listener.Close()
	if l.closeErr != nil {
		return l.closeErr
	}
	return err
}

func serveListenersDialRefused(t *testing.T, addr string) bool {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		_ = c.Close()
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

func TestServeListenerCloseOnceRetainsFirstResult(t *testing.T) {
	fake := &fakeServeListener{closeErr: errServeListenerClose}
	lis := newCloseOnceListener(fake)
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = lis.Close()
		}(i)
	}
	wg.Wait()
	if n := fake.closes.Load(); n != 1 {
		t.Fatalf("the underlying Close ran %d times under concurrent calls, want exactly 1", n)
	}
	for i, err := range errs {
		if !errors.Is(err, errServeListenerClose) {
			t.Errorf("call %d returned %v, want the retained first close result", i, err)
		}
	}

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingServeListener{Listener: raw}
	real := newCloseOnceListener(rec)
	addr := real.Addr().String()
	if err := real.Close(); err != nil {
		t.Fatalf("first close of a real listener: %v", err)
	}
	if !serveListenersDialRefused(t, addr) {
		t.Fatalf("%s still admits after the close authority ran", addr)
	}
	if err := real.Close(); err != nil {
		t.Errorf("a repeated close returned %v instead of the retained nil", err)
	}
	if n := rec.closes.Load(); n != 1 {
		t.Errorf("the real listener was closed %d times, want 1", n)
	}
}

func TestServeListenerWrapperPreservesSyscallConn(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lis := newCloseOnceListener(raw)
	t.Cleanup(func() { _ = lis.Close() })
	sc, ok := lis.(syscall.Conn)
	if !ok {
		t.Fatal("the owned wrapper of a *net.TCPListener hides syscall.Conn, so gRPC channelz loses its socket options")
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var fdSeen bool
	if err := rc.Control(func(fd uintptr) { fdSeen = fd > 0 }); err != nil || !fdSeen {
		t.Fatalf("RawConn.Control on the wrapper did not reach the socket: err=%v fd=%v", err, fdSeen)
	}
	if lis.Addr().String() != raw.Addr().String() {
		t.Errorf("Addr changed through the wrapper: %s vs %s", lis.Addr(), raw.Addr())
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := lis.Accept()
		accepted <- c
	}()
	client, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	select {
	case c := <-accepted:
		if _, isTCP := c.(*net.TCPConn); !isTCP {
			t.Errorf("Accept returned %T through the wrapper, want the original *net.TCPConn", c)
		}
		if c != nil {
			_ = c.Close()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept through the wrapper did not return")
	}

	if _, ok := newCloseOnceListener(&fakeServeListener{}).(syscall.Conn); ok {
		t.Error("the wrapper claims syscall.Conn for a listener that does not supply it")
	}
}

func TestServeListenerAcquisitionUnwindsInReverse(t *testing.T) {
	log := serveListenersLogger()
	t.Run("bind failure", func(t *testing.T) {
		var (
			mu    sync.Mutex
			order []int
			addrs []string
			calls int
		)
		bind := func(ctx context.Context, addr string, reuse bool) (net.Listener, error) {
			calls++
			if calls == 3 {
				return nil, errServeListenerBind
			}
			raw, err := bindServeListener(ctx, addr, reuse)
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, raw.Addr().String())
			rec := &recordingServeListener{Listener: raw, id: calls, order: &order, mu: &mu}
			if calls == 2 {
				rec.closeErr = errServeListenerClose
			}
			return rec, nil
		}
		specs := []serveListenerSpec{{addr: "127.0.0.1:0", http: true}, {addr: "127.0.0.1:0"}, {addr: "127.0.0.1:0", http: true}}
		owned, err := acquireServeListeners(context.Background(), bind, specs, false, log)
		if owned != nil {
			t.Error("a failed acquisition returned a listener set")
		}
		if !errors.Is(err, errServeListenerBind) || !errors.Is(err, errServeListenerClose) {
			t.Fatalf("got %v, want the bind cause and the joined close failure", err)
		}
		if msg := err.Error(); strings.Index(msg, errServeListenerBind.Error()) > strings.Index(msg, errServeListenerClose.Error()) {
			t.Errorf("the close failure precedes the original cause: %q", msg)
		}
		mu.Lock()
		got := append([]int(nil), order...)
		mu.Unlock()
		if len(got) != 2 || got[0] != 2 || got[1] != 1 {
			t.Errorf("unwind close order %v, want [2 1]", got)
		}
		requireBindAnnounceRebindable(t, "bind-failure unwind", addrs...)
	})
	t.Run("canceled before the first bind", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		bind := func(context.Context, string, bool) (net.Listener, error) { calls++; return nil, errServeListenerBind }
		_, err := acquireServeListeners(ctx, bind, []serveListenerSpec{{addr: "127.0.0.1:0"}}, false, log)
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Errorf("err=%v binds=%d, want context.Canceled and no bind", err, calls)
		}
	})
	t.Run("canceled between binds", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var first *recordingServeListener
		calls := 0
		bind := func(ctx context.Context, addr string, reuse bool) (net.Listener, error) {
			calls++
			raw, err := bindServeListener(ctx, addr, reuse)
			if err != nil {
				return nil, err
			}
			first = &recordingServeListener{Listener: raw}
			cancel()
			return first, nil
		}
		_, err := acquireServeListeners(ctx, bind, []serveListenerSpec{{addr: "127.0.0.1:0"}, {addr: "127.0.0.1:0"}}, false, log)
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("err=%v binds=%d, want context.Canceled after exactly one bind", err, calls)
		}
		if first.closes.Load() != 1 {
			t.Errorf("the acquired listener was closed %d times by the unwind, want 1", first.closes.Load())
		}
		requireBindAnnounceRebindable(t, "canceled between binds", first.Addr().String())
	})
}

// TestServeBindAnnounceCancellationAtFinalBind cancels during the final real bind of
// an actual runEngine: the post-acquisition check must refuse the announcement.
func TestServeBindAnnounceCancellationAtFinalBind(t *testing.T) {
	dataDir := t.TempDir()
	opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
	var cancelRun context.CancelFunc
	var binds int
	opts.bindListener = func(ctx context.Context, addr string, reuse bool) (net.Listener, error) {
		binds++
		if binds == 2 { // the final present listener: HTTP, then gRPC
			cancelRun()
		}
		return bindServeListener(ctx, addr, reuse)
	}
	out := &bindAnnounceBuffer{}
	announce := func(run *bindAnnounceRun) func(context.Context, io.Writer, *engine, consoleAddress) error {
		cancelRun = run.cancel
		return serveAnnounce(true)(run)
	}
	res := runBindAnnounce(t, opts, out, announce, nil)
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("runEngine returned %v, want the cancellation observed after the final bind", res.err)
	}
	if binds != 2 {
		t.Errorf("%d binds ran, want 2", binds)
	}
	requireNoAnnouncementEffects(t, "cancellation at final bind", res, out, dataDir)
	requireBindAnnounceRebindable(t, "cancellation at final bind", opts.listen, opts.grpcListen)
}

func TestServeListenerEmptyHTTPAddressCompatibility(t *testing.T) {
	for _, tc := range []struct {
		addr            string
		insecure, reuse bool
		want            string
	}{
		{"", true, false, ":http"},   // ListenAndServe
		{"", false, false, ":https"}, // ListenAndServeTLS
		{"", true, true, ""},         // reuse-port always passed Addr straight through
		{"", false, true, ""},
		{"127.0.0.1:8443", false, false, "127.0.0.1:8443"},
		{":8443", true, true, ":8443"},
	} {
		if got := httpBindAddr(tc.addr, tc.insecure, tc.reuse); got != tc.want {
			t.Errorf("httpBindAddr(%q, insecure=%v, reuse=%v) = %q, want %q", tc.addr, tc.insecure, tc.reuse, got, tc.want)
		}
	}
	// The spec address reaches the bind verbatim; no generic normalization happens there.
	var seen []string
	bind := func(ctx context.Context, addr string, reuse bool) (net.Listener, error) {
		seen = append(seen, addr)
		return bindServeListener(ctx, "127.0.0.1:0", reuse)
	}
	owned, err := acquireServeListeners(context.Background(), bind,
		[]serveListenerSpec{{addr: httpBindAddr("", false, false), http: true}, {addr: ""}}, false, serveListenersLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.closeAll(); err != nil {
		t.Errorf("closeAll: %v", err)
	}
	if len(seen) != 2 || seen[0] != ":https" || seen[1] != "" {
		t.Errorf("bind saw %q, want [\":https\" \"\"]", seen)
	}
}

func TestServeListenerReusePortSharingAndPlainOccupant(t *testing.T) {
	if !serverhandover.Supported() {
		t.Skip("SO_REUSEPORT is unsupported on this platform; the plain-listener fallback is covered elsewhere")
	}
	log := serveListenersLogger()

	sharer, err := serverhandover.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sharer.Close() }()
	owned, err := acquireServeListeners(context.Background(), bindServeListener,
		[]serveListenerSpec{{addr: sharer.Addr().String(), http: true}, {addr: sharer.Addr().String()}}, true, log)
	if err != nil {
		t.Fatalf("reuse-port acquisition refused an address shared by another SO_REUSEPORT holder: %v", err)
	}
	if err := owned.closeAll(); err != nil {
		t.Errorf("closeAll: %v", err)
	}

	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close() }()
	_, err = acquireServeListeners(context.Background(), bindServeListener,
		[]serveListenerSpec{{addr: plain.Addr().String(), http: true}}, true, log)
	if !errors.Is(err, syscall.EADDRINUSE) || !strings.Contains(err.Error(), "reuse-port listen "+plain.Addr().String()) {
		t.Errorf("a plain occupant under reuse-port: got %v, want EADDRINUSE with the reuse-port wrap", err)
	}
	_, err = acquireServeListeners(context.Background(), bindServeListener,
		[]serveListenerSpec{{addr: plain.Addr().String()}}, false, log)
	if !errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "reuse-port") {
		t.Errorf("a plain occupant on the ordinary path: got %v, want bare EADDRINUSE", err)
	}
}

func TestServeListenerAnnouncementObserver(t *testing.T) {
	short := &announceOutput{w: bindAnnounceShortWriter{}}
	if _, err := short.Write([]byte("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short write returned %v", err)
	}
	var sink strings.Builder
	failing := &announceOutput{w: &sink}
	failing.err = errBindAnnounceFixture
	if n, err := failing.Write([]byte("later")); n != 0 || !errors.Is(err, errBindAnnounceFixture) || sink.Len() != 0 {
		t.Errorf("a write after the first failure forwarded bytes or lost the retained error: n=%d err=%v", n, err)
	}
	err := runAnnouncement(context.Background(), bindAnnounceFailingWriter{}, nil, consoleAddress{},
		func(_ context.Context, w io.Writer, _ *engine, _ consoleAddress) error {
			_, _ = io.WriteString(w, "banner")
			return errServeListenerBind
		})
	if !errors.Is(err, errServeListenerBind) || !errors.Is(err, errBindAnnounceFixture) {
		t.Errorf("runAnnouncement returned %v, want both the callback and the output failure", err)
	}
	if err := withCloseErrors(nil, errServeListenerClose); !errors.Is(err, errServeListenerClose) {
		t.Errorf("nil primary with a real close failure returned %v, want an error", err)
	}
}

// serveListenersTLSServer returns an http.Server configured through the production
// configureHTTPServerTLS with a real self-signed certificate.
func serveListenersTLSServer(t *testing.T, cfg *tls.Config) *http.Server {
	t.Helper()
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := secure.EnsureTLSCert(cert, key); err != nil {
		t.Fatal(err)
	}
	loader, err := secure.NewCertificateLoader(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler(), ReadHeaderTimeout: 5 * time.Second, TLSConfig: cfg}
	if err := configureHTTPServerTLS(srv, loader); err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestServeListenerTLSAndHTTP2OverAcquiredListener(t *testing.T) {
	log := serveListenersLogger()
	srv := serveListenersTLSServer(t, nil)
	owned, err := acquireServeListeners(context.Background(), bindServeListener, []serveListenerSpec{{addr: srv.Addr, http: true}}, false, log)
	if err != nil {
		t.Fatal(err)
	}
	addr := owned.listeners[0].Addr().String()
	errCh := make(chan error, 1)
	go serveHTTP(srv, owned.listeners[0], false, log, errCh)

	for _, tc := range []struct {
		name  string
		major int
		tr    *http.Transport
	}{
		{"h2", 2, &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},                                                   //nolint:gosec // loopback fixture
		{"http1.1", 1, &http.Transport{TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}, //nolint:gosec // loopback fixture
	} {
		resp, err := (&http.Client{Transport: tc.tr, Timeout: 10 * time.Second}).Get("https://" + addr + "/")
		if err != nil {
			t.Fatalf("%s over the acquired TLS listener: %v", tc.name, err)
		}
		_ = resp.Body.Close()
		tc.tr.CloseIdleConnections()
		if resp.ProtoMajor != tc.major || resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: proto=%s status=%d", tc.name, resp.Proto, resp.StatusCode)
		}
	}
	_ = srv.Close()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serveHTTP returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveHTTP did not return after Close")
	}
	if err := owned.closeAll(); err != nil {
		t.Errorf("owner fallback after the library close: %v", err)
	}
	if !serveListenersDialRefused(t, addr) {
		t.Errorf("%s still admits after Close", addr)
	}
}

// TestServeListenerEarlyServeTLSFailureClosesWhileGRPCDrainBlocked is the F2 case: the
// HTTP adapter's ServeTLS fails in its HTTP/2 setup (an adapter case built with the
// library's rejected cipher configuration, not a demonstrated production flag), while
// a real gRPC health Watch stream holds the unchanged DS1 GracefulStop. The failed
// HTTP listener must be closed while that drain is still blocked.
func TestServeListenerEarlyServeTLSFailureClosesWhileGRPCDrainBlocked(t *testing.T) {
	log := serveListenersLogger()
	httpSrv := serveListenersTLSServer(t, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384},
	})
	grpcSrv := grpc.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, health.NewServer())

	var raws []*recordingServeListener
	bind := func(ctx context.Context, addr string, reuse bool) (net.Listener, error) {
		raw, err := bindServeListener(ctx, addr, reuse)
		if err != nil {
			return nil, err
		}
		rec := &recordingServeListener{Listener: raw}
		raws = append(raws, rec)
		return rec, nil
	}
	owned, err := acquireServeListeners(context.Background(), bind,
		[]serveListenerSpec{{addr: httpSrv.Addr, http: true}, {addr: "127.0.0.1:0"}}, false, log)
	if err != nil {
		t.Fatal(err)
	}
	httpAddr, grpcAddr := raws[0].Addr().String(), raws[1].Addr().String()

	errCh := make(chan error, 2)
	go serveGRPC(grpcSrv, owned.listeners[1], errCh)

	conn, err := grpc.NewClient("passthrough:///"+grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	streamCtx, releaseStream := context.WithCancel(context.Background())
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(releaseStream) }
	t.Cleanup(func() {
		release()
		_ = conn.Close()
		grpcSrv.Stop()
		_ = owned.closeAll()
	})
	stream, err := healthpb.NewHealthClient(conn).Watch(streamCtx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("the gRPC Watch stream did not become active: %v", err)
	}

	go serveHTTP(httpSrv, owned.listeners[0], false, log, errCh)

	fixtureCtx, cancelFixture := context.WithCancel(context.Background())
	defer cancelFixture()
	done := make(chan error, 1)
	go func() {
		done <- waitAndShutdown(fixtureCtx, httpSrv, grpcSrv, nil, nil, nil, nil, nil, nil, nil, errCh, log)
	}()

	// Causal barrier: the gRPC listener is closed only by GracefulStop, so its refusal
	// proves DS1 has passed HTTP Shutdown and entered the gRPC drain.
	deadline := time.Now().Add(30 * time.Second)
	for !serveListenersDialRefused(t, grpcAddr) {
		if time.Now().After(deadline) {
			t.Fatal("UNRESOLVED: DS1 never reached GracefulStop")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Observed before any Close call from this test: the raw HTTP listener was already
	// closed exactly once, and a fresh dial is refused.
	if n := raws[0].closes.Load(); n != 1 {
		t.Errorf("the failed HTTP listener was closed %d times before the gRPC drain finished, want 1", n)
	}
	if !serveListenersDialRefused(t, httpAddr) {
		t.Errorf("the HTTP listener whose ServeTLS failed still admits while the gRPC drain is blocked")
	}
	select {
	case err := <-done:
		t.Fatalf("DS1 returned (%v) while the gRPC stream was still held, so the blocked-drain premise did not hold", err)
	default:
	}

	release()
	var ret error
	select {
	case ret = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("UNRESOLVED: DS1 did not return after the gRPC stream was released")
	}
	if ret == nil || !strings.Contains(ret.Error(), "HTTP/2-required") {
		t.Errorf("DS1 returned %v, want the retained ServeTLS setup failure", ret)
	}
	if err := withCloseErrors(ret, owned.closeAll()); err != ret {
		t.Errorf("owner fallback added a close failure: %v", err)
	}
	for i, rec := range raws {
		if n := rec.closes.Load(); n != 1 {
			t.Errorf("listener %d was closed %d times in total, want 1", i, n)
		}
	}
}
