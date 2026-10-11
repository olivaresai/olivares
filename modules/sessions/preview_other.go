// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package sessions

import "errors"

func sessionListenPorts(int) ([]listenPort, error) {
	return nil, errors.New("session preview reads listening ports from Linux /proc")
}
