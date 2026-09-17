// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The personal incoming-handoff surface is the recipient's own discovery and
// reading of an offer that was addressed to it. It exists because nothing else
// in K3 lets a recipient find its offer: HandoffOfferResult returns the ID, the
// version and the ETag to the SENDER, HandoffResponseResult returns the effect
// to whoever already responded, and the workflow projections answer the private
// execution of a workflow, not an authenticated HTTP reader.
//
// AUTHORITY. Both reads are authorized as the EXACT CURRENT DELIVERY READ the
// recipient already holds — `sessions:delivery:read` on that one Delivery — and
// nothing else. Reading a Delivery that IS a handoff offer opens the offer
// context that is inseparable from it; it does not confer Handoff administration
// and it does not confer any authority over the WorkItem. The read gate's carrier
// stays the Delivery, with the ordinary Delivery snapshot: the Handoff is not
// dressed up as the carrier. The independent Handoff validation below then binds
// the protected payload to that already-authorized result.
//
// The four-permission ceiling of the communication-session credential
// (core/auth/communicationsession.go) is unchanged: delivery:read, delivery:write,
// message-send:write and handoff-response:write. Neither read needs a new
// permission, and the response still goes through the existing
// POST /v1/m/sessions/handoffs/{id}/responses under handoff-response:write.
//
// WHAT IS NEVER PROJECTED. The listing never opens content: its cards carry state,
// deadline and authorized references only. The detail shows the summary and next
// action the OFFERER put in the protected payload, and nothing of the WorkItem
// beyond its id under an explicit `presentation: "handoff_context"` marker — no
// brief, description, events, acceptance, lease data, secrets or full title.
// Whoever also has authority over the work reads it through the existing
// GET /v1/m/sessions/work-items/{id} under `sessions:work:read`; being the
// recipient of an offer never implies that permission.
//
// NO GET WRITES. Neither read acknowledges, advances a cursor, transitions an
// expiry, appends a work event or writes a command receipt. The bound transaction
// exists to LOCK and corroborate, not to mutate.
const (
	incomingHandoffDefaultLimit = directNoticeInboxDefaultLimit
	incomingHandoffMaximumLimit = directNoticeInboxMaximumLimit
	// incomingHandoffScanBatch is the candidate page per discovery round.
	incomingHandoffScanBatch = directNoticeInboxScanBatch
	// incomingHandoffCandidateBound bounds total candidate work for one page. A
	// page that cannot prove completeness within it answers unavailable; it never
	// answers an empty page or a fabricated has_more.
	incomingHandoffCandidateBound = directNoticeInboxCandidateBound
	// incomingHandoffWorkItemPresentation marks the WorkItem reference as the
	// handoff context projection, so no reader mistakes it for the work record.
	incomingHandoffWorkItemPresentation = "handoff_context"
)

// IncomingHandoffOfferContext reports whether an offered Handoff is still
// coherent with the work it names. It is NOT a capability token and it promises
// nothing about a later response: the mutation revalidates everything again.
type IncomingHandoffOfferContext string

const (
	// IncomingHandoffContextCurrent: the offer is `offered` and its owner,
	// owner epoch, context sequence and offered lease fence still match the
	// WorkItem and WorkLease observed under lock.
	IncomingHandoffContextCurrent IncomingHandoffOfferContext = "current"
	// IncomingHandoffContextStale: the offer is still `offered` but the work it
	// names moved on. An owner change makes the offer obsolete; it does not make
	// the new owner an authorized recipient.
	IncomingHandoffContextStale IncomingHandoffOfferContext = "stale"
	// IncomingHandoffContextTerminal: the Handoff is in any terminal state.
	IncomingHandoffContextTerminal IncomingHandoffOfferContext = "terminal"
)

// IncomingHandoffRequest is the public navigation contract of the personal
// listing. The continuation is an opaque h3n1 token anchored to the last offer
// returned; raw deadlines, ids, offsets and store cursors are never accepted.
type IncomingHandoffRequest struct {
	State        HandoffState `json:"state,omitempty"`
	Continuation string       `json:"continuation,omitempty"`
	Limit        int          `json:"limit,omitempty"`
}

// IncomingHandoffOfferView is the trustworthy identity and version of one offer.
// `etag` is the strong Handoff ETag the existing response endpoint takes in
// If-Match; it is the aggregate's CAS coordinate, NOT an HTTP cache validator
// for this projection.
type IncomingHandoffOfferView struct {
	ID           model.ID     `json:"id"`
	Version      int64        `json:"version"`
	ETag         string       `json:"etag"`
	State        HandoffState `json:"state"`
	From         RecipientRef `json:"from"`
	To           RecipientRef `json:"to"`
	AckDeadline  time.Time    `json:"ack_deadline"`
	CreatedAt    time.Time    `json:"created_at"`
	TerminalAt   *time.Time   `json:"terminal_at,omitempty"`
	TerminalCode string       `json:"terminal_code,omitempty"`
}

// IncomingHandoffCarrierView is the carrier the offer travelled on. The
// delivery version is the Delivery's own CAS coordinate; it must never be used
// as the If-Match of the Handoff response.
type IncomingHandoffCarrierView struct {
	ChannelID       model.ID `json:"channel_id"`
	MessageID       model.ID `json:"message_id"`
	DeliveryID      model.ID `json:"delivery_id"`
	DeliveryVersion int64    `json:"delivery_version"`
}

// IncomingHandoffWorkItemView is a reference, not a work projection.
type IncomingHandoffWorkItemView struct {
	ID           model.ID `json:"id"`
	Presentation string   `json:"presentation"`
}

// IncomingHandoffSummary is one visible offer card. It carries no content.
type IncomingHandoffSummary struct {
	Handoff  IncomingHandoffOfferView    `json:"handoff"`
	Carrier  IncomingHandoffCarrierView  `json:"carrier"`
	WorkItem IncomingHandoffWorkItemView `json:"work_item"`
	// ObservedAt is the DATABASE time of the transaction that authorized this
	// card, never the process clock.
	ObservedAt time.Time `json:"observed_at"`
	// DeadlineElapsed reports the response window against that database time. A
	// row may still be persisted as `offered` after its deadline: this read never
	// runs the reaper and never transitions it.
	DeadlineElapsed bool `json:"deadline_elapsed"`
}

// IncomingHandoffPage reports only visible offers. has_more is true exactly when
// the bound transaction authorized a visible limit+1th offer; there is no total
// and no hidden-row side channel.
type IncomingHandoffPage struct {
	Items        api.JSONArray[IncomingHandoffSummary] `json:"items"`
	Continuation string                                `json:"continuation,omitempty"`
	HasMore      bool                                  `json:"has_more"`
}

// IncomingHandoffReadResult is the personal detail: the summary plus the
// protected handoff context the offerer supplied.
type IncomingHandoffReadResult struct {
	IncomingHandoffSummary
	Content        HandoffContent              `json:"content"`
	TerminalReason *CommunicationReasonContent `json:"terminal_reason,omitempty"`
	OfferContext   IncomingHandoffOfferContext `json:"offer_context"`
}

// incomingHandoffAnchor is the exact keyset coordinate (ack_deadline, id) of the
// last offer a page returned. The deadline is the canonical stored text, so the
// resumption predicate compares the same bytes the column holds.
type incomingHandoffAnchor struct {
	deadline  string
	handoffID model.ID
}

func (a incomingHandoffAnchor) set() bool {
	return a.deadline != "" && !a.handoffID.IsZero()
}

type incomingHandoffCandidate struct {
	handoffID  model.ID
	deliveryID model.ID
	deadline   string
	question   communicationAuthorityQuestion
}

// incomingHandoffContentIdentity is the durable coordinate set that the opened
// bytes belong to. It is compared across the sealer wait: the bytes published
// must be the bytes the FINAL authority close still names.
type incomingHandoffContentIdentity struct {
	handoffID     model.ID
	messageID     model.ID
	deliveryID    model.ID
	channelID     model.ID
	workItemID    model.ID
	payloadDigest string
	reasonDigest  string
	hasReason     bool
}

