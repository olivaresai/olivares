// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashKey returns the hex SHA-256 of s. It is used for FindingReport.DetailHash so
// a finding carries a stable, correlatable reference to a process WITHOUT shipping
// the raw command line or any potentially sensitive token (docs/SECURITY-HARDENING.md,
// minimal-data). It hashes a process key, never a secret.
//
// NOTE on argv: the eBPF backstop never emits raw process arguments — an edge
// carries only the (scrubbed) resource path/endpoint (observations.go) and a
// finding carries only this hash. There is therefore no argv-redaction helper
// here: a value the connector does not emit cannot leak. If a future change emits
// argv, redaction MUST be added at that emission point and tested end-to-end.
func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
