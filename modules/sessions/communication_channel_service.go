// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	communicationChannelCreateAudit = "sessions.communication.channel.create"
	communicationChannelUpdateAudit = "sessions.communication.channel.update"
	communicationChannelGrantAudit  = "sessions.communication.channel.grant"
	communicationChannelRevokeAudit = "sessions.communication.channel.revoke"
)

var errCommunicationChannelVersionRequired = errors.New(
	"sessions: Channel version_required",
)

type ChannelGrantInput struct {
	Subject   CommunicationSubjectRef `json:"subject"`
	CanRead   bool                    `json:"can_read"`
	CanWrite  bool                    `json:"can_write"`
	CanAdmin  bool                    `json:"can_admin"`
	ExpiresAt *time.Time              `json:"expires_at,omitempty"`
}

type ChannelCreateCommand struct {
	Slug                string              `json:"slug"`
	Name                string              `json:"name"`
	Description         string              `json:"description,omitempty"`
	Kind                ChannelKind         `json:"kind"`
	Sensitivity         ChannelSensitivity  `json:"sensitivity"`
	ContentProtection   ContentProtection   `json:"content_protection"`
	DefaultAckPolicy    AckPolicy           `json:"default_ack_policy"`
	DefaultAckTimeoutMS int64               `json:"default_ack_timeout_ms"`
	DefaultWake         WakePolicy          `json:"default_wake"`
	RetentionPolicyRef  string              `json:"retention_policy_ref,omitempty"`
	MaxFanout           int64               `json:"max_fanout"`
	MaxAutomationDepth  int64               `json:"max_automation_depth"`
	InitialGrants       []ChannelGrantInput `json:"initial_grants"`
}

type ChannelUpdateCommand struct {
	ChannelID           model.ID            `json:"channel_id"`
	Name                *string             `json:"name,omitempty"`
	Description         *string             `json:"description,omitempty"`
	State               *ChannelState       `json:"state,omitempty"`
	Sensitivity         *ChannelSensitivity `json:"sensitivity,omitempty"`
	ContentProtection   *ContentProtection  `json:"content_protection,omitempty"`
	DefaultAckPolicy    *AckPolicy          `json:"default_ack_policy,omitempty"`
	DefaultAckTimeoutMS *int64              `json:"default_ack_timeout_ms,omitempty"`
	DefaultWake         *WakePolicy         `json:"default_wake,omitempty"`
	RetentionPolicyRef  *string             `json:"retention_policy_ref,omitempty"`
	MaxFanout           *int64              `json:"max_fanout,omitempty"`
	MaxAutomationDepth  *int64              `json:"max_automation_depth,omitempty"`
	IfMatch             string              `json:"-"`
}

type ChannelGrantCommand struct {
	ChannelID model.ID          `json:"-"`
	Grant     ChannelGrantInput `json:"grant"`
	IfMatch   string            `json:"-"`
}

type ChannelGrantRevokeCommand struct {
	ChannelID model.ID `json:"-"`
	GrantID   model.ID `json:"-"`
	IfMatch   string   `json:"-"`
}

type ChannelMutationResult struct {
	Channel  Channel        `json:"channel"`
	Grant    *ChannelGrant  `json:"grant,omitempty"`
	Grants   []ChannelGrant `json:"grants,omitempty"`
	ETag     string         `json:"etag"`
	AuditSeq int64          `json:"audit_seq"`
}

// GetChannel returns channel metadata only after the authenticated principal's
// exact core channel:read decision and current local ChannelGrant.read closure
// have both been pinned in one transaction. Local denial is concealed so the
// route does not become a channel-enumeration oracle.
func (m *Module) GetChannel(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	channelID model.ID,
) (Channel, error) {
	if !validCanonicalCommunicationID(channelID) {
		return Channel{}, directNoticeReadNotFound("Channel is not visible")
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, channelID, CommunicationRead,
	)
	if err != nil {
		return Channel{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return Channel{}, normalizeDirectNoticePointReadError(err)
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return Channel{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"channel-read authority context crossed its exact request",
		)
	}
	readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
	if readinessErr != nil || !readiness.Effective {
		return Channel{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
		)
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return Channel{}, normalizeDirectNoticePointReadError(err)
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return Channel{}, err
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(identity.Principal),
	)
	if err != nil {
		return Channel{}, err
	}
	var result Channel
	var hidden bool
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
				"sessions_channel_read|%s|%s|%s", scope.TenantID, scope.WorkspaceID, channelID,
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
			if err != nil || channel.TenantID != scope.TenantID ||
				channel.WorkspaceID != scope.WorkspaceID {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "locked Channel is malformed",
				)
			}
			grants, err := lockCurrentChannelGrants(ctx, tx, channelID)
			if err != nil {
				return err
			}
			epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
			if err != nil || epoch.Validate() != nil || epoch.TenantID != scope.TenantID ||
				epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "locked channel directory epoch is unavailable",
				)
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			grant := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
				Verdict: VerdictClean, Code: "channel_grants_locked",
				ACLRevision: channel.ACLRevision, ObservedAt: tx.now.Time(), Grants: grants,
			}, scope.TenantID, scope.WorkspaceID, channelID, preflight.Closure,
				ChannelGrantRead, tx.now.Time())
			switch evidenceVerdict(grant.Evidence) {
			case VerdictClean:
				if deadline, constrained, horizonErr := directNoticeReadGrantFreshUntil(
					grants, preflight.Closure, tx.now.Time(),
				); horizonErr != nil {
					return horizonErr
				} else if constrained {
					if err := tx.narrowRequestAuthorityFreshUntil(deadline); err != nil {
						return err
					}
				}
				result = channel
				return nil
			case VerdictBroken:
				hidden = true
				return nil
			default:
				return communicationError(
					ErrCommunicationEvidenceUnknown, "channel read grant is unavailable",
				)
			}
		},
	)
	if err != nil {
		return Channel{}, normalizeDirectNoticePointReadError(err)
	}
	if hidden {
		return Channel{}, directNoticeReadNotFound("Channel is not visible")
	}
	return result, nil
}

