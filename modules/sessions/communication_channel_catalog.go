// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The visible-channel catalog is the K3 collection read behind
// GET /v1/m/sessions/channels. A Channel is visible to the caller only when BOTH
// hold at transaction time: the exact core Channel/read question for that
// Channel is allowed, AND the caller's current grant-subject closure holds an
// active, unexpired local grant with the read bit. Local write or admin bits,
// ownership and collection-level core read never imply visibility.
//
// Discovery is a prefilter, not authorization. It projects candidate Channel IDs
// grant-first (current read grants of the closure subjects, in Channel-ID order,
// strictly after the continuation anchor) through one parameterized store
// statement per bounded subject batch, corroborates Channel state and asks the
// exact core question per candidate. The page is then closed in ONE bound
// communication transaction that locks the fact union, every selected Channel
// and every current grant row, corroborates the directory epoch, refreshes
// database time after all blocking locks, evaluates the three grant bits
// independently and narrows the request window to the OR-horizon of every
// reported positive bit. Nothing is materialized before that transaction
// commits; the continuation is minted afterwards from the last Channel actually
// returned.
const (
	channelCatalogDefaultLimit = directNoticeInboxDefaultLimit
	channelCatalogMaximumLimit = directNoticeInboxMaximumLimit
	// channelCatalogSubjectBatch bounds the ORed subject alternatives per
	// projection statement (store.DistinctProjectionMaxAlternatives is the hard
	// ceiling); a larger closure runs several statements whose ordered results are
	// merged below.
	channelCatalogSubjectBatch = 128
	// channelCatalogDiscoveryBatch is the projected candidate page per discovery
	// round. Work scales with returned/provisionally visible Channels and closure
	// batches; hidden unrelated Channels are never read.
	channelCatalogDiscoveryBatch = 128
	// channelCatalogClosureBound is the existing closure cardinality ceiling.
	channelCatalogClosureBound = directNoticeReadSetBound
)

