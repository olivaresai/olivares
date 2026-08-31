// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// Durable rollout state: how a control that did not exist in an earlier release
// behaves on a deployment that predates it.
//
// The problem it solves is narrow and was found in the egress destination
// policy. That control reads its operator configuration as a tri-state — absent
// permits, authored-empty denies, unreadable denies and retries — and "absent
// permits" is the only upgrade-safe reading of a file nobody has written yet:
// anything else breaks every subscription already in the field the moment the
// binary carrying the control is deployed.
//
// But the tri-state cannot tell a FRESH INSTALL from an UPGRADE, and on a fresh
// install "absent permits" is allow-all with no expiry date, in the module whose
// product thesis is governing egress. The install has no history to protect and
// no compatibility debt to pay; it is simply ungoverned by default, forever, and
// nothing in the product says so.
//
// The missing fact is therefore not a configuration setting but a piece of the
// deployment's own history: did this deployment exist before the control did? A
// setting cannot answer it (an operator would have to know, and a wrong answer is
// silent), and the data cannot be asked at runtime (a later boot would re-derive
// it from a history that has since moved on, which is how a deploy silently
// changes the rule a live estate runs under). So it is classified ONCE, when the
// answer is free, and recorded durably.
//
// The shape follows the unit-D precedent for audit metadata blinding: a
// global, un-guarded bookkeeping row, and a transition that is one-way in the
// direction that matters. It departs from that precedent in one way, and the
// departure is the correction an adversarial review of unit G produced: the
// CLASSIFIED fact and the CURRENT mode are separate columns, because one column
// asked to be both history and current state loses the history the first time an
// operator decides anything. See reconcileAuditBlindingState for the precedent
// and classifyRolloutControls for this.

// RolloutMode is a control's disposition on a deployment.
//
// There are three, and the third one is the point. Leaving "this deployment does
// not require the control to be configured" reachable ONLY as the absence of a
// decision is the failure this repository has already paid for once: the ownerless
// clause withdrawn from the SIEM-egress contract was
// indistinguishable from a decision because nobody had made one. An operator who
// genuinely does not want to author the control can say so, and then it is a
// decision with an owner, a timestamp and a recorded reason instead of a gap.
type RolloutMode string

const (
	// RolloutEnforced is the control in force: its configuration is authoritative
	// and an ABSENT configuration denies rather than permits. This is what a fresh
	// install is CLASSIFIED as, because it has nothing to grandfather.
	RolloutEnforced RolloutMode = "enforced"

	// RolloutLegacyCompat is the transitional state a deployment that predates the
	// control is classified as. The control's configuration still applies where it is
	// authored, but the exact entitlements the deployment already had keep working so
	// that installing a new binary is not a breaking change.
	//
	// It is never a TRANSITION TARGET. A deployment is classified into it once and
	// leaves it once, because it honors an unbounded set of grandfathered
	// entitlements collected from the deployment's own history and re-entering it
	// would reopen every one of them at once.
	RolloutLegacyCompat RolloutMode = "legacy_compat"

	// RolloutPolicyOptional is a deliberate, recorded decision that this deployment does
	// not require the control to be configured: an ABSENT configuration permits, as it
	// did before the control existed.
	//
	// It is named for exactly what it does. An earlier draft called it "unrestricted",
	// and an adversarial review showed the name promised something the semantics do
	// not deliver: an AUTHORED policy still decides, so "unrestricted" would have been
	// STRICTER than compatibility mode whenever a policy existed — compatibility still
	// honors its exceptions. A mode whose name and behavior disagree is worse than
	// no mode.
	//
	// It is reachable only until a deployment has COMMITTED to enforcement. After that
	// the way to permit a destination is to author it — scoped, per tenant, auditable —
	// rather than to relax the control globally.
	RolloutPolicyOptional RolloutMode = "policy_optional"
)

// Valid reports whether m is one of the three modes.
func (m RolloutMode) Valid() bool {
	switch m {
	case RolloutEnforced, RolloutLegacyCompat, RolloutPolicyOptional:
		return true
	}
	return false
}

// ValidClassification reports whether m is a mode the ENGINE may classify a
// deployment into. RolloutPolicyOptional is not one: it is a decision, and the
// engine does not make decisions on an operator's behalf.
func (m RolloutMode) ValidClassification() bool {
	switch m {
	case RolloutEnforced, RolloutLegacyCompat:
		return true
	}
	return false
}