type incomingHandoffAuthorizedRead struct {
	summary    IncomingHandoffSummary
	context    IncomingHandoffOfferContext
	identity   incomingHandoffContentIdentity
	openPlan   ProtectedPayloadOpenPlan
	reasonPlan *ProtectedPayloadOpenPlan
}

// requireIncomingHandoffRecipientPrincipal admits exactly the directory
// principals that can BE a recipient: users, exchanged agents and communication
// sessions. A system principal is refused; it is nobody's mailbox.
func requireIncomingHandoffRecipientPrincipal(principal CommunicationPrincipal) error {
	if ValidateCommunicationPrincipal(principal) != nil || principal.System {
		return communicationError(
			ErrCommunicationForbidden,
			"incoming handoff read requires a directory principal credential",
		)
	}
	return nil
}

// ListIncomingHandoffs is the handler-facing personal listing, gated by the
// complete K3 readiness conjunction after the current credential is rebound.
func (m *Module) ListIncomingHandoffs(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request IncomingHandoffRequest,
) (IncomingHandoffPage, error) {
	return m.listIncomingHandoffs(ctx, scope, ref, request, true)
}

// listIncomingHandoffsWithAuthority is the exact private seam the focused module
// tests use while the aggregate readiness conjunction is OFF, in the same shape
// as listVisibleChannelsWithAuthority. The credential is still rebound first;
// only the readiness term is skipped.
func (m *Module) listIncomingHandoffsWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request IncomingHandoffRequest,
) (IncomingHandoffPage, error) {
	return m.listIncomingHandoffs(ctx, scope, ref, request, false)
}

