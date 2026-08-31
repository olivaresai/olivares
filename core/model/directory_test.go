// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

const (
	directoryTestTenantA TenantID = "018f0000-0000-7000-8000-000000000001"
	directoryTestTenantB TenantID = "018f0000-0000-7000-8000-000000000002"
	directoryTestID      ID       = "018f0000-0000-7000-8000-000000000101"
	directoryTestSource  ID       = "018f0000-0000-7000-8000-000000000102"
	directoryTestEvent   ID       = "018f0000-0000-7000-8000-000000000103"
	directoryTestSpace   ID       = "018f0000-0000-7000-8000-000000000104"
)

func TestDirectoryEpochEvidenceCanonicalOrderAndLookup(t *testing.T) {
	evidence, err := NewDirectoryEpochEvidence(map[TenantID]int64{
		directoryTestTenantB: 7,
		directoryTestTenantA: 3,
	})
	if err != nil {
		t.Fatalf("canonicalize epoch evidence: %v", err)
	}
	if len(evidence) != 2 || evidence[0].TenantID != directoryTestTenantA ||
		evidence[1].TenantID != directoryTestTenantB {
		t.Fatalf("epoch evidence order = %+v, want tenant A then tenant B", evidence)
	}
	if got, ok := evidence.EpochFor(directoryTestTenantB); !ok || got != 7 {
		t.Fatalf("tenant B epoch = %d, %t; want 7, true", got, ok)
	}
	if got, ok := evidence.EpochFor("018f0000-0000-7000-8000-000000000009"); ok || got != 0 {
		t.Fatalf("absent tenant epoch = %d, %t; want 0, false", got, ok)
	}

	for name, bad := range map[string]DirectoryEpochEvidence{
		"unsorted": {
			{TenantID: directoryTestTenantB, Epoch: 7},
			{TenantID: directoryTestTenantA, Epoch: 3},
		},
		"duplicate": {
			{TenantID: directoryTestTenantA, Epoch: 3},
			{TenantID: directoryTestTenantA, Epoch: 4},
		},
		"zero epoch":    {{TenantID: directoryTestTenantA, Epoch: 0}},
		"system tenant": {{TenantID: SystemTenantID, Epoch: 1}},
		"non-v7 tenant": {{TenantID: "123e4567-e89b-12d3-a456-426614174000", Epoch: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := bad.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDirectoryEvidence", err)
			}
		})
	}
}

func TestDirectoryEpochValidationDeniesInvalidShape(t *testing.T) {
	valid := DirectoryEpoch{BaseFields: BaseFields{
		ID: ID(directoryTestTenantA), TenantID: directoryTestTenantA, Version: 1,
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid epoch: %v", err)
	}

	tests := map[string]DirectoryEpoch{
		"system": {BaseFields: BaseFields{
			ID: ID(SystemTenantID), TenantID: SystemTenantID, Version: 1,
		}},
		"id mismatch": {BaseFields: BaseFields{
			ID: directoryTestID, TenantID: directoryTestTenantA, Version: 1,
		}},
		"zero version": {BaseFields: BaseFields{
			ID: ID(directoryTestTenantA), TenantID: directoryTestTenantA,
		}},
	}
	for name, epoch := range tests {
		t.Run(name, func(t *testing.T) {
			if err := epoch.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDirectoryEvidence", err)
			}
		})
	}
}

