// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package nativepam

import (
	"context"
	"net"
)

const AuthoritySocketPath = "/run/olivares-portal-api/authority.sock"

type AuthorityPeer struct{}

func (*AuthorityPeer) UID() uint32                 { return 0 }
func (*AuthorityPeer) PID() int32                  { return 0 }
func (*AuthorityPeer) Close()                      {}
func (*AuthorityPeer) Check(context.Context) error { return ErrRefused }
func (*AuthorityPeer) Alive(context.Context) error { return ErrRefused }
func HoldPortalAuthority(context.Context, *net.UnixConn) (*AuthorityPeer, error) {
	return nil, ErrRefused
}
func HoldEngineAuthority(context.Context) (*AuthorityPeer, error) { return nil, ErrRefused }
func HoldOriginalAuthority(context.Context, int) (*AuthorityPeer, error) {
	return nil, ErrRefused
}
