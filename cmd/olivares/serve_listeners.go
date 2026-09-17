// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"syscall"

	"google.golang.org/grpc"

	"github.com/olivaresai/olivares/core/serverhandover"
)

// B1 (ROOT-CONSTRUCTION-B1): runEngine acquires every present serve-family listener
// BEFORE the first-account announcement, and launches no Serve until that
// announcement was accepted by its writer. What this file does NOT establish, stated
// because a raw bind reads like more than it is: a successful bind is not readiness —
// ServeTLS still performs HTTP/2 setup after it, Serve may still fail, and nothing
// here joins Serve goroutines, bounds the gRPC drain or serializes a later
// cancellation with launch. Those remain separately named L2 obligations.

// serveListenerBind acquires one serve-family socket. Production uses
// bindServeListener; a focused test may wrap it through serveOptions.bindListener. It
// is not a flag and selects no alternate transport.
type serveListenerBind func(ctx context.Context, addr string, reusePort bool) (net.Listener, error)

// bindServeListener is the real acquisition. The reuse-port path keeps the
// context.Background it always used: a pre-bind context check does not make name
// resolution cancelable, and this does not pretend otherwise.
func bindServeListener(_ context.Context, addr string, reusePort bool) (net.Listener, error) {
	if reusePort {
		return serverhandover.Listen(context.Background(), "tcp", addr)
	}
	return net.Listen("tcp", addr)
}

// serveListenerSpec is one listener to acquire, in launch order.
type serveListenerSpec struct {
	addr string
	// http marks an HTTP listener: it keeps the reuse-port error wrap and the
	// unsupported-platform warning serveHTTP used to emit. gRPC never had either.
	http bool
}

// httpBindAddr is the address an HTTP listener binds. The ordinary path keeps what
// net/http's ListenAndServe (":http") and ListenAndServeTLS (":https") did with an
// empty Addr; the reuse-port path always passed the address straight through, and
// still does. A direct net.Listen("tcp", "") would silently pick a random port.
func httpBindAddr(addr string, insecure, reusePort bool) string {
	if addr != "" || reusePort {
		return addr
	}
	if insecure {
		return ":http"
	}
	return ":https"
}

// plaintextBindRefusal is the choke point for plaintext exposure. insecureBindGuard
// reads --listen and --grpc-listen, but the auxiliary listeners take their addresses
// from operator config files (agentGatewayConfig.Listen and friends) and are served
// with the same global --insecure switch — so loopback primaries let that guard pass
// while an auxiliary socket served plain HTTP off-host (found by the Codex contrast
// of 2026-08-06, F-01). Adding one more address to the guard's arguments would fix
// today's listeners and miss the next one someone adds.
//
// runEngine calls this for every HTTP listener it starts — the primary server and
// every present auxiliary — BEFORE any socket is acquired, because refusing after
// binding is not refusing.
//
// HONEST BOUND, stated precisely because a looser version of this sentence was
// wrong twice. This is NOT "every plaintext listener in the process":
//   - the gRPC listener does not pass through here. It is covered, but by the
//     FLAG-level guard on --grpc-listen, which is the mechanism the paragraph above
//     argues against relying on alone.
//   - in-process source connectors open their own servers entirely outside both
//     guards. THREE of them carry no loopback refusal and no allow_public_bind
//     opt-in: github and gitlab call ListenAndServe directly
//     (connectors/{github,gitlab}/gather.go) with WILDCARD defaults :9800/:9801,
//     and connectors/tak serves plaintext CoT over TCP/UDP with 0.0.0.0 examples
//     in its own field docs. --insecure governs none of them.
//
// Those bypasses predate this guard and are reported separately; do not read the
// line above as covering them.
func plaintextBindRefusal(addr string, insecure, allowPublicBind bool) error {
	if insecure && !allowPublicBind && !hostIsLoopback(addr) {
		return fmt.Errorf("refusing to serve PLAINTEXT on %q, which is reachable off-host: with --insecure there is no TLS, so this listener's traffic (bearer tokens, governed decisions, the first-boot setup token) would cross the network in the clear. Bind it to loopback, drop --insecure, or — only if something in front of the engine terminates TLS — declare it with --insecure-allow-public-bind", addr)
	}
	return nil
}

// closeOnceListener is the single close authority for one acquired listener. The
// library's own close (http.Server tracks it, tls.Listener and gRPC's listenSocket
// delegate to it), the adapter's entry cleanup and the owner's unwind all call this
// Close, and every call returns the first result. Accept and Addr are the underlying
// listener's own, so accepted connections are unchanged.
type closeOnceListener struct {
	net.Listener
	once     sync.Once
	closeErr error
}

func (l *closeOnceListener) Close() error {
	l.once.Do(func() { l.closeErr = l.Listener.Close() })
	return l.closeErr
}

// syscallCloseOnceListener keeps syscall.Conn visible when the backing listener has
// it: gRPC's Serve reads socket options for channelz through that assertion, and
// embedding only net.Listener would hide it.
type syscallCloseOnceListener struct {
	*closeOnceListener
	raw syscall.Conn
}

