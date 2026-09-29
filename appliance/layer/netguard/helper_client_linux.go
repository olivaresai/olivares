//go:build linux && (amd64 || arm64)

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// RootHelper reaches only the fixed restore socket. The response has no settlement
// authority: Report reads the independently root-owned call intents and completions.
type RootHelper struct {
	mu   sync.Mutex
	sent map[string]bool
}

func (r *RootHelper) Report(_ context.Context, w Window) (RestoreReport, error) {
	return ReadRestoreReport(w)
}
func (r *RootHelper) Restore(ctx context.Context, w Window) (RestoreReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if report, err := ReadRestoreReport(w); err == nil {
		return report, nil
	}
	key := ledgerDirectory(w)
	if r.sent == nil {
		r.sent = map[string]bool{}
	}
	if r.sent[key] {
		return RestoreReport{}, errors.New("network_restore_invocation_unknown")
	}
	const socket = "/run/olivares-helpers/netrestore.sock"
	info, err := os.Lstat(socket)
	if err != nil {
		return RestoreReport{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return RestoreReport{}, errors.New("network_restore_socket_custody_refused")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return RestoreReport{}, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	r.sent[key] = true
	b, err := json.Marshal(RestoreRequest{ConnectionUUID: w.Baseline.UUID, OperationID: w.OperationID})
	if err != nil {
		return RestoreReport{}, err
	}
	if _, err := conn.Write(b); err != nil {
		return RestoreReport{}, errors.New("network_restore_invocation_unknown")
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		unix.CloseWrite()
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, 4097))
	return ReadRestoreReport(w)
}
