// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The administrative Channel sheet is the K3 point read behind
// GET /v1/m/sessions/channels/{id}/grants. It answers, for ONE Channel the
// caller may currently administer, the Channel's stored configuration, the
// strong ETag that the existing mutations take as their If-Match precondition,
// and a KEYSET page of the grant generations the requested filters select.
//
// Both doors are required and independent: the exact core
// Channel/ChannelAdmin decision AND a current local grant carrying the ADMIN
// bit. Local read is neither required nor granted — the sheet never opens a
// message payload, a delivery or an inbox — and the route conceals a denial as
// 404 so it cannot become a Channel-enumeration oracle.
//
// The page is bounded by the Channel and the filters in the STORE: it never
// loads the whole history to paginate it in memory, which is what makes a
// Channel with more generations than the writer's own historical ceiling
// enumerable here. The declared sessions_channel_grant_history and
// sessions_channel_grant_subject_history indexes carry exactly the columns these
// two shapes bind.
//
// The read is PURE in the domain: it materializes no expiry, writes no grant or
// revision, appends no mutation audit, mints no idempotency receipt, advances no
// cursor and opens no sealed payload. It does take short locks — the Channel row
// and the rows it reports — because the answer must be one coherent snapshot.
const (
	channelGrantAdministrationDefaultLimit = channelCatalogDefaultLimit
	channelGrantAdministrationMaximumLimit = channelCatalogMaximumLimit
)

// ErrCommunicationChannelSnapshotChanged reports that the Channel's version or
// ACL revision moved between two pages of one administrative sheet. It is a
// distinct wire meaning (409 channel_snapshot_changed) because the remedy is
// specific: discard every page collected so far and restart from the first one.
// It is deliberately NOT store.ErrConflict, so it cannot be confused with the
// Channel version mismatch the existing mutations already return as 409.
var ErrCommunicationChannelSnapshotChanged = errors.New(
	"sessions: channel administration snapshot changed",
)

// ChannelGrantAdministrationStateFilter selects grant generations by their
// PERSISTED state. It is not a temporal predicate: an `active` row whose TTL has
// already passed is still persisted active and still selected by `active`,
// because it is exactly the row the next mutation has to reckon with.
type ChannelGrantAdministrationStateFilter string

const (
	ChannelGrantAdministrationStateActive  ChannelGrantAdministrationStateFilter = "active"
	ChannelGrantAdministrationStateRevoked ChannelGrantAdministrationStateFilter = "revoked"
	ChannelGrantAdministrationStateExpired ChannelGrantAdministrationStateFilter = "expired"
	ChannelGrantAdministrationStateAll     ChannelGrantAdministrationStateFilter = "all"
)

// channelGrantAdministrationStateFilters is the CLOSED set. Anything outside it
// is a 400.
func channelGrantAdministrationStateFilters() []ChannelGrantAdministrationStateFilter {
	return []ChannelGrantAdministrationStateFilter{
		ChannelGrantAdministrationStateActive,
		ChannelGrantAdministrationStateRevoked,
		ChannelGrantAdministrationStateExpired,
		ChannelGrantAdministrationStateAll,
	}
}

func (f ChannelGrantAdministrationStateFilter) Valid() bool {
	switch f {
	case ChannelGrantAdministrationStateActive, ChannelGrantAdministrationStateRevoked,
		ChannelGrantAdministrationStateExpired, ChannelGrantAdministrationStateAll:
		return true
	default:
		return false
	}
}

// persistedState maps the filter onto the stored column value; `all` binds no
// state predicate at all.
func (f ChannelGrantAdministrationStateFilter) persistedState() (ChannelGrantState, bool) {
	switch f {
	case ChannelGrantAdministrationStateActive:
		return ChannelGrantActive, true
	case ChannelGrantAdministrationStateRevoked:
		return ChannelGrantRevoked, true
	case ChannelGrantAdministrationStateExpired:
		return ChannelGrantExpired, true
	default:
		return "", false
	}
}

