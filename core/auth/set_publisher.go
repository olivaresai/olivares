// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// SET publisher infrastructure shared by every SET receiver profile (SCIM, CAEP,
// RISC). A SETPublisher is one IdP/transmitter's trust material: its public keys,
// issuer/audience binding, and freshness/replay parameters. The verification
// functions (verifySETSignature, checkIssuerAudience) operate on this type; each
// receiver's config (SCIMSetConfig, CAEPSetConfig) carries or references a
// publisher. The publisher is a PUBLIC-key-only config (no secrets, docs/SECURITY-HARDENING.md),
// persisted in Org.Settings.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SETVerificationKey is one publisher verification key: a PEM-encoded PKIX
// (SubjectPublicKeyInfo) public key, the JWS algorithm it verifies, and an
// optional key-id for selection.
type SETVerificationKey struct {
	// Kid is the optional JWS key id matched against the SET header's kid.
	Kid string `json:"kid,omitempty"`
	// Alg is the JWS algorithm this key verifies (e.g. "ES256", "RS256", "EdDSA").
	Alg string `json:"alg"`
	// PEM is the PEM-encoded PKIX public key ("-----BEGIN PUBLIC KEY-----").
	PEM string `json:"pem"`
}

// SETPublisher holds the trust material for verifying SETs from one publisher
// (IdP/transmitter). Stored in the SET publisher registry (Org.Settings).
type SETPublisher struct {
	// ID is an optional identifier for the publisher (used by multi-publisher configs).
	ID string `json:"id,omitempty"`
	// Enabled turns the receiver on for the tenant. The zero value (off) is deny-closed.
	Enabled bool `json:"enabled"`
	// Issuer is the expected SET "iss" (the IdP). Empty means do not check iss.
	Issuer string `json:"issuer,omitempty"`
	// Audiences is the set of acceptable SET "aud" values. Empty means do not check aud.
	Audiences []string `json:"audiences,omitempty"`
	// Keys are the publisher's public verification keys.
	Keys []SETVerificationKey `json:"keys,omitempty"`
	// MaxClockSkewSeconds bounds how far in the future a SET "iat" may be; 0 uses
	// a conservative default.
	MaxClockSkewSeconds int64 `json:"max_clock_skew_seconds,omitempty"`
	// MaxAgeSeconds bounds how far in the past a SET "iat" may be — it rejects a
	// stale (captured-and-replayed) SET. 0 uses a conservative default.
	MaxAgeSeconds int64 `json:"max_age_seconds,omitempty"`
}

// SETEnvelope is the JWS-layer fields common to every SET regardless of profile.
// Each receiver's envelope (SCIMEventEnvelope, CAEPEventEnvelope) carries these
// fields; the verification functions operate on them.
type SETEnvelope struct {
	Alg          string
	Kid          string
	SigningInput []byte
	Signature    []byte
	Issuer       string
	Audience     []string
	JTI          string
	IssuedAt     int64
}

// ValidatePublisherKeys checks that every key in pub parses as a valid PKIX
// public key and uses an accepted asymmetric algorithm. It is called on
// configure (save) so a broken publisher config can never be persisted.
func ValidatePublisherKeys(pub SETPublisher) error {
	for _, k := range pub.Keys {
		if !acceptedSetAlgs[k.Alg] {
			return ErrSCIMSetSignature
		}
		if _, err := parsePKIXPublic(k.PEM); err != nil {
			return ErrSCIMSetSignature
		}
	}
	return nil
}

// orgSettingPublishers is the Org.Settings key for the shared SET publisher registry.
const orgSettingPublishers = "set_publishers"

var (
	// ErrSETPublisherNotFound means the requested publisher ID does not exist in
	// the tenant's registry.
	ErrSETPublisherNotFound = errors.New("auth: SET publisher not found")
	// ErrSETJTIDuplicate means this SET jti has already been processed.
	ErrSETJTIDuplicate = errors.New("auth: SET jti already processed (duplicate)")
)

// SETPublisherRegistry is the decoded Org.Settings["set_publishers"] value.
type SETPublisherRegistry struct {
	Publishers []SETPublisher `json:"publishers"`
}

