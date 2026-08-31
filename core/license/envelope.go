// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// envelope.go — the ONE place that decides which signed container a blob holds.
//
// ============ WHY THIS FILE EXISTS ========================================================
// The blob envelope — `base64url(payload).base64url(ed25519-signature)` — is SHARED by two
// containers: the flat v1/v2 claim set in license.go and the `olivares.commercial.credential.v3`
// aggregate in credential_v3.go. Nothing decided between them, and the consequence was measured
// on 2026-08-11 rather than reasoned about:
//
//	core/license/license.go:127                        wire.Licensee is a STRING
//	commercial/license-worker/.../credential-v3.ts:211 the issuer emits licensee as an OBJECT
//	ParseCredentialV3                                  zero production callers
//	Verify(signed v3 vector, correct key)              -> "license: malformed"
//
// so every consumer — the ten call sites in cmd/olivares and core/api — rejected the credential
// this project signs, before a single grant was evaluated. The cryptography was correct and
// nobody invoked it.
//
// ============ THE ORDER IS THE SECURITY PROPERTY, AND IT IS NOT NEGOTIABLE ================
// The signature is checked over the payload bytes AS RECEIVED, and the container is chosen only
// AFTERWARDS, from bytes the embedded key has already attested. The reverse order would turn
// `schema` — a field an attacker writes — into a parser selector, which is a class of bug worth
// naming: choosing how to interpret data using the data you have not yet authenticated.
//
// ============ AND AN UNKNOWN CONTAINER IS REFUSED, NEVER FALLEN BACK =====================
// encoding/json IGNORES unknown fields, so a future v4 payload decodes into the flat `wire`
// struct as a license with an empty licensee and no expiry — a silent misread, not an error.
// A schema this build does not know therefore refuses, exactly as IssuancePhase.known() refuses
// a phase it does not recognize: an unknown container never asserts a right.
//
// ============ WHAT IS *NOT* HERE, DELIBERATELY ===========================================
// There is no conversion from a v3 credential to a flat Claims. A v3 credential carries a grant
// PER PURCHASED LINE with its own product, term and phase; collapsing them into one Claims means
// picking one line's expiry for all of them, which is the aggregation v3 exists to remove (see
// the header of credential_v3.go). Verify therefore keeps returning the flat claim set it always
// returned, and refuses a v3 blob with a NAME a caller can route on instead of "malformed".

package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Container names the signed container a verified payload holds. It is a CLOSED set: the router
// refuses anything that is not one of these, and never guesses from shape.
type Container string

const (
	// ContainerFlat is the v1/v2 flat claim set (license.go). It declares no schema, which is
	// what identifies it: every container added after it names itself.
	ContainerFlat Container = "license.flat"
	// ContainerCredentialV3 is the aggregate credential of credential_v3.go.
	ContainerCredentialV3 Container = CredentialSchemaV3
)

// Router errors. Like every error in this package they mean "no valid commercial license", never
// "deny": Verify and VerifyEnvelope block nothing (see the package doc).
var (
	// ErrCredentialV3 reports that a correctly-signed blob carries a v3 aggregate credential,
	// which has no flat claim set to return. It exists so a caller can ROUTE instead of guessing:
	// before it, this case was indistinguishable from a corrupt blob.
	ErrCredentialV3 = errors.New("license: the blob carries an " + CredentialSchemaV3 +
		" aggregate, which has no flat claim set; read it with VerifyEnvelope")
	// ErrUnknownContainer reports a payload naming a container schema this build does not know.
	ErrUnknownContainer = errors.New("license: the payload names a signed container this build does not know")
)

// Verified is what ONE signature attests: the container the payload declares, and the content of
// exactly that container. Never both, never a merge of the two — the zero value of the other
// field is not "missing data", it is "this blob is not that container".
//
// The accessors below exist so that the ten call sites that consume a license cannot diverge on
// what a credential looks like. A verified-license question answered in ten places is answered
// ten ways; that is how "one of them accepts a v3 and another rejects it" happens.
type Verified struct {
	// Container says which of the two below is populated.
	Container Container
	// Claims is the flat claim set. Meaningful only when Container == ContainerFlat.
	Claims Claims
	// Credential is the aggregate credential. Meaningful only when Container == ContainerCredentialV3.
	Credential Credential
}

