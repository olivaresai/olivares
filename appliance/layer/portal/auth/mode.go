// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package auth decides who may ask the Appliance Console for an act, in each of the three
// states this appliance can be in, and decides nothing else.
//
// While the product answers, the product's own identity decides and a dangerous act steps
// up through the product's existing authorizer. This package adds no second decision path:
// it asks, once, and returns what the product answered, keeping no decision of its own.
//
// While the product does not answer, PAM answers the only question PAM is asked here —
// who is this human logging in to this host — for a member of the named local
// administrators group, and the repair verbs alone are offered. In that mode a step-up is
// a re-authentication, because no policy engine is running to ask.
//
// When the product does not answer and this console's own sign-in stack cannot be used,
// nobody signs in over the network at all, and the remaining door is the console on tty1.
//
// Nothing here is a session and nothing here is stored: an offer carries no credential, no
// token and no expiry, so every act asks again. PAM is never a session identity and never
// a provider-account identity; the account contract in the product's core owns that
// boundary and this package never crosses it. The package holds no password, no hash and
// no credential store of its own: the only check of a human's credential is the one the
// host's stack performs inside a transaction opened and closed here. It names verbs; it
// implements none.
package auth

// Mode is the sign-in mode in force. Every page of the console states it.
type Mode string

const (
	// ProductUp is the normal mode: the product answers, so the product's identity
	// decides and everything the console has is offered.
	ProductUp Mode = "product-up"
	// ProductDownRepair is the repair mode: the product does not answer, so a human in
	// the named local administrators group signs in through the host's own stack and only
	// the repair verbs are offered.
	ProductDownRepair Mode = "product-down-repair"
	// Unavailable is the refusing mode: nobody signs in over the network, because the
	// product does not answer and this console's sign-in stack is unusable or was never
	// measured. The remaining door is the console on tty1.
	Unavailable Mode = "unavailable"
)

// String names the mode. Any other value — including the zero value of Mode, which is what
// a caller that measured nothing holds — reads as Unavailable, so an unset mode opens no
// door.
func (m Mode) String() string {
	switch m {
	case ProductUp, ProductDownRepair, Unavailable:
		return string(m)
	}
	return string(Unavailable)
}

// AdministratorsGroup is the named local administrators group: the group whose members,
// and nobody else, this console admits while the product is down. It is a local group,
// because the question answered in that mode is who this human is on this host; the group
// is the host's own record of who administers it, so reading it grants no right of its own.
const AdministratorsGroup = "olivares-admins"

// PAMService is the service name this console opens its transactions for, which on a
// Debian host selects the stack in the file of that name under /etc/pam.d. The layer ships
// that file: a service with no file of its own does not fail closed there, because the
// fallback stack includes the host's common authentication scheme, so an absent file would
// admit every local account instead of admitting nobody.
const PAMService = "olivares-portal"

// Verb names one act an operator may ask this console for. This package decides who may
// ask: a verb here is a name and an audit label, never an effect. A verb's effect belongs to
// its helper, in the layer's helper seam, never to this package.
type Verb string

const (
	// VerbApplyUpdate applies an approved update.
	VerbApplyUpdate Verb = "apply-update"
	// VerbRollBack rolls the host back to the previous snapshot.
	VerbRollBack Verb = "roll-back"
	// VerbNetwork changes addresses, routes, name resolution or the firewall policy.
	VerbNetwork Verb = "network"
	// VerbCertificate replaces the console's TLS certificate.
	VerbCertificate Verb = "certificate"
	// VerbSupportBundle produces a support bundle.
	VerbSupportBundle Verb = "support-bundle"
	// VerbPower reboots or shuts down the host.
	VerbPower Verb = "power"
	// VerbVPN changes the VPN. It is not a repair verb — it is offered only while the
	// product answers — and it is named here because it is one of the acts that always
	// require a step-up.
	VerbVPN Verb = "vpn"
	// VerbRepairPortalPAM repairs this console's own sign-in stack. Only the console on
	// tty1 carries it, because over the network that stack is the thing that is broken.
	VerbRepairPortalPAM Verb = "repair-portal-pam"
)

// RepairVerbs are the verbs the repair mode offers and the console on tty1 carries, in a
// fixed order. They are the whole of what a sign-in through the host's stack is offered.
// The result is a fresh list, so writing to it cannot widen the next offer.
func RepairVerbs() []Verb {
	return []Verb{VerbApplyUpdate, VerbRollBack, VerbNetwork, VerbCertificate, VerbSupportBundle, VerbPower}
}

// RequiresStepUp reports whether v is one of the acts that always require a step-up:
// applying an update, rolling back, changing the network, changing the VPN, replacing the
// certificate and powering the host. Producing a support bundle does not. Repairing the
// sign-in stack does not either: it is offered only on the repair console on tty1, which no
// act over the network can reach. There, as for power, the step-up is not access to this
// machine: every ordinary act and every repair needs the console's own qualified sign-in.
//
// Which acts demand a step-up is settled here; whether this operator has satisfied one is
// the product's answer while it is up, and a re-authentication on the host while it is not.
func (v Verb) RequiresStepUp() bool {
	switch v {
	case VerbApplyUpdate, VerbRollBack, VerbNetwork, VerbVPN, VerbCertificate, VerbPower:
		return true
	}
	return false
}

// The fixed sentences a refusing mode states. Each names a check and carries no path, no
// address and no credential material, so a page may state it to any caller.
const (
	reasonNoSelector        = "No sign-in mode has been selected on this console."
	reasonProductUnmeasured = "The product's reachability is unmeasured, and an unmeasured product is not a product that is down."
	reasonNoStack           = "This console has no sign-in stack wired."
	reasonStackUnusable     = "This console's sign-in stack cannot be used."
)

// State is the sign-in mode in force and why. It is a measurement, not a permission: it
// says which door is open, never who came through it.
type State struct {
	// Mode is the mode in force.
	Mode Mode
	// Reason is a fixed sentence naming what put the console in this mode, empty when the
	// mode needs none.
	Reason string
}

// OffersEverything reports whether the mode offers every act this console has, which only
// the product's own identity does.
func (s State) OffersEverything() bool { return s.Mode == ProductUp }

// NetworkVerbs are the verbs this mode offers over the network. The repair mode offers the
// repair verbs and nothing else; the refusing mode offers nothing at all. While the product
// answers the answer is not a list, because the product decides act by act: ask
// OffersEverything instead.
func (s State) NetworkVerbs() []Verb {
	if s.Mode == ProductDownRepair {
		return RepairVerbs()
	}
	return nil
}

// Statement is the sentence every page states: the mode in force, what decides in it and,
// when nobody may sign in over the network, the door that remains.
func (s State) Statement() string {
	var says string
	switch s.Mode {
	case ProductUp:
		says = "Sign-in mode " + string(ProductUp) + ": the product answers, so the product's own identity " +
			"decides and a dangerous act steps up through the product's authorizer."
	case ProductDownRepair:
		says = "Sign-in mode " + string(ProductDownRepair) + ": the product does not answer, so a human in " +
			"the " + AdministratorsGroup + " group signs in on this host and only the repair verbs are offered."
	default:
		says = "Sign-in mode " + string(Unavailable) + ": nobody signs in over the network. The remaining " +
			"door is the repair console on tty1, which carries the same repair verbs and is the only " +
			"surface that can repair this console's sign-in stack."
	}
	if s.Reason == "" {
		return says
	}
	return says + " " + s.Reason
}
