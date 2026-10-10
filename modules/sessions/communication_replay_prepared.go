// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrJoinedEvidenceUnprepared refuses a read inside a replay transaction that
// its owning replay did not prepare. The read would open its own store
// transaction beside the owning one, which on SQLite waits for the connection
// the owning transaction holds.
var ErrJoinedEvidenceUnprepared = fmt.Errorf(
	"%w: a joined call has no prepared evidence", ErrCommunicationEvidenceUnknown,
)

// errPreparedEvidenceMoved answers a joined read whose arguments no longer
// match what the owning replay prepared, such as a channel whose revisions
// moved in between. The owning replay prepares again once.
var errPreparedEvidenceMoved = errors.New("sessions: prepared evidence no longer matches its read")

// ProtocolReplayPlan declares what an owning replay's mutation reads, so that
// every read that opens its own store transaction runs before the owning
// transaction does.
type ProtocolReplayPlan struct {
	// Accounts are the accounts the mutation's fenced writes name. Their
	// standing is read before the transaction, which pins them first.
	Accounts []model.ID
	// Nested are the claims of the plain replays the mutation joins. When one
	// is already settled, the mutation only reloads, so nothing is fenced or
	// prepared.
	Nested []ProtocolReplayClaim
	// Participants are the work owners the mutation's commands resolve.
	Participants []ProtocolReplayParticipant
	// Publishes are the Messages the mutation publishes.
	Publishes []ProtocolReplayPublish
}

// ProtocolReplayParticipant names a work owner as a command resolves it.
type ProtocolReplayParticipant struct {
	WorkspaceID model.ID
	Kind        string
	Ref         string
}

// ProtocolReplayPublish names a Message publish by everything its directory
// evidence depends on. Its WorkItem, content and digest are read and built
// inside the owning transaction.
type ProtocolReplayPublish struct {
	Route       ProtocolInterruptRoute
	Kind        MessageKind
	SourceKind  ChannelRouteSourceKind
	SourceEvent string
	Urgency     MessageUrgency
	AckRequired bool
}

// ProtocolInterruptPublish is the publish RecordProtocolInterrupt makes for
// each request on route.
func ProtocolInterruptPublish(route ProtocolInterruptRoute) ProtocolReplayPublish {
	return ProtocolReplayPublish{
		Route: route, Kind: MessageWorkTask, SourceKind: RouteSourceUserMessage, Urgency: UrgencyHigh,
	}
}

// ProtocolReplyPublish is the publish ProjectProtocolReply makes for a reply
// of flow on route.
func ProtocolReplyPublish(route ProtocolInterruptRoute, flow ProtocolReplyFlow) ProtocolReplayPublish {
	_, _, _, sourceEvent := protocolReplyOperation(flow)
	return ProtocolReplayPublish{
		Route: route, Kind: MessageNotice, SourceKind: RouteSourceProtocol,
		SourceEvent: sourceEvent, Urgency: UrgencyNormal,
	}
}

// preparedReplay is what an owning prepared replay carries into its
// transaction: the accounts it fenced and pinned, and the evidence it read.
// An empty one, for a replay found settled, prepares nothing.
type preparedReplay struct {
	accounts map[model.ID]bool
	evidence *preparedEvidence
}

func (p *preparedReplay) covers(users []model.ID) bool {
	for _, user := range users {
		if !user.IsZero() && !p.accounts[user] {
			return false
		}
	}
	return true
}

