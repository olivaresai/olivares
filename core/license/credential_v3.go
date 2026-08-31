// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// credential_v3.go — the `olivares.commercial.credential.v3` container: ONE signed aggregate
// snapshot carrying a grant PER PURCHASED LINE, each with its own phase, guarantee window and
// lease.
//
// ============ WHY A SECOND CONTAINER AND NOT A FIELD ON THE FIRST ========================
// The v1/v2 blob in license.go attests ONE flat grant: a licensee, a plan, one expiry. A real
// v8 purchase is not one grant. It is a base product plus add-ons bought together, and E-1
// (an internal design note (not shipped):864, signed) put each of them on its own line with
// its own lifecycle — because they genuinely diverge: the money-back window is counted PER
// PRODUCT LINE, so a buyer can be past the window on Business while still inside it on an
// add-on bought later. `mixed_phase_allowed: true` (an internal design note (not shipped):176) is that fact
// written down. A flat container cannot represent it; flattening it back would mean picking
// one line's phase for all of them, which either extends a guarantee nobody is owed or
// terminates one that is.
//
// Every positive cohort in the captured Dodo corpus carries at least one add-on, so this is
// the ordinary case and not an edge one.
//
// ============ THE FREEZE, AND WHAT IN IT IS DERIVED RATHER THAN QUOTED ===================
// The v8 package (an internal design note (not shipped):646-681)
// prints the field list as a FLAT object and then says, in a note at :641-644, that E-1
// supersedes its SHAPE: the per-line fields move into `grants[]`, `product_id`/`addon_ids`
// leave the top level, and "los invariantes de validación de abajo se conservan aplicados por
// línea". It also says the definitive wire is frozen by the G-1 rebrief — phase 3 of
// an internal design note (not shipped):238. This file IS that freeze for the
// verifier, so the placement of every field is stated here with its reason:
//
//   ENVELOPE (one per credential) — identity of the document and of the deployment it binds:
//     schema, serial, issue_seq, key_id, key_epoch, issued_at, not_before, entity_id,
//     deployment_id, purpose, licensee, supersedes_serial, support_profile, max_users,
//     clock_policy, clock_key_id, clock_anchor_id, clock_generation.
//     These describe WHO holds the credential and HOW to trust it. None of them can differ
//     between two lines of one purchase: a single blob has one signature, one key, one
//     deployment binding and one clock policy.
//
//   PER GRANT (one per purchased line) — everything E-1 names, plus everything the v8
//   invariants compare LINE AGAINST LINE:
//     grant_id, order_line_id, product_id, kind, cadence, price_vintage,
//     paid_through, expires_at, issuance_phase, guarantee_deadline,
//     promotion_hold_deadline, lease_until, grace_reason, grace_ends_at.
//
//     `expires_at` and `paid_through` are per-grant and NOT envelope fields, and that is a
//     derivation rather than a quote. The invariants force it: "expires_at ≤ paid_through"
//     and "un add-on no puede superar el paid_through de Business" (:688, :697) are both
//     comparisons BETWEEN lines, which is unrepresentable if either lives once at the top.
//
//   `max_users` stays at the envelope because :698-699 fixes it at 0 = unlimited and forbids
//   any tier turning it into a positive number — a per-line copy would invite exactly that.
//
// ⚠ ONE CONTRADICTION IN THE SOURCES, RESOLVED HERE AND FLAGGED SO IT IS NOT INHERITED
// SILENTLY. an internal design note (not shipped):929 says `lease_until` is the runway "during refund-window
// AND hold", but :932 lists `allowed_issuance_phase: [refund-window]` only, and the
// `effective_paid_boundary` table at :940-943 has entries for refund-window, term and grace —
// and NONE for the hold. The canon contradicts itself in eight lines. This file follows the
// v8 package, which the canon projects (PRICING-CANON.md:1270-1272 makes the decision document
// authoritative on conflict): a lease is issued in BOTH provisional phases, and the effective
// boundary during the hold is min(paid_through, lease_until). The canon needs a PR to match.
//
// ⚠ AND ONE SYMBOL THAT MEANS TWO THINGS. The package (:559) defines `H` as the end of the
// NOMINAL hold, G+72h, and writes the lease clamp as min(now+72h, T, H). Signature E-3 then
// made the hold a MINIMUM that extends while the payment stays settled, capped by a published
// Hmax = min(paid_through, settlement+30d). Read as the nominal H, the clamp would make E-3
// inoperative — the lease could never follow the extension the signature grants. So `H` in the
// clamp is read here as Hmax, and the field that carries it is named
// `promotion_hold_deadline` for exactly that reason.
//
// ============ WHAT THIS FILE DOES NOT DO ================================================
// It does not decide the grant state machine and it does not issue. It parses, validates and
// reports. Verify NEVER blocks (see license.go): every error here means "no valid commercial
// credential", never "deny", so a malformed or hostile blob can never become an authorization
// bypass.

