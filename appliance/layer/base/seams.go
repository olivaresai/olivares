// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import "context"

// ProductPorts are the listeners the product declares (cmd/olivares/binddefaults.go): the
// console and API on 8443 and gRPC on 8444. A firewall adapter measures a policy that
// admits these and the operator's SSH, and nothing else.
var ProductPorts = []string{"8443/tcp", "8444/tcp"}

// RefusingSetupDelivery is the setup-token seam until the product's setup-token owner
// offers a protected delivery sink: the product's first start prints the token to its
// standard output, which a systemd unit sends to the journal. This seam never mints,
// prints or stores a token, and first boot does not start the product while it refuses.
// Once the owner ships a sink flag, the product-configuration stage passes it to the
// generator, so the configuration hash it records covers the flag, and this stage only
// verifies the sink it names; it never rewrites the product configuration.
type RefusingSetupDelivery struct{}

// Apply refuses.
func (RefusingSetupDelivery) Apply(context.Context, Input) (Effect, error) {
	return "", Refuse("the product has no protected setup-token delivery sink yet; it belongs to the setup-token owner")
}

// Verify refuses: no delivery can have been recorded through this seam.
func (RefusingSetupDelivery) Verify(context.Context, Input, Effect) error {
	return Refuse("a recorded setup-token delivery cannot be verified without the product's delivery sink")
}

// UnmeasuredFirewall is the firewall seam until an operating-system firewall owner is
// selected and its policy measured. No service is exposed beyond loopback while it refuses.
type UnmeasuredFirewall struct{}

// Apply refuses.
func (UnmeasuredFirewall) Apply(context.Context, Input) (Effect, error) {
	return "", Refuse("the host firewall prerequisite is unmeasured; no service is exposed beyond loopback")
}

// Verify refuses: no firewall measurement can have been recorded through this seam.
func (UnmeasuredFirewall) Verify(context.Context, Input, Effect) error {
	return Refuse("the host firewall prerequisite is unmeasured; no service is exposed beyond loopback")
}
