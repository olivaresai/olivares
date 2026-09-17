// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The administrable-Channel catalog is the K3 collection read behind
// GET /v1/m/sessions/channels/administration. It is the ADMINISTRATIVE twin of
// the read catalog and shares none of its verdicts: a Channel appears here only
// when BOTH hold at transaction time — the exact core Channel/ChannelAdmin
// question for that Channel is allowed, AND the caller's current grant-subject
// closure holds an active, unexpired local grant with the ADMIN bit. Local read
// is neither required nor implied: an administrator who cannot read a Channel's
// content still has to be able to find it, and finding it here never opens it.
//
// The parameterization is closed and typed: the operation is
// CommunicationChannelAdmin and the local bit is ChannelGrantAdmin, named at
// every step. Nothing copies a read context and relabels it, and no code path
// substitutes read for admin globally.
//
// Discovery is a prefilter, not authorization. It projects candidate Channel IDs
// grant-first (current ADMIN grants of the closure subjects, in Channel-ID
// order, strictly after the continuation anchor) through one parameterized store
// statement per bounded subject batch, corroborates that the Channel row is in
// the confined scope and matches the requested persisted state selection, and
// asks the exact core question per candidate. The page is then closed in ONE
// bound communication transaction that locks the fact union, every selected
// Channel and its current grant rows, corroborates the directory epoch,
// refreshes database time after all blocking locks, re-evaluates the admin bit
// and narrows the request window to the OR-horizon of the admin grants that
// still confer it. Nothing is materialized before that transaction commits; the
// continuation is minted afterwards from the last Channel actually returned.
//
// The page is PURE in the domain: it grants nothing, revokes nothing,
// materializes no expiry, advances no revision, appends no mutation audit,
// acknowledges nothing and opens no protected payload.
const (
	channelAdministrationDefaultLimit = channelCatalogDefaultLimit
	channelAdministrationMaximumLimit = channelCatalogMaximumLimit
	// channelAdministrationSubjectBatch bounds the ORed subject alternatives per
	// projection statement, exactly as the read catalog does.
	channelAdministrationSubjectBatch = channelCatalogSubjectBatch
	// channelAdministrationDiscoveryBatch is the projected candidate page per
	// discovery round.
	channelAdministrationDiscoveryBatch = channelCatalogDiscoveryBatch
	// channelAdministrationClosureBound is the existing closure cardinality ceiling.
	channelAdministrationClosureBound = channelCatalogClosureBound
)

// ChannelAdministrationStateFilter selects which persisted Channel states the
// administrative catalog reports. Archived Channels are inspectable on purpose:
// reading an archived Channel's configuration and grants must not depend on the
// operational read catalog, which only lists active ones. Nothing here offers to
// un-archive: archived remains terminal.
type ChannelAdministrationStateFilter string

const (
	ChannelAdministrationStateAll      ChannelAdministrationStateFilter = "all"
	ChannelAdministrationStateActive   ChannelAdministrationStateFilter = "active"
	ChannelAdministrationStateArchived ChannelAdministrationStateFilter = "archived"
)

// channelAdministrationStateFilters is the CLOSED set, in the order the filter
// hash census and the contract enumerate it. Anything outside it is a 400.
func channelAdministrationStateFilters() []ChannelAdministrationStateFilter {
	return []ChannelAdministrationStateFilter{
		ChannelAdministrationStateAll,
		ChannelAdministrationStateActive,
		ChannelAdministrationStateArchived,
	}
}

func (f ChannelAdministrationStateFilter) Valid() bool {
	switch f {
	case ChannelAdministrationStateAll, ChannelAdministrationStateActive,
		ChannelAdministrationStateArchived:
		return true
	default:
		return false
	}
}

// admits reports whether a stored Channel state belongs to this selection.
func (f ChannelAdministrationStateFilter) admits(state ChannelState) bool {
	switch f {
	case ChannelAdministrationStateAll:
		return state == ChannelActive || state == ChannelArchived
	case ChannelAdministrationStateActive:
		return state == ChannelActive
	case ChannelAdministrationStateArchived:
		return state == ChannelArchived
	default:
		return false
	}
}