// ChannelCatalogRequest is the public catalog navigation contract. The
// continuation is a c3n1 token anchored to the last returned Channel; raw
// Channel positions and store cursors are never accepted.
type ChannelCatalogRequest struct {
	Continuation string `json:"continuation,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

// ChannelCatalogAccess reports the caller's three independent current LOCAL
// grant bits on a visible Channel. It says nothing about the core permission a
// later write or admin route will also require.
type ChannelCatalogAccess struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
	Admin bool `json:"admin"`
}

// ChannelCatalogItem is one visible Channel with the caller's own access bits.
type ChannelCatalogItem struct {
	Channel
	MyAccess ChannelCatalogAccess `json:"my_access"`
}

// ChannelCatalogPage reports only visible Channels. has_more is true exactly
// when the final bound transaction authorized a visible limit+1th Channel, and
// continuation is present only in that case. There is no total and no
// hidden-row side channel.
type ChannelCatalogPage struct {
	Items        api.JSONArray[ChannelCatalogItem] `json:"items"`
	Continuation string                            `json:"continuation,omitempty"`
	HasMore      bool                              `json:"has_more"`
}

// requireChannelCatalogPrincipal admits the directory principals the catalog
// serves: users and exchanged agents (and any other valid, non-system K3
// principal). A communication-session credential lacks the core channel:read
// ceiling and is refused by the outer PEP before reaching here.
func requireChannelCatalogPrincipal(principal CommunicationPrincipal) error {
	if ValidateCommunicationPrincipal(principal) != nil || principal.System {
		return communicationError(
			ErrCommunicationForbidden,
			"channel catalog requires a directory principal credential",
		)
	}
	return nil
}

type channelCatalogCandidate struct {
	channelID model.ID
	question  communicationAuthorityQuestion
}

type channelCatalogProjectionPage struct {
	candidates []model.ID
	hasMore    bool
	observedAt time.Time
}

// ListVisibleChannels is the handler-facing catalog read, gated by the complete
// K3 readiness conjunction after the current credential is rebound.
func (m *Module) ListVisibleChannels(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelCatalogRequest,
) (ChannelCatalogPage, error) {
	return m.listVisibleChannels(ctx, scope, ref, request, true)
}

// listVisibleChannelsWithAuthority is the exact private seam used by the
// focused module tests while the aggregate readiness conjunction is OFF, in the
// same shape as listDirectNoticeInboxWithAuthority. The credential is still
// rebound first; only the readiness term is skipped.
func (m *Module) listVisibleChannelsWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelCatalogRequest,
) (ChannelCatalogPage, error) {
	return m.listVisibleChannels(ctx, scope, ref, request, false)
}

func (m *Module) listVisibleChannels(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelCatalogRequest,
	requireReadiness bool,
) (ChannelCatalogPage, error) {
	if request.Limit < 0 || request.Limit > channelCatalogMaximumLimit ||
		len(request.Continuation) > communicationCursorTokenMaxBytes {
		return ChannelCatalogPage{}, communicationError(
			ErrInvalidCommunicationModel, "invalid channel catalog navigation",
		)
	}
	limit := request.Limit
	if limit == 0 {
		limit = channelCatalogDefaultLimit
	}
	identity, err := m.bindCurrentCommunicationIdentity(
		ctx, scope, ref, requireChannelCatalogPrincipal,
	)
	if err != nil {
		return ChannelCatalogPage{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return ChannelCatalogPage{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	if !m.CommunicationCursorTokenKeyringBound() {
		return ChannelCatalogPage{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel catalog navigation keyring is unavailable",
		)
	}
	reader, err := m.preflightChannelCatalogReader(ctx, identity)
	if err != nil {
		if errors.Is(err, errDirectNoticePrincipalNotFound) {
			return ChannelCatalogPage{}, nil
		}
		return ChannelCatalogPage{}, err
	}
	if reader.Closure.Outcome == ReadDeny {
		// A valid, current DENY closure: the principal is not a current member of
		// anything in this workspace. Nothing is visible; nothing is disclosed.
		return ChannelCatalogPage{}, nil
	}

	var anchor model.ID
	if request.Continuation != "" {
		observedAt, err := m.observeChannelCatalogDatabaseTime(ctx, scope)
		if err != nil {
			return ChannelCatalogPage{}, err
		}
		claims, err := m.communicationCursorTokenKeyring().verifyChannelCatalogNavigation(
			request.Continuation, observedAt,
		)
		if err != nil {
			return ChannelCatalogPage{}, err
		}
		if claims.tenantID != scope.TenantID || claims.workspaceID != scope.WorkspaceID ||
			claims.reader != reader.Recipient {
			return ChannelCatalogPage{}, communicationCursorTokenInvalid(
				"catalog navigation token crossed its authenticated reader",
			)
		}
		anchor = claims.lastChannelID
	}

	provisional, err := m.discoverChannelCatalogCandidates(
		ctx, identity, reader, anchor, limit+1,
	)
	if err != nil {
		return ChannelCatalogPage{}, err
	}
	if len(provisional) == 0 {
		return ChannelCatalogPage{}, nil
	}
	items, observedAt, err := m.closeChannelCatalogPage(ctx, identity, provisional)
	if err != nil {
		return ChannelCatalogPage{}, err
	}
	page := ChannelCatalogPage{Items: api.JSONArray[ChannelCatalogItem]{}}
	if len(items) > limit {
		page.HasMore = true
		items = items[:limit]
	}
	page.Items = append(page.Items, items...)
	if page.HasMore {
		last := items[len(items)-1]
		filter := channelCatalogFilterHash()
		token, err := m.communicationCursorTokenKeyring().mintChannelCatalogNavigation(
			communicationChannelCatalogNavigationClaims{
				tenantID: scope.TenantID, workspaceID: scope.WorkspaceID,
				reader: reader.Recipient, filterHash: filter[:], lastChannelID: last.ID,
			},
			observedAt,
		)
		if err != nil {
			return ChannelCatalogPage{}, err
		}
		page.Continuation = token
	}
	return page, nil
}

// preflightChannelCatalogReader resolves the rebound principal once, then
// validates and deduplicates its grant-subject closure under the existing
// cardinality bound. ReadUnknown is unavailable; a DENY closure is returned to
// the caller, which answers an empty page.
func (m *Module) preflightChannelCatalogReader(
	ctx context.Context,
	identity communicationIdentityBinding,
) (directNoticeReaderIdentityPreflight, error) {
	reader, err := m.preflightDirectNoticeReaderIdentity(
		ctx, identity.scope, identity.principal, nil,
	)
	if err != nil {
		return directNoticeReaderIdentityPreflight{}, err
	}
	if err := validateChannelCatalogClosure(reader.Closure); err != nil {
		return directNoticeReaderIdentityPreflight{}, err
	}
	return reader, nil
}

func validateChannelCatalogClosure(closure ChannelGrantSubjectClosure) error {
	if closure.Outcome == ReadUnknown || !closure.Outcome.Valid() {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "channel catalog ChannelGrant closure is unavailable",
		)
	}
	if len(closure.Subjects) > channelCatalogClosureBound {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "channel catalog ChannelGrant closure exceeds bound",
		)
	}
	seen := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "channel catalog closure subject is malformed",
			)
		}
		if _, duplicate := seen[subject]; duplicate {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "channel catalog closure subject is repeated",
			)
		}
		seen[subject] = struct{}{}
	}
	if closure.Outcome == ReadAllow && len(closure.Subjects) == 0 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "channel catalog closure allows without subjects",
		)
	}
	return nil
}

// observeChannelCatalogDatabaseTime samples database time through the confined
// View so a continuation's lifetime is judged by the engine clock, never by the
// process clock.
func (m *Module) observeChannelCatalogDatabaseTime(
	ctx context.Context,
	scope DirectoryScopeRef,
) (time.Time, error) {
	var observedAt time.Time
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return communicationTransactionUnavailable("transaction clock", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return communicationTransactionUnavailable("transaction clock", err)
		}
		observedAt = now.Time()
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	if observedAt.IsZero() {
		return time.Time{}, communicationTransactionUnavailable("transaction clock returned zero", nil)
	}
	return observedAt, nil
}

// discoverChannelCatalogCandidates runs discovery rounds until `want`
// provisionally visible candidates exist or the grant projection is exhausted.
// Every round projects grant-first candidates after the current anchor, keeps
// the active Channels among them and asks the exact core Channel/read question
// for each; core DENY is concealed, core UNKNOWN aborts. There is no candidate
// cutoff: a run of hidden Channels only costs bounded projection rounds.
func (m *Module) discoverChannelCatalogCandidates(
	ctx context.Context,
	identity communicationIdentityBinding,
	reader directNoticeReaderIdentityPreflight,
	anchor model.ID,
	want int,
) ([]channelCatalogCandidate, error) {
	if want < 1 || want > directNoticeInboxCandidateBound {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "channel catalog discovery size is invalid",
		)
	}
	provisional := make([]channelCatalogCandidate, 0, want)
	after := anchor
	for len(provisional) < want {
		page, err := m.projectChannelCatalogCandidates(
			ctx, identity.scope, reader.Closure.Subjects, after, channelCatalogDiscoveryBatch,
		)
		if err != nil {
			return nil, err
		}
		if len(page.candidates) == 0 {
			if page.hasMore {
				return nil, communicationError(
					ErrCommunicationEvidenceUnknown,
					"channel catalog projection was truncated without a continuation",
				)
			}
			break
		}
		if err := m.requireDirectNoticeIdentityCurrent(reader); err != nil {
			return nil, err
		}
		active, err := m.corroborateChannelCatalogCandidates(ctx, identity.scope, page.candidates)
		if err != nil {
			return nil, err
		}
		if len(active) > 0 {
			questions := make([]communicationAuthorityQuestion, 0, len(active))
			for _, channelID := range active {
				question, err := newCommunicationAuthorityQuestion(
					identity.scope, channelKind, channelID, CommunicationRead,
				)
				if err != nil {
					return nil, err
				}
				questions = append(questions, question)
			}
			_, admitted, err := bindCommunicationAuthorityBatch(
				ctx, identity, requireChannelCatalogPrincipal, questions,
			)
			if err != nil {
				return nil, err
			}
			for _, index := range admitted {
				provisional = append(provisional, channelCatalogCandidate{
					channelID: active[index], question: questions[index],
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

// requireDirectNoticeIdentityCurrent refuses to keep discovering on an expired
// principal resolution or closure; the final closure re-resolves both anyway.
func (m *Module) requireDirectNoticeIdentityCurrent(
	reader directNoticeReaderIdentityPreflight,
) error {
	observedAt := m.clock.Now().Time()
	if !communicationEvidenceCurrent(
		reader.Resolution.ObservedAt, reader.Resolution.FreshUntil, observedAt,
	) || !communicationEvidenceCurrent(
		reader.Closure.ObservedAt, reader.Closure.FreshUntil, observedAt,
	) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel catalog reader authority expired during discovery",
		)
	}
	return nil
}

// projectChannelCatalogCandidates is the grant-first candidate projection. It
// runs on the confined View that observes database time, so the expiry
// predicate and the observation share one engine clock. Each subject batch is
// one store.DistinctProjector statement over sessions_channel_grant (state
// active, read bit, unset-or-after expiry, Channel ID strictly after the anchor,
// ascending) in which the engine renders every closure subject as its own
// bounded index-range arm and unions the arms, so the rows examined scale with
// the closure and the page, never with the grants other subjects hold in the
// workspace; the ordered results of several batches are merged so the returned
// page is the same ordered distinct prefix one statement would have produced.
func (m *Module) projectChannelCatalogCandidates(
	ctx context.Context,
	scope DirectoryScopeRef,
	subjects []CommunicationSubjectRef,
	after model.ID,
	limit int,
) (channelCatalogProjectionPage, error) {
	if limit < 1 || limit > store.DistinctProjectionMaxLimit || len(subjects) == 0 ||
		len(subjects) > channelCatalogClosureBound {
		return channelCatalogProjectionPage{}, communicationError(
			ErrInvalidCommunicationModel, "channel catalog projection request is invalid",
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
		for start := 0; start < len(subjects); start += channelCatalogSubjectBatch {
			end := start + channelCatalogSubjectBatch
			if end > len(subjects) {
				end = len(subjects)
			}
			projection := store.DistinctProjection{
				Column: colCommChannelID, Limit: limit,
				Filters: []model.Filter{
					{Column: colCommState, Op: model.OpEq, Value: string(ChannelGrantActive)},
					{Column: colCommCanRead, Op: model.OpEq, Value: true},
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

// mergeChannelCatalogProjection folds the ordered pages of several subject
// batches into one ordered distinct prefix. When a batch had more values than
// it returned, only values up to that batch's last value are complete across
// all batches (a later value from another batch could be preceded by an unseen
// value of the truncated batch), so the merged page is cut there and hasMore
// reports the remainder. The next round resumes strictly after the last
// returned candidate, which re-finds every trimmed value.
func mergeChannelCatalogProjection(
	pages []store.DistinctPage,
	after model.ID,
	limit int,
) ([]model.ID, bool, error) {
	set := make(map[model.ID]struct{})
	var cut model.ID
	truncated := false
	for _, page := range pages {
		for _, raw := range page.Values {
			id, err := model.ParseID(raw)
			if err != nil || !validCanonicalCommunicationID(id) || id.String() != raw ||
				(!after.IsZero() && id.String() <= after.String()) {
				return nil, false, communicationError(
					ErrCommunicationEvidenceUnknown, "channel catalog projection row is malformed",
				)
			}
			set[id] = struct{}{}
		}
		if page.HasMore && len(page.Values) > 0 {
			last := model.ID(page.Values[len(page.Values)-1])
			if !truncated || last.String() < cut.String() {
				cut = last
			}
			truncated = true
		}
	}
	ids := make([]model.ID, 0, len(set))
	for id := range set {
		if truncated && id.String() > cut.String() {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	hasMore := truncated
	if len(ids) > limit {
		ids = ids[:limit]
		hasMore = true
	}
	return ids, hasMore, nil
}

// corroborateChannelCatalogCandidates keeps the projected candidates whose
// Channel row exists in the confined workspace and is active. Work scales with
// projected (grant-visible) candidates only; a Channel without a current read
// grant for the caller is never read here.
func (m *Module) corroborateChannelCatalogCandidates(
	ctx context.Context,
	scope DirectoryScopeRef,
	candidates []model.ID,
) ([]model.ID, error) {
	active := make([]model.ID, 0, len(candidates))
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
					ErrCommunicationEvidenceUnknown, "channel catalog candidate row is malformed",
				)
			}
			if channel.ID != channelID || channel.TenantID != scope.TenantID ||
				channel.WorkspaceID != scope.WorkspaceID || channel.State != ChannelActive {
				continue
			}
			active = append(active, channelID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return active, nil
}

// closeChannelCatalogPage freshly binds exact core evidence for every selected
// candidate (page plus lookahead), freshly resolves the reader identity and
// closure, and closes the whole set in one bound transaction. Any Channel whose
// read bit is not Clean at that point aborts the page as evidence unavailable:
// a selected item is never silently dropped, because dropping it would let a
// later Channel take its place unseen.
func (m *Module) closeChannelCatalogPage(
	ctx context.Context,
	identity communicationIdentityBinding,
	selected []channelCatalogCandidate,
) ([]ChannelCatalogItem, time.Time, error) {
	if len(selected) == 0 || len(selected) > directNoticeInboxCandidateBound {
		return nil, time.Time{}, communicationError(
			ErrInvalidCommunicationModel, "channel catalog page closure is invalid",
		)
	}
	reader, err := m.preflightChannelCatalogReader(ctx, identity)
	if err != nil {
		return nil, time.Time{}, err
	}
	if reader.Closure.Outcome != ReadAllow {
		return nil, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel catalog closure changed before closure",
		)
	}
	window, err := directNoticeReaderAuthorityWindow(reader)
	if err != nil {
		return nil, time.Time{}, err
	}
	questions := make([]communicationAuthorityQuestion, len(selected))
	for index, candidate := range selected {
		if candidate.question.entity.Kind != channelKind ||
			candidate.question.operation != CommunicationRead ||
			candidate.question.entity.ID != candidate.channelID ||
			(index > 0 && candidate.channelID.String() <= selected[index-1].channelID.String()) {
			return nil, time.Time{}, communicationError(
				ErrCommunicationEvidenceUnknown, "channel catalog candidate crossed its question",
			)
		}
		questions[index] = candidate.question
	}
	batch, admitted, err := bindCommunicationAuthorityBatch(
		ctx, identity, requireChannelCatalogPrincipal, questions,
	)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(admitted) != len(questions) {
		return nil, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel catalog core authority changed before page closure",
		)
	}

	var items []ChannelCatalogItem
	var observedAt time.Time
	err = m.mutateCommunicationWithBoundAuthorityBatch(
		ctx, identity.scope, identity, requireChannelCatalogPrincipal,
		questions, batch, window,
		func(tx *communicationTx, contexts []communicationRequestAuthorityBatchContext) error {
			if len(contexts) != len(questions) {
				return directNoticeReadUnknown("channel catalog authority batch changed size", nil)
			}
			allFacts := make([]store.AuthorizationFactRef, 0)
			for index, bound := range contexts {
				if bound.question != questions[index] || bound.principal != identity.principal {
					return directNoticeReadUnknown("channel catalog authority crossed its candidate", nil)
				}
				preflight, err := directNoticeReaderPreflightWithCore(reader, bound.witness)
				if err != nil {
					return err
				}
				allFacts = append(allFacts, preflight.Facts...)
			}
			facts, err := canonicalAuthorizationFactUnion(allFacts)
			if err != nil {
				return directNoticeReadUnknown("channel catalog authority facts are unavailable", err)
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
					return directNoticeReadUnknown("locked catalog Channel is malformed", err)
				}
				if channel.ID != candidate.channelID || channel.TenantID != scope.TenantID ||
					channel.WorkspaceID != scope.WorkspaceID || channel.State != ChannelActive ||
					channel.ACLRevision < 1 {
					return directNoticeReadUnknown("locked catalog Channel is no longer current", nil)
				}
				channels = append(channels, channel)
			}
			grants := make([][]ChannelGrant, len(channels))
			for index, channel := range channels {
				locked, err := lockCurrentChannelGrants(ctx, tx, channel.ID)
				if err != nil {
					return err
				}
				grants[index] = locked
			}
			epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
			if err != nil || epoch.Validate() != nil || epoch.TenantID != scope.TenantID ||
				epoch.Version != reader.Resolution.Recipient.DirectoryEpoch ||
				epoch.Version != reader.Closure.DirectoryEpoch {
				return directNoticeReadUnknown("locked catalog directory epoch is unavailable", err)
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			observedAt = dbNow
			built := make([]ChannelCatalogItem, 0, len(channels))
			for index, channel := range channels {
				access, err := evaluateChannelCatalogAccessLocked(
					tx, scope, channel, grants[index], reader.Closure, dbNow,
				)
				if err != nil {
					return err
				}
				built = append(built, ChannelCatalogItem{Channel: channel, MyAccess: access})
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

// evaluateChannelCatalogAccessLocked evaluates the read, write and admin bits
// independently against the same locked grant set and fresh closure. Read must
// be Clean or the page aborts; any Unknown bit aborts; Broken write/admin is
// simply false. Every positive bit narrows the transaction's request window to
// that bit's OR-horizon; the earliest constrained horizon across the batch wins,
// and a non-expiring matching grant removes the extra horizon for its bit only.
func evaluateChannelCatalogAccessLocked(
	tx *communicationTx,
	scope DirectoryScopeRef,
	channel Channel,
	grants []ChannelGrant,
	closure ChannelGrantSubjectClosure,
	dbNow time.Time,
) (ChannelCatalogAccess, error) {
	snapshot := ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "channel_grants_locked",
		ACLRevision: channel.ACLRevision, ObservedAt: dbNow, Grants: grants,
	}
	var access ChannelCatalogAccess
	for _, bit := range []ChannelGrantBit{ChannelGrantRead, ChannelGrantWrite, ChannelGrantAdmin} {
		evidence := EvaluateCurrentChannelGrant(
			snapshot, scope.TenantID, scope.WorkspaceID, channel.ID, closure, bit, dbNow,
		)
		verdict := evidenceVerdict(evidence.Evidence)
		switch verdict {
		case VerdictClean:
		case VerdictBroken:
			if bit == ChannelGrantRead {
				return ChannelCatalogAccess{}, directNoticeReadUnknown(
					"channel catalog read grant is no longer current", nil,
				)
			}
			continue
		default:
			return ChannelCatalogAccess{}, directNoticeReadUnknown(
				"channel catalog grant evidence is unavailable", nil,
			)
		}
		horizon, constrained, err := channelGrantBitFreshUntil(grants, closure, bit, dbNow)
		if err != nil {
			return ChannelCatalogAccess{}, err
		}
		if constrained {
			if err := tx.narrowRequestAuthorityFreshUntil(horizon); err != nil {
				return ChannelCatalogAccess{}, err
			}
		}
		switch bit {
		case ChannelGrantRead:
			access.Read = true
		case ChannelGrantWrite:
			access.Write = true
		case ChannelGrantAdmin:
			access.Admin = true
		}
	}
	if !access.Read {
		return ChannelCatalogAccess{}, directNoticeReadUnknown(
			"channel catalog read grant is no longer current", nil,
		)
	}
	return access, nil
}
