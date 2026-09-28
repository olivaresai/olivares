// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !(linux && (amd64 || arm64))

package localframe

import "net"

func Next(*net.UnixConn, Expect, func() error) (Record, error) {
	return Record{}, fault("local_protocol_unavailable")
}
func Write(*net.UnixConn, Record) error { return fault("local_protocol_unavailable") }