func (m *Module) CreateChannel(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd ChannelCreateCommand,
) (ChannelMutationResult, error) {
	readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
	if readinessErr != nil || !readiness.Effective {
		return ChannelMutationResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
		)
	}
	channelID := model.NewID()
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, channelID, CommunicationChannelWrite,
	)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	recipient, _, err := m.communicationPrincipalRecipient(ctx, scope, inspected.principal)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	actor, err := communicationActorForRecipient(recipient)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	if len(cmd.InitialGrants) == 0 || len(cmd.InitialGrants) > 64 {
		return ChannelMutationResult{}, communicationError(
			ErrInvalidCommunicationModel, "channel requires explicit bounded initial grants",
		)
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(inspected.principal),
	)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	var result ChannelMutationResult
	err = m.mutateCommunicationWithAuthority(
		ctx, question, bound, claims,
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			if err := tx.lockTransaction(ctx, fmt.Sprintf(
				"sessions_channel_create|%s|%s|%s", scope.TenantID, scope.WorkspaceID, cmd.Slug,
			)); err != nil {
				return err
			}
			routeGuard, err := lockCommunicationGuardByKind(
				ctx, tx, CommunicationGuardRouteRevision,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			routePlan, err := PlanCommunicationGuardAdvance(routeGuard, 1, tx.now.Time())
			if err != nil {
				return err
			}
			channel := Channel{
				MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
					ID: channelID, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
					Version: 1, CreatedAt: tx.now.Time(),
				}, UpdatedAt: tx.now.Time()},
				Slug: cmd.Slug, Name: cmd.Name, Description: cmd.Description, Kind: cmd.Kind,
				State: ChannelActive, Sensitivity: cmd.Sensitivity,
				ContentProtection: cmd.ContentProtection, ProtectionGeneration: 1,
				DefaultAckPolicy: cmd.DefaultAckPolicy, DefaultAckTimeoutMS: cmd.DefaultAckTimeoutMS,
				DefaultWake: cmd.DefaultWake, RetentionPolicyRef: cmd.RetentionPolicyRef,
				MaxFanout: cmd.MaxFanout, MaxAutomationDepth: cmd.MaxAutomationDepth,
				ACLRevision: 1, RouteRevision: routePlan.AllocatedSeq[0], SubscriptionRevision: 1,
			}
			if err := ValidateChannel(channel); err != nil {
				return err
			}
			record, err := channelToRecord(channel)
			if err != nil {
				return err
			}
			if _, err = tx.createWithID(ctx, channelKind, channel.ID, record); err != nil {
				return err
			}
			guardRecord, err := communicationGuardToRecord(routePlan.After)
			if err != nil {
				return err
			}
			guardRecord[model.ColVersion] = routePlan.Before.Version
			if _, err = tx.update(ctx, communicationGuardKind, guardRecord); err != nil {
				return err
			}
			grants := make([]ChannelGrant, 0, len(cmd.InitialGrants))
			seen := make(map[CommunicationSubjectRef]struct{}, len(cmd.InitialGrants))
			for _, input := range cmd.InitialGrants {
				if _, duplicate := seen[input.Subject]; duplicate {
					return communicationError(ErrInvalidCommunicationModel, "initial grant subject is repeated")
				}
				seen[input.Subject] = struct{}{}
				grant := newChannelGrant(scope, channel.ID, actor, input, 1, "", tx.now.Time())
				grantRecord, encodeErr := channelGrantToRecord(grant)
				if encodeErr != nil {
					return encodeErr
				}
				if _, createErr := tx.createWithID(ctx, channelGrantKind, grant.ID, grantRecord); createErr != nil {
					return createErr
				}
				grants = append(grants, grant)
			}
			payload, err := canonicalJSON(struct {
				ChannelID  model.ID `json:"channel_id"`
				GrantCount int      `json:"grant_count"`
			}{ChannelID: channel.ID, GrantCount: len(grants)})
			if err != nil {
				return err
			}
			hash := sha256.Sum256(payload)
			_, auditKind := communicationAuditActor(actor)
			audit, err := tx.appendAudit(ctx, model.AuditDraft{
				Actor: directNoticeActor(inspected.principal), ActorKind: auditKind,
				Action: communicationChannelCreateAudit, TargetKind: channelKind,
				TargetID: channel.ID, PayloadHash: hash[:],
				Meta: map[string]any{"workspace_id": scope.WorkspaceID.String(), "grant_count": len(grants)},
			})
			if err != nil || audit.Seq < 1 {
				return communicationError(ErrCommunicationEvidenceUnknown, "channel audit append failed")
			}
			result = ChannelMutationResult{
				Channel: channel, Grants: grants, ETag: communicationVersionETag(channel.Version),
				AuditSeq: audit.Seq,
			}
			return nil
		},
	)
	return result, err
}

