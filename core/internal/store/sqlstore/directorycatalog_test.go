// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const (
	directoryCatalogTenantA model.TenantID = "018f1000-0000-7000-8000-000000000001"
	directoryCatalogTenantB model.TenantID = "018f1000-0000-7000-8000-000000000002"
	directoryCatalogID      model.ID       = "018f1000-0000-7000-8000-000000000101"
	directoryCatalogSource  model.ID       = "018f1000-0000-7000-8000-000000000102"
	directoryCatalogEvent   model.ID       = "018f1000-0000-7000-8000-000000000103"
)

func TestDirectoryDescriptorShapesAreExact(t *testing.T) {
	if got, want := directoryEpochDescriptor.Kind, model.DirectoryEpochKind; got != want {
		t.Errorf("epoch kind = %q, want %q", got, want)
	}
	if got, want := directoryEpochDescriptor.Table, "core_directory_epoch"; got != want {
		t.Errorf("epoch table = %q, want %q", got, want)
	}
	if len(directoryEpochDescriptor.Fields) != 0 {
		t.Errorf("epoch fields = %+v, want no entity fields", directoryEpochDescriptor.Fields)
	}
	if !directoryEpochDescriptor.AuthorizationFact ||
		directoryEpochDescriptor.AuthorizationLockOrder != 5 {
		t.Errorf("epoch authorization declaration = fact:%t order:%d, want true/5",
			directoryEpochDescriptor.AuthorizationFact,
			directoryEpochDescriptor.AuthorizationLockOrder)
	}
	if !allowedAuthorizationFactKind(model.DirectoryEpochKind) {
		t.Error("core.directory_epoch is absent from the closed authorization-fact allowlist")
	}
	if got, want := directoryEpochDescriptor.Indexes, []model.IndexSpec{{
		Name: "core_directory_epoch_tenant_uniq", Columns: []string{"tenant_id"}, Unique: true,
	}}; !reflect.DeepEqual(got, want) {
		t.Errorf("epoch indexes = %+v, want %+v", got, want)
	}
	if got, want := directoryEpochDescriptor.Checks, []string{
		"id = tenant_id",
		"version >= 1",
		"tenant_id <> 'ffffffff-ffff-ffff-ffff-ffffffffffff'",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("epoch checks = %v, want %v", got, want)
	}

	assertTombstone := func(
		t *testing.T,
		d model.EntityDescriptor,
		wantKind model.Kind,
		wantTable string,
		wantFields []model.FieldSpec,
		wantUnique []string,
	) {
		t.Helper()
		if d.Kind != wantKind || d.Table != wantTable {
			t.Errorf("descriptor identity = %q/%q, want %q/%q",
				d.Kind, d.Table, wantKind, wantTable)
		}
		if !d.AppendOnly || !d.RetainOnTenantDrop || d.Audited {
			t.Errorf("descriptor lifecycle = append:%t retain:%t audited:%t, want true/true/false",
				d.AppendOnly, d.RetainOnTenantDrop, d.Audited)
		}
		if !reflect.DeepEqual(d.Fields, wantFields) {
			t.Errorf("%s fields = %+v, want %+v", d.Kind, d.Fields, wantFields)
		}
		if len(d.Indexes) != 2 || !d.Indexes[0].Unique || d.Indexes[1].Unique {
			t.Fatalf("%s index uniqueness = %+v, want principal unique and source non-unique",
				d.Kind, d.Indexes)
		}
		if got := d.Indexes[0].Columns; !reflect.DeepEqual(got, wantUnique) {
			t.Errorf("%s principal uniqueness = %v, want %v", d.Kind, got, wantUnique)
		}
		if got, want := d.Indexes[1].Columns,
			[]string{"tenant_id", "source_kind", "source_id"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s source index = %v, want non-unique %v", d.Kind, got, want)
		}
	}

	commonTail := []model.FieldSpec{
		field("cause", model.KindText, false),
		field("actor", model.KindText, false),
		field("retired_at", model.KindTimestamp, false),
		field("audit_event_id", model.KindUUID, false),
		field("audit_seq", model.KindInt, false),
		field("audit_hash", model.KindBytes, false),
		field("audit_action", model.KindText, false),
		field("audit_target_kind", model.KindText, false),
		field("audit_target_id", model.KindUUID, false),
	}
	directoryFields := []model.FieldSpec{
		field("principal_kind", model.KindText, false),
		field("principal_ref", model.KindUUID, false),
		field("source_kind", model.KindText, false),
		field("source_id", model.KindUUID, false),
		field("workspace_ref", model.KindUUID, false),
		field("resulting_epoch", model.KindInt, false),
	}
	directoryFields = append(directoryFields, commonTail...)
	assertTombstone(
		t, directoryTombstoneDescriptor, model.DirectoryTombstoneKind,
		"core_directory_tombstone", directoryFields,
		[]string{"tenant_id", "principal_kind", "principal_ref", "workspace_ref"},
	)
	if got, want := directoryTombstoneDescriptor.Checks, []string{
		"tenant_id <> 'ffffffff-ffff-ffff-ffff-ffffffffffff'",
		"version = 1",
		"updated_at = created_at",
		"principal_kind IN ('identity','agent')",
		"cause IN ('identity_retired','agent_retired')",
		"((principal_kind = 'identity' AND source_kind = 'core.identity' AND cause = 'identity_retired') OR " +
			"(principal_kind = 'agent' AND source_kind = 'core.agent' AND cause = 'agent_retired'))",
		"resulting_epoch >= 1",
		"audit_seq >= 1",
		"length(audit_hash) = 32",
		"audit_action = 'directory_principal.retire'",
		"audit_target_kind = 'core.directory_tombstone'",
		"audit_target_id = id",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("directory tombstone checks = %v, want %v", got, want)
	}
	userFields := []model.FieldSpec{
		field("principal_kind", model.KindText, false),
		field("principal_ref", model.KindUUID, false),
		field("source_kind", model.KindText, false),
		field("source_id", model.KindUUID, false),
		field("resulting_epochs", model.KindJSON, false),
	}
	userFields = append(userFields, commonTail...)
	assertTombstone(
		t, userTombstoneDescriptor, model.UserTombstoneKind,
		"core_user_tombstone", userFields,
		[]string{"tenant_id", "principal_kind", "principal_ref"},
	)
	if got, want := userTombstoneDescriptor.Checks, []string{
		"tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'",
		"version = 1",
		"updated_at = created_at",
		"principal_kind = 'user'",
		"source_kind = 'core.user'",
		"source_id = principal_ref",
		"cause = 'user_erased'",
		"audit_seq >= 1",
		"length(audit_hash) = 32",
		"audit_action = 'user.retire'",
		"audit_target_kind = 'core.user_tombstone'",
		"audit_target_id = id",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("user tombstone checks = %v, want %v", got, want)
	}

	wantRegistrations := map[model.Kind]int{
		model.DirectoryEpochKind:     1,
		model.DirectoryTombstoneKind: 1,
		model.UserTombstoneKind:      1,
	}
	for _, d := range coreDescriptors() {
		if _, ok := wantRegistrations[d.Kind]; ok {
			wantRegistrations[d.Kind]--
		}
	}
	for kind, remaining := range wantRegistrations {
		if remaining != 0 {
			t.Errorf("core catalog registration balance for %q = %d, want 0", kind, remaining)
		}
	}
}

