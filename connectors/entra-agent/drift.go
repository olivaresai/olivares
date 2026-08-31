// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Gather emits the connector's drift findings: one nhi_longlived_credential
// FindingReport per agent blueprint that holds static client secrets
// (passwordCredentials). Grounding: the Five Eyes joint guidance "Careful
// adoption of agentic AI services" (ASD/CISA/NSA/CCCS/NCSC-NZ/NCSC-UK,
// 2026-05-01) — "Replace static, long-lived secrets with ephemeral credentials".
// A blueprint secret is the worst long-lived credential in this registry: it is
// shared by EVERY agent the blueprint stamps out (via the blueprint principal),
// so one static secret is many agents' credential.
//
// Severity is detect/alert-first: SeverityMedium, escalated to SeverityHigh
// when ANY passwordCredential lacks endDateTime (a never-expiring secret).
// keyCredentials (certificates) are deliberately NOT flagged: certificate-based
// auth is the recommended replacement for static secrets, so flagging it would
// punish the fix.
//
// Minimal data: the $select asks Graph for the credential METADATA arrays only,
// the wire decode reads only each passwordCredential's endDateTime, and
// secretText/hint/customKeyIdentifier values are never read, stored, logged or
// emitted. Offline (credential not configured) it returns nil.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.offline() {
		return nil
	}
	tok, err := s.token(ctx)
	if err != nil {
		return err
	}
	client, err := s.graphClientFromToken(tok, nil)
	if err != nil {
		return err
	}
	now := s.clock().UTC()

	// Verified call shape (Graph v1.0 reference, 2026-06-11): the application
	// cast endpoint with $select=id,appId,displayName,passwordCredentials,
	// keyCredentials, paged via @odata.nextLink.
	query := url.Values{"$select": {"id,appId,displayName,passwordCredentials,keyCredentials"}}
	blueprints, err := collectPages[blueprint](ctx, client, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", query, s.maxPages)
	if err != nil {
		return err
	}

	for _, bp := range blueprints {
		n := len(bp.PasswordCredentials)
		if n == 0 {
			continue
		}
		severity := model.SeverityMedium
		for _, pc := range bp.PasswordCredentials {
			if pc.EndDateTime == "" { // null/absent expiry => the secret never expires
				severity = model.SeverityHigh
				break
			}
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        identitysource.FindingLongLivedCredential,
			Severity:    severity,
			SubjectKind: "identity",
			SubjectRef:  bp.AppID,
			Title:       fmt.Sprintf("agent blueprint holds %d static client secret(s)", n),
			// DetailHash fingerprints a stable, non-sensitive key (kind|source|
			// subject|count) so the engine de-duplicates the finding without any
			// credential detail ever traveling.
			DetailHash: redact.Hash("nhi_longlived_credential|entra-agent|" + bp.AppID + "|password_credentials=" + strconv.Itoa(n)),
			OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	if err := s.gatherPosture(ctx, sink, client, tok, now); err != nil {
		return err
	}
	if !s.ingestSignIns {
		return nil
	}
	signInClient, err := s.graphClientFromToken(tok, map[string]string{"Prefer": "include-unknown-enum-members"})
	if err != nil {
		return err
	}
	if err := s.gatherSignIns(ctx, sink, signInClient, now); err != nil && !toleratedPostureStatus(err) {
		return err
	}
	return nil
}
