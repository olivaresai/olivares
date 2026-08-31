// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// SCIM Security Event Token receiver (RFC 9967), the credential-touching half of
// IDN-11. The wire parsing (compact-JWS split, SET claim codec, event taxonomy)
// lives in core/api/scim; this file owns what only core may do: verify the SET's
// JWS signature against the tenant's configured PUBLISHER key, and turn an
// access-affecting event into the same credential cut the polled SCIM lifecycle
// performs (deprovision / disable / re-activate). The receiver is deny-closed: a
// tenant that has not configured a SET publisher key (issuer + public key)
// rejects every event. The publisher key is a PUBLIC key — it is stored in the
// tenant's Org.Settings (no-secrets config, docs/SECURITY-HARDENING.md), not the secret store.
//
// RFC 9967 is PUBLISHED (IETF Standards Track, verified 2026-06-06,
// recorded in the verification ledger), so this is implemented against final text —
// unlike the still-draft SCIM device/NHI schema, which is only structured-toward.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"hash"
	"math/big"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// orgSettingSCIMSet is the Org.Settings key under which a tenant's SET receiver
// configuration is stored.
const orgSettingSCIMSet = "scim_set"

// Errors the SET receiver surfaces. The handler maps these to RFC 8935 delivery
// responses (a 400 with a machine-readable "err" code, or 202 on success).
var (
	// ErrSCIMSetDisabled means no SET publisher key is configured for the tenant
	// (deny-closed): the receiver will not act on an unverifiable event.
	ErrSCIMSetDisabled = errors.New("auth: SCIM SET receiver not configured for this tenant")
	// ErrSCIMSetSignature means the SET's JWS signature did not verify against any
	// configured publisher key (or used an unaccepted algorithm).
	ErrSCIMSetSignature = errors.New("auth: SCIM SET signature verification failed")
	// ErrSCIMSetIssuer means the SET issuer/audience did not match the configured
	// publisher.
	ErrSCIMSetIssuer = errors.New("auth: SCIM SET issuer or audience mismatch")
	// ErrSCIMSetSubject means the SET subject did not resolve to a tenant member.
	ErrSCIMSetSubject = errors.New("auth: SCIM SET subject is not a member of this tenant")
)

// acceptedSetAlgs is the pinned JWS algorithm allowlist for SET verification.
// Only asymmetric signatures are accepted (a SET is signed by the publisher's
// private key and verified with its public key); "none" and the symmetric HS*
// family are rejected, matching the federation posture (never accept "none").
var acceptedSetAlgs = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
	"EdDSA": true,
}

// SCIMSetVerificationKey is a backward-compatible alias for SETVerificationKey.
// All SCIM SET config stored under this name remains readable via the alias.
type SCIMSetVerificationKey = SETVerificationKey

// SCIMSetConfig is a tenant's SET receiver configuration, persisted in
// Org.Settings. It embeds SETPublisher so the JSON wire format is identical to
// the previous flat layout: enabled/issuer/audiences/keys/max_clock_skew_seconds/
// max_age_seconds are promoted fields. The id field (omitempty) is new and only
// appears for CAEP/multi-publisher configs; existing SCIM-only configs are
// round-trippable with no change.
type SCIMSetConfig struct {
	SETPublisher
	// MaxAgeSeconds note: a stale SET (captured-and-replayed) is rejected by the
	// iat past-skew bound; full duplicate-SET suppression by "jti" is a tracked
	// follow-up (see SCIMReceiveEvent) — today access actions are idempotent so a
	// same-action replay within the window is harmless.
}

// SCIMSetAction is the access effect the receiver applies for an event, derived
// by the handler from the (wire) event URI and passed in so core/auth stays free
// of the wire taxonomy.
type SCIMSetAction string

const (
	// SCIMSetDeprovision is the leaver path (prov:delete).
	SCIMSetDeprovision SCIMSetAction = "deprovision"
	// SCIMSetDisable cuts access but keeps the record (prov:deactivate).
	SCIMSetDisable SCIMSetAction = "disable"
	// SCIMSetActivate re-enables a disabled member (prov:activate).
	SCIMSetActivate SCIMSetAction = "activate"
	// SCIMSetIgnore is acknowledged-but-not-acted-on.
	SCIMSetIgnore SCIMSetAction = "ignore"
)

