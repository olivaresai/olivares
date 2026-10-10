// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package nativepam

import (
	"context"
	"net"
)

func check(context.Context, string, string, []byte) (Result, error)        { return Result{}, ErrRefused }
func trustedFile(string, bool) bool                                        { return false }
func trustedSocket(string) bool                                            { return false }
func ServeWorker(context.Context, *net.UnixConn, context.CancelFunc) error { return ErrRefused }