// ChannelAdministrationRequest is the public administrative catalog navigation
// contract. The continuation is a c3a1 token anchored to the last returned
// Channel; raw Channel positions, store cursors and client-supplied anchors are
// never accepted.
type ChannelAdministrationRequest struct {
	State        ChannelAdministrationStateFilter `json:"state,omitempty"`
	Continuation string                           `json:"continuation,omitempty"`
	Limit        int                              `json:"limit,omitempty"`
}

// ChannelAdministrationItem is one administrable Channel and the strong ETag of
// its CURRENT version. The ETag is the precondition of the existing Channel
// mutations; it is deliberately not an HTTP representation validator for this
// page, whose body also varies with filters and pagination.
//
// It carries no my_access: this page authorizes ONE administrative decision and
// does not emit accessory read/write claims that would need their own closures.
type ChannelAdministrationItem struct {
	Channel Channel `json:"channel"`
	ETag    string  `json:"etag"`
}

// ChannelAdministrationPage reports only administrable Channels. has_more is
// true exactly when the same bound transaction authorized a limit+1th Channel,
// and continuation is present only in that case. There is no total and no
// hidden-row side channel.
type ChannelAdministrationPage struct {
	Items        api.JSONArray[ChannelAdministrationItem] `json:"items"`
	Continuation string                                   `json:"continuation,omitempty"`
	HasMore      bool                                     `json:"has_more"`
}

// requireChannelAdministrationPrincipal admits the directory principals the
// administrative surfaces serve: users and exchanged agents (and any other
// valid, non-system K3 principal). A communication-session credential lacks the
// core sessions:channel:admin ceiling and is refused by the outer PEP before it
// reaches here; holding a grant whose subject kind is `session` does not create
// that core permission.
func requireChannelAdministrationPrincipal(principal CommunicationPrincipal) error {
	if ValidateCommunicationPrincipal(principal) != nil || principal.System {
		return communicationError(
			ErrCommunicationForbidden,
			"channel administration requires a directory principal credential",
		)
	}
	return nil
}

type channelAdministrationCandidate struct {
	channelID model.ID
	question  communicationAuthorityQuestion
}

// ListAdministrableChannels is the handler-facing administrative catalog read,
// gated by the complete K3 readiness conjunction after the current credential is
// rebound.
func (m *Module) ListAdministrableChannels(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelAdministrationRequest,
) (ChannelAdministrationPage, error) {
	return m.listAdministrableChannels(ctx, scope, ref, request, true)
}

// listAdministrableChannelsWithAuthority is the exact private seam the focused
// module tests use while the aggregate readiness conjunction is OFF, in the same
// shape as listVisibleChannelsWithAuthority. The credential is still rebound
// first; only the readiness term is skipped.
func (m *Module) listAdministrableChannelsWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelAdministrationRequest,
) (ChannelAdministrationPage, error) {
	return m.listAdministrableChannels(ctx, scope, ref, request, false)
}

