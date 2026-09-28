// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"encoding/json"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// directoryWorkspaceNone is the one durable, non-NULL encoding of an absent
// workspace dimension. model.ID deliberately treats both the Go zero value and
// the nil UUID as unset; normalising both here prevents two SQL keys from
// representing the same canonical recipient and bypassing the unique index.
const directoryWorkspaceNone = "00000000-0000-0000-0000-000000000000"

// directoryDescriptors are core v7's three directory evidence relations. The
// epoch is mutable because its BaseFields.Version is the fencing value. The two
// tombstones are append-only, retained evidence and deliberately not Audited by
// generic CRUD: the engine-owned retirement operation appends and persists its
// explicit audit anchor in the same transaction.
func directoryDescriptors() []model.EntityDescriptor {
	return []model.EntityDescriptor{
		directoryEpochDescriptor,
		directoryTombstoneDescriptor,
		userTombstoneDescriptor,
	}
}

var directoryEpochDescriptor = model.EntityDescriptor{
	Kind:                   model.DirectoryEpochKind,
	Table:                  "core_directory_epoch",
	AuthorizationFact:      true,
	AuthorizationLockOrder: 5,
	Indexes: []model.IndexSpec{{
		Name:    "core_directory_epoch_tenant_uniq",
		Columns: []string{"tenant_id"},
		Unique:  true,
	}},
	Checks: []string{
		"id = tenant_id",
		"version >= 1",
		fmt.Sprintf("tenant_id <> '%s'", model.SystemTenantID),
	},
}

var directoryEpochCodec = model.Codec[model.DirectoryEpoch]{
	Base: func(e *model.DirectoryEpoch) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.DirectoryEpoch) (model.Record, error) {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		return model.Record{}, nil
	},
	Decode: func(b model.BaseFields, _ model.Record) (model.DirectoryEpoch, error) {
		e := model.DirectoryEpoch{BaseFields: b}
		if err := e.Validate(); err != nil {
			return model.DirectoryEpoch{}, err
		}
		return e, nil
	},
}

// pdeclNoneRetireAudit* are why no reader resolves a tombstone's audit anchor
// to a principal: the validator compares it with the audit event appended in
// the same transaction. pdeclRetireActor is the audit actor that retired the
// principal ("user:<id>", "agent:<id>", "system", ...), recorded as evidence.
var (
	pdeclNoneRetireAuditEvent  = model.None("the id of the audit event appended with the tombstone: core/model/directory.go:200")
	pdeclNoneRetireAuditHash   = model.None("the 32-byte audit chain hash of that event: core/model/directory.go:206")
	pdeclNoneRetireAuditAction = model.None("the one audit action the tombstone kind accepts: core/model/directory.go:209")
	pdeclNoneRetireAuditTarget = model.None("the audit target, which must be the tombstone itself: core/model/directory.go:213")
	pdeclRetireActor           = model.Ref(model.EncodeUserRef, model.ClassEvidence)
)