// ApplyPreparedProtocolReplay is ApplyProtocolReplay for a mutation that
// names accounts, resolves work owners or publishes. Before its transaction it
// looks for a settled guard, reads the standing of plan.Accounts and prepares
// the plan's participants and publish evidence; its transaction pins the
// accounts first, and the mutation's joined calls consume what was prepared.
// A move between preparation and transaction prepares once more; a second one
// answers ErrProtocolReplayAuthorityMoved. Inside another replay's
// transaction it refuses with ErrProtocolReplayPreparedNested: a nested
// replay stays a plain joined call whose claim the owning plan declares.
func (m *Module) ApplyPreparedProtocolReplay(
	ctx context.Context,
	tenant model.TenantID,
	claim ProtocolReplayClaim,
	plan ProtocolReplayPlan,
	mutation ProtocolReplayMutation,
) (ProtocolReplayResult, error) {
	if tenant.IsZero() || tenant.IsSystem() || mutation == nil {
		return ProtocolReplayResult{}, protocolReplayInvalid("invalid_claim")
	}
	if _, joined := protocolReplayScopeFromContext(ctx, tenant); joined {
		return ProtocolReplayResult{}, ErrProtocolReplayPreparedNested
	}
	normalized, err := normalizeProtocolReplayClaim(claim)
	if err != nil {
		return ProtocolReplayResult{}, err
	}
	claims := []normalizedProtocolReplayClaim{normalized}
	for _, nested := range plan.Nested {
		declared, err := normalizeProtocolReplayClaim(nested)
		if err != nil {
			return ProtocolReplayResult{}, err
		}
		claims = append(claims, declared)
	}
	for attempt := 0; ; attempt++ {
		settled, err := m.protocolReplaySettled(ctx, tenant, claims)
		if err != nil {
			return ProtocolReplayResult{}, classifyProtocolReplayStoreError(err)
		}
		prepared := &preparedReplay{}
		var refs []store.UserAuthorityFactRef
		if !settled {
			refs, err = auth.FenceSubjects(ctx, m.standingFor(ctx), tenant, plan.Accounts)
			if err != nil {
				return ProtocolReplayResult{}, err
			}
			prepared.accounts = make(map[model.ID]bool, len(plan.Accounts))
			for _, account := range plan.Accounts {
				prepared.accounts[account] = true
			}
			// A plan with no participant and no publish prepares nothing, so it
			// keeps no record: an evidence read inside its transaction then
			// refuses with ErrJoinedEvidenceUnprepared, as a settled replay's
			// does, instead of missing a record, which would count as a move.
			if len(plan.Participants) > 0 || len(plan.Publishes) > 0 {
				prepared.evidence = newPreparedEvidence()
				if err := m.prepareProtocolReplayEvidence(ctx, tenant, normalized.WorkspaceID, plan, prepared.evidence); err != nil {
					return ProtocolReplayResult{}, err
				}
			}
		}
		result, err := m.protocolReplayAttempt(ctx, tenant, normalized, true, prepared, refs, mutation)
		if err == nil {
			return result, nil
		}
		// A record miss is decided by the record's flag, which every miss sets,
		// whatever error the read that missed returns in its place.
		moved := protocolReplayAuthorityMoved(err) || prepared.evidence.moved()
		if attempt == 0 && (moved || errors.Is(err, store.ErrConflict)) {
			continue
		}
		if moved {
			return ProtocolReplayResult{}, fmt.Errorf("%w: %v", ErrProtocolReplayAuthorityMoved, err)
		}
		return ProtocolReplayResult{}, classifyProtocolReplayStoreError(err)
	}
}

// protocolReplayAuthorityMoved reports whether err is a move between a
// prepared replay's preparation and its transaction: its barrier conflicted,
// the channel's publish fence changed or the directory epoch moved.
func protocolReplayAuthorityMoved(err error) bool {
	return auth.FenceMoved(err) || errors.Is(err, errChannelPublishFenceChanged) ||
		errors.Is(err, ErrCommunicationSnapshotStale)
}

// protocolReplaySettled reports, in one read-only view, whether any of claims
// already has its guard. A settled owning claim answers an exact replay and a
// settled nested claim makes its replay only reload, so neither needs a fence
// or evidence.
func (m *Module) protocolReplaySettled(
	ctx context.Context,
	tenant model.TenantID,
	claims []normalizedProtocolReplayClaim,
) (bool, error) {
	settled := false
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolReplayGuardKind)
		if err != nil {
			return err
		}
		for _, claim := range claims {
			_, found, err := findProtocolReplayGuard(ctx, repo, claim)
			if err != nil {
				return err
			}
			if found {
				settled = true
				return nil
			}
		}
		return nil
	})
	return settled, err
}

// joinedFence answers, for a fenced write inside a replay transaction,
// whether its owning prepared replay already read the standing of users and
// pinned them. A replay that did not name them refuses the write with
// ErrJoinedEvidenceUnprepared: reading their standing now would open a store
// transaction beside the owning one, which on SQLite waits for the connection
// that transaction holds. A plain replay prepared nothing, so it refuses any
// write that names an account. Outside a replay the write fences itself.
func joinedFence(ctx context.Context, tenant model.TenantID, users []model.ID) (bool, error) {
	prepared, joined := joinedPreparedReplay(ctx, tenant)
	if !joined {
		return false, nil
	}
	if prepared == nil {
		for _, user := range users {
			if !user.IsZero() {
				return false, ErrJoinedEvidenceUnprepared
			}
		}
		return false, nil
	}
	if !prepared.covers(users) {
		return false, ErrJoinedEvidenceUnprepared
	}
	return true, nil
}