package license

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CredentialSchemaV3 is the only schema string this container accepts. It is compared
// EXACTLY: a credential that does not name itself is not a v3 credential, and guessing from
// shape is how a v4 with different semantics gets read under v3 rules.
const CredentialSchemaV3 = "olivares.commercial.credential.v3"

// IssuancePhase is the lifecycle phase of ONE grant. Closed set: a phase this build does not
// recognize never asserts a right, exactly like the classifier's closed status allow-list.
type IssuancePhase string

const (
	// PhaseRefundWindow — inside the voluntary money-back window. Provisional: the grant
	// runs on a short lease that must be refreshed.
	PhaseRefundWindow IssuancePhase = "refund_window"
	// PhasePromotionHold — the window closed with no terminal refund event; the grant is
	// waiting out the hold before promotion. STILL provisional, still on a lease (see the
	// contradiction note above).
	PhasePromotionHold IssuancePhase = "promotion_hold"
	// PhaseTerm — promoted. No lease; the right runs to paid_through.
	PhaseTerm IssuancePhase = "term"
	// PhaseRenewalGrace — the term ended on an INVOLUNTARY renewal failure and the issuer
	// attested a bounded extension. Never inferred by a verifier.
	PhaseRenewalGrace IssuancePhase = "renewal_grace"
)

// provisional reports whether the phase runs on a lease. Written as a method over the closed
// set rather than as a comparison at each call site, so adding a phase forces a decision here
// instead of silently defaulting to "not provisional" — which is the direction that grants.
func (p IssuancePhase) provisional() bool {
	return p == PhaseRefundWindow || p == PhasePromotionHold
}

func (p IssuancePhase) known() bool {
	switch p {
	case PhaseRefundWindow, PhasePromotionHold, PhaseTerm, PhaseRenewalGrace:
		return true
	}
	return false
}

// MaxLeaseFromIssue is the ceiling on a provisional lease measured from the credential's own
// issued_at (v8 package :691; PRICING-CANON.md:933 `maximum_hours_from_issue: 72`). It is a
// CEILING and not the lease itself: the issuer clamps to min(issued_at+72h, Hmax, paid_through).
const MaxLeaseFromIssue = 72 * time.Hour

// GrantKind separates the two id namespaces. Dodo product and add-on ids are opaque and could
// in principle collide across namespaces, and the invariant "an add-on may not outlast
// Business" (v8 :697) is unenforceable without knowing which line is which.
type GrantKind string

const (
	GrantKindBase  GrantKind = "base"
	GrantKindAddon GrantKind = "addon"
)

// Grant is one purchased line with its own lifecycle.
type Grant struct {
	GrantID     string    `json:"grant_id"`
	OrderLineID string    `json:"order_line_id"`
	ProductID   string    `json:"product_id"`
	Kind        GrantKind `json:"kind"`
	Cadence     string    `json:"cadence"`

	PaidThrough time.Time `json:"-"`
	ExpiresAt   time.Time `json:"-"`

	Phase IssuancePhase `json:"issuance_phase"`

	// GuaranteeDeadline is the end of the voluntary money-back window for THIS line, or the
	// zero time on a renewal that carries no new guarantee (v8 :694-696). Evidence of origin,
	// never authority on its own.
	GuaranteeDeadline time.Time `json:"-"`
	// PromotionHoldDeadline carries Hmax — see the symbol note in the file header.
	PromotionHoldDeadline time.Time `json:"-"`
	// LeaseUntil is the provisional runway. Zero in `term`.
	LeaseUntil time.Time `json:"-"`

	GraceReason string    `json:"grace_reason,omitempty"`
	GraceEndsAt time.Time `json:"-"`

	PriceVintage string `json:"price_vintage,omitempty"`
}

