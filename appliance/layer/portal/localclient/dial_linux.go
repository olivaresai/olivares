// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package localclient

import (
	"errors"
	"net"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
)

// Dial authenticates the first record from the static portal account. Neither a
// socket path nor a portal UID can be supplied by the caller. Client SO_PEERCRED
// would identify the listener's creator, so it is never used as responder proof.
func Dial() (*Client, error) { return dialAfterTLS(os.Lstat, dialSocket) }

func dialSocket() (*Client, error) {
	account, err := user.Lookup("olivares-portal")
	if err != nil {
		return nil, &Failure{Code: "local_protocol_mismatch", Reason: "portal_account_unavailable"}
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		return nil, &Failure{Code: "local_protocol_mismatch", Reason: "portal_account_unavailable"}
	}
	for _, item := range []struct {
		path      string
		mode      os.FileMode
		directory bool
	}{{"/run/olivares-portal-api", 0755, true}, {localsession.SocketPath, 0666, false}} {
		info, err := os.Lstat(item.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, &Failure{Code: "local_unavailable", Reason: "socket_unavailable"}
		}
		if err != nil || info.Mode().Perm() != item.mode || info.Mode()&os.ModeSymlink != 0 {
			return nil, &Failure{Code: "local_protocol_mismatch", Reason: "socket_custody"}
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 || (item.directory && !info.IsDir()) || (!item.directory && info.Mode()&os.ModeSocket == 0) {
			return nil, &Failure{Code: "local_protocol_mismatch", Reason: "socket_custody"}
		}
	}
	// No child process is started by this client while the session is open.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0, 0, 0, 0); errno != 0 {
		return nil, &Failure{Code: "local_protocol_mismatch", Reason: "process_custody"}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, raw syscall.RawConn) error {
		if network != "unix" || address != localsession.SocketPath {
			return errors.New("socket_custody")
		}
		var optionErr error
		if err := raw.Control(func(fd uintptr) {
			optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
		}); err != nil {
			return err
		}
		return optionErr
	}}
	// net creates a close-on-exec Unix stream socket; Control runs before connect.
	conn, err := dialer.Dial("unix", localsession.SocketPath)
	if err != nil {
		return nil, &Failure{Code: "local_unavailable", Reason: "connection_closed"}
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, &Failure{Code: "local_protocol_mismatch", Reason: "socket_custody"}
	}
	return connect(socketWire{conn: unix, uid: uint32(uid)})
}