func newChannelGrant(
	scope DirectoryScopeRef,
	channelID model.ID,
	actor CommunicationActorRef,
	input ChannelGrantInput,
	generation int64,
	supersedes model.ID,
	now time.Time,
) ChannelGrant {
	return ChannelGrant{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: model.NewID(), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			Version: 1, CreatedAt: now,
		}, UpdatedAt: now},
		ChannelID: channelID, Subject: input.Subject, Generation: generation,
		CanRead: input.CanRead, CanWrite: input.CanWrite, CanAdmin: input.CanAdmin,
		State: ChannelGrantActive, GrantedBy: actor, ExpiresAt: input.ExpiresAt,
		SupersedesID: supersedes,
	}
}

const (
	// channelAdminSubjectRowBound is how many rows ONE exact per-subject
	// ChannelGrant query reads. A legal history holds at most one persisted
	// active generation for a subject on a Channel, and exactly one row at that
	// subject's highest generation, because sessions_channel_grant_uniq is
	// UNIQUE on (tenant_id, channel_id, subject_kind, subject_ref, generation).
	// Reading two rows is therefore enough to SEE an ambiguity and refuse it by
	// name instead of resolving it by luck.
	channelAdminSubjectRowBound = 2
)

// listChannelGrantRows runs ONE exact ChannelGrant query inside the mutation
// transaction, always confined to the Channel the transaction has already
// locked, and decodes every returned row against that Channel's tenant,
// workspace and ID.
//
// It is the reason an administrative mutation no longer depends on how many
// generations a Channel has accumulated: every caller below names the exact
// rows its own semantics need — one subject, one addressed ID, the caller's own
// closure — so the rows an engine examines scale with the request, never with
// the history behind it. The bound is a page size the caller declares AND
// checks: `more` reports that the engine had further rows, so a caller that
// cannot legally have more refuses by name and nothing is silently truncated.
func listChannelGrantRows(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	channelID model.ID,
	filters []model.Filter,
	order []model.Sort,
	bound int,
) (grants []ChannelGrant, more bool, err error) {
	if bound < 1 || !validCanonicalCommunicationID(channelID) {
		return nil, false, communicationError(
			ErrCommunicationEvidenceUnknown, "ChannelGrant selection is not bounded to a Channel",
		)
	}
	repo, err := tx.repo(channelGrantKind)
	if err != nil {
		return nil, false, err
	}
	query := model.Query{
		Filters: append([]model.Filter{
			{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
		}, filters...),
		Sort:  append([]model.Sort(nil), order...),
		Limit: bound,
	}
	rows, page, err := repo.List(ctx, query)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > bound {
		return nil, false, communicationError(
			ErrCommunicationEvidenceUnknown, "ChannelGrant selection returned more rows than it bounded",
		)
	}
	selected := make([]ChannelGrant, 0, len(rows))
	seen := make(map[model.ID]struct{}, len(rows))
	for _, row := range rows {
		grant, decodeErr := channelGrantFromRecord(row)
		if decodeErr != nil {
			return nil, false, communicationError(
				ErrCommunicationEvidenceUnknown, "selected ChannelGrant row is malformed",
			)
		}
		if validateErr := validateChannelGrantSnapshotRow(
			grant, scope.TenantID, scope.WorkspaceID, channelID,
		); validateErr != nil {
			return nil, false, communicationError(
				ErrCommunicationEvidenceUnknown, "selected ChannelGrant row left its Channel or scope",
			)
		}
		if _, duplicate := seen[grant.ID]; duplicate {
			return nil, false, communicationError(
				ErrCommunicationEvidenceUnknown, "ChannelGrant selection repeats a row",
			)
		}
		seen[grant.ID] = struct{}{}
		selected = append(selected, grant)
	}
	return selected, page.HasMore, nil
}

// lockSelectedChannelGrants locks the exact rows an administrative mutation
// selected, in ascending ID order — the discipline every other communication
// row-set lock follows, so two transactions can never take two of these rows in
// opposite order — and re-corroborates each LOCKED copy against the Channel and
// the scope, because a lock may have waited.
//
// Only rows a bounded query already proved to belong to the locked Channel
// reach this function, so a transaction never fences a row of a Channel it does
// not hold; that is what keeps the lock graph acyclic now that the row set is
// chosen by the request instead of by the Channel.
func lockSelectedChannelGrants(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	channelID model.ID,
	selected ...[]ChannelGrant,
) (map[model.ID]ChannelGrant, error) {
	ids := make([]model.ID, 0)
	seen := make(map[model.ID]struct{})
	for _, group := range selected {
		for _, grant := range group {
			if grant.ChannelID != channelID || !validCanonicalCommunicationID(grant.ID) {
				return nil, communicationError(
					ErrCommunicationEvidenceUnknown, "selected ChannelGrant is not a row of this Channel",
				)
			}
			if _, duplicate := seen[grant.ID]; duplicate {
				continue
			}
			seen[grant.ID] = struct{}{}
			ids = append(ids, grant.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	locked := make(map[model.ID]ChannelGrant, len(ids))
	for _, id := range ids {
		record, lockErr := tx.lockRecord(ctx, channelGrantKind, id)
		if errors.Is(lockErr, store.ErrNotFound) {
			// A ChannelGrant row is never deleted, so this is a stale selection
			// rather than an absence: it is the caller's view that is out of date.
			return nil, fmt.Errorf("%w: selected ChannelGrant is no longer present", store.ErrConflict)
		}
		if lockErr != nil {
			return nil, lockErr
		}
		grant, decodeErr := channelGrantFromRecord(record)
		if decodeErr != nil || grant.ID != id {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "locked ChannelGrant row is malformed",
			)
		}
		if err := validateChannelGrantSnapshotRow(
			grant, scope.TenantID, scope.WorkspaceID, channelID,
		); err != nil {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "locked ChannelGrant row left its Channel or scope",
			)
		}
		locked[id] = grant
	}
	return locked, nil
}

// currentChannelGrantSubjectFilters is the ONE exact predicate both current-row
// questions ask: the persisted-active generations one subject holds on the
// Channel this transaction has locked. The caller's authority asks it about each
// of its own closure subjects; the grant asks it about the subject it is about
// to serve.
//
// The admin BIT is deliberately not a predicate here. Selecting the subject's
// current row and letting EvaluateCurrentChannelGrant apply the bit keeps both
// questions on one index and one shape, and it makes the ambiguity this
// selection refuses the right one: two current generations for one subject is
// broken whichever bits they carry.
func currentChannelGrantSubjectFilters(subject CommunicationSubjectRef) []model.Filter {
	return []model.Filter{
		{Column: colCommSubjectKind, Op: model.OpEq, Value: string(subject.Kind)},
		{Column: colCommSubjectRef, Op: model.OpEq, Value: subject.Ref},
		{Column: colCommState, Op: model.OpEq, Value: string(ChannelGrantActive)},
	}
}

// currentChannelGrantSubjectOrder is the ordering that keeps the predicate above
// bounded on BOTH engines. Ordering by generation is not cosmetic: with the
// store's default `ORDER BY id`, PostgreSQL answered the question from an
// ordered primary-key scan — because a primary-key scan satisfies that ordering
// — and discarded 2 199 of 2 200 rows for the one subject that owned most of the
// relation. No index can prevent that substitution; removing the ordering it
// substitutes for does.
func currentChannelGrantSubjectOrder() []model.Sort {
	return []model.Sort{
		{Column: colCommGeneration, Desc: true}, {Column: model.ColID, Desc: true},
	}
}

// selectChannelAdminAuthorityGrants selects the ChannelGrant rows that can
// carry the CALLER's own admin bit on this Channel: one exact query per subject
// of the caller's resolved grant-subject closure, each bounded to the rows a
// legal history can hold for one subject on one Channel.
//
// This is the exact-closure replacement for reading every generation a Channel
// ever had. Work scales with the caller's own authority, so neither a long
// history nor the grants OTHER subjects hold on the same Channel can make an
// authorised administrator unauthorisable: an unrelated subject's rows are
// never read, and therefore never counted against any ceiling.
//
// Superseded generations are not selected, because a revoked or expired row can
// never confer the bit; the returned rows are exactly the candidates
// EvaluateCurrentChannelGrant would have kept out of the whole history.
func selectChannelAdminAuthorityGrants(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	channelID model.ID,
	closure ChannelGrantSubjectClosure,
) ([]ChannelGrant, error) {
	selected := make([]ChannelGrant, 0, len(closure.Subjects))
	seen := make(map[model.ID]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		rows, more, err := listChannelGrantRows(
			ctx, tx, scope, channelID, currentChannelGrantSubjectFilters(subject),
			currentChannelGrantSubjectOrder(), channelAdminSubjectRowBound,
		)
		if err != nil {
			return nil, err
		}
		if more || len(rows) > 1 {
			// One subject cannot legally hold two current generations on one
			// Channel. Two would make "the" current grant of that subject
			// ambiguous, so the mutation refuses rather than choosing one.
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"ChannelGrant history holds more than one active generation for a subject",
			)
		}
		for _, grant := range rows {
			if grant.Subject != subject || grant.State != ChannelGrantActive {
				return nil, communicationError(
					ErrCommunicationEvidenceUnknown, "selected admin ChannelGrant does not answer its query",
				)
			}
			if _, duplicate := seen[grant.ID]; duplicate {
				return nil, communicationError(
					ErrCommunicationEvidenceUnknown, "admin ChannelGrant selection repeats a row",
				)
			}
			seen[grant.ID] = struct{}{}
			selected = append(selected, grant)
		}
	}
	return selected, nil
}

func (m *Module) UpdateChannel(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd ChannelUpdateCommand,
) (ChannelMutationResult, error) {
	return m.mutateChannelAdmin(ctx, scope, ref, cmd.ChannelID, cmd.IfMatch,
		communicationChannelUpdateAudit, &channelConfigurationMutation{cmd: cmd})
}

// channelConfigurationMutation is PATCH. It changes the Channel's own
// configuration and addresses NO ChannelGrant row: its authority is the
// caller's admin closure, which mutateChannelAdmin selects and locks, and its
// precondition is the Channel version. Reading the Channel's grant history here
// is exactly what used to make a long history block a configuration change that
// has nothing to do with it.
type channelConfigurationMutation struct{ cmd ChannelUpdateCommand }

func (*channelConfigurationMutation) selectRows(
	context.Context, *communicationTx, DirectoryScopeRef, Channel,
) ([]ChannelGrant, error) {
	return nil, nil
}

func (m *channelConfigurationMutation) apply(
	_ context.Context,
	tx *communicationTx,
	in channelAdminMutationInput,
) (Channel, *ChannelGrant, error) {
	cmd, before := m.cmd, in.before
	after := before
	after.Version++
	after.UpdatedAt = tx.now.Time()
	if cmd.Name != nil {
		after.Name = *cmd.Name
	}
	if cmd.Description != nil {
		after.Description = *cmd.Description
	}
	if cmd.State != nil {
		after.State = *cmd.State
	}
	if cmd.Sensitivity != nil {
		after.Sensitivity = *cmd.Sensitivity
	}
	if cmd.ContentProtection != nil {
		after.ContentProtection = *cmd.ContentProtection
	}
	if cmd.DefaultAckPolicy != nil {
		after.DefaultAckPolicy = *cmd.DefaultAckPolicy
	}
	if cmd.DefaultAckTimeoutMS != nil {
		after.DefaultAckTimeoutMS = *cmd.DefaultAckTimeoutMS
	}
	if cmd.DefaultWake != nil {
		after.DefaultWake = *cmd.DefaultWake
	}
	if cmd.RetentionPolicyRef != nil {
		after.RetentionPolicyRef = *cmd.RetentionPolicyRef
	}
	if cmd.MaxFanout != nil {
		after.MaxFanout = *cmd.MaxFanout
	}
	if cmd.MaxAutomationDepth != nil {
		after.MaxAutomationDepth = *cmd.MaxAutomationDepth
	}
	if before.Sensitivity != after.Sensitivity || before.ContentProtection != after.ContentProtection {
		after.ProtectionGeneration++
	}
	return after, nil, ValidateChannelUpdate(before, after)
}

func (m *Module) GrantChannel(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd ChannelGrantCommand,
) (ChannelMutationResult, error) {
	return m.mutateChannelAdmin(ctx, scope, ref, cmd.ChannelID, cmd.IfMatch,
		communicationChannelGrantAudit, &channelGrantMutation{cmd: cmd})
}

// channelGrantMutation issues the granted subject's NEXT generation. It needs
// exactly two facts about that ONE subject — its highest existing generation,
// which fixes the next number and the supersedes_id, and whether it already
// holds a persisted-active generation — and it reads neither from the Channel's
// other subjects nor from the generations behind the last one.
type channelGrantMutation struct {
	cmd ChannelGrantCommand
	// latest is the granted subject's exact highest generation on this Channel
	// as selectRows listed it, or nil when the subject holds none.
	latest *ChannelGrant
	// active are the granted subject's persisted-active generations. The
	// explicit revoke -> grant flow requires this to be empty: a lapsed TTL does
	// NOT free the subject, exactly as before.
	active []ChannelGrant
}

func (m *channelGrantMutation) selectRows(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	before Channel,
) ([]ChannelGrant, error) {
	m.latest, m.active = nil, nil
	subject := m.cmd.Grant.Subject
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	exact := func(extra ...model.Filter) []model.Filter {
		return append([]model.Filter{
			{Column: colCommSubjectKind, Op: model.OpEq, Value: string(subject.Kind)},
			{Column: colCommSubjectRef, Op: model.OpEq, Value: subject.Ref},
		}, extra...)
	}
	// The subject's own last two generations, highest first. Ordering by
	// generation with the ID tiebreaker in the SAME direction keeps this an
	// exact reverse scan of the unique (tenant, channel, subject, generation)
	// index: two rows read, whatever the length of the history behind them.
	newest, _, err := listChannelGrantRows(ctx, tx, scope, before.ID, exact(),
		[]model.Sort{{Column: colCommGeneration, Desc: true}, {Column: model.ColID, Desc: true}},
		channelAdminSubjectRowBound)
	if err != nil {
		return nil, err
	}
	for _, grant := range newest {
		if grant.Subject != subject {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "selected ChannelGrant generation answers another subject",
			)
		}
	}
	if len(newest) > 1 && newest[0].Generation <= newest[1].Generation {
		// Two rows at the same generation for one subject, or an order the
		// engine did not honour: the successor's legal predecessor is not
		// unique, so there is no defensible next number and no defensible
		// supersedes_id. Refuse by name rather than pick one.
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"ChannelGrant history does not establish a unique predecessor generation",
		)
	}
	if len(newest) > 0 {
		latest := newest[0]
		m.latest = &latest
	}
	// Whether the subject already holds a persisted-active generation is its own
	// exact question, answered by its own query rather than inferred from the
	// newest row: an active generation anywhere in this subject's history
	// refuses the grant.
	active, _, err := listChannelGrantRows(ctx, tx, scope, before.ID,
		currentChannelGrantSubjectFilters(subject), currentChannelGrantSubjectOrder(),
		channelAdminSubjectRowBound)
	if err != nil {
		return nil, err
	}
	m.active = active
	rows := append([]ChannelGrant(nil), active...)
	if m.latest != nil {
		rows = append(rows, *m.latest)
	}
	return rows, nil
}

