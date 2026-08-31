// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ProtocolReplayKind is the closed provider identifier vocabulary from the K5
// communication contract. The identifier itself is never persisted.
type ProtocolReplayKind string

const (
	ProtocolReplayJTI       ProtocolReplayKind = "jti"
	ProtocolReplayMessageID ProtocolReplayKind = "message_id"
	ProtocolReplayRequestID ProtocolReplayKind = "request_id"
)

var (
	ErrInvalidProtocolReplay  = errors.New("sessions: invalid protocol replay claim")
	ErrProtocolReplayConflict = errors.New("sessions: protocol replay conflict")
	ErrProtocolReplayUnknown  = errors.New("sessions: protocol replay evidence unavailable")
)

// ProtocolReplayClaim is the server-resolved identity of one authenticated
// provider envelope. ReplayID is accepted only by this in-process port and is
// immediately reduced to a domain-bound SHA-256 commitment.
type ProtocolReplayClaim struct {
	WorkspaceID       model.ID           `json:"workspace_id"`
	Protocol          BindingProtocol    `json:"protocol"`
	PeerAuthority     string             `json:"peer_authority"`
	Kind              ProtocolReplayKind `json:"replay_kind"`
	ReplayID          string             `json:"-"`
	ExpiresAt         time.Time          `json:"expires_at"`
	ExpectedBindingID model.ID           `json:"expected_binding_id,omitempty"`
}

// ProtocolReplaySettlement is returned by the guarded mutation. BindingID is
// optional for authenticated envelopes that create no protocol binding.
type ProtocolReplaySettlement struct {
	BindingID model.ID `json:"binding_id,omitempty"`
}