func (m *Module) listIncomingHandoffs(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request IncomingHandoffRequest,
	requireReadiness bool,
) (IncomingHandoffPage, error) {
	state := request.State
	if state == "" {
		state = HandoffOffered
	}
	if request.Limit < 0 || request.Limit > incomingHandoffMaximumLimit ||
		len(request.Continuation) > communicationCursorTokenMaxBytes ||
		!validIncomingHandoffStateFilter(state) {
		return IncomingHandoffPage{}, communicationError(
			ErrInvalidCommunicationModel, "invalid incoming handoff navigation",
		)
	}
	limit := request.Limit
	if limit == 0 {
		limit = incomingHandoffDefaultLimit
	}
	identity, err := m.bindCurrentCommunicationIdentity(
		ctx, scope, ref, requireIncomingHandoffRecipientPrincipal,
	)
	if err != nil {
		return IncomingHandoffPage{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return IncomingHandoffPage{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	if !m.CommunicationCursorTokenKeyringBound() {
		return IncomingHandoffPage{}, communicationError(
			ErrCommunicationEvidenceUnknown, "incoming handoff navigation keyring is unavailable",
		)
	}
	reader, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, identity.principal, nil)
	if err != nil {
		if errors.Is(err, errDirectNoticePrincipalNotFound) {
			return emptyIncomingHandoffPage(), nil
		}
		return IncomingHandoffPage{}, err
	}
	if reader.Closure.Outcome == ReadDeny {
		// A valid, current DENY closure: this principal is not a current member of
		// anything in the workspace. Nothing is visible and nothing is disclosed.
		return emptyIncomingHandoffPage(), nil
	}

	var anchor incomingHandoffAnchor
	if request.Continuation != "" {
		observedAt, timeErr := m.observeChannelCatalogDatabaseTime(ctx, scope)
		if timeErr != nil {
			return IncomingHandoffPage{}, timeErr
		}
		claims, verifyErr := m.communicationCursorTokenKeyring().verifyIncomingHandoffNavigation(
			request.Continuation, observedAt,
		)
		if verifyErr != nil {
			return IncomingHandoffPage{}, verifyErr
		}
		// A token conveys no authority, so it is checked against the identity this
		// request just re-resolved, never the other way round.
		if claims.tenantID != scope.TenantID || claims.workspaceID != scope.WorkspaceID ||
			claims.recipient != reader.Recipient || claims.state != state {
			return IncomingHandoffPage{}, communicationCursorTokenInvalid(
				"incoming handoff navigation token crossed its authenticated recipient or filter",
			)
		}
		if claims.recipient.Kind == RecipientSession &&
			(claims.sessionSID != identity.principal.SessionID ||
				claims.sessionFence != identity.principal.SessionFence) {
			return IncomingHandoffPage{}, communicationCursorTokenInvalid(
				"incoming handoff navigation token crossed its session claim generation",
			)
		}
		anchor = incomingHandoffAnchor{
			deadline: claims.anchorDeadline, handoffID: claims.anchorHandoffID,
		}
	}

	// EVERY PUBLISHED CARD COMES FROM ONE CLOSE OF THE WHOLE SELECTION.
	//
	// Discovery is a prefilter over the recipient's own rows, so a candidate the
	// bound transaction then hides is an ORDINARY exclusion (a revoked local
	// grant, a stale Claim, an offer that just left the filter), not an anomaly.
	// Excluding it may under-fill the lookahead, and an under-filled lookahead
	// cannot prove has_more=false, so the scan CONTINUES from the last candidate
	// examined until limit+1 offers are visible or the recipient's rows of this
	// state are exhausted. Only exhaustion licenses has_more=false; a budget that
	// runs out first answers unavailable.
	//
	// What the scan accumulates is CANDIDATES, never cards. A page assembled from
	// the results of several closes would mix authority instants: an offer
	// authorized by an early round could be published by a request whose own later
	// round already observed the revocation that hides it. So the selection is
	// re-closed WHOLE on every attempt, and the answer is the cards of the LAST
	// close — one transaction, one database instant, one authority. If that close
	// drops a member the selection is not short-changed either: the scan resumes
	// and looks for a replacement, and the next close covers the new whole.
	selected := make([]incomingHandoffCandidate, 0, limit+1)
	var items []IncomingHandoffSummary
	var observedAt time.Time
	scan := anchor
	budget := incomingHandoffCandidateBound
	exhausted := false
	for {
		for len(selected) <= limit && !exhausted && budget >= 1 {
			provisional, examined, done, discoverErr := m.discoverIncomingHandoffCandidates(
				ctx, identity, reader, state, scan, limit+1-len(selected), budget,
			)
			if discoverErr != nil {
				return IncomingHandoffPage{}, discoverErr
			}
			budget -= examined.scanned
			if examined.last.set() {
				scan = examined.last
			}
			selected = append(selected, provisional...)
			exhausted = done
			if examined.scanned == 0 && !done {
				// The budget could not fund another round; the outer guard reports it.
				break
			}
		}
		if len(selected) == 0 {
			if exhausted {
				return emptyIncomingHandoffPage(), nil
			}
			return IncomingHandoffPage{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"incoming handoff candidate work exceeds bound",
			)
		}
		cards, survivors, closedAt, closeErr := m.closeIncomingHandoffPage(
			ctx, identity, reader, state, selected,
		)
		if closeErr != nil {
			return IncomingHandoffPage{}, closeErr
		}
		complete := len(survivors) == len(selected)
		items, observedAt, selected = cards, closedAt, survivors
		if complete || exhausted || budget < 1 {
			break
		}
		// The close proved some members invisible at ITS OWN instant, so they are
		// dropped by evidence rather than by silence. The scan resumes for
		// replacements and the next close covers the new whole.
	}
	if !exhausted && len(items) <= limit && budget < 1 {
		// The scan is neither complete nor able to continue, so has_more cannot be
		// answered honestly in either direction.
		return IncomingHandoffPage{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"incoming handoff candidate work exceeds bound",
		)
	}
	if len(items) == 0 {
		return emptyIncomingHandoffPage(), nil
	}
	page := IncomingHandoffPage{Items: api.JSONArray[IncomingHandoffSummary]{}}
	if len(items) > limit {
		page.HasMore = true
		items = items[:limit]
	}
	page.Items = append(page.Items, items...)
	if page.HasMore {
		last := items[len(items)-1]
		filter, filterErr := incomingHandoffFilterHash(state)
		if filterErr != nil {
			return IncomingHandoffPage{}, filterErr
		}
		claims := communicationIncomingHandoffNavigationClaims{
			tenantID: scope.TenantID, workspaceID: scope.WorkspaceID,
			recipient: reader.Recipient, state: state, filterHash: filter[:],
			anchorDeadline:  model.NewTimestamp(last.Handoff.AckDeadline).String(),
			anchorHandoffID: last.Handoff.ID,
		}
		if reader.Recipient.Kind == RecipientSession {
			claims.sessionSID = identity.principal.SessionID
			claims.sessionFence = identity.principal.SessionFence
		}
		token, mintErr := m.communicationCursorTokenKeyring().mintIncomingHandoffNavigation(
			claims, observedAt,
		)
		if mintErr != nil {
			return IncomingHandoffPage{}, mintErr
		}
		page.Continuation = token
	}
	return page, nil
}

func emptyIncomingHandoffPage() IncomingHandoffPage {
	return IncomingHandoffPage{Items: api.JSONArray[IncomingHandoffSummary]{}}
}

// incomingHandoffScanProgress reports what one discovery call consumed: how many
// candidate rows it examined against the budget, and the exact keyset coordinate
// of the LAST row it looked at, so the next call resumes strictly after it.
type incomingHandoffScanProgress struct {
	scanned int
	last    incomingHandoffAnchor
}

// discoverIncomingHandoffCandidates runs bounded rounds until `want`
// provisionally visible candidates exist, the budget is spent, or the
// recipient's rows of this state are exhausted. Discovery is a PREFILTER: the
// candidate row supplies no witness and authorizes nothing. Each round asks the
// exact core Delivery/read question per candidate; core DENY is concealed (its
// index is simply absent) and core UNKNOWN aborts the page.
//
// `exhausted` is true only when the scan reached the end of those rows. It is the
// only evidence that licenses has_more=false, so it is never inferred from an
// empty round.
func (m *Module) discoverIncomingHandoffCandidates(
	ctx context.Context,
	identity communicationIdentityBinding,
	reader directNoticeReaderIdentityPreflight,
	state HandoffState,
	anchor incomingHandoffAnchor,
	want int,
	budget int,
) ([]incomingHandoffCandidate, incomingHandoffScanProgress, bool, error) {
	if want < 1 || want > incomingHandoffCandidateBound || budget < 0 {
		return nil, incomingHandoffScanProgress{}, false, communicationError(
			ErrInvalidCommunicationModel, "incoming handoff discovery size is invalid",
		)
	}
	provisional := make([]incomingHandoffCandidate, 0, want)
	seen := make(map[model.ID]struct{}, want)
	var progress incomingHandoffScanProgress
	after := anchor
	exhausted := false
	for len(provisional) < want {
		if budget-progress.scanned < 1 {
			return provisional, progress, false, nil
		}
		batch := min(incomingHandoffScanBatch, budget-progress.scanned)
		rows, hasMore, err := m.readIncomingHandoffCandidateRows(
			ctx, identity.scope, reader.Recipient, state, after, batch,
		)
		if err != nil {
			return nil, incomingHandoffScanProgress{}, false, err
		}
		if len(rows) == 0 {
			if hasMore {
				return nil, incomingHandoffScanProgress{}, false, communicationError(
					ErrCommunicationEvidenceUnknown,
					"incoming handoff scan was truncated without a continuation",
				)
			}
			exhausted = true
			break
		}
		progress.scanned += len(rows)
		if err := m.requireDirectNoticeIdentityCurrent(reader); err != nil {
			return nil, incomingHandoffScanProgress{}, false, err
		}
		questions := make([]communicationAuthorityQuestion, 0, len(rows))
		for _, row := range rows {
			if _, duplicate := seen[row.handoffID]; duplicate {
				return nil, incomingHandoffScanProgress{}, false, communicationError(
					ErrCommunicationEvidenceUnknown, "incoming handoff scan repeated a candidate",
				)
			}
			seen[row.handoffID] = struct{}{}
			question, questionErr := newCommunicationAuthorityQuestion(
				identity.scope, messageDeliveryKind, row.deliveryID, CommunicationRead,
			)
			if questionErr != nil {
				return nil, incomingHandoffScanProgress{}, false, questionErr
			}
			questions = append(questions, question)
		}
		_, admitted, err := bindCommunicationAuthorityBatch(
			ctx, identity, requireIncomingHandoffRecipientPrincipal, questions,
		)
		if err != nil {
			return nil, incomingHandoffScanProgress{}, false, err
		}
		examined := len(rows)
		for _, index := range admitted {
			if len(provisional) == want {
				// The rows from this one on were fetched but NOT examined: the next
				// call must resume at the last candidate actually taken.
				examined = index
				break
			}
			provisional = append(provisional, incomingHandoffCandidate{
				handoffID: rows[index].handoffID, deliveryID: rows[index].deliveryID,
				deadline: rows[index].deadline, question: questions[index],
			})
			examined = index + 1
		}
		if examined < 1 {
			examined = 1
		}
		last := rows[examined-1]
		progress.last = incomingHandoffAnchor{deadline: last.deadline, handoffID: last.handoffID}
		after = progress.last
		if len(provisional) == want {
			break
		}
		if !hasMore {
			exhausted = true
			break
		}
	}
	return provisional, progress, exhausted, nil
}

type incomingHandoffCandidateRow struct {
	handoffID  model.ID
	deliveryID model.ID
	deadline   string
}

// readIncomingHandoffCandidateRows walks the recipient's own offers of one state
// in (ack_deadline, id) order through the EXISTING
// sessions_work_handoff_target(tenant_id, workspace_id, to_kind, to_ref, state,
// ack_deadline, id) index. The workspace term is supplied by the confined scope.
//
// The keyset is exact and needs no new migration. Resuming after (A, I) is two
// index-range reads rather than one, because the store ANDs its filters and has
// no OR: first the remainder of the tie group at deadline A (ack_deadline = A AND
// id > I, ordered by the store's own id tiebreaker), then everything strictly
// after A. A single `ack_deadline >= A` scan would have to re-read and discard
// the whole tie group every round, which stops making progress as soon as a tie
// group is longer than one batch.
func (m *Module) readIncomingHandoffCandidateRows(
	ctx context.Context,
	scope DirectoryScopeRef,
	recipient RecipientRef,
	state HandoffState,
	after incomingHandoffAnchor,
	limit int,
) ([]incomingHandoffCandidateRow, bool, error) {
	if limit < 1 || limit > incomingHandoffCandidateBound || recipient.Validate() != nil ||
		!validIncomingHandoffStateFilter(state) {
		return nil, false, communicationError(
			ErrInvalidCommunicationModel, "incoming handoff candidate scan is invalid",
		)
	}
	rows := make([]incomingHandoffCandidateRow, 0, limit)
	hasMore := false
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		repo, err := sc.Ext(handoffKind)
		if err != nil {
			return err
		}
		base := []model.Filter{
			{Column: colCommToKind, Op: model.OpEq, Value: string(recipient.Kind)},
			{Column: colCommToRef, Op: model.OpEq, Value: recipient.Ref},
			{Column: colCommState, Op: model.OpEq, Value: string(state)},
		}
		collect := func(records []model.Record, wantDeadline string) error {
			for _, record := range records {
				row, decodeErr := incomingHandoffCandidateRowFrom(record)
				if decodeErr != nil {
					return decodeErr
				}
				if wantDeadline != "" && row.deadline != wantDeadline {
					return communicationError(
						ErrCommunicationEvidenceUnknown,
						"incoming handoff tie scan returned a foreign deadline",
					)
				}
				rows = append(rows, row)
			}
			return nil
		}
		if after.set() {
			ties, page, listErr := repo.List(ctx, model.Query{
				Filters: append(append([]model.Filter(nil), base...),
					model.Filter{Column: colCommAckDeadline, Op: model.OpEq, Value: after.deadline},
					model.Filter{Column: model.ColID, Op: model.OpGt, Value: after.handoffID.String()},
				),
				Limit: limit,
			})
			if listErr != nil {
				return listErr
			}
			if err := collect(ties, after.deadline); err != nil {
				return err
			}
			if page.HasMore || len(rows) >= limit {
				hasMore = true
				if len(rows) > limit {
					rows = rows[:limit]
				}
				return nil
			}
		}
		filters := append([]model.Filter(nil), base...)
		if after.set() {
			filters = append(filters, model.Filter{
				Column: colCommAckDeadline, Op: model.OpGt, Value: after.deadline,
			})
		}
		next, page, listErr := repo.List(ctx, model.Query{
			Filters: filters,
			Sort:    []model.Sort{{Column: colCommAckDeadline}},
			Limit:   limit - len(rows),
		})
		if listErr != nil {
			return listErr
		}
		if err := collect(next, ""); err != nil {
			return err
		}
		hasMore = page.HasMore || len(rows) >= limit
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	// The deadline column is fixed-width canonical text, so its chronology is
	// engine-independent; assert it here rather than trusting the ordering of a
	// row set that already crossed a collation boundary. The id tiebreaker inside
	// one deadline is the store's own, and repetition (not byte order) is what a
	// broken tiebreaker would show, which discoverIncomingHandoffCandidates checks.
	var previous time.Time
	for _, row := range rows {
		parsed, parseErr := model.ParseTimestamp(row.deadline)
		if parseErr != nil {
			return nil, false, communicationError(
				ErrCommunicationEvidenceUnknown, "incoming handoff candidate deadline is malformed",
			)
		}
		if parsed.Time().Before(previous) {
			return nil, false, communicationError(
				ErrCommunicationEvidenceUnknown, "incoming handoff candidate ordering is malformed",
			)
		}
		previous = parsed.Time()
	}
	return rows, hasMore, nil
}

func incomingHandoffCandidateRowFrom(record model.Record) (incomingHandoffCandidateRow, error) {
	handoffID, err := directNoticeRecordID(record, model.ColID)
	if err != nil {
		return incomingHandoffCandidateRow{}, err
	}
	deliveryID, err := directNoticeRecordID(record, colCommDeliveryID)
	if err != nil {
		return incomingHandoffCandidateRow{}, err
	}
	deadline := record.String(colCommAckDeadline)
	parsed, parseErr := model.ParseTimestamp(deadline)
	if parseErr != nil || parsed.String() != deadline {
		return incomingHandoffCandidateRow{}, communicationError(
			ErrCommunicationEvidenceUnknown, "incoming handoff candidate deadline is malformed",
		)
	}
	return incomingHandoffCandidateRow{
		handoffID: handoffID, deliveryID: deliveryID, deadline: deadline,
	}, nil
}

// closeIncomingHandoffPage freshly binds exact core Delivery evidence for every
// selected candidate (page plus lookahead) and closes the whole set in ONE bound
// transaction: the fact union is locked, every carrier graph is locked and
// corroborated, database time is refreshed after all blocking locks, and the
// request window is narrowed to the read grant's horizon. Nothing is materialized
// before that transaction commits.
//
// It returns the cards AND the candidates that produced them, at ONE database
// instant. The caller publishes cards from a single call, never from several: a
// page unioned across closes could carry an offer authorized by an early close
// through a request whose own later close already observed the revocation that
// hides it. The survivors let the caller resume the scan for replacements instead
// of shipping a short page or a page assembled from two authority instants.
func (m *Module) closeIncomingHandoffPage(
	ctx context.Context,
	identity communicationIdentityBinding,
	reader directNoticeReaderIdentityPreflight,
	state HandoffState,
	selected []incomingHandoffCandidate,
) ([]IncomingHandoffSummary, []incomingHandoffCandidate, time.Time, error) {
	if len(selected) == 0 || len(selected) > incomingHandoffCandidateBound {
		return nil, nil, time.Time{}, communicationError(
			ErrInvalidCommunicationModel, "incoming handoff page closure is invalid",
		)
	}
	fresh, err := m.preflightDirectNoticeReaderIdentity(ctx, identity.scope, identity.principal, nil)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if fresh.Recipient != reader.Recipient || fresh.Closure.Outcome != ReadAllow {
		return nil, nil, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"incoming handoff recipient authority changed before page closure",
		)
	}
	window, err := directNoticeReaderAuthorityWindow(fresh)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	questions := make([]communicationAuthorityQuestion, len(selected))
	for index, candidate := range selected {
		if candidate.question.entity.Kind != messageDeliveryKind ||
			candidate.question.operation != CommunicationRead ||
			candidate.question.entity.ID != candidate.deliveryID {
			return nil, nil, time.Time{}, communicationError(
				ErrCommunicationEvidenceUnknown, "incoming handoff candidate crossed its question",
			)
		}
		questions[index] = candidate.question
	}
	batch, admitted, err := bindCommunicationAuthorityBatch(
		ctx, identity, requireIncomingHandoffRecipientPrincipal, questions,
	)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if len(admitted) != len(questions) {
		return nil, nil, time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"incoming handoff core authority changed before page closure",
		)
	}

	var items []IncomingHandoffSummary
	var survivors []incomingHandoffCandidate
	var observedAt time.Time
	err = m.mutateCommunicationWithBoundAuthorityBatch(
		ctx, identity.scope, identity, requireIncomingHandoffRecipientPrincipal,
		questions, batch, window,
		func(tx *communicationTx, contexts []communicationRequestAuthorityBatchContext) error {
			if len(contexts) != len(questions) {
				return directNoticeReadUnknown("incoming handoff authority batch changed size", nil)
			}
			preflights := make([]directNoticeReaderPreflight, len(contexts))
			allFacts := make([]store.AuthorizationFactRef, 0)
			for index, bound := range contexts {
				if bound.question != questions[index] || bound.principal != identity.principal {
					return directNoticeReadUnknown(
						"incoming handoff authority crossed its candidate", nil,
					)
				}
				preflight, preflightErr := directNoticeReaderPreflightWithCore(fresh, bound.witness)
				if preflightErr != nil {
					return preflightErr
				}
				preflights[index] = preflight
				allFacts = append(allFacts, preflight.Facts...)
			}
			facts, factErr := canonicalAuthorizationFactUnion(allFacts)
			if factErr != nil {
				return directNoticeReadUnknown(
					"incoming handoff authority facts are unavailable", factErr,
				)
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			// Every selected graph is locked BEFORE the clock is refreshed, because
			// the bound transaction refreshes exactly once, after all wait-capable
			// locks. One database instant then judges the whole page.
			locked := make([]incomingHandoffLockedOffer, 0, len(selected))
			kept := make([]incomingHandoffCandidate, 0, len(selected))
			keptPreflights := make([]directNoticeReaderPreflight, 0, len(selected))
			for index, candidate := range selected {
				graph, lockErr := lockIncomingHandoffOfferGraph(
					ctx, tx, identity.scope, candidate.handoffID, nil,
				)
				if lockErr != nil {
					if directNoticeReadIsHidden(lockErr) {
						continue
					}
					return lockErr
				}
				locked = append(locked, graph)
				kept = append(kept, candidate)
				keptPreflights = append(keptPreflights, preflights[index])
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			built := make([]IncomingHandoffSummary, 0, len(kept))
			visible := make([]incomingHandoffCandidate, 0, len(kept))
			for index, candidate := range kept {
				authorized, evaluateErr := evaluateLockedIncomingHandoff(
					tx, keptPreflights[index], locked[index], state,
					candidate.deliveryID, dbNow,
				)
				if evaluateErr != nil {
					// A candidate the locked evaluation hides is an ordinary
					// exclusion for THIS reader, not a page failure: the caller
					// keeps scanning, so an excluded offer can never make has_more
					// answer false while a visible one waits behind it. Evidence
					// that could not be established still aborts.
					if directNoticeReadIsHidden(evaluateErr) ||
						errors.Is(evaluateErr, errIncomingHandoffLeftFilter) {
						continue
					}
					return evaluateErr
				}
				built = append(built, authorized.summary)
				visible = append(visible, candidate)
			}
			items, survivors = built, visible
			observedAt = dbNow
			return nil
		},
	)
	if err != nil {
		return nil, nil, time.Time{}, normalizeDirectNoticeAuthorizationError(err)
	}
	return items, survivors, observedAt, nil
}

// GetIncomingHandoffByDelivery is the handler-facing personal detail read for
// one exact Delivery, gated by the complete K3 readiness conjunction.
func (m *Module) GetIncomingHandoffByDelivery(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
) (IncomingHandoffReadResult, error) {
	return m.getIncomingHandoffByDeliveryWithCurrentAuthority(
		ctx, scope, ref, deliveryID, OpenProtectedPayload, true,
	)
}

func (m *Module) getIncomingHandoffByDeliveryWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
) (IncomingHandoffReadResult, error) {
	return m.getIncomingHandoffByDeliveryWithCurrentAuthority(
		ctx, scope, ref, deliveryID, OpenProtectedPayload, false,
	)
}

func (m *Module) getIncomingHandoffByDeliveryWithAuthorityAndOpener(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
	opener directNoticePayloadOpener,
) (IncomingHandoffReadResult, error) {
	return m.getIncomingHandoffByDeliveryWithCurrentAuthority(
		ctx, scope, ref, deliveryID, opener, false,
	)
}

// getIncomingHandoffByDeliveryWithCurrentAuthority closes authority TWICE around
// the sealer, and the SECOND close is the authority point of the response.
//
// The first close authorizes and produces the open plans while every relevant row
// is locked. The sealer/KMS work then runs with NO lock held, because holding a
// row lock across external I/O is how a slow custody backend becomes a store-wide
// stall. An opener suspended across a revocation, a Claim change or a content
// change must not publish the bytes it opened, so the second close re-resolves
// the credential, re-locks the same graph and must still name the same durable
// content; anything else answers unavailable with no partial payload.
//
// The second close is unconditional, not conditional on the payload being sealed:
// making the authority point depend on the channel's protection configuration
// would give two deployments two different answers to "when was this authorized".
func (m *Module) getIncomingHandoffByDeliveryWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
	opener directNoticePayloadOpener,
	requireReadiness bool,
) (IncomingHandoffReadResult, error) {
	if !validCanonicalCommunicationID(deliveryID) {
		return IncomingHandoffReadResult{}, incomingHandoffNotFound()
	}
	if opener == nil {
		return IncomingHandoffReadResult{}, communicationError(
			ErrInvalidCommunicationModel, "incoming handoff content opener is unavailable",
		)
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return IncomingHandoffReadResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	planned, hidden, err := m.closeIncomingHandoffPointRead(ctx, scope, ref, deliveryID)
	if err != nil {
		return IncomingHandoffReadResult{}, normalizeIncomingHandoffReadError(err)
	}
	if hidden {
		return IncomingHandoffReadResult{}, incomingHandoffNotFound()
	}
	content, reason, err := m.openIncomingHandoffContent(ctx, planned, opener)
	if err != nil {
		return IncomingHandoffReadResult{}, err
	}
	final, hidden, err := m.closeIncomingHandoffPointRead(ctx, scope, ref, deliveryID)
	if err != nil {
		return IncomingHandoffReadResult{}, normalizeIncomingHandoffReadError(err)
	}
	if hidden {
		// The offer stopped being visible while its content was being opened. That
		// is not a clean absence of bytes we already hold: it is a refusal to
		// publish them.
		return IncomingHandoffReadResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"incoming handoff authority was withdrawn while its content was opening",
		)
	}
	if planned.identity != final.identity {
		return IncomingHandoffReadResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"incoming handoff content changed while it was opening",
		)
	}
	return IncomingHandoffReadResult{
		IncomingHandoffSummary: final.summary,
		Content:                content,
		TerminalReason:         reason,
		OfferContext:           final.context,
	}, nil
}

