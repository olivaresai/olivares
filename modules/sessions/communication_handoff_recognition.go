// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

type handoffResponseMode uint8

const (
	handoffResponseApply handoffResponseMode = iota + 1
	handoffResponseRecognize
)

// handoffStaleClaimFailure is returned only from the original response's failed
// authority phase. It retains the supplied witness, not the Store cause, so the
// public error remains unknown and cannot accidentally become a 409 conflict.
type handoffStaleClaimFailure struct {
	fact store.AuthorizationFactRef
}

func (*handoffStaleClaimFailure) Error() string {
	return "Handoff responder authority changed while locking"
}

func (*handoffStaleClaimFailure) Unwrap() error { return ErrCommunicationEvidenceUnknown }

func handoffRecognitionUnknown() error {
	return communicationError(ErrCommunicationEvidenceUnknown, "Handoff recognition is unavailable")
}

func captureHandoffResponseAuthorityFailure(
	err error,
	normalized handoffResponseNormalized,
	claims CommunicationClaimAuthoritySnapshot,
) error {
	if normalized.command.Transition == HandoffAccept && normalized.principal.SessionID != "" {
		for _, fact := range claims.facts {
			if !handoffResponderClaim(fact, normalized.principal) {
				continue
			}
			if handoffRecognitionErrorTree(err, true, func(node error) bool {
				conflict, ok := node.(*store.LockedLeasedVersionConflict)
				return ok && conflict.Matches(fact)
			}) {
				return &handoffStaleClaimFailure{fact: fact}
			}
		}
	}
	return normalizeDirectNoticeAuthorityLockError(err)
}

// handoffRecognitionErrorTree accepts one exact failure and, at the authority
// phase only, the existing unknown sentinel in the two-cause wrapper. Looking
// for any errors.As match would admit unrelated failures joined by an adapter.
// The node budget also refuses malformed cyclic or unbounded wrapper trees.
func handoffRecognitionErrorTree(err error, allowUnknown bool, match func(error) bool) bool {
	remaining, matches, unknowns := 64, 0, 0
	var visit func(error) bool
	visit = func(node error) bool {
		remaining--
		if node == nil || remaining < 0 {
			return false
		}
		if match(node) {
			matches++
			return matches == 1
		}
		if node == ErrCommunicationEvidenceUnknown {
			unknowns++
			return allowUnknown && unknowns == 1
		}
		switch wrapped := node.(type) {
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			if len(children) == 0 || len(children) > remaining {
				return false
			}
			for _, child := range children {
				if !visit(child) {
					return false
				}
			}
			return true
		case interface{ Unwrap() error }:
			return visit(wrapped.Unwrap())
		default:
			return false
		}
	}
	return visit(err) && matches == 1
}

func handoffResponseRecognitionFailure(err error) (*handoffStaleClaimFailure, bool) {
	var result *handoffStaleClaimFailure
	ok := handoffRecognitionErrorTree(err, false, func(node error) bool {
		stale, found := node.(*handoffStaleClaimFailure)
		if found && stale != nil {
			result = stale
			return true
		}
		return false
	})
	return result, ok
}

func handoffResponderClaim(fact store.AuthorizationFactRef, principal CommunicationPrincipal) bool {
	sid, fence, _, ok := fact.LeaseFenceWitness()
	return ok && fact.Kind == claimKind && !fact.ID.IsZero() && fact.Version > 0 &&
		principal.SessionID != "" && sid == principal.SessionID && fence == principal.SessionFence
}

func sameHandoffClaimSemantics(before, current store.AuthorizationFactRef) bool {
	sid, fence, deadline, ok := before.LeaseFenceWitness()
	currentSID, currentFence, currentDeadline, currentOK := current.LeaseFenceWitness()
	return ok && currentOK && before.Kind == current.Kind && before.ID == current.ID &&
		before.Version > 0 && current.Version > 0 && sid == currentSID && fence == currentFence &&
		deadline.String() == currentDeadline.String()
}

func sameHandoffResponseIdentity(before, current handoffResponseNormalized) bool {
	return before.scope == current.scope && before.principal == current.principal &&
		before.actor == current.actor && before.handoffID == current.handoffID &&
		before.commandScope == current.commandScope && before.expectedVersion == current.expectedVersion &&
		before.command.Transition == current.command.Transition && before.command.IfMatch == current.command.IfMatch &&
		before.command.IdempotencyKey == current.command.IdempotencyKey &&
		bytes.Equal(before.actorFingerprint, current.actorFingerprint) &&
		bytes.Equal(before.idempotencyKeyHash, current.idempotencyKeyHash) &&
		bytes.Equal(before.requestDigest, current.requestDigest)
}

// recognizeHandoffResponse owns one new admission after the original mutation
// returned. It never invokes the ordinary response service recursively and never
// joins a failed protocol transaction. Any failure, including an uncertain commit
// of the Claim touch, ends this attempt without publishing a successful result.
func (m *Module) recognizeHandoffResponse(
	ctx context.Context,
	ref auth.PrincipalRef,
	original handoffResponseNormalized,
	originalClaim store.AuthorizationFactRef,
	requireReadiness bool,
) (HandoffResponseResult, error) {
	deadline, finite := ctx.Deadline()
	if ctx.Err() != nil || !finite || !time.Now().Before(deadline) ||
		original.command.Transition != HandoffAccept || !handoffResponderClaim(originalClaim, original.principal) {
		return HandoffResponseResult{}, handoffRecognitionUnknown()
	}
	if _, joined := protocolReplayScopeFromContext(ctx, original.scope.TenantID); joined {
		return HandoffResponseResult{}, handoffRecognitionUnknown()
	}
	question, bound, inspected, identity, normalized, window, err := m.prepareHandoffResponseAuthority(
		ctx, original.scope, ref, original.handoffID, original.command,
	)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	if !sameHandoffResponseIdentity(original, normalized) {
		return HandoffResponseResult{}, handoffRecognitionUnknown()
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return HandoffResponseResult{}, handoffRecognitionUnknown()
		}
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, original.scope.TenantID, communicationClaimsForPrincipal(normalized.principal),
	)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	if len(claims.facts) != 1 || !sameHandoffClaimSemantics(originalClaim, claims.facts[0]) {
		return HandoffResponseResult{}, handoffRecognitionUnknown()
	}
	return m.applyHandoffResponse(
		ctx, question, bound, inspected, identity, window, claims, normalized,
		handoffResponseIDs{}, handoffResponsePrepared{}, handoffResponseRecognize,
	)
}