func TestTombstoneValidationBindsClosedCauseAndAuditAnchor(t *testing.T) {
	now := NewTimestamp(time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	hash := bytes.Repeat([]byte{0x5a}, 32)
	epochs, err := NewDirectoryEpochEvidence(map[TenantID]int64{directoryTestTenantA: 4})
	if err != nil {
		t.Fatal(err)
	}

	user := UserTombstone{
		BaseFields: BaseFields{
			ID: directoryTestID, TenantID: SystemTenantID, Version: 1,
		},
		PrincipalKind:   DirectoryPrincipalUser,
		PrincipalRef:    directoryTestSource,
		SourceKind:      "core.user",
		SourceID:        directoryTestSource,
		ResultingEpochs: epochs,
		Cause:           DirectoryCauseUserErased,
		Actor:           "user:operator",
		RetiredAt:       now,
		AuditAnchor: RetirementAuditAnchor{
			EventID: directoryTestEvent, Seq: 19, Hash: hash,
			Action: AuditActionUserRetire, TargetKind: UserTombstoneKind,
			TargetID: directoryTestID,
		},
	}
	if err := user.Validate(); err != nil {
		t.Fatalf("valid user tombstone: %v", err)
	}

	directory := DirectoryTombstone{
		BaseFields: BaseFields{
			ID: directoryTestID, TenantID: directoryTestTenantA, Version: 1,
		},
		PrincipalKind:  DirectoryPrincipalAgent,
		PrincipalRef:   directoryTestSource,
		SourceKind:     "core.agent",
		SourceID:       directoryTestID,
		WorkspaceRef:   directoryTestSpace,
		ResultingEpoch: 8,
		Cause:          DirectoryCauseAgentRetired,
		Actor:          "system",
		RetiredAt:      now,
		AuditAnchor: RetirementAuditAnchor{
			EventID: directoryTestEvent, Seq: 20, Hash: hash,
			Action:     AuditActionDirectoryPrincipalRetire,
			TargetKind: DirectoryTombstoneKind, TargetID: directoryTestID,
		},
	}
	if err := directory.Validate(); err != nil {
		t.Fatalf("valid directory tombstone: %v", err)
	}

	badCause := directory
	badCause.Cause = DirectoryCauseIdentityRetired
	if err := badCause.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("wrong closed cause error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	badAnchor := user
	badAnchor.AuditAnchor.Hash = hash[:31]
	if err := badAnchor.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("short audit hash error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	badTarget := directory
	badTarget.AuditAnchor.TargetID = directoryTestSource
	if err := badTarget.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("rebound audit target error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	badWorkspace := directory
	badWorkspace.WorkspaceRef = "123e4567-e89b-12d3-a456-426614174000"
	if err := badWorkspace.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("non-v7 workspace error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	badWorkspace.WorkspaceRef = ID(nilUUID)
	if err := badWorkspace.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("all-zero workspace error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	badDirectoryVersion := directory
	badDirectoryVersion.Version = 2
	if err := badDirectoryVersion.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("directory version error = %v, want ErrInvalidDirectoryEvidence", err)
	}
	badUserVersion := user
	badUserVersion.Version = 0
	if err := badUserVersion.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
		t.Fatalf("user version error = %v, want ErrInvalidDirectoryEvidence", err)
	}

	for name, mutate := range map[string]func(*DirectoryTombstone){
		"empty actor":  func(v *DirectoryTombstone) { v.Actor = "" },
		"padded actor": func(v *DirectoryTombstone) { v.Actor = " system" },
		"zero DB time": func(v *DirectoryTombstone) { v.RetiredAt = Timestamp{} },
		"zero audit sequence": func(v *DirectoryTombstone) {
			v.AuditAnchor.Seq = 0
		},
		"wrong audit action": func(v *DirectoryTombstone) {
			v.AuditAnchor.Action = AuditActionUserRetire
		},
		"non-v7 audit event": func(v *DirectoryTombstone) {
			v.AuditAnchor.EventID = "123e4567-e89b-12d3-a456-426614174000"
		},
		"non-v7 source": func(v *DirectoryTombstone) {
			v.SourceID = "123e4567-e89b-12d3-a456-426614174000"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := directory
			mutate(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrInvalidDirectoryEvidence) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDirectoryEvidence", err)
			}
		})
	}
}

func TestDirectoryRetirementVocabularyIsClosed(t *testing.T) {
	for _, cause := range []DirectoryRetirementCause{
		DirectoryCauseUserErased,
		DirectoryCauseIdentityRetired,
		DirectoryCauseAgentRetired,
	} {
		if !cause.Valid() {
			t.Errorf("documented cause %q is not valid", cause)
		}
	}
	for _, cause := range []DirectoryRetirementCause{
		"deactivated", "provider_failed", "membership_lost", "",
	} {
		if cause.Valid() {
			t.Errorf("reversible/free-form cause %q was accepted", cause)
		}
	}
}
