// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

type heldKernel struct {
	invocation.Kernel
	held []int
}

func (k *heldKernel) Close(fd int) { k.held = append(k.held, fd) }
func (k *heldKernel) release() {
	for _, fd := range k.held {
		k.Kernel.Close(fd)
	}
}
func (k *heldKernel) alive() bool {
	if len(k.held) == 0 {
		return false
	}
	for _, fd := range k.held {
		if k.Kernel.Alive(fd) != nil {
			return false
		}
	}
	return true
}

// Serve accepts at most sixteen bounded connections and retains the attested peer's
// descriptor until its response is written. A peer cannot replace itself during parsing.
func (e Edge) Serve(ctx context.Context, listener net.Listener) error {
	slots := make(chan struct{}, 16)
	go func() { <-ctx.Done(); listener.Close() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
			go func() { defer func() { <-slots }(); e.serveConnection(ctx, conn) }()
		default:
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			json.NewEncoder(conn).Encode(EdgeResponse{Code: "network_status_busy"})
			conn.Close()
		}
	}
}
func (e Edge) serveConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return
	}
	kernel := &heldKernel{Kernel: invocation.Linux{}}
	defer kernel.release()
	var peer helperschema.Peer
	var attestErr error
	err = raw.Control(func(fd uintptr) { peer, attestErr = invocation.Attest(kernel, int(fd)) })
	if err != nil {
		return
	}
	response := EdgeResponse{Code: "network_peer_unattested"}
	if attestErr == nil && peer.Attested {
		request, err := DecodeEdge(conn)
		if err != nil {
			response.Code = "network_input_refused"
		} else if !kernel.alive() {
			response.Code = "network_peer_exited"
		} else {
			response = e.Handle(ctx, peer, request)
		}
	}
	if peer.Attested && !kernel.alive() {
		response = EdgeResponse{Code: "network_peer_exited"}
	}

	json.NewEncoder(conn).Encode(response)
}

func staticGroup(name string) (uint32, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(g.Gid, 10, 32)
	return uint32(n), err
}
func ownedDirectory(name string, gid uint32, mode os.FileMode) error {
	if err := os.Mkdir(name, mode); err == nil {
		if err := os.Chown(name, 0, int(gid)); err != nil {
			return err
		}
		if err := os.Chmod(name, mode); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != 0 || st.Gid != gid || info.Mode().Perm() != mode {
		return errors.New("network_runtime_custody_refused")
	}
	return nil
}

// InitializeRuntime creates the network's own inodes once per boot. It creates no
// lifecycle primitive, portal receipt directory or product-channel account group.
const namespaceProofPath = "/run/olivares-network/pid-namespace.json"

type namespaceProof struct{ Namespace, BootID string }

func InitializeRuntime() error {
	if os.Geteuid() != 0 {
		return errors.New("network_runtime_requires_root")
	}
	gid, err := staticGroup("olivares-net-guard")
	if err != nil {
		return err
	}
	for _, dir := range []struct {
		name string
		gid  uint32
		mode os.FileMode
	}{{"/run/olivares-network", gid, 0750}, {"/run/olivares-net-guard-api", 0, 0755}, {"/run/olivares-netrestore", gid, 0750}} {
		if err := ownedDirectory(dir.name, dir.gid, dir.mode); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(NetworkLockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0640)
	if err == nil {
		if err := f.Chown(0, int(gid)); err != nil {
			f.Close()
			return err
		}
		if err := f.Chmod(0640); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	lock, err := NewFileLock()
	if err != nil {
		return err
	}
	release, err := lock.Acquire()
	if err != nil {
		return err
	}
	release()
	ns, err := os.Readlink("/proc/1/ns/pid")
	if err != nil {
		return err
	}
	clock, err := NewBootClock()
	if err != nil {
		return err
	}
	proof := namespaceProof{Namespace: ns, BootID: clock.boot}
	ledger := rootLedger{dir: "/run/olivares-network", uid: 0, gid: gid}
	if err := ledger.write("pid-namespace.json", proof); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	var existing namespaceProof
	if err := protectedJSON(namespaceProofPath, 0, gid, 0640, 1024, &existing); err != nil {
		return err
	}
	if existing != proof {
		return errors.New("network_namespace_custody_refused")
	}
	return nil
}
