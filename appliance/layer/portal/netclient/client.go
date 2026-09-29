// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package netclient reaches the network guard. It has no NetworkManager authority.
package netclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

type Client struct {
	Socket string
	Owner  uint32
}

func (c Client) Call(ctx context.Context, request netguard.EdgeRequest) (netguard.EdgeResponse, error) {
	var response netguard.EdgeResponse
	payload, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	if _, err := netguard.DecodeEdge(bytes.NewReader(payload)); err != nil {
		return response, err
	}
	socket := c.Socket
	if socket == "" {
		socket = netguard.GuardSocket
	}
	info, err := os.Lstat(socket)
	if err != nil {
		return response, errors.New("network_guard_unavailable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != c.Owner || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return response, errors.New("network_guard_custody_refused")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var dial net.Dialer
	conn, err := dial.DialContext(ctx, "unix", socket)
	if err != nil {
		return response, errors.New("network_guard_unavailable")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if _, err := conn.Write(payload); err != nil {
		return response, errors.New("network_outcome_unknown")
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		if err := unix.CloseWrite(); err != nil {
			return response, errors.New("network_outcome_unknown")
		}
	}
	b, err := io.ReadAll(io.LimitReader(conn, netguard.MaxEdgeBytes+1))
	if err != nil || len(b) > netguard.MaxEdgeBytes {
		return response, errors.New("network_outcome_unknown")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&response); err != nil {
		return response, errors.New("network_outcome_unknown")
	}
	if d.Decode(new(any)) != io.EOF || response.Code == "" {
		return netguard.EdgeResponse{}, errors.New("network_outcome_unknown")
	}
	return response, nil
}