func (m *channelGrantMutation) apply(
	ctx context.Context,
	tx *communicationTx,
	in channelAdminMutationInput,
) (Channel, *ChannelGrant, error) {
	for _, listed := range m.active {
		locked, ok := in.locked[listed.ID]
		if !ok {
			return Channel{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown, "selected active ChannelGrant was not locked",
			)
		}
		if locked.State == ChannelGrantActive {
			return Channel{}, nil, fmt.Errorf("%w: active ChannelGrant already exists", store.ErrConflict)
		}
	}
	generation := int64(1)
	var supersedes model.ID
	if m.latest != nil {
		locked, ok := in.locked[m.latest.ID]
		if !ok || locked.Generation != m.latest.Generation || locked.Subject != m.cmd.Grant.Subject {
			return Channel{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown, "ChannelGrant predecessor changed while it was locked",
			)
		}
		if locked.State == ChannelGrantActive {
			return Channel{}, nil, fmt.Errorf("%w: active ChannelGrant already exists", store.ErrConflict)
		}
		if locked.Generation == math.MaxInt64 {
			return Channel{}, nil, store.ErrConflict
		}
		generation, supersedes = locked.Generation+1, locked.ID
	}
	grant := newChannelGrant(
		in.scope, in.before.ID, in.actor, m.cmd.Grant, generation, supersedes, tx.now.Time(),
	)
	if err := ValidateChannelGrant(grant); err != nil {
		return Channel{}, nil, err
	}
	record, err := channelGrantToRecord(grant)
	if err != nil {
		return Channel{}, nil, err
	}
	if _, err = tx.createWithID(ctx, channelGrantKind, grant.ID, record); err != nil {
		return Channel{}, nil, err
	}
	after := in.before
	after.Version++
	after.ACLRevision++
	after.UpdatedAt = tx.now.Time()
	return after, &grant, ValidateChannelUpdate(in.before, after)
}