func (m *Module) listAdministrableChannels(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelAdministrationRequest,
	requireReadiness bool,
) (ChannelAdministrationPage, error) {
	state := request.State
	if state == "" {
		state = ChannelAdministrationStateAll
	}
	if !state.Valid() || request.Limit < 0 || request.Limit > channelAdministrationMaximumLimit ||
		len(request.Continuation) > communicationCursorTokenMaxBytes {
		return ChannelAdministrationPage{}, communicationError(
			ErrInvalidCommunicationModel, "invalid channel administration navigation",
		)
	}
	limit := request.Limit
	if limit == 0 {
		limit = channelAdministrationDefaultLimit
	}
	identity, err := m.bindCurrentCommunicationIdentity(
		ctx, scope, ref, requireChannelAdministrationPrincipal,
	)
	if err != nil {
		return ChannelAdministrationPage{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return ChannelAdministrationPage{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	if !m.CommunicationCursorTokenKeyringBound() {
		return ChannelAdministrationPage{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel administration navigation keyring is unavailable",
		)
	}
	reader, err := m.preflightChannelAdministrationReader(ctx, identity)
	if err != nil {
		if errors.Is(err, errDirectNoticePrincipalNotFound) {
			return ChannelAdministrationPage{}, nil
		}
		return ChannelAdministrationPage{}, err
	}
	if reader.Closure.Outcome == ReadDeny {
		// A valid, current DENY closure: the principal is not a current member of
		// anything in this workspace. Nothing is administrable; nothing is disclosed.
		return ChannelAdministrationPage{}, nil
	}

	var anchor model.ID
	if request.Continuation != "" {
		observedAt, err := m.observeChannelCatalogDatabaseTime(ctx, scope)
		if err != nil {
			return ChannelAdministrationPage{}, err
		}
		claims, err := m.communicationCursorTokenKeyring().verifyChannelAdministrationNavigation(
			request.Continuation, observedAt,
		)
		if err != nil {
			return ChannelAdministrationPage{}, err
		}
		filter := channelAdministrationFilterHash(state)
		if claims.tenantID != scope.TenantID || claims.workspaceID != scope.WorkspaceID ||
			claims.reader != reader.Recipient || !bytes.Equal(claims.filterHash, filter[:]) {
			return ChannelAdministrationPage{}, communicationCursorTokenInvalid(
				"administration navigation token crossed its authenticated reader or selection",
			)
		}
		anchor = claims.lastChannelID
	}

	provisional, err := m.discoverChannelAdministrationCandidates(
		ctx, identity, reader, state, anchor, limit+1,
	)
	if err != nil {
		return ChannelAdministrationPage{}, err
	}
	if len(provisional) == 0 {
		return ChannelAdministrationPage{}, nil
	}
	items, observedAt, err := m.closeChannelAdministrationPage(ctx, identity, state, provisional)
	if err != nil {
		return ChannelAdministrationPage{}, err
	}
	page := ChannelAdministrationPage{Items: api.JSONArray[ChannelAdministrationItem]{}}
	if len(items) > limit {
		page.HasMore = true
		items = items[:limit]
	}
	page.Items = append(page.Items, items...)
	if page.HasMore {
		last := items[len(items)-1]
		filter := channelAdministrationFilterHash(state)
		token, err := m.communicationCursorTokenKeyring().mintChannelAdministrationNavigation(
			communicationChannelAdministrationNavigationClaims{
				tenantID: scope.TenantID, workspaceID: scope.WorkspaceID,
				reader: reader.Recipient, filterHash: filter[:], lastChannelID: last.Channel.ID,
			},
			observedAt,
		)
		if err != nil {
			return ChannelAdministrationPage{}, err
		}
		page.Continuation = token
	}
	return page, nil
}

// preflightChannelAdministrationReader resolves the rebound principal once, then
// validates and deduplicates its grant-subject closure under the existing
// cardinality bound. ReadUnknown is unavailable; a DENY closure is returned to
// the caller, which answers an empty page.
func (m *Module) preflightChannelAdministrationReader(
	ctx context.Context,
	identity communicationIdentityBinding,
) (directNoticeReaderIdentityPreflight, error) {
	reader, err := m.preflightDirectNoticeReaderIdentity(
		ctx, identity.scope, identity.principal, nil,
	)
	if err != nil {
		return directNoticeReaderIdentityPreflight{}, err
	}
	if err := validateChannelAdministrationClosure(reader.Closure); err != nil {
		return directNoticeReaderIdentityPreflight{}, err
	}
	return reader, nil
}

func validateChannelAdministrationClosure(closure ChannelGrantSubjectClosure) error {
	if closure.Outcome == ReadUnknown || !closure.Outcome.Valid() {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "channel administration ChannelGrant closure is unavailable",
		)
	}
	if len(closure.Subjects) > channelAdministrationClosureBound {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "channel administration ChannelGrant closure exceeds bound",
		)
	}
	seen := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "channel administration closure subject is malformed",
			)
		}
		if _, duplicate := seen[subject]; duplicate {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "channel administration closure subject is repeated",
			)
		}
		seen[subject] = struct{}{}
	}
	if closure.Outcome == ReadAllow && len(closure.Subjects) == 0 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "channel administration closure allows without subjects",
		)
	}
	return nil
}

