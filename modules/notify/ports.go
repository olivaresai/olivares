// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// This file defines the one SEAM module XV depends on but does not own — the
// transport — expressed in the module's OWN terms so the module stays decoupled
// from the /connectors packages (the deploy/orchestration convention). The
// composition root injects the real adapter, which is backed by the output
// connectors (Slack/Teams/PagerDuty/Opsgenie/webhook/SIEM). Until it is wired the
// default fails CLOSED: a matched notification is recorded as undelivered, never
// silently dropped and never a pretend success.

// ErrUnknownDestination is returned by a wired Dispatcher when a route names a
// destination that has not been provisioned. The delivery is then recorded with a
// non-sensitive "unknown_destination" status so the misconfiguration is visible.
var ErrUnknownDestination = errors.New("notify: unknown destination")

// errNoDispatcher is the fail-closed error the unwired dispatcher returns — a
// deliberate, explicit failure, never a pretend success that would let the control
// plane believe an alert was delivered.
var errNoDispatcher = errors.New("notify: no dispatcher wired; notification recorded, not delivered")

// Dispatcher is the transport seam: it delivers a redacted notification to a
// named, pre-provisioned destination. The real adapter (composition root) maps the
// destination name to an opened OutputConnector and calls Notify; this module
// only decides what/who/when and asks. Delivery is best-effort within the
// connector's own attempt budget — durable retry/dead-letter policy, if any, is
// this module's concern (hands back only the outcome).
type Dispatcher interface {
	// Destinations returns EVERY provisioned destination name. It is the operator's
	// view — the boot warning and diagnostics — and never a credential. It is NOT
	// what a tenant-facing surface may show: see DestinationsFor.
	Destinations() []string
	// DestinationsFor returns the destinations a TENANT may address.
	//
	// It exists because the two answers were the same one, and that was a hole
	// rather than a simplification. Destinations were resolved from a single flat
	// map with no tenant anywhere in the lookup, while routes are tenant-scoped —
	// so a tenant's editor could name any destination the operator had provisioned
	// for anyone, and the notification carried that tenant's own identity to it.
	// The list endpoint returned the global set, which is the discovery step that
	// made it trivial.
	//
	// A destination the operator did not DECLARE a scope for stays addressable by
	// every tenant, which is what every existing deployment configured and therefore
	// the only upgrade-safe reading of an unwritten field. A destination scoped to an
	// EMPTY set is addressable by nobody — declaring nothing and declaring the empty
	// set are different statements and neither is read as the other.
	DestinationsFor(tenant model.TenantID) []string
	// Deliver sends n to the named destination on behalf of tenant, returning
	// ErrUnknownDestination if the name is not provisioned OR not addressable by
	// that tenant — the two are deliberately indistinguishable to the caller, so a
	// route author cannot probe another tenant's destination names by watching
	// which error comes back.
	Deliver(ctx context.Context, tenant model.TenantID, destination string, n sdk.Notification) error
	// ConnectorFingerprint returns an OPAQUE, stable digest of the operator
	// connector configuration behind a destination NAME (its kind + settings —
	// never a resolved secret VALUE), so a workflow can freeze it at approval and
	// BLOCK if the operator swaps/reconfigures the connector behind an unchanged
	// route destination (Flag B). ok=false when the seam cannot answer (an
	// unwired transport); the caller then omits it — nothing actuates anyway.
	ConnectorFingerprint(destination string) (fingerprint string, ok bool)
}

// nopDispatcher is the fail-closed default: no destinations, every delivery
// declared-not-sent. NOT a silent no-op; it is the safest behavior and Start()
// warns once so the un-wired transport is VISIBLE.
type nopDispatcher struct{}

func (nopDispatcher) Destinations() []string { return nil }

func (nopDispatcher) DestinationsFor(model.TenantID) []string { return nil }

func (nopDispatcher) Deliver(context.Context, model.TenantID, string, sdk.Notification) error {
	return errNoDispatcher
}

func (nopDispatcher) ConnectorFingerprint(string) (string, bool) { return "", false }
