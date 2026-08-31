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

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type preparedProtocolSpecPlan struct {
	public         ProtocolBindingSpecPlan
	input          *ProtocolBindingSpecInput
	target         storedProtocolBindingSpec
	priorActive    storedProtocolBindingSpec
	commandKeyHash []byte
	requestHash    []byte
	planHash       []byte
	replay         *storedProtocolBindingSpec
}

func (m *Module) PlanProtocolBindingSpec(
	ctx context.Context,
	tenant model.TenantID,
	command ProtocolBindingSpecCommand,
) (ProtocolBindingSpecPlan, error) {
	var prepared preparedProtocolSpecPlan
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		var err error
		prepared, err = prepareProtocolSpecPlan(ctx, sc, command)
		return err
	})
	if err != nil {
		return ProtocolBindingSpecPlan{}, classifyProtocolBindingStoreError(err)
	}
	return prepared.public, nil
}

func (m *Module) ApplyProtocolBindingSpec(
	ctx context.Context,
	tenant model.TenantID,
	command ProtocolBindingSpecCommand,
) (ProtocolBindingSpecResult, error) {
	var result ProtocolBindingSpecResult
	err := m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		prepared, err := prepareProtocolSpecPlan(ctx, sc, command)
		if err != nil {
			return err
		}
		if prepared.replay != nil {
			result = ProtocolBindingSpecResult{
				ProtocolBindingSpecPlan: prepared.public,
				Spec:                    prepared.replay.ProtocolBindingSpec,
				Replayed:                true,
			}
			return nil
		}
		if command.ExpectedPlanHash == "" ||
			!strings.EqualFold(command.ExpectedPlanHash, protocolBindingHex(prepared.planHash)) {
			return protocolBindingConflict("plan_changed")
		}
		repo, err := sc.Ext(protocolBindingSpecKind)
		if err != nil {
			return err
		}
		switch command.Operation {
		case ProtocolBindingSpecCreateDraft:
			input := *prepared.input
			mappingHash, lossesHash, specHash, err := protocolBindingSpecHashes(input)
			if err != nil {
				return err
			}
			value := storedProtocolBindingSpec{
				ProtocolBindingSpec: ProtocolBindingSpec{
					MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
						WorkspaceID: input.WorkspaceID,
					}},
					BindingKey: input.BindingKey, Generation: input.Generation,
					Protocol: input.Protocol, ProtocolVersion: input.ProtocolVersion,
					Direction: input.Direction, LocalKind: input.LocalKind,
					LocalSelector: input.LocalSelector, PeerAuthority: input.PeerAuthority,
					RemoteResourceKind: input.RemoteResourceKind,
					RemoteResourceRef:  input.RemoteResourceRef, MappingSchema: input.MappingSchema,
					Mapping: input.Mapping, MappingHash: mappingHash,
					KnownLosses: input.KnownLosses, LossesHash: lossesHash,
					RuleRefs: input.RuleRefs, PermissionProfileRef: input.PermissionProfileRef,
					CurrencyPolicy: input.CurrencyPolicy, Validation: input.Validation,
					State: ProtocolBindingSpecDraft, SupersedesID: input.SupersedesID,
					SpecHash: specHash, PlanHash: prepared.planHash,
				},
				commandKeyHash: prepared.commandKeyHash,
				requestHash:    prepared.requestHash,
			}
			record, err := encodeProtocolBindingSpec(value)
			if err != nil {
				return err
			}
			created, err := repo.Create(ctx, record)
			if err != nil {
				if errors.Is(err, store.ErrConflict) {
					replayed, replayErr := findProtocolSpecReplay(
						ctx, repo, input.WorkspaceID, prepared.commandKeyHash, prepared.requestHash,
					)
					if replayErr == nil && replayed != nil {
						result = ProtocolBindingSpecResult{
							ProtocolBindingSpecPlan: protocolSpecPlanFromStored(*replayed, ProtocolBindingSpecCreateDraft, "draft_replayed"),
							Spec:                    replayed.ProtocolBindingSpec, Replayed: true,
						}
						return nil
					}
				}
				return err
			}
			stored, err := decodeProtocolBindingSpec(created)
			if err != nil {
				return err
			}
			prepared.public.SpecID = stored.ID
			result = ProtocolBindingSpecResult{ProtocolBindingSpecPlan: prepared.public, Spec: stored.ProtocolBindingSpec}
			return nil

		case ProtocolBindingSpecActivate:
			if !prepared.priorActive.ID.IsZero() {
				prior := prepared.priorActive
				prior.State = ProtocolBindingSpecSuperseded
				prior.PlanHash = prepared.planHash
				record, err := encodeProtocolBindingSpec(prior)
				if err != nil {
					return err
				}
				if _, err = repo.Update(ctx, record); err != nil {
					return err
				}
			}
			target := prepared.target
			target.State = ProtocolBindingSpecActive
			target.PlanHash = prepared.planHash
			record, err := encodeProtocolBindingSpec(target)
			if err != nil {
				return err
			}
			updated, err := repo.Update(ctx, record)
			if err != nil {
				return err
			}
			stored, err := decodeProtocolBindingSpec(updated)
			if err != nil {
				return err
			}
			result = ProtocolBindingSpecResult{ProtocolBindingSpecPlan: prepared.public, Spec: stored.ProtocolBindingSpec}
			return nil

		case ProtocolBindingSpecDisable:
			target := prepared.target
			target.State = ProtocolBindingSpecDisabled
			target.PlanHash = prepared.planHash
			record, err := encodeProtocolBindingSpec(target)
			if err != nil {
				return err
			}
			updated, err := repo.Update(ctx, record)
			if err != nil {
				return err
			}
			stored, err := decodeProtocolBindingSpec(updated)
			if err != nil {
				return err
			}
			result = ProtocolBindingSpecResult{ProtocolBindingSpecPlan: prepared.public, Spec: stored.ProtocolBindingSpec}
			return nil
		default:
			return protocolBindingInvalid("invalid_spec_operation")
		}
	})
	if err != nil {
		return ProtocolBindingSpecResult{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

func prepareProtocolSpecPlan(
	ctx context.Context,
	sc store.Scope,
	command ProtocolBindingSpecCommand,
) (preparedProtocolSpecPlan, error) {
	if !command.Operation.valid() || !validCanonicalCommunicationID(command.WorkspaceID) {
		return preparedProtocolSpecPlan{}, protocolBindingInvalid("invalid_spec_command")
	}
	if _, err := sc.Workspaces().Get(ctx, command.WorkspaceID); err != nil {
		return preparedProtocolSpecPlan{}, err
	}
	repo, err := sc.Ext(protocolBindingSpecKind)
	if err != nil {
		return preparedProtocolSpecPlan{}, err
	}
	prepared := preparedProtocolSpecPlan{}
	switch command.Operation {
	case ProtocolBindingSpecCreateDraft:
		if command.Input == nil || !command.SpecID.IsZero() || command.ExpectedVersion != 0 ||
			!validateOpaqueRef(strings.TrimSpace(command.IdempotencyKey)) {
			return prepared, protocolBindingInvalid("invalid_draft_command")
		}
		input := *command.Input
		if input.WorkspaceID != command.WorkspaceID {
			return prepared, protocolBindingInvalid("workspace_mismatch")
		}
		input, err = normalizeProtocolSpecInput(input)
		if err != nil {
			return prepared, err
		}
		prepared.input = &input
		prepared.commandKeyHash = hashBytes([]byte(strings.TrimSpace(command.IdempotencyKey)))
		requestInput := input
		// Capability validation is a server observation refreshed at create and
		// activate time. It is not desired configuration and its timestamp must
		// not make a previously issued plan hash unstable.
		requestInput.Validation = ProtocolBindingValidation{}
		prepared.requestHash, err = protocolBindingHash(struct {
			Operation ProtocolBindingSpecOperation `json:"operation"`
			Input     ProtocolBindingSpecInput     `json:"input"`
		}{Operation: command.Operation, Input: requestInput})
		if err != nil {
			return prepared, err
		}
		if replay, replayErr := findProtocolSpecReplay(ctx, repo, command.WorkspaceID,
			prepared.commandKeyHash, prepared.requestHash); replayErr != nil {
			return prepared, replayErr
		} else if replay != nil {
			prepared.replay = replay
			prepared.public = protocolSpecPlanFromStored(*replay, ProtocolBindingSpecCreateDraft, "draft_replayed")
			return prepared, nil
		}
		generationRows, err := listAll(ctx, repo,
			model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: command.WorkspaceID.String()},
			model.Filter{Column: colBindingKey, Op: model.OpEq, Value: input.BindingKey},
			model.Filter{Column: colCommGeneration, Op: model.OpEq, Value: input.Generation},
		)
		if err != nil {
			return prepared, err
		}
		if len(generationRows) != 0 {
			return prepared, protocolBindingConflict("spec_generation_exists")
		}
		active, err := findActiveProtocolSpec(ctx, repo, command.WorkspaceID, input.BindingKey)
		if err != nil {
			return prepared, err
		}
		if active != nil {
			if input.SupersedesID != active.ID || input.Generation <= active.Generation {
				return prepared, protocolBindingConflict("successor_mismatch")
			}
			prepared.priorActive = *active
		} else if !input.SupersedesID.IsZero() {
			return prepared, protocolBindingConflict("superseded_spec_not_active")
		}
		mappingHash, lossesHash, specHash, err := protocolBindingSpecHashes(input)
		if err != nil {
			return prepared, err
		}
		prepared.planHash, err = protocolBindingHash(struct {
			Operation          ProtocolBindingSpecOperation `json:"operation"`
			WorkspaceID        model.ID                     `json:"workspace_id"`
			RequestHash        []byte                       `json:"request_hash"`
			PriorActiveID      model.ID                     `json:"prior_active_id,omitempty"`
			PriorActiveVersion int64                        `json:"prior_active_version,omitempty"`
		}{command.Operation, command.WorkspaceID, prepared.requestHash,
			prepared.priorActive.ID, prepared.priorActive.Version})
		if err != nil {
			return prepared, err
		}
		prepared.public = ProtocolBindingSpecPlan{
			Verdict: ProtocolObservationClean, Code: "draft_planned",
			Validation: input.Validation,
			PlanHash:   protocolBindingHex(prepared.planHash), Operation: command.Operation,
			WorkspaceID: command.WorkspaceID, Generation: input.Generation,
			PriorActiveID: prepared.priorActive.ID, SpecHash: protocolBindingHex(specHash),
			MappingHash: protocolBindingHex(mappingHash), LossesHash: protocolBindingHex(lossesHash),
		}
		return prepared, nil

	case ProtocolBindingSpecActivate, ProtocolBindingSpecDisable:
		if command.Input != nil || !validCanonicalCommunicationID(command.SpecID) ||
			command.ExpectedVersion < 1 || command.IdempotencyKey != "" {
			return prepared, protocolBindingInvalid("invalid_spec_state_command")
		}
		record, err := repo.Get(ctx, command.SpecID)
		if err != nil {
			return prepared, err
		}
		prepared.target, err = decodeProtocolBindingSpec(record)
		if err != nil {
			return prepared, err
		}
		if prepared.target.WorkspaceID != command.WorkspaceID {
			return prepared, protocolBindingNotFound("spec_not_found")
		}
		if prepared.target.Version != command.ExpectedVersion {
			if (command.Operation == ProtocolBindingSpecActivate && prepared.target.State == ProtocolBindingSpecActive) ||
				(command.Operation == ProtocolBindingSpecDisable && prepared.target.State == ProtocolBindingSpecDisabled) {
				if command.ExpectedPlanHash != "" && strings.EqualFold(command.ExpectedPlanHash,
					protocolBindingHex(prepared.target.PlanHash)) {
					prepared.replay = &prepared.target
					prepared.public = protocolSpecPlanFromStored(prepared.target, command.Operation, string(command.Operation)+"_replayed")
					return prepared, nil
				}
			}
			return prepared, protocolBindingConflict("spec_version_conflict")
		}
		if prepared.target.State != ProtocolBindingSpecDraft &&
			!(command.Operation == ProtocolBindingSpecDisable && prepared.target.State == ProtocolBindingSpecActive) {
			return prepared, protocolBindingConflict("spec_terminal")
		}
		if command.validationOverride != nil {
			validation, validationErr := normalizeServerProtocolBindingValidation(*command.validationOverride)
			if validationErr != nil {
				return prepared, validationErr
			}
			prepared.target.Validation = validation
		}
		if command.Operation == ProtocolBindingSpecActivate {
			if prepared.target.Validation.Verdict == ProtocolObservationUnknown ||
				(prepared.target.Validation.Verdict == ProtocolObservationClean &&
					prepared.target.Validation.ObservedAt.IsZero()) {
				return prepared, protocolBindingUnknown("capability_not_observed", nil)
			}
			if prepared.target.Validation.Verdict != ProtocolObservationClean ||
				prepared.target.CurrencyPolicy != BindingCurrencyPinned {
				return prepared, protocolBindingConflict("capability_not_clean")
			}
			for _, loss := range prepared.target.KnownLosses {
				if !loss.Accepted || !validateOpaqueRef(loss.AcceptanceRef) {
					return prepared, protocolBindingConflict("loss_not_accepted")
				}
			}
			active, err := findActiveProtocolSpec(ctx, repo, command.WorkspaceID, prepared.target.BindingKey)
			if err != nil {
				return prepared, err
			}
			if active != nil {
				if active.ID == prepared.target.ID {
					return prepared, protocolBindingConflict("spec_already_active")
				}
				if prepared.target.SupersedesID != active.ID ||
					prepared.target.Generation <= active.Generation {
					return prepared, protocolBindingConflict("successor_mismatch")
				}
				prepared.priorActive = *active
			} else if !prepared.target.SupersedesID.IsZero() {
				return prepared, protocolBindingConflict("superseded_spec_not_active")
			}
		}
		prepared.requestHash, err = protocolBindingHash(struct {
			Operation          ProtocolBindingSpecOperation `json:"operation"`
			WorkspaceID        model.ID                     `json:"workspace_id"`
			SpecID             model.ID                     `json:"spec_id"`
			ExpectedVersion    int64                        `json:"expected_version"`
			SpecHash           []byte                       `json:"spec_hash"`
			PriorActiveID      model.ID                     `json:"prior_active_id,omitempty"`
			PriorActiveVersion int64                        `json:"prior_active_version,omitempty"`
		}{command.Operation, command.WorkspaceID, command.SpecID, command.ExpectedVersion,
			prepared.target.SpecHash, prepared.priorActive.ID, prepared.priorActive.Version})
		if err != nil {
			return prepared, err
		}
		prepared.planHash = hashBytes(prepared.requestHash)
		prepared.public = ProtocolBindingSpecPlan{
			Verdict: ProtocolObservationClean, Code: string(command.Operation) + "_planned",
			Validation: prepared.target.Validation,
			PlanHash:   protocolBindingHex(prepared.planHash), Operation: command.Operation,
			WorkspaceID: command.WorkspaceID, SpecID: command.SpecID,
			Generation: prepared.target.Generation, PriorActiveID: prepared.priorActive.ID,
			SpecHash:    protocolBindingHex(prepared.target.SpecHash),
			MappingHash: protocolBindingHex(prepared.target.MappingHash),
			LossesHash:  protocolBindingHex(prepared.target.LossesHash),
		}
		return prepared, nil
	}
	return prepared, protocolBindingInvalid("invalid_spec_operation")
}

func findProtocolSpecReplay(
	ctx context.Context,
	repo store.GenericRepo,
	workspace model.ID,
	commandKeyHash, requestHash []byte,
) (*storedProtocolBindingSpec, error) {
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace.String()},
		model.Filter{Column: colBindingCommandKeyHash, Op: model.OpEq, Value: commandKeyHash},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 {
		return nil, protocolBindingUnknown("duplicate_spec_command", nil)
	}
	value, err := decodeProtocolBindingSpec(rows[0])
	if err != nil {
		return nil, err
	}
	if !bytesEqual(value.requestHash, requestHash) {
		return nil, protocolBindingConflict("idempotency_key_reused")
	}
	return &value, nil
}

