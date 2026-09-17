// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CommunicationSchemaKinds lists every K3 entity kind the module registers,
// plus the two K1 kinds the local outbox pump depends on. The composition root
// proves each one is reachable through a tenant Scope before it calls the K3
// store phase ready; a kind the registry does not know is ErrUnknownEntity and
// keeps readiness OFF instead of surfacing later as a failed publish.
func CommunicationSchemaKinds() []model.Kind {
	return []model.Kind{
		channelKind, channelGrantKind, channelSubscriptionKind, channelLabelDefinitionKind,
		channelRouteKind, communicationEndpointKind, messageKind, messageAudienceKind,
		messageAudienceRecipientKind, messageDeliveryKind, inboxCursorKind, inboxCursorBarrierKind,
		messageAckKind, communicationGuardKind, decisionRequestKind, decisionResponseKind,
		handoffKind, deliveryDispatchKind, deliveryAttemptKind, communicationCommandKind,
		workEventKind, workOutboxKind, claimKind,
	}
}

// VerifyCommunicationSchema proves, inside one read-only transaction of the
// tenant, that every K3 kind resolves to a registered repository and that the
// table answers a bounded read. It proves reachability and shape, not
// row-level invariants: those belong to the guard witness and to each apply
// path's same-transaction checks.
func (m *Module) VerifyCommunicationSchema(ctx context.Context, tenant model.TenantID) error {
	if m == nil || m.data == nil {
		return store.ErrStoreUnavailable
	}
	if !validCanonicalCommunicationTenant(tenant) {
		return communicationError(ErrInvalidCommunicationTransition, "communication schema proof tenant is invalid")
	}
	return m.data.View(ctx, tenant, func(sc store.Scope) error {
		for _, kind := range CommunicationSchemaKinds() {
			repo, err := sc.Ext(kind)
			if err != nil {
				return fmt.Errorf("communication schema kind %s: %w", kind, err)
			}
			if _, _, err := repo.List(ctx, model.Query{Limit: 1}); err != nil {
				return fmt.Errorf("communication schema kind %s bounded read: %w", kind, err)
			}
		}
		return nil
	})
}