// SCIMEventEnvelope is the already-parsed SET a receiver handler hands to the
// Authenticator. The wire decode happens in core/api/scim; core/auth verifies the
// signature (over SigningInput) and acts. SubjectURI is the SCIM resource path
// ("Users/{id}"); SubjectExternalID is the optional alternative match key.
type SCIMEventEnvelope struct {
	Alg               string
	Kid               string
	SigningInput      []byte
	Signature         []byte
	Issuer            string
	Audience          []string
	JTI               string
	IssuedAt          int64
	SubjectID         string // the {id} parsed from SubjectURI, when present
	SubjectExternalID string
	Action            SCIMSetAction
}

// SCIMEventResult reports what the receiver did with a verified event.
type SCIMEventResult struct {
	Action SCIMSetAction
	UserID model.ID
	JTI    string
}

// SCIMSetConfigFor returns the tenant's SET receiver configuration and whether
// one is present. It reads the tenant's own Org.Settings.
func (a *Authenticator) SCIMSetConfigFor(ctx context.Context, tenant model.TenantID) (SCIMSetConfig, bool, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return SCIMSetConfig{}, false, ErrInvalidToken
	}
	var cfg SCIMSetConfig
	var present bool
	err := a.st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		raw, ok := org.Settings[orgSettingSCIMSet]
		if !ok {
			return nil
		}
		present = true
		// Round-trip through JSON so the free-form settings map decodes into the
		// typed config regardless of how it was stored.
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &cfg)
	})
	return cfg, present, err
}

// ConfigureSCIMSet stores (or replaces) the tenant's SET receiver configuration
// and records the change on the tenant's audit chain. The keys it stores are
// PUBLIC keys; it rejects a config whose keys do not parse or whose algs are not
// accepted, so a broken config can never be saved.
func (a *Authenticator) ConfigureSCIMSet(ctx context.Context, actor Principal, tenant model.TenantID, cfg SCIMSetConfig) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	if err := ValidatePublisherKeys(cfg.SETPublisher); err != nil {
		return err
	}
	asMap := map[string]any{}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &asMap); err != nil {
		return err
	}
	return a.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[orgSettingSCIMSet] = asMap
		if _, err := sc.SetOrgSettings(ctx, settings); err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "scim.set.configure", TargetKind: "core.org", TargetID: model.ID(tenant.String()),
		})
		return err
	})
}

// SCIMReceiveEvent verifies a parsed SET against the tenant's configured
// publisher and applies its access effect. It is deny-closed (unconfigured =>
// ErrSCIMSetDisabled), verifies the JWS signature, checks issuer/audience, and
// dispatches the action to the same credential-cut primitives the polled SCIM
// lifecycle uses. Non-access events (SCIMSetIgnore) are acknowledged without a
// store write.
//
// Replay: the iat past-skew bound (checkIssuerAudience, SCIMSetConfig.MaxAgeSeconds)
// caps the window in which a captured SET can be replayed, and every access
// action (deprovision/disable/activate) is state-idempotent — replaying the same
// event re-applies the same state, never toggling it. Full duplicate suppression
// by RFC 8417 "jti" (a per-tenant seen-set) is a tracked follow-up, deliberately
// not built here because the idempotent actions make it a standards-compliance
// nicety, not a security boundary (docs-VERIFICATION-LEDGER.md §3).
func (a *Authenticator) SCIMReceiveEvent(ctx context.Context, actor Principal, tenant model.TenantID, env SCIMEventEnvelope) (SCIMEventResult, error) {
	cfg, present, err := a.SCIMSetConfigFor(ctx, tenant)
	if err != nil {
		return SCIMEventResult{}, err
	}
	if !present || !cfg.Enabled || len(cfg.Keys) == 0 {
		return SCIMEventResult{}, ErrSCIMSetDisabled
	}
	if err := verifySETSignature(cfg.SETPublisher, scimEnvelopeToSET(env)); err != nil {
		return SCIMEventResult{}, err
	}
	if err := checkIssuerAudience(cfg.SETPublisher, scimEnvelopeToSET(env), a.clock.Now()); err != nil {
		return SCIMEventResult{}, err
	}
	res := SCIMEventResult{Action: env.Action, JTI: env.JTI}
	if env.Action == SCIMSetIgnore || env.Action == "" {
		res.Action = SCIMSetIgnore
		return res, nil
	}
	// Resolve the subject to a member of THIS tenant (not-found == not-a-member,
	// the same cross-tenant oracle guard the rest of SCIM enforces).
	user, err := a.resolveSetSubject(ctx, tenant, env)
	if err != nil {
		return SCIMEventResult{}, err
	}
	res.UserID = user.ID
	switch env.Action {
	case SCIMSetDeprovision:
		return res, a.SCIMDeprovisionUser(ctx, actor, tenant, user.ID)
	case SCIMSetDisable:
		return res, a.SCIMSetMemberActive(ctx, actor, tenant, user.ID, false)
	case SCIMSetActivate:
		return res, a.SCIMSetMemberActive(ctx, actor, tenant, user.ID, true)
	default:
		res.Action = SCIMSetIgnore
		return res, nil
	}
}