func TestDirectoryTombstoneCodecPreservesEmptyWorkspaceSentinelAndAnchor(t *testing.T) {
	now := model.NewTimestamp(time.Date(2026, 8, 14, 1, 2, 3, 4, time.UTC))
	tombstone := model.DirectoryTombstone{
		BaseFields: model.BaseFields{
			ID: directoryCatalogID, TenantID: directoryCatalogTenantA, Version: 1,
		},
		PrincipalKind:  model.DirectoryPrincipalIdentity,
		PrincipalRef:   directoryCatalogSource,
		SourceKind:     "core.identity",
		SourceID:       directoryCatalogSource,
		ResultingEpoch: 9,
		Cause:          model.DirectoryCauseIdentityRetired,
		Actor:          "agent:directory-reconciler",
		RetiredAt:      now,
		AuditAnchor: model.RetirementAuditAnchor{
			EventID: directoryCatalogEvent, Seq: 21,
			Hash:       bytes.Repeat([]byte{0x41}, 32),
			Action:     model.AuditActionDirectoryPrincipalRetire,
			TargetKind: model.DirectoryTombstoneKind, TargetID: directoryCatalogID,
		},
	}
	rec, err := directoryTombstoneCodec.Encode(tombstone)
	if err != nil {
		t.Fatalf("encode directory tombstone: %v", err)
	}
	if got, ok := rec["workspace_ref"].(string); !ok || got != directoryWorkspaceNone {
		t.Fatalf("workspace_ref = %#v, want canonical non-NULL nil UUID", rec["workspace_ref"])
	}
	got, err := directoryTombstoneCodec.Decode(tombstone.BaseFields, rec)
	if err != nil {
		t.Fatalf("decode directory tombstone: %v", err)
	}
	if got.WorkspaceRef != "" || got.ResultingEpoch != 9 ||
		got.AuditAnchor.TargetID != tombstone.ID ||
		!bytes.Equal(got.AuditAnchor.Hash, tombstone.AuditAnchor.Hash) {
		t.Fatalf("directory tombstone round trip = %+v, want sentinel/epoch/anchor preserved", got)
	}
	for _, version := range []int64{0, 2} {
		badBase := tombstone.BaseFields
		badBase.Version = version
		if _, err := directoryTombstoneCodec.Decode(badBase, rec); !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
			t.Fatalf("directory tombstone version %d error = %v, want ErrInvalidDirectoryEvidence", version, err)
		}
	}

	// There is exactly one public zero spelling and one durable encoding. An
	// all-zero UUID cannot become a second uniqueness key for the same recipient.
	tombstone.WorkspaceRef = model.ID(directoryWorkspaceNone)
	if _, err = directoryTombstoneCodec.Encode(tombstone); !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
		t.Fatalf("all-zero public workspace error = %v, want ErrInvalidDirectoryEvidence", err)
	}

	tombstone.WorkspaceRef = ""
	rec, err = directoryTombstoneCodec.Encode(tombstone)
	if err != nil {
		t.Fatal(err)
	}
	rec["audit_target_id"] = directoryCatalogSource.String()
	_, err = directoryTombstoneCodec.Decode(tombstone.BaseFields, rec)
	if !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
		t.Fatalf("rebound audit target error = %v, want ErrInvalidDirectoryEvidence", err)
	}
}

