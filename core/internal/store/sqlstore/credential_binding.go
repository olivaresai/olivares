// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// credentialBindingDescriptor stores the durable binding of a server-selected
// subject, such as a workflow run, to one exact credential revision. Only
// core/auth writes it (core/auth/credential_binding.go). The row lives in the
// system tenant; target_tenant_id is the subject's business tenant. A subject
// holds at most one row per generation, which is what lets a succession have a
// single winner.
var credentialBindingDescriptor = model.EntityDescriptor{
	Kind:  "core.credential_binding",
	Table: "core_credential_bindings",
	Fields: []model.FieldSpec{
		pdecl(field("target_tenant_id", model.KindUUID, false),
			model.None("the subject's business tenant, compared with the resolving tenant: core/auth/credential_binding.go:749")),
		pdecl(field("subject_kind", model.KindText, false),
			model.None("a subject kind from a closed set, compared: core/auth/credential_binding.go:749")),
		pdecl(field("subject_ref", model.KindUUID, false),
			model.None("the subject's id, a workflow run, compared: core/auth/credential_binding.go:749")),
		field("subject_generation", model.KindInt, false),
		// The account every binding of the subject belongs to. It only ever
		// refuses a successor bound to another account.
		pdecl(field("subject_user_id", model.KindUUID, false), model.Ref(model.EncodeUserID, model.ClassRestrict)),
		pdecl(field("credential_kind", model.KindText, false),
			model.None("the closed credential kind user or token, switched on: core/auth/credential_binding.go:702")),
		pdecl(field("credential_id", model.KindUUID, false),
			model.None("the session or token row, reloaded as such when bound and at every resolution: core/auth/credential_binding.go:704, core/auth/credential_binding.go:716, core/auth/principal_evidence.go:232")),
		field("credential_version", model.KindInt, false),
		// The subject's authority ceiling, copied from its first binding into
		// every successor and sealed with the row.
		pdecl(field("ceiling_kind", model.KindText, false),
			model.None("the closed credential kind user or token of the first binding, compared: core/auth/credential_binding.go:221")),
		pdecl(field("ceiling_role", model.KindText, true), pdeclNoneAuthRole),
		pdecl(field("ceiling_workspace_id", model.KindUUID, true),
			model.None("the workspace the first binding's credential was confined to, compared: core/auth/credential_binding.go:221")),
		pdecl(field("ceiling_agent", model.KindText, true),
			model.None("the external id of an agent identity, never an account, compared: core/auth/credential_binding.go:221")),
		pdecl(field("superseded_by", model.KindUUID, true),
			model.None("the successor binding row, only tested for presence: core/auth/credential_binding.go:317, core/auth/credential_binding.go:180")),
		field("superseded_at", model.KindTimestamp, true),
		pdecl(field("authorized_by_actor", model.KindText, true), pdeclActorEvidence),
		pdecl(field("seal", model.KindBytes, false),
			model.None("SHA-256 over the row's immutable fields, only compared: core/auth/credential_binding.go:784, core/auth/credential_binding.go:754")),
	},
	Indexes: []model.IndexSpec{
		{Name: "core_credential_bindings_subject_generation_uniq", Columns: []string{
			"tenant_id", "target_tenant_id", "subject_kind", "subject_ref", "subject_generation",
		}, Unique: true},
	},
}

var credentialBindingCodec = model.Codec[model.CredentialBinding]{
	Base: func(b *model.CredentialBinding) *model.BaseFields { return &b.BaseFields },
	Encode: func(b model.CredentialBinding) (model.Record, error) {
		return model.Record{
			"target_tenant_id": encTenant(b.TargetTenantID),
			"subject_kind":     b.SubjectKind, "subject_ref": b.SubjectRef.String(),
			"subject_generation": b.SubjectGeneration, "subject_user_id": b.SubjectUserID.String(),
			"credential_kind": b.CredentialKind, "credential_id": b.CredentialID.String(),
			"credential_version": b.CredentialVersion,
			"ceiling_kind":       b.CeilingKind, "ceiling_role": encOptStr(b.CeilingRole),
			"ceiling_workspace_id": encOptID(b.CeilingWorkspaceID), "ceiling_agent": encOptStr(b.CeilingAgent),
			"superseded_by": encOptID(b.SupersededBy), "superseded_at": encOptTS(b.SupersededAt),
			"authorized_by_actor": encOptStr(b.AuthorizedByActor), "seal": encBytes(b.Seal),
		}, nil
	},
	Decode: func(base model.BaseFields, r model.Record) (model.CredentialBinding, error) {
		superseded, err := decOptTS(r, "superseded_at")
		if err != nil {
			return model.CredentialBinding{}, err
		}
		return model.CredentialBinding{
			BaseFields: base, TargetTenantID: decTenant(r, "target_tenant_id"),
			SubjectKind: r.String("subject_kind"), SubjectRef: decID(r, "subject_ref"),
			SubjectGeneration: r.Int("subject_generation"), SubjectUserID: decID(r, "subject_user_id"),
			CredentialKind: r.String("credential_kind"), CredentialID: decID(r, "credential_id"),
			CredentialVersion: r.Int("credential_version"), SupersededBy: decID(r, "superseded_by"),
			CeilingKind: r.String("ceiling_kind"), CeilingRole: r.String("ceiling_role"),
			CeilingWorkspaceID: decID(r, "ceiling_workspace_id"), CeilingAgent: r.String("ceiling_agent"),
			SupersededAt: superseded, AuthorizedByActor: r.String("authorized_by_actor"),
			Seal: r.Bytes("seal"),
		}, nil
	},
}