// ConfigurePublisher adds or replaces a publisher in the tenant's registry.
func (a *Authenticator) ConfigurePublisher(ctx context.Context, actor Principal, tenant model.TenantID, pub SETPublisher) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	if pub.ID == "" {
		return errors.New("auth: publisher ID is required")
	}
	if err := ValidatePublisherKeys(pub); err != nil {
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
		reg := decodePublisherRegistry(settings)
		replaced := false
		for i, p := range reg.Publishers {
			if p.ID == pub.ID {
				reg.Publishers[i] = pub
				replaced = true
				break
			}
		}
		if !replaced {
			reg.Publishers = append(reg.Publishers, pub)
		}
		settings[orgSettingPublishers] = encodeRegistry(reg)
		if _, err := sc.SetOrgSettings(ctx, settings); err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.publisher.configured", TargetKind: "core.org",
			TargetID: model.ID(tenant.String()),
			Meta:     map[string]any{"publisher_id": pub.ID},
		})
		return err
	})
}

// PublisherFor returns the publisher with the given ID from the tenant's
// registry, or ErrSETPublisherNotFound.
func (a *Authenticator) PublisherFor(ctx context.Context, tenant model.TenantID, publisherID string) (SETPublisher, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return SETPublisher{}, ErrInvalidToken
	}
	var pub SETPublisher
	var found bool
	err := a.st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		reg := decodePublisherRegistry(org.Settings)
		for _, p := range reg.Publishers {
			if p.ID == publisherID {
				pub = p
				found = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return SETPublisher{}, err
	}
	if !found {
		return SETPublisher{}, ErrSETPublisherNotFound
	}
	return pub, nil
}

// RemovePublisher removes a publisher from the tenant's registry.
func (a *Authenticator) RemovePublisher(ctx context.Context, actor Principal, tenant model.TenantID, publisherID string) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	return a.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			return ErrSETPublisherNotFound
		}
		reg := decodePublisherRegistry(settings)
		idx := -1
		for i, p := range reg.Publishers {
			if p.ID == publisherID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrSETPublisherNotFound
		}
		reg.Publishers = append(reg.Publishers[:idx], reg.Publishers[idx+1:]...)
		settings[orgSettingPublishers] = encodeRegistry(reg)
		if _, err := sc.SetOrgSettings(ctx, settings); err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.publisher.removed", TargetKind: "core.org",
			TargetID: model.ID(tenant.String()),
			Meta:     map[string]any{"publisher_id": publisherID},
		})
		return err
	})
}

// CheckJTIDuplicate checks whether the given jti from publisherID has already
// been seen. If not, records it with an expiry of maxAge seconds from now.
// Returns ErrSETJTIDuplicate if already seen. Empty jti is a no-op (some SETs
// may omit it).
func (a *Authenticator) CheckJTIDuplicate(ctx context.Context, publisherID, jti string, maxAge int64) error {
	if jti == "" {
		return nil
	}
	if maxAge <= 0 {
		maxAge = 3600
	}
	now := a.clock.Now()
	var dup bool
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		existing, _, err := as.SeenJTIs().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "publisher_id", Op: model.OpEq, Value: publisherID},
				{Column: "jti", Op: model.OpEq, Value: jti},
			},
			Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			dup = true
			return nil
		}
		_, err = as.SeenJTIs().Create(ctx, model.SETSeenJTI{
			JTI:         jti,
			PublisherID: publisherID,
			ExpiresAt:   model.NewTimestamp(now.Time().Add(time.Duration(maxAge) * time.Second)),
		})
		return err
	})
	if err != nil {
		return err
	}
	if dup {
		return ErrSETJTIDuplicate
	}
	return nil
}

// decodePublisherRegistry decodes the registry from the tenant's org settings.
func decodePublisherRegistry(settings map[string]any) SETPublisherRegistry {
	if settings == nil {
		return SETPublisherRegistry{}
	}
	raw, ok := settings[orgSettingPublishers]
	if !ok {
		return SETPublisherRegistry{}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return SETPublisherRegistry{}
	}
	var reg SETPublisherRegistry
	_ = json.Unmarshal(b, &reg)
	return reg
}

// encodeRegistry serializes the registry to a free-form map for storage in
// Org.Settings (which takes map[string]any).
func encodeRegistry(reg SETPublisherRegistry) any {
	b, _ := json.Marshal(reg)
	var m any
	_ = json.Unmarshal(b, &m)
	return m
}
