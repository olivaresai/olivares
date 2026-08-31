// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package license verifies Olivares commercial licenses offline with Ed25519
// (LICENSING.md). It depends only on the standard library.
//
// OPEN-CORE, not pure dual license. The default (AGPL) binary is the COMPLETE
// open product and never reads a license: it does not disable, degrade or block
// any feature, request or boot on a license check, and it runs air-gapped with no
// license server. On top of that open core sits a small, ADDITIVE commercial line
// (enterprise/, built only with -tags enterprise, absent from the public binary);
// the commercial license is BOTH the legal AGPL exception and the entitlement to
// that line (LICENSING.md).
//
// What this package does: it VERIFIES a signed license offline and reports its
// attested claims. Verify NEVER blocks — any error means "no valid commercial
// license", never "deny" — so license-spoofing is closed as an authorization
// bypass and authorization stays solely in the RBAC/ABAC path. The OPEN product
// only DISPLAYS that status (`server-info`, `olivares version`). The code that
// CONSUMES an attested claim is the closed enterprise build, which entitles its
// additive add-ons from it. Claims.MaxUsers is NOT such a consumer any more: since
// B10 (2026-07-27) self-hosted user accounts are unlimited in every tier, the seat
// seam in core/auth refuses nothing, and no license state can cap or degrade the
// account estate. Forging is cryptographically impossible without the dedicated
// private key held by the narrowly scoped license Worker.
//
// SELF-ATTESTED — never a SERVER-side entitlement input. The status this package
// reports is SELF-attested by a customer-controlled binary against whatever key
// the build embedded. No OLIVARES server-side entitlement, billing, metering,
// support or cloud-plan decision may key off it. No managed cloud is deployed
// today; when one lands it MUST derive a tenant's plan SOLELY from its own
// Merchant-of-Record billing record, never from the engine's self-report
// (LICENSING.md). The MoR is named in an internal design note (not shipped); whichever provider
// it is, the entitlement record is the MoR's, never this blob. Any entitlement the enterprise build derives
// from these claims is a LOCAL decision in the customer's own commercial build
// honoring its own signed license, not an Olivares server decision.
//
// A license blob is "base64url(payloadJSON).base64url(ed25519-signature)" where
// the signature is over the exact payload JSON bytes. Verification is offline
// against the build's embedded public key (embedded.go): development builds carry
// the public dev key; a release build (`-tags release`) carries the real Olivares
// license key injected at link time. Its dedicated private half is held only by the
// narrowly scoped online license-issuance Worker; it is never an OTA signing key.
// Expiry is checked against the local clock — a clock rollback defeats it (an
// honest offline limit, not an enforcement signal). Revocation snapshots are
// caller-supplied display/attestation evidence and NEVER make Verify reject a
// blob. The open binary only displays that evidence; only the enterprise build
// consumes it for its add-on entitlements. The 14-day grace after first observing
// a CRL revocation lives in the enterprise overlay, not in this package.
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Verification errors. None of these should ever block a request or boot — a
// caller treats any error as "no valid commercial license" and continues.
var (
	// ErrMalformed means the blob is not a well-formed license.
	ErrMalformed = errors.New("license: malformed")
	// ErrBadSignature means the signature did not verify against the public key.
	ErrBadSignature = errors.New("license: signature invalid")
)