// IsCredentialV3 reports whether this blob carried the aggregate container.
func (v Verified) IsCredentialV3() bool { return v.Container == ContainerCredentialV3 }

// Licensee is the organization the license attests, from whichever container carries it.
func (v Verified) Licensee() string {
	if v.IsCredentialV3() {
		return v.Credential.Licensee
	}
	return v.Claims.Licensee
}

// Serial uniquely identifies the license, and is what a revocation list names.
func (v Verified) Serial() string {
	if v.IsCredentialV3() {
		return v.Credential.Serial
	}
	return v.Claims.Serial
}

// Plan is the free-form display plan label. A v3 credential HAS NO PLAN and returns empty: the
// plan label was the flat container's one-line stand-in for what was bought, and v3 replaced it
// with the grant list. Returning a base product id here would be that stand-in coming back.
func (v Verified) Plan() string {
	if v.IsCredentialV3() {
		return ""
	}
	return v.Claims.Plan
}

// SupportTier is the flat container's attested support relationship ("standard", "enterprise";
// empty = none/community), display-only (LICENSING.md key custody: no Olivares-side decision may
// trust this self-report).
//
// A v3 credential returns EMPTY, and that is a decision rather than an omission. Its envelope
// carries `support_profile`, which LOOKS like the same field and is not: the frozen vector's
// value is "business", a PRODUCT tier, where SupportTier's domain is the support relationship.
// Mapping one onto the other by name would render "Support: business" in a console badge — a
// fact nobody signed, in a slot that means something else. The credential's own field is
// available under its own name (SupportProfile) and `olivares license status` prints it there.
func (v Verified) SupportTier() string {
	if v.IsCredentialV3() {
		return ""
	}
	return v.Claims.SupportTier
}

// SupportProfile is the v3 envelope's own `support_profile`, under its own name. Empty for a
// flat license, which has no such field. See SupportTier for why the two are not merged.
func (v Verified) SupportProfile() string {
	if v.IsCredentialV3() {
		return v.Credential.SupportProfile
	}
	return ""
}

// Profile is the issuance environment (online/airgapped/trial) of a flat license. A v3 credential
// carries no such field — its envelope describes the deployment binding and the clock policy
// instead — so it returns empty rather than defaulting to "online", which would attest something
// nobody signed.
func (v Verified) Profile() string {
	if v.IsCredentialV3() {
		return ""
	}
	return v.Claims.NormalizedProfile()
}

// Status reports the license status at `now` for either container. See Credential.Status for the
// v3 definition and why it is the base grant's.
func (v Verified) Status(now time.Time) Status {
	if v.IsCredentialV3() {
		return v.Credential.Status(now)
	}
	return v.Claims.Status(now)
}

// StatusWithRevocation reports revocation ahead of every expiry status, in either container.
func (v Verified) StatusWithRevocation(now time.Time, r Revocation) Status {
	if v.IsCredentialV3() {
		return v.Credential.StatusWithRevocation(now, r)
	}
	return v.Claims.StatusWithRevocation(now, r)
}

// RevokedBy reports whether r names this license, in either container.
func (v Verified) RevokedBy(r Revocation) bool {
	if v.IsCredentialV3() {
		return v.Credential.RevokedBy(r)
	}
	return v.Claims.RevokedBy(r)
}

// MaxUsers is the attested seat figure, 0 = UNLIMITED in both containers. It is display-only
// everywhere since B10: no build may turn it into a runtime limit, and ParseCredentialV3 refuses
// a v3 credential that carries anything other than 0 rather than clamping it.
func (v Verified) MaxUsers() int {
	if v.IsCredentialV3() {
		return v.Credential.MaxUsers
	}
	return v.Claims.MaxUsers
}