var directoryTombstoneDescriptor = model.EntityDescriptor{
	Kind:               model.DirectoryTombstoneKind,
	Table:              "core_directory_tombstone",
	AppendOnly:         true,
	RetainOnTenantDrop: true,
	Fields: []model.FieldSpec{
		pdecl(field("principal_kind", model.KindText, false),
			model.None("a retired principal kind the validator limits to identity or agent: core/model/directory.go:312")),
		pdecl(field("principal_ref", model.KindUUID, false),
			model.None("the retired roster identity or agent row, never an account: core/model/directory.go:312, core/model/directory.go:317")),
		pdecl(field("source_kind", model.KindText, false),
			model.None("the source entity kind, core.identity or core.agent: core/model/directory.go:320, core/model/directory.go:326")),
		pdecl(field("source_id", model.KindUUID, false),
			model.None("the retired identity or agent source row: core/model/directory.go:330, core/model/directory.go:333")),
		// The nil UUID is the canonical non-NULL sentinel when workspace does not
		// participate in the principal identity.
		pdecl(field("workspace_ref", model.KindUUID, false),
			model.None("the workspace the retired recipient was bound in, or the nil sentinel: core/model/directory.go:340")),
		field("resulting_epoch", model.KindInt, false),
		pdecl(field("cause", model.KindText, false),
			model.None("a retirement cause from the closed vocabulary matched to the principal kind: core/model/directory.go:349")),
		pdecl(field("actor", model.KindText, false), pdeclRetireActor),
		field("retired_at", model.KindTimestamp, false),
		pdecl(field("audit_event_id", model.KindUUID, false), pdeclNoneRetireAuditEvent),
		field("audit_seq", model.KindInt, false),
		pdecl(field("audit_hash", model.KindBytes, false), pdeclNoneRetireAuditHash),
		pdecl(field("audit_action", model.KindText, false), pdeclNoneRetireAuditAction),
		pdecl(field("audit_target_kind", model.KindText, false), pdeclNoneRetireAuditTarget),
		pdecl(field("audit_target_id", model.KindUUID, false), pdeclNoneRetireAuditTarget),
	},
	Indexes: []model.IndexSpec{
		{
			Name: "core_directory_tombstone_principal_uniq",
			Columns: []string{
				"tenant_id", "principal_kind", "principal_ref", "workspace_ref",
			},
			Unique: true,
		},
		{
			Name: "core_directory_tombstone_source_idx",
			Columns: []string{
				"tenant_id", "source_kind", "source_id",
			},
		},
	},
	Checks: []string{
		fmt.Sprintf("tenant_id <> '%s'", model.SystemTenantID),
		"version = 1",
		"updated_at = created_at",
		fmt.Sprintf("principal_kind IN ('%s','%s')",
			model.DirectoryPrincipalIdentity, model.DirectoryPrincipalAgent),
		fmt.Sprintf("cause IN ('%s','%s')",
			model.DirectoryCauseIdentityRetired, model.DirectoryCauseAgentRetired),
		fmt.Sprintf("((principal_kind = '%s' AND source_kind = 'core.identity' AND cause = '%s') OR "+
			"(principal_kind = '%s' AND source_kind = 'core.agent' AND cause = '%s'))",
			model.DirectoryPrincipalIdentity, model.DirectoryCauseIdentityRetired,
			model.DirectoryPrincipalAgent, model.DirectoryCauseAgentRetired),
		"resulting_epoch >= 1",
		"audit_seq >= 1",
		"length(audit_hash) = 32",
		fmt.Sprintf("audit_action = '%s'", model.AuditActionDirectoryPrincipalRetire),
		fmt.Sprintf("audit_target_kind = '%s'", model.DirectoryTombstoneKind),
		"audit_target_id = id",
	},
}