// Credential is a parsed, structurally-valid v3 aggregate snapshot.
type Credential struct {
	Schema     string    `json:"schema"`
	Serial     string    `json:"serial"`
	IssueSeq   int       `json:"issue_seq"`
	KeyID      string    `json:"key_id"`
	KeyEpoch   int       `json:"key_epoch"`
	IssuedAt   time.Time `json:"-"`
	NotBefore  time.Time `json:"-"`
	EntityID   string    `json:"entity_id"`
	Deployment string    `json:"deployment_id"`
	Purpose    string    `json:"purpose"`
	Licensee   string    `json:"-"`

	SupersedesSerial string `json:"supersedes_serial,omitempty"`
	SupportProfile   string `json:"support_profile,omitempty"`
	MaxUsers         int    `json:"max_users,omitempty"`

	ClockPolicy     string `json:"clock_policy,omitempty"`
	ClockKeyID      string `json:"clock_key_id,omitempty"`
	ClockAnchorID   string `json:"clock_anchor_id,omitempty"`
	ClockGeneration int    `json:"clock_generation,omitempty"`

	Grants []Grant `json:"-"`
}

// ---- the wire ---------------------------------------------------------------------------
// Times ride as RFC3339 STRINGS so the signed bytes are canonical and reproducible by any
// verifier, exactly as the v1/v2 wire does. Pointers distinguish "absent" from "the zero
// instant": a null lease and a lease at the Unix epoch are different claims, and only one of
// them is a bug.

type grantWire struct {
	GrantID               string  `json:"grant_id"`
	OrderLineID           string  `json:"order_line_id"`
	ProductID             string  `json:"product_id"`
	Kind                  string  `json:"kind"`
	Cadence               string  `json:"cadence"`
	PaidThrough           string  `json:"paid_through"`
	ExpiresAt             string  `json:"expires_at"`
	IssuancePhase         string  `json:"issuance_phase"`
	GuaranteeDeadline     *string `json:"guarantee_deadline"`
	PromotionHoldDeadline *string `json:"promotion_hold_deadline"`
	LeaseUntil            *string `json:"lease_until"`
	GraceReason           *string `json:"grace_reason,omitempty"`
	GraceEndsAt           *string `json:"grace_ends_at,omitempty"`
	PriceVintage          string  `json:"price_vintage,omitempty"`
}

type credentialWire struct {
	Schema       string `json:"schema"`
	Serial       string `json:"serial"`
	IssueSeq     int    `json:"issue_seq"`
	KeyID        string `json:"key_id"`
	KeyEpoch     int    `json:"key_epoch"`
	IssuedAt     string `json:"issued_at"`
	NotBefore    string `json:"not_before"`
	EntityID     string `json:"entity_id"`
	DeploymentID string `json:"deployment_id"`
	Purpose      string `json:"purpose"`
	Licensee     struct {
		DisplayName string `json:"display_name"`
	} `json:"licensee"`
	SupersedesSerial *string     `json:"supersedes_serial,omitempty"`
	SupportProfile   string      `json:"support_profile,omitempty"`
	MaxUsers         int         `json:"max_users,omitempty"`
	ClockPolicy      string      `json:"clock_policy,omitempty"`
	ClockKeyID       string      `json:"clock_key_id,omitempty"`
	ClockAnchorID    string      `json:"clock_anchor_id,omitempty"`
	ClockGeneration  int         `json:"clock_generation,omitempty"`
	Grants           []grantWire `json:"grants"`
}

// ErrNotV3 reports that the payload is not a v3 credential at all. Kept distinct from a
// validation error so a caller can fall back to the v1/v2 reader without treating a
// structurally-broken v3 as "try the old parser", which would silence real corruption.
var ErrNotV3 = errors.New("license: not an olivares.commercial.credential.v3 payload")