// ChannelGrantTemporalState describes whether ONE reported row is in force at
// the page's observation instant. It is a property of the ROW, never a statement
// that its subject can act: membership, credential, forbids, core permission and
// the Channel's own state are separate current questions the UI must not fold
// into this value.
type ChannelGrantTemporalState string

const (
	ChannelGrantTemporalActive  ChannelGrantTemporalState = "active"
	ChannelGrantTemporalRevoked ChannelGrantTemporalState = "revoked"
	ChannelGrantTemporalExpired ChannelGrantTemporalState = "expired"
)

// ChannelGrantAdministrationRequest is the public sheet contract. The
// continuation is a c3g1 token anchored to the last returned grant and bound to
// the Channel revision the page observed; raw grant positions, store cursors and
// client-supplied anchors are never accepted.
type ChannelGrantAdministrationRequest struct {
	ChannelID    model.ID                              `json:"-"`
	State        ChannelGrantAdministrationStateFilter `json:"state,omitempty"`
	Subject      CommunicationSubjectRef               `json:"subject,omitempty"`
	HasSubject   bool                                  `json:"-"`
	Continuation string                                `json:"continuation,omitempty"`
	Limit        int                                   `json:"limit,omitempty"`
}

// ChannelGrantAdministrationItem is one stored grant generation and its temporal
// state at observed_at. The stored `grant.state` is preserved untouched: the
// derived value is reported beside it, never in place of it.
type ChannelGrantAdministrationItem struct {
	Grant         ChannelGrant              `json:"grant"`
	TemporalState ChannelGrantTemporalState `json:"temporal_state"`
}

// ChannelGrantAdministrationPage is the Channel, its precondition ETag, the
// database instant the page was closed at, and the selected generations.
//
// `etag` is the CHANNEL's strong validator and the precondition for the existing
// mutations. It is not a validator of this representation: filters, pagination
// and observed_at change the body without changing Channel.version, so the page
// carries no HTTP ETag and no 304.
type ChannelGrantAdministrationPage struct {
	Channel      Channel                                       `json:"channel"`
	ETag         string                                        `json:"etag"`
	ObservedAt   time.Time                                     `json:"observed_at"`
	Items        api.JSONArray[ChannelGrantAdministrationItem] `json:"items"`
	Continuation string                                        `json:"continuation,omitempty"`
	HasMore      bool                                          `json:"has_more"`
}

// ListChannelGrantAdministration is the handler-facing administrative sheet
// read, gated by the complete K3 readiness conjunction.
func (m *Module) ListChannelGrantAdministration(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelGrantAdministrationRequest,
) (ChannelGrantAdministrationPage, error) {
	return m.listChannelGrantAdministration(ctx, scope, ref, request, true)
}

// listChannelGrantAdministrationWithAuthority is the exact private seam the
// focused module tests use while the aggregate readiness conjunction is OFF.
func (m *Module) listChannelGrantAdministrationWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelGrantAdministrationRequest,
) (ChannelGrantAdministrationPage, error) {
	return m.listChannelGrantAdministration(ctx, scope, ref, request, false)
}

