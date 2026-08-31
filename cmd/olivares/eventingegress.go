// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// eventingegress.go is the composition-root adapter for the egress destination
// policy: the operator's ceiling on where a tenant may point its own event stream.
//
// It follows the operator-config pattern the notify dispatcher already uses
// (loadNotifyDestinations / loadOperatorJSONConfig): a path in the environment, read
// once at boot, and a read or syntax error is FATAL rather than a silent downgrade —
// starting less governed than the operator asked for is the failure mode this whole
// family of loaders exists to prevent.
//
// The policy lives in operator config rather than in a tenant table on purpose. It
// is a CEILING: the point is that the tenant role which creates subscriptions
// (editor, eventing:subscription:write) cannot widen it. A per-tenant table an
// in-tenant admin can author would be a useful second layer, and it would not be
// this one.

// envEventingEgressPolicy names the file holding the destination policy.
const envEventingEgressPolicy = "OLIVARES_EVENTING_EGRESS_POLICY"

// eventingEgressFile is the on-disk shape. `tenants` overrides `default` for the
// named tenants; a tenant with its own entry does NOT inherit the default, because
// an inherited allow-list would silently widen a policy an operator wrote to be
// exact.
type eventingEgressFile struct {
	// Default applies to every tenant without an entry in Tenants.
	Default *eventingEgressPolicyDoc `json:"default,omitempty"`
	// Tenants maps a tenant id to its own policy.
	Tenants map[string]eventingEgressPolicyDoc `json:"tenants,omitempty"`
}

// eventingEgressPolicyDoc is one policy as an operator writes it. `allow` being
// present-but-empty is an authored deny-all and is honored as one; the way to mean
// "unconstrained" is to omit the tenant (or the whole file), not to write `[]`.
type eventingEgressPolicyDoc struct {
	Allow []egress.Rule `json:"allow"`
}

// eventingEgressPolicy is the wired adapter. A nil *eventingEgressPolicy is a valid
// "no policy configured" value, and the module treats a nil source as absent.
type eventingEgressPolicy struct {
	def     *egress.Policy
	tenants map[model.TenantID]egress.Policy
}

var _ eventing.EgressPolicySource = (*eventingEgressPolicy)(nil)

// EgressPolicy returns the policy in force for a tenant.
func (p *eventingEgressPolicy) EgressPolicy(_ context.Context, tenant model.TenantID) (egress.Policy, error) {
	if p == nil {
		return egress.Policy{}, nil
	}
	if pol, ok := p.tenants[tenant]; ok {
		return pol, nil
	}
	if p.def != nil {
		return *p.def, nil
	}
	// No default and no entry: absent, which permits. This is the tri-state's third
	// arm and it is what keeps an upgrade from breaking every subscription already in
	// the field on an estate whose operator has not written a policy yet.
	return egress.Policy{}, nil
}

// loadEventingEgressPolicy reads the operator's policy. It returns (nil, nil) when
// the operator configured none.
func loadEventingEgressPolicy(getenv func(string) string) (*eventingEgressPolicy, error) {
	path := strings.TrimSpace(getenv(envEventingEgressPolicy))
	if path == "" {
		return nil, nil
	}
	var doc eventingEgressFile
	if err := loadOperatorJSONConfig(envEventingEgressPolicy, path, &doc); err != nil {
		return nil, err
	}
	out := &eventingEgressPolicy{}
	if doc.Default != nil {
		pol := egress.Policy{InForce: true, Allow: doc.Default.Allow, Ref: envEventingEgressPolicy + ":default"}
		if err := pol.Validate(); err != nil {
			return nil, fmt.Errorf("%s: default policy: %w", envEventingEgressPolicy, err)
		}
		out.def = &pol
	}
	if len(doc.Tenants) > 0 {
		out.tenants = make(map[model.TenantID]egress.Policy, len(doc.Tenants))
		for raw, d := range doc.Tenants {
			t, present, err := parseBusinessTenant(envEventingEgressPolicy+": tenant key", raw)
			if err != nil {
				return nil, err
			}
			if !present {
				return nil, fmt.Errorf("%s: a blank tenant key is not a tenant", envEventingEgressPolicy)
			}
			pol := egress.Policy{InForce: true, Allow: d.Allow, Ref: envEventingEgressPolicy + ":tenant"}
			if err := pol.Validate(); err != nil {
				return nil, fmt.Errorf("%s: tenant %q: %w", envEventingEgressPolicy, raw, err)
			}
			out.tenants[t] = pol
		}
	}
	if out.def == nil && len(out.tenants) == 0 {
		// The operator pointed at a file that constrains nothing. Refusing is the
		// honest answer: they asked for a control and would otherwise get none,
		// silently, with a green boot to suggest otherwise.
		return nil, fmt.Errorf("%s is set to %q but the file declares neither a default nor any tenant policy", envEventingEgressPolicy, path)
	}
	return out, nil
}

// eventingEgressRollout adapts the store's durable rollout capability to the seam the
// eventing module reads. It is the unit-G half of this file: the policy above is
// operator CONFIGURATION, and this is a fact about the deployment's own HISTORY that
// no configuration could supply.
//
// The two are deliberately separate. Moving the policy content into the database as
// well would create two writers for one authority — a file and a table — and the
// precedence between them has to be designed before either can be trusted. The
// install lineage needs no such design: it is written once, by the engine, before any
// module table exists.
type eventingEgressRollout struct {
	src store.RolloutStater
}