// ParseCredentialV3 parses and STRUCTURALLY validates a v3 payload.
//
// It does NOT check a signature and does NOT decide entitlement: signature verification is
// the caller's (the blob envelope is shared with v1/v2), and the phase evaluation is Evaluate
// below. Splitting them keeps this function total over its input — every rejection has a
// reason a human can act on, and none of them can be mistaken for "deny".
//
// STRICTNESS, all four rejections required by the v8 package (:633-634):
//   - unknown fields      — DisallowUnknownFields. A field we do not understand may be the
//     one that bounds the right.
//   - duplicate keys      — encoding/json silently keeps the LAST occurrence, so a payload
//     with two `paid_through` verifies under one and is read under the other. Checked
//     explicitly with a token scanner, because the standard decoder will not.
//   - non-canonical numbers — integers only, no exponents, no fractions.
//   - non-UTC timestamps  — an offset that is not Z is refused rather than converted: two
//     issuers writing the same instant differently would produce two different signed byte
//     strings for one credential.
func ParseCredentialV3(payload []byte) (Credential, error) {
	if err := rejectDuplicateKeys(payload); err != nil {
		return Credential{}, err
	}

	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var w credentialWire
	if err := dec.Decode(&w); err != nil {
		// A payload that does not NAME itself v3 is "not v3"; one that names v3 and then fails
		// to decode is a broken v3 and says so.
		//
		// The question is asked of the `schema` FIELD, never of the bytes. This used to be
		// bytes.Contains(payload, CredentialSchemaV3), and that literal is 33 bytes of
		// CLIENT-CONTROLLED text: a flat v2 license issued to an organization whose name
		// happened to contain it (licensee comes straight from the purchase —
		// commercial/license-worker/src/license/claims.ts:22) would be reported as a BROKEN v3
		// instead of "not v3", and a caller that fell back on ErrNotV3 would stop falling back.
		// Measured and named by the 2026-08-11 scope audit.
		// ErrNotV3 ONLY when no schema key is present. It is documented permission to fall back to
		// the v1/v2 reader, and handing that permission to a payload that names an UNKNOWN
		// container is how a future credential gets read under rules written for another one —
		// silently, because the flat decoder drops what it does not recognize. So a present-but-
		// unknown, empty or non-string schema propagates its own error instead. (2026-08-11 Codex
		// contrast, F-4.)
		if declaresNoSchema(payload) {
			return Credential{}, ErrNotV3
		}
		if _, cerr := containerOf(payload); cerr != nil {
			return Credential{}, cerr
		}
		return Credential{}, fmt.Errorf("license: v3 payload does not decode: %w", err)
	}
	if err := dec.Decode(new(struct{})); err != io.EOF {
		return Credential{}, errors.New("license: trailing data after the v3 payload")
	}
	if w.Schema != CredentialSchemaV3 {
		return Credential{}, ErrNotV3
	}

	c := Credential{
		Schema: w.Schema, Serial: w.Serial, IssueSeq: w.IssueSeq,
		KeyID: w.KeyID, KeyEpoch: w.KeyEpoch,
		EntityID: w.EntityID, Deployment: w.DeploymentID, Purpose: w.Purpose,
		Licensee: w.Licensee.DisplayName, SupportProfile: w.SupportProfile,
		MaxUsers: w.MaxUsers, ClockPolicy: w.ClockPolicy, ClockKeyID: w.ClockKeyID,
		ClockAnchorID: w.ClockAnchorID, ClockGeneration: w.ClockGeneration,
	}
	if w.SupersedesSerial != nil {
		c.SupersedesSerial = *w.SupersedesSerial
	}

	var err error
	if c.IssuedAt, err = utcTime("issued_at", &w.IssuedAt); err != nil {
		return Credential{}, err
	}
	if c.NotBefore, err = utcTime("not_before", &w.NotBefore); err != nil {
		return Credential{}, err
	}

	// Mandatory for ANY paid credential (v8 :685-687). Empty or absent is INVALID and never
	// "perpetual" — the v8 package removed the perpetual right entirely, so a missing bound
	// is a broken credential and not an unbounded one.
	for _, f := range []struct {
		name, val string
	}{
		{"serial", c.Serial}, {"key_id", c.KeyID}, {"entity_id", c.EntityID},
		{"deployment_id", c.Deployment}, {"licensee.display_name", c.Licensee},
	} {
		if strings.TrimSpace(f.val) == "" {
			return Credential{}, fmt.Errorf("license: v3 %s is empty; a paid credential without it is invalid, never perpetual", f.name)
		}
	}
	switch c.Purpose {
	case "production", "staging":
	default:
		return Credential{}, fmt.Errorf("license: v3 purpose %q is not production or staging", c.Purpose)
	}
	if c.MaxUsers != 0 {
		// :698-699 — 0 means unlimited and no tier may turn it into a positive number. A
		// credential that tries is refused rather than clamped, so the attempt is visible.
		return Credential{}, fmt.Errorf("license: v3 max_users must be 0 (unlimited); %d would reintroduce a seat cap", c.MaxUsers)
	}
	if len(w.Grants) == 0 {
		return Credential{}, errors.New("license: v3 credential carries no grants; an aggregate snapshot of nothing attests nothing")
	}

	seenGrant := make(map[string]struct{}, len(w.Grants))
	seenLine := make(map[string]struct{}, len(w.Grants))
	bases := 0
	for i := range w.Grants {
		g, err := parseGrant(&w.Grants[i], c.IssuedAt)
		if err != nil {
			return Credential{}, fmt.Errorf("license: v3 grant %d (%s): %w", i, w.Grants[i].OrderLineID, err)
		}
		if _, dup := seenGrant[g.GrantID]; dup {
			return Credential{}, fmt.Errorf("license: v3 grant_id %q appears twice", g.GrantID)
		}
		if _, dup := seenLine[g.OrderLineID]; dup {
			return Credential{}, fmt.Errorf("license: v3 order_line_id %q appears twice", g.OrderLineID)
		}
		seenGrant[g.GrantID] = struct{}{}
		seenLine[g.OrderLineID] = struct{}{}
		if g.Kind == GrantKindBase {
			bases++
		}
		c.Grants = append(c.Grants, g)
	}

	// One base per credential: `base_dependency: every-addon-requires-effective-business-grant`
	// (PRICING-CANON.md:925). Zero bases would leave add-ons with nothing to depend on; two
	// would make "the Business paid_through" ambiguous in the invariant below.
	if bases != 1 {
		return Credential{}, fmt.Errorf("license: v3 credential must carry exactly one base grant, found %d", bases)
	}
	var basePaidThrough time.Time
	for _, g := range c.Grants {
		if g.Kind == GrantKindBase {
			basePaidThrough = g.PaidThrough
		}
	}
	for _, g := range c.Grants {
		if g.Kind == GrantKindAddon && g.PaidThrough.After(basePaidThrough) {
			// v8 :697. An add-on outliving its base would keep a paid module alive after the
			// product that hosts it stopped being paid for.
			return Credential{}, fmt.Errorf(
				"license: v3 add-on %q is paid through %s, past its base at %s",
				g.OrderLineID, g.PaidThrough.Format(time.RFC3339), basePaidThrough.Format(time.RFC3339))
		}
	}
	return c, nil
}