// resolveSetSubject finds the tenant member named by the SET subject: by SCIM id
// when the sub_id URI carried one, else by externalId.
func (a *Authenticator) resolveSetSubject(ctx context.Context, tenant model.TenantID, env SCIMEventEnvelope) (model.User, error) {
	if env.SubjectID != "" {
		u, err := a.SCIMGetMember(ctx, tenant, model.ID(env.SubjectID))
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return model.User{}, err
		}
	}
	if env.SubjectExternalID != "" {
		if u, ok, err := a.SCIMFindMember(ctx, tenant, "external_id", env.SubjectExternalID); err != nil {
			return model.User{}, err
		} else if ok {
			return u, nil
		}
	}
	return model.User{}, ErrSCIMSetSubject
}

// SCIMSetMemberActive flips a tenant member's active status. Disabling cuts
// access (revoke tenant-bound tokens cascading to exchanged children + all
// sessions) and marks the account inactive while preserving the membership;
// re-activating only restores the status (access is re-granted by the IdP's next
// provisioning, not by reviving old tokens). It is idempotent.
func (a *Authenticator) SCIMSetMemberActive(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID, active bool) error {
	if _, err := a.SCIMGetMember(ctx, tenant, id); err != nil {
		return err
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		want := scimStatus(active)
		if u.Status != want {
			u.Status = want
			if _, err := as.Users().Update(ctx, u); err != nil {
				return err
			}
		}
		action := "scim.set.activate"
		if !active {
			action = "scim.set.deactivate"
		}
		if err := auditAct(ctx, as, actor, action, "core.user", id); err != nil {
			return err
		}
		if !active {
			return revokeUserAccess(ctx, as, actor, id, tenant, true)
		}
		return nil
	})
}

// checkIssuerAudience enforces the configured issuer/audience binding and a
// bounded future-skew on iat (a SET issued far in the future is rejected).
// It accepts the generic SETPublisher and SETEnvelope so it can be shared
// across SET receiver profiles (SCIM, CAEP, RISC).
func checkIssuerAudience(pub SETPublisher, env SETEnvelope, now model.Timestamp) error {
	if pub.Issuer != "" && env.Issuer != pub.Issuer {
		return ErrSCIMSetIssuer
	}
	if len(pub.Audiences) > 0 {
		ok := false
		for _, want := range pub.Audiences {
			for _, got := range env.Audience {
				if got == want {
					ok = true
				}
			}
		}
		if !ok {
			return ErrSCIMSetIssuer
		}
	}
	if env.IssuedAt > 0 {
		skew := pub.MaxClockSkewSeconds
		if skew <= 0 {
			skew = 120
		}
		if env.IssuedAt > now.Time().Add(time.Duration(skew)*time.Second).Unix() {
			return ErrSCIMSetIssuer
		}
		// Reject a stale SET (captured-and-replayed): bound how far in the past iat
		// may be. This caps the replay window even without jti de-dup.
		maxAge := pub.MaxAgeSeconds
		if maxAge <= 0 {
			maxAge = 3600
		}
		if env.IssuedAt < now.Time().Add(-time.Duration(maxAge)*time.Second).Unix() {
			return ErrSCIMSetIssuer
		}
	}
	return nil
}