func (m *Module) RevokeChannelGrant(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd ChannelGrantRevokeCommand,
) (ChannelMutationResult, error) {
	return m.mutateChannelAdmin(ctx, scope, ref, cmd.ChannelID, cmd.IfMatch,
		communicationChannelRevokeAudit, &channelGrantRevokeMutation{cmd: cmd})
}

// channelGrantRevokeMutation revokes the EXACT row the caller addressed. It
// resolves that row by its own ID under the locked Channel, never by the
// subject's "current" generation, so revoking a generation the caller was shown
// can never retire the successor that replaced it in the meantime.
type channelGrantRevokeMutation struct {
	cmd ChannelGrantRevokeCommand
	// addressed is that row as selectRows listed it under the locked Channel;
	// apply requires the LOCKED copy to still be it, down to the row version.
	addressed ChannelGrant
}

func (m *channelGrantRevokeMutation) selectRows(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	before Channel,
) ([]ChannelGrant, error) {
	m.addressed = ChannelGrant{}
	// The query carries the Channel as well as the ID, so a grant that belongs
	// to another Channel is not selected here and is therefore never locked by
	// this transaction: the only ChannelGrant rows it fences belong to the
	// Channel whose row it already holds.
	rows, more, err := listChannelGrantRows(ctx, tx, scope, before.ID, []model.Filter{
		{Column: model.ColID, Op: model.OpEq, Value: m.cmd.GrantID.String()},
	}, nil, channelAdminSubjectRowBound)
	if err != nil {
		return nil, err
	}
	if more {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "ChannelGrant ID selection is not unique",
		)
	}
	if len(rows) != 1 || rows[0].ID != m.cmd.GrantID || rows[0].State != ChannelGrantActive {
		return nil, fmt.Errorf("%w: ChannelGrant is not active", store.ErrConflict)
	}
	m.addressed = rows[0]
	return rows, nil
}