// discoverChannelAdministrationCandidates runs discovery rounds until `want`
// provisionally administrable candidates exist or the grant projection is
// exhausted. Every round projects admin-grant-first candidates after the current
// anchor, keeps the Channels of the selected persisted states among them and
// asks the exact core Channel/ChannelAdmin question for each; core DENY is
// concealed, core UNKNOWN aborts. There is no candidate cutoff: a run of hidden
// Channels only costs bounded projection rounds.
func (m *Module) discoverChannelAdministrationCandidates(
	ctx context.Context,
	identity communicationIdentityBinding,
	reader directNoticeReaderIdentityPreflight,
	state ChannelAdministrationStateFilter,
	anchor model.ID,
	want int,
) ([]channelAdministrationCandidate, error) {
	if want < 1 || want > directNoticeInboxCandidateBound {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "channel administration discovery size is invalid",
		)
	}
	provisional := make([]channelAdministrationCandidate, 0, want)
	after := anchor
	for len(provisional) < want {
		page, err := m.projectChannelAdministrationCandidates(
			ctx, identity.scope, reader.Closure.Subjects, after, channelAdministrationDiscoveryBatch,
		)
		if err != nil {
			return nil, err
		}
		if len(page.candidates) == 0 {
			if page.hasMore {
				return nil, communicationError(
					ErrCommunicationEvidenceUnknown,
					"channel administration projection was truncated without a continuation",
				)
			}
			break
		}
		if err := m.requireDirectNoticeIdentityCurrent(reader); err != nil {
			return nil, err
		}
		selected, err := m.corroborateChannelAdministrationCandidates(
			ctx, identity.scope, state, page.candidates,
		)
		if err != nil {
			return nil, err
		}
		if len(selected) > 0 {
			questions := make([]communicationAuthorityQuestion, 0, len(selected))
			for _, channelID := range selected {
				question, err := newCommunicationAuthorityQuestion(
					identity.scope, channelKind, channelID, CommunicationChannelAdmin,
				)
				if err != nil {
					return nil, err
				}
				questions = append(questions, question)
			}
			_, admitted, err := bindCommunicationAuthorityBatch(
				ctx, identity, requireChannelAdministrationPrincipal, questions,
			)
			if err != nil {
				return nil, err
			}
			for _, index := range admitted {
				provisional = append(provisional, channelAdministrationCandidate{
					channelID: selected[index], question: questions[index],
				})
				if len(provisional) == want {
					break
				}
			}
		}
		after = page.candidates[len(page.candidates)-1]
		if !page.hasMore {
			break
		}
	}
	return provisional, nil
}

// projectChannelAdministrationCandidates is the admin-grant-first candidate
// projection. It runs on the confined View that observes database time, so the
// expiry predicate and the observation share one engine clock. Each subject
// batch is one store.DistinctProjector statement over sessions_channel_grant
// (state active, ADMIN bit, unset-or-after expiry, Channel ID strictly after the
// anchor, ascending) in which the engine renders every closure subject as its
// own bounded index-range arm and unions the arms, so the rows examined scale
// with the closure and the page, never with the grants other subjects hold in
// the workspace. The ordered results of several batches are merged so the
// returned page is the same ordered distinct prefix one statement would have
// produced. The declared sessions_channel_grant_administration index carries
// exactly these columns.
func (m *Module) projectChannelAdministrationCandidates(
	ctx context.Context,
	scope DirectoryScopeRef,
	subjects []CommunicationSubjectRef,
	after model.ID,
	limit int,
) (channelCatalogProjectionPage, error) {
	if limit < 1 || limit > store.DistinctProjectionMaxLimit || len(subjects) == 0 ||
		len(subjects) > channelAdministrationClosureBound {
		return channelCatalogProjectionPage{}, communicationError(
			ErrInvalidCommunicationModel, "channel administration projection request is invalid",
		)
	}
	var page channelCatalogProjectionPage
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return communicationTransactionUnavailable("transaction clock", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return communicationTransactionUnavailable("transaction clock", err)
		}
		if now.IsZero() {
			return communicationTransactionUnavailable("transaction clock returned zero", nil)
		}
		repo, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		projector, ok := repo.(store.DistinctProjector)
		if !ok {
			return communicationTransactionUnavailable("channel grant distinct projector", nil)
		}
		var pages []store.DistinctPage
		for start := 0; start < len(subjects); start += channelAdministrationSubjectBatch {
			end := start + channelAdministrationSubjectBatch
			if end > len(subjects) {
				end = len(subjects)
			}
			projection := store.DistinctProjection{
				Column: colCommChannelID, Limit: limit,
				Filters: []model.Filter{
					{Column: colCommState, Op: model.OpEq, Value: string(ChannelGrantActive)},
					{Column: colCommCanAdmin, Op: model.OpEq, Value: true},
					{Column: colCommExpiresAt, Op: model.OpUnsetOrGt, Value: now.String()},
				},
			}
			if !after.IsZero() {
				projection.After = after.String()
			}
			for _, subject := range subjects[start:end] {
				projection.AnyOf = append(projection.AnyOf, []model.Filter{
					{Column: colCommSubjectKind, Op: model.OpEq, Value: string(subject.Kind)},
					{Column: colCommSubjectRef, Op: model.OpEq, Value: subject.Ref},
				})
			}
			projected, err := projector.ProjectDistinct(ctx, projection)
			if err != nil {
				return err
			}
			pages = append(pages, projected)
		}
		merged, hasMore, err := mergeChannelCatalogProjection(pages, after, limit)
		if err != nil {
			return err
		}
		page = channelCatalogProjectionPage{
			candidates: merged, hasMore: hasMore, observedAt: now.Time(),
		}
		return nil
	})
	if err != nil {
		return channelCatalogProjectionPage{}, err
	}
	return page, nil
}

