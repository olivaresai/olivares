// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

const maxDurableEventIDLen = 200

var (
	// ErrDurableIntakeUnavailable means Eventing has not received its durable
	// store handle. The caller must keep its source outbox pending; treating this
	// as success would acknowledge an event that Eventing never observed.
	ErrDurableIntakeUnavailable = errors.New("eventing: durable intake is not wired")

	// ErrInvalidDurableEvent marks a malformed envelope. Unlike the best-effort
	// bus capture, durable intake never drops an invalid event and returns nil:
	// nil is the source outbox's permission to settle the event as published.
	ErrInvalidDurableEvent = errors.New("eventing: invalid durable event")

	// ErrDurableEventIDConflict means an already-captured event ID was reused for
	// different content. A stable ID is a content binding, not merely a duplicate
	// suppression hint; callers must surface or dead-letter this condition rather
	// than silently accepting the first payload under the second command.
	ErrDurableEventIDConflict = errors.New("eventing: durable event id reused with different content")
)

// IngestDurable captures a cataloged event through an explicit durable
// boundary, without depending on the process-local bus. It differs deliberately
// from onEvent in four ways required by a source-side transactional outbox:
//
//   - every identity and payload error is returned, never logged and dropped;
//   - the caller must supply a stable ID and occurrence time (neither is minted
//     here, because a retry after an ambiguous settlement must be byte-identical);
//   - the event row is retained even with no current subscription, binding that
//     ID to its exact type/source/time/payload for every later retry;
//   - a same-ID replay succeeds only when all bound content is identical.
//
// The event row and all matching delivery rows commit atomically. A successful
// return therefore permits the source outbox to settle; a non-nil error must
// leave it retryable or visibly dead-lettered by the source's own policy.
func (m *Module) IngestDurable(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return ErrDurableIntakeUnavailable
	}
	if _, ok := typeInfo(e.Type); !ok {
		return fmt.Errorf("%w: event type %q is not cataloged", ErrInvalidDurableEvent, e.Type)
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return fmt.Errorf("%w: tenant is missing, invalid, or reserved", ErrInvalidDurableEvent)
	}
	if e.ID == "" || len(e.ID) > maxDurableEventIDLen || strings.TrimSpace(e.ID) != e.ID {
		return fmt.Errorf("%w: event id is required, bounded, and must not carry surrounding whitespace", ErrInvalidDurableEvent)
	}
	if e.Source == "" || len(e.Source) > maxSourceLen || strings.TrimSpace(e.Source) != e.Source {
		return fmt.Errorf("%w: source is required, bounded, and must not carry surrounding whitespace", ErrInvalidDurableEvent)
	}
	if e.Time.IsZero() {
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidDurableEvent)
	}
	if e.Payload == nil {
		return fmt.Errorf("%w: payload is required", ErrInvalidDurableEvent)
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("%w: payload is not JSON-serializable: %v", ErrInvalidDurableEvent, err)
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return fmt.Errorf("%w: payload must be a non-null JSON value", ErrInvalidDurableEvent)
	}
	if len(payload) > maxPayloadBytes {
		return fmt.Errorf("%w: payload exceeds %d bytes", ErrInvalidDurableEvent, maxPayloadBytes)
	}

	for attempt := 0; ; attempt++ {
		enqueued, err := m.captureEventOnce(ctx, tenant, e, e.ID, e.Time, payload, true)
		switch {
		case err == nil:
			if enqueued > 0 {
				m.nudgeTenant(tenant)
			}
			return nil
		case errors.Is(err, store.ErrConflict) && attempt < maxCaptureRetries:
			retrySleep(attempt)
			continue
		default:
			return err
		}
	}
}