// closeIncomingHandoffPointRead binds one fresh exact Delivery/read authority and
// closes the whole offer graph under it, including the WorkItem and WorkLease the
// offer context is measured against. It never opens content.
func (m *Module) closeIncomingHandoffPointRead(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
) (incomingHandoffAuthorizedRead, bool, error) {
	question, err := newCommunicationAuthorityQuestion(
		scope, messageDeliveryKind, deliveryID, CommunicationRead,
	)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, false, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, false, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return incomingHandoffAuthorizedRead{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"incoming handoff authority context crossed its exact request",
		)
	}
	if err := requireIncomingHandoffRecipientPrincipal(inspected.principal); err != nil {
		return incomingHandoffAuthorizedRead{}, false, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, false, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, false, err
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(identity.Principal),
	)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, false, err
	}
	var authorized incomingHandoffAuthorizedRead
	var hidden bool
	err = m.mutateHandoffWithAuthority(
		ctx, question, bound, window, claims,
		func(
			tx *communicationTx,
			repositories handoffWorkRepositories,
			consumed communicationRequestAuthorityContext,
		) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if consumed.question != question || consumed.bindingID == nil ||
				consumed.bindingID != inspected.bindingID ||
				consumed.question.entity != (EntityRef{
					TenantID: scope.TenantID, Kind: messageDeliveryKind, ID: deliveryID,
					WorkspaceID: scope.WorkspaceID,
				}) || consumed.question.operation != CommunicationRead ||
				consumed.principal != identity.Principal {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"incoming handoff authority crossed its exact Delivery request",
				)
			}
			preflight, preflightErr := directNoticeReaderPreflightWithCore(identity, consumed.witness)
			if preflightErr != nil {
				return preflightErr
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, preflight.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			handoffID, resolveErr := resolveIncomingHandoffByDelivery(ctx, tx, deliveryID)
			if resolveErr != nil {
				if directNoticeReadIsHidden(resolveErr) {
					if refreshErr := tx.refreshNow(ctx); refreshErr != nil {
						return refreshErr
					}
					hidden = true
					return nil
				}
				return resolveErr
			}
			graph, lockErr := lockIncomingHandoffOfferGraph(
				ctx, tx, scope, handoffID, &repositories,
			)
			if lockErr != nil {
				if directNoticeReadIsHidden(lockErr) {
					if refreshErr := tx.refreshNow(ctx); refreshErr != nil {
						return directNoticeReadUnknown(
							"incoming handoff carrier changed before final confirmation", refreshErr,
						)
					}
					hidden = true
					return nil
				}
				return lockErr
			}
			// The clock is refreshed once, after every wait-capable lock, and the
			// whole decision is judged against that one database instant.
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			read, evaluateErr := evaluateLockedIncomingHandoff(
				tx, preflight, graph, "", deliveryID, tx.now.Time(),
			)
			if evaluateErr != nil {
				if directNoticeReadIsHidden(evaluateErr) {
					hidden = true
					return nil
				}
				return evaluateErr
			}
			authorized = read
			return nil
		},
	)
	if err != nil && (errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, ErrCommunicationNotFound) || errors.Is(err, ErrCommunicationForbidden)) {
		err = directNoticeReadUnknown(
			"incoming handoff absence was not confirmed by the bound transaction", err,
		)
	}
	return authorized, hidden, normalizeDirectNoticeAuthorizationError(err)
}