// joinedPreparedReplay returns the prepared replay carried by the replay
// transaction open for tenant on ctx, and whether one is open at all.
func joinedPreparedReplay(ctx context.Context, tenant model.TenantID) (*preparedReplay, bool) {
	if ctx == nil {
		return nil, false
	}
	joined, ok := ctx.Value(protocolReplayTransactionContextKey{}).(*protocolReplayTransactionContext)
	if !ok || joined == nil || joined.scope == nil || joined.tenant != tenant || !joined.active.Load() {
		return nil, false
	}
	return joined.prepared, true
}

// prepareProtocolReplayEvidence reads, before the owning transaction, every
// participant and publish evidence plan declares, by running the reads the
// mutation will make through ports that record their answers.
func (m *Module) prepareProtocolReplayEvidence(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	plan ProtocolReplayPlan,
	record *preparedEvidence,
) error {
	recordCtx := context.WithValue(ctx, preparingEvidenceKey{}, record)
	if m.WorkIdentity != nil {
		for _, participant := range plan.Participants {
			// The answer, a refusal included, is what checkParticipant reads
			// inside the transaction and maps as it does today.
			_, _ = m.participantResolverFor(recordCtx, tenant).ResolveParticipant(
				recordCtx, tenant, participant.WorkspaceID, participant.Kind, participant.Ref,
			)
		}
	}
	for _, publish := range plan.Publishes {
		if err := m.prepareWorkflowCommunicationEvidence(recordCtx, tenant, workspace, publish); err != nil {
			return workflowCommunicationError("prepare protocol publish", err)
		}
	}
	return nil
}

// preparedEvidenceContent is the content a prepared publish's evidence is read
// with. No evidence read depends on content: the real content, its payload
// and its digest are built inside the owning transaction.
var preparedEvidenceContent = MessageContent{Subject: "Prepared evidence", Blocks: []MessageContentBlock{{
	Type: ContentBlockText, Format: TextPlain, Text: "Directory evidence read before the owning transaction.",
}}}

const preparedEvidenceCommandScope = "workflow.message.prepared-evidence"

// prepareWorkflowCommunicationEvidence runs a workflow publish's preparation
// for publish in workspace with a recording context. It reads the channel but
// no WorkItem, which the mutation may create inside the owning transaction.
func (m *Module) prepareWorkflowCommunicationEvidence(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	publish ProtocolReplayPublish,
) error {
	route, _, err := publish.Route.normalize()
	if err != nil {
		return err
	}
	target, err := m.workflowCommunicationChannelScope(ctx, tenant, workspace, route.ChannelID)
	if err != nil {
		return err
	}
	var ackDueAt *time.Time
	if publish.AckRequired {
		due := m.clock.Now().Time().Add(time.Hour)
		ackDueAt = &due
	}
	_, err = m.prepareWorkflowCommunicationPublishTarget(
		ctx, tenant, target, workflowProtocolUserActor(route.SenderUserID), model.NewID(), route.ChannelID,
		RecipientRef{Kind: RecipientUser, Ref: route.RecipientUserID.String()},
		preparedEvidenceContent, publish.Urgency, ackDueAt, "prepared-evidence:"+model.NewID().String(),
		publish.Kind, preparedEvidenceCommandScope, publish.SourceKind, publish.SourceEvent,
	)
	return err
}

// preparingEvidenceKey marks a context whose evidence reads are recorded for
// a prepared replay.
type preparingEvidenceKey struct{}

func preparingEvidence(ctx context.Context) *preparedEvidence {
	if ctx == nil {
		return nil
	}
	record, _ := ctx.Value(preparingEvidenceKey{}).(*preparedEvidence)
	return record
}

type preparedPrincipalKey struct {
	scope     DirectoryScopeRef
	principal CommunicationPrincipal
}

type preparedOperationKey struct {
	principal CommunicationPrincipal
	entity    EntityRef
	operation CommunicationOperation
}

type preparedParticipantKey struct {
	tenant    model.TenantID
	workspace model.ID
	kind      string
	ref       string
}