func (m *channelGrantRevokeMutation) apply(
	ctx context.Context,
	tx *communicationTx,
	in channelAdminMutationInput,
) (Channel, *ChannelGrant, error) {
	grant, ok := in.locked[m.cmd.GrantID]
	if !ok || grant.ID != m.addressed.ID || grant.ChannelID != in.before.ID ||
		grant.Subject != m.addressed.Subject || grant.Generation != m.addressed.Generation ||
		grant.Version != m.addressed.Version || grant.State != ChannelGrantActive {
		return Channel{}, nil, fmt.Errorf("%w: ChannelGrant is not active", store.ErrConflict)
	}
	beforeVersion := grant.Version
	grant.Version++
	grant.UpdatedAt = tx.now.Time()
	grant.State = ChannelGrantRevoked
	grant.RevokedBy = &in.actor
	encoded, err := channelGrantToRecord(grant)
	if err != nil {
		return Channel{}, nil, err
	}
	encoded[model.ColVersion] = beforeVersion
	if _, err = tx.update(ctx, channelGrantKind, encoded); err != nil {
		return Channel{}, nil, err
	}
	after := in.before
	after.Version++
	after.ACLRevision++
	after.UpdatedAt = tx.now.Time()
	return after, &grant, ValidateChannelUpdate(in.before, after)
}