func findActiveProtocolSpec(
	ctx context.Context,
	repo store.GenericRepo,
	workspace model.ID,
	bindingKey string,
) (*storedProtocolBindingSpec, error) {
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace.String()},
		model.Filter{Column: colBindingActiveSlot, Op: model.OpEq, Value: bindingKey},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 {
		return nil, protocolBindingUnknown("multiple_active_specs", nil)
	}
	value, err := decodeProtocolBindingSpec(rows[0])
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func protocolSpecPlanFromStored(
	value storedProtocolBindingSpec,
	operation ProtocolBindingSpecOperation,
	code string,
) ProtocolBindingSpecPlan {
	return ProtocolBindingSpecPlan{
		Verdict: ProtocolObservationClean, Code: code, PlanHash: protocolBindingHex(value.PlanHash),
		Validation: value.Validation,
		Operation:  operation, WorkspaceID: value.WorkspaceID,
		SpecID: value.ID, Generation: value.Generation, SpecHash: protocolBindingHex(value.SpecHash),
		MappingHash: protocolBindingHex(value.MappingHash), LossesHash: protocolBindingHex(value.LossesHash),
	}
}

func classifyProtocolBindingStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidProtocolBinding), errors.Is(err, ErrProtocolBindingConflict),
		errors.Is(err, ErrProtocolBindingNotFound), errors.Is(err, ErrProtocolBindingUnknown):
		return err
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrUnknownEntity):
		return protocolBindingNotFound("not_found")
	case errors.Is(err, store.ErrConflict):
		return protocolBindingConflict("state_conflict")
	case errors.Is(err, store.ErrWorkspaceConfinement), errors.Is(err, store.ErrWorkspaceLineageRequired):
		return protocolBindingNotFound("not_found")
	default:
		return protocolBindingUnknown("store_unavailable", err)
	}
}

func requireProtocolHash(value []byte, name string) error {
	if len(value) != sha256.Size {
		return fmt.Errorf("%w: invalid %s", ErrInvalidProtocolBinding, name)
	}
	return nil
}
