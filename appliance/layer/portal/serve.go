// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// listenFDsStart is the first descriptor the service manager passes (SD_LISTEN_FDS_START).
const listenFDsStart = 3

// maxListenFDs bounds how many passed descriptors the console accepts.
const maxListenFDs = 8

var errNoSockets = errors.New("no sockets were passed by the service manager")

// tlsDirectoryVariable names the environment variable in which the service unit tells the
// console where the operator keeps the TLS files it delivers as credentials.
const tlsDirectoryVariable = "OLIVARES_PORTAL_TLS_DIRECTORY"

// Run serves the console over TLS on the sockets the service manager passed to process
// pid and returns the process exit code. getenv reads the environment and diagnostics go
// to logw; neither ever carries key material. Custody is measured on the operator's
// directory named by OLIVARES_PORTAL_TLS_DIRECTORY, and the pair is read from
// $CREDENTIALS_DIRECTORY once its certificate is the operator's. The operator's selection is
// the file first boot publishes (SelectionFile), and the firewall prerequisite is the firewall
// owner's measurement of this boot (MeasurementFile); until that owner publishes one, the
// prerequisite is unmeasured and the console serves loopback at most. Each passed socket is
// served only if its kernel binding to an interface, read back from the socket, satisfies
// the listen decision.
//
// While it serves, the console reads the published selection again every selectionPoll. When
// the file is no longer the one it decided on, it stops with exit 0, so the next connection to
// its socket starts a console that reads the file anew: a rewritten selection is never served
// under the decision taken for the one before it.
//
// Neither the product probe nor the host's sign-in stack is wired in this version either,
// so the mode selector measures no product and no stack: the mode is the refusing one, and
// every page says so and names the repair console on tty1 as the door that remains.
func Run(getenv func(string) string, pid int, logw io.Writer) int {
	ticker := time.NewTicker(selectionPoll)
	defer ticker.Stop()
	return run(getenv, pid, logw, productionFiles, ticker.C)
}

// selectionPoll is how often a serving console reads the published selection again.
const selectionPoll = 5 * time.Second

// selectionWatch is the published selection as the console read it before deciding: whether
// the file was there, the check it failed if any, and its bytes.
type selectionWatch struct {
	path    string
	present bool
	reason  string
	digest  [sha256.Size]byte
}

// watchSelection reads the published selection at path. It is read before the snapshot, so a
// file rewritten in between is seen as changed at the next reading, never missed.
func watchSelection(path string) *selectionWatch {
	w := &selectionWatch{path: path}
	w.present, w.reason, w.digest = w.read()
	return w
}

func (w *selectionWatch) read() (bool, string, [sha256.Size]byte) {
	data, present, reason := readRootOwned(w.path, maxSelectionBytes)
	return present, reason, sha256.Sum256(data)
}

// Changed reads the published selection again and reports whether it is no longer the one the
// console decided on: it appeared or disappeared, its bytes changed, or it now fails, or
// passes, a check of its owner, mode or size.
func (w *selectionWatch) Changed() bool {
	present, reason, digest := w.read()
	return present != w.present || reason != w.reason || digest != w.digest
}

// hostFiles names the host files the console reads besides its TLS material.
type hostFiles struct{ selection, measurement, bootID string }

// productionFiles are the files an installed console reads.
var productionFiles = hostFiles{selection: SelectionFile, measurement: MeasurementFile, bootID: bootIDFile}

// snapshot reads the published selection and, through the firewall probe, the measurement,
// and decides the listener. A refused selection is served as none, loopback at most, and the
// refusal is logged.
func (h hostFiles) snapshot(custody Custody, logf func(string, ...any)) Status {
	selection, reason := ReadSelection(h.selection)
	if reason != "" {
		logf("%s; serving as if none were published", reason)
	}
	return Snapshot(selection, custody, MeasuredFirewall{Path: h.measurement, BootID: h.bootID, Selection: selection})
}