// channelAdminMutation is one administrative Channel mutation, split in two
// steps so that every ChannelGrant row it needs is SELECTED by an exact query
// and LOCKED before the transaction refreshes its database time, and APPLIED
// only after authority has been re-established at that refreshed instant.
//
// The split is what removes the historical ceiling: the shared step no longer
// reads a Channel's grant history at all, and each mutation names the exact
// rows its own semantics require.
type channelAdminMutation interface {
	// selectRows names the exact ChannelGrant rows this mutation will read or
	// update. It runs under the already-locked Channel, issues only bounded
	// queries confined to it, and must not write.
	selectRows(
		ctx context.Context,
		tx *communicationTx,
		scope DirectoryScopeRef,
		before Channel,
	) ([]ChannelGrant, error)
	// apply materialises the change from the LOCKED copies of those rows, at
	// the refreshed database time.
	apply(
		ctx context.Context,
		tx *communicationTx,
		in channelAdminMutationInput,
	) (Channel, *ChannelGrant, error)
}

// channelAdminMutationInput is everything a mutation may use: the locked
// Channel, the resolved actor and the LOCKED copies of the rows selectRows
// named. There is deliberately no "every grant of this Channel" member.
type channelAdminMutationInput struct {
	scope  DirectoryScopeRef
	before Channel
	actor  CommunicationActorRef
	locked map[model.ID]ChannelGrant
}