func (m *Module) listChannelGrantAdministration(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request ChannelGrantAdministrationRequest,
	requireReadiness bool,
) (ChannelGrantAdministrationPage, error) {
	if !validCanonicalCommunicationID(request.ChannelID) {
		return ChannelGrantAdministrationPage{}, directNoticeReadNotFound("Channel is not visible")
	}
	state := request.State
	if state == "" {
		state = ChannelGrantAdministrationStateActive
	}
	if !state.Valid() || request.Limit < 0 || request.Limit > channelGrantAdministrationMaximumLimit ||
		len(request.Continuation) > communicationCursorTokenMaxBytes {
		return ChannelGrantAdministrationPage{}, communicationError(
			ErrInvalidCommunicationModel, "invalid channel grant administration navigation",
		)
	}
	subject := CommunicationSubjectRef{}
	if request.HasSubject {
		if request.Subject.Validate() != nil {
			return ChannelGrantAdministrationPage{}, communicationError(
				ErrInvalidCommunicationModel, "channel grant administration subject is invalid",
			)
		}
		subject = request.Subject
	} else if request.Subject != (CommunicationSubjectRef{}) {
		return ChannelGrantAdministrationPage{}, communicationError(
			ErrInvalidCommunicationModel, "channel grant administration subject is incomplete",
		)
	}
	limit := request.Limit
	if limit == 0 {
		limit = channelGrantAdministrationDefaultLimit
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, request.ChannelID, CommunicationChannelAdmin,
	)
	if err != nil {
		return ChannelGrantAdministrationPage{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return ChannelGrantAdministrationPage{}, normalizeDirectNoticePointReadError(err)
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return ChannelGrantAdministrationPage{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel administration authority context crossed its exact request",
		)
	}
	if err := requireChannelAdministrationPrincipal(inspected.principal); err != nil {
		return ChannelGrantAdministrationPage{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return ChannelGrantAdministrationPage{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	if !m.CommunicationCursorTokenKeyringBound() {
		return ChannelGrantAdministrationPage{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel administration navigation keyring is unavailable",
		)
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return ChannelGrantAdministrationPage{}, normalizeDirectNoticePointReadError(err)
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return ChannelGrantAdministrationPage{}, err
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(identity.Principal),
	)
	if err != nil {
		return ChannelGrantAdministrationPage{}, err
	}

	filter := channelGrantAdministrationFilterHash(state, subject)
	var anchor model.ID
	var expected communicationChannelGrantAdministrationNavigationClaims
	resumed := false
	if request.Continuation != "" {
		observedAt, err := m.observeChannelCatalogDatabaseTime(ctx, scope)
		if err != nil {
			return ChannelGrantAdministrationPage{}, err
		}
		verified, err := m.communicationCursorTokenKeyring().verifyChannelGrantAdministrationNavigation(
			request.Continuation, observedAt,
		)
		if err != nil {
			return ChannelGrantAdministrationPage{}, err
		}
		if verified.tenantID != scope.TenantID || verified.workspaceID != scope.WorkspaceID ||
			verified.reader != identity.Recipient || verified.channelID != request.ChannelID ||
			!bytes.Equal(verified.filterHash, filter[:]) {
			return ChannelGrantAdministrationPage{}, communicationCursorTokenInvalid(
				"grant navigation token crossed its authenticated reader, Channel or selection",
			)
		}
		anchor = verified.lastGrantID
		expected = verified
		resumed = true
	}

	var page ChannelGrantAdministrationPage
	var hidden, snapshotChanged bool
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
			if err := tx.lockTransaction(ctx, fmt.Sprintf(
				"sessions_channel_admin_read|%s|%s|%s",
				scope.TenantID, scope.WorkspaceID, request.ChannelID,
			)); err != nil {
				return err
			}
			record, err := tx.lockRecord(ctx, channelKind, request.ChannelID)
			if errors.Is(err, store.ErrNotFound) {
				hidden = true
				return tx.refreshNow(ctx)
			}
			if err != nil {
				return err
			}
			channel, err := channelFromRecord(record)
			if err != nil || channel.ID != request.ChannelID ||
				channel.TenantID != scope.TenantID || channel.WorkspaceID != scope.WorkspaceID ||
				channel.Version < 1 || channel.ACLRevision < 1 {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "locked administrable Channel is malformed",
				)
			}
			// The authority set and the reported page are two different reads, and
			// BOTH are bounded by what this request is about. The first is the
			// reader's OWN closure grants — the rows the local admin verdict is
			// actually decided on — so a Channel crowded with other subjects'
			// grants cannot deny this reader a page. The second is the keyset page
			// of the generations the filters select.
			current, err := lockChannelAdministrationAuthorityGrants(
				ctx, tx, scope, request.ChannelID, preflight.Closure,
			)
			if err != nil {
				return err
			}
			rows, err := lockChannelGrantAdministrationPage(
				ctx, tx, scope, request.ChannelID, state, subject, request.HasSubject, anchor, limit+1,
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
			// Database time is refreshed AFTER every blocking lock, so the admin
			// decision, the horizon and every temporal_state are judged at the
			// instant the snapshot actually closed.
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			admin := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
				Verdict: VerdictClean, Code: "channel_grants_locked",
				ACLRevision: channel.ACLRevision, ObservedAt: dbNow, Grants: current,
			}, scope.TenantID, scope.WorkspaceID, request.ChannelID, preflight.Closure,
				ChannelGrantAdmin, dbNow)
			switch evidenceVerdict(admin.Evidence) {
			case VerdictClean:
				horizon, constrained, horizonErr := channelGrantBitFreshUntil(
					current, preflight.Closure, ChannelGrantAdmin, dbNow,
				)
				if horizonErr != nil {
					return horizonErr
				}
				if constrained {
					if err := tx.narrowRequestAuthorityFreshUntil(horizon); err != nil {
						return err
					}
				}
			case VerdictBroken:
				hidden = true
				return nil
			default:
				return communicationError(
					ErrCommunicationEvidenceUnknown, "channel administration grant is unavailable",
				)
			}
			// Revision coherence is checked only AFTER current authority: a caller
			// who has lost the right to inspect this Channel is told 404, never the
			// fact that its ACL moved.
			if resumed && (channel.Version != expected.channelVersion ||
				channel.ACLRevision != expected.aclRevision) {
				snapshotChanged = true
				return nil
			}
			items := make([]ChannelGrantAdministrationItem, 0, len(rows))
			for _, grant := range rows {
				items = append(items, ChannelGrantAdministrationItem{
					Grant: grant, TemporalState: channelGrantTemporalState(grant, dbNow),
				})
			}
			page = ChannelGrantAdministrationPage{
				Channel: channel, ETag: communicationVersionETag(channel.Version),
				ObservedAt: dbNow.UTC(),
				Items:      api.JSONArray[ChannelGrantAdministrationItem]{},
			}
			if len(items) > limit {
				page.HasMore = true
				items = items[:limit]
			}
			page.Items = append(page.Items, items...)
			return nil
		},
	)
	if err != nil {
		return ChannelGrantAdministrationPage{}, normalizeDirectNoticePointReadError(err)
	}
	if hidden {
		return ChannelGrantAdministrationPage{}, directNoticeReadNotFound("Channel is not visible")
	}
	if snapshotChanged {
		return ChannelGrantAdministrationPage{}, communicationError(
			ErrCommunicationChannelSnapshotChanged,
			"channel revision changed between administration pages",
		)
	}
	if page.HasMore {
		last := page.Items[len(page.Items)-1].Grant
		token, err := m.communicationCursorTokenKeyring().mintChannelGrantAdministrationNavigation(
			communicationChannelGrantAdministrationNavigationClaims{
				tenantID: scope.TenantID, workspaceID: scope.WorkspaceID,
				reader: identity.Recipient, channelID: page.Channel.ID,
				channelVersion: page.Channel.Version, aclRevision: page.Channel.ACLRevision,
				filterHash: filter[:], lastGrantID: last.ID,
			},
			page.ObservedAt,
		)
		if err != nil {
			return ChannelGrantAdministrationPage{}, err
		}
		page.Continuation = token
	}
	return page, nil
}

