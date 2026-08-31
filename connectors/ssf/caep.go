// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ssf

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// caepBase is the CAEP 1.0 event-type URI prefix (OpenID Continuous Access
// Evaluation Profile 1.0, Final — OIDF membership-approved 2025-09-02; spec
// published 2025-08-29). Every CAEP event type is this prefix + a slug, and the
// SET carries it as a key in the "events" claim.
const caepBase = "https://schemas.openid.net/secevent/caep/event-type/"

// The CAEP 1.0 event types (verified verbatim against the Final spec). The four
// the connector treats as a governance kill-switch / change signal are the first
// four; the rest are recognized so a SET carrying them is acknowledged, not
// silently dropped.
const (
	evtSessionRevoked         = caepBase + "session-revoked"
	evtCredentialChange       = caepBase + "credential-change"
	evtTokenClaimsChange      = caepBase + "token-claims-change"
	evtAssuranceLevelChange   = caepBase + "assurance-level-change"
	evtDeviceComplianceChange = caepBase + "device-compliance-change"
	evtSessionEstablished     = caepBase + "session-established"
	evtSessionPresented       = caepBase + "session-presented"
	evtRiskLevelChange        = caepBase + "risk-level-change"
)

// subjectID is the SSF/CAEP Subject Identifier. A subject may be named by several
// formats (opaque id, email, iss_sub, uri, account); the connector reads the one
// the transmitter used. It never carries a credential.
type subjectID struct {
	Format string `json:"format"`
	ID     string `json:"id"`
	Email  string `json:"email"`
	URI    string `json:"uri"`
	Sub    string `json:"sub"`
	Iss    string `json:"iss"`
}

// ref returns the best stable reference for a subject, used as the finding's
// SubjectRef so it converges on the roster identity (external_id).
func (s *subjectID) ref() string {
	if s == nil {
		return ""
	}
	for _, v := range []string{s.ID, s.Email, s.URI, s.Sub} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// caepEvent is the parsed per-event payload the connector reads from one entry of
// the SET's "events" claim. Only governance-relevant, non-sensitive fields are read.
type caepEvent struct {
	Subject        *subjectID `json:"subject"`
	EventTimestamp int64      `json:"event_timestamp"`
	// credential-change
	ChangeType     string `json:"change_type"`
	CredentialType string `json:"credential_type"`
	// token-claims-change carries the new claims under "claims"; the connector does
	// NOT read their values (minimal data) — only that the event occurred.
}

// deriveFinding maps one CAEP event (its type URI + payload) to a governance
// FindingReport. The subject falls back to the SET-level sub_id when the per-event
// subject is absent. It returns ok=false for an event type the connector does not
// model. The finding is the kill-switch SIGNAL; governance applies the
// revocation of the observed session/credential.
func deriveFinding(eventType string, ev caepEvent, setSubject *subjectID, iss, jti string, fallback time.Time) (model.FindingReport, bool) {
	subject := ev.Subject
	if subject.ref() == "" {
		subject = setSubject
	}
	subjectRef := subject.ref()

	occurred := fallback
	if ev.EventTimestamp > 0 {
		occurred = time.Unix(ev.EventTimestamp, 0).UTC()
	}
	detail := redact.Hash(fmt.Sprintf("caep|%s|%s|%s|%s", eventType, subjectRef, iss, jti))

	kind, sev, label := classifyCAEP(eventType, ev)
	if kind == "" {
		return model.FindingReport{}, false
	}
	title := fmt.Sprintf("CAEP %s for subject %q from transmitter %q — %s", slug(eventType), subjectRef, iss, label)
	return model.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: "identity",
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  detail,
		OccurredAt:  occurred,
	}, true
}

// classifyCAEP maps an event type to the finding kind, severity and a short
// governance label. session-revoked and a credential revocation are the hard
// kill-switch (High); claims/assurance/compliance/risk changes are change signals.
func classifyCAEP(eventType string, ev caepEvent) (kind string, sev model.Severity, label string) {
	switch eventType {
	case evtSessionRevoked:
		return "caep_session_revoked", model.SeverityHigh, "revoke the agent's observed session (kill-switch)"
	case evtCredentialChange:
		if isRevocation(ev.ChangeType) {
			return "caep_credential_revoked", model.SeverityHigh, "a credential was revoked/deleted (kill-switch)"
		}
		return "caep_credential_change", model.SeverityMedium, "a credential changed"
	case evtTokenClaimsChange:
		return "caep_token_claims_change", model.SeverityMedium, "the agent's token claims changed (re-evaluate access)"
	case evtAssuranceLevelChange:
		return "caep_assurance_change", model.SeverityMedium, "the identity's assurance level changed"
	case evtDeviceComplianceChange:
		return "caep_device_compliance_change", model.SeverityMedium, "device compliance changed"
	case evtRiskLevelChange:
		return "caep_risk_level_change", model.SeverityMedium, "risk level changed"
	case evtSessionEstablished, evtSessionPresented:
		return "caep_session_event", model.SeverityInfo, "session lifecycle event observed"
	default:
		return "", "", ""
	}
}

// isRevocation reports whether a credential-change change_type denotes a removal.
func isRevocation(changeType string) bool {
	switch strings.ToLower(strings.TrimSpace(changeType)) {
	case "revoke", "delete":
		return true
	default:
		return false
	}
}

// slug returns the CAEP event slug (the part after the base URI) for display.
func slug(eventType string) string {
	return strings.TrimPrefix(eventType, caepBase)
}

// rawSubject lets the per-event subject be absent without breaking json decoding.
func decodeCAEPEvent(raw json.RawMessage) (caepEvent, error) {
	var ev caepEvent
	if len(raw) == 0 {
		return ev, nil
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}