func (m *Module) mutateChannelAdmin(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	channelID model.ID,
	ifMatch string,
	action string,
	mutation channelAdminMutation,
) (ChannelMutationResult, error) {
	readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
	if readinessErr != nil || !readiness.Effective {
		return ChannelMutationResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
		)
	}
	if mutation == nil {
		return ChannelMutationResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel admin mutation is unavailable",
		)
	}
	expected, err := parseCommunicationVersionETag(ifMatch)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, channelID, CommunicationChannelAdmin,
	)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	recipient, _, err := m.communicationPrincipalRecipient(ctx, scope, inspected.principal)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	actor, err := communicationActorForRecipient(recipient)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	closure, err := m.communicationGrantClosure.ResolveChannelGrantSubjects(ctx, scope, inspected.principal)
	if err != nil || closure.Outcome != ReadAllow {
		return ChannelMutationResult{}, communicationError(
			ErrCommunicationForbidden, "channel admin grant is unavailable",
		)
	}
	// The authority selection below issues ONE exact query per closure subject,
	// so the closure's cardinality is now the writer's own work. This is the only
	// property the change adds to the closure contract, and it is checked before
	// the work rather than discovered row by row; a malformed, duplicated or
	// empty closure keeps being decided exactly where it was decided before, by
	// EvaluateCurrentChannelGrant on the locked rows.
	if len(closure.Subjects) > directNoticeReadSetBound {
		return ChannelMutationResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "channel admin ChannelGrant closure exceeds bound",
		)
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(inspected.principal),
	)
	if err != nil {
		return ChannelMutationResult{}, err
	}
	var result ChannelMutationResult
	err = m.mutateCommunicationWithAuthority(
		ctx, question, bound, claims,
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			channelRecord, err := tx.lockRecord(ctx, channelKind, channelID)
			if err != nil {
				return err
			}
			before, err := channelFromRecord(channelRecord)
			if err != nil {
				return err
			}
			if before.TenantID != scope.TenantID || before.WorkspaceID != scope.WorkspaceID ||
				before.Version != expected {
				return fmt.Errorf("%w: Channel version mismatch", store.ErrConflict)
			}
			epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
			if err != nil || epoch.Version != closure.DirectoryEpoch || epoch.TenantID != scope.TenantID {
				return communicationError(ErrCommunicationEvidenceUnknown, "channel admin directory epoch changed")
			}
			// Exactly the rows this request needs and nothing else: the caller's
			// own admin closure on THIS Channel, plus the rows the mutation
			// addresses. The Channel row this transaction already holds fences
			// every ChannelGrant writer of the same Channel — create, grant and
			// revoke all pass through it — so a selection made here is stable for
			// the rest of the transaction, and is corroborated again once locked.
			authority, err := selectChannelAdminAuthorityGrants(ctx, tx, scope, before.ID, closure)
			if err != nil {
				return err
			}
			addressed, err := mutation.selectRows(ctx, tx, scope, before)
			if err != nil {
				return err
			}
			locked, err := lockSelectedChannelGrants(ctx, tx, scope, before.ID, authority, addressed)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			// Database time is refreshed AFTER every blocking lock this
			// transaction takes, so expiry and freshness are judged at the instant
			// the transaction reached, not at the instant it began waiting.
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			current := make([]ChannelGrant, 0, len(authority))
			for _, selected := range authority {
				grant, ok := locked[selected.ID]
				if !ok {
					return communicationError(
						ErrCommunicationEvidenceUnknown, "selected admin ChannelGrant was not locked",
					)
				}
				current = append(current, grant)
			}
			admin := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
				Verdict: VerdictClean, Code: "channel_grants_locked",
				ACLRevision: before.ACLRevision, ObservedAt: tx.now.Time(), Grants: current,
			}, scope.TenantID, scope.WorkspaceID, channelID, closure, ChannelGrantAdmin, tx.now.Time())
			if evidenceVerdict(admin.Evidence) != VerdictClean {
				return communicationError(ErrCommunicationForbidden, "caller lacks ChannelGrant.admin")
			}
			if deadline, constrained, err := handoffGrantFreshUntil(
				current, closure, ChannelGrantAdmin, tx.now.Time(),
			); err != nil {
				return err
			} else if constrained {
				if err := tx.narrowRequestAuthorityFreshUntil(deadline); err != nil {
					return err
				}
			}
			after, grant, err := mutation.apply(ctx, tx, channelAdminMutationInput{
				scope: scope, before: before, actor: actor, locked: locked,
			})
			if err != nil {
				return err
			}
			afterRecord, err := channelToRecord(after)
			if err != nil {
				return err
			}
			afterRecord[model.ColVersion] = before.Version
			if _, err = tx.update(ctx, channelKind, afterRecord); err != nil {
				return err
			}
			payload, err := canonicalJSON(struct {
				ChannelID model.ID `json:"channel_id"`
				Before    int64    `json:"before"`
				After     int64    `json:"after"`
				GrantID   model.ID `json:"grant_id,omitempty"`
			}{ChannelID: after.ID, Before: before.Version, After: after.Version,
				GrantID: func() model.ID {
					if grant == nil {
						return ""
					}
					return grant.ID
				}()})
			if err != nil {
				return err
			}
			hash := sha256.Sum256(payload)
			_, auditKind := communicationAuditActor(actor)
			audit, err := tx.appendAudit(ctx, model.AuditDraft{
				Actor: directNoticeActor(inspected.principal), ActorKind: auditKind,
				Action: action, TargetKind: channelKind, TargetID: channelID, PayloadHash: hash[:],
				Meta: map[string]any{"workspace_id": scope.WorkspaceID.String(), "acl_revision": after.ACLRevision},
			})
			if err != nil || audit.Seq < 1 {
				return communicationError(ErrCommunicationEvidenceUnknown, "channel admin audit append failed")
			}
			result = ChannelMutationResult{
				Channel: after, Grant: grant, ETag: communicationVersionETag(after.Version),
				AuditSeq: audit.Seq,
			}
			return nil
		},
	)
	return result, err
}

func communicationVersionETag(version int64) string { return fmt.Sprintf("\"v%d\"", version) }

func parseCommunicationVersionETag(value string) (int64, error) {
	if value == "" {
		return 0, errCommunicationChannelVersionRequired
	}
	version, present, err := parseWorkETag(value)
	if err != nil || !present || version < 1 {
		return 0, communicationError(ErrInvalidCommunicationModel, "If-Match requires a strong Channel ETag")
	}
	return version, nil
}

func sameChannelMutationResult(left, right ChannelMutationResult) bool {
	leftRaw, leftErr := canonicalJSON(left)
	rightRaw, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}