func parseGrant(w *grantWire, issuedAt time.Time) (Grant, error) {
	g := Grant{
		GrantID: w.GrantID, OrderLineID: w.OrderLineID, ProductID: w.ProductID,
		Kind: GrantKind(w.Kind), Cadence: w.Cadence, Phase: IssuancePhase(w.IssuancePhase),
		PriceVintage: w.PriceVintage,
	}
	for _, f := range []struct{ name, val string }{
		{"grant_id", g.GrantID}, {"order_line_id", g.OrderLineID}, {"product_id", g.ProductID},
	} {
		if strings.TrimSpace(f.val) == "" {
			return Grant{}, fmt.Errorf("%s is empty; a paid grant without it is invalid, never perpetual", f.name)
		}
	}
	switch g.Kind {
	case GrantKindBase, GrantKindAddon:
	default:
		return Grant{}, fmt.Errorf("kind %q is neither base nor addon", w.Kind)
	}
	switch g.Cadence {
	case "month", "year":
	default:
		return Grant{}, fmt.Errorf("cadence %q is neither month nor year", w.Cadence)
	}
	if !g.Phase.known() {
		return Grant{}, fmt.Errorf("issuance_phase %q is not a phase this build knows; an unknown phase never asserts a right", w.IssuancePhase)
	}

	var err error
	if g.PaidThrough, err = utcTime("paid_through", &w.PaidThrough); err != nil {
		return Grant{}, err
	}
	if g.ExpiresAt, err = utcTime("expires_at", &w.ExpiresAt); err != nil {
		return Grant{}, err
	}
	if g.GuaranteeDeadline, err = optionalUTC("guarantee_deadline", w.GuaranteeDeadline); err != nil {
		return Grant{}, err
	}
	if g.PromotionHoldDeadline, err = optionalUTC("promotion_hold_deadline", w.PromotionHoldDeadline); err != nil {
		return Grant{}, err
	}
	if g.LeaseUntil, err = optionalUTC("lease_until", w.LeaseUntil); err != nil {
		return Grant{}, err
	}
	if g.GraceEndsAt, err = optionalUTC("grace_ends_at", w.GraceEndsAt); err != nil {
		return Grant{}, err
	}
	if w.GraceReason != nil {
		g.GraceReason = *w.GraceReason
	}

	// ---- the per-phase invariants (v8 :688-696) -----------------------------------------
	switch {
	case g.Phase.provisional():
		// lease_until == expires_at <= min(issued_at + 72h, promotion_hold_deadline)
		if g.LeaseUntil.IsZero() {
			return Grant{}, fmt.Errorf("phase %s carries no lease_until; a provisional grant with no runway is not provisional, it is unbounded", g.Phase)
		}
		if !g.LeaseUntil.Equal(g.ExpiresAt) {
			return Grant{}, fmt.Errorf("phase %s must have lease_until == expires_at (%s vs %s)",
				g.Phase, g.LeaseUntil.Format(time.RFC3339), g.ExpiresAt.Format(time.RFC3339))
		}
		if ceiling := issuedAt.Add(MaxLeaseFromIssue); g.LeaseUntil.After(ceiling) {
			return Grant{}, fmt.Errorf("lease_until %s is past the %s ceiling from issue (%s)",
				g.LeaseUntil.Format(time.RFC3339), MaxLeaseFromIssue, ceiling.Format(time.RFC3339))
		}
		if g.PromotionHoldDeadline.IsZero() {
			return Grant{}, errors.New("a provisional grant carries no promotion_hold_deadline; without it the lease has no cap to be clamped against")
		}
		if g.LeaseUntil.After(g.PromotionHoldDeadline) {
			return Grant{}, fmt.Errorf("lease_until %s is past promotion_hold_deadline %s",
				g.LeaseUntil.Format(time.RFC3339), g.PromotionHoldDeadline.Format(time.RFC3339))
		}
	case g.Phase == PhaseTerm:
		if !g.LeaseUntil.IsZero() {
			return Grant{}, errors.New("phase term must carry no lease_until; a promoted grant that still leases was never promoted")
		}
		if !g.ExpiresAt.Equal(g.PaidThrough) {
			return Grant{}, fmt.Errorf("phase term must have expires_at == paid_through (%s vs %s)",
				g.ExpiresAt.Format(time.RFC3339), g.PaidThrough.Format(time.RFC3339))
		}
	case g.Phase == PhaseRenewalGrace:
		// The ONLY phase allowed past paid_through, and only on an issuer-attested
		// involuntary failure with a bounded end (LICENSING.md §ADR: 168h, once per 365 days).
		if g.GraceReason != "renewal_failure" {
			return Grant{}, fmt.Errorf("phase renewal_grace needs grace_reason=renewal_failure, got %q", g.GraceReason)
		}
		if g.GraceEndsAt.IsZero() {
			return Grant{}, errors.New("phase renewal_grace carries no grace_ends_at; an unbounded grace is a perpetual right by another name")
		}
		// A grace is an EXTENSION past the paid term (PRICING-CANON.md:935,943), so its end must
		// come after paid_through. Only the upper bound was checked, and a NEGATIVE duration
		// passes any "greater than the maximum" test: an inverted window was accepted and then
		// SHORTENED the right, because EffectiveBoundary returns grace_ends_at in this phase. A
		// grant in "grace" that ends before its own paid term is not a grace, and it reported
		// expired while the attested term was still running. (2026-08-11 Codex contrast, F-5.)
		if !g.GraceEndsAt.After(g.PaidThrough) {
			return Grant{}, fmt.Errorf("phase renewal_grace ends at %s, which is not after paid_through %s; a grace extends the term, it never shortens it",
				g.GraceEndsAt.Format(time.RFC3339), g.PaidThrough.Format(time.RFC3339))
		}
		if g.GraceEndsAt.Sub(g.PaidThrough) > MaxGracePeriod {
			return Grant{}, fmt.Errorf("grace of %s exceeds the published maximum %s",
				g.GraceEndsAt.Sub(g.PaidThrough), MaxGracePeriod)
		}
	}

	// expires_at <= paid_through, except an explicit renewal grace (v8 :688-689).
	if g.Phase != PhaseRenewalGrace && g.ExpiresAt.After(g.PaidThrough) {
		return Grant{}, fmt.Errorf("expires_at %s is past paid_through %s outside a renewal grace",
			g.ExpiresAt.Format(time.RFC3339), g.PaidThrough.Format(time.RFC3339))
	}
	return g, nil
}