type preparedAttestation struct {
	requestedAt time.Time
	snapshot    DirectorySnapshot
	attestation PublicationAudienceAttestation
}

type preparedParticipant struct {
	participant Participant
	err         error
}

// preparedEvidence records the answers of the evidence reads a prepared
// replay made before its transaction, keyed by each read's arguments.
type preparedEvidence struct {
	mu           sync.Mutex
	closures     map[preparedPrincipalKey]ChannelGrantSubjectClosure
	principals   map[preparedPrincipalKey]PrincipalResolution
	operations   map[preparedOperationKey]ReadWitness
	attestations map[string]preparedAttestation
	participants map[preparedParticipantKey]preparedParticipant
	missed       bool
}

func newPreparedEvidence() *preparedEvidence {
	return &preparedEvidence{
		closures:     map[preparedPrincipalKey]ChannelGrantSubjectClosure{},
		principals:   map[preparedPrincipalKey]PrincipalResolution{},
		operations:   map[preparedOperationKey]ReadWitness{},
		attestations: map[string]preparedAttestation{},
		participants: map[preparedParticipantKey]preparedParticipant{},
	}
}

// moved reports whether a joined read found no prepared answer for its
// arguments. A nil record prepared nothing and never moves.
func (e *preparedEvidence) moved() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.missed
}

func (e *preparedEvidence) miss() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.missed = true
	return errPreparedEvidenceMoved
}

