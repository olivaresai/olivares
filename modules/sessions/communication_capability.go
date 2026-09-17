// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// communicationCapabilityDeadline bounds ONE capability projection. The K3 authority
// binder refuses an unbounded context on purpose — an authority reconstruction with no
// finite lifetime cannot state a freshness window — so the projection supplies its own
// when the caller's context carries none.
const communicationCapabilityDeadline = 15 * time.Second

var _ api.ModuleCapabilityProjector = (*Module)(nil)

// ProjectModuleCapability answers, for ONE of this module's registered CHANNEL
// ADMINISTRATION operations, whether K3's own authorization stage permits the calling
// credential right now — without performing the operation.
//
// ⛔ IT IS THE ROUTE'S SECOND STAGE, NOT A SECOND OPINION ABOUT THE FIRST. The engine's
// outer decision has already run when this is called; what this adds is the question the
// outer stage does not ask and cannot answer: the CURRENT local ChannelGrant. An ALLOW
// here means core admin AND local ADMIN, evaluated together against locked rows at
// database time, which is the same conjunction the administrative reads and the
// mutations apply.
//
// ⛔ IT NEVER CONSULTS THE READ OR WRITE BIT. Administration is independent of content
// read, and completing an admin decision with a read grant is exactly the substitution
// the whole administrative surface exists to refuse.
//
// ⛔ AND IT PERFORMS NO ACT. It does not call UpdateChannel, GrantChannel or
// RevokeChannelGrant, does not write a row, an audit event or an expiry, does not take a
// mutation's write locks or fences, and produces nothing the later mutation may consume
// as authority. It reuses the READ unit the administrative sheet uses — the one the HTTP
// battery already measures as effect-free — rather than a mutation with a rollback.
//
// ⛔ WHAT IT DELIBERATELY DOES NOT EVALUATE: the kernel readiness conjunction, the
// navigation keyring, If-Match, the successor generation, the recording gate. Those are
// readiness and business preconditions of the ACT; running them to paint a permission
// would make asking a question have consequences, and reporting them as a refusal would
// tell an operator their role is insufficient when their kernel is merely warming up.
// SupportsModuleCapability reports whether this module has a typed adapter for the
// operation. The discriminator is the permission the route was REGISTERED with, not a
// list of paths kept beside the router: a path table would be a second declaration of
// the same fact and would answer for a route it had never seen.
//
// It reads only the operation, so the engine can settle it before naming any target.
func (m *Module) SupportsModuleCapability(operation api.ModuleCapabilityOperation) bool {
	return operation.Permission == permChannelAdmin
}

// ModuleCapabilityAvailable reports whether this module's authorization stage can be
// evaluated at all for one caller in one workspace, WITHOUT naming a channel.
//
// It resolves exactly the shared authority the per-target projection would go on to use —
// the credential's directory identity and its ChannelGrant subject closure in that scope —
// and nothing else. No channel is read, no grant row is touched, and the scope is the
// workspace the QUESTION named rather than any row's stored lineage.
//
// ⛔ A PRINCIPAL THAT DOES NOT RESOLVE IS AVAILABLE, NOT UNAVAILABLE, and the distinction
// is the point of the whole probe. "There is no such directory principal in this scope"
// is an answer the stage CAN give; only an unbound port, a resolver error or an
// unreadable closure means it cannot answer at all. Collapsing the two would convert
// every legitimate refusal in the pilot into UNKNOWN — trading one wrong answer for a
// much larger one.
func (m *Module) ModuleCapabilityAvailable(
	ctx context.Context,
	operation api.ModuleCapabilityOperation,
	scope api.ModuleCapabilityScope,
) api.ModuleCapabilityAvailability {
	if !m.SupportsModuleCapability(operation) {
		return api.ModuleCapabilityAvailability{Code: "not_supported"}
	}
	sources := m.communicationAuthoritySources
	if sources == nil || !communicationPortBound(sources.resolver) ||
		!communicationPortBound(sources.source) {
		return api.ModuleCapabilityAvailability{Code: "engine_unready"}
	}
	directory := DirectoryScopeRef{TenantID: scope.Tenant, WorkspaceID: scope.Workspace}
	if directory.Validate() != nil {
		return api.ModuleCapabilityAvailability{Code: "inputs_required"}
	}
	principal, err := communicationPrincipalFromResolvedAuth(scope.Principal)
	if err != nil {
		// The credential is not a K3 principal at all. That is an established property
		// of the caller, not an outage: the stage can answer, and it will refuse.
		return api.ModuleCapabilityAvailability{Available: true}
	}
	if requireChannelAdministrationPrincipal(principal) != nil ||
		ValidateCommunicationPrincipalForScope(principal, directory) != nil {
		return api.ModuleCapabilityAvailability{Available: true}
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		bounded, cancel := context.WithTimeout(ctx, communicationCapabilityDeadline)
		defer cancel()
		ctx = bounded
	}
	_, err = m.preflightDirectNoticeReaderIdentity(ctx, directory, principal, nil)
	switch {
	case err == nil,
		errors.Is(err, errDirectNoticePrincipalNotFound),
		errors.Is(err, ErrCommunicationForbidden):
		return api.ModuleCapabilityAvailability{Available: true}
	default:
		return api.ModuleCapabilityAvailability{Code: "evidence_unavailable"}
	}
}

