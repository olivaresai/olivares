// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package caep defines the durable wire contracts for the Business CAEP/RISC
// receiver. Community retains the contracts and inert edition seams.
package caep

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
