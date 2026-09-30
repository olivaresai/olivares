// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// A governed resume preserves the operated SID, run attribution and workspace;
// acquiring its successor Claim advances that SID's monotonically increasing
// fence. The current directory resolution has already corroborated the exact
// SID/run/agent/fence tuple. Historical audience provenance is never rewritten.
func directNoticeSessionGenerationReadable(
	preflight directNoticeReaderPreflight,
	contribution MessageAudienceRecipient,
) bool {
	p := preflight.Principal
	return p.PurposeRestricted && p.SessionID != "" && p.SessionRunRef != "" &&
		p.SessionWorkspaceID == preflight.Scope.WorkspaceID &&
		contribution.Recipient == (RecipientRef{Kind: RecipientSession, Ref: p.SessionID}) &&
		contribution.ObservedSessionSID == p.SessionID &&
		contribution.ObservedClaimFence > 0 && contribution.ObservedClaimFence <= p.SessionFence
}

// Only the two recipient READ gates may discharge a historical causal Claim
// through its locked current successor. Mutation/ack/handoff planners continue
// to demand their own exact current authority. The evaluator's historical
// requirement and the stored causal arc both retain their original meaning.
func directNoticeReadClaimsMatch(
	preflight directNoticeReaderPreflight,
	contribution MessageAudienceRecipient,
	required []CommunicationClaimRef,
	locked []store.AuthorizationFactRef,
) bool {
	snapshot := CommunicationClaimAuthoritySnapshot{facts: locked}
	if communicationClaimsEqualSnapshot(required, snapshot) {
		return true
	}
	if !directNoticeSessionGenerationReadable(preflight, contribution) ||
		contribution.ObservedClaimFence >= preflight.Principal.SessionFence || len(required) != 2 ||
		required[0] != (CommunicationClaimRef{SessionSID: contribution.ObservedSessionSID, Fence: contribution.ObservedClaimFence}) ||
		required[1] != (CommunicationClaimRef{SessionSID: preflight.Principal.SessionID, Fence: preflight.Principal.SessionFence}) {
		return false
	}
	return communicationClaimsEqualSnapshot(communicationClaimsForPrincipal(preflight.Principal), snapshot)
}

// The shared builder keeps its exact-generation behavior for every mutation.
// Only the read gates construct a clean successor witness for a historical
// carrier, after the locked graph has corroborated its immutable causal seal.
func buildDirectNoticeReadCurrentAudience(
	preflight directNoticeReaderPreflight,
	message Message,
	delivery MessageDelivery,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
	dbNow time.Time,
) (CurrentAudienceEvidence, error) {
	evidence, err := buildDirectNoticeCurrentAudience(preflight, message, delivery, audiences, contributions, dbNow)
	if err != nil {
		return CurrentAudienceEvidence{}, err
	}
	contribution := contributions[0]
	if directNoticeSessionGenerationReadable(preflight, contribution) &&
		contribution.ObservedClaimFence < preflight.Principal.SessionFence {
		evidence.Contributions[0].Witness.Evidence.Verdict = VerdictClean
		evidence.Contributions[0].Witness.Evidence.Code = "session_claim_successor_read"
	}
	return evidence, nil
}