// ProtocolReplayGuard is the immutable, hash-only durable replay authority.
type ProtocolReplayGuard struct {
	AppendOnlyCommunicationEntity
	Protocol      BindingProtocol    `json:"protocol"`
	PeerAuthority string             `json:"peer_authority"`
	ReplayKind    ProtocolReplayKind `json:"replay_kind"`
	ReplayHash    []byte             `json:"replay_hash"`
	FirstSeenAt   time.Time          `json:"first_seen_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
	BindingID     model.ID           `json:"binding_id,omitempty"`
}

type ProtocolReplayResult struct {
	Guard    ProtocolReplayGuard `json:"guard"`
	Replayed bool                `json:"replayed"`
}

// ProtocolReplayMutation performs the provider-derived domain mutation. The
// supplied context must be passed to sessions methods: those calls then join
// the replay transaction instead of opening an independent transaction.
type ProtocolReplayMutation func(context.Context) (ProtocolReplaySettlement, error)

// ProtocolReplayStore is the composition-root port used by A2A/MCP adapters.
// ApplyProtocolReplay commits the guarded mutation and its replay row together.
type ProtocolReplayStore interface {
	ApplyProtocolReplay(
		context.Context,
		model.TenantID,
		ProtocolReplayClaim,
		ProtocolReplayMutation,
	) (ProtocolReplayResult, error)
}

var _ ProtocolReplayStore = (*Module)(nil)

func (kind ProtocolReplayKind) valid() bool {
	return kind == ProtocolReplayJTI || kind == ProtocolReplayMessageID ||
		kind == ProtocolReplayRequestID
}

type normalizedProtocolReplayClaim struct {
	ProtocolReplayClaim
	replayHash []byte
}

func normalizeProtocolReplayClaim(claim ProtocolReplayClaim) (normalizedProtocolReplayClaim, error) {
	claim.Protocol = BindingProtocol(strings.ToLower(strings.TrimSpace(string(claim.Protocol))))
	claim.Kind = ProtocolReplayKind(strings.ToLower(strings.TrimSpace(string(claim.Kind))))
	claim.ReplayID = strings.TrimSpace(claim.ReplayID)
	claim.ExpiresAt = claim.ExpiresAt.UTC()
	peer, err := normalizeProtocolAuthority(claim.PeerAuthority)
	if err != nil {
		return normalizedProtocolReplayClaim{}, protocolReplayInvalid("invalid_peer_authority")
	}
	claim.PeerAuthority = peer
	if !validCanonicalCommunicationID(claim.WorkspaceID) || !claim.Protocol.valid() ||
		!claim.Kind.valid() || !validateOpaqueRef(claim.ReplayID) || claim.ExpiresAt.IsZero() ||
		(!claim.ExpectedBindingID.IsZero() && !validCanonicalCommunicationID(claim.ExpectedBindingID)) {
		return normalizedProtocolReplayClaim{}, protocolReplayInvalid("invalid_claim")
	}
	digest := sha256.Sum256([]byte(
		"olivares.sessions.protocol-replay.v1\x00" + string(claim.Protocol) + "\x00" +
			claim.PeerAuthority + "\x00" + string(claim.Kind) + "\x00" + claim.ReplayID,
	))
	return normalizedProtocolReplayClaim{ProtocolReplayClaim: claim, replayHash: digest[:]}, nil
}

// ApplyProtocolReplay is a claim/replay/settle transaction. On an exact replay
// mutation is not called. A mutation error rolls back every joined sessions
// write and leaves no guard, so a corrected retry can proceed.
func (m *Module) ApplyProtocolReplay(
	ctx context.Context,
	tenant model.TenantID,
	claim ProtocolReplayClaim,
	mutation ProtocolReplayMutation,
) (ProtocolReplayResult, error) {
	if tenant.IsZero() || tenant.IsSystem() || mutation == nil {
		return ProtocolReplayResult{}, protocolReplayInvalid("invalid_claim")
	}
	normalized, err := normalizeProtocolReplayClaim(claim)
	if err != nil {
		return ProtocolReplayResult{}, err
	}
	var result ProtocolReplayResult
	for attempt := 0; attempt < 2; attempt++ {
		err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
			repo, err := sc.Ext(protocolReplayGuardKind)
			if err != nil {
				return err
			}
			current, found, err := findProtocolReplayGuard(ctx, repo, normalized)
			if err != nil {
				return err
			}
			if found {
				if current.WorkspaceID != normalized.WorkspaceID ||
					(!normalized.ExpectedBindingID.IsZero() && current.BindingID != normalized.ExpectedBindingID) {
					return protocolReplayConflict("claim_identity_changed")
				}
				result = ProtocolReplayResult{Guard: current, Replayed: true}
				return nil
			}
			now, err := transactionNow(ctx, sc)
			if err != nil {
				return err
			}
			if !normalized.ExpiresAt.After(now.Time()) {
				return protocolReplayInvalid("claim_expired")
			}

			joined, joinedCtx := newProtocolReplayTransactionContext(ctx, tenant, sc)
			settlement, mutationErr := mutation(joinedCtx)
			joined.active.Store(false)
			if mutationErr != nil {
				return mutationErr
			}
			if !normalized.ExpectedBindingID.IsZero() &&
				settlement.BindingID != normalized.ExpectedBindingID {
				return protocolReplayConflict("settlement_binding_changed")
			}
			if !settlement.BindingID.IsZero() {
				if err := validateProtocolReplayBinding(ctx, sc, normalized, settlement.BindingID); err != nil {
					return err
				}
			}
			guard := ProtocolReplayGuard{
				AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
					CommunicationEntity: CommunicationEntity{
						ID: model.NewID(), TenantID: tenant, WorkspaceID: normalized.WorkspaceID,
						Version: 1, CreatedAt: now.Time(),
					},
				},
				Protocol: normalized.Protocol, PeerAuthority: normalized.PeerAuthority,
				ReplayKind: normalized.Kind, ReplayHash: append([]byte(nil), normalized.replayHash...),
				FirstSeenAt: now.Time(), ExpiresAt: normalized.ExpiresAt,
				BindingID: settlement.BindingID,
			}
			created, err := repo.CreateWithID(ctx, guard.ID, protocolReplayGuardRecord(guard))
			if err != nil {
				return err
			}
			guard, err = protocolReplayGuardFromRecord(created)
			if err != nil {
				return err
			}
			result = ProtocolReplayResult{Guard: guard}
			return nil
		})
		if err == nil || !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if err != nil {
		return ProtocolReplayResult{}, classifyProtocolReplayStoreError(err)
	}
	return result, nil
}

func findProtocolReplayGuard(
	ctx context.Context,
	repo store.GenericRepo,
	claim normalizedProtocolReplayClaim,
) (ProtocolReplayGuard, bool, error) {
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colReplayProtocol, Op: model.OpEq, Value: string(claim.Protocol)},
		{Column: colReplayPeerAuthority, Op: model.OpEq, Value: claim.PeerAuthority},
		{Column: colReplayKind, Op: model.OpEq, Value: string(claim.Kind)},
		{Column: colReplayHash, Op: model.OpEq, Value: claim.replayHash},
	}, Limit: 2})
	if err != nil || len(rows) == 0 {
		return ProtocolReplayGuard{}, false, err
	}
	if len(rows) != 1 {
		return ProtocolReplayGuard{}, false, protocolReplayUnknown("claim_not_unique", nil)
	}
	guard, err := protocolReplayGuardFromRecord(rows[0])
	return guard, err == nil, err
}

func validateProtocolReplayBinding(
	ctx context.Context,
	sc store.Scope,
	claim normalizedProtocolReplayClaim,
	bindingID model.ID,
) error {
	repo, err := sc.Ext(protocolBindingKind)
	if err != nil {
		return err
	}
	stored, err := loadProtocolBindingByID(ctx, repo, bindingID)
	if err != nil {
		return err
	}
	if stored.WorkspaceID != claim.WorkspaceID || stored.Protocol != claim.Protocol ||
		stored.PeerAuthority != claim.PeerAuthority {
		return protocolReplayConflict("settlement_binding_mismatch")
	}
	return nil
}

func protocolReplayGuardRecord(guard ProtocolReplayGuard) model.Record {
	record := appendOnlyCommunicationRecord(guard.AppendOnlyCommunicationEntity)
	record[colReplayProtocol] = string(guard.Protocol)
	record[colReplayPeerAuthority] = guard.PeerAuthority
	record[colReplayKind] = string(guard.ReplayKind)
	record[colReplayHash] = append([]byte(nil), guard.ReplayHash...)
	record[colReplayFirstSeenAt] = model.NewTimestamp(guard.FirstSeenAt).String()
	record[colReplayExpiresAt] = model.NewTimestamp(guard.ExpiresAt).String()
	record[colReplayBindingID] = optionalCommunicationID(guard.BindingID)
	return record
}

func protocolReplayGuardFromRecord(record model.Record) (ProtocolReplayGuard, error) {
	reader := newCommunicationRecordReader(protocolReplayGuardKind, record)
	guard := ProtocolReplayGuard{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(),
		Protocol:                      BindingProtocol(reader.text(colReplayProtocol)),
		PeerAuthority:                 reader.text(colReplayPeerAuthority),
		ReplayKind:                    ProtocolReplayKind(reader.text(colReplayKind)),
		ReplayHash:                    reader.bytes(colReplayHash),
		FirstSeenAt:                   reader.timestamp(colReplayFirstSeenAt),
		ExpiresAt:                     reader.timestamp(colReplayExpiresAt),
		BindingID:                     reader.optionalID(colReplayBindingID),
	}
	if reader.err != nil || !guard.Protocol.valid() || !guard.ReplayKind.valid() ||
		len(guard.ReplayHash) != sha256.Size || guard.PeerAuthority == "" ||
		guard.FirstSeenAt.IsZero() || !guard.ExpiresAt.After(guard.FirstSeenAt) {
		if reader.err != nil {
			return ProtocolReplayGuard{}, reader.err
		}
		return ProtocolReplayGuard{}, protocolReplayUnknown("invalid_durable_guard", nil)
	}
	return guard, nil
}

func protocolReplayInvalid(code string) error {
	return fmt.Errorf("%w: %s", ErrInvalidProtocolReplay, code)
}

func protocolReplayConflict(code string) error {
	return fmt.Errorf("%w: %s", ErrProtocolReplayConflict, code)
}

func protocolReplayUnknown(code string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrProtocolReplayUnknown, code)
	}
	return fmt.Errorf("%w: %s: %v", ErrProtocolReplayUnknown, code, cause)
}

func classifyProtocolReplayStoreError(err error) error {
	switch {
	case err == nil,
		errors.Is(err, ErrInvalidProtocolReplay),
		errors.Is(err, ErrProtocolReplayConflict),
		errors.Is(err, ErrProtocolReplayUnknown):
		return err
	case errors.Is(err, store.ErrConflict):
		return protocolReplayConflict("claim_conflict")
	case errors.Is(err, store.ErrStoreUnavailable), errors.Is(err, store.ErrNotLeader),
		errors.Is(err, store.ErrAuditSpoolFull):
		return protocolReplayUnknown("observation_unavailable", err)
	default:
		return err
	}
}

type protocolReplayTransactionContextKey struct{}

type protocolReplayTransactionContext struct {
	tenant model.TenantID
	scope  store.Scope
	active atomic.Bool
}

func newProtocolReplayTransactionContext(
	ctx context.Context,
	tenant model.TenantID,
	scope store.Scope,
) (*protocolReplayTransactionContext, context.Context) {
	joined := &protocolReplayTransactionContext{tenant: tenant, scope: scope}
	joined.active.Store(true)
	return joined, context.WithValue(ctx, protocolReplayTransactionContextKey{}, joined)
}

func protocolReplayScopeFromContext(
	ctx context.Context,
	tenant model.TenantID,
) (store.Scope, bool) {
	if ctx == nil {
		return nil, false
	}
	joined, ok := ctx.Value(protocolReplayTransactionContextKey{}).(*protocolReplayTransactionContext)
	if !ok || joined == nil || joined.scope == nil || joined.tenant != tenant || !joined.active.Load() {
		return nil, false
	}
	return joined.scope, true
}