// Claims are the attested facts of a license. They are display/record only.
type Claims struct {
	// Licensee is the organization the exception is granted to.
	Licensee string `json:"licensee"`
	// Plan is a free-form plan label (e.g. "commercial").
	Plan string `json:"plan,omitempty"`
	// SupportTier is the attested commercial support relationship the licensee
	// purchased (e.g. "standard", "enterprise"; empty = none/community). It is a
	// free-form DISPLAY/RECORD label like Plan — informational only, NEVER gates,
	// degrades or blocks anything, and is consumed by no part of the binary. The
	// support relationship is legal/commercial, not a feature flag; this claim only
	// lets a deployment's own console/CLI DISPLAY which tier its license attests
	// (SUPPORT.md). Per LICENSING.md the key-custody rule, no Olivares-side support,
	// billing or cloud-plan decision may trust this self-report — the source of truth
	// for an actual support entitlement is the Merchant-of-Record record.
	SupportTier string `json:"support_tier,omitempty"`
	// HolderID is an opaque holder identifier.
	HolderID string `json:"holder_id,omitempty"`
	// Serial uniquely identifies this license. An empty serial identifies a legacy
	// blob, which can be revoked only by holder ID or license-key epoch.
	Serial string `json:"serial,omitempty"`
	// Profile describes the issuance environment (online, airgapped or trial).
	// It is a free-form display/record value; unknown values remain valid claims.
	Profile string `json:"profile,omitempty"`
	// Features lists attested feature tags (informational only; never gates).
	Features []string `json:"features,omitempty"`
	// MaxTenants is an attested soft figure (informational only; never enforced).
	MaxTenants int `json:"max_tenants,omitempty"`
	// MaxUsers is an attested seat figure kept for wire compatibility with licenses
	// issued before B10 (2026-07-27). 0 means UNLIMITED and is what every license
	// issued from now on carries: self-hosted user accounts are unlimited in every
	// tier, so NO build turns this number into a runtime limit — it is display-only
	// everywhere, exactly like MaxTenants.
	MaxUsers int `json:"max_users,omitempty"`
	// IssuedAt is when the license was signed.
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is when the term ends. Every v8 offer is TERM-ONLY, so the zero
	// time is NOT a perpetual grant: it is a blob that attests no term, and Status
	// reports it as expired. (It used to mean perpetual; the v8 package removed the
	// perpetual right entirely and LICENSING.md §ADR-0010 signs "no perpetual fallback".)
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// GraceUntil is the instant the ISSUER attests the entitlement survives to after
	// ExpiresAt. It is SIGNED, never inferred: whether a lapse was involuntary, and
	// whether this holder has already spent its one allowance in the rolling 365
	// days, are facts of the Merchant-of-Record record that no verifier can
	// reconstruct from a blob. Absent (the zero time) therefore means ZERO grace —
	// the conservative reading, because a verifier must not manufacture a right
	// nobody granted. Status also refuses a window wider than MaxGracePeriod, so a
	// buggy or over-generous issuance cannot recreate a quasi-perpetual term.
	GraceUntil time.Time `json:"grace_until,omitempty"`
}