// Features is the flat container's attested feature-tag list (informational; never gates). A v3
// credential has none and returns nil: what was bought is the grant list, and mapping a product
// id to modules is the consuming build's job, deliberately kept out of the signed artifact.
func (v Verified) Features() []string {
	if v.IsCredentialV3() {
		return nil
	}
	return v.Claims.Features
}

// IssuedAt is when the license was signed, in either container.
func (v Verified) IssuedAt() time.Time {
	if v.IsCredentialV3() {
		return v.Credential.IssuedAt
	}
	return v.Claims.IssuedAt
}

// Term is when the right ends, BEFORE any attested post-term grace: ExpiresAt for a flat
// license, and the BASE grant's effective boundary for a v3 credential.
//
// The base line is the only coherent candidate and it is not a merge: ParseCredentialV3
// guarantees exactly one base grant and that no add-on outlives it, so the base's boundary is
// the credential's. Using its paid_through instead would OVERSTATE a provisional purchase —
// during the refund window the money-back lease can end in 72h while paid_through sits months
// out — which is why this reads EffectiveBoundary and only steps back to paid_through in a
// renewal grace, where the boundary IS the grace end and belongs to RightEnds.
//
// It is deliberately narrow: the add-on lines have their own terms, and the only honest way to
// see them is Grants().
func (v Verified) Term() time.Time {
	if v.IsCredentialV3() {
		base, ok := v.Credential.BaseGrant()
		if !ok {
			return time.Time{}
		}
		if base.Phase == PhaseRenewalGrace {
			return base.PaidThrough
		}
		return base.EffectiveBoundary()
	}
	return v.Claims.ExpiresAt
}

// RightEnds is the last instant the license confers its right, INCLUDING any attested grace:
// ExpiresAt+GracePeriod for a flat license, the base grant's effective boundary for a v3. Zero
// when nothing is attested.
func (v Verified) RightEnds() time.Time {
	if v.IsCredentialV3() {
		if base, ok := v.Credential.BaseGrant(); ok {
			return base.EffectiveBoundary()
		}
		return time.Time{}
	}
	if v.Claims.ExpiresAt.IsZero() {
		return time.Time{}
	}
	return v.Claims.ExpiresAt.Add(v.Claims.GracePeriod())
}

// Grants is the per-line detail: EMPTY for a flat license (which has none) and the full list for
// a v3, in wire order. It is never summarized — a caller that wants one answer has to say which
// line it is asking about.
func (v Verified) Grants() []Grant {
	if v.IsCredentialV3() {
		return v.Credential.Grants
	}
	return nil
}