// verifySETSignature verifies env's JWS signature against the configured keys.
// Key selection is STRICT on kid: when the SET header carries a kid, ONLY a key
// whose kid matches EXACTLY is eligible — a keyless configured key is never a
// fallback for a kid-bearing SET (otherwise a SET could name an arbitrary kid and
// be verified by an unrelated keyless key). When the SET carries no kid, every
// key whose alg matches is a candidate. The alg must be in the pinned allowlist.
// It accepts the generic SETPublisher and SETEnvelope so it can be shared across
// SET receiver profiles (SCIM, CAEP, RISC).
func verifySETSignature(pub SETPublisher, env SETEnvelope) error {
	if !acceptedSetAlgs[env.Alg] {
		return ErrSCIMSetSignature
	}
	for _, k := range pub.Keys {
		if env.Kid != "" && k.Kid != env.Kid {
			continue
		}
		if k.Alg != env.Alg {
			continue
		}
		p, err := parsePKIXPublic(k.PEM)
		if err != nil {
			continue
		}
		if jwsVerify(env.Alg, p, env.SigningInput, env.Signature) == nil {
			return nil
		}
	}
	return ErrSCIMSetSignature
}

// scimEnvelopeToSET converts a SCIM-specific SCIMEventEnvelope to the generic
// SETEnvelope that verifySETSignature and checkIssuerAudience accept.
func scimEnvelopeToSET(env SCIMEventEnvelope) SETEnvelope {
	return SETEnvelope{
		Alg: env.Alg, Kid: env.Kid, SigningInput: env.SigningInput,
		Signature: env.Signature, Issuer: env.Issuer, Audience: env.Audience,
		JTI: env.JTI, IssuedAt: env.IssuedAt,
	}
}

// parsePKIXPublic decodes a PEM-encoded PKIX (SubjectPublicKeyInfo) public key.
func parsePKIXPublic(pemStr string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("auth: not a PEM public key")
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

// jwsVerify verifies a compact-JWS signature over signingInput using pub and the
// JWS algorithm alg. It supports the asymmetric RS/PS/ES/EdDSA families; the
// caller has already checked alg is in the allowlist.
func jwsVerify(alg string, pub crypto.PublicKey, signingInput, sig []byte) error {
	switch alg {
	case "RS256", "RS384", "RS512":
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		h, hashed := digest(alg, signingInput)
		return rsa.VerifyPKCS1v15(rsaPub, h, hashed, sig)
	case "PS256", "PS384", "PS512":
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		h, hashed := digest(alg, signingInput)
		return rsa.VerifyPSS(rsaPub, h, hashed, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: h})
	case "ES256", "ES384", "ES512":
		ecPub, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		// JWS ECDSA signatures are the fixed-width R||S concatenation (RFC 7518
		// §3.4), not ASN.1. Each half is the curve's byte size.
		n := (ecPub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*n {
			return ErrSCIMSetSignature
		}
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		_, hashed := digest(alg, signingInput)
		if ecdsa.Verify(ecPub, hashed, r, s) {
			return nil
		}
		return ErrSCIMSetSignature
	case "EdDSA":
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		if ed25519.Verify(edPub, signingInput, sig) {
			return nil
		}
		return ErrSCIMSetSignature
	default:
		return ErrSCIMSetSignature
	}
}

// digest returns the hash function an alg uses and the digest of signingInput
// (Ed25519 needs no pre-hash, so callers ignore the returned values for EdDSA).
func digest(alg string, signingInput []byte) (crypto.Hash, []byte) {
	var h hash.Hash
	var ch crypto.Hash
	switch alg[2:] {
	case "384":
		ch, h = crypto.SHA384, sha512.New384()
	case "512":
		ch, h = crypto.SHA512, sha512.New()
	default:
		ch, h = crypto.SHA256, sha256.New()
	}
	h.Write(signingInput)
	return ch, h.Sum(nil)
}