var directoryTombstoneCodec = model.Codec[model.DirectoryTombstone]{
	Base: func(t *model.DirectoryTombstone) *model.BaseFields { return &t.BaseFields },
	Encode: func(t model.DirectoryTombstone) (model.Record, error) {
		if err := t.Validate(); err != nil {
			return nil, err
		}
		return model.Record{
			"principal_kind":    string(t.PrincipalKind),
			"principal_ref":     t.PrincipalRef.String(),
			"source_kind":       string(t.SourceKind),
			"source_id":         t.SourceID.String(),
			"workspace_ref":     encodeDirectoryWorkspaceRef(t.WorkspaceRef),
			"resulting_epoch":   t.ResultingEpoch,
			"cause":             string(t.Cause),
			"actor":             t.Actor,
			"retired_at":        encTS(t.RetiredAt),
			"audit_event_id":    t.AuditAnchor.EventID.String(),
			"audit_seq":         t.AuditAnchor.Seq,
			"audit_hash":        append([]byte(nil), t.AuditAnchor.Hash...),
			"audit_action":      t.AuditAnchor.Action,
			"audit_target_kind": string(t.AuditAnchor.TargetKind),
			"audit_target_id":   t.AuditAnchor.TargetID.String(),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.DirectoryTombstone, error) {
		retiredAt, err := decTS(r, "retired_at")
		if err != nil {
			return model.DirectoryTombstone{}, err
		}
		t := model.DirectoryTombstone{
			BaseFields:     b,
			PrincipalKind:  model.DirectoryPrincipalKind(r.String("principal_kind")),
			PrincipalRef:   decID(r, "principal_ref"),
			SourceKind:     model.Kind(r.String("source_kind")),
			SourceID:       decID(r, "source_id"),
			WorkspaceRef:   decodeDirectoryWorkspaceRef(r),
			ResultingEpoch: r.Int("resulting_epoch"),
			Cause:          model.DirectoryRetirementCause(r.String("cause")),
			Actor:          r.String("actor"),
			RetiredAt:      retiredAt,
			AuditAnchor: model.RetirementAuditAnchor{
				EventID:    decID(r, "audit_event_id"),
				Seq:        r.Int("audit_seq"),
				Hash:       append([]byte(nil), r.Bytes("audit_hash")...),
				Action:     r.String("audit_action"),
				TargetKind: model.Kind(r.String("audit_target_kind")),
				TargetID:   decID(r, "audit_target_id"),
			},
		}
		if b.Version != 1 {
			return model.DirectoryTombstone{}, fmt.Errorf(
				"%w: directory tombstone version must be one",
				model.ErrInvalidDirectoryEvidence,
			)
		}
		if err := t.Validate(); err != nil {
			return model.DirectoryTombstone{}, err
		}
		return t, nil
	},
}

var userTombstoneDescriptor = model.EntityDescriptor{
	Kind:               model.UserTombstoneKind,
	Table:              "core_user_tombstone",
	AppendOnly:         true,
	RetainOnTenantDrop: true,
	Fields: []model.FieldSpec{
		pdecl(field("principal_kind", model.KindText, false),
			model.None("the principal kind, which the validator fixes to user: core/model/directory.go:250")),
		// principal_ref and source_id both hold the erased account's id: the
		// tombstone records the retirement and grants nothing.
		pdecl(field("principal_ref", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassEvidence)),
		pdecl(field("source_kind", model.KindText, false),
			model.None("the source entity kind, fixed to core.user: core/model/directory.go:257")),
		pdecl(field("source_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassEvidence)),
		pdecl(field("resulting_epochs", model.KindJSON, false), model.Nested(map[string]int64(nil), model.ClassEvidence,
			model.Leaf("{key}", model.None("a tenant id keyed to its resulting epoch: core/model/directory.go:146")))),
		pdecl(field("cause", model.KindText, false),
			model.None("a retirement cause the validator fixes to user_erased: core/model/directory.go:268")),
		pdecl(field("actor", model.KindText, false), pdeclRetireActor),
		field("retired_at", model.KindTimestamp, false),
		pdecl(field("audit_event_id", model.KindUUID, false), pdeclNoneRetireAuditEvent),
		field("audit_seq", model.KindInt, false),
		pdecl(field("audit_hash", model.KindBytes, false), pdeclNoneRetireAuditHash),
		pdecl(field("audit_action", model.KindText, false), pdeclNoneRetireAuditAction),
		pdecl(field("audit_target_kind", model.KindText, false), pdeclNoneRetireAuditTarget),
		pdecl(field("audit_target_id", model.KindUUID, false), pdeclNoneRetireAuditTarget),
	},
	Indexes: []model.IndexSpec{
		{
			Name: "core_user_tombstone_principal_uniq",
			Columns: []string{
				"tenant_id", "principal_kind", "principal_ref",
			},
			Unique: true,
		},
		{
			Name: "core_user_tombstone_source_idx",
			Columns: []string{
				"tenant_id", "source_kind", "source_id",
			},
		},
	},
	Checks: []string{
		fmt.Sprintf("tenant_id = '%s'", model.SystemTenantID),
		"version = 1",
		"updated_at = created_at",
		fmt.Sprintf("principal_kind = '%s'", model.DirectoryPrincipalUser),
		"source_kind = 'core.user'",
		"source_id = principal_ref",
		fmt.Sprintf("cause = '%s'", model.DirectoryCauseUserErased),
		"audit_seq >= 1",
		"length(audit_hash) = 32",
		fmt.Sprintf("audit_action = '%s'", model.AuditActionUserRetire),
		fmt.Sprintf("audit_target_kind = '%s'", model.UserTombstoneKind),
		"audit_target_id = id",
	},
}

var userTombstoneCodec = model.Codec[model.UserTombstone]{
	Base: func(t *model.UserTombstone) *model.BaseFields { return &t.BaseFields },
	Encode: func(t model.UserTombstone) (model.Record, error) {
		if err := t.Validate(); err != nil {
			return nil, err
		}
		epochs, err := encodeDirectoryEpochEvidence(t.ResultingEpochs)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"principal_kind":    string(t.PrincipalKind),
			"principal_ref":     t.PrincipalRef.String(),
			"source_kind":       string(t.SourceKind),
			"source_id":         t.SourceID.String(),
			"resulting_epochs":  epochs,
			"cause":             string(t.Cause),
			"actor":             t.Actor,
			"retired_at":        encTS(t.RetiredAt),
			"audit_event_id":    t.AuditAnchor.EventID.String(),
			"audit_seq":         t.AuditAnchor.Seq,
			"audit_hash":        append([]byte(nil), t.AuditAnchor.Hash...),
			"audit_action":      t.AuditAnchor.Action,
			"audit_target_kind": string(t.AuditAnchor.TargetKind),
			"audit_target_id":   t.AuditAnchor.TargetID.String(),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.UserTombstone, error) {
		epochs, err := decodeDirectoryEpochEvidence(r.String("resulting_epochs"))
		if err != nil {
			return model.UserTombstone{}, err
		}
		retiredAt, err := decTS(r, "retired_at")
		if err != nil {
			return model.UserTombstone{}, err
		}
		t := model.UserTombstone{
			BaseFields:      b,
			PrincipalKind:   model.DirectoryPrincipalKind(r.String("principal_kind")),
			PrincipalRef:    decID(r, "principal_ref"),
			SourceKind:      model.Kind(r.String("source_kind")),
			SourceID:        decID(r, "source_id"),
			ResultingEpochs: epochs,
			Cause:           model.DirectoryRetirementCause(r.String("cause")),
			Actor:           r.String("actor"),
			RetiredAt:       retiredAt,
			AuditAnchor: model.RetirementAuditAnchor{
				EventID:    decID(r, "audit_event_id"),
				Seq:        r.Int("audit_seq"),
				Hash:       append([]byte(nil), r.Bytes("audit_hash")...),
				Action:     r.String("audit_action"),
				TargetKind: model.Kind(r.String("audit_target_kind")),
				TargetID:   decID(r, "audit_target_id"),
			},
		}
		if b.Version != 1 {
			return model.UserTombstone{}, fmt.Errorf(
				"%w: user tombstone version must be one",
				model.ErrInvalidDirectoryEvidence,
			)
		}
		if err := t.Validate(); err != nil {
			return model.UserTombstone{}, err
		}
		return t, nil
	},
}

func encodeDirectoryWorkspaceRef(id model.ID) string {
	if id.IsZero() {
		return directoryWorkspaceNone
	}
	return id.String()
}

func decodeDirectoryWorkspaceRef(r model.Record) model.ID {
	if r.String("workspace_ref") == directoryWorkspaceNone {
		return ""
	}
	return decID(r, "workspace_ref")
}

// encodeDirectoryEpochEvidence renders the ordered in-memory evidence as one
// canonical JSON object. encoding/json sorts string keys, so the bytes are
// identical across insertion order and both SQL engines.
func encodeDirectoryEpochEvidence(e model.DirectoryEpochEvidence) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	m := make(map[string]int64, len(e))
	for _, entry := range e {
		m[entry.TenantID.String()] = entry.Epoch
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeDirectoryEpochEvidence rejects merely equivalent JSON: whitespace,
// duplicate/out-of-order keys and non-canonical numeric forms would otherwise
// make one immutable map admit several durable byte representations.
func decodeDirectoryEpochEvidence(raw string) (model.DirectoryEpochEvidence, error) {
	var m map[string]int64
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("%w: decode resulting epochs: %v",
			model.ErrInvalidDirectoryEvidence, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%w: resulting epochs must be a JSON object",
			model.ErrInvalidDirectoryEvidence)
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if raw != string(canonical) {
		return nil, fmt.Errorf("%w: resulting epochs JSON is not canonical",
			model.ErrInvalidDirectoryEvidence)
	}
	typed := make(map[model.TenantID]int64, len(m))
	for tenant, epoch := range m {
		typed[model.TenantID(tenant)] = epoch
	}
	return model.NewDirectoryEpochEvidence(typed)
}