// EffectiveBoundary is the instant this grant stops conferring its right, by phase
// (PRICING-CANON.md:939-943, with the hold added — see the contradiction note in the header).
//
//	refund_window / promotion_hold -> min(paid_through, lease_until)
//	term                           -> paid_through
//	renewal_grace                  -> grace_ends_at
func (g Grant) EffectiveBoundary() time.Time {
	switch {
	case g.Phase.provisional():
		if g.LeaseUntil.Before(g.PaidThrough) {
			return g.LeaseUntil
		}
		return g.PaidThrough
	case g.Phase == PhaseRenewalGrace:
		return g.GraceEndsAt
	default:
		return g.PaidThrough
	}
}

// Active reports whether this grant confers its right at `now`. Half-open intervals
// (PRICING-CANON.md:132 `intervals: half-open`): the boundary instant is NOT included, so a
// grant and its successor never both hold at the same moment.
func (g Grant) Active(now time.Time) bool {
	return now.Before(g.EffectiveBoundary())
}

// ActiveGrants returns the lines that CONFER A RIGHT at `now`, in wire order.
//
// It is a FILTER and never a summary: with mixed phases allowed, "is this credential valid"
// has no single answer, and any code that wants one has to say which line it is asking about.
// Returning a boolean here is how an add-on's expiry would silently become the base's.
//
// ⚠ AN ADD-ON WHOSE BASE HAS LAPSED CONFERS NOTHING, whatever its own window still says.
// `base_dependency: every-addon-requires-effective-business-grant` (PRICING-CANON.md:925) is the
// canon's rule and this function claims to answer exactly the question it governs. Until the
// 2026-08-11 Codex contrast (F-3) it filtered on each line's own upper bound alone, so a
// credential could report itself EXPIRED — Status follows the base, correctly — while this list
// handed a caller an add-on to render as live. Two answers to one question, from adjacent methods.
//
// Grant.Active stays what it is: one line against its own boundary, with no view of the rest.
// The dependency lives here because it is a fact about the credential, not about a line.
func (c Credential) ActiveGrants(now time.Time) []Grant {
	if now.Before(c.NotBefore) {
		return nil
	}
	base, ok := c.BaseGrant()
	if !ok || !base.Active(now) {
		return nil
	}
	out := make([]Grant, 0, len(c.Grants))
	for _, g := range c.Grants {
		if g.Active(now) {
			out = append(out, g)
		}
	}
	return out
}