var _ eventing.EgressRolloutSource = eventingEgressRollout{}

// EgressRollout reads the durable state. A store that does not implement the
// capability, or a missing row, is an ERROR and never a permissive default: the module
// turns it into a retryable denial, because "this plane could not establish whether
// the control is in force" must never be delivered as "the control is not in force".
func (r eventingEgressRollout) EgressRollout(ctx context.Context) (store.RolloutState, error) {
	if r.src == nil {
		return store.RolloutState{}, fmt.Errorf("eventing: this store does not expose durable rollout state, so the disposition of %q cannot be established", eventing.EgressRolloutControlKey)
	}
	return r.src.RolloutState(ctx, eventing.EgressRolloutControlKey)
}

// newEventingEgressRollout adapts a store if it carries the capability. It returns
// (zero, false) when it does not, so the caller decides what to do about it rather
// than silently receiving an adapter that always errors.
func newEventingEgressRollout(st store.Store) (eventingEgressRollout, bool) {
	rs, ok := st.(store.RolloutStater)
	if !ok {
		return eventingEgressRollout{}, false
	}
	return eventingEgressRollout{src: rs}, true
}

// eventingWriterFence adapts the store's durable rollout capability to the seam the writer fence
// reads (unit H). It is a SECOND control key, not a second copy of the first: see
// eventing.EgressWriterFenceControlKey for why the fence needs its own epoch.
type eventingWriterFence struct {
	src store.RolloutStater
}

var _ eventing.EgressWriterFenceSource = eventingWriterFence{}

// EgressWriterFence reads the fence's durable state. A store without the capability, or a missing
// row, is an ERROR and never a dormant fence: the module turns it into a refusal, because "this
// plane could not establish whether the fence is armed" must never be delivered as "the fence is
// not armed".
func (r eventingWriterFence) EgressWriterFence(ctx context.Context) (store.RolloutState, error) {
	if r.src == nil {
		return store.RolloutState{}, fmt.Errorf("eventing: this store does not expose durable rollout state, so the disposition of %q cannot be established", eventing.EgressWriterFenceControlKey)
	}
	return r.src.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
}

// newEventingWriterFence adapts a store if it carries the capability.
func newEventingWriterFence(st store.Store) (eventingWriterFence, bool) {
	rs, ok := st.(store.RolloutStater)
	if !ok {
		return eventingWriterFence{}, false
	}
	return eventingWriterFence{src: rs}, true
}

// cliEventingEndpointChecker builds the SAME destination check the API applies, for
// the CLI path that writes the endpoint column directly. It is a function rather
// than a duplicated rule set because a second copy is exactly how the two answers
// drift apart — the defect this closes was that one path had the rules and the other
// had none.
//
// It takes the OPEN STORE, and that is a requirement rather than a convenience. Since
// unit G the answer depends on the deployment's durable disposition and, under
// compatibility mode, on the destinations it already had — neither of which is in the
// JSON file. A checker built from the file alone would report "no policy in force,
// permitted" on a fresh install where every delivery is refused, and the CLI would be
// the third copy of a destination rule in this campaign to be the one that was wrong.
//
// A nil store is accepted for the paths that genuinely have none, and it is the strict
// direction: no disposition means the module's own nil-source reading, which is the
// pre-unit-G behavior, and no compatibility record means an exception cannot be
// found — so an existing grandfathered destination reads as refused rather than as
// permitted.
func cliEventingEndpointChecker(st store.Store, purpose eventing.EgressPurpose, subRef model.ID) (eventing.EndpointChecker, error) {
	pol, err := loadEventingEgressPolicy(os.Getenv)
	if err != nil {
		return eventing.EndpointChecker{}, err
	}
	var (
		rollout eventing.EgressRolloutSource
		data    eventing.EgressCompatStore
	)
	if st != nil {
		data = st
		if adapter, ok := newEventingEgressRollout(st); ok {
			rollout = adapter
		} else {
			return eventing.EndpointChecker{}, fmt.Errorf("this store does not expose durable rollout state, so the egress destination control's disposition cannot be established")
		}
	}
	var polSrc eventing.EgressPolicySource
	if pol != nil {
		polSrc = pol
	}
	c := eventing.NewEndpointChecker(eventingAllowLoopback(os.Getenv), polSrc, rollout, data, nil)
	// The PURPOSE, not only the reference. Setting the reference alone left the checker at its
	// create default, and a create deliberately ignores the reference — so `subscriptions test`
	// asked as a destination that does not exist yet and refused the very endpoint whose real
	// deliveries succeed through an exact compatibility exception. Its own comment claimed it
	// asked "AS the subscription"; it did not.
	c.Purpose = purpose
	c.SubscriptionRef = subRef
	return c, nil
}

// cliGuardedClient is the engine's guarded client, not a copy of it. The copy this
// replaces refused any dial address that was not already an IP literal — while an
// http.Transport hands its dialer "hostname:port" — so every hostname destination was
// unreachable, and it carried a smaller reserved-address set than the engine's.
func cliGuardedClient() *http.Client {
	return eventing.GuardedClient(eventingAllowLoopback(os.Getenv))
}
