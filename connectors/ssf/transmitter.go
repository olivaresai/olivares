// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ssf

import (
	"encoding/json"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Transmitter is the optional SSF transmitter half: it BUILDS and
// SIGNS a Security Event Token so the operator can emit a kill-switch from the
// panel — e.g. a CAEP session-revoked for a compromised agent — to a
// downstream receiver/IdP. It is a thin builder the host drives; the signing key
// is supplied by the host and held only for the call, never persisted by this
// package (docs/SECURITY-HARDENING.md).
//
// Delivering the SET (the HTTP push to the transmitter's stream, RFC 8935, or
// poll, RFC 8936) is the host's concern; this builder produces the signed compact
// JWS to deliver.
type Transmitter struct {
	Issuer   string          // the SET iss (this transmitter's id)
	Audience string          // the SET aud (the target receiver's id)
	Key      jose.JSONWebKey // the private signing key (Algorithm and KeyID set)
}

// RevokeSession builds and signs a CAEP session-revoked SET for the given subject.
// It is the canonical kill-switch the panel emits. now and jti are injected so the
// caller controls freshness and the unique token id (the package uses no ambient
// clock or randomness).
func (t Transmitter) RevokeSession(subject subjectID, now time.Time, jti string) (string, error) {
	return t.Build(evtSessionRevoked, subject, caepEvent{Subject: &subject, EventTimestamp: now.Unix()}, now, jti)
}

// Build assembles a SET carrying one CAEP event and signs it with the transmitter
// key, returning the compact JWS. The event payload travels under its event-type
// URI key in the "events" claim, per CAEP 1.0.
func (t Transmitter) Build(eventType string, subject subjectID, ev caepEvent, now time.Time, jti string) (string, error) {
	if t.Issuer == "" || t.Audience == "" {
		return "", fmt.Errorf("ssf: transmitter needs an issuer and an audience")
	}
	evBytes, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("ssf: marshal event: %w", err)
	}
	set := setToken{
		Iss:   t.Issuer,
		Aud:   audience{t.Audience},
		Iat:   now.Unix(),
		Jti:   jti,
		SubID: &subject,
		Events: map[string]json.RawMessage{
			eventType: evBytes,
		},
	}
	payload, err := json.Marshal(set)
	if err != nil {
		return "", fmt.Errorf("ssf: marshal SET: %w", err)
	}
	if t.Key.Algorithm == "" {
		return "", fmt.Errorf("ssf: signing key has no algorithm")
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(t.Key.Algorithm), Key: t.Key},
		(&jose.SignerOptions{}).WithType("secevent+jwt").WithHeader("kid", t.Key.KeyID),
	)
	if err != nil {
		return "", fmt.Errorf("ssf: new signer: %w", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("ssf: sign SET: %w", err)
	}
	return obj.CompactSerialize()
}

// MarshalJSON lets a setToken round-trip (the transmitter marshals it; the
// receiver decodes it). audience marshals as a JSON array.
func (a audience) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(a))
}