// credentialBindingRelation reports whether kind is the relation v15 adds
// whole: v2 never renders it, and the reconcile creates it.
func credentialBindingRelation(kind model.Kind) bool {
	return kind == credentialBindingDescriptor.Kind
}

// credentialBindingStore is the CONCRETE narrow surface returned by
// authScope.CredentialBindings. It does not embed store.Repository, so a core
// caller cannot type-assert it back to a repository and recover a generic
// Update or Delete.
type credentialBindingStore struct {
	g *genericRepo
	// directory is the auth transaction's directory writer. A supersession moves
	// the subject tenant's directory epoch through it (Supersede).
	directory *directoryWriteTracker
}

func (s credentialBindingStore) decode(rec model.Record) (model.CredentialBinding, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	return credentialBindingCodec.Decode(base, rec)
}

func (s credentialBindingStore) Get(ctx context.Context, id model.ID) (model.CredentialBinding, error) {
	rec, err := s.g.Get(ctx, id)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	return s.decode(rec)
}

func credentialBindingSubjectFilters(target model.TenantID, kind string, ref model.ID) []model.Filter {
	return []model.Filter{
		{Column: "target_tenant_id", Op: model.OpEq, Value: target.String()},
		{Column: "subject_kind", Op: model.OpEq, Value: kind},
		{Column: "subject_ref", Op: model.OpEq, Value: ref.String()},
	}
}

// Current reads the subject's unsuperseded rows through the subject prefix of
// the unique subject-generation index, never its whole history. It returns at
// most two rows, enough to report a broken custody invariant.
func (s credentialBindingStore) Current(
	ctx context.Context,
	target model.TenantID,
	kind string,
	ref model.ID,
) ([]model.CredentialBinding, error) {
	filters := append(credentialBindingSubjectFilters(target, kind, ref),
		model.Filter{Column: "superseded_by", Op: model.OpIsNull})
	recs, _, err := s.g.List(ctx, model.Query{Filters: filters, Limit: 2})
	if err != nil {
		return nil, err
	}
	out := make([]model.CredentialBinding, 0, len(recs))
	for _, rec := range recs {
		row, err := s.decode(rec)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// AtGeneration reads the one row the unique subject-generation key allows.
func (s credentialBindingStore) AtGeneration(
	ctx context.Context,
	target model.TenantID,
	kind string,
	ref model.ID,
	generation int64,
) (model.CredentialBinding, error) {
	filters := append(credentialBindingSubjectFilters(target, kind, ref),
		model.Filter{Column: "subject_generation", Op: model.OpEq, Value: generation})
	recs, _, err := s.g.List(ctx, model.Query{Filters: filters, Limit: 2})
	if err != nil {
		return model.CredentialBinding{}, err
	}
	if len(recs) == 0 {
		return model.CredentialBinding{}, store.ErrNotFound
	}
	return s.decode(recs[0])
}

func (s credentialBindingStore) Create(
	ctx context.Context,
	v model.CredentialBinding,
) (model.CredentialBinding, error) {
	rec, err := credentialBindingCodec.Encode(v)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	created, err := s.g.CreateWithID(ctx, v.ID, rec)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	return s.decode(created)
}

func (s credentialBindingStore) Supersede(
	ctx context.Context,
	current model.CredentialBinding,
	successor model.ID,
	at model.Timestamp,
) (model.CredentialBinding, error) {
	stored, err := s.Get(ctx, current.ID)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	if stored.Version != current.Version || !stored.SupersededBy.IsZero() || stored.SupersededAt != nil ||
		successor.IsZero() || successor == current.ID || at.IsZero() {
		return model.CredentialBinding{}, store.ErrConflict
	}
	// The supersession moves the subject tenant's directory epoch in this same
	// transaction. Every workflow effect's commit locks the epoch version its
	// authority was observed at (the transaction-consumed authority proof), so
	// an effect prepared under the superseded binding either committed before
	// this transaction or is refused at its own commit. No claim or clock is
	// consulted.
	if s.directory == nil {
		return model.CredentialBinding{}, fmt.Errorf("sqlstore: credential-binding supersession has no directory writer")
	}
	if err := s.directory.prepare(ctx, func() ([]model.TenantID, error) {
		return []model.TenantID{stored.TargetTenantID}, nil
	}); err != nil {
		return model.CredentialBinding{}, err
	}
	stored.SupersededBy, stored.SupersededAt = successor, &at
	rec, err := credentialBindingCodec.Encode(stored)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	rec[model.ColID] = stored.ID.String()
	rec[model.ColVersion] = stored.Version
	updated, err := s.g.Update(ctx, rec)
	if err != nil {
		return model.CredentialBinding{}, err
	}
	return s.decode(updated)
}