// channelGrantTemporalState derives whether one stored row is in force at the
// observation instant. A revoked or expired row stays what it is; an active row
// whose expiry has passed reads `expired` WITHOUT the stored state being
// touched — this GET never materializes that transition.
func channelGrantTemporalState(grant ChannelGrant, dbNow time.Time) ChannelGrantTemporalState {
	switch grant.State {
	case ChannelGrantRevoked:
		return ChannelGrantTemporalRevoked
	case ChannelGrantExpired:
		return ChannelGrantTemporalExpired
	}
	if grant.ExpiresAt != nil && !dbNow.Before(*grant.ExpiresAt) {
		return ChannelGrantTemporalExpired
	}
	return ChannelGrantTemporalActive
}

// lockChannelGrantAdministrationPage reads ONE keyset page of grant generations
// bounded in the store by Channel, the persisted-state selection, the optional
// exact subject and the anchor, then locks exactly the rows it will report. The
// whole history is never loaded: `want` rows are requested and `want` rows are
// locked, so a Channel with more generations than the writer's historical
// ceiling is still enumerable page by page.
//
// Every locked row is re-corroborated against the tenant, the workspace, the
// Channel and the filters AFTER the lock, and the ordering is checked to be
// strictly ascending past the anchor: a duplicate, a crossed row, a
// non-canonical ID or a broken order is UNKNOWN, never a silently shortened page.
func lockChannelGrantAdministrationPage(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	channelID model.ID,
	state ChannelGrantAdministrationStateFilter,
	subject CommunicationSubjectRef,
	hasSubject bool,
	anchor model.ID,
	want int,
) ([]ChannelGrant, error) {
	if want < 1 || want > channelGrantAdministrationMaximumLimit+1 {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "channel grant administration page size is invalid",
		)
	}
	repo, err := tx.repo(channelGrantKind)
	if err != nil {
		return nil, err
	}
	if err := refuseAppendOnlyRowLock(channelGrantKind, repo); err != nil {
		return nil, err
	}
	filters := []model.Filter{
		{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
	}
	if persisted, bounded := state.persistedState(); bounded {
		filters = append(filters, model.Filter{
			Column: colCommState, Op: model.OpEq, Value: string(persisted),
		})
	}
	if hasSubject {
		filters = append(filters,
			model.Filter{Column: colCommSubjectKind, Op: model.OpEq, Value: string(subject.Kind)},
			model.Filter{Column: colCommSubjectRef, Op: model.OpEq, Value: subject.Ref},
		)
	}
	if !anchor.IsZero() {
		filters = append(filters, model.Filter{
			Column: model.ColID, Op: model.OpGt, Value: anchor.String(),
		})
	}
	records, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: want})
	if err != nil {
		return nil, err
	}
	if len(records) > want {
		return nil, directNoticeReadUnknown("channel grant page exceeded its requested size", nil)
	}
	ids := make([]model.ID, 0, len(records))
	seen := make(map[model.ID]struct{}, len(records))
	previous := anchor
	for _, record := range records {
		id, parseErr := directNoticeRecordID(record, model.ColID)
		if parseErr != nil {
			return nil, parseErr
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, directNoticeReadUnknown("channel grant page repeats an ID", nil)
		}
		if !previous.IsZero() && id.String() <= previous.String() {
			return nil, directNoticeReadUnknown("channel grant page is not ordered past its anchor", nil)
		}
		seen[id] = struct{}{}
		previous = id
		ids = append(ids, id)
	}
	grants := make([]ChannelGrant, 0, len(ids))
	for _, id := range ids {
		locked, lockErr := tx.lockRecord(ctx, channelGrantKind, id)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		grant, decodeErr := channelGrantFromRecord(locked)
		if decodeErr != nil {
			return nil, directNoticeReadUnknown("locked ChannelGrant row is malformed", decodeErr)
		}
		if grant.ID != id || grant.ChannelID != channelID ||
			grant.TenantID != scope.TenantID || grant.WorkspaceID != scope.WorkspaceID ||
			!channelGrantMatchesAdministrationFilters(grant, state, subject, hasSubject) {
			return nil, directNoticeReadUnknown("locked ChannelGrant row crossed its page", nil)
		}
		grants = append(grants, grant)
	}
	return grants, nil
}

// channelGrantMatchesAdministrationFilters re-checks in Go what the store
// statement bound, so a row that changed identity between the list and the lock
// is refused instead of reported.
func channelGrantMatchesAdministrationFilters(
	grant ChannelGrant,
	state ChannelGrantAdministrationStateFilter,
	subject CommunicationSubjectRef,
	hasSubject bool,
) bool {
	if persisted, bounded := state.persistedState(); bounded && grant.State != persisted {
		return false
	}
	if !state.Valid() || !grant.State.Valid() {
		return false
	}
	if hasSubject && grant.Subject != subject {
		return false
	}
	return true
}