// run serves as Run does; ticks paces the readings of the published selection, and nil reads
// it only once.
func run(getenv func(string) string, pid int, logw io.Writer, files hostFiles, ticks <-chan time.Time) int {
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(logw, "appliance console: "+format+"\n", args...) }
	custody, cert := CheckTLSCustody(getenv(tlsDirectoryVariable), getenv("CREDENTIALS_DIRECTORY"))
	watch := watchSelection(files.selection)
	status := files.snapshot(custody, logf)
	status.SignIn = auth.NewSelector(nil, nil).Select()
	if status.Listen.Mode == Disabled || cert == nil {
		logf("network disabled: %s", strings.Join(status.Listen.Reasons, "; "))
	}
	logf("sign-in over the network: %s", status.SignIn.Mode)
	listeners, err := passedListeners(getenv, pid)
	if err != nil {
		logf("%v", err)
		return 1
	}
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	var tlsConfig *tls.Config
	if cert != nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}
	}
	// The module helpers are read by the console itself, never for a request, and the firewall
	// owner's measurement with the firewall helper's status. The local socket's module.read and the
	// Firewall page answer from this one read model.
	reads := newModuleReads(time.Now)
	reads.measure = firewall.MeasurementReader{Path: files.measurement, BootID: files.bootID}.Read
	status.Firewalls = reads
	readsStarted := false
	server := &http.Server{
		Handler:           NewHandler(status),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(logw, "appliance console: ", 0),
	}
	defer server.Close()
	done := make(chan error, len(listeners))
	serving := 0
	names := strings.Split(getenv("LISTEN_FDNAMES"), ":")
	for i, l := range listeners {
		name := ""
		if len(names) == len(listeners) {
			name = names[i]
		}
		if localActivation(name, l.Addr(), custody.Verified && cert != nil) {
			records, err := hostops.Open(localsession.OperationDirectory)
			catalog, catalogErr := hostops.NewCatalog(servedDescriptors())
			if err != nil || catalogErr != nil {
				logf("local operation model unavailable")
				_ = l.Close()
				continue
			}
			// The read model runs with the local listener, once.
			if !readsStarted {
				readsStarted = true
				refresh := time.NewTicker(moduleRefresh)
				stopReads := reads.start(helperclient.Client{}.Call, refresh.C, sharedLifecycle)
				defer func() {
					stopReads()
					refresh.Stop()
				}()
			}
			local := localsession.NewServer(records, catalog, localsession.Linux{}).ReadModules(reads)
			serving++
			go func(l net.Listener) { done <- local.Serve(l) }(l)
			continue
		}
		if cert == nil {
			_ = l.Close()
			continue
		}
		addr := boundAddr{Addr: l.Addr(), device: boundDevice(l)}
		if reason := status.Listen.Refusal(addr); reason != "" {
			logf("not serving %s: %s", addr, reason)
			_ = l.Close()
			continue
		}
		serving++
		logf("serving %s, certificate SHA-256 %s", addr, custody.CertificateSHA256)
		go func(l net.Listener) { done <- server.ServeTLS(l, "", "") }(l)
	}
	if serving == 0 {
		logf("no passed socket may be served")
		return 1
	}
	logf("remote access waits for: %s", strings.Join(status.Listen.Reasons, "; "))
	select {
	case err := <-done:
		logf("stopped: %v", err)
		return 1
	case <-selectionChanges(watch, ticks):
		logf("the published selection changed; stopping so that the next connection starts a console that reads it")
		return 0
	}
}

// selectionChanges returns a channel closed at the first tick on which watch reports a change.
// It receives every tick until ticks is closed, reading no more once it has seen a change, so a
// tick is taken only after the reading of the one before it ended. With no ticks it is never
// closed.
func selectionChanges(watch *selectionWatch, ticks <-chan time.Time) <-chan struct{} {
	changed := make(chan struct{})
	if ticks == nil {
		return changed
	}
	go func() {
		seen := false
		for range ticks {
			if !seen && watch.Changed() {
				seen = true
				close(changed)
			}
		}
	}()
	return changed
}

// passedListeners returns the stream sockets passed to process pid under the service
// manager's socket-passing protocol: LISTEN_PID names the process and LISTEN_FDS counts
// descriptors from listenFDsStart.
func passedListeners(getenv func(string) string, pid int) ([]net.Listener, error) {
	if getenv("LISTEN_PID") != strconv.Itoa(pid) {
		return nil, errNoSockets
	}
	n, err := strconv.Atoi(getenv("LISTEN_FDS"))
	if err != nil || n < 1 || n > maxListenFDs {
		return nil, errNoSockets
	}
	listeners := make([]net.Listener, 0, n)
	for fd := listenFDsStart; fd < listenFDsStart+n; fd++ {
		f := os.NewFile(uintptr(fd), "passed-socket-"+strconv.Itoa(fd))
		l, err := net.FileListener(f)
		_ = f.Close()
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("passed descriptor %d is not a stream socket", fd)
		}
		listeners = append(listeners, l)
	}
	return listeners, nil
}

// boundAddr is a passed socket's local address and the interface the kernel binds the
// socket to, "" for none or unknown.
type boundAddr struct {
	net.Addr
	device string
}

// BoundDevice implements DeviceBound.
func (a boundAddr) BoundDevice() string { return a.device }

// boundDevice returns the interface the kernel binds a passed socket to, or "" when it is
// bound to none or the binding cannot be read.
func boundDevice(l net.Listener) string {
	conn, ok := l.(syscall.Conn)
	if !ok {
		return ""
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return ""
	}
	return socketDevice(raw)
}

// localDescriptors are the tasks the local socket describes and plans: the host status and each
// installed module's tasks.
func localDescriptors() []hostops.Descriptor {
	return append([]hostops.Descriptor{hostops.StatusDescriptor()}, storage.Descriptors()...)
}

// localActivation requires both the service manager's name and the fixed Unix
// address. A passed TCP listener or a caller-selected path never becomes local.
func localActivation(name string, addr net.Addr, verifiedTLS bool) bool {
	local, ok := addr.(*net.UnixAddr)
	return verifiedTLS && ok && name == "local" && local.Net == "unix" && local.Name == localsession.SocketPath
}