// LegacySeamClaims projects a verified license onto the FLAT claim set, for the one seam that is
// typed to it and cannot be widened from this repository: licenseClaimsFunc
// (cmd/olivares/seatcapwire.go), which the closed enterprise overlay implements in its own tree.
// ok is false when there is no license to project.
//
// ⚠ READ THIS BEFORE USING IT ANYWHERE ELSE. It is NOT a v3-to-Claims conversion and it must not
// become one. Everything that identifies WHAT WAS BOUGHT is deliberately dropped — no plan, no
// features, no products, no per-line terms — precisely because collapsing a grant per purchased
// line into one claim set is the aggregation the v3 container exists to prevent. What survives is
// only what the consuming seam actually asks: WHO holds the license, WHICH serial (so a CRL can
// still name it), and UNTIL WHEN the right runs.
//
// The term is the BASE line's, by the canon's own dependency rather than by a merge: every add-on
// requires an effective base grant (PRICING-CANON.md:925), so a lapsed base ends the credential
// whatever an add-on still says, and a live base never claims more than the base supports.
//
// WHY IT EXISTS AT ALL, measured on 2026-08-11 (Codex contrast, F-1). Answering "no license" for a
// v3 credential looked free — the seat seam refuses nobody since B10 — and it was not: the overlay
// publishes this SAME provider as the process-wide add-on license source
// (cmd-overlay/olivares/wire_enterprise.go installAddonLicenseSources → addonClaims → addonGate),
// where ok=false is StateUnentitled and every add-on operation is refused. A paying customer
// holding the credential this project issues would have lost every add-on, quietly. The gate
// decides on the TERM alone (addongate.termState) and has no per-slug logic, so this projection
// answers exactly the question it asks and no more.
//
// ⚠ AND ITS LIMIT, which is the overlay's work and not this repository's: because the seam cannot
// carry the grant list, a consumer reading through it CANNOT honor which lines were purchased. A
// container-aware source is what makes per-line entitlement possible; until one exists, this seam
// treats a credential the same way it has always treated a flat license.
func (v Verified) LegacySeamClaims() (Claims, bool) {
	if v.Container == "" {
		return Claims{}, false
	}
	if !v.IsCredentialV3() {
		return v.Claims, true
	}
	base, ok := v.Credential.BaseGrant()
	if !ok {
		return Claims{}, false
	}
	c := Claims{
		Licensee: v.Credential.Licensee,
		Serial:   v.Credential.Serial,
		IssuedAt: v.Credential.IssuedAt,
	}
	// ExpiresAt is the paid term and GraceUntil the attested extension, so the seam's own
	// Status/termState arithmetic lands on the same answer Credential.Status gives.
	if base.Phase == PhaseRenewalGrace {
		c.ExpiresAt, c.GraceUntil = base.PaidThrough, base.GraceEndsAt
	} else {
		c.ExpiresAt = base.EffectiveBoundary()
	}
	return c, true
}

// VerifyEnvelope checks the blob's signature against pub and returns whichever container the
// VERIFIED payload declares. It is the entry point every consumer should use; Verify remains for
// the flat container alone.
//
// Like Verify it NEVER gates: an error means "treat as no commercial license", not "deny".
func VerifyEnvelope(blob string, pub ed25519.PublicKey) (Verified, error) {
	payload, err := openEnvelope(blob, pub)
	if err != nil {
		return Verified{}, err
	}
	return readContainer(payload)
}

// openEnvelope splits the blob and verifies the Ed25519 signature over the payload bytes AS
// RECEIVED. It returns the payload ONLY after the embedded key has attested it — every caller
// downstream is therefore parsing bytes we signed, not bytes somebody sent.
func openEnvelope(blob string, pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: bad public key size %d", len(pub))
	}
	payloadB64, sigB64, ok := strings.Cut(blob, ".")
	if !ok {
		return nil, ErrMalformed
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(payloadB64)
	if err != nil {
		return nil, ErrMalformed
	}
	sig, err := enc.DecodeString(sigB64)
	if err != nil {
		return nil, ErrMalformed
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, ErrBadSignature
	}
	return payload, nil
}

// readContainer decides which container an ALREADY-VERIFIED payload holds and parses it with
// that container's own reader. It is never called with unauthenticated bytes.
func readContainer(payload []byte) (Verified, error) {
	kind, err := containerOf(payload)
	if err != nil {
		return Verified{}, err
	}
	if kind == ContainerCredentialV3 {
		c, err := ParseCredentialV3(payload)
		if err != nil {
			return Verified{}, err
		}
		return Verified{Container: ContainerCredentialV3, Credential: c}, nil
	}
	var w wire
	if err := json.Unmarshal(payload, &w); err != nil {
		return Verified{}, ErrMalformed
	}
	c, err := fromWire(w)
	if err != nil {
		return Verified{}, ErrMalformed
	}
	return Verified{Container: ContainerFlat, Claims: c}, nil
}