func (m *Module) ProjectModuleCapability(
	ctx context.Context,
	question api.ModuleCapabilityQuestion,
) api.ModuleCapabilityResult {
	// The adapter covers the channel ADMINISTRATION permission and nothing else. The
	// discriminator is the permission the route was REGISTERED with, not a list of
	// paths kept beside the router: a path table would be a second declaration of the
	// same fact and would answer for a route it had never seen.
	if question.Permission != permChannelAdmin {
		return unsupportedCommunicationCapability()
	}
	scope := DirectoryScopeRef{TenantID: question.Tenant, WorkspaceID: question.Resource.WorkspaceID}
	if scope.Validate() != nil {
		return unknownCommunicationCapability()
	}
	channelID, err := model.ParseID(question.Resource.ID)
	if err != nil || !validCanonicalCommunicationID(channelID) {
		return unknownCommunicationCapability()
	}
	ref, ok := question.Principal.Ref()
	if !ok {
		return unknownCommunicationCapability()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		bounded, cancel := context.WithTimeout(ctx, communicationCapabilityDeadline)
		defer cancel()
		ctx = bounded
	}
	observedAt, freshUntil, err := m.projectChannelAdministrationAuthority(ctx, scope, ref, channelID)
	if err != nil {
		return communicationCapabilityFromError(err)
	}
	return api.ModuleCapabilityResult{
		State: api.CapabilityAllowed, Code: "channel_admin_current",
		ObservedAt: observedAt, FreshUntil: freshUntil,
	}
}

