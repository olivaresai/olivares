// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

// Seam — emergent workload-identity assertion/exchange formats. The live SVID
// path above (X.509-SVID + JWT-SVID over the Workload API, JWT-bearer WIF exchange)
// is the STABLE, deployable identity story. Beyond it, two IETF efforts are
// emerging for cross-domain workload identity:
//
//   - WIMSE (Workload Identity in a Multi System Environment): the WIT/WPT token
//     model and workload-to-workload HTTP-message-signature auth — verified pre-RFC
//     drafts as of jun-2026: draft-ietf-wimse-arch-07 (2026-03-02, Informational)
//     and draft-ietf-wimse-http-signature-03 (2026-04-07, Standards Track).
//   - ID-JAG / identity chaining: cross-domain token exchange —
//     draft-ietf-oauth-identity-chaining-14, expiring 2026-12-04 (a -15 is
//     anticipated; IESG "Revised I-D Needed").
//
// These are ACTIVE PRE-RFC DRAFTS that expire late 2026. Per the no-fabrication rule
// we declare the seam DENY-CLOSED behind a feature flag and do NOT
// implement against the drafts — wire shapes can still change. Concretizes this
// once a draft stabilizes (CSA's Agent Identity Governance Framework names SPIFFE/SVID
// the preferred orchestrator-identity credential, so this is the forward path, not a
// replacement of the SVID path).

import (
	"context"
	"errors"
)

// ErrEmergentIdentityDisabled is returned by the deny-closed default seam. It is not
// a failure of configuration but the honest state: the emergent format is not
// implemented (the draft is not final), so the caller must use the stable SVID path.
var ErrEmergentIdentityDisabled = errors.New("spiffe: emergent identity (WIMSE/ID-JAG) is disabled — pre-RFC drafts are not implemented; use the SVID/WIF path")

// EmergentIdentitySeam is the placeholder for an emergent workload-identity provider
// (WIMSE WIT/WPT, ID-JAG token exchange). It mirrors the Workload's audience-bound
// fetch so a future backend can slot in where FetchAnthropicAssertion sits today,
// without changing the egress call sites.
type EmergentIdentitySeam interface {
	// PresentWorkloadIdentity returns an assertion/credential for the given audience.
	// The deny-closed default returns ErrEmergentIdentityDisabled.
	PresentWorkloadIdentity(ctx context.Context, audience string) (string, error)
}

// disabledEmergentSeam is the deny-closed default: it never produces a credential.
type disabledEmergentSeam struct{}

func (disabledEmergentSeam) PresentWorkloadIdentity(context.Context, string) (string, error) {
	return "", ErrEmergentIdentityDisabled
}

// EmergentIdentity returns the configured emergent-identity seam. The feature flag
// is OFF by default and there is no in-tree backend, so it always returns the
// deny-closed seam today; the parameter exists so the composition root threads the
// operator flag through to the (future) backend selection without an API change.
func EmergentIdentity(enabled bool) EmergentIdentitySeam {
	// enabled is honored deny-closed: with no backend compiled in, even enabled=true
	// yields the disabled seam (we never pretend a draft is implemented).
	_ = enabled
	return disabledEmergentSeam{}
}