// resolveIncomingHandoffByDelivery observes the single Handoff a Delivery
// carries. The delivery uniqueness is a stored INDEX
// (sessions_work_handoff_delivery_uniq); two rows for the same Delivery is an
// evidence failure, not a choice to make.
func resolveIncomingHandoffByDelivery(
	ctx context.Context,
	tx *communicationTx,
	deliveryID model.ID,
) (model.ID, error) {
	repo, err := tx.repo(handoffKind)
	if err != nil {
		return "", err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommDeliveryID, Op: model.OpEq, Value: deliveryID.String()},
		},
		Limit: 2,
	})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", incomingHandoffNotFound()
	}
	if len(rows) != 1 || page.HasMore {
		return "", directNoticeReadUnknown("Delivery carries an ambiguous Handoff", nil)
	}
	return directNoticeRecordID(rows[0], model.ColID)
}

// incomingHandoffLockedOffer is the locked, still-unjudged offer graph. It is
// produced while the transaction is in its locking phase and evaluated later,
// after the single clock refresh, so a page of offers is judged at one database
// instant instead of drifting across candidates.
type incomingHandoffLockedOffer struct {
	handoff Handoff
	carrier handoffLockedCarrier
	work    handoffLockedWork
	hasWork bool
}

// lockIncomingHandoffOfferGraph locks one exact offer graph. When repositories is
// non-nil it also locks the WorkItem/WorkLease the offer context is measured
// against; the listing passes nil, because it projects neither content nor work
// state and must not take work locks it does not read.
//
// Lock order is the order lockHandoffLifecycleTarget already establishes for this
// aggregate (work state, then the Handoff row, then the carrier), so a read
// cannot deadlock against the response mutation.
func lockIncomingHandoffOfferGraph(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	handoffID model.ID,
	repositories *handoffWorkRepositories,
) (incomingHandoffLockedOffer, error) {
	if tx == nil || !validCanonicalCommunicationID(handoffID) {
		return incomingHandoffLockedOffer{}, directNoticeReadUnknown(
			"incoming handoff locked read is malformed", nil,
		)
	}
	if repositories != nil {
		handoff, carrier, work, err := lockHandoffLifecycleTarget(
			ctx, tx, *repositories, scope, handoffID,
		)
		if err != nil {
			return incomingHandoffLockedOffer{}, err
		}
		return incomingHandoffLockedOffer{
			handoff: handoff, carrier: carrier, work: work, hasWork: true,
		}, nil
	}
	handoff, carrier, err := lockIncomingHandoffCarrierOnly(ctx, tx, scope, handoffID)
	if err != nil {
		return incomingHandoffLockedOffer{}, err
	}
	return incomingHandoffLockedOffer{handoff: handoff, carrier: carrier}, nil
}