func TestUserTombstoneCodecRequiresOneCanonicalEpochMap(t *testing.T) {
	now := model.NewTimestamp(time.Date(2026, 8, 14, 1, 2, 3, 4, time.UTC))
	epochs, err := model.NewDirectoryEpochEvidence(map[model.TenantID]int64{
		directoryCatalogTenantB: 7,
		directoryCatalogTenantA: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	tombstone := model.UserTombstone{
		BaseFields: model.BaseFields{
			ID: directoryCatalogID, TenantID: model.SystemTenantID, Version: 1,
		},
		PrincipalKind:   model.DirectoryPrincipalUser,
		PrincipalRef:    directoryCatalogSource,
		SourceKind:      "core.user",
		SourceID:        directoryCatalogSource,
		ResultingEpochs: epochs,
		Cause:           model.DirectoryCauseUserErased,
		Actor:           "user:operator",
		RetiredAt:       now,
		AuditAnchor: model.RetirementAuditAnchor{
			EventID: directoryCatalogEvent, Seq: 22,
			Hash: bytes.Repeat([]byte{0x42}, 32), Action: model.AuditActionUserRetire,
			TargetKind: model.UserTombstoneKind, TargetID: directoryCatalogID,
		},
	}
	rec, err := userTombstoneCodec.Encode(tombstone)
	if err != nil {
		t.Fatalf("encode user tombstone: %v", err)
	}
	wantJSON := `{"018f1000-0000-7000-8000-000000000001":3,"018f1000-0000-7000-8000-000000000002":7}`
	if got := rec.String("resulting_epochs"); got != wantJSON {
		t.Fatalf("resulting_epochs = %s, want canonical %s", got, wantJSON)
	}
	got, err := userTombstoneCodec.Decode(tombstone.BaseFields, rec)
	if err != nil {
		t.Fatalf("decode user tombstone: %v", err)
	}
	if epoch, ok := got.ResultingEpochs.EpochFor(directoryCatalogTenantB); !ok || epoch != 7 {
		t.Fatalf("decoded tenant B epoch = %d, %t; want 7, true", epoch, ok)
	}
	for _, version := range []int64{0, 2} {
		badBase := tombstone.BaseFields
		badBase.Version = version
		if _, err := userTombstoneCodec.Decode(badBase, rec); !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
			t.Fatalf("user tombstone version %d error = %v, want ErrInvalidDirectoryEvidence", version, err)
		}
	}

	for name, raw := range map[string]string{
		"whitespace": ` {"018f1000-0000-7000-8000-000000000001":3}`,
		"out of order": `{"018f1000-0000-7000-8000-000000000002":7,` +
			`"018f1000-0000-7000-8000-000000000001":3}`,
		"duplicate": `{"018f1000-0000-7000-8000-000000000001":3,` +
			`"018f1000-0000-7000-8000-000000000001":4}`,
		"zero epoch":     `{"018f1000-0000-7000-8000-000000000001":0}`,
		"overflow epoch": `{"018f1000-0000-7000-8000-000000000001":9223372036854775808}`,
		"fraction epoch": `{"018f1000-0000-7000-8000-000000000001":1.5}`,
		"exponent epoch": `{"018f1000-0000-7000-8000-000000000001":1e1}`,
		"system tenant":  `{"ffffffff-ffff-ffff-ffff-ffffffffffff":1}`,
		"null":           `null`,
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneRecord(rec)
			bad["resulting_epochs"] = raw
			_, err := userTombstoneCodec.Decode(tombstone.BaseFields, bad)
			if !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
				t.Fatalf("Decode() error = %v, want ErrInvalidDirectoryEvidence", err)
			}
		})
	}
}

func TestDirectoryEpochCodecRejectsZeroAndMismatchedFacts(t *testing.T) {
	base := model.BaseFields{
		ID: model.ID(directoryCatalogTenantA), TenantID: directoryCatalogTenantA, Version: 1,
	}
	if _, err := directoryEpochCodec.Decode(base, model.Record{}); err != nil {
		t.Fatalf("decode valid directory epoch: %v", err)
	}
	base.Version = 0
	_, err := directoryEpochCodec.Decode(base, model.Record{})
	if !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
		t.Fatalf("zero epoch error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	base.Version = 1
	base.ID = directoryCatalogID
	_, err = directoryEpochCodec.Decode(base, model.Record{})
	if !errors.Is(err, model.ErrInvalidDirectoryEvidence) {
		t.Fatalf("mismatched epoch id error = %v, want ErrInvalidDirectoryEvidence", err)
	}
}

func cloneRecord(in model.Record) model.Record {
	out := make(model.Record, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