// corroborateChannelAdministrationCandidates keeps the projected candidates
// whose Channel row exists in the confined workspace and whose persisted state
// belongs to the requested selection. Work scales with projected
// (admin-grant-visible) candidates only; a Channel on which the caller holds no
// current admin grant is never read here.
func (m *Module) corroborateChannelAdministrationCandidates(
	ctx context.Context,
	scope DirectoryScopeRef,
	state ChannelAdministrationStateFilter,
	candidates []model.ID,
) ([]model.ID, error) {
	selected := make([]model.ID, 0, len(candidates))
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		repo, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		for _, channelID := range candidates {
			record, err := repo.Get(ctx, channelID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			channel, err := channelFromRecord(record)
			if err != nil {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "channel administration candidate row is malformed",
				)
			}
			if channel.ID != channelID || channel.TenantID != scope.TenantID ||
				channel.WorkspaceID != scope.WorkspaceID || !state.admits(channel.State) {
				continue
			}
			selected = append(selected, channelID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}

// closeChannelAdministrationPage freshly binds exact core ChannelAdmin evidence
// for every selected candidate (page plus lookahead), freshly resolves the
// reader identity and closure, and closes the whole set in one bound
// transaction. Any Channel whose admin bit is not Clean at that point aborts the
// page as evidence unavailable: a selected item is never silently dropped,
// because dropping it would let a later Channel take its place unseen, and a
// partially closed page must never be returned.
func (m *Module) closeChannelAdministrationPage(
	ctx context.Context,
	identity communicationIdentityBinding,
	state ChannelAdministrationStateFilter,
	selected []channelAdministrationCandidate,
) ([]ChannelAdministrationItem, time.Time, error) {
	if len(selected) == 0 || len(selected) > directNoticeInboxCandidateBound {
		return nil, time.Time{}, communicationError(
			ErrInvalidCommunicationModel, "channel administration page closure is invalid",
		)
	}
	reader, err := m.preflightChannelAdministrationReader(ctx, identity)
	if err != nil {
		return nil, time.Time{}, err
	}
	if reader.Closure.Outcome != ReadAllow {
		return nil, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel administration closure changed before closure",
		)
	}
	window, err := directNoticeReaderAuthorityWindow(reader)
	if err != nil {
		return nil, time.Time{}, err
	}
	questions := make([]communicationAuthorityQuestion, len(selected))
	for index, candidate := range selected {
		if candidate.question.entity.Kind != channelKind ||
			candidate.question.operation != CommunicationChannelAdmin ||
			candidate.question.entity.ID != candidate.channelID ||
			(index > 0 && candidate.channelID.String() <= selected[index-1].channelID.String()) {
			return nil, time.Time{}, communicationError(
				ErrCommunicationEvidenceUnknown, "channel administration candidate crossed its question",
			)
		}
		questions[index] = candidate.question
	}
	batch, admitted, err := bindCommunicationAuthorityBatch(
		ctx, identity, requireChannelAdministrationPrincipal, questions,
	)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(admitted) != len(questions) {
		return nil, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel administration core authority changed before page closure",
		)
	}

	var items []ChannelAdministrationItem
	var observedAt time.Time
	err = m.mutateCommunicationWithBoundAuthorityBatch(
		ctx, identity.scope, identity, requireChannelAdministrationPrincipal,
		questions, batch, window,
		func(tx *communicationTx, contexts []communicationRequestAuthorityBatchContext) error {
			if len(contexts) != len(questions) {
				return directNoticeReadUnknown("channel administration authority batch changed size", nil)
			}
			allFacts := make([]store.AuthorizationFactRef, 0)
			for index, bound := range contexts {
				if bound.question != questions[index] || bound.principal != identity.principal {
					return directNoticeReadUnknown("channel administration authority crossed its candidate", nil)
				}
				preflight, err := directNoticeReaderPreflightWithCore(reader, bound.witness)
				if err != nil {
					return err
				}
				allFacts = append(allFacts, preflight.Facts...)
			}
			facts, err := canonicalAuthorizationFactUnion(allFacts)
			if err != nil {
				return directNoticeReadUnknown("channel administration authority facts are unavailable", err)
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			scope := identity.scope
			channels := make([]Channel, 0, len(selected))
			for _, candidate := range selected {
				record, err := tx.lockRecord(ctx, channelKind, candidate.channelID)
				if err != nil {
					return normalizeDirectNoticeAuthorityLockError(err)
				}
				channel, err := channelFromRecord(record)
				if err != nil {
					return directNoticeReadUnknown("locked administrable Channel is malformed", err)
				}
				if channel.ID != candidate.channelID || channel.TenantID != scope.TenantID ||
					channel.WorkspaceID != scope.WorkspaceID || !state.admits(channel.State) ||
					channel.ACLRevision < 1 || channel.Version < 1 {
					return directNoticeReadUnknown("locked administrable Channel is no longer current", nil)
				}
				channels = append(channels, channel)
			}
			// The authority set is the READER'S OWN closure grants on each selected
			// Channel — not every active grant the Channel carries. Unrelated
			// subjects therefore impose no ceiling on this page.
			grants := make([][]ChannelGrant, len(channels))
			for index, channel := range channels {
				locked, err := lockChannelAdministrationAuthorityGrants(
					ctx, tx, scope, channel.ID, reader.Closure,
				)
				if err != nil {
					return err
				}
				grants[index] = locked
			}
			epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
			if err != nil || epoch.Validate() != nil || epoch.TenantID != scope.TenantID ||
				epoch.Version != reader.Resolution.Recipient.DirectoryEpoch ||
				epoch.Version != reader.Closure.DirectoryEpoch {
				return directNoticeReadUnknown("locked administration directory epoch is unavailable", err)
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			observedAt = dbNow
			built := make([]ChannelAdministrationItem, 0, len(channels))
			for index, channel := range channels {
				if err := requireCurrentChannelAdminGrantLocked(
					tx, scope, channel, grants[index], reader.Closure, dbNow,
				); err != nil {
					return err
				}
				built = append(built, ChannelAdministrationItem{
					Channel: channel, ETag: communicationVersionETag(channel.Version),
				})
			}
			items = built
			return nil
		},
	)
	if err != nil {
		return nil, time.Time{}, normalizeDirectNoticeAuthorizationError(err)
	}
	return items, observedAt, nil
}

// requireCurrentChannelAdminGrantLocked evaluates the ADMIN bit — and only the
// admin bit — against the locked grant set and the fresh closure at database
// time, then narrows the transaction's request window to the OR-horizon of the
// admin grants that still confer it: one still-valid admin path is enough, and a
// non-expiring one removes the extra horizon. A Broken verdict means the
// evidence the discovery prefilter saw is gone, and an Unknown one means it
// cannot be judged; both abort rather than silently drop the row.
//
// It never consults the read or write bits: administration is independent of
// content read, and completing an admin decision with a read or write grant is
// exactly the substitution this surface exists to refuse.
func requireCurrentChannelAdminGrantLocked(
	tx *communicationTx,
	scope DirectoryScopeRef,
	channel Channel,
	grants []ChannelGrant,
	closure ChannelGrantSubjectClosure,
	dbNow time.Time,
) error {
	snapshot := ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "channel_grants_locked",
		ACLRevision: channel.ACLRevision, ObservedAt: dbNow, Grants: grants,
	}
	evidence := EvaluateCurrentChannelGrant(
		snapshot, scope.TenantID, scope.WorkspaceID, channel.ID, closure,
		ChannelGrantAdmin, dbNow,
	)
	switch evidenceVerdict(evidence.Evidence) {
	case VerdictClean:
	case VerdictBroken:
		return directNoticeReadUnknown(
			"channel administration grant is no longer current", nil,
		)
	default:
		return directNoticeReadUnknown(
			"channel administration grant evidence is unavailable", nil,
		)
	}
	horizon, constrained, err := channelGrantBitFreshUntil(
		grants, closure, ChannelGrantAdmin, dbNow,
	)
	if err != nil {
		return err
	}
	if constrained {
		if err := tx.narrowRequestAuthorityFreshUntil(horizon); err != nil {
			return err
		}
	}
	return nil
}

// channelAdministrationSubjectGrantBound is the per-SUBJECT ceiling on CURRENT
// active grants of one Channel. The writer refuses a second active generation for
// a subject (GrantChannel) and channel creation refuses a repeated subject, so
// the real number is one; the bound is generous and exists to make a malformed
// store an explicit UNKNOWN instead of an unbounded read.
//
// It is deliberately NOT the whole-Channel bound. The Channel-wide active set is
// a property of every OTHER subject too, and letting it decide this reader's
// answer is exactly the hidden total-cardinality ceiling this helper removes.
const channelAdministrationSubjectGrantBound = directNoticeReadSetPageSize

// channelAdministrationAuthorityGrantQuery is the EXACT store query the
// administrative authority closure binds for ONE closure subject on ONE Channel.
//
// ⛔ THE ORDERING IS THE CORRECTION, NOT THE FILTERS. K3-IR-02's second half was
// measured against a version of this function that supplied no Sort. The store
// then renders `ORDER BY id ASC` (sqlstore.orderClause appends the id tiebreaker
// when the caller names none), and on a populated split-owner PostgreSQL 16 the
// planner answered the whole predicate from an ORDERED PRIMARY-KEY SCAN —
// because a primary-key scan already satisfies `ORDER BY id` — examining 4,003
// rows to return ONE. Every equality term stayed a row filter. The independent
// review reproduced it with 2,000 unrelated subjects on the target Channel and
// the SAME subject holding grants on 2,000 OTHER Channels.
//
// NO INDEX FIXES THAT, and this is the part the first correction got wrong: the
// planner is not failing to find an index, it is preferring a cheaper way to
// satisfy an ordering nobody needs. What removes the substitution is asking for
// an order the primary key CANNOT serve. So this query now binds the same
// ordering the administrative WRITER already binds for the same predicate,
// `currentChannelGrantSubjectOrder` — generation DESC, id DESC — which the
// composed `sessions_channel_grant_subject_current` index serves as a range scan
// after its six-column equality prefix.
//
// The filters are ALSO shared with the writer now (`currentChannelGrantSubjectFilters`),
// so the two lanes ask the store one question with one shape. That is the whole
// reason no second index is declared here: the composition merges the writer's
// implementation instead of copying its schema.
//
// The equality prefix the engine binds, in order, is
// (tenant_id, workspace_id, channel_id, subject_kind, subject_ref, state):
// tenant comes from the generic repository, workspace from the confined scope,
// and the rest from this query. That is exactly the leading prefix of
// `sessions_channel_grant_subject_current`
// (…, state, generation, id), whose next columns are the sort terms. Both
// predicates are re-checked on every locked row regardless.
//
// ⚠ An index that CAN serve this is not proof that the engine DOES. The claim
// this function makes is only as good as the populated, tenant-pinned plans in
// TestAdministrativeAuthorityReadCostIsBoundedAmidForeignEstate, which measure
// rows examined on both engines with a present target and an absent control.
func channelAdministrationAuthorityGrantQuery(
	channelID model.ID,
	subject CommunicationSubjectRef,
) model.Query {
	return model.Query{
		Filters: append([]model.Filter{
			{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
		}, currentChannelGrantSubjectFilters(subject)...),
		Sort:  currentChannelGrantSubjectOrder(),
		Limit: channelAdministrationSubjectGrantBound + 1,
	}
}

// lockChannelAdministrationAuthorityGrants locks the current active grants of the
// CURRENT ACTOR'S OWN subject closure on one Channel, and nothing else. It is the
// administrative readers' authority set: the rows the local admin verdict and its
// OR-horizon are actually decided on.
//
// ⛔ IT EXISTS BECAUSE THE CHANNEL-WIDE HELPER MADE UNRELATED SUBJECTS A CEILING.
// lockCurrentChannelGrants reads EVERY active grant of the Channel and answers
// UNKNOWN above the shared 4,096-row bound, so one filtered first page for a
// reader with a single admin grant could be refused by 4,096 grants belonging to
// people the reader will never see. That is a total-cardinality ceiling wearing
// an authority read's clothes. The Channel-wide helper is UNCHANGED and still
// serves the read catalog, the point read and every writer; this one is the
// readers' own, in their own files.
//
// It preserves the semantics it replaces, term by term:
//
//   - the same rows the verdict consults. EvaluateCurrentChannelGrant only ever
//     matches grants whose subject is IN the closure, so restricting the read to
//     the closure cannot change any verdict it could reach. What narrows is the
//     incidental whole-Channel row validation, which was never this reader's
//     question and is not evidence it may act on.
//   - the same fence. Every grant writer holds this Channel's row lock, which the
//     caller already holds before calling here, so the selected set cannot move
//     between the store read and the lock — and a row that moved anyway is
//     UNKNOWN, not a stale verdict.
//   - the same lock ORDER: ascending row id, exactly as the Channel-wide helper.
//   - the same fail-closed posture: a malformed row, a repeated id, a row that
//     crossed its tenant, workspace, Channel or subject, a subject outside the
//     closure and a truncated page are each UNKNOWN. Nothing is skipped, dropped
//     or silently shortened.
//
// It never widens the closure: group, agent-group and session subjects are read
// exactly as the resolver produced them, and a closure that is not a current
// ALLOW with at least one valid, unrepeated subject is refused before any read.
func lockChannelAdministrationAuthorityGrants(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	channelID model.ID,
	closure ChannelGrantSubjectClosure,
) ([]ChannelGrant, error) {
	if closure.Outcome != ReadAllow || len(closure.Subjects) == 0 ||
		len(closure.Subjects) > channelAdministrationClosureBound ||
		!validCanonicalCommunicationID(channelID) {
		return nil, directNoticeReadUnknown(
			"channel administration authority closure is unusable", nil,
		)
	}
	repo, err := tx.repo(channelGrantKind)
	if err != nil {
		return nil, err
	}
	if err := refuseAppendOnlyRowLock(channelGrantKind, repo); err != nil {
		return nil, err
	}
	relevant := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	seen := make(map[model.ID]struct{}, len(closure.Subjects))
	ids := make([]model.ID, 0, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return nil, directNoticeReadUnknown(
				"channel administration authority subject is malformed", nil,
			)
		}
		if _, duplicate := relevant[subject]; duplicate {
			return nil, directNoticeReadUnknown(
				"channel administration authority subject is repeated", nil,
			)
		}
		relevant[subject] = struct{}{}
		rows, page, listErr := repo.List(
			ctx, channelAdministrationAuthorityGrantQuery(channelID, subject),
		)
		if listErr != nil {
			return nil, listErr
		}
		if page.HasMore || len(rows) > channelAdministrationSubjectGrantBound {
			return nil, directNoticeReadUnknown(
				"current ChannelGrant set for one closure subject exceeds bound", nil,
			)
		}
		for _, row := range rows {
			id, parseErr := directNoticeRecordID(row, model.ColID)
			if parseErr != nil {
				return nil, parseErr
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, directNoticeReadUnknown(
					"channel administration authority set repeats an ID", nil,
				)
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	grants := make([]ChannelGrant, 0, len(ids))
	for _, id := range ids {
		locked, lockErr := tx.lockRecord(ctx, channelGrantKind, id)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		grant, decodeErr := channelGrantFromRecord(locked)
		if decodeErr != nil {
			return nil, directNoticeReadUnknown(
				"locked ChannelGrant authority row is malformed", decodeErr,
			)
		}
		if grant.ID != id || grant.ChannelID != channelID ||
			grant.TenantID != scope.TenantID || grant.WorkspaceID != scope.WorkspaceID ||
			grant.State != ChannelGrantActive {
			return nil, directNoticeReadUnknown(
				"locked ChannelGrant authority row is no longer the row that was selected", nil,
			)
		}
		if _, member := relevant[grant.Subject]; !member {
			return nil, directNoticeReadUnknown(
				"locked ChannelGrant authority row crossed the reader's closure", nil,
			)
		}
		grants = append(grants, grant)
	}
	return grants, nil
}