// preparedAttestationKey keys an audience request by everything but the time
// it was requested at, which the joined request takes from the record.
func preparedAttestationKey(request PublicationAudienceRequest) (string, bool) {
	request.RequestedAt = time.Time{}
	encoded, err := canonicalJSON(request)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func (e *preparedEvidence) attestation(request PublicationAudienceRequest) (preparedAttestation, bool) {
	key, ok := preparedAttestationKey(request)
	if !ok {
		return preparedAttestation{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	prepared, found := e.attestations[key]
	return prepared, found
}

// communicationEvidencePorts are the evidence ports a publish or read
// preflight calls. Preparing a replay, they record; inside a prepared replay,
// they answer from the record; elsewhere they are the module's ports.
type communicationEvidencePorts struct {
	closure   ChannelGrantSubjectClosureResolver
	attestor  PublicationAudienceAttestor
	directory DirectorySnapshotResolver
	operation CoreEntityOperationAuthorizer
	record    *preparedEvidence
	replaying bool
}

// communicationEvidencePorts selects the ports for ctx. Inside a replay
// transaction with no prepared record, a publish (failUnprepared) refuses
// every read with ErrJoinedEvidenceUnprepared, and any other caller keeps the
// module's ports.
func (m *Module) communicationEvidencePorts(
	ctx context.Context,
	tenant model.TenantID,
	failUnprepared bool,
) communicationEvidencePorts {
	direct := communicationEvidencePorts{
		closure: m.CommunicationGrantClosure, attestor: m.CommunicationAudienceAttestor,
		directory: m.CommunicationDirectoryResolver, operation: m.CommunicationOperationAuthorizer,
	}
	if record := preparingEvidence(ctx); record != nil {
		return communicationEvidencePorts{
			closure:   recordingClosure{next: direct.closure, record: record},
			attestor:  recordingAttestor{next: direct.attestor, record: record},
			directory: recordingDirectory{next: direct.directory, record: record},
			operation: recordingOperations{next: direct.operation, record: record},
			record:    record,
		}
	}
	prepared, joined := joinedPreparedReplay(ctx, tenant)
	if !joined {
		return direct
	}
	if prepared != nil && prepared.evidence != nil {
		replay := replayingEvidence{record: prepared.evidence}
		return communicationEvidencePorts{
			closure: replay, attestor: replay, directory: replay, operation: replay,
			record: prepared.evidence, replaying: true,
		}
	}
	if prepared == nil && !failUnprepared {
		return direct
	}
	var refuse unpreparedEvidence
	return communicationEvidencePorts{closure: refuse, attestor: refuse, directory: refuse, operation: refuse}
}

// audienceRequestedAt is the time request is attested at. Inside a prepared
// replay it is the time its prepared attestation was requested, which the
// attestation's snapshot must not precede.
func (p communicationEvidencePorts) audienceRequestedAt(request PublicationAudienceRequest) time.Time {
	if !p.replaying || p.record == nil {
		return request.RequestedAt
	}
	if prepared, ok := p.record.attestation(request); ok {
		return prepared.requestedAt
	}
	return request.RequestedAt
}

type participantResolver interface {
	ResolveParticipant(context.Context, model.TenantID, model.ID, string, string) (Participant, error)
}

// participantResolverFor selects the participant port for ctx as
// communicationEvidencePorts does. A replay found settled prepared nothing,
// so its joined resolution refuses instead of waiting.
func (m *Module) participantResolverFor(ctx context.Context, tenant model.TenantID) participantResolver {
	if record := preparingEvidence(ctx); record != nil {
		return recordingParticipants{next: m.WorkIdentity, record: record}
	}
	if prepared, joined := joinedPreparedReplay(ctx, tenant); joined && prepared != nil {
		if prepared.evidence == nil {
			return unpreparedEvidence{}
		}
		return replayingEvidence{record: prepared.evidence}
	}
	return m.WorkIdentity
}

type recordingClosure struct {
	next   ChannelGrantSubjectClosureResolver
	record *preparedEvidence
}

func (r recordingClosure) ResolveChannelGrantSubjects(
	ctx context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	closure, err := r.next.ResolveChannelGrantSubjects(ctx, scope, principal)
	if err == nil {
		r.record.mu.Lock()
		r.record.closures[preparedPrincipalKey{scope: scope, principal: principal}] = closure
		r.record.mu.Unlock()
	}
	return closure, err
}

type recordingAttestor struct {
	next   PublicationAudienceAttestor
	record *preparedEvidence
}

func (r recordingAttestor) AttestPublicationAudience(
	ctx context.Context, request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	snapshot, attestation, err := r.next.AttestPublicationAudience(ctx, request)
	if err == nil {
		if key, ok := preparedAttestationKey(request); ok {
			r.record.mu.Lock()
			r.record.attestations[key] = preparedAttestation{
				requestedAt: request.RequestedAt, snapshot: snapshot, attestation: attestation,
			}
			r.record.mu.Unlock()
		}
	}
	return snapshot, attestation, err
}

type recordingDirectory struct {
	next   DirectorySnapshotResolver
	record *preparedEvidence
}

func (r recordingDirectory) ResolveAudience(
	ctx context.Context, scope DirectoryScopeRef, selectors []AudienceSelector,
) (DirectorySnapshot, error) {
	return r.next.ResolveAudience(ctx, scope, selectors)
}

func (r recordingDirectory) ResolveRecipient(
	ctx context.Context, scope DirectoryScopeRef, recipient RecipientRef,
) (RecipientSnapshot, error) {
	return r.next.ResolveRecipient(ctx, scope, recipient)
}

func (r recordingDirectory) ResolvePrincipal(
	ctx context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal,
) (PrincipalResolution, error) {
	resolution, err := r.next.ResolvePrincipal(ctx, scope, principal)
	if err == nil {
		r.record.mu.Lock()
		r.record.principals[preparedPrincipalKey{scope: scope, principal: principal}] = resolution
		r.record.mu.Unlock()
	}
	return resolution, err
}

type recordingOperations struct {
	next   CoreEntityOperationAuthorizer
	record *preparedEvidence
}

func (r recordingOperations) AuthorizeEntityOperation(
	ctx context.Context, principal CommunicationPrincipal, entity EntityRef, operation CommunicationOperation,
) (ReadWitness, error) {
	witness, err := r.next.AuthorizeEntityOperation(ctx, principal, entity, operation)
	if err == nil {
		r.record.mu.Lock()
		r.record.operations[preparedOperationKey{principal: principal, entity: entity, operation: operation}] = witness
		r.record.mu.Unlock()
	}
	return witness, err
}

type recordingParticipants struct {
	next   WorkIdentityResolver
	record *preparedEvidence
}

func (r recordingParticipants) ResolveParticipant(
	ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (Participant, error) {
	participant, err := r.next.ResolveParticipant(ctx, tenant, workspace, kind, ref)
	r.record.mu.Lock()
	r.record.participants[preparedParticipantKey{tenant: tenant, workspace: workspace, kind: kind, ref: ref}] =
		preparedParticipant{participant: participant, err: err}
	r.record.mu.Unlock()
	return participant, err
}

// replayingEvidence answers a joined read from its owning replay's record. A
// read with other arguments than prepared is a move; a read of a kind the
// record never holds refuses as unprepared.
type replayingEvidence struct {
	record *preparedEvidence
}

func (r replayingEvidence) ResolveChannelGrantSubjects(
	_ context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	r.record.mu.Lock()
	closure, ok := r.record.closures[preparedPrincipalKey{scope: scope, principal: principal}]
	r.record.mu.Unlock()
	if !ok {
		return ChannelGrantSubjectClosure{}, r.record.miss()
	}
	return closure, nil
}

func (r replayingEvidence) AttestPublicationAudience(
	_ context.Context, request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	prepared, ok := r.record.attestation(request)
	if !ok || !prepared.requestedAt.Equal(request.RequestedAt) {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, r.record.miss()
	}
	return prepared.snapshot, prepared.attestation, nil
}

func (r replayingEvidence) ResolveAudience(
	context.Context, DirectoryScopeRef, []AudienceSelector,
) (DirectorySnapshot, error) {
	return DirectorySnapshot{}, ErrJoinedEvidenceUnprepared
}

func (r replayingEvidence) ResolveRecipient(
	context.Context, DirectoryScopeRef, RecipientRef,
) (RecipientSnapshot, error) {
	return RecipientSnapshot{}, ErrJoinedEvidenceUnprepared
}

func (r replayingEvidence) ResolvePrincipal(
	_ context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal,
) (PrincipalResolution, error) {
	r.record.mu.Lock()
	resolution, ok := r.record.principals[preparedPrincipalKey{scope: scope, principal: principal}]
	r.record.mu.Unlock()
	if !ok {
		return PrincipalResolution{}, r.record.miss()
	}
	return resolution, nil
}

func (r replayingEvidence) AuthorizeEntityOperation(
	_ context.Context, principal CommunicationPrincipal, entity EntityRef, operation CommunicationOperation,
) (ReadWitness, error) {
	r.record.mu.Lock()
	witness, ok := r.record.operations[preparedOperationKey{principal: principal, entity: entity, operation: operation}]
	r.record.mu.Unlock()
	if !ok {
		return ReadWitness{}, r.record.miss()
	}
	return witness, nil
}

func (r replayingEvidence) ResolveParticipant(
	_ context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (Participant, error) {
	r.record.mu.Lock()
	prepared, ok := r.record.participants[preparedParticipantKey{tenant: tenant, workspace: workspace, kind: kind, ref: ref}]
	r.record.mu.Unlock()
	if !ok {
		return Participant{}, ErrJoinedEvidenceUnprepared
	}
	return prepared.participant, prepared.err
}

// unpreparedEvidence refuses every read of a joined call its owning replay
// did not prepare.
type unpreparedEvidence struct{}

func (unpreparedEvidence) ResolveChannelGrantSubjects(
	context.Context, DirectoryScopeRef, CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	return ChannelGrantSubjectClosure{}, ErrJoinedEvidenceUnprepared
}

func (unpreparedEvidence) AttestPublicationAudience(
	context.Context, PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	return DirectorySnapshot{}, PublicationAudienceAttestation{}, ErrJoinedEvidenceUnprepared
}

func (unpreparedEvidence) ResolveAudience(
	context.Context, DirectoryScopeRef, []AudienceSelector,
) (DirectorySnapshot, error) {
	return DirectorySnapshot{}, ErrJoinedEvidenceUnprepared
}

func (unpreparedEvidence) ResolveRecipient(
	context.Context, DirectoryScopeRef, RecipientRef,
) (RecipientSnapshot, error) {
	return RecipientSnapshot{}, ErrJoinedEvidenceUnprepared
}

func (unpreparedEvidence) ResolvePrincipal(
	context.Context, DirectoryScopeRef, CommunicationPrincipal,
) (PrincipalResolution, error) {
	return PrincipalResolution{}, ErrJoinedEvidenceUnprepared
}

func (unpreparedEvidence) AuthorizeEntityOperation(
	context.Context, CommunicationPrincipal, EntityRef, CommunicationOperation,
) (ReadWitness, error) {
	return ReadWitness{}, ErrJoinedEvidenceUnprepared
}

func (unpreparedEvidence) ResolveParticipant(
	context.Context, model.TenantID, model.ID, string, string,
) (Participant, error) {
	return Participant{}, ErrJoinedEvidenceUnprepared
}
