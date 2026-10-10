// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/sdk/netbind"
)

const namespaceArg = "--session-network-v1"
const execArg = "--session-network-exec-v1"

type helperSpec struct {
	Socket  string
	Preview string
	CAPEM   []byte
	Policy  Policy
	Files   int
	ReadyFD int
	Args    []string
}

// Wrap keeps the caller's confinement helper and held descriptors, and adds a
// fresh user/network namespace. WaitReady must succeed before reporting launch.
// preview is the bridge's Unix socket for DialPreview, removed with release.
func Wrap(ctx context.Context, cmd *exec.Cmd, policy Policy) (waitReady func() error, release func(), preview string, err error) {
	ds, err := destinations(policy)
	if err != nil {
		return nil, nil, "", err
	}
	p, err := startProxy(ctx, ds)
	if err != nil {
		return nil, nil, "", fmt.Errorf("session egress proxy: %w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		p.close()
		return nil, nil, "", err
	}
	// Shorter than proxy.sock, so startProxy's sun_path check covers it too.
	preview = filepath.Join(p.dir, "pv.sock")
	req := helperSpec{Socket: p.socket, Preview: preview, CAPEM: p.caPEM, Policy: policy, Files: len(cmd.ExtraFiles), ReadyFD: 3 + len(cmd.ExtraFiles), Args: cmd.Args}
	payload, err := json.Marshal(req)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		p.close()
		return nil, nil, "", err
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "__olivares_confine" {
		_ = reader.Close()
		_ = writer.Close()
		p.close()
		return nil, nil, "", errors.New("session egress requires the filesystem confinement helper")
	}
	cmd.Args = []string{cmd.Path, "__olivares_confine", namespaceArg, string(payload)}
	cmd.ExtraFiles = append(cmd.ExtraFiles, writer)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
	cmd.SysProcAttr.GidMappingsEnableSetgroups = false
	// Preserve only setup capabilities across the namespace helper's exec;
	// restrictSockets drops the entire bounding/effective set before the tool.
	cmd.SysProcAttr.AmbientCaps = []uintptr{unix.CAP_NET_ADMIN, unix.CAP_SETPCAP}
	for _, d := range ds {
		ip := net.ParseIP(d.host)
		port, err := strconv.Atoi(d.port) // destinations has validated and normalized the port.
		if err == nil && port < 1024 && (d.host == "localhost" || (ip != nil && ip.IsLoopback())) {
			cmd.SysProcAttr.AmbientCaps = append(cmd.SysProcAttr.AmbientCaps, unix.CAP_NET_BIND_SERVICE)
			break
		}
	}
	result := make(chan error, 1)
	go func() {
		defer reader.Close()
		if err := reader.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			result <- fmt.Errorf("session network boundary readiness deadline: %w", err)
			return
		}
		response, err := io.ReadAll(io.LimitReader(reader, 1024))
		if err != nil {
			result <- fmt.Errorf("session network boundary readiness read failed (%q): %w", strings.TrimSpace(string(response)), err)
			return
		}
		if string(response) != "ready\n" {
			reason := strings.TrimSpace(string(response))
			if reason == "" {
				reason = "helper exited before reporting readiness"
			}
			result <- fmt.Errorf("session network boundary could not be established (%q); enable unprivileged user/network namespaces and Landlock on the engine host", reason)
			return
		}
		result <- nil
	}()
	release = func() { _ = writer.Close(); p.close() }
	waitReady = func() error {
		_ = writer.Close()
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			_ = reader.Close()
			return ctx.Err()
		}
	}
	return waitReady, release, preview, nil
}

// DialPreview opens a connection to address (a loopback host:port) inside the
// session's network namespace, through the bridge listening on socket.
func DialPreview(ctx context.Context, socket, address string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(c, "%s\n", address); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Handles recognizes only the private network helper flags.
func Handles(args []string) bool {
	return len(args) > 0 && (args[0] == namespaceArg || args[0] == execArg)
}

// RunHelper keeps the TCP bridges outside the filtered tool process. next
// enters the existing filesystem helper, which signals readiness after Landlock.
func RunHelper(args []string, next func([]string, *os.File) int) int {
	if len(args) >= 3 && args[0] == execArg {
		fd, err := strconv.Atoi(args[1])
		if err != nil || fd < 3 {
			return 126
		}
		ready := os.NewFile(uintptr(fd), "session-egress-ready")
		defer ready.Close()
		unix.CloseOnExec(fd)
		if err := restrictSockets(); err != nil {
			_, _ = fmt.Fprintf(ready, "socket restrictions failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "olivares: session network confinement failed:", err)
			return 126
		}
		return next(args[2:], ready)
	}
	if len(args) != 2 || args[0] != namespaceArg {
		return 126
	}
	var req helperSpec
	if err := json.Unmarshal([]byte(args[1]), &req); err != nil || req.ReadyFD != 3+req.Files || req.Files < 0 || req.Files > 128 || len(req.Args) < 3 {
		return 126
	}
	ready := os.NewFile(uintptr(req.ReadyFD), "session-egress-ready")
	// Keep ownership until setup errors have been reported. On successful
	// launch serveNamespace closes this copy after passing it to the child.
	defer ready.Close()
	code, err := serveNamespace(req, ready)
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			// Restore the kernel disposition, not Go's crash/stack-dump handler.
			// The supported Linux amd64/arm64 rt_sigaction layout uses four words.
			action := struct {
				handler, flags, restorer uintptr
				mask                     uint64
			}{}
			sig := status.Signal()
			// RLIMIT_CORE does not prevent a piped systemd-coredump collector.
			if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
				return 126
			}
			if sig != syscall.SIGKILL {
				if _, _, errno := unix.RawSyscall6(unix.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&action)), 0, 8, 0, 0); errno != 0 {
					return 126
				}
			}
			if err := unix.Kill(os.Getpid(), sig); err != nil {
				return 126
			}
			// Delivery can occur on another runtime thread. Never fall through to
			// os.Exit(-1), which would turn a signal into the normal exit code 255.
			for {
				_ = unix.Pause()
			}
		}
		return code
	}
	if err != nil {
		_, _ = fmt.Fprintf(ready, "network setup failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "olivares: session network setup failed:", err)
		return 126
	}
	return code
}