// projectChannelAdministrationAuthority reconstructs current core authority for the
// calling credential, locks the Channel and the caller's OWN closure grants, and
// evaluates the ADMIN bit at database time.
//
// It returns the window this stage ACTUALLY CONSUMED, which is the transaction's own
// accumulated authority window at the instant the unit closed. By construction that is
// the intersection of every window the stage relied on:
//
//   - the core authorization evidence the binder sealed (transactionSnapshot),
//   - the reader's directory-resolution and grant-closure evidence
//     (directNoticeReaderAuthorityWindow, narrowed into the snapshot before the unit),
//   - the claim evidence where the credential carries one,
//   - and the OR-horizon of the ADMIN grants that still confer the bit.
//
// ⛔ AN EARLIER VERSION RETURNED ONLY THE LAST OF THOSE, AND THAT WAS THE DEFECT. It
// returned `(dbNow, channelGrantBitFreshUntil)` and this comment argued that the inner
// core window should be discarded because the binder clips it to this call's deadline —
// "a budget derived from a projection timeout rather than from authority". The argument
// was wrong in the direction that matters: a context-clipped window is still a REAL
// bound on evidence the kernel accepted and consumed, and dropping it let the endpoint
// publish a budget that OUTLIVED the evidence founding it. Measured by the independent
// review: a concrete closure producer configured with a 750 ms window still returned a
// 4990 ms budget.
//
// ⇒ The rule this now follows: a stage reports what it consumed, not what it judges
// worth reporting. Narrowing is always safe; deciding which real bound to omit is not.
// The transaction is the one place that already knows the whole intersection, so this
// reads it rather than recomputing a second opinion beside it.
func (m *Module) projectChannelAdministrationAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	channelID model.ID,
) (time.Time, time.Time, error) {
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, channelID, CommunicationChannelAdmin,
	)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return time.Time{}, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel capability authority context crossed its exact question",
		)
	}
	if err := requireChannelAdministrationPrincipal(inspected.principal); err != nil {
		return time.Time{}, time.Time{}, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(identity.Principal),
	)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var (
		observedAt time.Time
		freshUntil time.Time
		hidden     bool
	)
	err = m.mutateCommunicationWithNarrowedAuthority(
		ctx, question, bound, claims, window,
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			preflight, err := directNoticeReaderPreflightWithCore(identity, consumed.witness)
			if err != nil {
				return err
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, preflight.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			// The SAME transaction lock name the administrative sheet takes for this
			// Channel: this is that read unit, not a private one beside it.
			if err := tx.lockTransaction(ctx, fmt.Sprintf(
				"sessions_channel_admin_read|%s|%s|%s",
				scope.TenantID, scope.WorkspaceID, channelID,
			)); err != nil {
				return err
			}
			record, err := tx.lockRecord(ctx, channelKind, channelID)
			if errors.Is(err, store.ErrNotFound) {
				hidden = true
				return tx.refreshNow(ctx)
			}
			if err != nil {
				return err
			}
			channel, err := channelFromRecord(record)
			if err != nil || channel.ID != channelID ||
				channel.TenantID != scope.TenantID || channel.WorkspaceID != scope.WorkspaceID ||
				channel.Version < 1 || channel.ACLRevision < 1 {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "locked administrable Channel is malformed",
				)
			}
			// The authority set is the READER'S OWN closure grants, so a Channel
			// crowded with other subjects' grants imposes no ceiling on this answer.
			current, err := lockChannelAdministrationAuthorityGrants(
				ctx, tx, scope, channelID, preflight.Closure,
			)
			if err != nil {
				return err
			}
			epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
			if err != nil || epoch.Validate() != nil || epoch.TenantID != scope.TenantID ||
				epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch ||
				epoch.Version != preflight.Closure.DirectoryEpoch {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"locked administration directory epoch is unavailable",
				)
			}
			// Database time is refreshed AFTER the blocking locks, so the decision and
			// its horizon are judged at the instant the snapshot actually closed.
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			admin := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
				Verdict: VerdictClean, Code: "channel_grants_locked",
				ACLRevision: channel.ACLRevision, ObservedAt: dbNow, Grants: current,
			}, scope.TenantID, scope.WorkspaceID, channelID, preflight.Closure,
				ChannelGrantAdmin, dbNow)
			switch evidenceVerdict(admin.Evidence) {
			case VerdictClean:
				deadline, constrained, horizonErr := channelGrantBitFreshUntil(
					current, preflight.Closure, ChannelGrantAdmin, dbNow,
				)
				if horizonErr != nil {
					return horizonErr
				}
				if constrained {
					if err := tx.narrowRequestAuthorityFreshUntil(deadline); err != nil {
						return err
					}
				}
				// Read the accumulated window AFTER the last narrowing, so what leaves
				// this stage is exactly what it consumed.
				observedAt, freshUntil = tx.requestObservedAt, tx.requestFreshUntil
				return nil
			case VerdictBroken:
				// The caller holds no current ADMIN bit on this Channel. That is an
				// ESTABLISHED denial, and the concealment of the route decides how it
				// is shown — which is the engine's decision, not this adapter's.
				hidden = true
				return nil
			default:
				return communicationError(
					ErrCommunicationEvidenceUnknown, "channel administration grant is unavailable",
				)
			}
		},
	)
	if err != nil {
		// ⛔ NO normalizeDirectNoticePointReadError HERE, AND THAT IS A CORRECTION RATHER
		// THAN AN OMISSION. That helper folds ErrCommunicationForbidden into a not-found
		// so a point READ can conceal on the wire — but this adapter writes no wire, and
		// the ENGINE applies the route's own concealment afterwards. Folding here would
		// destroy the one distinction the projection is built on, turning an ESTABLISHED
		// denial into "I could not look" and reporting a broken kernel and a refused
		// caller with the same code. Concealment belongs to the route; classification
		// belongs here, and they are not the same decision.
		return time.Time{}, time.Time{}, err
	}
	if hidden {
		return time.Time{}, time.Time{}, communicationError(
			ErrCommunicationForbidden, "channel administration grant is not current",
		)
	}
	// A positive with no coherent window is not a short-lived positive: this stage
	// consumed evidence it cannot describe, so it reports no verdict rather than an
	// unbounded one.
	if observedAt.IsZero() || !freshUntil.After(observedAt) {
		return time.Time{}, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel administration authority window is unavailable",
		)
	}
	return observedAt, freshUntil, nil
}

// communicationCapabilityFromError maps a K3 decision onto the projection vocabulary.
//
// ⛔ ONLY A TYPED FORBIDDEN BECOMES A DENIAL. Everything else — an unavailable resolver,
// a store failure, a lock that could not be taken, an authority that could not be
// reconstructed — is UNKNOWN, because none of them is anybody saying no. This is the one
// mapping that decides whether a broken kernel is reported to an operator as "you may
// not", and it answers that question in the safe direction on every path.
func communicationCapabilityFromError(err error) api.ModuleCapabilityResult {
	switch {
	case errors.Is(err, ErrCommunicationForbidden):
		return api.ModuleCapabilityResult{
			State: api.CapabilityDenied, Code: "channel_admin_denied",
		}
	case errors.Is(err, ErrInvalidCommunicationModel):
		return api.ModuleCapabilityResult{
			State: api.CapabilityUnknown, Code: "inputs_required",
		}
	default:
		return unknownCommunicationCapability()
	}
}

func unknownCommunicationCapability() api.ModuleCapabilityResult {
	return api.ModuleCapabilityResult{
		State: api.CapabilityUnknown, Code: "evidence_unavailable",
	}
}

func unsupportedCommunicationCapability() api.ModuleCapabilityResult {
	return api.ModuleCapabilityResult{
		State: api.CapabilityUnknown, Code: "not_supported",
	}
}