// evaluateLockedIncomingHandoff judges one locked offer graph against the shared
// database instant: exact graph, current read gate over the Delivery carrier and,
// when the work rows were locked, the offer context and the protected open plans.
// It writes nothing.
func evaluateLockedIncomingHandoff(
	tx *communicationTx,
	preflight directNoticeReaderPreflight,
	locked incomingHandoffLockedOffer,
	wantState HandoffState,
	deliveryID model.ID,
	dbNow time.Time,
) (incomingHandoffAuthorizedRead, error) {
	if tx == nil || preflight.Resolution.Recipient == nil ||
		!validCanonicalCommunicationID(deliveryID) || dbNow.IsZero() {
		return incomingHandoffAuthorizedRead{}, directNoticeReadUnknown(
			"incoming handoff evaluation is malformed", nil,
		)
	}
	scope := preflight.Scope
	handoff := locked.handoff
	carrier := locked.carrier
	if handoff.DeliveryID != deliveryID || carrier.delivery.ID != deliveryID {
		return incomingHandoffAuthorizedRead{}, directNoticeReadUnknown(
			"incoming handoff left its exact Delivery", nil,
		)
	}
	if wantState != "" && handoff.State != wantState {
		// The offer transitioned between discovery and closure. It genuinely no
		// longer belongs to this filter, so the listing excludes it and keeps
		// scanning rather than reporting a short page as complete.
		return incomingHandoffAuthorizedRead{}, errIncomingHandoffLeftFilter
	}
	if err := exactIncomingHandoffReadGraph(preflight, carrier, handoff); err != nil {
		return incomingHandoffAuthorizedRead{}, err
	}
	if err := evaluateIncomingHandoffReadGate(tx, preflight, carrier, dbNow); err != nil {
		return incomingHandoffAuthorizedRead{}, err
	}
	summary := IncomingHandoffSummary{
		Handoff: IncomingHandoffOfferView{
			ID: handoff.ID, Version: handoff.Version,
			ETag:  communicationVersionETag(handoff.Version),
			State: handoff.State, From: handoff.From, To: handoff.To,
			AckDeadline: handoff.AckDeadline.UTC(), CreatedAt: handoff.CreatedAt.UTC(),
			TerminalAt:   incomingHandoffTerminalAt(handoff),
			TerminalCode: handoff.TerminalCode,
		},
		Carrier: IncomingHandoffCarrierView{
			ChannelID: carrier.channel.ID, MessageID: carrier.message.ID,
			DeliveryID: carrier.delivery.ID, DeliveryVersion: carrier.delivery.Version,
		},
		WorkItem: IncomingHandoffWorkItemView{
			ID: handoff.WorkItemID, Presentation: incomingHandoffWorkItemPresentation,
		},
		ObservedAt:      dbNow,
		DeadlineElapsed: !dbNow.Before(handoff.AckDeadline),
	}
	authorized := incomingHandoffAuthorizedRead{
		summary: summary,
		identity: incomingHandoffContentIdentity{
			handoffID: handoff.ID, messageID: carrier.message.ID,
			deliveryID: carrier.delivery.ID, channelID: carrier.channel.ID,
			workItemID:    handoff.WorkItemID,
			payloadDigest: hex.EncodeToString(handoff.Payload.Digest),
		},
	}
	if !locked.hasWork {
		return authorized, nil
	}
	context, err := incomingHandoffOfferContext(scope, handoff, locked.work)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, err
	}
	authorized.context = context
	plans, err := planIncomingHandoffContent(scope, carrier, handoff)
	if err != nil {
		return incomingHandoffAuthorizedRead{}, err
	}
	authorized.openPlan = plans.payload
	authorized.reasonPlan = plans.reason
	if handoff.TerminalReason != nil {
		authorized.identity.hasReason = true
		authorized.identity.reasonDigest = hex.EncodeToString(handoff.TerminalReason.Digest)
	}
	return authorized, nil
}

// errIncomingHandoffLeftFilter marks a candidate that transitioned out of the
// requested state between discovery and closure. It is an exclusion, not a
// denial and not an evidence failure, and it never reaches an HTTP caller.
var errIncomingHandoffLeftFilter = errors.New(
	"sessions: incoming handoff left its state filter",
)