// BaseGrant returns the credential's single base line. ParseCredentialV3 refuses any credential
// that does not carry exactly one, so `ok` is false only on a zero Credential — but it is
// returned rather than assumed, because a caller reaching for the base of something it did not
// parse is exactly where an index-0 assumption would rot.
func (c Credential) BaseGrant() (Grant, bool) {
	for _, g := range c.Grants {
		if g.Kind == GrantKindBase {
			return g, true
		}
	}
	return Grant{}, false
}

// Status is the credential-wide answer for the surfaces that can only show ONE word — the
// server-info line, the CLI status header, the boot log — and it is the BASE grant's status BY
// DEFINITION, not by merging the lines.
//
// The definition comes from the canon and not from this file: `base_dependency:
// every-addon-requires-effective-business-grant` (PRICING-CANON.md:925, the same rule
// ParseCredentialV3 enforces when it demands exactly one base). A credential whose base has
// lapsed confers nothing whatever its add-on lines still say, so the base's status IS the
// credential's — and in the other direction it never reports more than the base supports.
//
// It is NOT a summary of the grants and must not be used as one: with mixed phases allowed, "is
// this credential valid" has no single answer per line. ActiveGrants is the honest question, and
// every surface that can show more than one word shows that instead.
//
// Before not_before the credential confers nothing yet. The closed status set has no "not yet
// valid", so this reports the state that grants nothing rather than inventing one that does.
func (c Credential) Status(now time.Time) Status {
	if now.Before(c.NotBefore) {
		return StatusExpired
	}
	base, ok := c.BaseGrant()
	if !ok || !base.Active(now) {
		return StatusExpired
	}
	if base.Phase == PhaseRenewalGrace {
		// The phase IS the grace: the term ended on an involuntary renewal failure and the issuer
		// attested a bounded extension. Reporting "valid" would hide a billing failure the
		// operator has to act on.
		return StatusGrace
	}
	return StatusValid
}

// RevokedBy reports whether r names this credential or fences its signing epoch.
//
// ⚠ NAMED GAP, not an oversight: r.HolderIDs is NOT matched. The flat container's holder_id is
// the payment provider's subscription/customer/order id (commercial/license-worker/src/polar/
// events.ts:79); a v3 credential carries entity_id and deployment_id, which the issuer has not
// yet been wired to fill from that namespace. Matching them would assert an identity nobody
// established, and matching them WRONGLY revokes a paying customer. So this matches on the two
// facts that are unambiguous in both containers — the credential's own serial, and the
// signing-key epoch against issued_at — and the holder axis is reported as follow-up work for
// whoever wires issuance. It is strictly more than today: today the CRL path never parses a v3
// credential at all, so no axis matches.
func (c Credential) RevokedBy(r Revocation) bool {
	if c.Serial != "" {
		for _, serial := range r.Serials {
			if serial != "" && serial == c.Serial {
				return true
			}
		}
	}
	return r.LicenseKeyEpoch > 0 &&
		!c.IssuedAt.IsZero() &&
		c.IssuedAt.Unix() < r.LicenseKeyEpoch
}