func serveNamespace(req helperSpec, ready *os.File) (int, error) {
	// Stop signals the entire owned group. Keep the bridge supervisor alive
	// while the tool flushes; its death signal must not turn TERM into KILL.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	if err := loopbackUp(); err != nil {
		return 126, err
	}
	ds, err := destinations(req.Policy)
	if err != nil {
		return 126, err
	}
	proxyListener, err := netbind.Listen(context.Background(), "tcp4", "127.0.0.1:0", netbind.Policy{Component: "sessions", Purpose: "egress bridge"})
	if err != nil {
		return 126, err
	}
	defer proxyListener.Close()
	go relayAccept(proxyListener, req.Socket, "")
	// The preview relay never reaches the bridge's own listeners.
	own := map[string]bool{portOf(proxyListener.Addr()): true}
	// Local clients commonly bypass HTTP_PROXY. Listen on precisely the bound
	// loopback address/port, and tunnel only to that endpoint through the ACL.
	for _, d := range ds {
		addresses := []string{d.address()}
		ip := net.ParseIP(d.host)
		if d.host == "localhost" {
			addresses = []string{net.JoinHostPort("127.0.0.1", d.port), net.JoinHostPort("::1", d.port)}
		} else if ip == nil || !ip.IsLoopback() {
			continue
		}
		listening := false
		for _, address := range addresses {
			l, err := netbind.Listen(context.Background(), "tcp", address, netbind.Policy{Component: "sessions", Purpose: "endpoint relay"})
			if err != nil {
				// IPv6 may be disabled while an IPv4 localhost provider is valid.
				if d.host == "localhost" &&
					(errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EADDRNOTAVAIL)) {
					continue
				}
				return 126, err
			}
			listening = true
			own[portOf(l.Addr())] = true
			defer l.Close()
			if d.control || d.scheme == "http" {
				srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: endpointRelay(req.Socket, d)}
				defer srv.Close()
				go func() {
					if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
						logRelayFailure("HTTP serve", err)
					}
				}()
			} else {
				go relayAccept(l, req.Socket, d.address())
			}
		}
		if !listening {
			return 126, errors.New("no loopback address family is available")
		}
	}
	if req.Preview != "" {
		l, err := netbind.Listen(context.Background(), "unix", req.Preview, netbind.Policy{Component: "sessions", Purpose: "preview relay"})
		if err != nil {
			return 126, err
		}
		if err := os.Chmod(req.Preview, 0o600); err != nil {
			_ = l.Close()
			return 126, err
		}
		defer l.Close()
		go previewAccept(l, own)
	}
	self, err := os.Executable()
	if err != nil {
		return 126, err
	}
	argv := append([]string{"__olivares_confine", execArg, strconv.Itoa(req.ReadyFD)}, req.Args[2:]...)
	child := exec.Command(self, argv...) // #nosec G204 -- reexecutes the engine's existing helper
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	var caFile string
	if len(req.CAPEM) != 0 {
		// TMPDIR is the runner-owned scratch grant, removed after this session.
		dir, err := os.MkdirTemp("", "proxy-trust-")
		if err != nil {
			return 126, err
		}
		defer os.RemoveAll(dir)
		caFile = filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(caFile, req.CAPEM, 0o600); err != nil {
			return 126, err
		}
	}
	child.Env = proxyEnvironment(os.Environ(), "http://"+proxyListener.Addr().String(), caFile)
	child.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	// Pdeathsig fires when the starting thread exits, not the process; keep
	// this goroutine on that thread for the supervisor's whole life.
	runtime.LockOSThread()
	for i := 0; i < req.Files; i++ {
		child.ExtraFiles = append(child.ExtraFiles, os.NewFile(uintptr(3+i), "session-grant"))
	}
	child.ExtraFiles = append(child.ExtraFiles, ready)
	if err := child.Start(); err != nil {
		return 126, err
	}
	_ = ready.Close()
	for _, f := range child.ExtraFiles[:req.Files] {
		_ = f.Close()
	}
	err = child.Wait()
	if err == nil {
		return 0, nil
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return exited.ExitCode(), err
	}
	return 126, err
}