// wire is the JSON shape on the cable, with times as RFC3339 strings so the
// signed bytes are canonical and reproducible by any verifier.
type wire struct {
	Licensee    string   `json:"licensee"`
	Plan        string   `json:"plan,omitempty"`
	SupportTier string   `json:"support_tier,omitempty"`
	HolderID    string   `json:"holder_id,omitempty"`
	Serial      string   `json:"serial,omitempty"`
	Profile     string   `json:"profile,omitempty"`
	Features    []string `json:"features,omitempty"`
	MaxTenants  int      `json:"max_tenants,omitempty"`
	MaxUsers    int      `json:"max_users,omitempty"`
	IssuedAt    string   `json:"issued_at"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	// GraceUntil rides LAST so every blob issued before it existed encodes to the
	// exact same bytes it always did: omitempty drops it, and the signature over
	// those bytes is unchanged. Adding it anywhere else would move the field order
	// of the canonical payload and invalidate every golden vector.
	GraceUntil string `json:"grace_until,omitempty"`
}

// Status is the lifecycle status of a verified license.
type Status string

// The license statuses (informational; none changes behavior).
const (
	// StatusValid means signed and not expired.
	StatusValid Status = "valid"
	// StatusGrace means signed and inside its profile-specific expiry grace period.
	StatusGrace Status = "grace"
	// StatusExpired means signed but past its expiry (or past its attested grace).
	StatusExpired Status = "expired"
	// StatusPerpetual meant "signed with no expiry". Since the v8 package every offer
	// is term-only and there is no perpetual right, so Status NEVER returns this any
	// more. The constant is deliberately kept exported — an SDK, a console build or
	// the closed overlay that still switches on it keeps compiling and keeps behaving
	// — exactly as core/auth keeps ErrUserCapRequiresEnterprise after B10 removed the
	// cap. Removing the RIGHT is not the same as removing the SYMBOL.
	StatusPerpetual Status = "perpetual"
	// StatusRevoked means a supplied revocation snapshot matches the license.
	StatusRevoked Status = "revoked"
)

// NormalizedProfile returns a supported profile, defaulting empty and unknown
// display/record values to online for lifecycle calculations.
func (c Claims) NormalizedProfile() string {
	switch c.Profile {
	case "online", "airgapped", "trial":
		return c.Profile
	default:
		return "online"
	}
}

// MaxGracePeriod is the widest post-expiry window the canon allows an issuance to
// attest: G = T+168h (an internal design note (not shipped), LICENSING.md §ADR-0010). The verifier
// enforces it as a STRUCTURAL upper bound even though the issuer owns the decision
// to grant, because a signed-but-wrong grace_until in the distant future would
// otherwise recreate the perpetual right the v8 package removed. The issuer still
// owns "was the lapse involuntary" and "has this holder already spent its one
// allowance in 365 rolling days" — neither is reconstructible here.
const MaxGracePeriod = 168 * time.Hour

// GracePeriod returns the ATTESTED grace window: GraceUntil − ExpiresAt, clamped to
// MaxGracePeriod, and zero whenever the issuance attested none. It no longer infers
// a window from the profile.
//
// It used to return 90d (airgapped), 30d (online) or 0 (trial) — figures no signature
// ever carried, which meant two independent display paths computed a deadline the
// product did not honor (cmd/olivares/license_holder.go, cmd/olivares/cmd_license.go
// both render ExpiresAt.Add(GracePeriod())). Returning the signed delta keeps those
// callers correct without touching them: with no attested grace they now render the
// expiry itself, which is the truth.
//
// The method is retained rather than deleted so every existing caller keeps compiling;
// what changed is that it can no longer manufacture an entitlement.
func (c Claims) GracePeriod() time.Duration {
	if c.ExpiresAt.IsZero() || c.GraceUntil.IsZero() {
		return 0
	}
	d := c.GraceUntil.Sub(c.ExpiresAt)
	if d <= 0 {
		return 0
	}
	if d > MaxGracePeriod {
		return MaxGracePeriod
	}
	return d
}

// Status reports the license status relative to now. Expiry is checked against
// the local clock (clock rollback defeats it — an honest offline limit). It does
// not evaluate revocation; callers with a snapshot use StatusWithRevocation.
//
// The classifier, and every branch of it is deliberate:
//
//	no ExpiresAt                     -> expired  (term-only: no term attested, no right)
//	now <= ExpiresAt                 -> valid
//	no attested grace                -> expired  (a verifier never invents a window)
//	grace not after the expiry       -> expired  (a malformed window grants nothing)
//	now <= attested grace (<= 168h)  -> grace
//	otherwise                        -> expired
//
// None of this blocks anything in the open binary: Status is a DISPLAY fact there
// (LICENSING.md §ADR-0010), and Verify still never rejects a blob for any of these reasons.
// What it does change is what the CLOSED build is entitled to consume, which is the
// only place a commercial term was ever meant to bite.
func (c Claims) Status(now time.Time) Status {
	if c.ExpiresAt.IsZero() {
		return StatusExpired
	}
	if !now.After(c.ExpiresAt) {
		return StatusValid
	}
	grace := c.GracePeriod() // zero unless the issuance attested a window
	if grace <= 0 {
		return StatusExpired
	}
	if !now.After(c.ExpiresAt.Add(grace)) {
		return StatusGrace
	}
	return StatusExpired
}

// Revocation is a caller-supplied revocation snapshot. LicenseKeyEpoch is a UTC
// Unix timestamp in seconds; zero means that no signing-key fence is present.
// Empty list entries never match empty legacy claim fields.
type Revocation struct {
	Serials         []string
	HolderIDs       []string
	LicenseKeyEpoch int64
}

// RevokedBy reports whether r names this license or fences its signing epoch.
// A legacy blob with no serial can be revoked only by holder ID or key epoch.
func (c Claims) RevokedBy(r Revocation) bool {
	if c.Serial != "" {
		for _, serial := range r.Serials {
			if serial != "" && serial == c.Serial {
				return true
			}
		}
	}
	if c.HolderID != "" {
		for _, holderID := range r.HolderIDs {
			if holderID != "" && holderID == c.HolderID {
				return true
			}
		}
	}
	return r.LicenseKeyEpoch > 0 &&
		!c.IssuedAt.IsZero() &&
		c.IssuedAt.Unix() < r.LicenseKeyEpoch
}

// StatusWithRevocation reports revocation before every expiry status, including
// perpetual. This is display/attestation only: Verify NEVER blocks, and the
// enterprise overlay owns any post-observation CRL grace before consumption.
func (c Claims) StatusWithRevocation(now time.Time, r Revocation) Status {
	if c.RevokedBy(r) {
		return StatusRevoked
	}
	return c.Status(now)
}

// GenerateKey returns a fresh Ed25519 keypair for signing licenses.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("license: generate key: %w", err)
	}
	return pub, priv, nil
}

// Sign produces a license blob signing c with priv.
func Sign(c Claims, priv ed25519.PrivateKey) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("license: bad private key size %d", len(priv))
	}
	payload, err := json.Marshal(toWire(c))
	if err != nil {
		return "", fmt.Errorf("license: marshal: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sig), nil
}

// Verify checks the blob's signature against pub and returns the FLAT claims of a v1/v2
// license. It does NOT check expiry (a caller reads Claims.Status for that) and never
// gates: an error here means "treat as no commercial license", not "deny".
//
// It is deliberately NOT the container router. The envelope is shared with the v3 aggregate
// credential (envelope.go), which carries a grant per purchased line and therefore has no
// flat claim set to return — building one would mean picking one line's expiry for all of
// them. So a v3 blob is refused HERE with ErrCredentialV3, a name a caller can route on,
// and read THERE with VerifyEnvelope. New consumers should call VerifyEnvelope.
func Verify(blob string, pub ed25519.PublicKey) (Claims, error) {
	v, err := VerifyEnvelope(blob, pub)
	if err != nil {
		return Claims{}, err
	}
	if v.Container != ContainerFlat {
		return Claims{}, ErrCredentialV3
	}
	return v.Claims, nil
}

func toWire(c Claims) wire {
	w := wire{
		Licensee: c.Licensee, Plan: c.Plan, SupportTier: c.SupportTier, HolderID: c.HolderID,
		Serial: c.Serial, Profile: c.Profile,
		Features: c.Features, MaxTenants: c.MaxTenants, MaxUsers: c.MaxUsers,
		IssuedAt: c.IssuedAt.UTC().Format(time.RFC3339),
	}
	if !c.ExpiresAt.IsZero() {
		w.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if !c.GraceUntil.IsZero() {
		w.GraceUntil = c.GraceUntil.UTC().Format(time.RFC3339)
	}
	return w
}

func fromWire(w wire) (Claims, error) {
	c := Claims{
		Licensee: w.Licensee, Plan: w.Plan, SupportTier: w.SupportTier, HolderID: w.HolderID,
		Serial: w.Serial, Profile: w.Profile,
		Features: w.Features, MaxTenants: w.MaxTenants, MaxUsers: w.MaxUsers,
	}
	if w.IssuedAt != "" {
		t, err := time.Parse(time.RFC3339, w.IssuedAt)
		if err != nil {
			return Claims{}, err
		}
		c.IssuedAt = t
	}
	if w.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, w.ExpiresAt)
		if err != nil {
			return Claims{}, err
		}
		c.ExpiresAt = t
	}
	if w.GraceUntil != "" {
		t, err := time.Parse(time.RFC3339, w.GraceUntil)
		if err != nil {
			return Claims{}, err
		}
		c.GraceUntil = t
	}
	return c, nil
}

// EncodeKey base64-encodes key material for storage/transport.
func EncodeKey(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// DecodePublicKey parses a base64 Ed25519 public key.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: invalid public key")
	}
	return ed25519.PublicKey(b), nil
}

// DecodePrivateKey parses a base64 Ed25519 private key.
func DecodePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("license: invalid private key")
	}
	return ed25519.PrivateKey(b), nil
}
