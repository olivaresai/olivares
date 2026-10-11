// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"errors"
	"time"
)

// AuditIntake is one sealed audit-ledger record handed to IngestAudit for SIEM
// forwarding. It carries ONLY the data the delivery needs: the audit event id (the
// stable idempotency key — the consumer dedups on it), the per-tenant Seq (the
// natural ledger key), the event time, the emitting source, and the already-encoded
// minimal-data Payload. Payload is opaque to the engine: it is stored verbatim and
// handed to the SinkRenderer at send time, which re-shapes it into the tower's
// dialect. The ledger forwarder (cmd) builds Payload from the sealed model.AuditEvent
// so the integrity fields (Seq/PrevHash/Hash/Sig) ride through untouched.
type AuditIntake struct {
	EventID    string
	Seq        int64
	OccurredAt time.Time
	Source     string
	Payload    []byte
}

// errAuditIntake guards the required fields.
var errAuditIntake = errors.New("eventing: audit intake requires an event id and payload")
