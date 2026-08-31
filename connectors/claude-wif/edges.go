// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// resWorkspaceAPI is the ResourceKind of a PERMITTED grant: an identity is permitted
// to call a workspace's Claude API. The granted oauth scope rides in the edge's
// ToolRef so module III can tell a developer grant from a tunnels grant.
const resWorkspaceAPI = "anthropic.workspace"

// Gather emits the PERMITTED grant edges and the WIF governance finding. It emits,
// in order: (1) the federated service-account → workspace grants (config-driven,
// independent of the admin credential); (2) the WIF footgun finding when a static
// key shadows federation; and (3), with an admin credential, the API-key → workspace
// grants enumerated from the Admin API. Every grant is a model.SignalPolicy edge —
// the PERMITTED side of the permitted-vs-observed diff (ARCHITECTURE.md). It is a batch
// source: it returns nil when drained.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := s.clock().UTC()

	// (1) Federated service-account grants — operator-declared, always emitted.
	for _, r := range s.federation {
		if err := sink.Emit(ctx, serviceAccountGrant(r, at)); err != nil {
			return err
		}
	}

	// (2) The WIF footgun finding.
	if finding, ok := s.detectShadowing(at); ok {
		if err := sink.Emit(ctx, finding); err != nil {
			return err
		}
	}

	// (3) Live WIF reconciliation drift — requires the org:admin OAuth client (distinct
	// from the sk-ant-admin key, which these endpoints reject). Runs independently of the
	// Admin key. A live-list failure degrades honestly: it emits a single
	// reconciliation-unavailable finding and continues, rather than failing the whole
	// Gather (the roster grants/footgun must not be coupled to the org:admin token's
	// health).
	if s.wifClient != nil {
		if live, ok, err := s.fetchLiveSet(ctx); err != nil {
			// A canceled/expired context is an abort, not an org-config problem — surface
			// it honestly instead of masquerading it as a reconciliation-unavailable finding.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := sink.Emit(ctx, s.driftFinding(driftReconUnavailable, model.SeverityMedium,
				subjectFederation, orgRefOr(s.orgID),
				"Live WIF reconciliation unavailable", "unavailable", at)); e != nil {
				return e
			}
		} else if ok {
			for _, f := range s.reconcileFindings(live, at) {
				if err := sink.Emit(ctx, f); err != nil {
					return err
				}
			}
		}
	}

	// (4) API-key grants — require the read-only Admin credential.
	if s.adminKey == "" || s.client == nil {
		return nil
	}
	keys, err := s.fetchAPIKeys(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.WorkspaceID == "" || k.Status != "active" {
			continue // an inactive key, or one with no workspace, grants no live access
		}
		if err := sink.Emit(ctx, apiKeyGrant(k, at)); err != nil {
			return err
		}
	}
	return nil
}

// orgRefOr returns the org id for an org-level finding subject, or a stable fallback
// when the org id was not configured.
func orgRefOr(orgID string) string {
	if orgID != "" {
		return orgID
	}
	return "anthropic.org"
}

// apiKeyGrant is the PERMITTED edge for an active API key: it is permitted its
// workspace's API at workspace:developer level (the access an API key has, verified
// against the WIF reference). The key's raw id is the origin external_id, so the
// grant resolves to the same NHI the roster created.
func apiKeyGrant(k apiKey, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   "identity",
		OriginRef:    k.ID,
		ResourceKind: resWorkspaceAPI,
		ResourceRef:  k.WorkspaceID,
		Mode:         model.ModeReadWrite,
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      scopeWorkspaceDeveloper,
		ObservedAt:   at,
	}
}

// serviceAccountGrant is the PERMITTED edge for a federated service account: it is
// permitted its rule's oauth_scope in the rule's workspace. The workspace target is
// the declared workspace id, or the literal "default" when the rule omits it.
func serviceAccountGrant(r FederationRule, at time.Time) model.EdgeObservation {
	target := r.WorkspaceID
	if target == "" {
		target = workspaceDefault
	}
	return model.EdgeObservation{
		OriginKind:   "identity",
		OriginRef:    r.ServiceAccountID,
		ResourceKind: resWorkspaceAPI,
		ResourceRef:  target,
		Mode:         modeForScope(r.scope()),
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      r.scope(),
		ObservedAt:   at,
	}
}

// modeForScope maps an oauth scope to a read/write classification. Both documented
// scopes confer write capability (developer = full non-administrative API; tunnels =
// managing the org's MCP tunnels), so both are read-write; an unknown scope is
// classified unknown rather than guessed.
func modeForScope(scope string) model.AccessMode {
	switch scope {
	case scopeWorkspaceDeveloper, scopeManageTunnels:
		return model.ModeReadWrite
	default:
		return model.ModeUnknown
	}
}