func (l syscallCloseOnceListener) SyscallConn() (syscall.RawConn, error) {
	return l.raw.SyscallConn()
}

func newCloseOnceListener(lis net.Listener) net.Listener {
	owned := &closeOnceListener{Listener: lis}
	if raw, ok := lis.(syscall.Conn); ok {
		return syscallCloseOnceListener{closeOnceListener: owned, raw: raw}
	}
	return owned
}

// ownedServeListeners is the acquired set, in acquisition order.
type ownedServeListeners struct {
	listeners []net.Listener
}

// closeAll closes the set in reverse acquisition order through each close authority
// and joins the meaningful failures. A listener a library already closed returns its
// retained first result. A returned error is reported, never read as proof of
// closure.
func (o *ownedServeListeners) closeAll() error {
	var errs []error
	for i := len(o.listeners) - 1; i >= 0; i-- {
		lis := o.listeners[i]
		if err := lis.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close serve listener %s: %w", lis.Addr(), err))
		}
	}
	return errors.Join(errs...)
}

// acquireServeListeners synchronously acquires every spec in order. Each success
// registers its close authority at once; the first failure unwinds what was acquired
// and returns the original cause first, with close failures joined after it.
func acquireServeListeners(ctx context.Context, bind serveListenerBind, specs []serveListenerSpec, reusePort bool, log *slog.Logger) (*ownedServeListeners, error) {
	owned := &ownedServeListeners{listeners: make([]net.Listener, 0, len(specs))}
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, withCloseErrors(fmt.Errorf("serve startup canceled before binding %q: %w", spec.addr, err), owned.closeAll())
		}
		if spec.http && reusePort && !serverhandover.Supported() {
			log.Warn("--reuse-port set but SO_REUSEPORT is unsupported here; using a plain listener (drain+restart, not overlap)", "addr", spec.addr)
		}
		lis, err := bind(ctx, spec.addr, reusePort)
		if err == nil && lis == nil {
			err = fmt.Errorf("no listener returned for %q", spec.addr)
		}
		if err != nil {
			if spec.http && reusePort {
				err = fmt.Errorf("reuse-port listen %s: %w", spec.addr, err)
			}
			return nil, withCloseErrors(fmt.Errorf("listener failed: %w", err), owned.closeAll())
		}
		owned.listeners = append(owned.listeners, newCloseOnceListener(lis))
	}
	return owned, nil
}

// withCloseErrors keeps primary as the leading cause and adds close failures after
// it. A nil primary with a real close failure is still an error.
func withCloseErrors(primary, closeErr error) error {
	switch {
	case closeErr == nil:
		return primary
	case primary == nil:
		return fmt.Errorf("serve listener close failed: %w", closeErr)
	default:
		return errors.Join(primary, closeErr)
	}
}

// announceOutput observes the announcement's writer. The existing callbacks discard
// their fmt.Fprintf results, so without this a rejected or short write would still
// admit the launch. Success means only that the writer accepted the bytes — not that
// a terminal flushed them or a person read them. After the first failure no further
// bytes are forwarded.
type announceOutput struct {
	w   io.Writer
	mu  sync.Mutex
	err error
}

func (o *announceOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return 0, o.err
	}
	n, err := o.w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.err = err
	}
	return n, err
}

func (o *announceOutput) failure() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

// runAnnouncement invokes the existing announce callback over an observed writer and
// returns every failure it saw. Neither error carries output bytes, so no token
// plaintext reaches an error or a log through here.
func runAnnouncement(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress, announce func(context.Context, io.Writer, *engine, consoleAddress) error) error {
	observed := &announceOutput{w: out}
	var errs []error
	if err := announce(ctx, observed, eng, addr); err != nil {
		errs = append(errs, fmt.Errorf("startup announcement failed: %w", err))
	}
	if err := observed.failure(); err != nil {
		errs = append(errs, fmt.Errorf("startup announcement output was not accepted: %w", err))
	}
	return errors.Join(errs...)
}

// serveHTTP runs one acquired HTTP listener. The close authority is deferred at entry,
// so a ServeTLS that fails in its HTTP/2 setup — before net/http tracks the listener —
// still releases the socket before the failure is published, exactly as
// ListenAndServeTLS's own deferred close did.
func serveHTTP(srv *http.Server, lis net.Listener, insecure bool, log *slog.Logger, errCh chan<- error) {
	errCh <- func() error {
		defer func() { _ = lis.Close() }()
		if insecure {
			log.Warn("INSECURE MODE: serving plaintext HTTP — never expose beyond localhost", "addr", srv.Addr)
			return srv.Serve(lis)
		}
		// The shared reloadable GetCertificate callback was installed before any
		// listener was acquired. Empty file arguments make net/http use that callback.
		return srv.ServeTLS(lis, "", "")
	}()
}

// serveGRPC runs the acquired gRPC listener with the same entry-deferred close.
func serveGRPC(srv *grpc.Server, lis net.Listener, errCh chan<- error) {
	errCh <- func() error {
		defer func() { _ = lis.Close() }()
		return srv.Serve(lis)
	}()
}