// StatusWithRevocation reports revocation before every expiry status, exactly as the flat
// container does. Display/attestation only: nothing here blocks.
func (c Credential) StatusWithRevocation(now time.Time, r Revocation) Status {
	if c.RevokedBy(r) {
		return StatusRevoked
	}
	return c.Status(now)
}

// ---- strictness helpers ------------------------------------------------------------------

// utcTime parses a REQUIRED RFC3339 instant and refuses any offset other than Z.
func utcTime(field string, s *string) (time.Time, error) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return time.Time{}, fmt.Errorf("%s is required and empty", field)
	}
	return parseUTC(field, *s)
}

func optionalUTC(field string, s *string) (time.Time, error) {
	if s == nil {
		return time.Time{}, nil
	}
	if strings.TrimSpace(*s) == "" {
		return time.Time{}, fmt.Errorf("%s is present but empty; use null for absent", field)
	}
	return parseUTC(field, *s)
}

func parseUTC(field, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is not RFC3339: %w", field, err)
	}
	// REFUSED, not converted. "+00:00" and "Z" are the same instant and DIFFERENT bytes; a
	// verifier that normalised them would accept two distinct signed payloads for one
	// credential, and the canonical-bytes guarantee would stop being a guarantee.
	if !strings.HasSuffix(s, "Z") {
		return time.Time{}, fmt.Errorf("%s must be UTC with a trailing Z, got %q", field, s)
	}
	return t.UTC(), nil
}

// rejectDuplicateKeys refuses an object that names the same key twice, at any depth.
//
// encoding/json keeps the LAST occurrence and reports nothing. That is not a style problem: a
// payload carrying two `paid_through` values is signed over bytes that contain both, so a
// verifier reading one and a tool reading the other disagree about when the right ends while
// both hold a valid signature. The standard decoder cannot see it, so the token stream is
// walked here.
func rejectDuplicateKeys(payload []byte) error {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()

	// A frame per open container. `isObject` says which kind, `seen` collects that object's
	// keys, and `wantKey` tracks the key/value alternation.
	//
	// THE ALTERNATION IS TRACKED EXPLICITLY, and the first version of this function did not:
	// it asked `dec.More()` to tell a key from a value. That is wrong — More() reports whether
	// another element follows, which is true after a key AND after a value — so every VALUE
	// string was recorded as a key. The golden fixture caught it immediately, because
	// `issued_at` and `not_before` legitimately carry the SAME instant and the walker called
	// that a duplicate. A detector that fires on a correct document is worse than none: it
	// would have taught the next person to delete it.
	type frame struct {
		isObject bool
		wantKey  bool
		seen     map[string]struct{}
	}
	var stack []frame

	// valueSeen is called after any complete value; inside an object the next token is a key.
	valueSeen := func() {
		if n := len(stack); n > 0 && stack[n-1].isObject {
			stack[n-1].wantKey = true
		}
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("license: v3 payload is not valid JSON: %w", err)
		}

		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				stack = append(stack, frame{isObject: true, wantKey: true, seen: map[string]struct{}{}})
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				// The container that just closed WAS a value of whatever encloses it.
				valueSeen()
			}
			continue
		}

		// A scalar. Inside an object it is either the key or the value, and which one is
		// decided by the alternation rather than guessed from its content.
		if n := len(stack); n > 0 && stack[n-1].isObject && stack[n-1].wantKey {
			key, ok := tok.(string)
			if !ok {
				return fmt.Errorf("license: v3 payload has a non-string object key %v", tok)
			}
			if _, dup := stack[n-1].seen[key]; dup {
				return fmt.Errorf("license: v3 payload names %q twice in one object; a signed blob with two values for one field is ambiguous", key)
			}
			stack[n-1].seen[key] = struct{}{}
			stack[n-1].wantKey = false
			continue
		}

		if num, ok := tok.(json.Number); ok {
			// Canonical integers only (v8 :633-634): no exponent, no fraction. A number the
			// issuer wrote as 1e0 and a verifier re-encodes as 1 breaks byte reproducibility.
			if s := num.String(); strings.ContainsAny(s, ".eE") {
				return fmt.Errorf("license: v3 payload carries the non-canonical number %s; integers only", s)
			}
		}
		valueSeen()
	}
}