func proxyEnvironment(env []string, address, caFile string) []string {
	var out []string
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if proxy, trust := driverfacts.NetworkEnv(key); !proxy && (!trust || caFile == "") {
			out = append(out, value)
		}
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		out = append(out, name+"="+address)
	}
	if caFile != "" {
		for _, name := range driverfacts.TrustEnv {
			out = append(out, name+"="+caFile)
		}
	}
	return out
}

func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request)
}

func relayAccept(l net.Listener, socket, destination string) {
	for {
		c, err := l.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logRelayFailure("accept", err)
			}
			return
		}
		go relayConnection(c, socket, destination)
	}
}

func relayConnection(client net.Conn, socket, destination string) {
	defer client.Close()
	upstream, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		logRelayFailure("dial", err)
		return
	}
	defer upstream.Close()
	var source io.Reader = upstream
	if destination != "" {
		if err := upstream.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			logRelayFailure("CONNECT deadline", err)
			return
		}
		if _, err := fmt.Fprintf(upstream, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", destination, destination); err != nil {
			logRelayFailure("CONNECT write", err)
			return
		}
		reader := bufio.NewReader(upstream)
		resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if err != nil {
			logRelayFailure("CONNECT response", err)
			return
		}
		if resp.StatusCode != http.StatusOK {
			log.Printf("olivares: session egress relay CONNECT failed: HTTP status %d", resp.StatusCode)
			return
		}
		if err := upstream.SetDeadline(time.Time{}); err != nil {
			logRelayFailure("CONNECT deadline reset", err)
			return
		}
		source = reader
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close(); close(done) }()
	_, _ = io.Copy(client, source)
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func portOf(a net.Addr) string {
	_, port, _ := net.SplitHostPort(a.String())
	return port
}

// previewAccept serves the engine's DialPreview. Each connection names one
// loopback address on its first line; the bridge dials it inside this network
// namespace and copies bytes both ways. The engine only names a port that one of
// the session's own processes listens on; own refuses the bridge's ports anyway.
func previewAccept(l net.Listener, own map[string]bool) {
	for {
		c, err := l.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logRelayFailure("preview accept", err)
			}
			return
		}
		go previewConnection(c, own)
	}
}

func previewConnection(client net.Conn, own map[string]bool) {
	defer client.Close()
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		logRelayFailure("preview deadline", err)
		return
	}
	reader := bufio.NewReaderSize(client, 64)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		logRelayFailure("preview request", err)
		return
	}
	address := strings.TrimSuffix(string(line), "\n")
	host, portText, err := net.SplitHostPort(address)
	port, perr := strconv.ParseUint(portText, 10, 16)
	if ip := net.ParseIP(host); err != nil || perr != nil || ip == nil || !ip.IsLoopback() || own[strconv.FormatUint(port, 10)] {
		log.Printf("olivares: session preview relay refused an address that is not a session loopback port")
		return
	}
	if err := client.SetReadDeadline(time.Time{}); err != nil {
		logRelayFailure("preview deadline reset", err)
		return
	}
	upstream, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		logRelayFailure("preview dial", err)
		return
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(upstream, reader); _ = upstream.Close(); close(done) }()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	<-done
}

// Error text from HTTP peers can contain credentials. Keep the operation and
// trusted OS/error classes, never URLs, paths, headers or response bodies.
func logRelayFailure(operation string, err error) {
	reason := "I/O error"
	var errno syscall.Errno
	var networkError net.Error
	switch {
	case errors.As(err, &errno):
		reason = errno.Error()
	case errors.As(err, &networkError) && networkError.Timeout():
		reason = "I/O timeout"
	case errors.Is(err, io.ErrUnexpectedEOF):
		reason = "unexpected EOF"
	case errors.Is(err, io.EOF):
		reason = "EOF"
	case errors.Is(err, net.ErrClosed):
		reason = "connection closed"
	case errors.Is(err, context.Canceled):
		reason = "request canceled"
	default:
		if operation == "CONNECT response" {
			reason = "invalid HTTP response"
		}
	}
	log.Printf("olivares: session egress relay %s failed: %s", operation, reason)
}

func endpointRelay(socket string, d destination) http.Handler {
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "session-proxy"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	reverse := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = "http", d.address()
			r.Out.Host = d.address()
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			logRelayFailure("HTTP upstream", err)
			http.Error(w, "session control endpoint unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward := r.Clone(r.Context())
		forward.URL.Scheme, forward.URL.Host = "http", d.address()
		forward.Host, forward.RequestURI = d.address(), ""
		if r.Header.Get("Upgrade") != "" || !requestAllowed(forward, []destination{d}) {
			http.Error(w, "session destination denied", http.StatusForbidden)
			return
		}
		reverse.ServeHTTP(w, forward)
	})
}
