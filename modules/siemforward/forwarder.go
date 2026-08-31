// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/eventing"
)

// Compile-time proof the module satisfies the dormant core/audit.Forwarder seam:
// flipping it from the NopForwarder is the deliverable. The push is driven by
// ForwardDue (the leader-gated cursor walk), so Forward is invoked per walked
// record rather than inside the ledger Append transaction — a network/store write
// must never sit in the seal tx.
var _ audit.Forwarder = (*Module)(nil)

// Forward ships ONE sealed ledger record off-box for SIEM forwarding: it projects
// the record into the format-neutral wire DTO (integrity fields verbatim — never
// re-derived) and hands it to the durable eventing engine via IngestAudit, which
// enqueues a delivery per matching audit.recorded sink subscription. It NEVER
// mutates ev (the Forwarder contract). A tenant with no ledger sink subscription is
// a no-op (storage-frugal): IngestAudit enqueues nothing, the cursor still advances,
// nothing is lost.
func (m *Module) Forward(ctx context.Context, ev model.AuditEvent) error {
	if m.evt == nil {
		return nil
	}
	payload, err := json.Marshal(auditWireFrom(ev))
	if err != nil {
		return fmt.Errorf("siemforward: encode ledger record: %w", err)
	}
	_, err = m.evt.IngestAudit(ctx, ev.TenantID, eventing.AuditIntake{
		EventID:    ev.ID.String(),
		Seq:        ev.Seq,
		OccurredAt: ev.OccurredAt.Time(),
		Source:     auditSource,
		Payload:    payload,
	})
	return err
}
