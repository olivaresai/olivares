// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

// orgSettingCAEPSet is the Org.Settings key for the CAEP SET receiver configuration.
const orgSettingCAEPSet = "caep_set"

// ErrCAEPRequiresBusiness is returned when this build has no CAEP receiver.
var ErrCAEPRequiresBusiness = errors.New("auth: CAEP receiver requires Business")

// ErrCAEPSetDisabled means no CAEP SET publisher is configured for the tenant
// (deny-closed): the receiver will not act on an unverifiable event.
var ErrCAEPSetDisabled = errors.New("auth: CAEP SET receiver not configured for this tenant")

// CAEPEventAction is the access effect the CAEP/RISC receiver applies for an event.
// Defined in core/auth (not imported from core/api/caep) to keep core/auth free of
// the wire layer. The handler maps from caep.CAEPEventAction to this type.
type CAEPEventAction string

const (
	// CAEPSessionRevoke maps to CAEP session-revoked: cut sessions for the subject.
	CAEPSessionRevoke CAEPEventAction = "session_revoke"
	// CAEPTokenRevoke maps to CAEP token-claims-change: revoke tokens (not sessions).
	CAEPTokenRevoke CAEPEventAction = "token_revoke"
	// CAEPCredentialRevoke maps to CAEP credential-change: revoke all credentials.
	CAEPCredentialRevoke CAEPEventAction = "credential_revoke"
	// CAEPDeviceNonCompliant maps to CAEP device-compliance-change: configurable.
	CAEPDeviceNonCompliant CAEPEventAction = "device_noncompliant"
	// CAEPAccountDisable maps to RISC account-disabled: total cut.
	CAEPAccountDisable CAEPEventAction = "account_disable"
	// CAEPCredentialCompromise maps to RISC credential-compromise: total cut.
	CAEPCredentialCompromise CAEPEventAction = "credential_compromise"
	// CAEPIgnore is an acknowledged-but-not-acted-on event.
	CAEPIgnore CAEPEventAction = "ignore"
)

// CAEPSetConfig is the tenant's CAEP SET receiver configuration, persisted in
// Org.Settings["caep_set"]. It references a publisher from the shared publisher
// registry (ConfigurePublisher / PublisherFor).
type CAEPSetConfig struct {
	// Enabled turns the receiver on. The zero value (false) is deny-closed.
	Enabled bool `json:"enabled"`
	// PublisherID references the SETPublisher from the tenant's shared registry.
	// Empty means deny-closed (no publisher resolved).
	PublisherID string `json:"publisher_id"`
	// DeviceNonCompliantAction controls the response to device-compliance-change
	// events: "step_up" degrades session assurance to AAL1 (DegradeSessionAssurance);
	// the default ("" or "revoke") cuts all sessions and tenant-bound tokens.
	DeviceNonCompliantAction string `json:"device_noncompliant_action,omitempty"`
}

// CAEPEventEnvelope is the already-parsed SET a CAEP/RISC handler passes to the
// Authenticator. Wire decode and subject parsing happen in core/api; core/auth
// verifies the JWS signature and applies the access effect. Embeds SETEnvelope so
// verifySETSignature and checkIssuerAudience can consume it directly.
type CAEPEventEnvelope struct {
	SETEnvelope
	// Action is the access effect derived by the handler from the SET's event URI.
	Action CAEPEventAction
	// SubjectEmail is set when the SSF sub_id format is "email".
	SubjectEmail string
	// SubjectExternalID is set when the SSF sub_id format is "iss_sub" (the sub component).
	SubjectExternalID string
	// SubjectUserID is set when the SSF sub_id format is "opaque": either an internal
	// user ID or — for session-revoked events — a session ID. Subject resolution
	// tries it as a user ID first; dispatch tries it as a session ID.
	SubjectUserID string
	// EventPayload is the raw JSON of the event-specific payload (e.g. device compliance).
	EventPayload []byte
}

// CAEPEventResult reports what the receiver did with a verified CAEP/RISC event.
type CAEPEventResult struct {
	// Action is the string representation of the applied CAEPEventAction.
	Action string
	// UserID is the resolved tenant member the action was applied to (zero for ignore).
	UserID model.ID
	// JTI is the SET's jti (for caller correlation).
	JTI string
}
