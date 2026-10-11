// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package egress

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
)

func Wrap(context.Context, *exec.Cmd, Policy) (func() error, func(), string, error) {
	return nil, nil, "", errors.New("session network confinement requires Linux user/network namespaces; session was not started")
}
func DialPreview(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("session preview requires Linux")
}
func Handles([]string) bool                                { return false }
func RunHelper([]string, func([]string, *os.File) int) int { return 126 }