// lockIncomingHandoffCarrierOnly is lockHandoffLifecycleTarget without the
// WorkItem/WorkLease phase, for the listing, which projects neither. It keeps the
// same observe-then-lock discipline: the lineage observed before the row lock must
// still be the lineage after it.
func lockIncomingHandoffCarrierOnly(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	handoffID model.ID,
) (Handoff, handoffLockedCarrier, error) {
	handoffs, err := tx.repo(handoffKind)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	observedRecord, err := handoffs.Get(ctx, handoffID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	observed, err := handoffFromRecord(observedRecord)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	messages, err := tx.repo(messageKind)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	messageObservation, err := messages.Get(ctx, observed.MessageID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	channelID, err := directNoticeRecordID(messageObservation, colCommChannelID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	lockedRecord, err := tx.lockRecord(ctx, handoffKind, handoffID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	locked, err := handoffFromRecord(lockedRecord)
	if err != nil || locked.WorkItemID != observed.WorkItemID ||
		locked.MessageID != observed.MessageID || locked.DeliveryID != observed.DeliveryID {
		return Handoff{}, handoffLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff lineage changed while locking",
		)
	}
	carrier, err := lockHandoffCarrier(ctx, tx, scope, channelID, locked.MessageID, locked.DeliveryID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, err
	}
	return locked, carrier, nil
}

// exactIncomingHandoffReadGraph refuses everything that is not one complete,
// sealed, directly addressed handoff offer for THIS recipient. Reading the
// candidate row without closing these links is never enough.
func exactIncomingHandoffReadGraph(
	preflight directNoticeReaderPreflight,
	carrier handoffLockedCarrier,
	handoff Handoff,
) error {
	if len(carrier.deliveries) != 1 || len(carrier.audiences) != 1 ||
		len(carrier.contributions) != 1 || carrier.requiredCount != 1 {
		return incomingHandoffNotFound()
	}
	delivery := carrier.delivery
	audience := carrier.audiences[0]
	contribution := carrier.contributions[0]
	audienceKind, audienceKindErr := recipientAudienceKind(delivery.Recipient)
	sessionWitnessValid := contribution.ObservedSessionSID == "" &&
		contribution.ObservedClaimFence == 0
	if delivery.Recipient.Kind == RecipientSession {
		sessionWitnessValid = contribution.ObservedSessionSID == delivery.Recipient.Ref &&
			contribution.ObservedClaimFence > 0
	}
	if audienceKindErr != nil ||
		handoff.TenantID != preflight.Scope.TenantID ||
		handoff.WorkspaceID != preflight.Scope.WorkspaceID ||
		carrier.message.TenantID != preflight.Scope.TenantID ||
		carrier.message.WorkspaceID != preflight.Scope.WorkspaceID ||
		carrier.message.ChannelID != carrier.channel.ID ||
		carrier.message.Kind != MessageHandoffOffer ||
		carrier.message.WorkItemID != handoff.WorkItemID ||
		carrier.message.ID != handoff.MessageID ||
		delivery.ID != handoff.DeliveryID ||
		// The recipient of the offer, the recipient of the Delivery and the
		// principal this request resolved to must be ONE identity. Possession of an
		// id, being the sender, the owner or a channel admin is none of them.
		handoff.To != preflight.Recipient || delivery.Recipient != preflight.Recipient ||
		handoff.From == handoff.To ||
		audience.MessageID != carrier.message.ID || audience.Ordinal != 1 ||
		audience.RouteRuleID != "" || audience.Selector.Kind != audienceKind ||
		audience.Selector.Ref != delivery.Recipient.Ref || audience.ResolvedCount != 1 ||
		contribution.MessageAudienceID != audience.ID ||
		contribution.MessageDeliveryID != delivery.ID ||
		contribution.Recipient != delivery.Recipient ||
		contribution.RecipientEpoch != delivery.RecipientEpoch ||
		contribution.Selector != audience.Selector || contribution.CausalKind != CausalDirect ||
		contribution.CausalRef != delivery.Recipient.Ref || contribution.CausalFactKind != "" ||
		contribution.CausalFactID != "" || contribution.CausalFactVersion != 0 ||
		!sessionWitnessValid || contribution.OriginalSubscriber != nil ||
		contribution.SubscriptionID != "" || contribution.SubscriptionGeneration != 0 ||
		contribution.RouteRuleID != "" || contribution.RouteRuleGeneration != 0 {
		return incomingHandoffNotFound()
	}
	if err := ValidateHandoff(handoff); err != nil {
		return directNoticeReadUnknown("locked Handoff envelope is malformed", err)
	}
	if err := ValidateHandoffLineage(
		carrier.message, delivery, handoff, carrier.requiredCount,
	); err != nil {
		return directNoticeReadUnknown("incoming handoff lineage is malformed", err)
	}
	if audience.DirectoryEpoch != contribution.DirectoryEpoch ||
		audience.ChannelACLRevision != contribution.ChannelACLRevision ||
		audience.RouteRevision != contribution.RouteRevision ||
		audience.SubscriptionRevision != contribution.SubscriptionRevision {
		return directNoticeReadUnknown("incoming handoff audience provenance diverges", nil)
	}
	fold, err := FoldAudienceContributions(carrier.contributions)
	if err != nil {
		return directNoticeReadUnknown("incoming handoff audience fold is malformed", err)
	}
	if fold.Recipient != delivery.Recipient || fold.Required != delivery.Required ||
		fold.WakePolicy != delivery.WakePolicy ||
		!canonicalCommunicationValueEqual(fold.RouteReasons, delivery.RouteReasons) {
		return directNoticeReadUnknown("incoming handoff Delivery diverges from audience fold", nil)
	}
	if carrier.epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch {
		return directNoticeReadUnknown("locked directory epoch is unavailable", nil)
	}
	return nil
}

// evaluateIncomingHandoffReadGate runs the SAME four current-authority doors the
// direct notice read runs, over the SAME carrier kind: the exact Delivery, with
// the ordinary Delivery snapshot and the CommunicationRead operation. The Handoff
// is deliberately not presented as the carrier — that would relabel a
// delivery-read witness as handoff authority.
func evaluateIncomingHandoffReadGate(
	tx *communicationTx,
	preflight directNoticeReaderPreflight,
	carrier handoffLockedCarrier,
	dbNow time.Time,
) error {
	entity := EntityRef{
		TenantID: preflight.Scope.TenantID, Kind: messageDeliveryKind,
		ID: carrier.delivery.ID, WorkspaceID: preflight.Scope.WorkspaceID,
	}
	if preflight.Core.Operation != CommunicationRead || preflight.Core.Entity != entity ||
		directNoticeReadRowsCarryFutureDBTime(
			carrier.channel, carrier.grants, carrier.message, carrier.deliveries,
			carrier.audiences, carrier.contributions, dbNow,
		) || !communicationEvidenceCurrent(
		preflight.Core.ObservedAt, preflight.Core.FreshUntil, dbNow,
	) || !communicationEvidenceCurrent(
		preflight.Resolution.ObservedAt, preflight.Resolution.FreshUntil, dbNow,
	) || !dbNow.Before(preflight.Resolution.FreshUntil) || !communicationEvidenceCurrent(
		preflight.Closure.ObservedAt, preflight.Closure.FreshUntil, dbNow,
	) {
		return directNoticeReadUnknown(
			"incoming handoff authority expired while waiting for locks", nil,
		)
	}
	if dbNow.Before(carrier.message.AvailableAt) || dbNow.Before(carrier.delivery.AvailableAt) {
		return incomingHandoffNotFound()
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	grantEvidence := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: carrier.channel.ACLRevision, ObservedAt: dbNow, Grants: carrier.grants,
		},
		preflight.Scope.TenantID, preflight.Scope.WorkspaceID, carrier.channel.ID,
		preflight.Closure, ChannelGrantRead, dbNow,
	)
	carrierRef := ProtectedCarrierRef{
		Entity: entity, ChannelID: carrier.channel.ID,
		MessageID: carrier.message.ID, DeliveryID: carrier.delivery.ID,
	}
	currentAudience, err := buildDirectNoticeCurrentAudience(
		preflight, carrier.message, carrier.delivery, carrier.audiences,
		carrier.contributions, dbNow,
	)
	if err != nil {
		return err
	}
	decision, err := EvaluateReadGate(ReadGateEvidence{
		Scope: preflight.Scope, ChannelID: carrier.channel.ID,
		ChannelACLRevision: carrier.channel.ACLRevision, DBNow: dbNow,
		Operation: CommunicationRead, Carrier: carrierRef,
		CarrierState: ProtectedCarrierSnapshot{
			Message: carrier.message, Delivery: carrier.delivery,
			RequiredDeliveryCount: carrier.requiredCount, ObservedAt: dbNow,
			Evidence: clean("carrier_rows_locked", "same_tx:incoming_handoff_carrier"),
		},
		Core: preflight.Core, Principal: preflight.Principal,
		PrincipalResolution: preflight.Resolution, Recipient: preflight.Recipient,
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(preflight.Scope.TenantID),
			Version: carrier.epoch.Version,
		},
		CurrentChannelGrant: grantEvidence,
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: preflight.Scope, Carrier: carrierRef, Principal: preflight.Principal,
			Recipient: preflight.Recipient, DirectoryEpoch: carrier.epoch.Version,
			EvaluatedAt: dbNow,
			Evidence:    clean("entity_recipient_current", "same_tx:incoming_handoff_recipient"),
		},
		CurrentAudience: currentAudience,
	})
	if err != nil {
		return err
	}
	switch decision.Verdict {
	case VerdictUnknown:
		return directNoticeReadUnknown("incoming handoff read gate is unavailable", nil)
	case VerdictBroken:
		return incomingHandoffNotFound()
	case VerdictClean:
	default:
		return directNoticeReadUnknown("incoming handoff read gate has no verdict", nil)
	}
	if !communicationClaimsEqualSnapshot(decision.RequiredClaims,
		CommunicationClaimAuthoritySnapshot{facts: tx.claimAuthorityFacts}) ||
		len(decision.SurvivingContributionIDs) != 1 ||
		decision.SurvivingContributionIDs[0] != carrier.contributions[0].ID ||
		!equalDirectNoticeAuthorityFacts(preflight.Facts, decision.Facts) {
		return directNoticeReadUnknown(
			"incoming handoff read returned non-direct authority effects", nil,
		)
	}
	horizon, constrained, err := directNoticeReadGrantFreshUntil(
		carrier.grants, preflight.Closure, dbNow,
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

// incomingHandoffOfferContext measures an OFFERED handoff against the work it
// names, under the same locks and with the same predicate the response mutation
// uses to reject a stale offer. It is not a promise: the mutation revalidates
// version, recipient, deadline, owner/epoch, sequence and lease again.
func incomingHandoffOfferContext(
	scope DirectoryScopeRef,
	handoff Handoff,
	work handoffLockedWork,
) (IncomingHandoffOfferContext, error) {
	if handoff.State != HandoffOffered {
		return IncomingHandoffContextTerminal, nil
	}
	if len(work.item) == 0 || len(work.lease) == 0 {
		return "", directNoticeReadUnknown("incoming handoff work state is unavailable", nil)
	}
	if terminalWorkStatuses[work.item.String(colWorkStatus)] ||
		work.item.String(colWorkWorkspaceID) != scope.WorkspaceID.String() ||
		recordID(work.item) != handoff.WorkItemID ||
		work.item.String(colWorkOwnerKind) != string(handoff.From.Kind) ||
		work.item.String(colWorkOwnerRef) != handoff.From.Ref ||
		work.item.Int(colWorkOwnerEpoch) != handoff.FromOwnerEpoch ||
		work.item.Int(colWorkLastEventSeq) != handoff.ContextEventSeq+1 ||
		work.lease.String(colWorkWorkspaceID) != scope.WorkspaceID.String() ||
		work.lease.String(colWorkItemID) != handoff.WorkItemID.String() ||
		work.leaseState.Fence != handoff.OfferedLeaseFence {
		return IncomingHandoffContextStale, nil
	}
	return IncomingHandoffContextCurrent, nil
}

type incomingHandoffContentPlans struct {
	payload ProtectedPayloadOpenPlan
	reason  *ProtectedPayloadOpenPlan
}

// planIncomingHandoffContent prepares SEPARATE plans for the handoff payload and
// the terminal reason, each with its own AAD built from the LOCKED rows: tenant,
// workspace, channel, the Handoff entity kind and id, the slot schema and the
// protection generation. The envelope, the ciphertext, the key versions and the
// digests are never projected; they are inputs to the open, not substitutes for
// the content.
func planIncomingHandoffContent(
	scope DirectoryScopeRef,
	carrier handoffLockedCarrier,
	handoff Handoff,
) (incomingHandoffContentPlans, error) {
	policy := protectedPayloadPolicyFrom(carrier.message.Payload)
	aad := func(slot ProtectedPayloadSlot) (ContentAAD, error) {
		schema, ok := slot.schema()
		if !ok {
			return ContentAAD{}, directNoticeReadUnknown(
				"incoming handoff payload slot is unknown", nil,
			)
		}
		return ContentAAD{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			ChannelID: carrier.channel.ID, EntityKind: handoffKind, EntityID: handoff.ID,
			Schema: schema, ProtectionGeneration: policy.ProtectionGeneration,
		}, nil
	}
	payloadAAD, err := aad(PayloadSlotHandoff)
	if err != nil {
		return incomingHandoffContentPlans{}, err
	}
	payloadPlan, err := PlanProtectedPayloadRead(
		handoff.Payload, PayloadSlotHandoff, policy, payloadAAD, payloadAAD,
	)
	if err != nil {
		return incomingHandoffContentPlans{}, directNoticeReadUnknown(
			"incoming handoff payload cannot be opened", err,
		)
	}
	plans := incomingHandoffContentPlans{payload: payloadPlan}
	if handoff.TerminalReason == nil {
		return plans, nil
	}
	reasonAAD, err := aad(PayloadSlotHandoffTerminalReason)
	if err != nil {
		return incomingHandoffContentPlans{}, err
	}
	reasonPlan, err := PlanProtectedPayloadRead(
		*handoff.TerminalReason, PayloadSlotHandoffTerminalReason, policy, reasonAAD, reasonAAD,
	)
	if err != nil {
		return incomingHandoffContentPlans{}, directNoticeReadUnknown(
			"incoming handoff terminal reason cannot be opened", err,
		)
	}
	plans.reason = &reasonPlan
	return plans, nil
}

// openIncomingHandoffContent performs the sealer work with NO lock held and
// answers unavailable, with no partial payload, on any custody, digest or
// canonicalisation failure.
func (m *Module) openIncomingHandoffContent(
	ctx context.Context,
	authorized incomingHandoffAuthorizedRead,
	opener directNoticePayloadOpener,
) (HandoffContent, *CommunicationReasonContent, error) {
	raw, err := opener(ctx, m.communicationSealer, authorized.openPlan)
	if err != nil {
		return HandoffContent{}, nil, err
	}
	var content HandoffContent
	if err := decodeCanonicalIncomingHandoffSlot(raw, PayloadSlotHandoff, &content); err != nil {
		return HandoffContent{}, nil, err
	}
	if authorized.reasonPlan == nil {
		return content, nil, nil
	}
	rawReason, err := opener(ctx, m.communicationSealer, *authorized.reasonPlan)
	if err != nil {
		return HandoffContent{}, nil, err
	}
	var reason CommunicationReasonContent
	if err := decodeCanonicalIncomingHandoffSlot(
		rawReason, PayloadSlotHandoffTerminalReason, &reason,
	); err != nil {
		return HandoffContent{}, nil, err
	}
	return content, &reason, nil
}

func decodeCanonicalIncomingHandoffSlot(
	raw json.RawMessage,
	slot ProtectedPayloadSlot,
	target any,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return directNoticeReadUnknown("opened incoming handoff content is malformed", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return directNoticeReadUnknown("opened incoming handoff content has trailing values", err)
	}
	var canonical []byte
	var err error
	switch typed := target.(type) {
	case *HandoffContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	case *CommunicationReasonContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	default:
		return directNoticeReadUnknown("incoming handoff content slot is unsupported", nil)
	}
	if err != nil || !bytes.Equal(canonical, raw) {
		return directNoticeReadUnknown("opened incoming handoff content is not canonical", err)
	}
	return nil
}

func incomingHandoffTerminalAt(handoff Handoff) *time.Time {
	for _, value := range []*time.Time{
		handoff.AcceptedAt, handoff.RejectedAt, handoff.WithdrawnAt, handoff.ExpiredAt,
	} {
		if value != nil {
			return cloneDirectNoticeTime(value)
		}
	}
	return nil
}

func incomingHandoffNotFound() error {
	return communicationError(ErrCommunicationNotFound, "incoming handoff is not visible")
}

func normalizeIncomingHandoffReadError(err error) error {
	if errors.Is(err, ErrCommunicationEvidenceUnknown) {
		return err
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrCommunicationForbidden) {
		return incomingHandoffNotFound()
	}
	return err
}