// controlKeyRE constrains a control key to a dotted, versioned identifier. The key
// is a PRIMARY KEY value in a shared table, so an unconstrained one would let two
// modules collide by accident; the version suffix exists so that a control whose
// SEMANTICS change ships as a new key and classifies afresh instead of inheriting a
// disposition that was decided about a different rule.
var controlKeyRE = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9_]+)*\.v[0-9]+$`)

// witnessTableRE mirrors the module-table naming rule (docs/contracts).
var witnessTableRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// RolloutControl declares a control whose default disposition depends on whether
// the deployment predates it. A module registers it through ExtensionRegistry
// alongside its entity descriptors, and the engine — not the module — classifies
// it, once, under the migration lock and before any schema is created.
type RolloutControl struct {
	// Key identifies the control durably, e.g. "eventing.egress.destination.v1".
	Key string

	// WitnessTable is the physical table whose PRIOR existence is evidence that this
	// deployment ran the witnessing module before the control existed.
	//
	// It is CONSERVATIVE EVIDENCE and not a lineage oracle, and the distinction is
	// worth stating because an earlier draft of this design overstated it. Present
	// means an entitlement COULD have been authored without the gate — even if the
	// table is empty now, because a deployment may have created and deleted rows.
	// Absent, together with the corroborating checks the engine runs, means it could
	// not. What presence cannot do is distinguish a partial restore from a lineage,
	// which is why contradictory evidence fails the boot instead of being guessed at
	// (see classifyRolloutControls).
	//
	// It must name a table the same module registers, which the engine checks: a typo
	// would otherwise classify every upgrade as fresh — the unsafe direction, because
	// a fresh classification grandfathers nothing.
	WitnessTable string

	// LegacyMode is what a deployment whose witness table already existed is
	// classified as. FreshMode is what a deployment meeting the witness table for the
	// first time is classified as. Both must be classifiable modes.
	//
	// They are declared rather than assumed so that a control whose safe default runs
	// the other way — safe to enforce on an upgrade, needing easing in on a fresh
	// install — can say so.
	LegacyMode RolloutMode
	FreshMode  RolloutMode
}

// Validate checks the declaration. It is called by the engine at registration.
func (c RolloutControl) Validate() error {
	if !controlKeyRE.MatchString(c.Key) {
		return fmt.Errorf("%w: rollout control key %q must be a dotted lower-case identifier ending in a version, e.g. \"eventing.egress.destination.v1\"", ErrInvalidDescriptor, c.Key)
	}
	if !witnessTableRE.MatchString(c.WitnessTable) {
		return fmt.Errorf("%w: rollout control %q witness table %q is not a legal table name", ErrInvalidDescriptor, c.Key, c.WitnessTable)
	}
	if !c.LegacyMode.ValidClassification() {
		return fmt.Errorf("%w: rollout control %q legacy mode %q is not a mode the engine may classify into (%q or %q)", ErrInvalidDescriptor, c.Key, c.LegacyMode, RolloutEnforced, RolloutLegacyCompat)
	}
	if !c.FreshMode.ValidClassification() {
		return fmt.Errorf("%w: rollout control %q fresh mode %q is not a mode the engine may classify into (%q or %q)", ErrInvalidDescriptor, c.Key, c.FreshMode, RolloutEnforced, RolloutLegacyCompat)
	}
	return nil
}

// RolloutState is the durable record of one control's disposition.
type RolloutState struct {
	// Key is the control this state belongs to.
	Key string

	// ClassifiedMode is what the ENGINE decided when it first met this deployment. It
	// is IMMUTABLE: no transition rewrites it, so "how was this deployment
	// classified?" remains answerable after any number of operator decisions. An
	// earlier draft kept one mode column and overwrote it, which destroyed exactly
	// that answer — over the record whose entire purpose is to be durable.
	ClassifiedMode RolloutMode
	// CurrentMode is the disposition in force now.
	CurrentMode RolloutMode

	// EnforcementCommitted records that somebody DECIDED to make this control
	// authoritative. It is ONE-WAY, and it is the only irreversible fact here.
	//
	// It is separate from CurrentMode because a fresh install is already enforced
	// without anybody having decided anything, and only a DECISION licenses the engine
	// to refuse a later relaxation. Conflating them would make the legitimate posture —
	// a laboratory deployment that does not want to author the control — unreachable on
	// precisely the installs where it is the honest choice.
	//
	// An earlier draft called this "compatibility retired" and set it on ANY deliberate
	// decision, which made the rules contradict each other: choosing RolloutPolicyOptional
	// would have retired compatibility and thereby forbidden the mode that had just been
	// chosen. The fact worth recording is narrower and this is it. Compatibility being
	// unreachable needs no flag at all: RolloutLegacyCompat is never a transition target,
	// so a deployment that has left it can never return whatever else it does.
	EnforcementCommitted bool

	// Generation counts state versions, starting at 1 for the classified seed. A
	// transition supplies the generation it believes is current, so two operators
	// racing the same ceremony cannot both win.
	Generation int64

	// ClassifiedAt is when the engine decided the seed. WitnessKind and WitnessDetail
	// record HOW, so the classification can be audited after the fact rather than
	// re-derived — re-deriving is the failure this whole record prevents.
	ClassifiedAt  time.Time
	WitnessKind   string
	WitnessDetail string

	// DecidedAt, DecidedBy and DecidedReason describe the most recent deliberate
	// transition; they are zero until one happens. The FULL history lives in the
	// append-only transition log, because these three columns only ever describe the
	// latest write.
	DecidedAt     time.Time
	DecidedBy     string
	DecidedReason string
}

// RolloutTransition is a deliberate change of a control's disposition.
type RolloutTransition struct {
	// Key is the control to move.
	Key string
	// Mode is the disposition to move it to. RolloutLegacyCompat is never a legal
	// target.
	Mode RolloutMode
	// Actor identifies who decided. The caller is responsible for deriving it from an
	// AUTHENTICATED principal: this layer records what it is given and cannot
	// authenticate anything, which is why a caller that passes request text or an OS
	// username produces a record that says only that.
	Actor string
	// Reason is the operator's justification or change-ticket reference. It is
	// REQUIRED: a rollout decision with no recorded reason is the ownerless state this
	// mechanism exists to eliminate, arriving by another route.
	Reason string
	// Evidence is the opaque, control-specific proof the decision was taken against —
	// for the egress control, digests of the policy, the seed coverage and the
	// blocked-destination diff. It is stored verbatim in the transition log so a later
	// audit can ask what the operator was actually looking at.
	Evidence string
	// ExpectGeneration is the generation the caller read before deciding. The
	// transition applies only if it is still current.
	ExpectGeneration int64
}

// RolloutTransitionRecord is one entry of the append-only decision history.
type RolloutTransitionRecord struct {
	Key        string
	Generation int64
	FromMode   RolloutMode
	ToMode     RolloutMode
	// Committed reports the state of the one-way enforcement commitment AFTER this
	// transition.
	Committed bool
	At        time.Time
	Actor     string
	Reason    string
	Evidence  string
}

// RolloutStater is the capability a store exposes for reading and moving durable
// rollout state. It is a capability interface rather than a method on Store for
// the same reason AuditSpoolStatuser is: a store that cannot answer simply does
// not implement it, and the composition root — never a module — holds it.
//
// The key is an opaque string here on purpose. This layer must not learn what any
// particular control means; it keeps the history and enforces the generic state
// machine, and the module that owns the control interprets the mode and proves the
// control-specific preconditions before asking for a transition.
type RolloutStater interface {
	// RolloutState reads a control's durable state. It returns ErrNotFound when no
	// control with that key was classified, which for a registered control means the
	// row is missing and the caller must treat the answer as UNAVAILABLE — never as
	// permissive.
	RolloutState(ctx context.Context, key string) (RolloutState, error)

	// RolloutHistory returns the append-only decision history, oldest first.
	RolloutHistory(ctx context.Context, key string) ([]RolloutTransitionRecord, error)

	// SetRolloutMode applies a deliberate transition and returns the new state. It
	// refuses a stale generation, a mode outside the closed set, RolloutLegacyCompat as
	// a target, an empty actor or reason, and RolloutPolicyOptional once enforcement has
	// been committed.
	//
	// It writes the state change and its history entry in ONE transaction. It does NOT
	// write a tenant audit event, and cannot: this layer has no tenant bound and the
	// audit ledger is tenant-guarded. A caller that needs the decision in the evidence
	// ledger must record it through a path that can, and must not present two
	// independently committing writes as one audited act.
	SetRolloutMode(ctx context.Context, t RolloutTransition) (RolloutState, error)
}
