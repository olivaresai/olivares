// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package caep implements the wire layer for CAEP 1.0 (Continuous Access
// Evaluation Protocol) and RISC 1.0 (Risk Information Sharing and
// Coordination) Security Event Tokens. It defines the event URI taxonomy,
// subject identifier formats, and the event→action mapping. It is pure wire
// logic — it imports only the standard library. Cryptographic verification
// and the credential-cut actions live in core/auth.
//
// CAEP 1.0, RISC 1.0, and SSF 1.0 are OpenID Final specifications
// (2025-09-02). The event URIs below are committed against final text.
package caep

import "encoding/json"

// CAEP 1.0 event type URIs (OpenID CAEP 1.0 Final, 2025-09-02).
const (
	EventSessionRevoked         = "https://schemas.openid.net/secevent/caep/event-type/session-revoked"
	EventTokenClaimsChange      = "https://schemas.openid.net/secevent/caep/event-type/token-claims-change"
	EventCredentialChange       = "https://schemas.openid.net/secevent/caep/event-type/credential-change"
	EventDeviceComplianceChange = "https://schemas.openid.net/secevent/caep/event-type/device-compliance-change"
)

// RISC 1.0 event type URIs (OpenID RISC 1.0 Final, 2025-09-02).
const (
	EventAccountDisabled      = "https://schemas.openid.net/secevent/risc/event-type/account-disabled"
	EventCredentialCompromise = "https://schemas.openid.net/secevent/risc/event-type/credential-compromise"
)

// CAEPEventAction is the access effect the receiver applies for a CAEP/RISC event.
type CAEPEventAction string

const (
	ActionSessionRevoke        CAEPEventAction = "session_revoke"
	ActionTokenRevoke          CAEPEventAction = "token_revoke"
	ActionCredentialRevoke     CAEPEventAction = "credential_revoke"
	ActionDeviceNonCompliant   CAEPEventAction = "device_noncompliant"
	ActionAccountDisable       CAEPEventAction = "account_disable"
	ActionCredentialCompromise CAEPEventAction = "credential_compromise"
	ActionIgnore               CAEPEventAction = "ignore"
)

// ActionForCAEPEvent maps a CAEP/RISC event URI to the access action.
// Unknown URIs return ActionIgnore.
func ActionForCAEPEvent(uri string) CAEPEventAction {
	switch uri {
	case EventSessionRevoked:
		return ActionSessionRevoke
	case EventTokenClaimsChange:
		return ActionTokenRevoke
	case EventCredentialChange:
		return ActionCredentialRevoke
	case EventDeviceComplianceChange:
		return ActionDeviceNonCompliant
	case EventAccountDisabled:
		return ActionAccountDisable
	case EventCredentialCompromise:
		return ActionCredentialCompromise
	default:
		return ActionIgnore
	}
}

// SubjectIdentifier represents a subject in SSF/CAEP/RISC events. The format
// field selects how to resolve the subject to a tenant member.
type SubjectIdentifier struct {
	Format  string `json:"format"`
	Email   string `json:"email,omitempty"`
	Issuer  string `json:"iss,omitempty"`
	Subject string `json:"sub,omitempty"`
	ID      string `json:"id,omitempty"`
}

// DeviceCompliancePayload is the event payload for device-compliance-change.
type DeviceCompliancePayload struct {
	CurrentStatus  string `json:"current_status"`
	PreviousStatus string `json:"previous_status,omitempty"`
}

// DecodeDeviceCompliance extracts the device compliance payload from the event.
func DecodeDeviceCompliance(raw json.RawMessage) (DeviceCompliancePayload, error) {
	var p DeviceCompliancePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return DeviceCompliancePayload{}, err
	}
	return p, nil
}

// ResolveSubjectEmail returns the email when the format is "email".
func (s SubjectIdentifier) ResolveSubjectEmail() string {
	if s.Format == "email" {
		return s.Email
	}
	return ""
}

// ResolveSubjectIssSub returns the issuer and subject when the format is "iss_sub".
func (s SubjectIdentifier) ResolveSubjectIssSub() (issuer, subject string) {
	if s.Format == "iss_sub" {
		return s.Issuer, s.Subject
	}
	return "", ""
}