// containerOf reads the ONE field that discriminates the containers, and reads nothing else.
//
// The flat container declares no schema — it predates the idea — so an ABSENT schema is what
// identifies it, and only an absent one. Anything else that is not v3 is refused rather than
// tried against the old parser: falling back is how a container with different semantics gets
// read under rules never written for it, silently, because encoding/json drops what it does not
// recognize instead of complaining.
//
// It is read by TOKEN rather than by unmarshalling the whole document, for one reason: a
// truncated payload that names itself v3 must stay a BROKEN V3 and must not become "not v3".
// Whole-document decoding cannot tell those apart — it fails on both — and ErrNotV3 is exactly
// the answer that invites a caller to fall back to the old reader, which would silence real
// corruption in a genuine credential.
func containerOf(payload []byte) (Container, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		return "", ErrMalformed
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		// Not a JSON object: neither container can be built from it.
		return "", ErrMalformed
	}
	schema, found := "", false
	for {
		tok, err := dec.Token()
		if err != nil {
			// Truncated. If the schema was already read, the payload has still NAMED itself and
			// stays that container — a truncated v3 is a broken v3, never "not v3", because the
			// second answer is the one that invites a fallback. If it was not, nothing
			// identifies the payload and it is classified as nothing.
			if !found {
				return "", ErrMalformed
			}
			break
		}
		if d, ok := tok.(json.Delim); ok && d == '}' {
			break
		}
		key, ok := tok.(string)
		if !ok {
			return "", ErrMalformed
		}
		val, err := dec.Token()
		if err != nil {
			if found {
				break
			}
			return "", ErrMalformed
		}
		if _, ok := val.(json.Delim); ok {
			// A nested object or array: skip it whole. Only the TOP-LEVEL schema decides, so a
			// grant — or a licensee — carrying its own "schema" field cannot vote.
			//
			// ⚠ EXCEPT when the key IS schema. Skipping here without looking at the key was a
			// hole, found by this file's own attack table: `{"schema":[…]}` fell through to the
			// flat reader, which drops the field in silence. A schema we cannot READ is not a
			// missing schema — it is a payload that names a container in a shape we do not
			// understand, and that is refused.
			if key == "schema" {
				return "", fmt.Errorf("%w: \"schema\" is not a string", ErrMalformed)
			}
			if err := skipContainer(dec); err != nil {
				if found {
					break
				}
				return "", ErrMalformed
			}
			continue
		}
		if key != "schema" {
			continue
		}
		if found {
			// TWO schema keys in one object. encoding/json keeps the LAST and this scan sees the
			// FIRST, so a payload could be routed by one value and read under the other — the
			// duplicate-key ambiguity rejectDuplicateKeys already refuses INSIDE the credential,
			// applied here to the field that chooses the reader. Nothing legitimate emits it.
			return "", fmt.Errorf("%w: the payload names \"schema\" twice, so which container it "+
				"declares is ambiguous", ErrMalformed)
		}
		s, ok := val.(string)
		if !ok {
			return "", ErrMalformed
		}
		schema, found = s, true
	}

	// ABSENCE identifies the flat container, and only absence. The `found` bit is what decides,
	// never the value: `{"schema":""}` NAMES a discriminator and does not name the one this build
	// reads, so it is refused like any other unknown. Switching on the value alone mapped it to
	// flat — where the decoder then dropped the field in silence — which contradicted this
	// function's own rule two paragraphs up. Found by the 2026-08-11 Codex contrast (F-2).
	if !found {
		return ContainerFlat, nil
	}
	if schema == CredentialSchemaV3 {
		return ContainerCredentialV3, nil
	}
	return "", fmt.Errorf("%w: %q (this build reads %q and the flat v1/v2 claim set; upgrade this binary first)",
		ErrUnknownContainer, schema, CredentialSchemaV3)
}

// declaresNoSchema reports whether the payload carries NO top-level schema key at all — the one
// condition under which falling back to the v1/v2 reader is safe. It is the question
// ParseCredentialV3 has to ask before returning ErrNotV3, which is documented permission to fall
// back: answering it with "containerOf returned some error" hands that permission to an unknown
// container too.
func declaresNoSchema(payload []byte) bool {
	kind, err := containerOf(payload)
	return err == nil && kind == ContainerFlat
}

// skipContainer consumes the rest of an already-opened object or array.
func skipContainer(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
